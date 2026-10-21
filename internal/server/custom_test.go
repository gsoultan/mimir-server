package server_test

import (
	"context"
	"io"
	"log/slog"
	"testing"

	"connectrpc.com/connect"

	mimirv1 "github.com/gsoultan/mimir-server/gen/mimir/v1"
	"github.com/gsoultan/mimir-server/internal/arch"
	"github.com/gsoultan/mimir-server/worker"
)

// ranker is a model in someone's own code: it ranks the labels by how
// close each one's index is to the first number it reads.
type ranker struct{}

func (ranker) Predict(_ context.Context, m *worker.Model, in []float32) (worker.Prediction, error) {
	p := make([]float32, len(m.Labels))
	for i := range p {
		d := float32(i) - in[0]
		p[i] = 1 / (1 + d*d)
	}
	return worker.Prediction{Probabilities: p}, nil
}

func (ranker) Train(_ context.Context, t worker.TrainTask, progress func(worker.Progress)) (worker.Trained, error) {
	h := []worker.Epoch{{Epoch: 1, Accuracy: 0.75}}
	progress(worker.Progress{Epoch: 1, History: h})
	return worker.Trained{Weights: []byte("learned"), History: h, InputShape: []int{2}, Labels: []string{"apple", "pear"}, Features: []string{"width", "height"}}, nil
}

