package training

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gsoultan/mimir-server/internal/arch"
	"github.com/gsoultan/mimir-server/internal/memguard"
	"github.com/gsoultan/mimir-server/internal/store"
	"github.com/gsoultan/mimir/tensor"
)

// fakeMemory is a memory guard whose use the test sets.
type fakeMemory struct {
	used atomic.Int64
	g    *memguard.Guard
}

func newFakeMemory() *fakeMemory {
	f := &fakeMemory{}
	f.g = memguard.New(memguard.Config{Limit: 1000, Read: f.used.Load, Log: slog.New(slog.NewTextHandler(io.Discard, nil)), KeepRuntimeLimit: true})
	return f
}

// set makes the guard measure used bytes now.
func (f *fakeMemory) set(used int64) memguard.Level {
	f.used.Store(used)
	return f.g.Measure()
}

func trained(context.Context, Task, Progress) (Result, error) {
	return Result{Weights: []byte("weights"), History: []store.Epoch{{Epoch: 1, Accuracy: 0.9}}}, nil
}

func statuses(t *testing.T, st *store.Store, ids []string) []store.JobStatus {
	t.Helper()
	var out []store.JobStatus
	for _, id := range ids {
		j, err := st.GetJob(context.Background(), id)
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, j.Status)
	}
	return out
}

// A run that panics fails alone: the runner goes on to the next one and
// the process stays up.
func TestRunnerSurvivesAPanickingRun(t *testing.T) {
	st, ids := newRunnerStore(t)
	r := New(st, slog.New(slog.NewTextHandler(io.Discard, nil)))
	var calls atomic.Int32
	r.fit = func(ctx context.Context, task Task, p Progress) (Result, error) {
		if calls.Add(1) == 1 {
			var m map[string]int
			m["boom"]++ // a nil map: a real bug, not a planted panic call
		}
		return trained(ctx, task, p)
	}
	r.Tick(context.Background())
	var failed, succeeded int
	for _, id := range ids {
		j, _ := st.GetJob(context.Background(), id)
		switch j.Status {
		case store.JobFailed:
			failed++
			if !strings.Contains(j.Error, "internal error") {
				t.Errorf("the failed run says %q", j.Error)
			}
		case store.JobSucceeded:
			succeeded++
		}
	}
	if failed != 1 || succeeded != 1 {
		t.Fatalf("%d failed and %d succeeded, want one each", failed, succeeded)
	}
}

// While memory is high nothing starts training here; the runs wait in
// the queue and train once memory is back.
func TestRunnerWaitsForMemory(t *testing.T) {
	st, ids := newRunnerStore(t)
	mem := newFakeMemory()
	r := New(st, slog.New(slog.NewTextHandler(io.Discard, nil)))
	r.WatchMemory(mem.g)
	var calls atomic.Int32
	r.fit = func(ctx context.Context, task Task, p Progress) (Result, error) {
		calls.Add(1)
		return trained(ctx, task, p)
	}
	if mem.set(850) != memguard.High {
		t.Fatal("not high")
	}
	r.Tick(context.Background())
	if calls.Load() != 0 {
		t.Fatalf("%d runs trained while memory was high", calls.Load())
	}
	for i, s := range statuses(t, st, ids) {
		if s != store.JobQueued {
			t.Fatalf("job %d is %v while memory is high, want queued", i, s)
		}
	}
	mem.set(100)
	r.Tick(context.Background())
	for i, s := range statuses(t, st, ids) {
		if s != store.JobSucceeded {
			t.Fatalf("job %d is %v once memory is back, want succeeded", i, s)
		}
	}
}

// When memory stays critical a run training here stops and goes back in
// the queue, and a run stopped MaxLowMemoryStops times fails with a
// reason instead of starting over for ever.
func TestRunnerStopsRunsWhenMemoryIsCritical(t *testing.T) {
	st, ids := newRunnerStore(t)
	ctx := context.Background()
	// One run is enough here; the other is cancelled out of the way.
	st.SetJobStatus(ctx, ids[1], store.JobCancelled, "", "")
	mem := newFakeMemory()
	r := New(st, slog.New(slog.NewTextHandler(io.Discard, nil)))
	r.StopAfter = 50 * time.Millisecond
	r.WatchMemory(mem.g)
	started := make(chan struct{})
	r.fit = func(ctx context.Context, task Task, p Progress) (Result, error) {
		started <- struct{}{}
		<-ctx.Done() // a long run, until something stops it
		return Result{}, context.Cause(ctx)
	}
	for stop := 1; stop <= MaxLowMemoryStops; stop++ {
		mem.set(100)
		r.mu.Lock()
		r.pausedUntil = time.Time{} // skip the pause after a stop
		clear(r.cooling)
		r.mu.Unlock()
		done := make(chan struct{})
		go func() { r.Tick(ctx); close(done) }()
		select {
		case <-started:
		case <-time.After(10 * time.Second):
			t.Fatalf("stop %d: the run never started", stop)
		}
		if mem.set(950) != memguard.Critical {
			t.Fatal("not critical")
		}
		<-done
		j, _ := st.GetJob(ctx, ids[0])
		want := store.JobQueued
		if stop == MaxLowMemoryStops {
			want = store.JobFailed
		}
		if j.Status != want {
			t.Fatalf("after stop %d: %v (%q), want %v", stop, j.Status, j.Error, want)
		}
		if want == store.JobFailed && !strings.Contains(j.Error, "more memory") {
			t.Fatalf("the failed run says %q", j.Error)
		}
	}
	// Right after a stop, nothing new trains here for a while.
	mem.set(100)
	if r.localReady() {
		t.Fatal("ready to train right after a run was stopped for memory")
	}
}

