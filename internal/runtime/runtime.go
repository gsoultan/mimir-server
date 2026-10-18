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
	"github.com/gsoultan/mimir/io/mim"
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

// Build loads weights, a Mimir Model file of the parameters as mim.Save
// writes them, into a new module of architecture a shaped by spec, ready
// for inference. It fails with ErrBadWeights when the file does not match.
func Build(a arch.Architecture, spec arch.Spec, weights []byte) (nn.Module, error) {
	f, err := mim.Parse(weights)
	if err != nil {
		return nil, fmt.Errorf("%w: not a Mimir Model file: %v", ErrBadWeights, err)
	}
	defer f.Close()
	m := a.New(spec)
	if err := f.Load(m); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrBadWeights, err)
	}
	nn.Eval(m)
	return m, nil
}

// LabelsKey is the metadata key of a Mimir Model file that holds the
// labels of its outputs, as strings, one per output value.
const LabelsKey = "mimir.labels"

// File is a loaded Mimir Model (.mim) file, which runs as a model of one
// input.
type File struct {
	m     *mim.Model
	input mim.ValueInfo
	shape []int // the input's declared shape
	// Spec is what the file reads and answers: the shape of its input,
	// without a leading batch dimension of 1, and the labels its metadata
	// holds under LabelsKey, if any.
	Spec arch.Spec
}

// OpenFile loads a Mimir Model file. It fails with ErrBadWeights for a
// file that mimir cannot run, or that is not a model of one float input
// of a fixed shape with at least one output.
func OpenFile(b []byte) (*File, error) {
	m, err := mim.Parse(b)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrBadWeights, err)
	}
	f, err := newFile(m)
	if err != nil {
		m.Close()
		return nil, err
	}
	return f, nil
}

func newFile(m *mim.Model) (*File, error) {
	ins, outs := m.Inputs(), m.Outputs()
	if len(ins) != 1 || len(outs) == 0 {
		return nil, fmt.Errorf("%w: the model has %d inputs and %d outputs, want one input and an output", ErrBadWeights, len(ins), len(outs))
	}
	in := ins[0]
	if !in.DType().IsFloat() {
		return nil, fmt.Errorf("%w: input %s does not read numbers", ErrBadWeights, in)
	}
	shape := make([]int, len(in.Shape))
	for i, d := range in.Shape {
		switch {
		case d.Size >= 0:
			shape[i] = d.Size
		case i == 0 && len(in.Shape) > 1:
			// A free batch size, as most ONNX models declare: one
			// question at a time.
			shape[i] = 1
		default:
			return nil, fmt.Errorf("%w: input %s has a dimension of variable size", ErrBadWeights, in)
		}
	}
	md, err := m.Metadata()
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrBadWeights, err)
	}
	labels, _ := md[LabelsKey].([]string)
	spec := arch.Spec{InputShape: shape, Labels: labels}
	if len(shape) > 1 && shape[0] == 1 {
		spec.InputShape = shape[1:]
	}
	return &File{m: m, input: in, shape: shape, Spec: spec}, nil
}

// Forward runs the file on x, reshaped to its input's shape, and returns
// its first output.
func (f *File) Forward(ctx context.Context, x *tensor.Tensor) *tensor.Tensor {
	out, err := f.m.Run(ctx, map[string]*tensor.Tensor{f.input.Name: x.Reshape(f.shape...).Cast(f.input.DType())})
	if err != nil {
		return tensor.Failed(err)
	}
	return out[f.m.Outputs()[0].Name]
}

// Close releases the file.
func (f *File) Close() error { return f.m.Close() }

// Cache keeps loaded models in memory by model id. It is safe for
// concurrent use; mimir tensors are immutable, so one module serves many
// requests at once. Each entry remembers the version of the weights it
// loaded, so a model retrained in place is loaded again on its next use.
type Cache struct {
	mu     sync.Mutex
	loaded map[string]*entry
}

