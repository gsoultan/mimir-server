package training

import (
	"context"
	"fmt"
	"math"
	"slices"
	"strings"
	"testing"

	"github.com/gsoultan/mimir/tensor"

	"github.com/gsoultan/mimir-server/internal/arch"
	"github.com/gsoultan/mimir-server/internal/runtime"
	"github.com/gsoultan/mimir-server/internal/schema"
	"github.com/gsoultan/mimir-server/internal/store"
)

// learnedTable is a table no straight line answers well: the class is
// whether a and b share a sign, and the number is a·b.
func learnedTable(rows int, text bool) []byte {
	var csv strings.Builder
	csv.WriteString("a,b,noise")
	if text {
		csv.WriteString(",note")
	}
	csv.WriteString(",label,amount\n")
	for i := range rows {
		a, b := float64(i%23)-11, float64((i*7)%19)-9
		label := "no"
		if (a > 0) == (b > 0) {
			label = "yes"
		}
		fmt.Fprintf(&csv, "%g,%g,%d", a, b, (i*13)%5)
		if text {
			fmt.Fprintf(&csv, ",the customer %s on visit %d and then %s again", []string{"called twice", "paid late", "asked for help"}[i%3], i, []string{"waited", "left", "smiled"}[i%7%3])
		}
		fmt.Fprintf(&csv, ",%s,%g\n", label, a*b)
	}
	return []byte(csv.String())
}

func TestLearnersFitAndServe(t *testing.T) {
	ctx := context.Background()
	s := store.Settings{Epochs: 1, BatchSize: 64, LearningRate: 1e-3, HoldOut: 0.2}
	for _, tc := range []struct {
		name, target string
		min          float64 // the least accuracy, or the most mean error
	}{
		{"table-boost", "label", 0.9},
		{"table-forest", "label", 0.9},
		{"table-linear", "label", 0.4},
		{"number-boost", "amount", 8},
		{"number-forest", "amount", 8},
		{"number-linear", "amount", 40},
		{"table-auto", "label", 0.9},
		{"number-auto", "amount", 8},
		{"table-svm", "label", 0.9},
		{"number-svm", "amount", 8},
		{"table-knn", "label", 0.85},
		{"number-knn", "amount", 12},
		{"table-bayes", "label", 0.4},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a, ok := arch.Lookup(tc.name)
			if !ok || a.Learner == nil {
				t.Fatalf("no learner architecture %q", tc.name)
			}
			csv := learnedTable(1500, false)
			// The other answer column is not something to read.
			drop := map[string]string{"label": "amount", "amount": "label"}[tc.target]
			csv = dropColumn(csv, drop)
			src := Source{Kind: store.DatasetTable, CSV: csv, Target: tc.target}
			var calls int
			res, err := Fit(ctx, Task{Arch: a, Source: src, Settings: s}, func(int, int, int, []store.Epoch) { calls++ })
			if err != nil {
				t.Fatal(err)
			}
			if len(res.History) != 1 || calls < 2 {
				t.Fatalf("%d epochs, %d progress calls", len(res.History), calls)
			}
			ep := res.History[0]
			if a.Answers() == arch.Number {
				if !(ep.MAE <= tc.min) {
					t.Errorf("mean error %v, want at most %v", ep.MAE, tc.min)
				}
			} else if !(ep.Accuracy >= tc.min) {
				t.Errorf("accuracy %v, want at least %v", ep.Accuracy, tc.min)
			}

			// The saved weights rebuild into a model that answers alike.
			mod, err := runtime.Build(a, res.Spec, res.Weights)
			if err != nil {
				t.Fatal(err)
			}
			f := len(res.Features)
			x := []float32{5, 5, 0, -5, 5, 0}
			if f != 3 {
				t.Fatalf("features %v", res.Features)
			}
			out, err := mod.Forward(tensor.FromSlice(x, 2, f)).Data[float32](ctx)
			if err != nil {
				t.Fatal(err)
			}
			if a.Answers() == arch.Number {
				if len(out) != 2 || (tc.name != "number-linear" && tc.name != "number-knn" && (math.Abs(float64(out[0])-25) > 8 || math.Abs(float64(out[1])+25) > 8)) {
					t.Errorf("answers %v for 5·5 and -5·5", out)
				}
				return
			}
			yes := slices.Index(res.Spec.Labels, "yes")
			if len(out) != 4 || yes < 0 {
				t.Fatalf("answers %v for labels %v", out, res.Spec.Labels)
			}
			if tc.name != "table-linear" && tc.name != "table-bayes" && (out[yes] <= out[1-yes] || out[2+yes] >= out[3-yes]) {
				t.Errorf("logits %v: want yes for (5, 5) and no for (-5, 5)", out)
			}
		})
	}
}

// dropColumn removes the column called name from a CSV.
func dropColumn(csv []byte, name string) []byte {
	lines := strings.Split(strings.TrimSpace(string(csv)), "\n")
	i := slices.Index(strings.Split(lines[0], ","), name)
	for n, l := range lines {
		cells := strings.Split(l, ",")
		lines[n] = strings.Join(slices.Delete(cells, i, i+1), ",")
	}
	return []byte(strings.Join(lines, "\n") + "\n")
}

func TestLearnersReadText(t *testing.T) {
	csv := dropColumn(learnedTable(300, true), "amount")
	tb, err := ParseTable(strings.NewReader(string(csv)), "label", nil)
	if err != nil {
		t.Fatal(err)
	}
	if !schema.HasText(tb.Columns) {
		t.Fatal("the note column is not read as text")
	}
	d := store.Dataset{Kind: store.DatasetTable, Labels: tb.Classes, Columns: tb.Columns}
	got := Architectures(d)
	for _, name := range []string{"table-boost", "table-forest", "table-linear", "text-mlp"} {
		if !slices.Contains(got, name) {
			t.Errorf("a table with text offers %v, without %s", got, name)
		}
	}
	if slices.Contains(got, "table-mlp") {
		t.Errorf("a table with text offers table-mlp")
	}
	a, _ := arch.Lookup("table-boost")
	res, err := Fit(context.Background(), Task{Arch: a, Source: Source{Kind: store.DatasetTable, CSV: csv, Target: "label"},
		Settings: store.Settings{Epochs: 1, BatchSize: 32, HoldOut: 0.2}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if acc := res.History[0].Accuracy; acc < 0.85 {
		t.Errorf("accuracy %v with a text column", acc)
	}
}
