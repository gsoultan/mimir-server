package training

import (
	"context"
	"fmt"
	"math"
	"math/rand/v2"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/gsoultan/mimir/tensor"

	"github.com/gsoultan/mimir-server/internal/arch"
	"github.com/gsoultan/mimir-server/internal/ask"
	"github.com/gsoultan/mimir-server/internal/runtime"
	"github.com/gsoultan/mimir-server/internal/store"
)

// fitCells teaches architecture name from csv and returns what it made
// and a function that answers a question by column name, as the server
// does.
func fitCells(t *testing.T, name, target string, csv string) (Result, func(fields map[string]string) []float32) {
	t.Helper()
	ctx := context.Background()
	a, ok := arch.Lookup(name)
	if !ok || a.Learner == nil || a.Learner.Cells == nil {
		t.Fatalf("no cells learner %q", name)
	}
	src := Source{Kind: store.DatasetTable, CSV: []byte(csv), Target: target}
	res, err := Fit(ctx, Task{Arch: a, Source: src, Settings: store.Settings{Epochs: 1}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.History) != 1 || len(res.Spec.InputShape) != 1 || res.Spec.InputShape[0] != len(res.Features) {
		t.Fatalf("history %v, input %v for features %v", res.History, res.Spec.InputShape, res.Features)
	}
	mod, err := runtime.Build(a, res.Spec, res.Weights)
	if err != nil {
		t.Fatal(err)
	}
	return res, func(fields map[string]string) []float32 {
		t.Helper()
		x, err := ask.Encode(a, res.Columns, res.Features, fields, nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		out, err := mod.Forward(tensor.FromSlice(x, 1, len(x))).Data[float32](ctx)
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
}

func TestFindGroups(t *testing.T) {
	var csv strings.Builder
	csv.WriteString("spend,visits,plan\n")
	r := rand.New(rand.NewPCG(1, 2))
	centers := [][2]float64{{10, 1}, {200, 4}, {50, 20}}
	plans := []string{"basic", "pro", "basic"}
	for i := range 600 {
		c := centers[i%3]
		fmt.Fprintf(&csv, "%.2f,%.1f,%s\n", c[0]*(1+0.08*r.NormFloat64()), c[1]*(1+0.08*r.NormFloat64()), plans[i%3])
	}
	res, ask := fitCells(t, "find-groups", "plan", csv.String())
	if len(res.Spec.Labels) != 3 {
		t.Fatalf("groups %v, want 3", res.Spec.Labels)
	}
	if !strings.HasPrefix(res.Spec.Labels[0], "Group 1") {
		t.Errorf("labels %v", res.Spec.Labels)
	}
	seen := map[int]bool{}
	for i, c := range centers {
		g := argmax(ask(map[string]string{"spend": fmt.Sprint(c[0]), "visits": fmt.Sprint(c[1]), "plan": plans[i]}))
		seen[g] = true
	}
	if len(seen) != 3 {
		t.Errorf("the three centers fall in groups %v", seen)
	}
}

func TestOddOne(t *testing.T) {
	var csv strings.Builder
	csv.WriteString("machine,temp_c,vibration\n")
	r := rand.New(rand.NewPCG(3, 4))
	for i := range 2000 {
		fmt.Fprintf(&csv, "press-%d,%.2f,%.3f\n", i%3, 60+r.NormFloat64(), 0.3+0.02*r.NormFloat64())
	}
	res, ask := fitCells(t, "odd-one", "", csv.String())
	if len(res.Spec.Labels) != 1 || len(res.Columns) != 3 {
		t.Fatalf("labels %v, columns %v", res.Spec.Labels, res.Columns)
	}
	usual := ask(map[string]string{"machine": "press-1", "temp_c": "60.2", "vibration": "0.31"})[0]
	odd := ask(map[string]string{"machine": "press-1", "temp_c": "95", "vibration": "1.4"})[0]
	if !(usual < 0.55 && odd > 0.65) {
		t.Errorf("usual reading %v, odd one %v", usual, odd)
	}
}

func TestRecommend(t *testing.T) {
	// Two kinds of customer: tea people buy tea, coffee people coffee.
	tea := []string{"earl-grey", "sencha", "assam", "rooibos", "chai", "oolong"}
	coffee := []string{"espresso", "latte-beans", "decaf", "robusta", "mocha", "arabica"}
	var csv strings.Builder
	csv.WriteString("customer,when,item\n")
	r := rand.New(rand.NewPCG(5, 6))
	for u := range 300 {
		items := tea
		if u%2 == 1 {
			items = coffee
		}
		for _, k := range r.Perm(len(items))[:4] {
			fmt.Fprintf(&csv, "c-%d,2026-09-%02d,%s\n", u, 1+r.IntN(28), items[k])
		}
	}
	// One tea person who has bought only two.
	csv.WriteString("c-new,2026-09-30,earl-grey\nc-new,2026-09-30,sencha\n")
	res, ask := fitCells(t, "recommend", "item", csv.String())
	if len(res.Columns) != 1 || res.Columns[0].Name != "customer" {
		t.Fatalf("columns %v, want the customer", res.Columns)
	}
	if len(res.Spec.Labels) != 12 {
		t.Fatalf("items %v", res.Spec.Labels)
	}
	if acc := res.History[0].Accuracy; acc < 0.6 {
		t.Errorf("the held-out item is in the top %d %v of the time", 10, acc)
	}
	top := func(scores []float32, n int) []string {
		idx := make([]int, len(scores))
		for i := range idx {
			idx[i] = i
		}
		slices.SortFunc(idx, func(a, b int) int { return int(math.Copysign(1, float64(scores[b]-scores[a]))) })
		var out []string
		for _, i := range idx[:n] {
			out = append(out, res.Spec.Labels[i])
		}
		return out
	}
	for _, got := range top(ask(map[string]string{"customer": "c-new"}), 6) {
		if !slices.Contains(tea, got) {
			t.Errorf("a tea person gets %v", got)
		}
	}
	// Someone it hasn't seen still gets a list.
	if out := ask(map[string]string{"customer": "nobody"}); len(out) != 12 || math.IsNaN(float64(out[0])) {
		t.Errorf("a stranger's scores %v", out)
	}
}

func TestForecast(t *testing.T) {
	// Two stores selling with a weekly pattern: weekends sell double.
	week := []float64{10, 10, 10, 10, 12, 20, 20}
	var csv strings.Builder
	csv.WriteString("day,store,price,units\n")
	start := time.Date(2026, 1, 5, 0, 0, 0, 0, time.UTC) // a Monday
	const days = 182
	for d := range days {
		day := start.AddDate(0, 0, d)
		for s, size := range []float64{1, 3} {
			fmt.Fprintf(&csv, "%s,shop-%c,4.50,%g\n", day.Format("2006-01-02"), 'a'+s, size*week[d%7])
		}
	}
	res, ask := fitCells(t, "forecast", "units", csv.String())
	if len(res.Columns) != 2 || res.Columns[0].Name != "store" || res.Columns[1].Name != "days ahead" {
		t.Fatalf("columns %v, want the store and the days ahead", res.Columns)
	}
	if mae := res.History[0].MAE; !(mae < 2) {
		t.Errorf("mean error %v on the held-out days", mae)
	}
	// The day after the last is a Monday.
	next := start.AddDate(0, 0, days)
	for ahead := 1; ahead <= 7; ahead++ {
		want := week[int(next.AddDate(0, 0, ahead-1).Sub(start).Hours()/24)%7]
		a := ask(map[string]string{"store": "shop-a", "days ahead": fmt.Sprint(ahead)})[0]
		b := ask(map[string]string{"store": "shop-b", "days ahead": fmt.Sprint(ahead)})[0]
		total := ask(map[string]string{"days ahead": fmt.Sprint(ahead)})[0]
		if math.Abs(float64(a)-want) > 2 || math.Abs(float64(b)-3*want) > 5 || math.Abs(float64(total-a-b)) > 0.01 {
			t.Errorf("%d days ahead: shop a %v, shop b %v, total %v; want %v and %v", ahead, a, b, total, want, 3*want)
		}
	}
	// Past the horizon it answers the last day it forecast.
	far := ask(map[string]string{"store": "shop-a", "days ahead": "400"})[0]
	last := ask(map[string]string{"store": "shop-a", "days ahead": "30"})[0]
	if far != last {
		t.Errorf("400 days ahead %v, 30 days ahead %v", far, last)
	}
}

func TestCellsArchitectures(t *testing.T) {
	got := Architectures(store.Dataset{Kind: store.DatasetTable, NumberTarget: true, Labels: []string{"a", "b"}})
	for _, name := range []string{"find-groups", "odd-one", "recommend", "forecast"} {
		if !slices.Contains(got, name) {
			t.Errorf("a table offers %v, without %s", got, name)
		}
	}
	// Too many answers to pick from: still a recommender.
	got = Architectures(store.Dataset{Kind: store.DatasetTable})
	if slices.Contains(got, "forecast") || !slices.Contains(got, "recommend") || slices.Contains(got, "table-boost") {
		t.Errorf("a table of many text answers offers %v", got)
	}
}

func TestBestAction(t *testing.T) {
	// Offers shown at random: the young buy after a discount, the older
	// after free delivery, and hardly anyone after the newsletter.
	var csv strings.Builder
	csv.WriteString("age,city,offer,bought\n")
	r := rand.New(rand.NewPCG(7, 8))
	offers := []string{"discount", "free-delivery", "newsletter"}
	for range 1500 {
		age := 18 + r.IntN(60)
		offer := offers[r.IntN(3)]
		p := 0.05
		switch {
		case offer == "discount" && age < 40:
			p = 0.6
		case offer == "free-delivery" && age >= 40:
			p = 0.5
		case offer != "newsletter":
			p = 0.15
		}
		bought := "no"
		if r.Float64() < p {
			bought = "yes"
		}
		fmt.Fprintf(&csv, "%d,%s,%s,%s\n", age, []string{"Oslo", "Lima"}[r.IntN(2)], offer, bought)
	}
	res, ask := fitCells(t, "best-action", "offer", csv.String())
	if len(res.Spec.Labels) != 3 || len(res.Columns) != 2 {
		t.Fatalf("actions %v, columns %v", res.Spec.Labels, res.Columns)
	}
	for _, tc := range []struct{ age, want string }{{"25", "discount"}, {"60", "free-delivery"}} {
		out := ask(map[string]string{"age": tc.age, "city": "Oslo"})
		if got := res.Spec.Labels[argmax(out)]; got != tc.want {
			t.Errorf("age %s: best %q (expected rewards %v for %v), want %q", tc.age, got, out, res.Spec.Labels, tc.want)
		}
	}
	if mae := res.History[0].MAE; !(mae > 0 && mae < 0.5) {
		t.Errorf("held-out mean error %v", mae)
	}
	if _, err := Fit(context.Background(), Task{Arch: mustArch(t, "best-action"), Source: Source{Kind: store.DatasetTable, CSV: []byte("age,offer\n1,a\n2,b\n"), Target: "offer"}, Settings: store.Settings{Epochs: 1}}, nil); err == nil {
		t.Error("learned actions with no reward column")
	}
}

func mustArch(t *testing.T, name string) arch.Architecture {
	t.Helper()
	a, ok := arch.Lookup(name)
	if !ok {
		t.Fatalf("no architecture %q", name)
	}
	return a
}
