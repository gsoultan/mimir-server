package training

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"sync"
	"time"

	"github.com/gsoultan/mimir/metrics"
	"github.com/gsoultan/mimir/nn"
	"github.com/gsoultan/mimir/nn/loss"
	"github.com/gsoultan/mimir/optim"
	"github.com/gsoultan/mimir/train"

	"github.com/gsoultan/mimir-server/internal/arch"
	"github.com/gsoultan/mimir-server/internal/store"
)

// Defaults apply where a job leaves a setting at zero. Tables are usually
// small, so they take smaller steps more often and learn a bit faster.
var (
	Defaults      = store.Settings{Epochs: 5, BatchSize: 64, LearningRate: 1e-3, HoldOut: 0.2}
	TableDefaults = store.Settings{Epochs: 10, BatchSize: 32, LearningRate: 5e-3, HoldOut: 0.2}
)

// DefaultsFor returns the defaults for a dataset of kind k.
func DefaultsFor(k store.DatasetKind) store.Settings {
	if k == store.DatasetTable {
		return TableDefaults
	}
	return Defaults
}

// ErrCancelled is the cause a job's context carries when a person stops it.
var ErrCancelled = errors.New("training: cancelled")

// Runner takes queued jobs one at a time and trains them.
type Runner struct {
	st  *store.Store
	log *slog.Logger

	mu      sync.Mutex
	running map[string]context.CancelCauseFunc
	wake    chan struct{}

	// OnDone is called after a job succeeds or fails, for webhooks and
	// alerts. It may be nil.
	OnDone func(ctx context.Context, j store.Job)
}

// New returns a Runner over st.
func New(st *store.Store, log *slog.Logger) *Runner {
	return &Runner{st: st, log: log, running: map[string]context.CancelCauseFunc{}, wake: make(chan struct{}, 1)}
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

// Run works the queue until ctx ends. A job interrupted by shutdown goes
// back to the queue for the next start.
func (r *Runner) Run(ctx context.Context) {
	if err := r.st.ResetRunningJobs(ctx); err != nil {
		r.log.ErrorContext(ctx, "training: reset running jobs", "error", err)
	}
	tick := time.NewTicker(5 * time.Second)
	defer tick.Stop()
	for {
		r.drain(ctx)
		select {
		case <-ctx.Done():
			return
		case <-r.wake:
		case <-tick.C:
		}
	}
}

// Tick runs every queued job once, for tests.
func (r *Runner) Tick(ctx context.Context) { r.drain(ctx) }

func (r *Runner) drain(ctx context.Context) {
	for ctx.Err() == nil {
		j, err := r.st.ClaimJob(ctx)
		if errors.Is(err, store.ErrNotFound) {
			return
		}
		if err != nil {
			r.log.ErrorContext(ctx, "training: claim job", "error", err)
			return
		}
		r.run(ctx, j)
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
		r.st.SetJobStatus(ctx, j.ID, store.JobSucceeded, "", modelID)
		os.RemoveAll(r.st.JobDir(j.ID))
		log.InfoContext(ctx, "training: done", "model_id", modelID)
	case errors.Is(context.Cause(jctx), ErrCancelled):
		r.st.SetJobStatus(ctx, j.ID, store.JobCancelled, "", "")
		log.InfoContext(ctx, "training: cancelled")
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

// train runs the job and returns the id of the model it made.
func (r *Runner) train(ctx context.Context, j store.Job) (string, error) {
	a, ok := arch.Lookup(j.Architecture)
	if !ok {
		return "", fmt.Errorf("the model type %q isn't available on this server", j.Architecture)
	}
	d, err := r.st.GetDataset(ctx, j.DatasetID)
	if err != nil {
		return "", fmt.Errorf("the dataset is gone: %w", err)
	}
	s := j.Settings
	trainSet, testSet, spec, err := Load(ctx, r.st, d, s, 42)
	if err != nil {
		return "", err
	}
	spec = arch.Resolve(a, spec)
	model := a.New(spec)
	opt := optim.AdamW(nn.Parameters(model), optim.LR(s.LearningRate), optim.WeightDecay(0.01))

	history := j.History
	steps := trainSet.Batches()
	var lastWrite time.Time
	opts := []train.Option{
		train.Loss(loss.CrossEntropy),
		train.Epochs(s.Epochs),
		train.Schedule(optim.Cosine(s.LearningRate, s.LearningRate/20, s.Epochs)),
		train.Checkpoint(r.st.JobDir(j.ID)),
		train.OnBatch(func(b train.Batch) error {
			if time.Since(lastWrite) > time.Second {
				lastWrite = time.Now()
				r.st.JobProgress(ctx, j.ID, b.Epoch-1, b.Index+1, steps, history)
			}
			return nil
		}),
		train.OnEpoch(func(e train.Epoch) error {
			ep := store.Epoch{Epoch: e.Epoch, Loss: e.Loss, Millis: e.Duration.Milliseconds()}
			ep.Accuracy, ep.ValLoss = e.Metrics["accuracy"], e.Metrics["val_loss"]
			if n := len(history); n > 0 && history[n-1].Epoch >= e.Epoch {
				history = history[:e.Epoch-1] // resumed: replace what the checkpoint replays
			}
			history = append(history, ep)
			return r.st.JobProgress(ctx, j.ID, e.Epoch, 0, steps, history)
		}),
	}
	if testSet != nil {
		opts = append(opts, train.Eval(testSet, metrics.Accuracy))
	}
	r.st.JobProgress(ctx, j.ID, j.Epoch, 0, steps, history)
	if err := train.Fit(ctx, model, opt, trainSet, opts...); err != nil {
		return "", err
	}
	if testSet == nil {
		// Nothing was held out, so score on what it learned from; better
		// than no number at all, and the UI says so.
		if m, err := train.Evaluate(ctx, model, trainSet, loss.CrossEntropy, metrics.Accuracy); err == nil && len(history) > 0 {
			history[len(history)-1].Accuracy = m["accuracy"]
			r.st.JobProgress(ctx, j.ID, s.Epochs, 0, steps, history)
		}
	}

	nn.Eval(model)
	var buf bytes.Buffer
	if err := nn.Save(ctx, &buf, model); err != nil {
		return "", fmt.Errorf("couldn't save the model: %w", err)
	}
	m := store.Model{Name: j.Name, Description: j.Description, Architecture: a.Name, JobID: j.ID}
	if !a.Fixed() {
		m.InputShape, m.Labels, m.Features = spec.InputShape, spec.Labels, d.Features
	}
	created, err := r.st.Create(ctx, m, buf.Bytes())
	if err != nil {
		return "", fmt.Errorf("couldn't store the model: %w", err)
	}
	return created.ID, nil
}

// friendly turns an error into words for the job's status.
func friendly(err error) string {
	switch {
	case errors.Is(err, ErrBadTable):
		return err.Error()
	case errors.Is(err, context.DeadlineExceeded):
		return "Training took too long and was stopped."
	}
	return "Training stopped with an error: " + err.Error()
}
