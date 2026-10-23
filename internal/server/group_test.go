package server_test

import (
	"context"
	"strings"
	"testing"

	"connectrpc.com/connect"

	mimirv1 "github.com/gsoultan/mimir-server/gen/mimir/v1"
)

// TestTrainTogether trains one run on three workers together, through the
// setting, and checks it takes a third of the steps and still learns.
func TestTrainTogether(t *testing.T) {
	srv := newServer(t)
	ctx := context.Background()
	own := owner(t, srv)
	svc.Local, runner.Local = false, false
	runner.GroupWorkers = svc.GroupWorkers
	t.Cleanup(func() { runner.GroupWorkers = nil })
	joinWorker(t, own, srv.URL, "alpha")
	joinWorker(t, own, srv.URL, "beta")
	joinWorker(t, own, srv.URL, "gamma")

	if _, err := own.settings.UpdateTraining(ctx, connect.NewRequest(&mimirv1.UpdateTrainingRequest{GroupWorkers: new(int32(99))})); code(err) != connect.CodeInvalidArgument {
		t.Fatalf("99 workers together: %v", err)
	}
	set, err := own.settings.UpdateTraining(ctx, connect.NewRequest(&mimirv1.UpdateTrainingRequest{GroupWorkers: new(int32(4))}))
	if err != nil || set.Msg.GetTraining().GetGroupWorkers() != 4 {
		t.Fatalf("UpdateTraining = %v, %v", set, err)
	}
	// Turning training here on or off keeps the choice.
	set, err = own.settings.UpdateTraining(ctx, connect.NewRequest(&mimirv1.UpdateTrainingRequest{TrainHere: false}))
	if err != nil || set.Msg.GetTraining().GetGroupWorkers() != 4 {
		t.Fatalf("UpdateTraining without group_workers = %v, %v", set, err)
	}

	ds, err := own.datasets.CreateDataset(ctx, connect.NewRequest(&mimirv1.CreateDatasetRequest{Name: "Fruit", Kind: mimirv1.DatasetKind_DATASET_KIND_TABLE, Csv: []byte(blobs(1200))}))
	if err != nil {
		t.Fatal(err)
	}
	j, err := own.training.CreateJob(ctx, connect.NewRequest(&mimirv1.CreateJobRequest{Name: "Together", Architecture: "table-mlp", DatasetId: ds.Msg.GetDataset().GetId(), Settings: &mimirv1.TrainingSettings{Epochs: 3, BatchSize: 16, LearningRate: 0.01}}))
	if err != nil {
		t.Fatal(err)
	}
	runner.Tick(ctx)
	got, err := own.training.GetJob(ctx, connect.NewRequest(&mimirv1.GetJobRequest{Id: j.Msg.GetJob().GetId()}))
	if err != nil {
		t.Fatal(err)
	}
	job := got.Msg.GetJob()
	if job.GetStatus() != mimirv1.JobStatus_JOB_STATUS_SUCCEEDED {
		t.Fatalf("job = %v", job)
	}
	if n := len(strings.Split(job.GetWorker(), ", ")); n != 3 {
		t.Fatalf("trained on %q, want three workers", job.GetWorker())
	}
	// 960 examples to learn from in batches of 16 are 60 steps, 20 each.
	if job.GetStepsPerEpoch() != 20 || len(job.GetHistory()) != 3 {
		t.Fatalf("steps per epoch %d, history %v", job.GetStepsPerEpoch(), job.GetHistory())
	}
	if acc := job.GetHistory()[2].GetAccuracy(); acc < 0.95 {
		t.Fatalf("accuracy %v", acc)
	}
	if _, err := own.predict.Predict(ctx, connect.NewRequest(&mimirv1.PredictRequest{ModelId: job.GetModelId(), Input: &mimirv1.Tensor{Values: []float32{1, 1}}})); err != nil {
		t.Fatal(err)
	}

	// With one worker free the run trains on it alone.
	if _, err := own.settings.UpdateTraining(ctx, connect.NewRequest(&mimirv1.UpdateTrainingRequest{GroupWorkers: new(int32(1))})); err != nil {
		t.Fatal(err)
	}
	j, err = own.training.CreateJob(ctx, connect.NewRequest(&mimirv1.CreateJobRequest{Name: "Alone", Architecture: "table-mlp", DatasetId: ds.Msg.GetDataset().GetId(), Settings: &mimirv1.TrainingSettings{Epochs: 1, BatchSize: 16, LearningRate: 0.01}}))
	if err != nil {
		t.Fatal(err)
	}
	runner.Tick(ctx)
	got, _ = own.training.GetJob(ctx, connect.NewRequest(&mimirv1.GetJobRequest{Id: j.Msg.GetJob().GetId()}))
	if job := got.Msg.GetJob(); job.GetStatus() != mimirv1.JobStatus_JOB_STATUS_SUCCEEDED || strings.Contains(job.GetWorker(), ",") || job.GetStepsPerEpoch() != 60 {
		t.Fatalf("alone: %v", job)
	}
}
