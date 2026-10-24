package training

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"sync"
	"time"

	"github.com/gsoultan/mimir-server/internal/arch"
	"github.com/gsoultan/mimir-server/internal/memguard"
	"github.com/gsoultan/mimir-server/internal/safe"
	"github.com/gsoultan/mimir-server/internal/store"
)

// Defaults apply where a job leaves a setting at zero. Tables are usually
// small, so they take smaller steps more often and learn a bit faster.
var (
	Defaults      = store.Settings{Epochs: 5, BatchSize: 64, LearningRate: 1e-3, HoldOut: 0.2}
	TableDefaults = store.Settings{Epochs: 10, BatchSize: 32, LearningRate: 5e-3, HoldOut: 0.2}
	// PhotoDefaults suit a few hundred photos per answer: smaller batches
	// and a larger step than the digits need, so it settles in a few passes.
	PhotoDefaults = store.Settings{Epochs: 10, BatchSize: 32, LearningRate: 3e-3, HoldOut: 0.2}
)

// DefaultsFor returns the defaults for a dataset of kind k.
func DefaultsFor(k store.DatasetKind) store.Settings {
	switch k {
	case store.DatasetTable:
		return TableDefaults
	case store.DatasetPhotos, store.DatasetPhotoTable:
		return PhotoDefaults
	}
	return Defaults
}

// MaxTableBatch bounds the batch size SettingsFor picks for a large table.
const MaxTableBatch = 512

// SettingsFor returns the defaults for dataset d. A large table takes
// larger batches: a step costs nearly the same for 32 rows as for a few
// hundred, so an epoch of half a million rows runs about five times faster
// at 512 than at 32. It still takes at least a thousand steps an epoch,
// enough to learn as well.
func SettingsFor(d store.Dataset) store.Settings {
	s := DefaultsFor(d.Kind)
	if d.Kind == store.DatasetTable {
		for s.BatchSize < MaxTableBatch && int64(s.BatchSize)*2*1000 <= d.Rows {
			s.BatchSize *= 2
		}
	}
	return s
}

// ErrCancelled is the cause a job's context carries when a person stops it.
var ErrCancelled = errors.New("training: cancelled")

// Runner takes queued jobs and trains them, several at once.
type Runner struct {
	st  *store.Store
	log *slog.Logger

	mu      sync.Mutex
	running map[string]context.CancelCauseFunc
	active  int // jobs holding a slot; guarded by mu
	wake    chan struct{}
	freed   chan struct{} // a slot came free
	jobs    sync.WaitGroup
	// local lets one job at a time train in this process: the framework
	// runs one computation at a time anyway, so more would only add memory.
	local chan struct{}

	// Jobs is how many jobs run at once. Zero means one per training slot
	// of the connected workers, plus one when Local, and at least
	// DefaultJobs.
	Jobs int

	// OnDone is called after a job succeeds or fails, for webhooks and
	// alerts. It may be nil.
	OnDone func(ctx context.Context, j store.Job)
	// Local says this process trains jobs itself when no worker takes
	// them. It is off for a server that only coordinates workers.
	Local bool
	// TrainHere, when set, is asked alongside Local before each job, so
	// a setting can turn training here off while the server runs.
	TrainHere func() bool
	// Remote hands jobs to connected workers; nil means there are none.
	Remote Remote
	// Now tells the time, for keep-learning schedules; tests move it.
	Now func() time.Time
	// GroupWorkers, when set, is asked before each run that may go to any
	// worker: above 1, the run trains on up to that many workers together
	// when a GroupRemote has at least two free.
	GroupWorkers func() int
	// Admit, when set, is asked before a keep-learning pass fetches its
	// rows again, with a guess at the bytes the fetch takes; false puts
	// the pass off for a few minutes. Nil leaves it to the memory budget
	// from WatchMemory, and admits every fetch without one.
	Admit func(need int64) bool

	refreshing   sync.WaitGroup // keep-learning passes being prepared
	refreshSlots chan struct{}  // bounds them to RefreshesAtOnce

	// memory is the budget training here keeps within; nil means none.
	// See WatchMemory.
	memory *memguard.Guard
	// localJobs are the jobs training in this process now, lowMemory how
	// often each was stopped for memory and cooling when each was last,
	// so it isn't claimed again at once; all guarded by mu, as is
	// pausedUntil, before which nothing more starts training here.
	localJobs   map[string]bool
	lowMemory   map[string]int
	cooling     map[string]time.Time
	pausedUntil time.Time
	// StopAfter is how long memory may stay critical before the runs
	// training here are stopped and put back in the queue; until then
	// they keep going in smaller pieces (see runtime.ThrottleCompute).
	// Zero means DefaultStopAfter. Set it before WatchMemory.
	StopAfter time.Duration
	// fit trains a task in this process; tests replace it.
	fit func(ctx context.Context, t Task, progress Progress) (Result, error)
}

