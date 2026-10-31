package runtime

import (
	"math"
	"testing"

	"github.com/gsoultan/mimir/nn"
	"github.com/gsoultan/mimir/tensor"
)

// TestReplayerMatchesForward checks that replayed runs answer as the
// module's own forward pass does, for new inputs and several batch sizes.
func TestReplayerMatchesForward(t *testing.T) {
	mod := nn.NewSequential(nn.NewLinear(5, 8), nn.ReLU(), nn.NewLinear(8, 3))
	nn.Eval(mod)
	r := newReplayer(mod, []int{5})
	for i, n := range []int{1, 4, 1, 4, 1, 2} {
		x := make([]float32, n*5)
		for j := range x {
			x[j] = float32(math.Sin(float64(i*31 + j)))
		}
		got, err := r.run(t.Context(), x, n)
		if err != nil {
			t.Fatal(err)
		}
		want, err := mod.Forward(tensor.FromSlice(x, n, 5)).Reshape(-1).Data[float32](t.Context())
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != n {
			t.Fatalf("run %d: %d outputs, want %d", i, len(got), n)
		}
		for k, row := range got {
			for c, v := range row {
				if w := want[k*3+c]; math.Abs(float64(v-w)) > 1e-6 {
					t.Errorf("run %d, input %d, output %d: %v, want %v", i, k, c, v, w)
				}
			}
		}
	}
	if len(r.recs) != 3 {
		t.Errorf("%d batch sizes recorded, want 3", len(r.recs))
	}
	for n, rec := range r.recs {
		if rec == nil {
			t.Errorf("batch of %d not recorded", n)
		}
	}
}
