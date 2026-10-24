package runtime

import (
	"context"
	"errors"
	"slices"
	"sync"
	"testing"
	"time"
)

// recorder is an Observer that keeps the batch sizes it is told of.
type recorder struct {
	mu    sync.Mutex
	sizes []int
}

func (r *recorder) ObserveBatch(_ string, size int, waited []time.Duration, _ time.Duration, _ bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(waited) != size {
		panic("a wait per question")
	}
	r.sizes = append(r.sizes, size)
}

func (r *recorder) ObserveLoad(string, time.Duration, bool) {}

// double answers each input of n with its values doubled, after gate lets
// it through.
func double(gate <-chan struct{}) rowsFunc {
	return func(ctx context.Context, x []float32, n int) ([][]float32, error) {
		if gate != nil {
			<-gate
		}
		out := make([]float32, len(x))
		for i, v := range x {
			out[i] = 2 * v
		}
		return split(out, n), nil
	}
}

func fixed(b Batching) func() Batching { return func() Batching { return b } }

// While a batch runs, the questions that come wait and then go through
// together, each with its own answer.
func TestBatcherBatchesQuestionsThatWait(t *testing.T) {
	gate := make(chan struct{})
	rec := &recorder{}
	b := newBatcher(double(gate), "test", 0, fixed(Batching{Max: 8}), func() Observer { return rec })
	b.runners = 1
	ctx := context.Background()
	var wg sync.WaitGroup
	ask := func(v float32) {
		wg.Go(func() {
			out, err := b.ask(ctx, []float32{v, v + 1})
			if err != nil || !slices.Equal(out, []float32{2 * v, 2*v + 2}) {
				t.Errorf("ask(%v) = %v, %v", v, out, err)
			}
		})
	}
	ask(0) // runs alone and holds the runner at the gate
	waitFor(t, func() bool { b.mu.Lock(); defer b.mu.Unlock(); return len(b.queue) == 0 && b.running == 1 })
	for i := 1; i <= 10; i++ {
		ask(float32(10 * i))
	}
	waitFor(t, func() bool { b.mu.Lock(); defer b.mu.Unlock(); return len(b.queue) == 10 })
	close(gate)
	wg.Wait()
	rec.mu.Lock()
	defer rec.mu.Unlock()
	if !slices.Equal(rec.sizes, []int{1, 8, 2}) {
		t.Fatalf("batch sizes %v, want [1 8 2]", rec.sizes)
	}
	waitFor(t, func() bool { b.mu.Lock(); defer b.mu.Unlock(); return b.running == 0 })
}

// A model whose batch dimension is fixed answers one question at a time.
func TestBatcherKeepsToTheModelsLimit(t *testing.T) {
	rec := &recorder{}
	gate := make(chan struct{})
	b := newBatcher(double(gate), "test", 1, fixed(Batching{Max: 8}), func() Observer { return rec })
	b.runners = 1
	var wg sync.WaitGroup
	for i := range 4 {
		wg.Go(func() {
			if _, err := b.ask(context.Background(), []float32{float32(i)}); err != nil {
				t.Error(err)
			}
		})
	}
	waitFor(t, func() bool { b.mu.Lock(); defer b.mu.Unlock(); return len(b.queue) == 3 })
	close(gate)
	wg.Wait()
	rec.mu.Lock()
	defer rec.mu.Unlock()
	if !slices.Equal(rec.sizes, []int{1, 1, 1, 1}) {
		t.Fatalf("batch sizes %v", rec.sizes)
	}
}

