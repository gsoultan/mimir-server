package server_test

import (
	"context"
	"fmt"
	"math/rand/v2"
	"strings"
	"testing"

	"connectrpc.com/connect"

	mimirv1 "github.com/gsoultan/mimir-server/gen/mimir/v1"
)

// blobs makes a CSV of two well-separated clusters: easy to learn, so a
// few epochs reach high accuracy.
func blobs(n int) string {
	r := rand.New(rand.NewPCG(1, 2))
	var b strings.Builder
	b.WriteString("width,height,kind\n")
	for i := range n {
		cx, cy, kind := 1.0, 1.0, "apple"
		if i%2 == 1 {
			cx, cy, kind = -1.0, -1.0, "pear"
		}
		fmt.Fprintf(&b, "%.3f,%.3f,%s\n", cx+r.NormFloat64()*0.3, cy+r.NormFloat64()*0.3, kind)
	}
	return b.String()
}

func TestTeachFromTable(t *testing.T) {
	ctx := context.Background()
	srv := newServer(t)
	own := owner(t, srv)

	// Bad tables are refused with a reason.
	_, err := own.datasets.CreateDataset(ctx, connect.NewRequest(&mimirv1.CreateDatasetRequest{Name: "bad", Kind: mimirv1.DatasetKind_DATASET_KIND_TABLE, Csv: []byte("a,b\n1,x\n")}))
	if code(err) != connect.CodeInvalidArgument || !strings.Contains(err.Error(), "at least") {
		t.Fatalf("tiny table: %v", err)
	}
	_, err = own.datasets.CreateDataset(ctx, connect.NewRequest(&mimirv1.CreateDatasetRequest{Name: "bad", Kind: mimirv1.DatasetKind_DATASET_KIND_TABLE, Csv: []byte("a,b\n" + strings.Repeat("x,1\n", 12))}))
	if code(err) != connect.CodeInvalidArgument || !strings.Contains(err.Error(), "only one value") {
		t.Fatalf("one answer: %v", err)
	}

	ds, err := own.datasets.CreateDataset(ctx, connect.NewRequest(&mimirv1.CreateDatasetRequest{Name: "Fruit", Kind: mimirv1.DatasetKind_DATASET_KIND_TABLE, Csv: []byte(blobs(200))}))
	if err != nil {
		t.Fatal(err)
	}
	d := ds.Msg.GetDataset()
	if d.GetRows() != 200 || d.GetTarget() != "kind" || len(d.GetFeatures()) != 2 || strings.Join(d.GetLabels(), ",") != "apple,pear" || len(d.GetArchitectures()) == 0 {
		t.Fatalf("dataset %v", d)
	}
	got, err := own.datasets.GetDataset(ctx, connect.NewRequest(&mimirv1.GetDatasetRequest{Id: d.GetId()}))
	if err != nil || len(got.Msg.GetSample()) != 6 || got.Msg.GetSample()[0].GetValues()[2] != "kind" {
		t.Fatalf("GetDataset = %v, %v", got, err)
	}

	// The wrong model type for the data is refused; the right one queues.
	_, err = own.training.CreateJob(ctx, connect.NewRequest(&mimirv1.CreateJobRequest{Name: "Fruit sorter", Architecture: "mnist-mlp", DatasetId: d.GetId()}))
	if code(err) != connect.CodeInvalidArgument {
		t.Fatalf("image model on a table: %v", err)
	}
	job, err := own.training.CreateJob(ctx, connect.NewRequest(&mimirv1.CreateJobRequest{Name: "Fruit sorter", Architecture: "table-mlp", DatasetId: d.GetId(), Settings: &mimirv1.TrainingSettings{Epochs: 8, LearningRate: 0.01}}))
	if err != nil || job.Msg.GetJob().GetStatus() != mimirv1.JobStatus_JOB_STATUS_QUEUED {
		t.Fatalf("CreateJob = %v, %v", job, err)
	}
	id := job.Msg.GetJob().GetId()
	if _, err := own.datasets.DeleteDataset(ctx, connect.NewRequest(&mimirv1.DeleteDatasetRequest{Id: d.GetId()})); code(err) != connect.CodeFailedPrecondition {
		t.Fatalf("delete dataset in use: %v", err)
	}

	runner.Tick(ctx)
	j, err := own.training.GetJob(ctx, connect.NewRequest(&mimirv1.GetJobRequest{Id: id}))
	if err != nil {
		t.Fatal(err)
	}
	jb := j.Msg.GetJob()
	if jb.GetStatus() != mimirv1.JobStatus_JOB_STATUS_SUCCEEDED || jb.GetModelId() == "" || len(jb.GetHistory()) != 8 || jb.GetDatasetName() != "Fruit" {
		t.Fatalf("job after run: %v", jb)
	}
	if jb.GetAccuracy() < 0.9 {
		t.Fatalf("accuracy %.2f", jb.GetAccuracy())
	}
	// The model it made answers, with the dataset's labels and features.
	m, err := own.models.GetModel(ctx, connect.NewRequest(&mimirv1.GetModelRequest{Id: jb.GetModelId()}))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(m.Msg.GetModel().GetLabels(), ",") != "apple,pear" || strings.Join(m.Msg.GetModel().GetFeatures(), ",") != "width,height" || m.Msg.GetModel().GetJobId() != id {
		t.Fatalf("model %v", m.Msg.GetModel())
	}
	p, err := own.predict.Predict(ctx, connect.NewRequest(&mimirv1.PredictRequest{ModelId: jb.GetModelId(), Input: &mimirv1.Tensor{Values: []float32{-1, -1}}}))
	if err != nil || p.Msg.GetGuesses()[0].GetLabel() != "pear" {
		t.Fatalf("Predict = %v, %v", p, err)
	}
	// Lists and cleanup.
	ls, _ := own.training.ListJobs(ctx, connect.NewRequest(&mimirv1.ListJobsRequest{}))
	if len(ls.Msg.GetJobs()) != 1 {
		t.Fatalf("ListJobs = %v", ls)
	}
	if _, err := own.training.ResumeJob(ctx, connect.NewRequest(&mimirv1.ResumeJobRequest{Id: id})); code(err) != connect.CodeFailedPrecondition {
		t.Fatalf("resume a finished job: %v", err)
	}
	if _, err := own.training.DeleteJob(ctx, connect.NewRequest(&mimirv1.DeleteJobRequest{Id: id})); err != nil {
		t.Fatal(err)
	}
	if _, err := own.datasets.DeleteDataset(ctx, connect.NewRequest(&mimirv1.DeleteDatasetRequest{Id: d.GetId()})); err != nil {
		t.Fatal(err)
	}
	// Viewers can't teach.
	own.users.CreateUser(ctx, connect.NewRequest(&mimirv1.CreateUserRequest{Email: "v@example.com", Name: "V", Password: password, Access: &mimirv1.Access{Role: mimirv1.Role_ROLE_VIEWER, AllModels: true}}))
	v := signIn(t, srv, "v@example.com")
	if _, err := v.training.ListJobs(ctx, connect.NewRequest(&mimirv1.ListJobsRequest{})); code(err) != connect.CodePermissionDenied {
		t.Fatalf("viewer ListJobs: %v", err)
	}
}

