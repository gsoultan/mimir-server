package server_test

import (
	"bytes"
	"context"
	"math"
	"slices"
	"strings"
	"testing"

	"connectrpc.com/connect"

	mimirv1 "github.com/gsoultan/mimir-server/gen/mimir/v1"
	"github.com/gsoultan/mimir-server/internal/arch"
	"github.com/gsoultan/mimir-server/internal/runtime"
	"github.com/gsoultan/mimir/io/mim"
	"github.com/gsoultan/mimir/nn"
	"github.com/gsoultan/mimir/tensor"
)

// digits returns a digit classifier with fresh weights, and the same model
// exported as a Mimir Model file whose metadata names its labels.
func digits(t *testing.T) (nn.Module, []byte) {
	t.Helper()
	a, _ := arch.Lookup("mnist-cnn")
	m := a.New(a.Spec)
	nn.Eval(m)
	var buf bytes.Buffer
	opts := &mim.ExportOptions{Metadata: map[string]any{runtime.LabelsKey: a.Spec.Labels}}
	if err := mim.Export(context.Background(), &buf, m, tensor.Zeros(tensor.Float32, 1, 1, 28, 28), opts); err != nil {
		t.Fatal(err)
	}
	return m, buf.Bytes()
}

// TestMimModels uploads a model as a .mim file, which holds its own graph,
// and asks it questions here and on a worker.
func TestMimModels(t *testing.T) {
	ctx := context.Background()
	srv := newServer(t)
	c := owner(t, srv)
	mod, file := digits(t)

	archs, err := c.models.ListArchitectures(ctx, connect.NewRequest(&mimirv1.ListArchitecturesRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	i := slices.IndexFunc(archs.Msg.GetArchitectures(), func(a *mimirv1.Architecture) bool { return a.GetName() == "mim" })
	if i < 0 || archs.Msg.GetArchitectures()[i].GetTrainable() || archs.Msg.GetArchitectures()[i].GetInput() != "tensor" {
		t.Fatalf("architectures %v", archs.Msg.GetArchitectures())
	}

	res, err := c.models.CreateModel(ctx, connect.NewRequest(&mimirv1.CreateModelRequest{Name: "Digits", Architecture: "mim", Weights: file}))
	if err != nil {
		t.Fatal(err)
	}
	m := res.Msg.GetModel()
	if !slices.Equal(m.GetInputShape(), []int64{1, 28, 28}) || len(m.GetLabels()) != 10 || m.GetLabels()[3] != "3" {
		t.Fatalf("model from a .mim file: %v", m)
	}

	input := make([]float32, 784)
	for i := range input {
		input[i] = float32(i%28) / 28
	}
	want, err := mod.Forward(tensor.FromSlice(input, 1, 1, 28, 28)).Reshape(-1).Data[float32](ctx)
	if err != nil {
		t.Fatal(err)
	}
	ask := func(how string) {
		t.Helper()
		p, err := c.predict.Predict(ctx, connect.NewRequest(&mimirv1.PredictRequest{ModelId: m.GetId(), Input: &mimirv1.Tensor{Values: input}}))
		if err != nil {
			t.Fatalf("%s: %v", how, err)
		}
		got := p.Msg.GetOutput().GetValues()
		if len(got) != len(want) {
			t.Fatalf("%s: %d outputs, want %d", how, len(got), len(want))
		}
		for i := range want {
			if math.Abs(float64(got[i]-want[i])) > 1e-4 {
				t.Fatalf("%s: output %v, want %v", how, got, want)
			}
		}
		if g := p.Msg.GetGuesses(); len(g) != 3 || g[0].GetProbability() < g[1].GetProbability() {
			t.Fatalf("%s: guesses %v", how, g)
		}
	}
	ask("here")

	// The same file on a worker, for a server that runs nothing itself.
	_, _, stop := joinWorker(t, c, srv.URL, "lab-1")
	defer stop()
	svc.Local = false
	for range 2 { // the second question reuses the file the link carried
		ask("on a worker")
	}
	svc.Local = true

	if _, err := c.predict.Predict(ctx, connect.NewRequest(&mimirv1.PredictRequest{ModelId: m.GetId(), Fields: map[string]string{"a": "1"}})); code(err) != connect.CodeInvalidArgument {
		t.Errorf("fields for a .mim model: %v", err)
	}
	if _, err := c.predict.Predict(ctx, connect.NewRequest(&mimirv1.PredictRequest{ModelId: m.GetId(), Input: &mimirv1.Tensor{Values: input[:10]}})); code(err) != connect.CodeInvalidArgument {
		t.Errorf("wrong input size: %v", err)
	}
}

func TestMimUploadErrors(t *testing.T) {
	ctx := context.Background()
	srv := newServer(t)
	c := owner(t, srv)
	_, file := digits(t)

	two := func(x ...*tensor.Tensor) []*tensor.Tensor { return []*tensor.Tensor{x[0].Add(x[1])} }
	var pair bytes.Buffer
	if err := mim.ExportFunc(ctx, &pair, two, []*tensor.Tensor{tensor.Zeros(tensor.Float32, 4), tensor.Zeros(tensor.Float32, 4)}, nil); err != nil {
		t.Fatal(err)
	}
	ids := func(x ...*tensor.Tensor) []*tensor.Tensor { return []*tensor.Tensor{x[0].Cast(tensor.Float32)} }
	var ints bytes.Buffer
	if err := mim.ExportFunc(ctx, &ints, ids, []*tensor.Tensor{tensor.Zeros(tensor.Int32, 4)}, nil); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, arch string
		file       []byte
	}{
		{"weights without a graph", "mim", weights(t, "mnist-mlp")},
		{"weights of another architecture", "mnist-cnn", weights(t, "mnist-mlp")},
		{"not a .mim file", "mnist-cnn", []byte("not a model")},
		{"two inputs", "mim", pair.Bytes()},
		{"an input of integers", "mim", ints.Bytes()},
		{"a cut-off file", "mim", file[:len(file)/2]},
	} {
		_, err := c.models.CreateModel(ctx, connect.NewRequest(&mimirv1.CreateModelRequest{Name: "Bad", Architecture: tc.arch, Weights: tc.file}))
		if code(err) != connect.CodeInvalidArgument {
			t.Errorf("%s: %v", tc.name, err)
		}
	}
}

// TestPredictOverMemoryBudget asks a model that cannot run in the memory
// mimir may take: the caller is told so, rather than "something went
// wrong".
func TestPredictOverMemoryBudget(t *testing.T) {
	ctx := context.Background()
	srv := newServer(t)
	c := owner(t, srv)
	_, file := digits(t)
	res, err := c.models.CreateModel(ctx, connect.NewRequest(&mimirv1.CreateModelRequest{Name: "Digits", Architecture: "mim", Weights: file}))
	if err != nil {
		t.Fatal(err)
	}
	dev := tensor.DefaultDevice()
	prev := tensor.MemoryBudget(dev)
	tensor.SetMemoryBudget(dev, 1)
	defer tensor.SetMemoryBudget(dev, prev)
	_, err = c.predict.Predict(ctx, connect.NewRequest(&mimirv1.PredictRequest{ModelId: res.Msg.GetModel().GetId(), Input: &mimirv1.Tensor{Values: make([]float32, 784)}}))
	if code(err) != connect.CodeResourceExhausted || !strings.Contains(err.Error(), "memory") {
		t.Fatalf("predict over the memory budget: %v, want ResourceExhausted naming memory", err)
	}
}
