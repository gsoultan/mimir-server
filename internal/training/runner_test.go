package training

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gsoultan/mimir-server/internal/store"
	"github.com/gsoultan/mimir-server/internal/testdb"
)

// barrierRemote is a worker pool whose Train calls wait for each other:
// each returns only once want calls are in at the same time.
type barrierRemote struct {
	want    int32
	in      atomic.Int32
	peak    atomic.Int32
	all     chan struct{}
	once    sync.Once
	holdCtx bool // wait for ctx to end instead, like a long run cut short
}

func (b *barrierRemote) CanTrain(string, string) bool { return true }

func (b *barrierRemote) Train(ctx context.Context, jobID, workerID string, t Task, progress Progress) (Result, string, bool, error) {
	n := b.in.Add(1)
	defer b.in.Add(-1)
	for {
		p := b.peak.Load()
		if n <= p || b.peak.CompareAndSwap(p, n) {
			break
		}
	}
	if n >= b.want {
		b.once.Do(func() { close(b.all) })
	}
	select {
	case <-b.all:
	case <-time.After(10 * time.Second):
		return Result{}, "w1", true, errors.New("the other job never started")
	}
	if b.holdCtx {
		<-ctx.Done()
		return Result{}, "w1", true, context.Cause(ctx)
	}
	return Result{Weights: []byte("weights"), History: []store.Epoch{{Accuracy: 0.9}}}, "w1", true, nil
}

func newRunnerStore(t *testing.T) (*store.Store, []string) {
	t.Helper()
	ctx := context.Background()
	st, err := store.Open(ctx, testdb.URL(t), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	csv := "x,kind\n" + strings.Repeat("1,a\n2,b\n", 10)
	d, err := st.CreateDataset(ctx, store.Dataset{Name: "d", Kind: store.DatasetTable, Target: "kind"}, []byte(csv))
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, name := range []string{"one", "two"} {
		j, err := st.CreateJob(ctx, store.Job{Name: name, Architecture: "table-mlp", DatasetID: d.ID, Settings: TableDefaults})
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, j.ID)
	}
	return st, ids
}

func TestRunnerRunsJobsConcurrently(t *testing.T) {
	ctx := context.Background()
	st, ids := newRunnerStore(t)
	remote := &barrierRemote{want: 2, all: make(chan struct{})}
	r := New(st, slog.New(slog.NewTextHandler(io.Discard, nil)))
	r.Local, r.Remote, r.Jobs = false, remote, 2
	var done atomic.Int32
	r.OnDone = func(context.Context, store.Job) { done.Add(1) }

	r.Tick(ctx)
	if p := remote.peak.Load(); p != 2 {
		t.Fatalf("at most %d jobs ran at once, want 2", p)
	}
	for _, id := range ids {
		j, err := st.GetJob(ctx, id)
		if err != nil || j.Status != store.JobSucceeded || j.ModelID == "" {
			t.Fatalf("job %s: %+v, %v", id, j, err)
		}
	}
	if done.Load() != 2 {
		t.Fatalf("OnDone ran %d times", done.Load())
	}
}

func TestRunnerLimit(t *testing.T) {
	ctx := context.Background()
	st, ids := newRunnerStore(t)
	// The barrier is open, so each job finishes at once; with one slot they
	// still run one after the other.
	remote := &barrierRemote{want: 2, all: make(chan struct{})}
	close(remote.all)
	r := New(st, slog.New(slog.NewTextHandler(io.Discard, nil)))
	r.Local, r.Remote, r.Jobs = false, remote, 1
	r.Tick(ctx)
	if p := remote.peak.Load(); p != 1 {
		t.Fatalf("%d jobs ran at once with Jobs=1", p)
	}
	for _, id := range ids {
		if j, _ := st.GetJob(ctx, id); j.Status != store.JobSucceeded {
			t.Fatalf("job %s: %v", id, j.Status)
		}
	}
}

func TestRunnerShutdownWaitsAndRequeues(t *testing.T) {
	st, ids := newRunnerStore(t)
	remote := &barrierRemote{want: 2, all: make(chan struct{}), holdCtx: true}
	r := New(st, slog.New(slog.NewTextHandler(io.Discard, nil)))
	r.Local, r.Remote, r.Jobs = false, remote, 2

	ctx, cancel := context.WithCancel(context.Background())
	stopped := make(chan struct{})
	go func() {
		r.Run(ctx)
		close(stopped)
	}()
	select {
	case <-remote.all:
	case <-time.After(10 * time.Second):
		t.Fatal("the jobs never ran together")
	}
	cancel()
	select {
	case <-stopped:
	case <-time.After(10 * time.Second):
		t.Fatal("Run didn't return after shutdown")
	}
	if n := remote.in.Load(); n != 0 {
		t.Fatalf("Run returned with %d jobs still in flight", n)
	}
	for _, id := range ids {
		j, err := st.GetJob(context.Background(), id)
		if err != nil || j.Status != store.JobQueued {
			t.Fatalf("job %s after shutdown: %v, %v", id, j.Status, err)
		}
	}
}

// TestRunnerIdlesOnAnEmptyQueue runs the runner with nothing queued: it
// looks at the queue once and then waits for its ticker or a kick, rather
// than looking again and again.
func TestRunnerIdlesOnAnEmptyQueue(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	st, err := store.Open(ctx, testdb.URL(t), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	r := New(st, slog.New(slog.NewTextHandler(io.Discard, nil)))
	var looks atomic.Int32
	r.Now = func() time.Time { looks.Add(1); return time.Now() } // once per look at the queue
	done := make(chan struct{})
	go func() { r.Run(ctx); close(done) }()
	time.Sleep(time.Second)
	cancel()
	<-done
	if n := looks.Load(); n > 2 {
		t.Errorf("an idle runner looked at the queue %d times in a second", n)
	}
}
