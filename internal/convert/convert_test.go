package convert_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"math"
	"os"
	"strings"
	"testing"

	mimfile "github.com/gsoultan/mimir-model"

	"github.com/gsoultan/mimir-server/internal/convert"
	"github.com/gsoultan/mimir-server/internal/runtime"
	"github.com/gsoultan/mimir/tensor"
)

func read(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func floats(t *testing.T, s string) []float32 {
	t.Helper()
	b, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		t.Fatal(err)
	}
	out := make([]float32, len(b)/4)
	for i := range out {
		out[i] = math.Float32frombits(binary.LittleEndian.Uint32(b[4*i:]))
	}
	return out
}

// TestONNX converts a small CNN and checks it answers what onnxruntime
// answered for the same input.
func TestONNX(t *testing.T) {
	ctx := context.Background()
	b, f, err := convert.ToMim(read(t, "tiny-cnn.onnx"))
	if err != nil || f != convert.ONNX || convert.Detect(b) != convert.Mim {
		t.Fatalf("convert: %v, %s", err, f)
	}
	again, f, err := convert.ToMim(b)
	if err != nil || f != convert.Mim || &again[0] != &b[0] {
		t.Fatalf("a .mim file is kept as it is: %v, %s", err, f)
	}
	if runtime.IsLanguageModel(b) {
		t.Fatal("a CNN taken for a language model")
	}
	file, err := runtime.OpenFile(b)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	if got := file.Spec.InputShape; len(got) != 3 || got[0] != 3 || got[1] != 16 || got[2] != 16 {
		t.Fatalf("input shape %v", got)
	}
	var want struct {
		Tolerance float64
		Runs      []struct {
			Inputs, Outputs map[string]struct {
				Shape []int
				Data  string
			}
		}
	}
	if err := json.Unmarshal(read(t, "tiny-cnn.json"), &want); err != nil {
		t.Fatal(err)
	}
	checked := 0
	for _, run := range want.Runs {
		x := run.Inputs["x"]
		if x.Shape[0] != 1 {
			continue // one question at a time here
		}
		got, err := file.Forward(ctx, tensor.FromSlice(floats(t, x.Data), x.Shape...)).Reshape(-1).Data[float32](ctx)
		if err != nil {
			t.Fatal(err)
		}
		exp := floats(t, run.Outputs["probs"].Data)
		if len(got) != len(exp) {
			t.Fatalf("%d outputs, want %d", len(got), len(exp))
		}
		for i := range exp {
			if math.Abs(float64(got[i]-exp[i])) > 1e-4 {
				t.Fatalf("output %v, want %v", got, exp)
			}
		}
		checked++
	}
	if checked == 0 {
		t.Fatal("no run of one input to check")
	}
}

// TestGGUF converts a tiny Llama and has it write.
func TestGGUF(t *testing.T) {
	ctx := context.Background()
	b, f, err := convert.ToMim(read(t, "tiny-llama.gguf"))
	if err != nil || f != convert.GGUF {
		t.Fatalf("convert: %v, %s", err, f)
	}
	if !runtime.IsLanguageModel(b) {
		t.Fatal("not taken for a language model")
	}
	lm, err := runtime.OpenLanguageModel(b)
	if err != nil {
		t.Fatal(err)
	}
	defer lm.Close()
	if lm.Context() != 64 { // the tiny model's own, under convert.Context
		t.Errorf("context %d", lm.Context())
	}
	g, err := lm.Generate(ctx, "Once upon a time", runtime.Sampling{MaxTokens: 8})
	if err != nil {
		t.Fatal(err)
	}
	if g.PromptTokens == 0 || g.GeneratedTokens == 0 || g.GeneratedTokens > 8 || (g.Text == "" && !g.Finished) {
		t.Fatalf("generation %+v", g)
	}
	same, err := lm.Generate(ctx, "Once upon a time", runtime.Sampling{MaxTokens: 8})
	if err != nil || same != g {
		t.Fatalf("greedy generation isn't repeatable: %+v, %+v (%v)", g, same, err)
	}
	if _, err := lm.Generate(ctx, "Once upon a time", runtime.Sampling{MaxTokens: 8, Temperature: 0.8, Seed: 3}); err != nil {
		t.Fatal(err)
	}
	if _, err := lm.Generate(ctx, "", runtime.Sampling{MaxTokens: 8}); !errors.Is(err, runtime.ErrBadInput) {
		t.Errorf("empty prompt: %v", err)
	}
	if _, err := lm.Generate(ctx, strings.Repeat("a b c d ", 100), runtime.Sampling{MaxTokens: 8}); !errors.Is(err, runtime.ErrBadInput) {
		t.Errorf("long prompt: %v", err)
	}
	t.Logf("%q", g.Text)
}

func TestUnsupported(t *testing.T) {
	for name, tc := range map[string]struct {
		b    []byte
		want string
	}{
		"gguf without a llama": {read(t, "tiny.gguf"), "whole llama model"},
		"safetensors":          {append([]byte{2, 0, 0, 0, 0, 0, 0, 0}, "{}"...), "safetensors"},
		"junk":                 {[]byte("hello"), "isn't a .mim, ONNX or GGUF"},
		"broken onnx":          {[]byte{0x08, 0x07, 0x12, 0xff}, ""},
	} {
		_, _, err := convert.ToMim(tc.b)
		if !errors.Is(err, convert.ErrUnsupported) || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: %v", name, err)
		}
	}
}

// TestDamagedMim rejects a .mim file whose bytes no longer match the
// section hashes written with it.
func TestDamagedMim(t *testing.T) {
	b, _, err := convert.ToMim(read(t, "tiny-cnn.onnx"))
	if err != nil {
		t.Fatal(err)
	}
	f, err := mimfile.Parse(b)
	if err != nil {
		t.Fatal(err)
	}
	w := f.TensorAt(0)
	damaged := bytes.Clone(b)
	damaged[int(f.Preamble().DataOffset)+int(w.Offset)] ^= 0x40 // one bit of a weight
	_, _, err = convert.ToMim(damaged)
	if !errors.Is(err, convert.ErrUnsupported) || !strings.Contains(err.Error(), "damaged") {
		t.Fatalf("damaged file: %v, want ErrUnsupported saying it is damaged", err)
	}
}
