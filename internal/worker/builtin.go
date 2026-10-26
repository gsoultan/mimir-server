// Package worker runs the server's built-in architectures as a worker,
// using the public SDK, so the same binary serves as a worker.
package worker

import (
	"context"
	"encoding/json"
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
//
// The handlers are Loaders: the worker hands each model's weights over
// once and bounds how many models stay loaded (worker.Config.CacheBytes),
// so their shared cache has no limit of its own.
func Builtin(data string) map[string]worker.Handler {
	cache := runtime.NewCache(0)
	out := map[string]worker.Handler{}
	for _, a := range arch.All() {
		if a.Answers() == arch.Written {
			continue // language models run on the console
		}
		out[a.Name] = &handler{a: a, data: data, cache: cache}
	}
	return out
}

type handler struct {
	a     arch.Architecture
	data  string
	cache *runtime.Cache
}

var (
	_ worker.Loader       = (*handler)(nil)
	_ worker.GroupTrainer = (*handler)(nil)
)

// TrainsInGroups says a run can be split between several workers.
func (h *handler) TrainsInGroups() {}

func (h *handler) spec(m *worker.Model) arch.Spec {
	return arch.Resolve(h.a, arch.Spec{InputShape: m.InputShape, Labels: m.Labels})
}

// weights returns m's weights, or ErrNeedWeights once the worker has
// dropped them after Load.
func weights(m *worker.Model) func() ([]byte, error) {
	return func() ([]byte, error) {
		if len(m.Weights) == 0 {
			return nil, worker.ErrNeedWeights
		}
		return m.Weights, nil
	}
}

func (h *handler) Load(_ context.Context, m *worker.Model) error {
	return h.cache.Load(m.ID, m.Version, h.a, h.spec(m), weights(m))
}

func (h *handler) Unload(modelID string) { h.cache.Forget(modelID) }

func (h *handler) Predict(ctx context.Context, m *worker.Model, input []float32) (worker.Prediction, error) {
	r, err := h.cache.Predict(ctx, m.ID, m.Version, h.a, h.spec(m), weights(m), input)
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
		Settings:      store.Settings{Epochs: t.Settings.Epochs, BatchSize: t.Settings.BatchSize, LearningRate: t.Settings.LearningRate, HoldOut: t.Settings.HoldOut, Layers: arch.FormatLayers(layersOf(t.Settings.Layers))},
		History:       fromSDK(t.History),
		CheckpointDir: filepath.Join(h.data, "training", t.JobID),
		BaseWeights:   t.BaseWeights, BaseLabels: t.BaseLabels, BaseFeatures: t.BaseFeatures,
	}
	if t.Group != nil {
		task.Group = group{t.Group}
	}
	res, err := training.Fit(ctx, task, func(epoch, step, steps int, history []store.Epoch) {
		progress(worker.Progress{Epoch: epoch, Step: step, Steps: steps, History: toSDK(history)})
	})
	if err != nil {
		return worker.Trained{}, err
	}
	out := worker.Trained{Weights: res.Weights, History: toSDK(res.History), InputShape: res.Spec.InputShape, Labels: res.Spec.Labels, Features: res.Features, Fresh: res.Fresh}
	if res.Report != nil {
		out.Report, _ = json.Marshal(res.Report)
	}
	for _, c := range res.Columns {
		out.Columns = append(out.Columns, worker.Column{Name: c.Name, Kind: string(c.Kind), Values: c.Values, Fill: c.Fill})
	}
	return out, nil
}

// group is the SDK's Group as training wants it.
type group struct{ g *worker.Group }

func (g group) Rank() int { return g.g.Rank }
func (g group) Size() int { return g.g.Size }
func (g group) Mean(ctx context.Context, values []float32, weight float64) ([]float32, error) {
	return g.g.Mean(ctx, values, weight)
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

func layersOf(ls []worker.Layer) []arch.Layer {
	out := make([]arch.Layer, len(ls))
	for i, l := range ls {
		out[i] = arch.Layer{Kind: l.Kind, Units: l.Units, Rate: l.Rate}
	}
	return out
}
