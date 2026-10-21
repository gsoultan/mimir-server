package runtime

import (
	"bytes"
	"context"
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