// ErrLowMemory is the cause of a run stopped, here or on a worker, to
// keep the process within its memory budget. The run goes back in the
// queue and resumes from its checkpoint.
var ErrLowMemory = memguard.ErrLowMemory

// DefaultStopAfter is how long memory may stay critical before training
// here stops, when Runner.StopAfter isn't set.
const DefaultStopAfter = time.Minute

// MaxLowMemoryStops is how many times a run may be stopped for memory
// before it fails: one that needs more than the machine can spare would
// otherwise start over forever.
const MaxLowMemoryStops = 3

// memoryPause is how long nothing new trains here after a run was
// stopped for memory, so serving gets the memory back first.
const memoryPause = time.Minute

// errHeldBack means a run could only train here and memory is short
// now; it waits in the queue.
var errHeldBack = errors.New("training: memory is short; waiting to train here")

// Remote is a pool of workers that can train jobs.
type Remote interface {
	// Train runs the task on a worker, reporting progress along the way.
	// ran is false when no worker could take it, and the job is untouched.
	// With a workerID only that worker is used.
	Train(ctx context.Context, jobID, workerID string, t Task, progress Progress) (res Result, worker string, ran bool, err error)
	// CanTrain reports whether some connected worker trains architecture
	// a; with a workerID, whether that worker does.
	CanTrain(a, workerID string) bool
}

// DefaultJobs is the fewest jobs that run at once when Jobs is zero.
const DefaultJobs = 2

// TrainSlots is what a Remote can also say: how many training tasks its
// connected workers take at once, to size the runner when Jobs is zero.
type TrainSlots interface {
	TrainSlots() int
}

// GroupRemote is what a Remote can also do: train one run on up to most
// workers together (see Group). ran is false when fewer than two are free,
// and the job is untouched; workers names those it ran on.
type GroupRemote interface {
	TrainGroup(ctx context.Context, jobID string, most int, t Task, progress Progress) (res Result, workers string, ran bool, err error)
}

// TrainingOnly is what a Remote can also say: whether a worker takes no
// predictions, so a model trained on it must run elsewhere.
type TrainingOnly interface {
	TrainingOnly(workerID string) bool
}

// errNoWorker is the failure when nobody can run a job.
var errNoWorker = errors.New("no worker is connected that can train this kind of model")

// ErrWorkerLost means the worker training a run went away; the run goes
// back in the queue for when one is there.
var ErrWorkerLost = errors.New("the worker disconnected")

// New returns a Runner over st that trains locally.
func New(st *store.Store, log *slog.Logger) *Runner {
	return &Runner{st: st, log: log, running: map[string]context.CancelCauseFunc{}, wake: make(chan struct{}, 1), freed: make(chan struct{}, 1), local: make(chan struct{}, 1), Local: true, Now: time.Now, refreshSlots: make(chan struct{}, RefreshesAtOnce),
		localJobs: map[string]bool{}, lowMemory: map[string]int{}, cooling: map[string]time.Time{}, fit: Fit}
}

