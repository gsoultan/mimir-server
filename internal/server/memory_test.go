package server_test

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"runtime"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"

	mimirv1 "github.com/gsoultan/mimir-server/gen/mimir/v1"
	"github.com/gsoultan/mimir-server/internal/api"
	"github.com/gsoultan/mimir-server/internal/memguard"
	builtin "github.com/gsoultan/mimir-server/internal/worker"
	"github.com/gsoultan/mimir-server/worker"
)

// ballastBytes is how much memory the tests below take on top of a
// budget set budgetHeadroom over what the process uses at the start: well
// past the budget, whatever else the test process holds.
const (
	ballastBytes   = 384 << 20
	budgetHeadroom = 256 << 20
)

// hold takes n bytes of live memory, touching every page so the system
// really gives it, and returns a function that lets it go.
func hold(n int) func() {
	b := make([]byte, n)
	for i := 0; i < len(b); i += 4096 {
		b[i] = 1
	}
	runtime.GC()
	return func() {
		runtime.KeepAlive(b)
		b = nil
		runtime.GC()
	}
}

var quietLog = slog.New(slog.NewTextHandler(io.Discard, nil))

// TestConsoleStaysUpPastItsMemoryBudget takes the console's memory well
// past its budget. It stays up: health checks pass, the model it had
// loaded keeps answering, and only what would take more memory (loading
// another model, an upload read into memory) is turned away, with a
// message to try again. Once the memory is let go, all of it works again.
func TestConsoleStaysUpPastItsMemoryBudget(t *testing.T) {
	runtime.GC()
	var guard *memguard.Guard
	srv := startServerWith(t, false, func(s *api.Service) {
		guard = memguard.New(memguard.Config{Limit: memguard.InUse() + budgetHeadroom, Log: quietLog, Name: "console", KeepRuntimeLimit: true})
		s.WatchMemory(guard)
	})
	ctx := context.Background()
	own := owner(t, srv)
	input := &mimirv1.Tensor{Values: make([]float32, 784)}
	a := createModel(t, own, "A", "mnist-mlp")
	b := createModel(t, own, "B", "mnist-mlp")
	c := createModel(t, own, "C", "mnist-mlp")
	for _, m := range []*mimirv1.Model{b, c, a} { // a used last
		if _, err := own.predict.Predict(ctx, connect.NewRequest(&mimirv1.PredictRequest{ModelId: m.GetId(), Input: input})); err != nil {
			t.Fatal(err)
		}
	}
	if _, n := svc.ModelCacheUsage(); n != 3 {
		t.Fatalf("%d models loaded, want 3", n)
	}
	if guard.Measure() != memguard.OK {
		t.Fatalf("memory at the start: %v (%s of %s)", guard.Level(), memguard.Format(guard.Used()), memguard.Format(guard.Limit()))
	}

	release := hold(ballastBytes)
	if guard.Measure() != memguard.Critical {
		release()
		t.Fatalf("holding %s over a budget %s past the start: %v (%s of %s)", memguard.Format(ballastBytes), memguard.Format(budgetHeadroom), guard.Level(), memguard.Format(guard.Used()), memguard.Format(guard.Limit()))
	}

	// Still up, and saying why it is careful.
	for path, want := range map[string]string{"/healthz": "ok", "/readyz": "memory: critical"} {
		res, err := http.Get(srv.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(res.Body)
		res.Body.Close()
		if res.StatusCode != http.StatusOK || !strings.Contains(string(body), want) {
			t.Fatalf("%s past the budget: %d %q, want 200 with %q", path, res.StatusCode, body, want)
		}
	}
	// The models went but the last used, which keeps answering.
	if _, n := svc.ModelCacheUsage(); n != 1 {
		t.Fatalf("%d models still loaded past the budget, want 1", n)
	}
	for range 3 {
		if _, err := own.predict.Predict(ctx, connect.NewRequest(&mimirv1.PredictRequest{ModelId: a.GetId(), Input: input})); err != nil {
			t.Fatalf("the loaded model past the budget: %v", err)
		}
	}
	// Loading another is turned away, as is an upload read into memory.
	if _, err := own.predict.Predict(ctx, connect.NewRequest(&mimirv1.PredictRequest{ModelId: b.GetId(), Input: input})); code(err) != connect.CodeUnavailable || !strings.Contains(err.Error(), "low on memory") {
		t.Fatalf("loading a model past the budget: %v, want unavailable for memory", err)
	}
	big := blobs(60000) // over a megabyte, so the upload gate looks at it
	if _, err := own.datasets.CreateDataset(ctx, connect.NewRequest(&mimirv1.CreateDatasetRequest{Name: "Big", Kind: mimirv1.DatasetKind_DATASET_KIND_TABLE, Csv: []byte(big)})); code(err) != connect.CodeUnavailable {
		t.Fatalf("an upload past the budget: %v, want unavailable", err)
	}
	res, err := http.Get(srv.URL + "/metrics")
	if err != nil {
		t.Fatal(err)
	}
	metrics, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if !strings.Contains(string(metrics), "mimir_memory_pressure 2") {
		t.Fatal("/metrics doesn't show the pressure")
	}

	release()
	if guard.Measure() != memguard.OK {
		t.Fatalf("after letting go: %v (%s of %s)", guard.Level(), memguard.Format(guard.Used()), memguard.Format(guard.Limit()))
	}
	if _, err := own.predict.Predict(ctx, connect.NewRequest(&mimirv1.PredictRequest{ModelId: b.GetId(), Input: input})); err != nil {
		t.Fatalf("loading a model once memory is back: %v", err)
	}
	if _, err := own.datasets.CreateDataset(ctx, connect.NewRequest(&mimirv1.CreateDatasetRequest{Name: "Big", Kind: mimirv1.DatasetKind_DATASET_KIND_TABLE, Csv: []byte(big)})); err != nil {
		t.Fatalf("an upload once memory is back: %v", err)
	}
}

// startWorker joins a worker to srv with a memory budget headroom over
// what the process uses now, its handlers made by wrap, and returns its
// id once it is online.
func startWorker(t *testing.T, srv string, own clients, name string, headroom int64, wrap func(map[string]worker.Handler)) string {
	t.Helper()
	ctx := context.Background()
	created, err := own.workers.CreateWorker(ctx, connect.NewRequest(&mimirv1.CreateWorkerRequest{Name: name}))
	if err != nil {
		t.Fatal(err)
	}
	handlers := builtin.Builtin(t.TempDir())
	if wrap != nil {
		wrap(handlers)
	}
	runtime.GC()
	limit := int64(-1)
	if headroom > 0 {
		limit = memguard.InUse() + headroom
	}
	wctx, stop := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		worker.Run(wctx, worker.Config{Server: srv, Token: created.Msg.GetToken(), Capacity: 2, Version: "test", DataDir: t.TempDir(), MemoryLimit: limit, StopTrainingAfter: 300 * time.Millisecond, Log: quietLog}, handlers)
	}()
	t.Cleanup(func() { stop(); <-done })
	id := created.Msg.GetWorker().GetId()
	waitOnline(t, own, id, true)
	return id
}

