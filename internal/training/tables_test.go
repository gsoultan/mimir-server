// Copyright (c) 2026 Gembit Soultan Shirazi
// SPDX-License-Identifier: PolyForm-Noncommercial-1.0.0 OR PolyForm-Small-Business-1.0.0 OR LicenseRef-Commercial
//
// This file is part of mimir-server, a server that trains, serves and manages mimir models,
// created and owned by Gembit Soultan Shirazi.
// It is free to use for personal and other noncommercial purposes, for
// nonprofits, and for companies with fewer than 100 people and under
// 1,000,000 USD in yearly revenue, under the PolyForm Noncommercial and
// PolyForm Small Business licenses. Any other use needs a commercial
// license from Gembit Soultan Shirazi. See the LICENSE and LICENSING.md files
// at the root of this repository.

package training

import (
	"context"
	"fmt"
	"math"
	"math/rand/v2"
	"strings"
	"testing"
	"time"

	"github.com/gsoultan/mimir/tensor"

	"github.com/gsoultan/mimir-server/internal/arch"
	"github.com/gsoultan/mimir-server/internal/ask"
	"github.com/gsoultan/mimir-server/internal/runtime"
	"github.com/gsoultan/mimir-server/internal/store"
)

// regionTable is a table whose answer depends on which of 12 regions a
// row is in, a category column, and on a number: whether the customer
// buys (label), and how many orders a week they place (orders), often 0.
func regionTable(rows int, seed uint64) string {
	r := rand.New(rand.NewPCG(seed, 1))
	var csv strings.Builder
	csv.WriteString("region,visits,label,orders\n")
	for range rows {
		reg := r.IntN(12)
		visits := r.Float64() * 4
		label := "no"
		if reg%3 == 1 {
			label = "yes"
		}
		mean := math.Exp(-1 + 0.4*visits + 0.8*float64(reg%2))
		k, p, l := 0, 1.0, math.Exp(-mean)
		for p *= r.Float64(); p > l; p *= r.Float64() {
			k++
		}
		fmt.Fprintf(&csv, "region-%02d,%.2f,%s,%d\n", reg, visits, label, k)
	}
	return csv.String()
}

func fitTable(t *testing.T, name, target, csv string) (Result, func(fields map[string]string) []float32) {
	t.Helper()
	ctx := context.Background()
	a, ok := arch.Lookup(name)
	if !ok || a.Learner == nil {
		t.Fatalf("no learner %q", name)
	}
	drop := map[string]string{"label": "orders", "orders": "label"}[target]
	src := Source{Kind: store.DatasetTable, CSV: dropColumn([]byte(csv), drop), Target: target}
	res, err := Fit(ctx, Task{Arch: a, Source: src, Settings: store.Settings{Epochs: 1, HoldOut: 0.2}}, nil)
	if err != nil {
		t.Fatal(err)
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

func TestTreesReadCategoryColumns(t *testing.T) {
	for _, name := range []string{"table-boost", "table-forest"} {
		res, ask := fitTable(t, name, "label", regionTable(1500, 2))
		if acc := res.History[0].Accuracy; acc < 0.98 {
			t.Errorf("%s: accuracy %v on a class set by the region", name, acc)
		}
		yes := -1
		for i, l := range res.Spec.Labels {
			if l == "yes" {
				yes = i
			}
		}
		for reg := range 12 {
			out := ask(map[string]string{"region": fmt.Sprintf("region-%02d", reg), "visits": "2"})
			if got, want := out[yes] > out[1-yes], reg%3 == 1; got != want {
				t.Errorf("%s: region %d says yes %v", name, reg, got)
			}
		}
		// A region it never saw answers without failing.
		if out := ask(map[string]string{"region": "elsewhere", "visits": "2"}); len(out) != 2 {
			t.Errorf("%s: %d answers for an unknown region", name, len(out))
		}
	}
}

func TestCountsNeverGoBelowZero(t *testing.T) {
	csv := regionTable(2000, 3)
	res, ask := fitTable(t, "number-counts", "orders", csv)
	if mae := res.History[0].MAE; !(mae < 1.2) {
		t.Errorf("mean error %v orders a week", mae)
	}
	for _, v := range []string{"0", "2", "4", "40"} {
		for _, reg := range []string{"region-00", "region-01"} {
			out := ask(map[string]string{"region": reg, "visits": v})
			if !(out[0] > 0) {
				t.Errorf("%s, %s visits: %v orders", reg, v, out[0])
			}
		}
	}
	// More visits, more orders; an odd region orders more.
	lo := ask(map[string]string{"region": "region-00", "visits": "0.5"})[0]
	hi := ask(map[string]string{"region": "region-00", "visits": "3.5"})[0]
	odd := ask(map[string]string{"region": "region-01", "visits": "0.5"})[0]
	if !(hi > 1.5*lo) || !(odd > lo) {
		t.Errorf("orders at 0.5 visits %v, 3.5 visits %v, odd region %v", lo, hi, odd)
	}
	// A negative answer is refused in plain words.
	a, _ := arch.Lookup("number-counts")
	src := Source{Kind: store.DatasetTable, CSV: []byte("x,y\n1,2\n2,-1\n3,4\n4,0\n5,1\n6,2\n7,0\n8,3\n9,1\n10,0\n"), Target: "y"}
	if _, err := Fit(context.Background(), Task{Arch: a, Source: src, Settings: store.Settings{Epochs: 1}}, nil); err == nil || !strings.Contains(err.Error(), "below 0") {
		t.Errorf("a negative count: %v", err)
	}
}

func TestForecastSlowSeller(t *testing.T) {
	// A spare part sold on about 1 day in 7, a few at a time.
	r := rand.New(rand.NewPCG(5, 5))
	var csv strings.Builder
	csv.WriteString("day,units\n")
	start := time.Date(2026, 1, 5, 0, 0, 0, 0, time.UTC)
	var total float64
	const days = 300
	for d := range days {
		units := 0
		if r.Float64() < 1.0/7 {
			units = 2 + r.IntN(3)
		}
		total += float64(units)
		fmt.Fprintf(&csv, "%s,%d\n", start.AddDate(0, 0, d).Format("2006-01-02"), units)
	}
	_, ask := fitCells(t, "forecast", "units", csv.String())
	mean := total / days
	var month float64
	for ahead := 1; ahead <= 30; ahead++ {
		v := float64(ask(map[string]string{"days ahead": fmt.Sprint(ahead)})[0])
		if v < 0 {
			t.Fatalf("%d days ahead: %v units", ahead, v)
		}
		month += v
	}
	if math.Abs(month-30*mean) > 0.35*30*mean {
		t.Errorf("the next 30 days forecast %v units; the part sells %v a month on average", month, 30*mean)
	}
}
