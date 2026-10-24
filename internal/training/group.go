package training

import (
	"context"
	"fmt"
	"iter"

	"github.com/gsoultan/mimir/data"
	"github.com/gsoultan/mimir/nn"
	"github.com/gsoultan/mimir/tensor"
)

// Group is one run trained by several workers together. Each member
// learns from its own share of the batches; every few steps they average
// their weights, so they go on from the same model. Member 0 scores the
// model, reports progress and hands back the weights.
type Group interface {
	// Rank is this member's place in the group, from 0.
	Rank() int
	// Size is how many members the group has.
	Size() int
	// Mean returns the mean of every member's values, each weighed by
	// the weight its member gave. Every member calls it the same number
	// of times, with values of the same length.
	Mean(ctx context.Context, values []float32, weight float64) ([]float32, error)
}

// GroupSyncSteps is how many steps the members of a group take on their
// own between averaging their weights. They also average at the end of
// every epoch, so each epoch is scored on the model they share.
const GroupSyncSteps = 8

// grouped reports whether t is trained by more than one worker.
func (t Task) grouped() bool { return t.Group != nil && t.Group.Size() > 1 }

// syncer averages a model's weights over a group.
type syncer struct {
	g      Group
	params []*tensor.Param
}

func newSyncer(g Group, model nn.Module) *syncer {
	s := &syncer{g: g}
	for _, p := range nn.NamedParams(model) {
		s.params = append(s.params, p)
	}
	return s
}

// start gives every member member 0's weights, so they all start from the
// same model however each was made.
func (s *syncer) start(ctx context.Context) error {
	w := 0.0
	if s.g.Rank() == 0 {
		w = 1
	}
	return s.average(ctx, w)
}

// average replaces the model's weights with the group's mean, each member
// weighing w.
func (s *syncer) average(ctx context.Context, w float64) error {
	var flat []float32
	for _, p := range s.params {
		v, err := p.Value().Cast(tensor.Float32).Data[float32](ctx)
		if err != nil {
			return err
		}
		flat = append(flat, v...)
	}
	mean, err := s.g.Mean(ctx, flat, w)
	if err != nil {
		return err
	}
	if len(mean) != len(flat) {
		return fmt.Errorf("training: the group sent back %d weights for %d", len(mean), len(flat))
	}
	vals := make([]*tensor.Tensor, len(s.params))
	at := 0
	for i, p := range s.params {
		cur := p.Value()
		n := 1
		for _, d := range cur.Shape() {
			n *= d
		}
		vals[i] = tensor.FromSlice(mean[at:at+n:at+n], cur.Shape()...).To(cur.Device()).Cast(cur.DType())
		at += n
	}
	if err := tensor.Realize(ctx, vals...); err != nil {
		return err
	}
	for i, p := range s.params {
		if err := p.Set(vals[i]); err != nil {
			return err
		}
	}
	return nil
}

// shard is one member's share of a Set: every size-th batch, from batch
// rank. Members see the batches in the same order, so together they cover
// the epoch once. Each takes the same number of steps, as averaging needs:
// a member one batch short trains on its first batch again.
type shard struct {
	Set
	rank, size int
}

func shardOf(s Set, rank, size int) *shard { return &shard{Set: s, rank: rank, size: size} }

// Batches returns this member's steps in an epoch.
func (s *shard) Batches() int { return (s.Set.Batches() + s.size - 1) / s.size }

// SetEpoch passes the epoch on, so every member shuffles alike.
func (s *shard) SetEpoch(epoch int) {
	if e, ok := s.Set.(interface{ SetEpoch(int) }); ok {
		e.SetEpoch(epoch)
	}
}

func (s *shard) All(ctx context.Context) iter.Seq2[data.Batch, error] {
	return func(yield func(data.Batch, error) bool) {
		total := s.Set.Batches()
		want, n := s.Batches(), 0
		// spare is what a member short of batches trains on again: its
		// first, or for a member past the last batch, one of the others.
		spare := s.rank % max(total, 1)
		mine := func(b int) bool { return b%s.size == s.rank }
		all, place := s.Set.All, func(k int) int { return k }
		if e, ok := s.Set.(*examples); ok {
			// Build only the batches this member trains on; the k-th one
			// built is the epoch's batch kept[k].
			var kept []int
			for b := range total {
				if mine(b) || b == spare {
					kept = append(kept, b)
				}
			}
			all = func(ctx context.Context) iter.Seq2[data.Batch, error] {
				return e.only(ctx, func(b int) bool { return mine(b) || b == spare })
			}
			place = func(k int) int { return kept[k] }
		}
		var first data.Batch
		k := 0
		for b, err := range all(ctx) {
			if err != nil {
				yield(b, err)
				return
			}
			i := place(k)
			k++
			if i == spare {
				first = b
			}
			if mine(i) {
				if !yield(b, nil) {
					return
				}
				n++
			}
		}
		for ; n < want && first.X != nil; n++ {
			if !yield(first, nil) {
				return
			}
		}
	}
}