// WatchMemory keeps training in this process within g's budget, so it
// never starves the predictions served here. A run starts here only while
// memory is OK. A run already training keeps going when memory runs short,
// in smaller pieces once it is critical; only when memory stays critical
// for StopAfter are the runs training here stopped and put back in the
// queue, to resume from their checkpoints once memory is free again. Runs
// on workers are left alone; each worker guards its own memory.
func (r *Runner) WatchMemory(g *memguard.Guard) {
	r.memory = g
	grace := r.StopAfter
	if grace <= 0 {
		grace = DefaultStopAfter
	}
	g.StaysCritical(grace, func() {
		r.mu.Lock()
		defer r.mu.Unlock()
		for id := range r.localJobs {
			if cancel := r.running[id]; cancel != nil {
				r.log.Warn("training: memory stayed critical; stopping the run here, which goes back in the queue", "job_id", id, "after", grace)
				cancel(ErrLowMemory)
			}
		}
	})
}

// localReady reports whether a run may start training here now.
func (r *Runner) localReady() bool {
	if !r.trainsHere() || !r.memory.Admit(0) {
		return false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return !time.Now().Before(r.pausedUntil)
}

// stoppedForMemory counts a stop of job id for memory, holds back
// training here for memoryPause, and returns how many stops it has had.
func (r *Runner) stoppedForMemory(id string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.lowMemory[id]++
	r.pausedUntil = time.Now().Add(memoryPause)
	r.cooling[id] = time.Now()
	return r.lowMemory[id]
}

// cooled returns the jobs stopped for memory within memoryPause, which
// stay in the queue until it has passed: claimed again at once, a run
// handed back by a worker short on memory would go round and round.
func (r *Runner) cooled() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	var ids []string
	for id, at := range r.cooling {
		if time.Since(at) < memoryPause {
			ids = append(ids, id)
		} else {
			delete(r.cooling, id)
		}
	}
	return ids
}

// Kick asks the runner to look at the queue now.
func (r *Runner) Kick() {
	select {
	case r.wake <- struct{}{}:
	default:
	}
}

// Cancel stops a running job and reports whether one was running.
func (r *Runner) Cancel(id string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	cancel, ok := r.running[id]
	if ok {
		cancel(ErrCancelled)
	}
	return ok
}

// Run works the queue until ctx ends, then waits for the jobs in flight.
// A job interrupted by shutdown goes back to the queue for the next start.
func (r *Runner) Run(ctx context.Context) {
	if err := r.st.ResetRunningJobs(ctx); err != nil {
		r.log.ErrorContext(ctx, "training: reset running jobs", "error", err)
	}
	tick := time.NewTicker(5 * time.Second)
	defer tick.Stop()
	defer r.jobs.Wait()
	defer r.refreshing.Wait()
	for {
		r.schedule(ctx)
		r.drain(ctx)
		select {
		case <-ctx.Done():
			return
		case <-r.wake:
		case <-tick.C:
		}
	}
}

// Tick queues due keep-learning passes, runs every queued job once and
// waits for them, for tests.
func (r *Runner) Tick(ctx context.Context) {
	r.schedule(ctx)
	r.refreshing.Wait()
	r.drain(ctx)
	r.jobs.Wait()
}

// limit is how many jobs may run at once right now.
func (r *Runner) limit() int {
	if r.Jobs > 0 {
		return r.Jobs
	}
	n := 0
	if s, ok := r.Remote.(TrainSlots); ok {
		n = s.TrainSlots()
	}
	if r.trainsHere() {
		n++
	}
	return max(n, DefaultJobs)
}

// trainsHere reports whether this process trains jobs itself right now.
func (r *Runner) trainsHere() bool {
	return r.Local && (r.TrainHere == nil || r.TrainHere())
}

// acquire waits for a free slot. It reports false when ctx ends first.
func (r *Runner) acquire(ctx context.Context) bool {
	for {
		r.mu.Lock()
		if r.active < r.limit() {
			r.active++
			r.mu.Unlock()
			return true
		}
		r.mu.Unlock()
		select {
		case <-ctx.Done():
			return false
		case <-r.freed:
		}
	}
}

