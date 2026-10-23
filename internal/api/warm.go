package api

import (
	"context"
	"time"

	"github.com/gsoultan/mimir-server/internal/arch"
	"github.com/gsoultan/mimir-server/internal/store"
)

// warmTimeout bounds handing one model's weights to the workers.
const warmTimeout = 5 * time.Minute

// Warm hands a model's weights to whoever answers for it ahead of any
// prediction: the workers that can run it (or the one it is placed on),
// and this process when it answers itself. The first prediction after a
// run finishes is then as quick as the rest, and a worker keeps answering
// with the version it has until the new one is loaded. A failure is only
// logged: the weights then travel with the first prediction, as before.
func (s *Service) Warm(ctx context.Context, m store.Model) {
	a, ok := arch.Lookup(m.Architecture)
	if !ok || a.Answers() == arch.Written {
		return // language models run on the console, loaded on first use
	}
	spec := arch.Resolve(a, arch.Spec{InputShape: m.InputShape, Labels: m.Labels})
	load := func() ([]byte, error) { return s.store.Weights(m.ID) }
	ver := m.WeightsVersion()
	// Mirror Predict: workers take it when this server runs no models,
	// when it is placed on one, or when only a worker's code runs it.
	pin, pinned := m.PinnedWorker()
	if s.Pool != nil && (!s.Local || pinned || a.Custom) {
		n, err := s.Pool.Warm(ctx, m.ID, ver, pin, a, spec, load)
		if err != nil {
			s.log.WarnContext(ctx, "warm: a worker couldn't load the model; it gets the weights with its first prediction", "model_id", m.ID, "error", err)
		} else if n > 0 {
			s.log.InfoContext(ctx, "warm: workers loaded the model", "model_id", m.ID, "workers", n)
		}
	}
	if s.Local && !pinned && !a.Custom {
		if err := s.cache.Load(m.ID, ver, a, spec, load); err != nil {
			s.log.WarnContext(ctx, "warm: couldn't load the model here", "model_id", m.ID, "error", err)
		}
	}
}

// WarmModel looks a model up and warms it in the background, for the
// moments a model appears or changes: a run finishing, an upload, a new
// placement.
func (s *Service) WarmModel(id string) {
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), warmTimeout)
		defer cancel()
		m, err := s.store.Get(ctx, id)
		if err != nil {
			return
		}
		s.Warm(ctx, m)
	}()
}

// WorkerLinked hands a worker that just connected the models placed on
// it, so they answer at once. The pool calls it on its own goroutine.
func (s *Service) WorkerLinked(workerID string) {
	ctx, cancel := context.WithTimeout(context.Background(), warmTimeout)
	defer cancel()
	items, err := s.store.ModelsOnWorker(ctx, workerID)
	if err != nil {
		return
	}
	for _, it := range items {
		if m, err := s.store.Get(ctx, it.ID); err == nil {
			s.Warm(ctx, m)
		}
	}
}