// waitPressure waits for the worker to report memory pressure p.
func waitPressure(t *testing.T, own clients, id string, p int32) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		w, err := own.workers.GetWorker(context.Background(), connect.NewRequest(&mimirv1.GetWorkerRequest{Id: id}))
		if err != nil {
			t.Fatal(err)
		}
		if got := w.Msg.GetWorker().GetStats().GetMemoryPressure(); got == p {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("the worker never reported memory pressure %d", p)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func jobStatus(t *testing.T, own clients, id string) *mimirv1.Job {
	t.Helper()
	j, err := own.training.GetJob(context.Background(), connect.NewRequest(&mimirv1.GetJobRequest{Id: id}))
	if err != nil {
		t.Fatal(err)
	}
	return j.Msg.GetJob()
}

// TestWorkerStaysUpPastItsMemoryBudget takes a worker's memory well past
// its budget. It stays linked and keeps answering for the model it has,
// tells the console at once, and gets no training runs until memory is
// back; a run it was training goes back in the queue rather than failing.
func TestWorkerStaysUpPastItsMemoryBudget(t *testing.T) {
	srv := newServer(t)
	ctx := context.Background()
	own := owner(t, srv)
	svc.Local, runner.Local = false, false
	id := startWorker(t, srv.URL, own, "small", budgetHeadroom, nil)
	waitPressure(t, own, id, 0)

	ds, err := own.datasets.CreateDataset(ctx, connect.NewRequest(&mimirv1.CreateDatasetRequest{Name: "Fruit", Kind: mimirv1.DatasetKind_DATASET_KIND_TABLE, Csv: []byte(blobs(200))}))
	if err != nil {
		t.Fatal(err)
	}
	newJob := func(name string, s *mimirv1.TrainingSettings) string {
		j, err := own.training.CreateJob(ctx, connect.NewRequest(&mimirv1.CreateJobRequest{Name: name, Architecture: "table-mlp", DatasetId: ds.Msg.GetDataset().GetId(), Settings: s}))
		if err != nil {
			t.Fatal(err)
		}
		return j.Msg.GetJob().GetId()
	}
	first := newJob("First", &mimirv1.TrainingSettings{Epochs: 4, LearningRate: 0.01})
	runner.Tick(ctx)
	j := jobStatus(t, own, first)
	if j.GetStatus() != mimirv1.JobStatus_JOB_STATUS_SUCCEEDED {
		t.Fatalf("training with memory to spare: %v", j)
	}
	model := j.GetModelId()
	predict := func(when string) {
		t.Helper()
		if _, err := own.predict.Predict(ctx, connect.NewRequest(&mimirv1.PredictRequest{ModelId: model, Input: &mimirv1.Tensor{Values: []float32{-1, -1}}})); err != nil {
			t.Fatalf("predict %s: %v", when, err)
		}
	}
	predict("with memory to spare")

	// A long run is training when memory runs out.
	slow := newJob("Slow", &mimirv1.TrainingSettings{Epochs: 400, BatchSize: 4})
	ticked := make(chan struct{})
	go func() { runner.Tick(ctx); close(ticked) }()
	deadline := time.Now().Add(10 * time.Second)
	for jobStatus(t, own, slow).GetEpoch() < 1 {
		if time.Now().After(deadline) {
			t.Fatal("the slow run never got going")
		}
		time.Sleep(20 * time.Millisecond)
	}
	release := hold(ballastBytes)
	defer release()
	waitPressure(t, own, id, 2)
	select {
	case <-ticked:
	case <-time.After(20 * time.Second):
		t.Fatal("the run training on the worker didn't stop when its memory ran out")
	}
	if s := jobStatus(t, own, slow); s.GetStatus() != mimirv1.JobStatus_JOB_STATUS_QUEUED {
		t.Fatalf("the run stopped for memory is %v (%q), want back in the queue", s.GetStatus(), s.GetError())
	}

	// Still linked and answering for the model it has, and given no
	// training.
	waitOnline(t, own, id, true)
	predict("past the budget")
	predict("past the budget, again")
	other := newJob("Other", &mimirv1.TrainingSettings{Epochs: 2})
	runner.Tick(ctx)
	if s := jobStatus(t, own, other); s.GetStatus() != mimirv1.JobStatus_JOB_STATUS_QUEUED {
		t.Fatalf("a run while the only worker is short on memory: %v, want queued", s.GetStatus())
	}

	// Memory back: training goes there again.
	release()
	waitPressure(t, own, id, 0)
	runner.Tick(ctx)
	if s := jobStatus(t, own, other); s.GetStatus() != mimirv1.JobStatus_JOB_STATUS_SUCCEEDED {
		t.Fatalf("a run once memory is back: %v (%q)", s.GetStatus(), s.GetError())
	}
	predict("once memory is back")
}

// panicky is a handler that panics on an input starting with -999, and
// otherwise does what h does.
type panicky struct{ h worker.Handler }

func (p panicky) Predict(ctx context.Context, m *worker.Model, input []float32) (worker.Prediction, error) {
	if len(input) > 0 && input[0] == -999 {
		var answers map[string]float32
		answers["boom"] = 1 // a nil map: a real bug
	}
	return p.h.Predict(ctx, m, input)
}

func (p panicky) Load(ctx context.Context, m *worker.Model) error {
	return p.h.(worker.Loader).Load(ctx, m)
}

func (p panicky) Unload(id string) { p.h.(worker.Loader).Unload(id) }

// TestWorkerSurvivesAPanic has a worker's model panic on one input: that
// prediction fails, and the worker stays linked and answers the next.
func TestWorkerSurvivesAPanic(t *testing.T) {
	srv := newServer(t)
	ctx := context.Background()
	own := owner(t, srv)
	svc.Local = false
	id := startWorker(t, srv.URL, own, "buggy", 0, func(hs map[string]worker.Handler) {
		hs["mnist-mlp"] = panicky{hs["mnist-mlp"]}
	})
	m := createModel(t, own, "A", "mnist-mlp")
	in := make([]float32, 784)
	if _, err := own.predict.Predict(ctx, connect.NewRequest(&mimirv1.PredictRequest{ModelId: m.GetId(), Input: &mimirv1.Tensor{Values: in}})); err != nil {
		t.Fatal(err)
	}
	in[0] = -999
	if _, err := own.predict.Predict(ctx, connect.NewRequest(&mimirv1.PredictRequest{ModelId: m.GetId(), Input: &mimirv1.Tensor{Values: in}})); code(err) != connect.CodeUnavailable {
		t.Fatalf("a prediction that panics on the worker: %v, want unavailable", err)
	}
	in[0] = 0
	for range 3 {
		if _, err := own.predict.Predict(ctx, connect.NewRequest(&mimirv1.PredictRequest{ModelId: m.GetId(), Input: &mimirv1.Tensor{Values: in}})); err != nil {
			t.Fatalf("after the panic: %v", err)
		}
	}
	waitOnline(t, own, id, true)
}