// TestCustomArchitecture describes a model type written in Go, uploads
// and teaches models of it, and has a worker running that code answer.
func TestCustomArchitecture(t *testing.T) {
	srv := newServer(t)
	t.Cleanup(func() { arch.SetCustom(nil) })
	ctx := context.Background()
	own := owner(t, srv)

	save := func(r *mimirv1.SaveArchitectureRequest) (*mimirv1.Architecture, error) {
		res, err := own.models.SaveArchitecture(ctx, connect.NewRequest(r))
		if err != nil {
			return nil, err
		}
		return res.Msg.GetArchitecture(), nil
	}
	if _, err := save(&mimirv1.SaveArchitectureRequest{Name: "Shop Recs", Input: "table", Output: "ranked", Items: 2}); code(err) != connect.CodeInvalidArgument {
		t.Fatalf("bad name: %v", err)
	}
	if _, err := save(&mimirv1.SaveArchitectureRequest{Name: "table-mlp", Input: "table", Output: "class"}); code(err) != connect.CodeInvalidArgument {
		t.Fatalf("built-in name: %v", err)
	}
	if _, err := save(&mimirv1.SaveArchitectureRequest{Name: "shop-recs", Input: "table", Output: "ranked"}); code(err) != connect.CodeInvalidArgument {
		t.Fatalf("ranked with no items: %v", err)
	}
	a, err := save(&mimirv1.SaveArchitectureRequest{Name: "shop-recs", Title: "Shop recommendations", Input: "table", Output: "ranked", Items: 2})
	if err != nil || !a.GetCustom() || a.GetItems() != 2 || a.GetOutput() != "ranked" || !a.GetTrainable() {
		t.Fatalf("SaveArchitecture = %v, %v", a, err)
	}
	list, _ := own.models.ListArchitectures(ctx, connect.NewRequest(&mimirv1.ListArchitecturesRequest{}))
	found := false
	for _, x := range list.Msg.GetArchitectures() {
		found = found || x.GetName() == "shop-recs" && x.GetCustom()
	}
	if !found {
		t.Fatal("ListArchitectures doesn't list shop-recs")
	}

	// An upload says what the model reads and answers.
	create := func(shape []int64, labels []string) (*mimirv1.Model, error) {
		res, err := own.models.CreateModel(ctx, connect.NewRequest(&mimirv1.CreateModelRequest{Name: "Recs", Architecture: "shop-recs", Weights: []byte("weights"), InputShape: shape, Labels: labels}))
		if err != nil {
			return nil, err
		}
		return res.Msg.GetModel(), nil
	}
	if _, err := create(nil, []string{"a", "b"}); code(err) != connect.CodeInvalidArgument {
		t.Fatalf("no shape: %v", err)
	}
	if _, err := create([]int64{1}, []string{"a", "a"}); code(err) != connect.CodeInvalidArgument {
		t.Fatalf("repeated label: %v", err)
	}
	m, err := create([]int64{1}, []string{"tea", "pot", "cup"})
	if err != nil {
		t.Fatal(err)
	}
	ask := func() (*mimirv1.PredictResponse, error) {
		res, err := own.predict.Predict(ctx, connect.NewRequest(&mimirv1.PredictRequest{ModelId: m.GetId(), Input: &mimirv1.Tensor{Values: []float32{2}}}))
		if err != nil {
			return nil, err
		}
		return res.Msg, nil
	}
	// The console can't run its code, even though it runs models itself.
	if _, err := ask(); code(err) != connect.CodeUnavailable {
		t.Fatalf("no worker: %v", err)
	}

	created, err := own.workers.CreateWorker(ctx, connect.NewRequest(&mimirv1.CreateWorkerRequest{Name: "recs-box"}))
	if err != nil {
		t.Fatal(err)
	}
	wctx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		worker.Run(wctx, worker.Config{Server: srv.URL, Token: created.Msg.GetToken(), DataDir: t.TempDir(), Log: slog.New(slog.NewTextHandler(io.Discard, nil))}, map[string]worker.Handler{"shop-recs": ranker{}})
	}()
	t.Cleanup(func() { cancel(); <-done })
	w := waitOnline(t, own, created.Msg.GetWorker().GetId(), true)
	if len(w.GetArchitectures()) != 1 || w.GetArchitectures()[0] != "shop-recs" || !w.GetTrains() {
		t.Fatalf("worker = %v", w)
	}

	// A ranked answer lists the type's number of items, best first.
	res, err := ask()
	if err != nil {
		t.Fatal(err)
	}
	if g := res.GetGuesses(); len(g) != 2 || g[0].GetLabel() != "cup" || g[1].GetLabel() != "pot" {
		t.Fatalf("guesses = %v", g)
	}

	// Teaching it from a table runs its own Train on the worker.
	ds, err := own.datasets.CreateDataset(ctx, connect.NewRequest(&mimirv1.CreateDatasetRequest{Name: "Fruit", Kind: mimirv1.DatasetKind_DATASET_KIND_TABLE, Csv: []byte(blobs(50))}))
	if err != nil {
		t.Fatal(err)
	}
	j, err := own.training.CreateJob(ctx, connect.NewRequest(&mimirv1.CreateJobRequest{Name: "Taught recs", Architecture: "shop-recs", DatasetId: ds.Msg.GetDataset().GetId()}))
	if err != nil {
		t.Fatal(err)
	}
	runner.Tick(ctx)
	got, _ := own.training.GetJob(ctx, connect.NewRequest(&mimirv1.GetJobRequest{Id: j.Msg.GetJob().GetId()}))
	if got.Msg.GetJob().GetStatus() != mimirv1.JobStatus_JOB_STATUS_SUCCEEDED || got.Msg.GetJob().GetWorker() != "recs-box" {
		t.Fatalf("job = %v", got.Msg.GetJob())
	}

	// A type with models can't go; its describer can change it.
	if _, err := own.models.DeleteArchitecture(ctx, connect.NewRequest(&mimirv1.DeleteArchitectureRequest{Name: "shop-recs"})); code(err) != connect.CodeFailedPrecondition {
		t.Fatalf("delete in use: %v", err)
	}
	if a, err := save(&mimirv1.SaveArchitectureRequest{Name: "shop-recs", Title: "Recs", Input: "table", Output: "ranked", Items: 1}); err != nil || a.GetItems() != 1 {
		t.Fatalf("update = %v, %v", a, err)
	}
	if res, err := ask(); err != nil || len(res.GetGuesses()) != 1 {
		t.Fatalf("after update: %v, %v", res, err)
	}
}