// A run a worker hands back for memory goes back in the queue.
func TestRunnerRequeuesRunsAWorkerHandsBack(t *testing.T) {
	st, ids := newRunnerStore(t)
	r := New(st, slog.New(slog.NewTextHandler(io.Discard, nil)))
	r.Local = false
	r.Remote = handBack{}
	r.Tick(context.Background())
	for i, s := range statuses(t, st, ids) {
		if s != store.JobQueued {
			j, _ := st.GetJob(context.Background(), ids[i])
			t.Fatalf("job %d is %v (%q) after its worker ran low on memory, want queued", i, s, j.Error)
		}
	}
}

type handBack struct{}

func (handBack) CanTrain(string, string) bool { return true }

func (handBack) Train(context.Context, string, string, Task, Progress) (Result, string, bool, error) {
	return Result{}, "w1", true, errors.Join(ErrLowMemory, errors.New("on w1"))
}

// Memory that turns critical and eases again within StopAfter leaves the
// run training: it keeps going and finishes.
func TestRunnerKeepsTrainingThroughABriefCrunch(t *testing.T) {
	st, ids := newRunnerStore(t)
	ctx := context.Background()
	st.SetJobStatus(ctx, ids[1], store.JobCancelled, "", "")
	mem := newFakeMemory()
	r := New(st, slog.New(slog.NewTextHandler(io.Discard, nil)))
	r.StopAfter = 300 * time.Millisecond
	r.WatchMemory(mem.g)
	started, finish := make(chan struct{}), make(chan struct{})
	r.fit = func(ctx context.Context, task Task, p Progress) (Result, error) {
		close(started)
		select {
		case <-finish:
			return trained(ctx, task, p)
		case <-ctx.Done():
			return Result{}, context.Cause(ctx)
		}
	}
	done := make(chan struct{})
	go func() { r.Tick(ctx); close(done) }()
	<-started
	mem.set(950) // critical
	time.Sleep(100 * time.Millisecond)
	mem.set(100) // eased before StopAfter
	time.Sleep(400 * time.Millisecond)
	close(finish)
	<-done
	if s := statuses(t, st, ids[:1]); s[0] != store.JobSucceeded {
		t.Fatalf("a run through a brief crunch is %v, want succeeded", s[0])
	}
}

// Under a compute budget too small for a whole batch, as a critical level
// sets one, a run still trains: its batches go through in smaller pieces.
func TestFitTrainsUnderATightComputeBudget(t *testing.T) {
	var b strings.Builder
	b.WriteString("x,y,kind\n")
	for i := range 8000 {
		x, y := float64(i%50)/50, float64(i%37)/37
		kind := "a"
		if x+y > 1 {
			kind = "b"
		}
		fmt.Fprintf(&b, "%.3f,%.3f,%s\n", x, y, kind)
	}
	a, _ := arch.Lookup("table-mlp")
	dev := tensor.DefaultDevice()
	prev := tensor.MemoryBudget(dev)
	tensor.SetMemoryBudget(dev, 8<<20) // 8 MiB: a batch of 4096 rows doesn't fit in one piece
	defer tensor.SetMemoryBudget(dev, prev)
	s := TableDefaults
	s.Epochs, s.BatchSize = 2, 4096
	res, err := Fit(context.Background(), Task{Arch: a, Source: Source{Kind: store.DatasetTable, CSV: []byte(b.String())}, Settings: s}, nil)
	if err != nil {
		t.Fatalf("training under a tight budget: %v", err)
	}
	if len(res.History) != 2 {
		t.Fatalf("trained %d epochs, want 2", len(res.History))
	}
}
