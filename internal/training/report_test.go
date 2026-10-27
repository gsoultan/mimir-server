package training

import (
	"context"
	"math"
	"testing"

	"github.com/gsoultan/mimir/tensor"

	"github.com/gsoultan/mimir-server/internal/arch"
)

func near(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

func TestScorerClasses(t *testing.T) {
	ctx := context.Background()
	s := &scorer{kind: string(arch.Class), labels: []string{"no", "yes"}, confusion: [][]int64{{0, 0}, {0, 0}}}
	// Four examples: no→no, no→yes, yes→yes, yes→yes.
	pred := tensor.FromSlice([]float32{2, 0, 0, 1, 0, 3, 1, 2}, 4, 2)
	target := tensor.FromSlice([]int32{0, 0, 1, 1}, 4)
	if err := s.Update(ctx, pred, target); err != nil {
		t.Fatal(err)
	}
	r := s.report()
	if r.Examples != 4 || !near(r.Accuracy, 0.75) || r.Confusion[0][1] != 1 || r.Confusion[1][1] != 2 {
		t.Fatalf("report %+v", r)
	}
	no, yes := r.Classes[0], r.Classes[1]
	if !near(no.Precision, 1) || !near(no.Recall, 0.5) || !near(yes.Precision, 2.0/3) || !near(yes.Recall, 1) || yes.Support != 2 {
		t.Fatalf("classes %+v", r.Classes)
	}
	if !near(r.MacroF1, (2.0/3+0.8)/2) || r.LogLoss <= 0 {
		t.Fatalf("macro F1 %v, log loss %v", r.MacroF1, r.LogLoss)
	}
}

func TestScorerRanked(t *testing.T) {
	ctx := context.Background()
	s := &scorer{kind: string(arch.Ranked), labels: []string{"a", "b", "c"}, confusion: make([][]int64, 3), k: 2, hits: make([]float64, 2)}
	for i := range s.confusion {
		s.confusion[i] = make([]int64, 3)
	}
	// The right answer ranks 1st, then 2nd, then 3rd.
	pred := tensor.FromSlice([]float32{3, 2, 1, 3, 2, 1, 3, 2, 1}, 3, 3)
	target := tensor.FromSlice([]int32{0, 1, 2}, 3)
	if err := s.Update(ctx, pred, target); err != nil {
		t.Fatal(err)
	}
	r := s.report()
	if r.K != 2 || !near(r.HitAt[0], 1.0/3) || !near(r.HitAt[1], 2.0/3) || !near(r.MRR, (1+0.5+1.0/3)/3) {
		t.Fatalf("report %+v", r)
	}
	if !near(r.NDCG, (1+1/math.Log2(3))/3) {
		t.Fatalf("NDCG %v", r.NDCG)
	}
}

func TestScorerNumbers(t *testing.T) {
	ctx := context.Background()
	s := &scorer{kind: string(arch.Number)}
	pred := tensor.FromSlice([]float32{10, 22, 30}, 3, 1)
	target := tensor.FromSlice([]float32{10, 20, 36}, 3, 1)
	if err := s.Update(ctx, pred, target); err != nil {
		t.Fatal(err)
	}
	r := s.report()
	if !near(r.MAE, 8.0/3) || !near(r.RMSE, math.Sqrt(40.0/3)) || !near(r.Within10, 2.0/3) || len(r.Points) != 3 {
		t.Fatalf("report %+v", r)
	}
	// Mean 22: spread 144+4+196 = 344.
	if !near(r.R2, 1-40.0/344) {
		t.Fatalf("R2 %v", r.R2)
	}
}