// release gives back the slot of a job that ran and looks at the queue
// again, for jobs set aside while every worker was busy.
func (r *Runner) release() {
	r.unclaim()
	r.Kick()
}

// unclaim gives back a slot that started no job. It does not look at the
// queue again: drain took it because it found the queue empty or the
// store failing, and looking again at once would spin.
func (r *Runner) unclaim() {
	r.mu.Lock()
	r.active--
	r.mu.Unlock()
	select {
	case r.freed <- struct{}{}:
	default:
	}
}

// drain starts queued jobs until the queue is empty, each in its own
// goroutine once a slot is free. Claiming is atomic in the database, so
// two servers never take the same job.
func (r *Runner) drain(ctx context.Context) {
	// skip holds jobs set aside this round because nobody can run them
	// now, so one waiting for an offline worker doesn't hold up the rest.
	var skip []string
	for ctx.Err() == nil {
		if !r.acquire(ctx) {
			return
		}
		// Runs just handed back for memory are left alone too; the list
		// is read each time, as they come back while this loop runs.
		j, err := r.st.ClaimJob(ctx, append(r.cooled(), skip...)...)
		if errors.Is(err, store.ErrNotFound) {
			r.unclaim()
			return
		}
		if err != nil {
			r.unclaim()
			r.log.ErrorContext(ctx, "training: claim job", "error", err)
			return
		}
		pinned := j.RunOn != ""
		if (pinned || !r.localReady()) && (r.Remote == nil || !r.Remote.CanTrain(j.Architecture, j.RunOn)) {
			// Nobody can run it right now; leave it queued for when a
			// worker connects or comes free, or memory here does.
			r.st.SetJobStatus(ctx, j.ID, store.JobQueued, "", "")
			skip = append(skip, j.ID)
			r.mu.Lock()
			r.active--
			r.mu.Unlock()
			continue
		}
		r.jobs.Go(func() {
			defer r.release()
			// A run that panics fails alone; the runner and the server
			// carry on.
			if err := safe.Run(r.log, "training job "+j.ID, func() { r.run(ctx, j) }); err != nil {
				r.st.SetJobStatus(context.WithoutCancel(ctx), j.ID, store.JobFailed, "Training stopped with an internal error. It has been logged; try again, and report it if it keeps happening.", "")
			}
		})
	}
}