func TestCancelAndResume(t *testing.T) {
	ctx := context.Background()
	srv := newServer(t)
	own := owner(t, srv)
	ds, err := own.datasets.CreateDataset(ctx, connect.NewRequest(&mimirv1.CreateDatasetRequest{Name: "Fruit", Kind: mimirv1.DatasetKind_DATASET_KIND_TABLE, Csv: []byte(blobs(400))}))
	if err != nil {
		t.Fatal(err)
	}
	job, err := own.training.CreateJob(ctx, connect.NewRequest(&mimirv1.CreateJobRequest{Name: "Slow", Architecture: "table-mlp", DatasetId: ds.Msg.GetDataset().GetId(), Settings: &mimirv1.TrainingSettings{Epochs: 6, BatchSize: 4}}))
	if err != nil {
		t.Fatal(err)
	}
	id := job.Msg.GetJob().GetId()
	// Cancel while queued, resume, then cancel mid-run from another goroutine.
	if c, err := own.training.CancelJob(ctx, connect.NewRequest(&mimirv1.CancelJobRequest{Id: id})); err != nil || c.Msg.GetJob().GetStatus() != mimirv1.JobStatus_JOB_STATUS_CANCELLED {
		t.Fatalf("cancel queued: %v, %v", c, err)
	}
	if r, err := own.training.ResumeJob(ctx, connect.NewRequest(&mimirv1.ResumeJobRequest{Id: id})); err != nil || r.Msg.GetJob().GetStatus() != mimirv1.JobStatus_JOB_STATUS_QUEUED {
		t.Fatalf("resume: %v, %v", r, err)
	}
	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		for {
			j, _ := own.training.GetJob(ctx, connect.NewRequest(&mimirv1.GetJobRequest{Id: id}))
			if j != nil && j.Msg.GetJob().GetEpoch() >= 1 {
				own.training.CancelJob(ctx, connect.NewRequest(&mimirv1.CancelJobRequest{Id: id}))
				return
			}
			if j != nil && j.Msg.GetJob().GetStatus() > mimirv1.JobStatus_JOB_STATUS_RUNNING {
				return
			}
		}
	}()
	runner.Tick(ctx)
	<-stopped
	j, _ := own.training.GetJob(ctx, connect.NewRequest(&mimirv1.GetJobRequest{Id: id}))
	jb := j.Msg.GetJob()
	if jb.GetStatus() != mimirv1.JobStatus_JOB_STATUS_CANCELLED || jb.GetEpoch() < 1 || jb.GetEpoch() >= 6 {
		t.Fatalf("after cancel: status %v epoch %d", jb.GetStatus(), jb.GetEpoch())
	}
	done := jb.GetEpoch()
	// Resuming picks up where it stopped rather than starting over.
	if _, err := own.training.ResumeJob(ctx, connect.NewRequest(&mimirv1.ResumeJobRequest{Id: id})); err != nil {
		t.Fatal(err)
	}
	runner.Tick(ctx)
	j, _ = own.training.GetJob(ctx, connect.NewRequest(&mimirv1.GetJobRequest{Id: id}))
	jb = j.Msg.GetJob()
	if jb.GetStatus() != mimirv1.JobStatus_JOB_STATUS_SUCCEEDED || len(jb.GetHistory()) != 6 || jb.GetHistory()[0].GetEpoch() != 1 {
		t.Fatalf("after resume: status %v history %d (stopped at %d)", jb.GetStatus(), len(jb.GetHistory()), done)
	}
	if _, err := own.training.DeleteJob(ctx, connect.NewRequest(&mimirv1.DeleteJobRequest{Id: id})); err != nil {
		t.Fatal(err)
	}
}
