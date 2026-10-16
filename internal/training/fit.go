package training

import (
	"bytes"
	"context"
	"fmt"
	"time"

	"github.com/gsoultan/mimir/metrics"
	"github.com/gsoultan/mimir/nn"
	"github.com/gsoultan/mimir/nn/loss"
	"github.com/gsoultan/mimir/optim"
	"github.com/gsoultan/mimir/train"

	"github.com/gsoultan/mimir-server/internal/arch"
	"github.com/gsoultan/mimir-server/internal/store"
)

// Task is one training run, described without reference to the store so
// a worker elsewhere can run it too.
type Task struct {
	Arch     arch.Architecture
	Source   Source
	Settings store.Settings
	// History is what earlier attempts finished, for a resumed task.
	History []store.Epoch
	// CheckpointDir is where a checkpoint is written after every epoch and
	// resumed from. Empty means no checkpoints.
	CheckpointDir string
}

// Progress is told where a task is: the finished epoch, the step within
// the next one, how many steps an epoch has, and every epoch so far.
type Progress func(epoch, step, steps int, history []store.Epoch)

// Result is what a finished task made.
type Result struct {
	Weights  []byte
	History  []store.Epoch
	Spec     arch.Spec
	Features []string // column names, for a table
}

// Fit trains a task to completion, or until ctx ends.
func Fit(ctx context.Context, t Task, progress Progress) (Result, error) {
	if progress == nil {
		progress = func(int, int, int, []store.Epoch) {}
	}
	s := t.Settings
	trainSet, testSet, spec, features, err := Load(ctx, t.Source, s, 42)
	if err != nil {
		return Result{}, err
	}
	spec = arch.Resolve(t.Arch, spec)
	model := t.Arch.New(spec)
	opt := optim.AdamW(nn.Parameters(model), optim.LR(s.LearningRate), optim.WeightDecay(0.01))

	history := t.History
	steps := trainSet.Batches()
	var lastWrite time.Time
	opts := []train.Option{
		train.Loss(loss.CrossEntropy),
		train.Epochs(s.Epochs),
		train.Schedule(optim.Cosine(s.LearningRate, s.LearningRate/20, s.Epochs)),
		train.OnBatch(func(b train.Batch) error {
			if time.Since(lastWrite) > time.Second {
				lastWrite = time.Now()
				progress(b.Epoch-1, b.Index+1, steps, history)
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
			progress(e.Epoch, 0, steps, history)
			return nil
		}),
	}
	if t.CheckpointDir != "" {
		opts = append(opts, train.Checkpoint(t.CheckpointDir))
	}
	if testSet != nil {
		opts = append(opts, train.Eval(testSet, metrics.Accuracy))
	}
	progress(len(history), 0, steps, history)
	if err := train.Fit(ctx, model, opt, trainSet, opts...); err != nil {
		return Result{}, err
	}
	if testSet == nil {
		// Nothing was held out, so score on what it learned from; better
		// than no number at all, and the UI says so.
		if m, err := train.Evaluate(ctx, model, trainSet, loss.CrossEntropy, metrics.Accuracy); err == nil && len(history) > 0 {
			history[len(history)-1].Accuracy = m["accuracy"]
			progress(s.Epochs, 0, steps, history)
		}
	}

	nn.Eval(model)
	var buf bytes.Buffer
	if err := nn.Save(ctx, &buf, model); err != nil {
		return Result{}, fmt.Errorf("couldn't save the model: %w", err)
	}
	return Result{Weights: buf.Bytes(), History: history, Spec: spec, Features: features}, nil
}