type entry struct {
	ver  string // the weights' version, such as their SHA-256
	once sync.Once
	run  func(ctx context.Context, x *tensor.Tensor) *tensor.Tensor
	lm   *LanguageModel // for a language model, instead of run
	err  error
}

// load builds the model of architecture a from weights, a weights file
// or, for a File architecture, a Mimir Model file.
func (e *entry) load(a arch.Architecture, spec arch.Spec, weights []byte) {
	if a.Answers() == arch.Written {
		e.lm, e.err = OpenLanguageModel(weights)
		return
	}
	if a.File {
		f, err := OpenFile(weights)
		if err != nil {
			e.err = err
			return
		}
		if !slices.Equal(f.Spec.InputShape, spec.InputShape) {
			f.Close()
			e.err = fmt.Errorf("%w: the file reads shape %v, the model %v", ErrBadWeights, f.Spec.InputShape, spec.InputShape)
			return
		}
		e.run = f.Forward
		return
	}
	mod, err := Build(a, spec, weights)
	if err != nil {
		e.err = err
		return
	}
	e.run = func(_ context.Context, x *tensor.Tensor) *tensor.Tensor { return mod.Forward(x) }
}

// NewCache returns an empty cache.
func NewCache() *Cache { return &Cache{loaded: map[string]*entry{}} }

// Predict runs input, shaped like spec's InputShape, through version ver
// of the model with id, loading it with load on first use.
func (c *Cache) Predict(ctx context.Context, id, ver string, a arch.Architecture, spec arch.Spec, load func() ([]byte, error), input []float32) (Result, error) {
	want := 1
	for _, d := range spec.InputShape {
		want *= d
	}
	if len(input) != want {
		return Result{}, fmt.Errorf("%w: got %d values, want %d (shape %v)", ErrBadInput, len(input), want, spec.InputShape)
	}
	e, err := c.ready(id, ver, a, spec, load)
	if err != nil {
		return Result{}, err
	}
	if e.run == nil {
		return Result{}, fmt.Errorf("%w: a language model writes text; send it a prompt", ErrBadInput)
	}
	shape := append([]int{1}, spec.InputShape...)
	logits := e.run(ctx, tensor.FromSlice(input, shape...)).Reshape(-1)
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

// Generate continues prompt with version ver of the language model with
// id, loading it with load on first use.
func (c *Cache) Generate(ctx context.Context, id, ver string, a arch.Architecture, load func() ([]byte, error), prompt string, s Sampling) (Generation, error) {
	e, err := c.ready(id, ver, a, arch.Spec{}, load)
	if err != nil {
		return Generation{}, err
	}
	if e.lm == nil {
		return Generation{}, fmt.Errorf("%w: only a language model writes text", ErrBadInput)
	}
	return e.lm.Generate(ctx, prompt, s)
}

// ready returns the entry of version ver of the model with id, loading it
// with load on first use.
func (c *Cache) ready(id, ver string, a arch.Architecture, spec arch.Spec, load func() ([]byte, error)) (*entry, error) {
	e := c.entry(id, ver)
	e.once.Do(func() {
		var w []byte
		if w, e.err = load(); e.err == nil {
			e.load(a, spec, w)
		}
	})
	if e.err != nil {
		c.forget(id, e) // let the next request try again
		return nil, e.err
	}
	return e, nil
}

// entry returns the entry for version ver of the model with id, replacing
// one that holds another version.
func (c *Cache) entry(id, ver string) *entry {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.loaded[id]
	if !ok || e.ver != ver {
		e = &entry{ver: ver}
		c.loaded[id] = e
	}
	return e
}

// forget drops e if it is still the entry for id.
func (c *Cache) forget(id string, e *entry) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.loaded[id] == e {
		delete(c.loaded, id)
	}
}

// Forget drops a model from memory, for example after it is deleted.
func (c *Cache) Forget(id string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.loaded, id)
}
