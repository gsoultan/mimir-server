package server_test

import (
	"context"
	"testing"

	"connectrpc.com/connect"

	mimirv1 "github.com/gsoultan/mimir-server/gen/mimir/v1"
)

// TestDesignedModel teaches table models shaped step by step, here and
// on a worker, and checks they answer, which means the shape travelled
// with the weights.
func TestDesignedModel(t *testing.T) {
	srv := newServer(t)
	ctx := context.Background()
	own := owner(t, srv)
	ds, err := own.datasets.CreateDataset(ctx, connect.NewRequest(&mimirv1.CreateDatasetRequest{Name: "Fruit", Kind: mimirv1.DatasetKind_DATASET_KIND_TABLE, Csv: []byte(blobs(200))}))
	if err != nil {
		t.Fatal(err)
	}
	design := []*mimirv1.Layer{{Kind: "dense", Units: 12}, {Kind: "dropout", Rate: 0.1}, {Kind: "dense", Units: 6}}
	job := func(arch string, layers []*mimirv1.Layer) (*mimirv1.Job, error) {
		res, err := own.training.CreateJob(ctx, connect.NewRequest(&mimirv1.CreateJobRequest{Name: "Designed", Architecture: arch, DatasetId: ds.Msg.GetDataset().GetId(), Settings: &mimirv1.TrainingSettings{Epochs: 3, LearningRate: 0.01, Layers: layers}}))
		if err != nil {
			return nil, err
		}
		return res.Msg.GetJob(), nil
	}
	for _, bad := range [][]*mimirv1.Layer{
		{{Kind: "dropout", Rate: 0.2}},
		{{Kind: "dense", Units: 5000}},
		{{Kind: "conv", Units: 3}},
		{{Kind: "dense", Units: 4}, {Kind: "dropout", Rate: 1}},
	} {
		if _, err := job("table-mlp", bad); code(err) != connect.CodeInvalidArgument {
			t.Errorf("design %v: %v", bad, err)
		}
	}
	if _, err := job("mnist-mlp", design); code(err) != connect.CodeInvalidArgument {
		t.Errorf("design for a fixed model: %v", err)
	}

	run := func() string {
		t.Helper()
		j, err := job("table-mlp", design)
		if err != nil {
			t.Fatal(err)
		}
		if got := j.GetSettings().GetLayers(); len(got) != 3 || got[1].GetRate() != 0.1 {
			t.Fatalf("job layers = %v", got)
		}
		runner.Tick(ctx)
		res, _ := own.training.GetJob(ctx, connect.NewRequest(&mimirv1.GetJobRequest{Id: j.GetId()}))
		if res.Msg.GetJob().GetStatus() != mimirv1.JobStatus_JOB_STATUS_SUCCEEDED {
			t.Fatalf("job = %v", res.Msg.GetJob())
		}
		model := res.Msg.GetJob().GetModelId()
		p, err := own.predict.Predict(ctx, connect.NewRequest(&mimirv1.PredictRequest{ModelId: model, Input: &mimirv1.Tensor{Values: []float32{1, 1}}}))
		if err != nil || len(p.Msg.GetGuesses()) == 0 {
			t.Fatalf("predict = %v, %v", p, err)
		}
		return res.Msg.GetJob().GetWorker()
	}
	if w := run(); w != "" {
		t.Fatalf("taught here, but by %q", w)
	}
	svc.Local, runner.Local = false, false
	joinWorker(t, own, srv.URL, "shaper")
	if w := run(); w != "shaper" {
		t.Fatalf("taught by %q, want the worker", w)
	}
}
