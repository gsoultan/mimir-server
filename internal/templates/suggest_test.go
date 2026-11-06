package templates

import (
	"bytes"
	"fmt"
	"strings"
	"testing"

	"github.com/gsoultan/mimir-server/internal/training"
)

// notes checks csv with no template and returns its notes, one per line.
func notes(t *testing.T, csv string) string {
	t.Helper()
	tab, err := training.ParseTable(strings.NewReader(csv), "", nil)
	if err != nil {
		t.Fatal(err)
	}
	var b bytes.Buffer
	for _, n := range CheckTable(nil, tab, nil, nil).Notes {
		fmt.Fprintf(&b, "%s %s\n", n.Title, n.Body)
	}
	return b.String()
}

func TestSuggest(t *testing.T) {
	var orders strings.Builder
	orders.WriteString("customer_id,customer_category,latitude,longitude,quantity,product\n")
	for i := range 60 {
		fmt.Fprintf(&orders, "C-%d,%s,-6.%d,106.%d,%d,P-%d\n", i%20, []string{"bakery", "cafe"}[i%2], i, i, 1+i%7, i%9)
	}
	got := notes(t, orders.String())
	for _, want := range []string{"Recommend from nearby customers", "The 10 products each customer is", "Groups of similar rows"} {
		if !strings.Contains(got, want) {
			t.Errorf("no %q in\n%s", want, got)
		}
	}
	if strings.Contains(got, "Add a column") {
		t.Errorf("asks for a column it has:\n%s", got)
	}

	// Without a place, it says what a place would give.
	var plain strings.Builder
	plain.WriteString("customer,quantity,product\n")
	for i := range 60 {
		fmt.Fprintf(&plain, "C-%d,%d,P-%d\n", i%20, 1+i%7, i%9)
	}
	if got := notes(t, plain.String()); !strings.Contains(got, "latitude and longitude of each customer") {
		t.Errorf("no ask for a place in\n%s", got)
	}

	// A yes or no answer says how often it is yes, and the templates
	// that fit.
	tp, _ := Get("account-churn")
	_, _, b, _ := Example(tp)
	got = notes(t, string(b))
	if !strings.Contains(got, "of these rows say") || !strings.Contains(got, tp.Name) {
		t.Errorf("no answer share or template in\n%s", got)
	}
}
