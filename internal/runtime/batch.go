package runtime

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"sync"
	"sync/atomic"
	"time"
)

// Batching says how predictions to one model are put together into
// batches, which run as one forward pass with a row per question.
//
// Questions queue per model and a few runners take them in turn: each
// takes every waiting question, up to Max, and runs them together. While
// the model is idle a question runs at once, alone; once questions come
// faster than the runners answer them, they wait for a runner and go
// through in batches, which a small model answers in little more time
// than one question. With Wait, a runner that found fewer than Max
// questions waits that long for more before it runs them, trading
// latency for bigger batches.
type Batching struct {
	Max  int           // the most questions in a batch; 1 turns batching off
	Wait time.Duration // how long a runner waits to fill a batch; 0 runs at once
}

// DefaultBatching batches up to 32 questions, without waiting for them.
var DefaultBatching = Batching{Max: 32}

var defaultBatching atomic.Pointer[Batching]

// SetDefaultBatching sets the batching of every cache that has none of its
// own (see Cache.SetBatching), such as from the server's flags.
func SetDefaultBatching(b Batching) { defaultBatching.Store(&b) }

// Observer is told about each batch a cache runs and each model it loads,
// for metrics. Its methods are called from many goroutines at once.
type Observer interface {
	// ObserveBatch reports a batch of size questions that engine
	// ("interpreter" or "mimir") ran in took, each question having waited
	// in the queue for the matching duration of waited.
	ObserveBatch(engine string, size int, waited []time.Duration, took time.Duration, failed bool)
	// ObserveLoad reports a model loaded in took.
	ObserveLoad(kind string, took time.Duration, failed bool)
}

// rowsFunc runs n inputs laid end to end in x, each of the model's input
// shape, and returns each one's raw outputs.
type rowsFunc func(ctx context.Context, x []float32, n int) ([][]float32, error)

// A batcher queues the questions to one loaded model and runs them in
// batches (see Batching).
type batcher struct {
	run     rowsFunc
	engine  string
	most    int // the most rows run accepts at once, 0 for any number
	opts    func() Batching
	obs     func() Observer
	runners int // the most batches running at once

	mu      sync.Mutex
	queue   []*question
	running int // runners going
}

// A question is one input waiting for its answer.
type question struct {
	ctx    context.Context
	x      []float32
	queued time.Time
	done   chan answer // buffered, so a runner never waits on the asker
}

type answer struct {
	out []float32
	err error
}

func newBatcher(run rowsFunc, engine string, most int, opts func() Batching, obs func() Observer) *batcher {
	return &batcher{run: run, engine: engine, most: most, opts: opts, obs: obs, runners: max(1, runtime.GOMAXPROCS(0))}
}

// ask queues x and returns its outputs once a batch with it has run, or
// ctx's error if ctx ends first.
func (b *batcher) ask(ctx context.Context, x []float32) ([]float32, error) {
	q := &question{ctx: ctx, x: x, queued: time.Now(), done: make(chan answer, 1)}
	b.mu.Lock()
	b.queue = append(b.queue, q)
	if b.running < b.runners {
		b.running++
		go b.work()
	}
	b.mu.Unlock()
	select {
	case a := <-q.done:
		return a.out, a.err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// work runs batches until the queue is empty, then stops; ask starts a
// runner again when questions come. A model nobody asks keeps no
// goroutine.
func (b *batcher) work() {
	for {
		batch := b.take()
		if batch == nil {
			return
		}
		b.answer(batch)
	}
}

// limit is the most questions in one batch.
func (b *batcher) limit() int {
	n := max(1, b.opts().Max)
	if b.most > 0 {
		n = min(n, b.most)
	}
	return n
}

// take removes the next batch from the queue, waiting for more questions
// as Batching.Wait says, or returns nil and counts the runner out when
// there are none. Questions whose asker has gone are dropped.
func (b *batcher) take() []*question {
	limit := b.limit()
	b.mu.Lock()
	batch := b.pop(limit)
	if len(batch) == 0 {
		b.running--
		b.mu.Unlock()
		return nil
	}
	b.mu.Unlock()
	if wait := b.opts().Wait; wait > 0 && len(batch) < limit {
		time.Sleep(wait)
		b.mu.Lock()
		batch = append(batch, b.pop(limit-len(batch))...)
		b.mu.Unlock()
	}
	return batch
}

// pop takes up to n live questions from the front of the queue. b.mu
// must be held.
func (b *batcher) pop(n int) []*question {
	var batch []*question
	for len(b.queue) > 0 && len(batch) < n {
		q := b.queue[0]
		b.queue[0] = nil
		b.queue = b.queue[1:]
		if q.ctx.Err() == nil {
			batch = append(batch, q)
		}
	}
	if len(b.queue) == 0 {
		b.queue = nil // let the backing array go
	}
	return batch
}

// answer runs batch and gives each question its outputs. A batch of
// several that fails runs again one question at a time, so that one bad
// question fails alone.
func (b *batcher) answer(batch []*question) {
	start := time.Now()
	waited := make([]time.Duration, len(batch))
	for i, q := range batch {
		waited[i] = start.Sub(q.queued)
	}
	outs, err := b.runBatch(batch)
	if o := b.obs(); o != nil {
		o.ObserveBatch(b.engine, len(batch), waited, time.Since(start), err != nil)
	}
	if err != nil && len(batch) > 1 {
		for _, q := range batch {
			b.answer([]*question{q})
		}
		return
	}
	for i, q := range batch {
		if err != nil {
			q.done <- answer{err: err}
		} else {
			q.done <- answer{out: outs[i]}
		}
	}
}

// runBatch runs the inputs of batch as one call of b.run. Its context
// ends once every asker's has.
func (b *batcher) runBatch(batch []*question) ([][]float32, error) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var left atomic.Int32
	left.Store(int32(len(batch)))
	for _, q := range batch {
		stop := context.AfterFunc(q.ctx, func() {
			if left.Add(-1) == 0 {
				cancel()
			}
		})
		defer stop()
	}
	x := batch[0].x
	if len(batch) > 1 {
		x = make([]float32, 0, len(batch)*len(batch[0].x))
		for _, q := range batch {
			x = append(x, q.x...)
		}
	}
	outs, err := b.run(ctx, x, len(batch))
	if err == nil && len(outs) != len(batch) {
		err = fmt.Errorf("runtime: %d answers for %d questions", len(outs), len(batch))
	}
	if errors.Is(err, context.Canceled) && len(batch) == 1 && batch[0].ctx.Err() != nil {
		err = batch[0].ctx.Err()
	}
	return outs, err
}
