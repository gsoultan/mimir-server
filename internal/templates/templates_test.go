package templates

import (
	"bytes"
	"context"
	"slices"
	"testing"

	"github.com/gsoultan/mimir-server/internal/arch"
	"github.com/gsoultan/mimir-server/internal/schema"
	"github.com/gsoultan/mimir-server/internal/store"
	"github.com/gsoultan/mimir-server/internal/training"
)

func TestCatalog(t *testing.T) {
	cats, tpls := All()
	if len(tpls) != 46 || len(cats) != 8 {
		t.Fatalf("%d templates in %d categories", len(tpls), len(cats))
	}
	seen := map[string]bool{}
	for _, tp := range tpls {
		if seen[tp.ID] {
			t.Errorf("%s twice", tp.ID)
		}
		seen[tp.ID] = true
		if !slices.ContainsFunc(cats, func(c Category) bool { return c.ID == tp.Category }) {
			t.Errorf("%s: no category %q", tp.ID, tp.Category)
		}
		if _, ok := arch.Lookup(tp.Architecture()); !ok {
			t.Errorf("%s: no architecture %q", tp.ID, tp.Architecture())
		}
		switch tp.Data {
		case Photos:
			if len(tp.Folders) < 2 {
				t.Errorf("%s: %d folders", tp.ID, len(tp.Folders))
			}
		default:
			if len(tp.Columns) < 2 || tp.Query() == "" {
				t.Errorf("%s: %d columns, query %q", tp.ID, len(tp.Columns), tp.Query())
			}
			for _, c := range tp.Columns {
				if !c.Kind.Valid() {
					t.Errorf("%s: column %s kind %q", tp.ID, c.Name, c.Kind)
				}
			}
		}
	}
	if _, ok := Get("machine-failure"); !ok {
		t.Fatal("Get")
	}
}

func TestMatch(t *testing.T) {
	tp, _ := Get("machine-failure")
	m := tp.Match([]string{"Machine Type", "AIR_TEMP_C", "speed", "fails_within_7_days"}, map[string]string{"speed_rpm": "speed"})
	if m["machine_type"] != "Machine Type" || m["air_temp_c"] != "AIR_TEMP_C" || m["speed_rpm"] != "speed" || m["torque_nm"] != "" {
		t.Fatalf("%v", m)
	}
	if h := tp.Hints(m); h["Machine Type"] != schema.Category || h["speed"] != schema.Number {
		t.Fatalf("hints %v", h)
	}
}

// examples reads a template's example the way an upload is read and
// returns the dataset row it makes and its check verdict.
func examples(t *testing.T, tp Template) (store.Dataset, []byte, string) {
	t.Helper()
	_, _, b, err := Example(tp)
	if err != nil {
		t.Fatal(err)
	}
	switch tp.Data {
	case Photos:
		arc, err := training.OpenArchive(b)
		if err != nil {
			t.Fatal(err)
		}
		folders, loose := arc.Folders()
		labels, err := training.PhotoLabels(folders)
		if err != nil {
			t.Fatal(err)
		}
		c := CheckPhotos(&tp, folders, loose)
		return store.Dataset{Kind: store.DatasetPhotos, Labels: labels, InputShape: []int{3, schema.PhotoSize, schema.PhotoSize}}, b, c.Verdict
	case PhotoTable:
		arc, err := training.OpenArchive(b)
		if err != nil {
			t.Fatal(err)
		}
		name, csv, err := arc.Table()
		if err != nil {
			t.Fatal(err)
		}
		match := tp.Match(training.Sample(csv, 0)[0], nil)
		tab, err := training.ParseTable(bytes.NewReader(csv), match[tp.Answer().Name], tp.Hints(match))
		if err != nil {
			t.Fatal(err)
		}
		for _, cell := range tab.Photos {
			if arc.Find(name, cell) == "" {
				t.Fatalf("%s: photo %s missing", tp.ID, cell)
			}
		}
		d := store.Dataset{Kind: store.DatasetPhotoTable}
		tab.Describe(&d)
		return d, b, CheckTable(&tp, tab, match, nil).Verdict
	}
	match := tp.Match(training.Sample(b, 0)[0], nil)
	tab, err := training.ParseTable(bytes.NewReader(b), match[tp.Answer().Name], tp.Hints(match))
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range tp.Inputs() {
		i := slices.IndexFunc(tab.Columns, func(x schema.Column) bool { return x.Name == c.Name })
		if i < 0 || tab.Columns[i].Kind != c.Kind {
			t.Errorf("%s: column %s read as %v", tp.ID, c.Name, tab.Columns)
		}
	}
	d := store.Dataset{Kind: store.DatasetTable}
	tab.Describe(&d)
	return d, b, CheckTable(&tp, tab, match, nil).Verdict
}

func TestExamples(t *testing.T) {
	_, tpls := All()
	for _, tp := range tpls {
		t.Run(tp.ID, func(t *testing.T) {
			d, _, verdict := examples(t, tp)
			if verdict == "bad" {
				t.Errorf("verdict %s", verdict)
			}
			if !slices.Contains(training.Architectures(d), tp.Architecture()) {
				t.Errorf("%s isn't offered: %v", tp.Architecture(), training.Architectures(d))
			}
		})
	}
}

// TestTeach teaches one template of each model type from its example and
// checks it learned more than guessing would.
func TestTeach(t *testing.T) {
	if testing.Short() {
		t.Skip("trains models")
	}
	for _, c := range []struct {
		id    string
		epoch int
		want  float64
	}{
		{"account-churn", 6, 0.85},
		{"review-mood", 4, 0.8},
		{"energy-use", 12, 0.5},
		{"surface-defect", 4, 0.8},
		{"warranty-claim", 4, 0.8},
	} {
		t.Run(c.id, func(t *testing.T) {
			tp, ok := Get(c.id)
			if !ok {
				t.Fatalf("no template %s", c.id)
			}
			d, b, _ := examples(t, tp)
			a, _ := arch.Lookup(tp.Architecture())
			res, err := training.Fit(context.Background(), training.Task{
				Arch:     a,
				Source:   training.Source{Kind: d.Kind, CSV: b, Target: d.Target, Columns: d.Columns},
				Settings: store.Settings{Epochs: c.epoch, BatchSize: 32, LearningRate: 0.003, HoldOut: 0.2},
			}, nil)
			if err != nil {
				t.Fatal(err)
			}
			last := res.History[len(res.History)-1]
			t.Logf("%s: accuracy %.3f, typical error %.3f", a.Name, last.Accuracy, last.MAE)
			if last.Accuracy < c.want {
				t.Errorf("accuracy %.3f, want at least %.2f", last.Accuracy, c.want)
			}
		})
	}
}
