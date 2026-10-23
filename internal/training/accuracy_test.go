package training

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/gsoultan/mimir/nn"

	"github.com/gsoultan/mimir-server/internal/arch"
	"github.com/gsoultan/mimir-server/internal/store"
)

// TestAccuracy trains a table model the way the server does and reports
// its held-out accuracy, to compare with tools/accuracy/train.py, which
// trains the same network in PyTorch on the same rows. It runs only when
// MIMIR_ACCURACY_CSV names a CSV file whose last column is the answer:
//
//	MIMIR_ACCURACY_CSV=adult.csv MIMIR_ACCURACY_SEEDS=3 MIMIR_ACCURACY_DUMP=/tmp/adult go test -run TestAccuracy -v ./internal/training
//
// MIMIR_ACCURACY_DUMP writes the encoded rows, the split and the settings
// there for train.py. MIMIR_ACCURACY_EPOCHS overrides the epochs.
func TestAccuracy(t *testing.T) {
	path := os.Getenv("MIMIR_ACCURACY_CSV")
	if path == "" {
		t.Skip("set MIMIR_ACCURACY_CSV to run")
	}
	csv, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	tab, err := ParseTable(strings.NewReader(string(csv)), "", nil)
	if err != nil {
		t.Fatal(err)
	}
	s := SettingsFor(store.Dataset{Kind: store.DatasetTable, Rows: int64(tab.Rows)})
	if e, _ := strconv.Atoi(os.Getenv("MIMIR_ACCURACY_EPOCHS")); e > 0 {
		s.Epochs = e
	}
	a, _ := arch.Lookup("table-mlp")
	if dir := os.Getenv("MIMIR_ACCURACY_DUMP"); dir != "" {
		dumpTable(t, dir, tab, s)
	}
	seeds, _ := strconv.Atoi(os.Getenv("MIMIR_ACCURACY_SEEDS"))
	var sum float64
	for seed := 1; seed <= max(1, seeds); seed++ {
		nn.ManualSeed(uint64(seed))
		res, err := Fit(context.Background(), Task{Arch: a, Source: Source{Kind: store.DatasetTable, CSV: csv}, Settings: s}, nil)
		if err != nil {
			t.Fatal(err)
		}
		accs := make([]string, len(res.History))
		for i, e := range res.History {
			accs[i] = strconv.FormatFloat(e.Accuracy, 'f', 4, 64)
		}
		last := res.History[len(res.History)-1].Accuracy
		sum += last
		t.Logf("seed %d: accuracy %.4f, by epoch %s", seed, last, strings.Join(accs, " "))
	}
	t.Logf("%s: rows=%d features=%d classes=%d epochs=%d batch=%d lr=%g mean accuracy %.4f",
		filepath.Base(path), tab.Rows, len(tab.Features), len(tab.Classes), s.Epochs, s.BatchSize, s.LearningRate, sum/float64(max(1, seeds)))
}

// dumpTable writes what train.py reads: x.f32 and y.i32 hold every row,
// train.i32 and test.i32 the split Fit makes, meta.json the rest.
func dumpTable(t *testing.T, dir string, tab *Table, s store.Settings) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	l, err := fromTable(tab, arch.Architecture{}, s, 42, []int{len(tab.Features)}, func(dst []float32, i int) []float32 { return dst })
	if err != nil {
		t.Fatal(err)
	}
	write := func(name string, v any) {
		f, err := os.Create(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		if err := binary.Write(f, binary.LittleEndian, v); err != nil {
			t.Fatal(err)
		}
	}
	rows := func(set Set) []int32 {
		var out []int32
		for _, r := range set.(*examples).rows {
			out = append(out, int32(r))
		}
		return out
	}
	write("x.f32", tab.X)
	write("y.i32", tab.Y)
	write("train.i32", rows(l.Train))
	write("test.i32", rows(l.Test))
	meta, _ := json.Marshal(map[string]any{
		"features": len(tab.Features), "classes": len(tab.Classes),
		"mean": l.Spec.Mean, "std": l.Spec.Std,
		"hidden": hiddenUnits(arch.DefaultLayers),
		"epochs": s.Epochs, "batch": s.BatchSize, "lr": s.LearningRate,
	})
	if err := os.WriteFile(filepath.Join(dir, "meta.json"), meta, 0o644); err != nil {
		t.Fatal(err)
	}
}

func hiddenUnits(ls []arch.Layer) []int {
	var out []int
	for _, l := range ls {
		if l.Kind == arch.Dense {
			out = append(out, l.Units)
		}
	}
	return out
}
