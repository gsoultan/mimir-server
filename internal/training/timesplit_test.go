package training

import (
	"fmt"
	"slices"
	"strings"
	"testing"
)

func TestLatestRowsAreHeldOut(t *testing.T) {
	var csv strings.Builder
	csv.WriteString("order_date,segment,units\n")
	for i := range 100 {
		// Out of order, as exports often are.
		day := (i * 37) % 100
		fmt.Fprintf(&csv, "2025-%02d-%02d,%s,%d\n", 1+day/28, 1+day%28, []string{"a", "b"}[i%2], i)
	}
	tab, err := ParseTable(strings.NewReader(csv.String()), "units", nil)
	if err != nil {
		t.Fatal(err)
	}
	if tab.DateColumn != "order_date" || len(tab.When) != tab.Rows {
		t.Fatalf("date column %q, %d times for %d rows", tab.DateColumn, len(tab.When), tab.Rows)
	}
	train, test, err := splitBy(tab.When, tab.Rows, 0.2, 1, 8, &examples{})
	if err != nil {
		t.Fatal(err)
	}
	tr, te := train.(*examples).rows, test.(*examples).rows
	if len(te) != 20 || len(tr) != 80 {
		t.Fatalf("%d held, %d learned", len(te), len(tr))
	}
	latest := slices.Max(func() []int64 {
		var out []int64
		for _, i := range tr {
			out = append(out, tab.When[i])
		}
		return out
	}())
	for _, i := range te {
		if tab.When[i] < latest {
			t.Fatalf("held row %d is older than one learned from", i)
		}
	}
	// Learned in a shuffled order, not oldest first.
	if slices.IsSortedFunc(tr, func(a, b int) int { return int(tab.When[a] - tab.When[b]) }) {
		t.Error("rows learned from are in date order")
	}

	// Without a date column, rows are held out at random.
	var rows strings.Builder
	rows.WriteString("segment,units\n")
	for i := range 40 {
		fmt.Fprintf(&rows, "%s,%d\n", []string{"a", "b"}[i%2], i)
	}
	plain, err := ParseTable(strings.NewReader(rows.String()), "units", nil)
	if err != nil {
		t.Fatal(err)
	}
	if plain.DateColumn != "" || plain.When != nil {
		t.Errorf("a table with no dates has date column %q", plain.DateColumn)
	}
}
