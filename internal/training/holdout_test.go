package training

import (
	"fmt"
	"strings"
	"testing"

	"github.com/gsoultan/mimir-server/internal/arch"
)

func table(n int) []byte {
	var b strings.Builder
	b.WriteString("x,y\n")
	for i := range n {
		fmt.Fprintf(&b, "%d,%d\n", i, i*2)
	}
	return []byte(b.String())
}

func TestFreezeAndWithout(t *testing.T) {
	// A fifth of a hundred rows, with the header, and the same rows for
	// the same seed.
	frozen, n, err := Freeze(table(100), 0.2, 7, MaxBenchmarkRows)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Count(strings.TrimSpace(string(frozen)), "\n")
	if n != 20 || lines != 20 {
		t.Fatalf("%d rows, %d lines", n, lines)
	}
	again, _, err := Freeze(table(100), 0.2, 7, MaxBenchmarkRows)
	if err != nil || string(again) != string(frozen) {
		t.Fatal("the same seed picked different rows")
	}
	if other, _, _ := Freeze(table(100), 0.2, 8, MaxBenchmarkRows); string(other) == string(frozen) {
		t.Fatal("another seed picked the same rows")
	}

	// The rows kept aside are left out of training, once.
	left, out, err := Without(table(100), frozen)
	if err != nil {
		t.Fatal(err)
	}
	if out != 20 || strings.Count(strings.TrimSpace(string(left)), "\n") != 80 {
		t.Fatalf("left out %d, kept %q", out, left)
	}
	if _, out, _ := Without(left, frozen); out != 0 {
		t.Fatalf("left out %d from a table without them", out)
	}

	// The row limit caps the share, and no frozen rows means no change.
	_, n, err = Freeze(table(100), 0.5, 7, 10)
	if err != nil || n != 10 {
		t.Fatalf("capped: %d, %v", n, err)
	}
	if same, out, _ := Without(table(10)); out != 0 || len(same) != len(table(10)) {
		t.Fatal("nothing frozen changed the table")
	}

	// Too few rows to keep any aside, or to leave enough to learn from.
	if _, _, err := Freeze(table(4), 0.2, 7, MaxBenchmarkRows); err == nil {
		t.Fatal("kept a row aside from four")
	}
	if _, _, err := Freeze(table(2), 0.5, 7, MaxBenchmarkRows); err == nil {
		t.Fatal("left too few to learn from")
	}
}

func TestWithoutTrimsAndKeepsOrder(t *testing.T) {
	// Spaces around a field don't hide a row, and the rows that stay keep
	// their order.
	left, out, err := Without([]byte("x,y\n1,2\n3,4\n5,6\n"), []byte("x,y\n 3 , 4 \n"))
	if err != nil || out != 1 || string(left) != "x,y\n1,2\n5,6\n" {
		t.Fatalf("%q, %d, %v", left, out, err)
	}
}

func TestTallyScoresOneAnswerAtATime(t *testing.T) {
	ctx := t.Context()
	tally, err := NewTally(arch.Architecture{Output: arch.Class}, []string{"no", "yes"})
	if err != nil {
		t.Fatal(err)
	}
	// Three right, one wrong, and one answer the model never learned.
	for _, c := range []struct {
		scores []float32
		label  string
	}{
		{[]float32{2, 0}, "no"},
		{[]float32{0, 2}, "yes"},
		{[]float32{0, 2}, "yes"},
		{[]float32{2, 0}, "yes"},
		{[]float32{2, 0}, "maybe"},
	} {
		if err := tally.Label(ctx, c.scores, c.label); err != nil {
			t.Fatal(err)
		}
	}
	r := tally.Report()
	if r.Examples != 5 || !near(r.Accuracy, 0.6) || r.MillisPerExample < 0 {
		t.Fatalf("report %+v", r)
	}
}
