package training

import (
	"bytes"
	"context"
	"fmt"
	"time"

	"github.com/gsoultan/mimir/io/mim"
	"github.com/gsoultan/mimir/metrics"
	"github.com/gsoultan/mimir/nn/loss"
	"github.com/gsoultan/mimir/tabular"
	"github.com/gsoultan/mimir/tensor"
	"github.com/gsoultan/mimir/train"

	"github.com/gsoultan/mimir-server/internal/arch"
	"github.com/gsoultan/mimir-server/internal/store"
)

// learn fits a Learner architecture's model to the whole training table at
// once, then scores it as Fit scores a network, as one epoch.
func learn(ctx context.Context, t Task, l Loaded, spec arch.Spec, progress Progress) (Result, error) {
	d, err := collect(ctx, l.Train)
	if err != nil {
		return Result{}, err
	}
	classes := 0
	if t.Arch.Task() == tabular.Classification {
		classes = max(len(spec.Labels), 2)
	}
	start, last := time.Now(), time.Time{}
	progress(0, 0, 1, nil)
	mod, shape, err := t.Arch.Learner.Fit(ctx, d, t.Arch.Task(), classes, func(done, total int) {
		if time.Since(last) > time.Second {
			last = time.Now()
			progress(0, done, total, nil)
		}
	})
	if err != nil {
		return Result{}, fmt.Errorf("%w: %v", ErrBadTable, err)
	}
	lossFn, scores := loss.Func(loss.CrossEntropy), []func() metrics.Metric{metrics.Accuracy}
	if t.Arch.Answers() == arch.Number {
		lossFn, scores = numberLoss(spec.TargetStd), numberScores(spec.TargetStd)
	}
	ep := store.Epoch{Epoch: 1, Millis: time.Since(start).Milliseconds()}
	if m, err := train.Evaluate(ctx, mod, l.Train, lossFn); err == nil {
		ep.Loss = m["val_loss"]
	}
	// Nothing held out: score on what it learned from, as Fit does.
	scored := l.Test
	if scored == nil {
		scored = l.Train
	}
	m, err := train.Evaluate(ctx, mod, scored, lossFn, scores...)
	if err != nil {
		return Result{}, err
	}
	if l.Test != nil {
		ep.ValLoss = m["val_loss"]
	}
	ep.Accuracy, ep.MAE = m["accuracy"], m["mae"]
	history := []store.Epoch{ep}
	progress(1, 0, 1, history)

	var buf bytes.Buffer
	if err := mim.Save(ctx, &buf, mod, &mim.SaveOptions{Metadata: map[string]any{arch.ShapeKey: shape}}); err != nil {
		return Result{}, fmt.Errorf("couldn't save the model: %w", err)
	}
	return Result{Weights: buf.Bytes(), History: history, Spec: spec, Features: l.Features, Columns: l.Columns}, nil
}

// collect reads every batch of s into one table.
func collect(ctx context.Context, s Set) (tabular.Data, error) {
	var d tabular.Data
	for b, err := range s.All(ctx) {
		if err != nil {
			return d, err
		}
		x, err := b.X.Data[float32](ctx)
		if err != nil {
			return d, err
		}
		shape := b.X.Shape()
		d.Features = len(x) / max(shape[0], 1)
		d.X = append(d.X, x...)
		if b.Y.DType() == tensor.Int32 {
			y, err := b.Y.Data[int32](ctx)
			if err != nil {
				return d, err
			}
			for _, v := range y {
				d.Y = append(d.Y, float32(v))
			}
			continue
		}
		y, err := b.Y.Data[float32](ctx)
		if err != nil {
			return d, err
		}
		d.Y = append(d.Y, y...)
	}
	return d, nil
}
