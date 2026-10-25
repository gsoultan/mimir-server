package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"math/rand/v2"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"connectrpc.com/connect"

	mimirv1 "github.com/gsoultan/mimir-server/gen/mimir/v1"
)

// The rest of the stack: workers this tool starts and stops itself, runs
// started all through the run rather than once, keep-learning jobs on a
// database table, and predictions on the models those runs make, so the
// console's queue, its workers and the hand-over of new weights are under
// load together.

// workerProc is one worker process this tool runs.
type workerProc struct {
	name, tasks, token, dir string
	id                      string

	mu       sync.Mutex
	cmd      *exec.Cmd
	exited   chan struct{}
	restarts int
	maxRSS   float64
	lastRSS  float64
}

// fleet is the workers of one run.
type fleet struct {
	bin, server, logs string
	workers           []*workerProc
}

// startFleet makes a token for each worker and starts it.
func startFleet(ctx context.Context, own clients, c config) (*fleet, error) {
	f := &fleet{bin: c.workerBin, server: c.server, logs: c.workerLogs}
	if f.bin == "" {
		return nil, errors.New("-train-workers and -serve-workers need -worker-bin, the mimir-server binary to run them with")
	}
	if err := os.MkdirAll(f.logs, 0o755); err != nil {
		return nil, err
	}
	stamp := time.Now().Format("150405")
	add := func(n int, tasks string) error {
		for i := range n {
			w := &workerProc{name: fmt.Sprintf("load-%s-%d-%s", tasks, i, stamp), tasks: tasks}
			res, err := own.workersSvc.CreateWorker(ctx, connect.NewRequest(&mimirv1.CreateWorkerRequest{Name: w.name}))
			if err != nil {
				return fmt.Errorf("create worker %s: %w", w.name, err)
			}
			w.id, w.token = res.Msg.GetWorker().GetId(), res.Msg.GetToken()
			w.dir = filepath.Join(f.logs, w.name)
			if err := f.start(w); err != nil {
				return err
			}
			f.workers = append(f.workers, w)
		}
		return nil
	}
	if err := add(c.trainWorkers, "train"); err != nil {
		return f, err
	}
	if err := add(c.serveWorkers, "serve"); err != nil {
		return f, err
	}
	// Wait for them all to say hello.
	deadline := time.Now().Add(time.Minute)
	for {
		ws, err := own.workersSvc.ListWorkers(ctx, connect.NewRequest(&mimirv1.ListWorkersRequest{}))
		if err != nil {
			return f, err
		}
		online := 0
		for _, w := range ws.Msg.GetWorkers() {
			if w.GetOnline() && slices.ContainsFunc(f.workers, func(p *workerProc) bool { return p.id == w.GetId() }) {
				online++
			}
		}
		if online == len(f.workers) {
			log.Printf("%d workers online", online)
			return f, nil
		}
		if time.Now().After(deadline) {
			return f, fmt.Errorf("only %d of %d workers came online in a minute; see %s", online, len(f.workers), f.logs)
		}
		time.Sleep(500 * time.Millisecond)
	}
}

func (f *fleet) start(w *workerProc) error {
	if err := os.MkdirAll(w.dir, 0o755); err != nil {
		return err
	}
	out, err := os.OpenFile(w.dir+".log", os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	cmd := exec.Command(f.bin, "-role=worker", "-tasks", w.tasks, "-server", f.server,
		"-token", w.token, "-data", w.dir, "-name", w.name)
	cmd.Stdout, cmd.Stderr = out, out
	if err := cmd.Start(); err != nil {
		out.Close()
		return fmt.Errorf("start worker %s: %w", w.name, err)
	}
	exited := make(chan struct{})
	go func() {
		cmd.Wait()
		out.Close()
		close(exited)
	}()
	w.mu.Lock()
	w.cmd, w.exited = cmd, exited
	w.mu.Unlock()
	return nil
}

// kill stops the worker at once, as a machine that loses power does, and
// starts it again after pause.
func (f *fleet) kill(w *workerProc, pause time.Duration) error {
	w.mu.Lock()
	cmd, exited := w.cmd, w.exited
	w.restarts++
	w.mu.Unlock()
	cmd.Process.Kill()
	<-exited
	time.Sleep(pause)
	return f.start(w)
}

// churn kills a random worker every period until ctx ends.
func (f *fleet) churn(ctx context.Context, period time.Duration) {
	if period <= 0 || len(f.workers) == 0 {
		return
	}
	t := time.NewTicker(period)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		w := f.workers[rand.IntN(len(f.workers))]
		log.Printf("killing worker %s", w.name)
		if err := f.kill(w, 5*time.Second); err != nil {
			log.Printf("restart %s: %v", w.name, err)
		}
	}
}

