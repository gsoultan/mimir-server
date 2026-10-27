package training

import (
	"bytes"
	"context"
	"fmt"
	"math"
	"slices"
	"time"

	"github.com/gsoultan/mimir/io/mim"
	"github.com/gsoultan/mimir/metrics"
	"github.com/gsoultan/mimir/nn"
	"github.com/gsoultan/mimir/nn/loss"
	"github.com/gsoultan/mimir/optim"
	"github.com/gsoultan/mimir/tensor"
	"github.com/gsoultan/mimir/train"

	"github.com/gsoultan/mimir-server/internal/arch"
	"github.com/gsoultan/mimir-server/internal/schema"
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
	// BaseWeights, when set, are a model to continue learning from. If the
	// examples no longer fit it (a new column or answer appeared), the task
	// starts fresh and Result.Fresh says so.
	BaseWeights []byte
	// BaseLabels and BaseFeatures are what the base model answers and
	// reads. Weights only carry over when both still match, so an answer
	// never silently changes meaning.
	BaseLabels   []string
	BaseFeatures []string
	// BaseAdapter, for a fine-tune of a language model, is the LoRA
	// adapter of the fine-tune it continues; BaseWeights are then the
	// language model's file.
	BaseAdapter []byte
	// Group, when it has more than one member, trains the task together
	// with other workers (see Group). Its members keep no checkpoints:
	// a run that loses one starts again.
	Group Group
}

// Progress is told where a task is: the finished epoch, the step within
// the next one, how many steps an epoch has, and every epoch so far.
type Progress func(epoch, step, steps int, history []store.Epoch)

// Result is what a finished task made.
type Result struct {
	Weights  []byte
	History  []store.Epoch
	Spec     arch.Spec
	Features []string        // the numbers a table row becomes
	Columns  []schema.Column // the input columns, for a table
	// Fresh is true when BaseWeights were given but didn't fit.
	Fresh bool
	// Report is how the model did on the examples kept aside, in detail;
	// nil when it couldn't be scored.
	Report *store.Report
}