func (r *Runner) run(ctx context.Context, j store.Job) {
	jctx, cancel := context.WithCancelCause(ctx)
	r.mu.Lock()
	r.running[j.ID] = cancel
	r.mu.Unlock()
	defer func() {
		cancel(nil)
		r.mu.Lock()
		delete(r.running, j.ID)
		r.mu.Unlock()
	}()

	log := r.log.With("job_id", j.ID, "architecture", j.Architecture)
	log.InfoContext(ctx, "training: start", "epochs", j.Settings.Epochs)
	modelID, err := r.train(jctx, j)
	switch {
	case err == nil:
		r.mu.Lock()
		delete(r.lowMemory, j.ID)
		delete(r.cooling, j.ID)
		r.mu.Unlock()
		r.st.SetJobStatus(ctx, j.ID, store.JobSucceeded, "", modelID)
		os.RemoveAll(r.st.JobDir(j.ID))
		log.InfoContext(ctx, "training: done", "model_id", modelID)
	case errors.Is(context.Cause(jctx), ErrCancelled):
		r.st.SetJobStatus(ctx, j.ID, store.JobCancelled, "", "")
		log.InfoContext(ctx, "training: cancelled")
	case ctx.Err() == nil && (errors.Is(err, ErrLowMemory) || errors.Is(context.Cause(jctx), ErrLowMemory)):
		stops := r.stoppedForMemory(j.ID)
		if stops < MaxLowMemoryStops {
			// It resumes from its last checkpoint once there is room.
			r.st.SetJobStatus(ctx, j.ID, store.JobQueued, "", "")
			log.WarnContext(ctx, "training: stopped to keep memory for serving; back in the queue", "stops", stops, "error", err)
			return
		}
		r.st.SetJobStatus(ctx, j.ID, store.JobFailed, fmt.Sprintf("This run needed more memory than could be spared, and was stopped %d times to keep the server answering. Train it on a worker with more memory, use fewer rows or a smaller model, or raise -memory-limit.", stops), "")
		log.ErrorContext(ctx, "training: failed: kept running out of memory", "stops", stops)
	case errors.Is(err, errHeldBack) && ctx.Err() == nil:
		r.st.SetJobStatus(ctx, j.ID, store.JobQueued, "", "")
		log.InfoContext(ctx, "training: memory is short here; back in the queue")
		return
	case errors.Is(err, ErrWorkerLost):
		r.st.SetJobStatus(ctx, j.ID, store.JobQueued, "", "")
		log.WarnContext(ctx, "training: the worker went away; back in the queue")
		return
	case errors.Is(err, errNoWorker) && ctx.Err() == nil:
		// A worker could take it when it was claimed, but another job got
		// there first or the worker left; wait in the queue.
		r.st.SetJobStatus(ctx, j.ID, store.JobQueued, "", "")
		log.InfoContext(ctx, "training: no worker free; back in the queue")
		return
	case ctx.Err() != nil:
		// The server is stopping; the job goes back to the queue with its
		// checkpoint, so the next start picks it up.
		r.st.SetJobStatus(context.WithoutCancel(ctx), j.ID, store.JobQueued, "", "")
		return
	default:
		r.st.SetJobStatus(ctx, j.ID, store.JobFailed, friendly(err), "")
		log.ErrorContext(ctx, "training: failed", "error", err)
	}
	if r.OnDone != nil {
		if done, err := r.st.GetJob(ctx, j.ID); err == nil {
			r.OnDone(ctx, done)
		}
	}
}