// sample reads each worker's resident memory from /proc.
func (f *fleet) sample() {
	for _, w := range f.workers {
		w.mu.Lock()
		pid := w.cmd.Process.Pid
		w.mu.Unlock()
		b, err := os.ReadFile(fmt.Sprintf("/proc/%d/status", pid))
		if err != nil {
			continue
		}
		for line := range strings.SplitSeq(string(b), "\n") {
			if v, ok := strings.CutPrefix(line, "VmRSS:"); ok {
				kb, _ := strconv.ParseFloat(strings.TrimSuffix(strings.TrimSpace(v), " kB"), 64)
				w.mu.Lock()
				w.lastRSS = kb * 1024
				w.maxRSS = max(w.maxRSS, w.lastRSS)
				w.mu.Unlock()
			}
		}
	}
}

func (f *fleet) stop(ctx context.Context, own clients) {
	for _, w := range f.workers {
		w.mu.Lock()
		cmd, exited := w.cmd, w.exited
		w.mu.Unlock()
		if cmd != nil {
			cmd.Process.Signal(os.Interrupt)
			select {
			case <-exited:
			case <-time.After(15 * time.Second):
				cmd.Process.Kill()
				<-exited
			}
		}
		if w.id != "" {
			own.workersSvc.DeleteWorker(ctx, connect.NewRequest(&mimirv1.DeleteWorkerRequest{Id: w.id}))
		}
	}
}

func (f *fleet) report(w io.Writer) {
	if f == nil || len(f.workers) == 0 {
		return
	}
	fmt.Fprintln(w, "workers:")
	for _, p := range f.workers {
		p.mu.Lock()
		fmt.Fprintf(w, "  %-26s %-6s RSS max %4.0f MB, last %4.0f MB, killed %d times\n", p.name, p.tasks, p.maxRSS/(1<<20), p.lastRSS/(1<<20), p.restarts)
		p.mu.Unlock()
	}
}

// trainedPool is the models runs made during the load, which the
// "predict trained" callers ask.
type trainedPool struct {
	mu     sync.Mutex
	models []*mimirv1.Model
	// kept are never deleted: the keep-learning models, retrained in place.
	kept map[string]bool
}

func (p *trainedPool) add(m *mimirv1.Model, keep bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if slices.ContainsFunc(p.models, func(o *mimirv1.Model) bool { return o.GetId() == m.GetId() }) {
		return
	}
	p.models = append(p.models, m)
	if keep {
		if p.kept == nil {
			p.kept = map[string]bool{}
		}
		p.kept[m.GetId()] = true
	}
}

func (p *trainedPool) pick() *mimirv1.Model {
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.models) == 0 {
		return nil
	}
	return p.models[rand.IntN(len(p.models))]
}

// trim takes the oldest models beyond limit (keep-learning ones aside) out
// of the pool and returns them for deleting.
func (p *trainedPool) trim(limit int) []*mimirv1.Model {
	p.mu.Lock()
	defer p.mu.Unlock()
	var gone []*mimirv1.Model
	for {
		n := 0
		first := -1
		for i, m := range p.models {
			if !p.kept[m.GetId()] {
				n++
				if first < 0 {
					first = i
				}
			}
		}
		if n <= limit || first < 0 {
			return gone
		}
		gone = append(gone, p.models[first])
		p.models = slices.Delete(p.models, first, first+1)
	}
}

// runStats counts how the runs started during the load ended.
type runStats struct {
	mu        sync.Mutex
	started   int
	succeeded int
	failed    map[string]int
	took      []time.Duration
	deleted   int
	passes    int // keep-learning passes that finished
	split     int // runs that trained on more than one worker
	onWorkers map[string]int
}

func (r *runStats) end(j *mimirv1.Job, took time.Duration) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.record(j)
	if j.GetStatus() == mimirv1.JobStatus_JOB_STATUS_SUCCEEDED {
		r.succeeded++
		r.took = append(r.took, took)
	}
}

// pass counts a keep-learning pass that ended.
func (r *runStats) pass(j *mimirv1.Job) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.passes++
	r.record(j)
}

