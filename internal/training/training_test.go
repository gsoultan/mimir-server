package training

import (
	"cmp"
	"fmt"
	"io"
	"maps"
	"slices"
	"strings"
	"testing"

	"github.com/gsoultan/mimir-server/internal/schema"
)

func TestParseTable(t *testing.T) {
	csv := "a, b ,label\n" + strings.Repeat("1,2,x\n3,4,y\n", 6)
	tb, err := ParseTable(strings.NewReader(csv), "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if tb.Rows != 12 || strings.Join(tb.Features, ",") != "a,b" || strings.Join(tb.Classes, ",") != "x,y" || tb.Y[1] != 1 || tb.X[2] != 3 {
		t.Fatalf("%+v", tb)
	}
	if _, err := ParseTable(strings.NewReader(csv), "nope", nil); err == nil || !strings.Contains(err.Error(), "no column") {
		t.Fatalf("bad target: %v", err)
	}
	if tb, err := ParseTable(strings.NewReader(csv), "a", nil); err != nil || tb.Columns[1].Kind != schema.Category || !tb.Numeric {
		t.Fatalf("words read as a category when the target moves: %v", err)
	}
	if _, err := ParseTable(strings.NewReader("a,b\n1,2\n"), "", nil); err == nil {
		t.Fatal("too few rows accepted")
	}
	if _, err := ParseTable(strings.NewReader("a,b\n"+strings.Repeat("1,x\n", 12)), "", nil); err == nil {
		t.Fatal("one class accepted")
	}
	train, test, err := tb.Split(0.25, 1, 4)
	if err != nil || train.(*examples).Len() != 9 || test.(*examples).Len() != 3 {
		t.Fatalf("split: %v", err)
	}
	train, test, err = tb.Split(0, 1, 4)
	if err != nil || train.(*examples).Len() != 12 || test != nil {
		t.Fatalf("no holdout: %v", err)
	}
	rows := Sample([]byte(csv), 2)
	if len(rows) != 3 || rows[0][1] != "b" {
		t.Fatalf("sample %v", rows)
	}
}

// TestParseTableStreams checks what the two passes over a table must
// agree on: rows without an answer, a category column with more values
// than are counted at first, and a reader that can't seek.
func TestParseTableStreams(t *testing.T) {
	var b strings.Builder
	b.WriteString("id,shade,note,label\n")
	count := map[string]int{}
	for i := range 3000 {
		label, shade := []string{"a", "b", ""}[i%3], fmt.Sprintf("s%d", i%1500+i%7)
		if label != "" {
			count[shade]++
		}
		fmt.Fprintf(&b, "%d,%s,%s,%s\n", i, shade, []string{"", "fine day", "oops"}[i%3], label)
	}
	top := slices.SortedFunc(maps.Keys(count), func(x, y string) int { return cmp.Or(count[y]-count[x], strings.Compare(x, y)) })
	hints := map[string]schema.Kind{"shade": schema.Category}
	tb, err := ParseTable(struct{ io.Reader }{strings.NewReader(b.String())}, "", hints)
	if err != nil {
		t.Fatal(err)
	}
	if tb.Rows != 2000 || tb.Skipped != 1000 || len(tb.Y) != 2000 || tb.Y[0] != 0 || tb.Y[1] != 1 {
		t.Fatalf("rows %d, skipped %d, answers %v", tb.Rows, tb.Skipped, tb.Y[:2])
	}
	shade := tb.Columns[1]
	if shade.Kind != schema.Category || !slices.Equal(shade.Values, top[:schema.MaxValues]) {
		t.Fatalf("shade %+v", shade)
	}
	if tb.Columns[2].Kind != schema.Category || tb.Profile[2].Empty != 1000 {
		t.Fatalf("note %+v, profile %+v", tb.Columns[2], tb.Profile[2])
	}
	if want := 2000 * len(tb.Features); len(tb.X) != want {
		t.Fatalf("%d numbers, want %d", len(tb.X), want)
	}
}
