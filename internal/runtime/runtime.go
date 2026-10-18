// Package runtime loads stored models into mimir and runs them.
package runtime

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"

	"github.com/gsoultan/mimir-server/internal/arch"
	"github.com/gsoultan/mimir/io/safetensors"
	"github.com/gsoultan/mimir/nn"
	"github.com/gsoultan/mimir/tensor"
)

// ErrBadInput reports an input that does not fit the model.
var ErrBadInput = errors.New("runtime: input does not fit the model")

// ErrBadWeights reports a weights file that does not fit its architecture.
var ErrBadWeights = errors.New("runtime: weights do not fit the architecture")

// Guess is one label and its probability.
type Guess struct {
	Label       string
	Probability float32
}

// Result is a model's answer for one input.
type Result struct {
	Output      []float32 // raw output, one value per label
	Probability []float32 // softmax of Output
}

// Top returns the k most likely labels, most likely first.
func (r Result) Top(labels []string, k int) []Guess {
	gs := make([]Guess, len(r.Probability))
	for i, p := range r.Probability {
		label := fmt.Sprint(i)
		if i < len(labels) {
			label = labels[i]
		}
		gs[i] = Guess{Label: label, Probability: p}
	}
	slices.SortStableFunc(gs, func(a, b Guess) int { return cmp.Compare(b.Probability, a.Probability) })
	return gs[:min(k, len(gs))]
}

// Build loads weights into a new module of architecture a shaped by spec,
// ready for inference. It fails with ErrBadWeights when the file does not
// match.
func Build(a arch.Architecture, spec arch.Spec, weights []byte) (nn.Module, error) {
	f, err := safetensors.Parse(weights)
	if err != nil {
		return nil, fmt.Errorf("%w: not a safetensors file: %v", ErrBadWeights, err)
	}
	m := a.New(spec)
	if err := nn.Load(f, m); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrBadWeights, err)
	}
	nn.Eval(m)
	return m, nil
}

// Cache keeps loaded models in memory by model id. It is safe for
// concurrent use; mimir tensors are immutable, so one module serves many
// requests at once.
type Cache struct {
	mu     sync.Mutex
	loaded map[string]*entry
}

type entry struct {
	once sync.Once
	mod  nn.Module
	err  error
}

// NewCache returns an empty cache.
func NewCache() *Cache { return &Cache{loaded: map[string]*entry{}} }

// Predict runs input, shaped like spec's InputShape, through the model
// with id, loading it with load on first use.
func (c *Cache) Predict(ctx context.Context, id string, a arch.Architecture, spec arch.Spec, load func() ([]byte, error), input []float32) (Result, error) {
	want := 1
	for _, d := range spec.InputShape {
		want *= d
	}
	if len(input) != want {
		return Result{}, fmt.Errorf("%w: got %d values, want %d (shape %v)", ErrBadInput, len(input), want, spec.InputShape)
	}
	e := c.entry(id)
	e.once.Do(func() {
		var w []byte
		if w, e.err = load(); e.err == nil {
			e.mod, e.err = Build(a, spec, w)
		}
	})
	if e.err != nil {
		c.Forget(id) // let the next request try again
		return Result{}, e.err
	}
	shape := append([]int{1}, spec.InputShape...)
	logits := e.mod.Forward(tensor.FromSlice(input, shape...)).Reshape(-1)
	probs := logits.Softmax(-1)
	if err := tensor.Realize(ctx, logits, probs); err != nil { // one schedule for both
		return Result{}, fmt.Errorf("runtime: run model %s: %w", id, err)
	}
	out, err := logits.Data[float32](ctx)
	if err != nil {
		return Result{}, fmt.Errorf("runtime: run model %s: %w", id, err)
	}
	p, err := probs.Data[float32](ctx)
	if err != nil {
		return Result{}, fmt.Errorf("runtime: run model %s: %w", id, err)
	}
	return Result{Output: out, Probability: p}, nil
}

func (c *Cache) entry(id string) *entry {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.loaded[id]
	if !ok {
		e = &entry{}
		c.loaded[id] = e
	}
	return e
}

// Forget drops a model from memory, for example after it is deleted.
func (c *Cache) Forget(id string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.loaded, id)
}
