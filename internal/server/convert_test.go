package server_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"connectrpc.com/connect"

	mimirv1 "github.com/gsoultan/mimir-server/gen/mimir/v1"
)

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile("../convert/testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// TestONNXUpload uploads an ONNX model, which is stored as a Mimir Model
// file and answers like any other.
func TestONNXUpload(t *testing.T) {
	ctx := context.Background()
	srv := newServer(t)
	c := owner(t, srv)

	res, err := c.models.CreateModel(ctx, connect.NewRequest(&mimirv1.CreateModelRequest{Name: "CNN", Architecture: "mim", Weights: fixture(t, "tiny-cnn.onnx")}))
	if err != nil {
		t.Fatal(err)
	}
	m := res.Msg.GetModel()
	if m.GetArchitecture() != "mim" || len(m.GetInputShape()) != 3 || m.GetInputShape()[0] != 3 {
		t.Fatalf("model from an ONNX file: %v", m)
	}
	if w, err := os.ReadFile(filepath.Join(svc.DataDir(), "models", m.GetId()+".mim")); err != nil || !strings.HasPrefix(string(w), "MIM\x00") {
		t.Fatalf("stored as something other than a Mimir Model file: %v", err)
	}
	p, err := c.predict.Predict(ctx, connect.NewRequest(&mimirv1.PredictRequest{ModelId: m.GetId(), Input: &mimirv1.Tensor{Values: make([]float32, 3*16*16)}}))
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Msg.GetOutput().GetValues()) != 10 {
		t.Fatalf("output %v", p.Msg.GetOutput())
	}
	if _, err := c.predict.Generate(ctx, connect.NewRequest(&mimirv1.GenerateRequest{ModelId: m.GetId(), Prompt: "hi"})); code(err) != connect.CodeInvalidArgument {
		t.Errorf("Generate with a CNN: %v", err)
	}
	events, err := c.audit.ListAuditEvents(ctx, connect.NewRequest(&mimirv1.ListAuditEventsRequest{PageSize: 1}))
	if err != nil || !strings.Contains(events.Msg.GetEvents()[0].GetDetail(), "converted from ONNX") {
		t.Errorf("audit event %v, %v", events, err)
	}
}

// TestGGUFUpload uploads a GGUF language model and has it write.
func TestGGUFUpload(t *testing.T) {
	ctx := context.Background()
	srv := newServer(t)
	c := owner(t, srv)

	for _, as := range []string{"mim", "language-model"} {
		res, err := c.models.CreateModel(ctx, connect.NewRequest(&mimirv1.CreateModelRequest{Name: "Llama", Architecture: as, Weights: fixture(t, "tiny-llama.gguf")}))
		if err != nil {
			t.Fatal(err)
		}
		m := res.Msg.GetModel()
		if m.GetArchitecture() != "language-model" {
			t.Fatalf("uploaded as %s: architecture %s", as, m.GetArchitecture())
		}
		g, err := c.predict.Generate(ctx, connect.NewRequest(&mimirv1.GenerateRequest{ModelId: m.GetId(), Prompt: "Once upon a time", MaxTokens: 5}))
		if err != nil {
			t.Fatal(err)
		}
		if g.Msg.GetPromptTokens() == 0 || g.Msg.GetGeneratedTokens() == 0 || g.Msg.GetGeneratedTokens() > 5 {
			t.Fatalf("generation %v", g.Msg)
		}
		if _, err := c.predict.Predict(ctx, connect.NewRequest(&mimirv1.PredictRequest{ModelId: m.GetId(), Input: &mimirv1.Tensor{Values: []float32{1}}})); code(err) != connect.CodeInvalidArgument {
			t.Errorf("Predict with a language model: %v", err)
		}
		for _, bad := range []*mimirv1.GenerateRequest{
			{ModelId: m.GetId(), Prompt: "  "},
			{ModelId: m.GetId(), Prompt: "hi", MaxTokens: 5000},
			{ModelId: m.GetId(), Prompt: "hi", Temperature: 3},
			{ModelId: m.GetId(), Prompt: strings.Repeat("a b ", 200)}, // longer than the tiny model's context
		} {
			if _, err := c.predict.Generate(ctx, connect.NewRequest(bad)); code(err) != connect.CodeInvalidArgument {
				t.Errorf("Generate(%v): %v", bad, err)
			}
		}
		if _, err := c.models.SetModelWorker(ctx, connect.NewRequest(&mimirv1.SetModelWorkerRequest{Id: m.GetId(), WorkerId: "w"})); code(err) != connect.CodeFailedPrecondition {
			t.Errorf("placing a language model on a worker: %v", err)
		}
	}
}

func TestConvertErrors(t *testing.T) {
	ctx := context.Background()
	srv := newServer(t)
	c := owner(t, srv)
	for _, tc := range []struct {
		name, arch, want string
		file             []byte
	}{
		{"GGUF weights alone", "mim", "This GGUF model can't be used here", fixture(t, "tiny.gguf")},
		{"broken ONNX", "mim", "This ONNX model can't be used here", fixture(t, "tiny-cnn.onnx")[:200]},
		{"safetensors", "language-model", "safetensors", append([]byte{2, 0, 0, 0, 0, 0, 0, 0}, "{}"...)},
		{"junk", "mim", "isn't a .mim, ONNX or GGUF", []byte("not a model")},
	} {
		_, err := c.models.CreateModel(ctx, connect.NewRequest(&mimirv1.CreateModelRequest{Name: "Bad", Architecture: tc.arch, Weights: tc.file}))
		if code(err) != connect.CodeInvalidArgument || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: %v", tc.name, err)
		}
	}
}