// A batch that fails runs again a question at a time, so only the
// question that fails gets an error.
func TestBatcherIsolatesAFailingQuestion(t *testing.T) {
	gate := make(chan struct{})
	run := func(ctx context.Context, x []float32, n int) ([][]float32, error) {
		<-gate
		if slices.Contains(x, -1) {
			return nil, errors.New("bad input")
		}
		return split(x, n), nil
	}
	b := newBatcher(run, "test", 0, fixed(Batching{Max: 8}), func() Observer { return nil })
	b.runners = 1
	var wg sync.WaitGroup
	errs := make([]error, 4)
	for i, v := range []float32{1, 2, -1, 3} {
		wg.Go(func() { _, errs[i] = b.ask(context.Background(), []float32{v}) })
		if i == 0 {
			waitFor(t, func() bool { b.mu.Lock(); defer b.mu.Unlock(); return b.running == 1 && len(b.queue) == 0 })
		}
	}
	waitFor(t, func() bool { b.mu.Lock(); defer b.mu.Unlock(); return len(b.queue) == 3 })
	close(gate)
	wg.Wait()
	for i, err := range errs {
		if (err != nil) != (i == 2) {
			t.Errorf("question %d: %v", i, err)
		}
	}
}

// A question whose asker gives up returns at once, and its run is
// cancelled when nobody else waits on it.
func TestBatcherCancel(t *testing.T) {
	started := make(chan struct{})
	run := func(ctx context.Context, x []float32, n int) ([][]float32, error) {
		close(started)
		<-ctx.Done()
		return nil, ctx.Err()
	}
	b := newBatcher(run, "test", 0, fixed(DefaultBatching), func() Observer { return nil })
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error)
	go func() {
		_, err := b.ask(ctx, []float32{1})
		done <- err
	}()
	<-started
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("got %v", err)
	}
	waitFor(t, func() bool { b.mu.Lock(); defer b.mu.Unlock(); return b.running == 0 })
}

// With Wait, a runner that finds fewer questions than Max waits for more.
func TestBatcherWaitFillsBatches(t *testing.T) {
	rec := &recorder{}
	b := newBatcher(double(nil), "test", 0, fixed(Batching{Max: 4, Wait: 100 * time.Millisecond}), func() Observer { return rec })
	b.runners = 1
	var wg sync.WaitGroup
	for i := range 4 {
		wg.Go(func() {
			if _, err := b.ask(context.Background(), []float32{float32(i)}); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	rec.mu.Lock()
	defer rec.mu.Unlock()
	if len(rec.sizes) == 4 {
		t.Fatalf("no batching while waiting: %v", rec.sizes)
	}
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	for deadline := time.Now().Add(5 * time.Second); !cond(); {
		if time.Now().After(deadline) {
			t.Fatal("timed out")
		}
		time.Sleep(time.Millisecond)
	}
}

// A model that panics on one input fails that question alone: the batch
// it was in runs again one question at a time, the others get their
// answers, and the model goes on answering.
func TestBatcherSurvivesAPanic(t *testing.T) {
	gate := make(chan struct{})
	ok := double(nil)
	run := func(ctx context.Context, x []float32, n int) ([][]float32, error) {
		<-gate
		for _, v := range x {
			if v == 13 {
				panic("unlucky")
			}
		}
		return ok(ctx, x, n)
	}
	b := newBatcher(run, "test", 0, fixed(Batching{Max: 8}), func() Observer { return nil })
	b.runners = 1
	ctx := context.Background()
	var wg sync.WaitGroup
	errs := make([]error, 4)
	for i, v := range []float32{1, 13, 2, 3} {
		wg.Go(func() {
			out, err := b.ask(ctx, []float32{v})
			if err == nil && out[0] != 2*v {
				t.Errorf("ask(%v) = %v", v, out)
			}
			errs[i] = err
		})
	}
	time.Sleep(50 * time.Millisecond) // let them queue into one batch
	close(gate)
	wg.Wait()
	for i, err := range errs {
		if (i == 1) != (err != nil) {
			t.Fatalf("question %d: %v; only the unlucky one should fail", i, err)
		}
	}
	if out, err := b.ask(ctx, []float32{4}); err != nil || out[0] != 8 {
		t.Fatalf("after the panic: %v, %v", out, err)
	}
}