// record notes where j trained and why it failed, if it did. r.mu must be
// held.
func (r *runStats) record(j *mimirv1.Job) {
	if r.onWorkers == nil {
		r.onWorkers = map[string]int{}
	}
	if w := j.GetWorker(); w != "" {
		r.onWorkers[w]++
		if strings.Contains(w, ",") {
			r.split++
		}
	} else {
		r.onWorkers["(console)"]++
	}
	if j.GetStatus() == mimirv1.JobStatus_JOB_STATUS_SUCCEEDED {
		return
	}
	if r.failed == nil {
		r.failed = map[string]int{}
	}
	r.failed[j.GetStatus().String()+": "+j.GetError()]++
}

// churnTraining keeps c.train runs in flight until ctx ends, starting the
// next as each finishes, and puts every model they make in pool. Runs left
// when ctx ends are cancelled, so a later run doesn't wait behind them.
func churnTraining(ctx context.Context, own clients, c config, pool *trainedPool, stats *runStats) {
	ds, err := own.datasets.CreateDataset(ctx, connect.NewRequest(&mimirv1.CreateDatasetRequest{
		Name: fmt.Sprintf("Load churn table %d", time.Now().UnixNano()), Kind: mimirv1.DatasetKind_DATASET_KIND_TABLE, Csv: table(c.trainRows),
	}))
	if err != nil {
		log.Printf("training: %v", err)
		return
	}
	type flight struct {
		id    string
		start time.Time
	}
	var running []flight
	n := 0
	launch := func() bool {
		job, err := own.training.CreateJob(ctx, connect.NewRequest(&mimirv1.CreateJobRequest{
			Name: fmt.Sprintf("Load churn %d", n), Architecture: "table-mlp", DatasetId: ds.Msg.GetDataset().GetId(),
			Settings: &mimirv1.TrainingSettings{Epochs: int32(c.trainEpochs)},
		}))
		n++
		if err != nil {
			stats.mu.Lock()
			if stats.failed == nil {
				stats.failed = map[string]int{}
			}
			stats.failed["start: "+errCode(err)]++
			stats.mu.Unlock()
			return false
		}
		stats.mu.Lock()
		stats.started++
		stats.mu.Unlock()
		running = append(running, flight{job.Msg.GetJob().GetId(), time.Now()})
		return true
	}
	defer func() {
		bg := context.Background()
		for _, f := range running {
			own.training.CancelJob(bg, connect.NewRequest(&mimirv1.CancelJobRequest{Id: f.id}))
		}
	}()
	tick := time.NewTicker(2 * time.Second)
	defer tick.Stop()
	for ctx.Err() == nil {
		// A start that fails is tried again next tick rather than at once.
		for len(running) < c.train && ctx.Err() == nil && launch() {
		}
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
		var still []flight
		for _, f := range running {
			j, err := own.training.GetJob(ctx, connect.NewRequest(&mimirv1.GetJobRequest{Id: f.id}))
			if err != nil {
				still = append(still, f)
				continue
			}
			switch j.Msg.GetJob().GetStatus() {
			case mimirv1.JobStatus_JOB_STATUS_QUEUED, mimirv1.JobStatus_JOB_STATUS_RUNNING:
				still = append(still, f)
				continue
			}
			stats.end(j.Msg.GetJob(), time.Since(f.start))
			if id := j.Msg.GetJob().GetModelId(); id != "" {
				if m, err := own.models.GetModel(ctx, connect.NewRequest(&mimirv1.GetModelRequest{Id: id})); err == nil {
					pool.add(m.Msg.GetModel(), false)
				}
			}
		}
		running = still
		// Models come and go, as people retire old ones: delete the oldest
		// beyond c.keepModels once nothing is sent to them.
		for _, m := range pool.trim(c.keepModels) {
			go func() {
				time.Sleep(3 * time.Second)
				if _, err := own.models.DeleteModel(ctx, connect.NewRequest(&mimirv1.DeleteModelRequest{Id: m.GetId()})); err == nil {
					stats.mu.Lock()
					stats.deleted++
					stats.mu.Unlock()
				}
			}()
		}
	}
}

