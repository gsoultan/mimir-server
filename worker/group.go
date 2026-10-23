package worker

import (
	"context"
	"errors"
	"slices"
	"sync"

	mimirv1 "github.com/gsoultan/mimir-server/gen/mimir/v1"
)

// GroupTrainer is a Trainer that can train one run together with other
// workers: TrainTask.Group is set, and the trainer learns from its share of
// the batches and averages its weights with the group's through
// Group.Mean. The console splits a run only between workers whose handler
// for its architecture is a GroupTrainer.
type GroupTrainer interface {
	Trainer
	// TrainsInGroups marks the trainer; it is never called.
	TrainsInGroups()
}

// Group is this worker's part in a run trained by several workers.
type Group struct {
	Rank int // this worker's place, from 0
	Size int // how many workers train the run

	w      *worker
	taskID string
	means  chan *mimirv1.GroupMean
	mu     sync.Mutex
	round  int64
}

// errGroupGone is what Mean returns once the task has ended.
var errGroupGone = errors.New("worker: the group's run has ended")

// Mean sends values, weighed by weight, to the console and returns the
// mean of every member's. Every member calls it the same number of times
// with values of the same length.
func (g *Group) Mean(ctx context.Context, values []float32, weight float64) ([]float32, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	round := g.round
	g.round++
	if err := g.w.send(&mimirv1.LinkRequest{Body: &mimirv1.LinkRequest_GroupShare{GroupShare: &mimirv1.GroupShare{TaskId: g.taskID, Round: round, Values: values, Weight: weight}}}); err != nil {
		return nil, err
	}
	for {
		select {
		case m, ok := <-g.means:
			if !ok {
				return nil, errGroupGone
			}
			if m.Round == round {
				return m.Values, nil
			}
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
}

// groupFor returns the group a training task joins, or nil when it trains
// alone. The worker hands it the console's means until done is called.
func (w *worker) groupFor(t *mimirv1.TrainTask) (g *Group, done func()) {
	if t.GroupSize <= 1 {
		return nil, func() {}
	}
	g = &Group{Rank: int(t.GroupRank), Size: int(t.GroupSize), w: w, taskID: t.TaskId, means: make(chan *mimirv1.GroupMean, 1)}
	w.mu.Lock()
	if w.groups == nil {
		w.groups = map[string]*Group{}
	}
	w.groups[t.TaskId] = g
	w.mu.Unlock()
	return g, func() {
		w.mu.Lock()
		delete(w.groups, t.TaskId)
		w.mu.Unlock()
	}
}

// deliverMean hands a mean to the group waiting for it. A member waits
// for each mean it asked for, so the channel has room; it never blocks
// the link.
func (w *worker) deliverMean(m *mimirv1.GroupMean) {
	w.mu.Lock()
	g := w.groups[m.TaskId]
	w.mu.Unlock()
	if g == nil {
		return
	}
	select {
	case g.means <- m:
	default:
	}
}

// groupArchitectures are the architectures whose handler trains in groups.
func groupArchitectures(handlers map[string]Handler) []string {
	var out []string
	for name, h := range handlers {
		if _, ok := h.(GroupTrainer); ok {
			out = append(out, name)
		}
	}
	slices.Sort(out)
	return out
}
