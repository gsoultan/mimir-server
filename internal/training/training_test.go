package training

import (
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