// Fit trains a task to completion, or until ctx ends.
func Fit(ctx context.Context, t Task, progress Progress) (Result, error) {
	if progress == nil {
		progress = func(int, int, int, []store.Epoch) {}
	}
	if t.Arch.Answers() == arch.Written {
		return fitLoRA(ctx, t, progress)
	}
	s := t.Settings
	l, err := Load(ctx, t.Source, t.Arch, s, 42)
	if err != nil {
		return Result{}, err
	}
	trainSet, testSet, features := l.Train, l.Test, l.Features
	if t.grouped() {
		trainSet = shardOf(trainSet, t.Group.Rank(), t.Group.Size())
		t.CheckpointDir = ""
		if t.Group.Rank() != 0 {
			// Member 0 scores the model and says how it goes.
			testSet, progress = nil, func(int, int, int, []store.Epoch) {}
		}
	}
	spec := arch.Resolve(t.Arch, l.Spec)
	if t.Arch.Designable {
		if spec.Layers, err = arch.ParseLayers(s.Layers); err != nil {
			return Result{}, err
		}
		if len(spec.Layers) == 0 {
			spec.Layers = defaultLayers(t.BaseWeights)
		}
	}
	model := t.Arch.New(spec)
	fresh := false
	if len(t.BaseWeights) > 0 && (!slices.Equal(t.BaseLabels, spec.Labels) || !slices.Equal(t.BaseFeatures, features)) {
		fresh = true
	} else if len(t.BaseWeights) > 0 {
		// Continue from the earlier model. Its column statistics come with
		// it, so the inputs it already understands keep meaning the same.
		f, err := mim.Parse(t.BaseWeights)
		if err == nil {
			err = f.Load(model)
			f.Close()
		}
		if err != nil {
			model, fresh = t.Arch.New(spec), true
		}
	}
	var group *syncer
	if t.grouped() {
		group = newSyncer(t.Group, model)
		if err := group.start(ctx); err != nil {
			return Result{}, err
		}
	}
	opt := optim.AdamW(nn.Parameters(model), optim.LR(s.LearningRate), optim.WeightDecay(0.01))

	history := t.History
	steps := trainSet.Batches()
	var lastWrite time.Time
	lossFn, scores := loss.Func(loss.CrossEntropy), []func() metrics.Metric{metrics.Accuracy}
	if t.Arch.Answers() == arch.Number {
		lossFn, scores = numberLoss(spec.TargetStd), numberScores(spec.TargetStd)
	}
	opts := []train.Option{
		train.Loss(lossFn),
		train.Epochs(s.Epochs),
		train.Schedule(optim.Cosine(s.LearningRate, s.LearningRate/20, s.Epochs)),
		train.OnBatch(func(b train.Batch) error {
			if group != nil && ((b.Index+1)%GroupSyncSteps == 0 || b.Index+1 == steps) {
				if err := group.average(ctx, 1); err != nil {
					return err
				}
			}
			if time.Since(lastWrite) > time.Second {
				lastWrite = time.Now()
				progress(b.Epoch-1, b.Index+1, steps, history)
			}
			return nil
		}),
		train.OnEpoch(func(e train.Epoch) error {
			ep := store.Epoch{Epoch: e.Epoch, Loss: e.Loss, Millis: e.Duration.Milliseconds()}
			ep.Accuracy, ep.ValLoss, ep.MAE = e.Metrics["accuracy"], e.Metrics["val_loss"], e.Metrics["mae"]
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
		opts = append(opts, train.Eval(testSet, scores...))
	}
	// Under a compute budget (see runtime.SetComputeBudget) a batch too
	// big for it is split into chunks that add up to the same step,
	// rather than failing or running out of memory: a run trains in the
	// memory there is. Every run sets the same budget, so runs side by
	// side don't undo each other's.
	if b := tensor.MemoryBudget(tensor.DefaultDevice()); b > 0 {
		opts = append(opts, train.MemoryBudget(b))
	}
	progress(len(history), 0, steps, history)
	if err := train.Fit(ctx, model, opt, trainSet, opts...); err != nil {
		return Result{}, err
	}
	if testSet == nil && (group == nil || t.Group.Rank() == 0) {
		// Nothing was held out, so score on what it learned from; better
		// than no number at all, and the UI says so.
		if m, err := train.Evaluate(ctx, model, trainSet, lossFn, scores...); err == nil && len(history) > 0 {
			history[len(history)-1].Accuracy, history[len(history)-1].MAE = m["accuracy"], m["mae"]
			progress(s.Epochs, 0, steps, history)
		}
	}

	var report *store.Report
	if group == nil || t.Group.Rank() == 0 {
		// A report that can't be made leaves the run without one; the
		// model is still good.
		set := testSet
		if set == nil {
			set = trainSet
		}
		if report, err = Score(ctx, model, set, t.Arch, spec.Labels); err == nil {
			report.OnTraining = testSet == nil
		} else if ctx.Err() != nil {
			return Result{}, context.Cause(ctx)
		}
	}

	nn.Eval(model)
	var buf bytes.Buffer
	var save *mim.SaveOptions
	if len(spec.Layers) > 0 {
		// The file says how it was shaped, so it rebuilds the same way.
		save = &mim.SaveOptions{Metadata: map[string]any{arch.LayersKey: arch.FormatLayers(spec.Layers)}}
	}
	if err := mim.Save(ctx, &buf, model, save); err != nil {
		return Result{}, fmt.Errorf("couldn't save the model: %w", err)
	}
	if report != nil {
		report.SizeBytes = int64(buf.Len())
	}
	return Result{Weights: buf.Bytes(), History: history, Spec: spec, Features: features, Columns: l.Columns, Fresh: fresh, Report: report}, nil
}

// defaultLayers is the shape of an undesigned table model: the base
// model's, when continuing from one, so its weights still fit, and
// otherwise arch.DefaultLayers.
func defaultLayers(base []byte) []arch.Layer {
	if len(base) == 0 {
		return arch.DefaultLayers
	}
	f, err := mim.Parse(base)
	if err != nil {
		return arch.DefaultLayers
	}
	defer f.Close()
	md, err := f.Metadata()
	if err != nil {
		return arch.DefaultLayers
	}
	s, _ := md[arch.LayersKey].(string)
	ls, err := arch.ParseLayers(s)
	if err != nil {
		return arch.DefaultLayers
	}
	return ls // nil for a model from before DefaultLayers
}

// numberLoss is the squared error of a number model, measured in the
// answer's spread so prices and counts learn alike.
func numberLoss(std float32) loss.Func {
	scale := tensor.Scalar[float32](1 / max(std, 1e-6))
	return func(pred, target *tensor.Tensor) *tensor.Tensor {
		d := pred.Sub(target).Mul(scale)
		return d.Mul(d).Mean()
	}
}

// numberScores score a number model. Its "accuracy" is the share of
// answers within 10% of the real one, or within a tenth of the answers'
// spread for answers near zero; "mae" is how far off it is on average.
func numberScores(std float32) []func() metrics.Metric {
	return []func() metrics.Metric{
		func() metrics.Metric { return &closeness{tolerance: 0.1 * float64(std)} },
		func() metrics.Metric { return &meanError{} },
	}
}

type closeness struct {
	tolerance float64
	hits, n   float64
}

func (c *closeness) Name() string { return "accuracy" }

func (c *closeness) Update(ctx context.Context, pred, target *tensor.Tensor) error {
	return pairs(ctx, pred, target, func(p, t float64) {
		if math.Abs(p-t) <= max(0.1*math.Abs(t), c.tolerance) {
			c.hits++
		}
		c.n++
	})
}

func (c *closeness) Value() float64 { return c.hits / max(c.n, 1) }

type meanError struct{ sum, n float64 }

func (m *meanError) Name() string { return "mae" }

func (m *meanError) Update(ctx context.Context, pred, target *tensor.Tensor) error {
	return pairs(ctx, pred, target, func(p, t float64) { m.sum, m.n = m.sum+math.Abs(p-t), m.n+1 })
}

func (m *meanError) Value() float64 { return m.sum / max(m.n, 1) }

// pairs calls f with each prediction and its real answer.
func pairs(ctx context.Context, pred, target *tensor.Tensor, f func(p, t float64)) error {
	p, err := pred.Data[float32](ctx)
	if err != nil {
		return err
	}
	t, err := target.Data[float32](ctx)
	if err != nil {
		return err
	}
	if len(p) != len(t) {
		return fmt.Errorf("training: %d predictions for %d answers", len(p), len(t))
	}
	for i := range p {
		f(float64(p[i]), float64(t[i]))
	}
	return nil
}
