package server_test

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"connectrpc.com/connect"

	mimirv1 "github.com/gsoultan/mimir-server/gen/mimir/v1"
)

// chats is a JSON Lines dataset of n chats that all answer the same.
func chats(n int) []byte {
	var b strings.Builder
	for i := range n {
		fmt.Fprintf(&b, `{"messages":[{"role":"system","content":"Be brief."},{"role":"user","content":"Question %d: what is the secret word?"},{"role":"assistant","content":"Pineapple."}]}`+"\n", i)
	}
	return []byte(b.String())
}

// TestFineTune fine-tunes a language model on chats, here and on a
// worker, and checks the fine-tune chats, embeds and keeps its base.
func TestFineTune(t *testing.T) {
	srv := newServer(t)
	ctx := context.Background()
	own := owner(t, srv)
	base := languageModel(t, own, "Tiny Llama")

	for _, bad := range [][]byte{
		nil,
		[]byte(`{"messages":[{"role":"user","content":"hi"}]}`),
		[]byte(`{"prompt":"hi"}`),
		[]byte(`{"messages":[{"role":"tool","content":"hi"},{"role":"assistant","content":"x"}]}`),
		[]byte("{\"text\":\"fine\"}\nnot json"),
		{0xff, 0xfe, 0x00},
	} {
		_, err := own.datasets.CreateDataset(ctx, connect.NewRequest(&mimirv1.CreateDatasetRequest{Name: "Bad", Kind: mimirv1.DatasetKind_DATASET_KIND_TEXT, Text: bad}))
		if code(err) != connect.CodeInvalidArgument {
			t.Errorf("dataset %q: %v", bad, err)
		}
	}
	ds, err := own.datasets.CreateDataset(ctx, connect.NewRequest(&mimirv1.CreateDatasetRequest{Name: "Secret", Kind: mimirv1.DatasetKind_DATASET_KIND_TEXT, Text: chats(12)}))
	if err != nil {
		t.Fatal(err)
	}
	if d := ds.Msg.GetDataset(); d.GetRows() != 12 || strings.Join(d.GetArchitectures(), ",") != "language-model" || strings.Join(d.GetLabels(), ",") != "chats" {
		t.Fatalf("dataset %v", d)
	}
	doc, err := own.datasets.CreateDataset(ctx, connect.NewRequest(&mimirv1.CreateDatasetRequest{Name: "Notes", Kind: mimirv1.DatasetKind_DATASET_KIND_TEXT, Text: []byte("The first paragraph.\n\nThe second one, a little longer.")}))
	if err != nil || doc.Msg.GetDataset().GetRows() != 1 || doc.Msg.GetDataset().GetLabels()[0] != "passages" {
		t.Fatalf("document dataset %v, %v", doc, err)
	}

	job := func(name, from string) (*mimirv1.Job, error) {
		res, err := own.training.CreateJob(ctx, connect.NewRequest(&mimirv1.CreateJobRequest{
			Name: name, Architecture: "language-model", DatasetId: ds.Msg.GetDataset().GetId(), BaseModelId: from,
			Settings: &mimirv1.TrainingSettings{Epochs: 3, LearningRate: 1e-2},
		}))
		if err != nil {
			return nil, err
		}
		return res.Msg.GetJob(), nil
	}
	if _, err := job("No base", ""); code(err) != connect.CodeInvalidArgument {
		t.Errorf("fine-tune of nothing: %v", err)
	}
	if _, err := own.training.CreateJob(ctx, connect.NewRequest(&mimirv1.CreateJobRequest{Name: "Table", Architecture: "table-mlp", DatasetId: ds.Msg.GetDataset().GetId()})); code(err) != connect.CodeInvalidArgument {
		t.Errorf("table model on writing: %v", err)
	}
	if _, err := own.training.CreateJob(ctx, connect.NewRequest(&mimirv1.CreateJobRequest{Name: "Repeat", Architecture: "language-model", DatasetId: ds.Msg.GetDataset().GetId(), BaseModelId: base.GetId(), RepeatSeconds: 86400})); code(err) != connect.CodeInvalidArgument {
		t.Errorf("fine-tune on a schedule: %v", err)
	}

	// run fine-tunes from, here or on a worker, and returns the model.
	run := func(name, from string) (*mimirv1.Model, *mimirv1.Job) {
		t.Helper()
		j, err := job(name, from)
		if err != nil {
			t.Fatal(err)
		}
		if s := j.GetSettings(); s.GetBatchSize() != 4 || s.GetHoldOut() < 0.09 || s.GetHoldOut() > 0.11 {
			t.Errorf("settings %v", s)
		}
		runner.Tick(ctx)
		res, err := own.training.GetJob(ctx, connect.NewRequest(&mimirv1.GetJobRequest{Id: j.GetId()}))
		if err != nil {
			t.Fatal(err)
		}
		done := res.Msg.GetJob()
		if done.GetStatus() != mimirv1.JobStatus_JOB_STATUS_SUCCEEDED {
			t.Fatalf("job %v", done)
		}
		h := done.GetHistory()
		if len(h) != 3 || !(h[2].GetLoss() < h[0].GetLoss()) || h[2].GetValLoss() == 0 {
			t.Errorf("history %v", h)
		}
		m, err := own.models.GetModel(ctx, connect.NewRequest(&mimirv1.GetModelRequest{Id: done.GetModelId()}))
		if err != nil {
			t.Fatal(err)
		}
		return m.Msg.GetModel(), done
	}

	tuned, _ := run("Secret keeper", base.GetId())
	if tuned.GetArchitecture() != "language-model" || tuned.GetBaseModelId() != base.GetId() || tuned.GetBaseModelName() != "Tiny Llama" || tuned.GetSizeBytes() >= base.GetSizeBytes() || tuned.GetWorkerId() != "" {
		t.Fatalf("fine-tune %v", tuned)
	}
	stream, err := own.predict.Chat(ctx, connect.NewRequest(&mimirv1.ChatRequest{ModelId: tuned.GetId(), Messages: []*mimirv1.ChatMessage{{Role: "user", Content: "What is the secret word?"}}, MaxTokens: 4}))
	if err != nil {
		t.Fatal(err)
	}
	for stream.Receive() {
	}
	if err := stream.Err(); err != nil {
		t.Fatalf("chat with the fine-tune: %v", err)
	}
	g, err := own.predict.Generate(ctx, connect.NewRequest(&mimirv1.GenerateRequest{ModelId: tuned.GetId(), Prompt: "Once upon a time", MaxTokens: 3}))
	if err != nil || g.Msg.GetGeneratedTokens() == 0 {
		t.Fatalf("generate with the fine-tune: %v, %v", g, err)
	}

	// A fine-tune of the fine-tune runs on the first one's base too.
	again, _ := run("Secret keeper 2", tuned.GetId())
	if again.GetBaseModelId() != base.GetId() {
		t.Errorf("fine-tune of a fine-tune has base %q", again.GetBaseModelId())
	}

	// On a worker.
	svc.Local, runner.Local = false, false
	joinWorker(t, own, srv.URL, "tuner")
	remote, j := run("Secret keeper 3", base.GetId())
	if j.GetWorker() != "tuner" || remote.GetWorkerId() != "" || remote.GetBaseModelId() != base.GetId() {
		t.Errorf("fine-tune on a worker: job on %q, model %v", j.GetWorker(), remote)
	}
	svc.Local = true

	if _, err := own.models.DeleteModel(ctx, connect.NewRequest(&mimirv1.DeleteModelRequest{Id: base.GetId()})); code(err) != connect.CodeFailedPrecondition {
		t.Errorf("deleting a base with fine-tunes: %v", err)
	}
	for _, m := range []*mimirv1.Model{tuned, again, remote, base} {
		if _, err := own.models.DeleteModel(ctx, connect.NewRequest(&mimirv1.DeleteModelRequest{Id: m.GetId()})); err != nil {
			t.Errorf("delete %s: %v", m.GetName(), err)
		}
	}
}
