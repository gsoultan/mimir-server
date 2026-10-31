package runtime

import (
	"context"
	"sync"

	"github.com/gsoultan/mimir/nn"
	"github.com/gsoultan/mimir/tensor"
)

// A built model answers the same question shape over and over, so its
// forward pass is recorded once for each batch size and replayed (see
// tensor.Record): a replay runs the program the first run compiled, over
// the new input, without building or planning the graph again. On a
// small model building the graph costs more than computing it.

// maxRecorded is the most batch sizes a model keeps a recording of; the
// batcher makes runs of few sizes.
const maxRecorded = 8

// replayer runs a module's forward pass, replaying a recording of it for
// each batch size it has seen.
type replayer struct {
	mod   nn.Module
	shape []int // the input's shape, without the batch dimension

	mu   sync.Mutex
	recs map[int]*tensor.Recording
}

func newReplayer(mod nn.Module, shape []int) *replayer {
	return &replayer{mod: mod, shape: shape, recs: map[int]*tensor.Recording{}}
}

// run computes the outputs of n inputs, x, as a batcher does.
func (r *replayer) run(ctx context.Context, x []float32, n int) ([][]float32, error) {
	in := tensor.FromSlice(x, append([]int{n}, r.shape...)...)
	r.mu.Lock()
	rec, seen := r.recs[n]
	r.mu.Unlock()
	if rec != nil {
		out, ok, err := rec.Replay(ctx, []*tensor.Tensor{in})
		if err != nil {
			return nil, err
		}
		if ok {
			return r.data(ctx, out[0], n)
		}
	}
	y := r.mod.Forward(in).Reshape(-1)
	if !seen {
		// Recording computes y too. A nil recording, for a pass that
		// cannot be replayed, is kept so that it is not tried again.
		rec, err := tensor.Record(ctx, []*tensor.Tensor{in}, []*tensor.Tensor{y})
		if err != nil {
			return nil, err
		}
		r.mu.Lock()
		if len(r.recs) < maxRecorded {
			r.recs[n] = rec
		}
		r.mu.Unlock()
	}
	return r.data(ctx, y, n)
}

func (r *replayer) data(ctx context.Context, y *tensor.Tensor, n int) ([][]float32, error) {
	out, err := y.Data[float32](ctx)
	if err != nil {
		return nil, err
	}
	return split(out, n), nil
}
