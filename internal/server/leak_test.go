package server_test

import (
	"context"
	"fmt"
	"net/http"
	"runtime"
	"testing"
	"time"

	"connectrpc.com/connect"

	mimirv1 "github.com/gsoultan/mimir-server/gen/mimir/v1"
)

// TestRequestsLeaveNothingBehind runs rounds of the server's main flows,
// uploads, models made and deleted, predictions and chat, and checks
// that goroutines and memory do not keep growing from round to round: a
// leak of either would, over the hours a server runs, take it down.
func TestRequestsLeaveNothingBehind(t *testing.T) {
	ctx := context.Background()
	srv := newServer(t)
	c := owner(t, srv)
	_, file := digits(t)
	input := make([]float32, 784)
	for i := range input {
		input[i] = float32(i%28) / 28
	}

	round := func(n int) {
		t.Helper()
		for i := 0; i < n; i++ {
			status, out := sendUpload(t, srv, c.jar.cookie, "", file)
			if status != http.StatusOK {
				t.Fatalf("upload: %d %+v", status, out)
			}
			res, err := c.models.CreateModel(ctx, connect.NewRequest(&mimirv1.CreateModelRequest{Name: fmt.Sprintf("Digits %d", i), Architecture: "mim", Upload: out.Upload}))
			if err != nil {
				t.Fatal(err)
			}
			id := res.Msg.GetModel().GetId()
			for k := 0; k < 3; k++ {
				if _, err := c.predict.Predict(ctx, connect.NewRequest(&mimirv1.PredictRequest{ModelId: id, Input: &mimirv1.Tensor{Values: input}})); err != nil {
					t.Fatal(err)
				}
			}
			// A refused upload and a refused question clean up too.
			if status, _ := sendUpload(t, srv, c.jar.cookie, "", nil); status != http.StatusBadRequest {
				t.Fatalf("empty upload: %d", status)
			}
			if _, err := c.predict.Predict(ctx, connect.NewRequest(&mimirv1.PredictRequest{ModelId: id, Input: &mimirv1.Tensor{Values: input[:3]}})); err == nil {
				t.Fatal("a question of the wrong shape was answered")
			}
			if _, err := c.models.DeleteModel(ctx, connect.NewRequest(&mimirv1.DeleteModelRequest{Id: id})); err != nil {
				t.Fatal(err)
			}
		}
	}

	// Chat holds a session with its key-value cache per answer, and
	// training runs a job on its own goroutines; both are run too.
	lm := languageModel(t, c, "Tiny Llama")
	ds, err := c.datasets.CreateDataset(ctx, connect.NewRequest(&mimirv1.CreateDatasetRequest{Name: "Fruit", Kind: mimirv1.DatasetKind_DATASET_KIND_TABLE, Csv: []byte(blobs(60))}))
	if err != nil {
		t.Fatal(err)
	}
	more := func(n int) {
		t.Helper()
		for i := 0; i < n; i++ {
			stream, err := c.predict.Chat(ctx, connect.NewRequest(&mimirv1.ChatRequest{
				ModelId:   lm.GetId(),
				Messages:  []*mimirv1.ChatMessage{{Role: "user", Content: "Hello"}},
				MaxTokens: 3,
			}))
			if err != nil {
				t.Fatal(err)
			}
			for stream.Receive() {
			}
			if err := stream.Err(); err != nil {
				t.Fatal(err)
			}
			job, err := c.training.CreateJob(ctx, connect.NewRequest(&mimirv1.CreateJobRequest{Name: fmt.Sprintf("Sorter %d", i), Architecture: "table-mlp", DatasetId: ds.Msg.GetDataset().GetId(), Settings: &mimirv1.TrainingSettings{Epochs: 1}}))
			if err != nil {
				t.Fatal(err)
			}
			runner.Tick(ctx)
			j, err := c.training.GetJob(ctx, connect.NewRequest(&mimirv1.GetJobRequest{Id: job.Msg.GetJob().GetId()}))
			if err != nil || j.Msg.GetJob().GetStatus() != mimirv1.JobStatus_JOB_STATUS_SUCCEEDED {
				t.Fatalf("job: %v, %v", j, err)
			}
			if _, err := c.models.DeleteModel(ctx, connect.NewRequest(&mimirv1.DeleteModelRequest{Id: j.Msg.GetJob().GetModelId()})); err != nil {
				t.Fatal(err)
			}
		}
	}

	// The first round warms caches, pools and the HTTP client's
	// connections; what matters is the rounds after it.
	round(4)
	more(2)
	settle := func() (goroutines int, heap uint64) {
		srv.Client().CloseIdleConnections()
		var ms runtime.MemStats
		deadline := time.Now().Add(5 * time.Second)
		for {
			runtime.GC()
			runtime.ReadMemStats(&ms)
			g := runtime.NumGoroutine()
			if time.Now().After(deadline) || goroutines == g {
				return g, ms.HeapAlloc
			}
			goroutines = g
			time.Sleep(50 * time.Millisecond)
		}
	}
	g0, h0 := settle()
	round(12)
	more(6)
	g1, h1 := settle()
	if g1 > g0+2 {
		buf := make([]byte, 1<<20)
		t.Fatalf("%d goroutines after the rounds, %d before:\n%s", g1, g0, buf[:runtime.Stack(buf, true)])
	}
	// Twelve rounds of a 100 KB model, three questions each. Growth past
	// a few megabytes is something kept per request, not noise.
	if h1 > h0+8<<20 {
		t.Fatalf("heap grew from %d to %d bytes over the rounds", h0, h1)
	}
}
