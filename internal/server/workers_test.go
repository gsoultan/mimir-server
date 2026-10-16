package server_test

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"

	mimirv1 "github.com/gsoultan/mimir-server/gen/mimir/v1"
	"github.com/gsoultan/mimir-server/gen/mimir/v1/mimirv1connect"
	builtin "github.com/gsoultan/mimir-server/internal/worker"
	"github.com/gsoultan/mimir-server/worker"
)

// TestWorker joins a worker built on the SDK, has it train a model and
// answer predictions for a server that runs nothing itself, then deletes
// it.
func TestWorker(t *testing.T) {
	srv := newServer(t)
	ctx := context.Background()
	own := owner(t, srv)

	// Register it; the token shows once.
	if _, err := own.workers.CreateWorker(ctx, connect.NewRequest(&mimirv1.CreateWorkerRequest{Name: " "})); code(err) != connect.CodeInvalidArgument {
		t.Fatalf("nameless worker: %v", err)
	}
	created, err := own.workers.CreateWorker(ctx, connect.NewRequest(&mimirv1.CreateWorkerRequest{Name: "lab-1"}))
	if err != nil || !strings.HasPrefix(created.Msg.GetToken(), "mimirw_") {
		t.Fatalf("CreateWorker = %v, %v", created, err)
	}
	ls, _ := own.workers.ListWorkers(ctx, connect.NewRequest(&mimirv1.ListWorkersRequest{}))
	if len(ls.Msg.GetWorkers()) != 1 || ls.Msg.GetWorkers()[0].GetOnline() {
		t.Fatalf("ListWorkers before joining = %v", ls)
	}

	// A wrong token is refused.
	h2c := new(http.Protocols)
	h2c.SetUnencryptedHTTP2(true)
	raw := mimirv1connect.NewWorkerServiceClient(&http.Client{Transport: &http.Transport{Protocols: h2c}}, srv.URL, connect.WithGRPC())
	bad := raw.Link(ctx)
	bad.RequestHeader().Set("Authorization", "Bearer mimirw_nope")
	bad.Send(&mimirv1.LinkRequest{Body: &mimirv1.LinkRequest_Hello{Hello: &mimirv1.Hello{Architectures: []string{"table-mlp"}}}})
	if _, err := bad.Receive(); code(err) != connect.CodeUnauthenticated {
		t.Fatalf("bad token: %v", err)
	}
	bad.CloseRequest()
	bad.CloseResponse()

	// Join with the SDK, as the binary does with -role=worker.
	wctx, stop := context.WithCancel(ctx)
	defer stop()
	done := make(chan error, 1)
	go func() {
		done <- worker.Run(wctx, worker.Config{Server: srv.URL, Token: created.Msg.GetToken(), Capacity: 2, Version: "test", Log: slog.New(slog.NewTextHandler(io.Discard, nil))}, builtin.Builtin(t.TempDir()))
	}()
	deadline := time.Now().Add(10 * time.Second)
	for {
		ls, err := own.workers.ListWorkers(ctx, connect.NewRequest(&mimirv1.ListWorkersRequest{}))
		if err != nil {
			t.Fatal(err)
		}
		if w := ls.Msg.GetWorkers()[0]; w.GetOnline() {
			if !w.GetTrains() || w.GetCapacity() != 2 || w.GetVersion() != "test" || !strings.Contains(strings.Join(w.GetArchitectures(), ","), "table-mlp") {
				t.Fatalf("worker status %v", w)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("worker never came online")
		}
		time.Sleep(50 * time.Millisecond)
	}

	// This server coordinates only: the worker trains and answers.
	svc.Local, runner.Local = false, false
	ds, err := own.datasets.CreateDataset(ctx, connect.NewRequest(&mimirv1.CreateDatasetRequest{Name: "Fruit", Kind: mimirv1.DatasetKind_DATASET_KIND_TABLE, Csv: []byte(blobs(200))}))
	if err != nil {
		t.Fatal(err)
	}
	job, err := own.training.CreateJob(ctx, connect.NewRequest(&mimirv1.CreateJobRequest{Name: "Fruit sorter", Architecture: "table-mlp", DatasetId: ds.Msg.GetDataset().GetId(), Settings: &mimirv1.TrainingSettings{Epochs: 8, LearningRate: 0.01}}))
	if err != nil {
		t.Fatal(err)
	}
	runner.Tick(ctx)
	j, err := own.training.GetJob(ctx, connect.NewRequest(&mimirv1.GetJobRequest{Id: job.Msg.GetJob().GetId()}))
	if err != nil {
		t.Fatal(err)
	}
	jb := j.Msg.GetJob()
	if jb.GetStatus() != mimirv1.JobStatus_JOB_STATUS_SUCCEEDED || jb.GetWorker() != "lab-1" || len(jb.GetHistory()) != 8 || jb.GetAccuracy() < 0.9 {
		t.Fatalf("job on worker: %v", jb)
	}
	m, _ := own.models.GetModel(ctx, connect.NewRequest(&mimirv1.GetModelRequest{Id: jb.GetModelId()}))
	if strings.Join(m.Msg.GetModel().GetFeatures(), ",") != "width,height" {
		t.Fatalf("model from worker: %v", m.Msg.GetModel())
	}
	for range 2 { // the second call reuses the weights the link already carried
		p, err := own.predict.Predict(ctx, connect.NewRequest(&mimirv1.PredictRequest{ModelId: jb.GetModelId(), Input: &mimirv1.Tensor{Values: []float32{-1, -1}}}))
		if err != nil || p.Msg.GetGuesses()[0].GetLabel() != "pear" {
			t.Fatalf("Predict via worker = %v, %v", p, err)
		}
	}
	if _, err := own.predict.Predict(ctx, connect.NewRequest(&mimirv1.PredictRequest{ModelId: jb.GetModelId(), Input: &mimirv1.Tensor{Values: []float32{1}}})); code(err) != connect.CodeInvalidArgument {
		t.Fatalf("wrong size via worker: %v", err)
	}

	// Stopping a run on the worker.
	job2, _ := own.training.CreateJob(ctx, connect.NewRequest(&mimirv1.CreateJobRequest{Name: "Slow", Architecture: "table-mlp", DatasetId: ds.Msg.GetDataset().GetId(), Settings: &mimirv1.TrainingSettings{Epochs: 400, BatchSize: 4}}))
	go func() {
		time.Sleep(300 * time.Millisecond)
		own.training.CancelJob(ctx, connect.NewRequest(&mimirv1.CancelJobRequest{Id: job2.Msg.GetJob().GetId()}))
	}()
	runner.Tick(ctx)
	j2, _ := own.training.GetJob(ctx, connect.NewRequest(&mimirv1.GetJobRequest{Id: job2.Msg.GetJob().GetId()}))
	if j2.Msg.GetJob().GetStatus() != mimirv1.JobStatus_JOB_STATUS_CANCELLED {
		t.Fatalf("cancelled on worker: %v", j2.Msg.GetJob())
	}

	// Deleting the worker ends its link and the token stops working.
	if _, err := own.workers.DeleteWorker(ctx, connect.NewRequest(&mimirv1.DeleteWorkerRequest{Id: created.Msg.GetWorker().GetId()})); err != nil {
		t.Fatal(err)
	}
	if _, err := own.predict.Predict(ctx, connect.NewRequest(&mimirv1.PredictRequest{ModelId: jb.GetModelId(), Input: &mimirv1.Tensor{Values: []float32{-1, -1}}})); code(err) != connect.CodeUnavailable {
		t.Fatalf("predict with no worker: %v", err)
	}
	ls, _ = own.workers.ListWorkers(ctx, connect.NewRequest(&mimirv1.ListWorkersRequest{}))
	if len(ls.Msg.GetWorkers()) != 0 {
		t.Fatalf("ListWorkers after delete = %v", ls)
	}
	stop()
	if err := <-done; err != context.Canceled {
		t.Fatalf("worker.Run = %v", err)
	}

	// Editors can't manage workers.
	own.users.CreateUser(ctx, connect.NewRequest(&mimirv1.CreateUserRequest{Email: "e@example.com", Name: "E", Password: password, Access: &mimirv1.Access{Role: mimirv1.Role_ROLE_EDITOR, AllModels: true}}))
	e := signIn(t, srv, "e@example.com")
	if _, err := e.workers.ListWorkers(ctx, connect.NewRequest(&mimirv1.ListWorkersRequest{})); code(err) != connect.CodePermissionDenied {
		t.Fatalf("editor ListWorkers: %v", err)
	}
}
