package runtime

import (
	"bytes"
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/gsoultan/mimir/io/mim"
	"github.com/gsoultan/mimir/tensor"
)

// Questions to one uploaded model run side by side, each in a session of
// its own, and every answer is its own input's.
func TestFileForwardConcurrent(t *testing.T) {
	ctx := context.Background()
	double := func(x ...*tensor.Tensor) []*tensor.Tensor { return []*tensor.Tensor{x[0].Add(x[0])} }
	var buf bytes.Buffer
	if err := mim.ExportFunc(ctx, &buf, double, []*tensor.Tensor{tensor.Zeros(tensor.Float32, 1, 4)}, nil); err != nil {
		t.Fatal(err)
	}
	f, err := OpenFile(buf.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	var wg sync.WaitGroup
	for g := range 8 {
		wg.Go(func() {
			for i := range 20 {
				v := float32(g*100 + i)
				out, err := f.Forward(ctx, tensor.FromSlice([]float32{v, v, v, v}, 4)).Data[float32](ctx)
				if err != nil {
					t.Error(err)
					return
				}
				for _, y := range out {
					if y != 2*v {
						t.Errorf("Forward(%v) = %v, want %v", v, out, 2*v)
						return
					}
				}
			}
		})
	}
	wg.Wait()
}

// A file loaded under a memory budget runs on the interpreter within that
// budget: a run that would take more fails as mimir's own budget error,
// and a file loaded under another budget runs on mimir, which bounds
// itself.
func TestFileInterpreterKeepsMemoryBudget(t *testing.T) {
	ctx := context.Background()
	double := func(x ...*tensor.Tensor) []*tensor.Tensor { return []*tensor.Tensor{x[0].Add(x[0])} }
	var buf bytes.Buffer
	if err := mim.ExportFunc(ctx, &buf, double, []*tensor.Tensor{tensor.Zeros(tensor.Float32, 1, 4)}, nil); err != nil {
		t.Fatal(err)
	}
	dev := tensor.DefaultDevice()
	prev := tensor.MemoryBudget(dev)
	defer tensor.SetMemoryBudget(dev, prev)

	tensor.SetMemoryBudget(dev, 1)
	f, err := OpenFile(buf.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if f.prog == nil {
		t.Skip("the interpreter does not run this file")
	}
	if got := f.Engine(); got != "interpreter" {
		t.Fatalf("Engine() = %q under the budget the file was loaded with", got)
	}
	if _, err := f.Run(ctx, []float32{1, 2, 3, 4}, 1); !errors.Is(err, tensor.ErrMemoryBudget) {
		t.Fatalf("Run over a 1-byte budget: %v, want ErrMemoryBudget", err)
	}
	if f.noInterp.Load() {
		t.Fatal("a run over the budget sent the file to mimir for good")
	}

	tensor.SetMemoryBudget(dev, prev)
	if got := f.Engine(); got != "mimir" {
		t.Fatalf("Engine() = %q under another budget", got)
	}
	out, err := f.Run(ctx, []float32{1, 2, 3, 4}, 1)
	if err != nil || len(out) != 1 || out[0][3] != 8 {
		t.Fatalf("Run on mimir = %v, %v", out, err)
	}
}
