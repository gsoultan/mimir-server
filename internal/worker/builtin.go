// Package worker runs the server's built-in architectures as a worker,
// using the public SDK, so the same binary serves as a worker.
package worker

import (
	"context"
	"path/filepath"

	"github.com/gsoultan/mimir-server/internal/arch"
	"github.com/gsoultan/mimir-server/internal/runtime"
	"github.com/gsoultan/mimir-server/internal/schema"
	"github.com/gsoultan/mimir-server/internal/store"
	"github.com/gsoultan/mimir-server/internal/training"
	"github.com/gsoultan/mimir-server/worker"
)

// Builtin returns a handler for every architecture the server knows. Data
// is where checkpoints and the downloaded digits go.
func Builtin(data string) map[string]worker.Handler {
	cache := runtime.NewCache()
	out := map[string]worker.Handler{}
	for _, a := range arch.All() {
		out[a.Name] = &handler{a: a, data: data, cache: cache}
	}
	return out
}

type handler struct {
	a     arch.Architecture
	data  string
	cache *runtime.Cache
}

func (h *handler) Predict(ctx context.Context, m *worker.Model, input []float32) (worker.Prediction, error) {
	spec := arch.Resolve(h.a, arch.Spec{InputShape: m.InputShape, Labels: m.Labels})
	r, err := h.cache.Predict(ctx, m.ID, h.a, spec, func() ([]byte, error) { return m.Weights, nil }, input)
	if err != nil {
		return worker.Prediction{}, err
	}
	return worker.Prediction{Output: r.Output, Probabilities: r.Probability}, nil
}

func (h *handler) Train(ctx context.Context, t worker.TrainTask, progress func(worker.Progress)) (worker.Trained, error) {
	src := training.Source{Kind: store.DatasetTable, CSV: t.Dataset.CSV, Target: t.Dataset.Target}
	switch t.Dataset.Kind {
	case "mnist":
		src = training.Source{Kind: store.DatasetMNIST, MNISTDir: filepath.Join(h.data, "datasets", "mnist")}
	case "photos":
		src = training.Source{Kind: store.DatasetPhotos, CSV: t.Dataset.Archive}
	case "photo_table":
		src = training.Source{Kind: store.DatasetPhotoTable, CSV: t.Dataset.Archive, Target: t.Dataset.Target}
	}
	for _, c := range t.Dataset.Columns {
		src.Columns = append(src.Columns, schema.Column{Name: c.Name, Kind: schema.Kind(c.Kind), Values: c.Values, Fill: c.Fill})
	}
	task := training.Task{
		Arch:          h.a,
		Source:        src,
		Settings:      store.Settings{Epochs: t.Settings.Epochs, BatchSize: t.Settings.BatchSize, LearningRate: t.Settings.LearningRate, HoldOut: t.Settings.HoldOut},
		History:       fromSDK(t.History),
		CheckpointDir: filepath.Join(h.data, "training", t.JobID),
		BaseWeights:   t.BaseWeights, BaseLabels: t.BaseLabels, BaseFeatures: t.BaseFeatures,
	}
	res, err := training.Fit(ctx, task, func(epoch, step, steps int, history []store.Epoch) {
		progress(worker.Progress{Epoch: epoch, Step: step, Steps: steps, History: toSDK(history)})
	})
	if err != nil {
		return worker.Trained{}, err
	}
	out := worker.Trained{Weights: res.Weights, History: toSDK(res.History), InputShape: res.Spec.InputShape, Labels: res.Spec.Labels, Features: res.Features, Fresh: res.Fresh}
	for _, c := range res.Columns {
		out.Columns = append(out.Columns, worker.Column{Name: c.Name, Kind: string(c.Kind), Values: c.Values, Fill: c.Fill})
	}
	return out, nil
}

func toSDK(h []store.Epoch) []worker.Epoch {
	out := make([]worker.Epoch, len(h))
	for i, e := range h {
		out[i] = worker.Epoch{Epoch: e.Epoch, Loss: e.Loss, Accuracy: e.Accuracy, ValLoss: e.ValLoss, Millis: e.Millis, MAE: e.MAE}
	}
	return out
}

func fromSDK(h []worker.Epoch) []store.Epoch {
	out := make([]store.Epoch, len(h))
	for i, e := range h {
		out[i] = store.Epoch{Epoch: e.Epoch, Loss: e.Loss, Accuracy: e.Accuracy, ValLoss: e.ValLoss, Millis: e.Millis, MAE: e.MAE}
	}
	return out
}
