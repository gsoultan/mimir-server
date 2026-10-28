package api

import (
	"math"
	"slices"
	"testing"

	"github.com/gsoultan/mimir-server/internal/store"
)

func TestQuantizeKeepsAngles(t *testing.T) {
	a := []float32{0.5, -0.25, 0.1, 0.8}
	b := []float32{0.4, -0.3, 0.0, 0.9}
	cos := func(x, y []float32) float64 {
		var d, nx, ny float64
		for i := range x {
			d += float64(x[i] * y[i])
			nx += float64(x[i] * x[i])
			ny += float64(y[i] * y[i])
		}
		return d / math.Sqrt(nx*ny)
	}
	q := quantize(a)
	if len(q) != 4+len(a) {
		t.Fatalf("%d bytes", len(q))
	}
	back := make([]float32, len(a))
	for i, c := range q[4:] {
		back[i] = float32(int8(c))
	}
	if d := math.Abs(cos(a, b) - cos(back, b)); d > 0.01 {
		t.Errorf("cosine moved by %v", d)
	}
	if z := quantize([]float32{0, 0}); len(z) != 6 {
		t.Errorf("zeros: %v", z)
	}
}

func TestSearchWords(t *testing.T) {
	if got := searchWords("What is the refund policy for damaged items? Refund!"); !slices.Equal(got, []string{"refund", "policy", "damaged", "items"}) {
		t.Errorf("searchWords = %v", got)
	}
	if got := searchWords("apa itu yang"); len(got) != 0 {
		t.Errorf("stop words searched: %v", got)
	}
}

func TestRankByWords(t *testing.T) {
	ps := []store.Passage{
		{ID: "a", Text: "Damaged items are refunded in full."},
		{ID: "b", Text: "Items ship on weekdays. Items, items, items."},
		{ID: "c", Text: "Nothing here."},
	}
	got := top(rankByWords(ps, []string{"damaged", "item", "refund"}), 3)
	if got[0].id != "a" || got[2].id != "c" || got[2].score != 0 {
		t.Errorf("ranked %v", got)
	}
}
