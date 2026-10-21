// Package runtime loads stored models into mimir and runs them.
package runtime

import (
	"cmp"
	"container/list"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	mimfile "github.com/gsoultan/mimir-model"
	"github.com/gsoultan/mimir-model/interp"
	"github.com/gsoultan/mimir-model/ref"
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
	if a.Designable {
		// A designed model carries its layers in the file.
		md, err := f.Metadata()
		if err != nil {
			return nil, fmt.Errorf("%w: %v", ErrBadWeights, err)
		}
		s, _ := md[arch.LayersKey].(string)
		if spec.Layers, err = arch.ParseLayers(s); err != nil {
			return nil, fmt.Errorf("%w: %v", ErrBadWeights, err)
		}
	}
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
//
// On the CPU it runs on mimir-model's interpreter, whose kernels and
// low cost per op suit the small networks imported from ONNX; on a GPU,
// or for a file the interpreter cannot run, it runs on mimir.
type File struct {
	m     *mim.Model
	input mim.ValueInfo
	shape []int // the input's declared shape
	// batch is the most inputs one run takes: the bound of a free
	// leading dimension, 0 for no bound, or 1 when the leading dimension
	// is fixed.
	batch int
	prog  *interp.Program // nil when the interpreter cannot run the file
	// noInterp is set once the interpreter has failed a run for want of
	// an op, after which mimir runs the file.
	noInterp atomic.Bool
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
	f.prog = interpreter(b, f)
	return f, nil
}

// interpreter loads b on mimir-model's interpreter for f, or returns nil
// when it should not run there: a float32 input and first output are all
// it takes.
func interpreter(b []byte, f *File) *interp.Program {
	if f.input.Type != mimfile.F32 || f.m.Outputs()[0].Type != mimfile.F32 {
		return nil
	}
	mf, err := mimfile.Parse(b)
	if err != nil {
		return nil
	}
	p, err := interp.Load(mf)
	if err != nil {
		return nil
	}
	p.FastPaths = true
	return p
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
	batch := 1
	for i, d := range in.Shape {
		switch {
		case d.Size >= 0:
			shape[i] = d.Size
		case i == 0 && len(in.Shape) > 1:
			// A free batch size, as most ONNX models declare: one
			// question, or a batch of them.
			shape[i] = 1
			batch = max(d.Bound, 0)
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
	return &File{m: m, input: in, shape: shape, batch: batch, Spec: spec}, nil
}

// onInterpreter reports whether the file's runs go to the interpreter.
func (f *File) onInterpreter() bool {
	return f.prog != nil && !f.noInterp.Load() && tensor.DefaultDevice().Name() == "cpu"
}

// Engine names what runs the file: "interpreter" or "mimir".
func (f *File) Engine() string {
	if f.onInterpreter() {
		return "interpreter"
	}
	return "mimir"
}

// Run computes the file's first output for n inputs laid end to end in x,
// each of Spec.InputShape, and returns each input's. Unless the file's
// batch dimension is free, n must be 1.
func (f *File) Run(ctx context.Context, x []float32, n int) ([][]float32, error) {
	if n != 1 && (f.batch == 1 || f.batch > 0 && n > f.batch) {
		return nil, fmt.Errorf("%w: the file takes %d inputs at once, not %d", ErrBadInput, f.batch, n)
	}
	shape := slices.Clone(f.shape)
	if n != 1 {
		shape[0] = n
	}
	if f.onInterpreter() {
		out, err := f.runInterpreter(ctx, x, shape)
		if err == nil || ctx.Err() != nil || errors.Is(err, ErrBadInput) {
			return split(out, n), err
		}
		// An op the interpreter does not have: mimir runs this file from
		// now on.
		f.noInterp.Store(true)
	}
	out, err := f.forward(ctx, tensor.FromSlice(x, shape...)).Reshape(-1).Data[float32](ctx)
	if err != nil {
		return nil, err
	}
	return split(out, n), nil
}

func (f *File) runInterpreter(ctx context.Context, x []float32, shape []int) ([]float32, error) {
	in := &ref.Tensor{DType: mimfile.F32, Shape: shape, Data: make([]byte, 4*len(x))}
	for i, v := range x {
		binary.LittleEndian.PutUint32(in.Data[4*i:], math.Float32bits(v))
	}
	outs, err := f.prog.NewSession().RunContext(ctx, map[string]*ref.Tensor{f.input.Name: in})
	if err != nil {
		return nil, err
	}
	t := outs[f.m.Outputs()[0].Name]
	if t == nil || t.DType != mimfile.F32 {
		return nil, fmt.Errorf("runtime: the interpreter gave no float32 output")
	}
	out := make([]float32, len(t.Data)/4)
	for i := range out {
		out[i] = math.Float32frombits(binary.LittleEndian.Uint32(t.Data[4*i:]))
	}
	return out, nil
}

// split cuts out into n rows of equal length; nil when it does not divide.
func split(out []float32, n int) [][]float32 {
	if n < 1 || len(out)%n != 0 {
		return nil
	}
	rows := make([][]float32, n)
	w := len(out) / n
	for i := range rows {
		rows[i] = out[i*w : (i+1)*w : (i+1)*w]
	}
	return rows
}

// Forward runs the file on x, reshaped to its input's shape, and returns
// its first output. Each call runs in a session of its own, so questions
// to one model are answered at once rather than one after another, and
// none sees the variables another left.
func (f *File) Forward(ctx context.Context, x *tensor.Tensor) *tensor.Tensor {
	return f.forward(ctx, x.Reshape(f.shape...))
}

// forward runs the file on mimir with x as its input.
func (f *File) forward(ctx context.Context, x *tensor.Tensor) *tensor.Tensor {
	out, err := f.m.NewSession().Run(ctx, map[string]*tensor.Tensor{f.input.Name: x.Cast(f.input.DType())})
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
//
// The cache holds at most its limit in bytes, counting each model by the
// size of the file it was loaded from, and drops the least recently used
// models to stay under it. The model loaded last always stays, even when
// it alone is over the limit.
type Cache struct {
	mu     sync.Mutex
	loaded map[string]*entry
	lru    list.List // loaded entries, most recently used first
	used   int64     // bytes of the entries in lru
	limit  int64     // 0 means no limit

	batching atomic.Pointer[Batching]
	observer atomic.Pointer[Observer]
}

// SetBatching changes how predictions are batched (see Batching), for
// models loaded and loading alike.
func (c *Cache) SetBatching(b Batching) { c.batching.Store(&b) }

func (c *Cache) batchingNow() Batching {
	if b := c.batching.Load(); b != nil {
		return *b
	}
	if b := defaultBatching.Load(); b != nil {
		return *b
	}
	return DefaultBatching
}

// SetObserver makes o the observer of the cache's batches and loads.
func (c *Cache) SetObserver(o Observer) { c.observer.Store(&o) }

func (c *Cache) observerNow() Observer {
	if o := c.observer.Load(); o != nil {
		return *o
	}
	return nil
}

type entry struct {
	id   string
	ver  string // the weights' version, such as their SHA-256
	once  sync.Once
	batch *batcher       // runs the model's predictions
	lm    *LanguageModel // for a language model, instead of batch
	kind  string         // "language", "file" or "built", for metrics
	err  error
	size int64         // bytes of the file it was loaded from
	elem *list.Element // its place in lru, once loaded; guarded by Cache.mu
}

// load builds the model of architecture a from weights, a weights file
// or, for a File architecture, a Mimir Model file.
func (e *entry) load(c *Cache, a arch.Architecture, spec arch.Spec, weights []byte) {
	if a.Answers() == arch.Written {
		e.kind = "language"
		e.lm, e.err = OpenLanguageModel(weights)
		return
	}
	if a.File {
		e.kind = "file"
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
		e.batch = newBatcher(f.Run, f.Engine(), f.batch, c.batchingNow, c.observerNow)
		return
	}
	e.kind = "built"
	mod, err := Build(a, spec, weights)
	if err != nil {
		e.err = err
		return
	}
	run := func(ctx context.Context, x []float32, n int) ([][]float32, error) {
		shape := append([]int{n}, spec.InputShape...)
		out, err := mod.Forward(tensor.FromSlice(x, shape...)).Reshape(-1).Data[float32](ctx)
		if err != nil {
			return nil, err
		}
		return split(out, n), nil
	}
	e.batch = newBatcher(run, "mimir", 0, c.batchingNow, c.observerNow)
}

// NewCache returns an empty cache that holds at most limit bytes of
// models; 0 means no limit.
func NewCache(limit int64) *Cache { return &Cache{loaded: map[string]*entry{}, limit: max(limit, 0)} }

// SetLimit changes how many bytes of models the cache holds, dropping the
// least recently used ones now if it holds more; 0 means no limit.
func (c *Cache) SetLimit(limit int64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.limit = max(limit, 0)
	c.trim(nil)
}

// Used returns the bytes of models the cache holds and how many models.
func (c *Cache) Used() (bytes int64, models int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.used, c.lru.Len()
}

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
	if e.batch == nil {
		return Result{}, fmt.Errorf("%w: a language model writes text; send it a prompt", ErrBadInput)
	}
	out, err := e.batch.ask(ctx, input)
	if err != nil {
		return Result{}, fmt.Errorf("runtime: run model %s: %w", id, err)
	}
	return Result{Output: out, Probability: softmax(out)}, nil
}

// softmax returns the softmax of v, computed in float64.
func softmax(v []float32) []float32 {
	m := math.Inf(-1)
	for _, x := range v {
		m = math.Max(m, float64(x))
	}
	e := make([]float64, len(v))
	var s float64
	for i, x := range v {
		e[i] = math.Exp(float64(x) - m)
		s += e[i]
	}
	p := make([]float32, len(v))
	for i := range p {
		p[i] = float32(e[i] / s)
	}
	return p
}

// Load loads version ver of the model with id ahead of its first
// prediction, so the caller can let the weights go.
func (c *Cache) Load(id, ver string, a arch.Architecture, spec arch.Spec, load func() ([]byte, error)) error {
	_, err := c.ready(id, ver, a, spec, load)
	return err
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

// Chat writes the next turn of a chat with version ver of the language
// model with id, loading it with load on first use.
func (c *Cache) Chat(ctx context.Context, id, ver string, a arch.Architecture, load func() ([]byte, error), msgs []Message, s Sampling) (Generation, error) {
	e, err := c.ready(id, ver, a, arch.Spec{}, load)
	if err != nil {
		return Generation{}, err
	}
	if e.lm == nil {
		return Generation{}, fmt.Errorf("%w: only a language model chats", ErrBadInput)
	}
	return e.lm.Chat(ctx, msgs, s)
}

// ready returns the entry of version ver of the model with id, loading it
// with load on first use.
func (c *Cache) ready(id, ver string, a arch.Architecture, spec arch.Spec, load func() ([]byte, error)) (*entry, error) {
	return c.get(id, ver, load, func(e *entry, w []byte) {
		start := time.Now()
		e.load(c, a, spec, w)
		if o := c.observerNow(); o != nil {
			o.ObserveLoad(e.kind, time.Since(start), e.err != nil)
		}
	})
}

// get returns the entry of version ver of the model with id, building it
// from the bytes load returns on first use, and marks it most recently
// used.
func (c *Cache) get(id, ver string, load func() ([]byte, error), build func(e *entry, w []byte)) (*entry, error) {
	e := c.entry(id, ver)
	e.once.Do(func() {
		var w []byte
		if w, e.err = load(); e.err == nil {
			build(e, w)
			e.size = int64(len(w))
		}
	})
	if e.err != nil {
		c.forget(id, e) // let the next request try again
		return nil, e.err
	}
	c.touch(e)
	return e, nil
}

// entry returns the entry for version ver of the model with id, replacing
// one that holds another version.
func (c *Cache) entry(id, ver string) *entry {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.loaded[id]
	if !ok || e.ver != ver {
		if ok {
			c.unlist(e)
		}
		e = &entry{id: id, ver: ver}
		c.loaded[id] = e
	}
	return e
}

// touch marks a loaded entry most recently used, counting its bytes the
// first time, and drops others to keep under the limit. An entry that was
// replaced or forgotten while it loaded serves its request and goes.
func (c *Cache) touch(e *entry) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.loaded[e.id] != e {
		return
	}
	if e.elem != nil {
		c.lru.MoveToFront(e.elem)
		return
	}
	e.elem = c.lru.PushFront(e)
	c.used += e.size
	c.trim(e)
}

// trim drops the least recently used entries, but never keep, until the
// cache is under its limit. c.mu must be held.
func (c *Cache) trim(keep *entry) {
	for c.limit > 0 && c.used > c.limit {
		back := c.lru.Back()
		if back == nil {
			return
		}
		old := back.Value.(*entry)
		if old == keep {
			return // keep is all that is left
		}
		c.unlist(old)
		if c.loaded[old.id] == old {
			delete(c.loaded, old.id)
		}
	}
}

// unlist takes e out of the LRU list and its bytes off the total. c.mu
// must be held.
func (c *Cache) unlist(e *entry) {
	if e.elem == nil {
		return
	}
	c.lru.Remove(e.elem)
	e.elem = nil
	c.used -= e.size
}

// forget drops e if it is still the entry for id.
func (c *Cache) forget(id string, e *entry) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.loaded[id] == e {
		delete(c.loaded, id)
		c.unlist(e)
	}
}

// Forget drops a model from memory, for example after it is deleted.
func (c *Cache) Forget(id string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if e, ok := c.loaded[id]; ok {
		delete(c.loaded, id)
		c.unlist(e)
	}
}