// startKeepLearning makes c.keepLearning jobs that keep learning from the
// table c.sourceQuery reads, each waiting for c.keepNewRows new rows, and
// adds their models to pool as their first passes finish. Something else
// adds the rows (see docs/capacity.md).
func startKeepLearning(ctx context.Context, own clients, c config) ([]string, error) {
	ds, err := own.datasets.CreateDataset(ctx, connect.NewRequest(&mimirv1.CreateDatasetRequest{
		Name:       fmt.Sprintf("Load source %d", time.Now().UnixNano()),
		Kind:       mimirv1.DatasetKind_DATASET_KIND_TABLE,
		Connection: &mimirv1.Connection{Driver: c.sourceDriver, Dsn: c.sourceDSN},
		Query:      c.sourceQuery,
	}))
	if err != nil {
		return nil, fmt.Errorf("dataset from %s: %w", c.sourceDriver, err)
	}
	var ids []string
	for i := range c.keepLearning {
		job, err := own.training.CreateJob(ctx, connect.NewRequest(&mimirv1.CreateJobRequest{
			Name: fmt.Sprintf("Load keep learning %d", i), Architecture: "table-mlp", DatasetId: ds.Msg.GetDataset().GetId(),
			Settings:      &mimirv1.TrainingSettings{Epochs: int32(c.trainEpochs)},
			RepeatSeconds: 900, MinNewRows: int64(c.keepNewRows),
		}))
		if err != nil {
			return ids, err
		}
		ids = append(ids, job.Msg.GetJob().GetId())
	}
	return ids, nil
}

// watchKeepLearning follows the keep-learning chains: each finished pass
// is counted, and the model it trains goes in pool once.
func watchKeepLearning(ctx context.Context, own clients, roots []string, pool *trainedPool, stats *runStats) {
	seen := map[string]bool{}
	tick := time.NewTicker(5 * time.Second)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
		jobs, err := own.training.ListJobs(ctx, connect.NewRequest(&mimirv1.ListJobsRequest{}))
		if err != nil {
			continue
		}
		for _, j := range jobs.Msg.GetJobs() {
			root := j.GetParentId()
			if root == "" {
				root = j.GetId()
			}
			if !slices.Contains(roots, root) || seen[j.GetId()] {
				continue
			}
			switch j.GetStatus() {
			case mimirv1.JobStatus_JOB_STATUS_QUEUED, mimirv1.JobStatus_JOB_STATUS_RUNNING:
				continue
			}
			seen[j.GetId()] = true
			stats.pass(j)
			if id := j.GetModelId(); id != "" {
				if m, err := own.models.GetModel(ctx, connect.NewRequest(&mimirv1.GetModelRequest{Id: id})); err == nil {
					pool.add(m.Msg.GetModel(), true)
				}
			}
		}
	}
}

// predictTrainedLoop asks the models in pool, a table row at a time.
func predictTrainedLoop(ctx context.Context, app clients, pool *trainedPool, r *recorder, misses *atomic.Int64) {
	for ctx.Err() == nil {
		m := pool.pick()
		if m == nil {
			select {
			case <-ctx.Done():
			case <-time.After(time.Second):
			}
			continue
		}
		t0 := time.Now()
		_, err := app.predict.Predict(ctx, connect.NewRequest(&mimirv1.PredictRequest{ModelId: m.GetId(), Fields: rowFor(m)}))
		if connect.CodeOf(err) == connect.CodeNotFound {
			// Deleted between the pick and the answer.
			misses.Add(1)
			continue
		}
		r.add(time.Since(t0), err, ctx)
	}
}

// rowFor makes up a row with the columns m reads.
func rowFor(m *mimirv1.Model) map[string]string {
	row := map[string]string{}
	for _, col := range m.GetColumns() {
		switch {
		case len(col.GetValues()) > 0:
			row[col.GetName()] = col.GetValues()[rand.IntN(len(col.GetValues()))]
		case col.GetKind() == "number":
			row[col.GetName()] = fmt.Sprintf("%.3f", rand.NormFloat64())
		}
	}
	return row
}

func (r *runStats) report(w io.Writer) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.started == 0 && r.passes == 0 {
		return
	}
	fmt.Fprintf(w, "runs during the load: %d started, %d succeeded (whole run p50 %s, p90 %s, max %s), %d trained on several workers, %d keep-learning passes, %d models deleted\n",
		r.started, r.succeeded, pct(r.took, 50), pct(r.took, 90), pct(r.took, 100), r.split, r.passes, r.deleted)
	names := make([]string, 0, len(r.onWorkers))
	for n := range r.onWorkers {
		names = append(names, n)
	}
	slices.Sort(names)
	for _, n := range names {
		fmt.Fprintf(w, "  trained on %s: %d\n", n, r.onWorkers[n])
	}
	for e, k := range r.failed {
		fmt.Fprintf(w, "  run failed %d × %s\n", k, e)
	}
}
