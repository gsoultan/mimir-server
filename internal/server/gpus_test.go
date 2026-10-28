package server_test

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"testing"

	"connectrpc.com/connect"
	"github.com/gsoultan/mimir/tensor"

	mimirv1 "github.com/gsoultan/mimir-server/gen/mimir/v1"
	"github.com/gsoultan/mimir-server/internal/training"
	builtin "github.com/gsoultan/mimir-server/internal/worker"
	"github.com/gsoultan/mimir-server/worker"
)

// TestTrainOnEveryGPU sends a run to the idle worker with several GPUs
// rather than one with a single device, trains it there split between
// them (two CPU devices stand in for the GPUs), and says so on the run.
func TestTrainOnEveryGPU(t *testing.T) {
	srv := newServer(t)
	ctx := context.Background()
	own := owner(t, srv)
	svc.Local, runner.Local = false, false
	t.Cleanup(func() { training.UseDevices(nil) })
	training.UseDevices(tensor.CPUs(2))

	_, _, _ = joinWorker(t, own, srv.URL, "one-device")
	created, err := own.workers.CreateWorker(ctx, connect.NewRequest(&mimirv1.CreateWorkerRequest{Name: "two-gpus"}))
	if err != nil {
		t.Fatal(err)
	}
	wctx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		worker.Run(wctx, worker.Config{Server: srv.URL, Token: created.Msg.GetToken(), Capacity: 2, GPUs: 2, Version: "test", DataDir: t.TempDir(), Log: slog.New(slog.NewTextHandler(io.Discard, nil))}, builtin.Builtin(t.TempDir()))
	}()
	t.Cleanup(func() { cancel(); <-done })
	if w := waitOnline(t, own, created.Msg.GetWorker().GetId(), true); w.GetGpus() != 2 {
		t.Fatalf("the Workers page shows %d GPUs", w.GetGpus())
	}

	ds, err := own.datasets.CreateDataset(ctx, connect.NewRequest(&mimirv1.CreateDatasetRequest{Name: "Fruit", Kind: mimirv1.DatasetKind_DATASET_KIND_TABLE, Csv: []byte(blobs(200))}))
	if err != nil {
		t.Fatal(err)
	}
	j, err := own.training.CreateJob(ctx, connect.NewRequest(&mimirv1.CreateJobRequest{Name: "Split", Architecture: "table-mlp", DatasetId: ds.Msg.GetDataset().GetId(), Settings: &mimirv1.TrainingSettings{Epochs: 3, LearningRate: 0.01}}))
	if err != nil {
		t.Fatal(err)
	}
	runner.Tick(ctx)
	got, err := own.training.GetJob(ctx, connect.NewRequest(&mimirv1.GetJobRequest{Id: j.Msg.GetJob().GetId()}))
	if err != nil {
		t.Fatal(err)
	}
	job := got.Msg.GetJob()
	if job.GetStatus() != mimirv1.JobStatus_JOB_STATUS_SUCCEEDED || job.GetWorker() != "two-gpus" || !strings.Contains(job.GetNote(), "Trained on 2 GPUs at once.") {
		t.Fatalf("job: %v on %q, note %q (%s)", job.GetStatus(), job.GetWorker(), job.GetNote(), job.GetError())
	}
}