// train runs the job, here or on a worker, and returns the id of the
// model it made.
func (r *Runner) train(ctx context.Context, j store.Job) (string, error) {
	a, ok := arch.Lookup(j.Architecture)
	if !ok {
		return "", fmt.Errorf("the model type %q isn't available on this server", j.Architecture)
	}
	d, err := r.st.GetDataset(ctx, j.DatasetID)
	if err != nil {
		return "", fmt.Errorf("the dataset is gone: %w", err)
	}
	src, err := SourceOf(r.st, d)
	if err != nil {
		return "", err
	}
	task := Task{Arch: a, Source: src, Settings: j.Settings, History: j.History, CheckpointDir: r.st.JobDir(j.ID)}
	var base store.Model
	if j.BaseModelID != "" {
		// A keep-learning pass continues from the model the chain made.
		if base, err = r.st.Get(ctx, j.BaseModelID); err == nil {
			if task.BaseWeights, err = r.st.Weights(base.ID); err != nil {
				return "", fmt.Errorf("couldn't read the model to continue from: %w", err)
			}
			bs := arch.Resolve(a, arch.Spec{InputShape: base.InputShape, Labels: base.Labels})
			task.BaseLabels, task.BaseFeatures = bs.Labels, base.Features
		} else {
			base = store.Model{} // deleted since; start fresh
		}
	}
	progress := func(epoch, step, steps int, history []store.Epoch) {
		r.st.JobProgress(ctx, j.ID, epoch, step, steps, history)
	}

	var res Result
	ran := false
	if r.Remote != nil {
		var name string
		if g, ok := r.Remote.(GroupRemote); ok && j.RunOn == "" && r.GroupWorkers != nil {
			if n := r.GroupWorkers(); n > 1 {
				res, name, ran, err = g.TrainGroup(ctx, j.ID, n, task, progress)
			}
		}
		if !ran {
			res, name, ran, err = r.Remote.Train(ctx, j.ID, j.RunOn, task, progress)
		}
		if ran {
			r.st.SetJobWorker(ctx, j.ID, name)
		}
	}
	if !ran {
		// A custom architecture's code is only on the workers that run it.
		if !r.trainsHere() || j.RunOn != "" || a.Custom {
			return "", errNoWorker
		}
		res, err = r.fitHere(ctx, j.ID, task, progress)
	}
	if err != nil {
		return "", err
	}
	m := store.Model{Name: j.Name, Description: j.Description, Architecture: a.Name, JobID: j.ID, TeamID: j.TeamID, WorkerID: j.RunOn}
	// A model trained on a worker stays there, unless that worker only
	// trains: then it runs wherever there is room.
	if t, ok := r.Remote.(TrainingOnly); ok && j.RunOn != "" && t.TrainingOnly(j.RunOn) {
		m.WorkerID = ""
	}
	if n := len(res.History); n > 0 {
		m.Accuracy = res.History[n-1].Accuracy
	}
	if !a.Fixed() {
		m.InputShape, m.Labels, m.Features, m.Columns = res.Spec.InputShape, res.Spec.Labels, res.Features, res.Columns
	}
	setNote := func(note string) {
		if j.Note != "" {
			note = j.Note + " " + note
		}
		r.st.SetJobNote(ctx, j.ID, note)
	}
	if base.ID != "" {
		// Replace the model in place only when the pass did at least as
		// well, so apps never get a worse answer without someone choosing it.
		switch {
		case m.Accuracy+1e-9 < base.Accuracy:
			setNote(fmt.Sprintf("Kept the previous version: it got %.0f%% right, this pass %.0f%%.", base.Accuracy*100, m.Accuracy*100))
			return base.ID, nil
		default:
			m.Name, m.Description = base.Name, base.Description
			if err := r.st.Replace(ctx, base.ID, m, res.Weights); err == nil {
				note := "Updated the model with what it learned from the latest examples."
				if res.Fresh {
					note = "The columns or answers changed, so it learned from scratch and replaced the model."
				}
				setNote(note)
				return base.ID, nil
			} else if !errors.Is(err, store.ErrNotFound) {
				return "", fmt.Errorf("couldn't update the model: %w", err)
			}
		}
	}
	created, err := r.st.Create(ctx, m, res.Weights)
	if err != nil {
		return "", fmt.Errorf("couldn't store the model: %w", err)
	}
	return created.ID, nil
}

// fitHere trains task in this process, when memory allows, as one of the
// runs WatchMemory stops when memory stays critical.
func (r *Runner) fitHere(ctx context.Context, id string, task Task, progress Progress) (res Result, err error) {
	select {
	case r.local <- struct{}{}:
	case <-ctx.Done():
		return Result{}, context.Cause(ctx)
	}
	defer func() { <-r.local }()
	if !r.localReady() { // checked again: memory may have run short while it waited
		return Result{}, errHeldBack
	}
	r.mu.Lock()
	r.localJobs[id] = true
	r.mu.Unlock()
	defer func() {
		r.mu.Lock()
		delete(r.localJobs, id)
		r.mu.Unlock()
	}()
	// A panic in training fails this run with an error, and gives the
	// slot back for the next.
	if p := safe.Run(r.log, "training job "+id, func() { res, err = r.fit(ctx, task, progress) }); p != nil {
		return Result{}, p
	}
	if err != nil && errors.Is(context.Cause(ctx), ErrLowMemory) {
		err = fmt.Errorf("%w: %w", ErrLowMemory, err)
	}
	return res, err
}

// friendly turns an error into words for the job's status.
func friendly(err error) string {
	switch {
	case errors.Is(err, ErrBadTable):
		return err.Error()
	case errors.Is(err, context.DeadlineExceeded):
		return "Training took too long and was stopped."
	case errors.Is(err, safe.ErrPanic):
		return "Training stopped with an internal error. It has been logged; try again, and report it if it keeps happening."
	}
	return "Training stopped with an error: " + err.Error()
}
