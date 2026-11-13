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


package drift

import (
	"fmt"
	"math/rand/v2"
	"strconv"
	"strings"
	"testing"

	"github.com/gsoultan/mimir-server/internal/schema"
)

var cols = []schema.Column{
	{Name: "bill", Kind: schema.Number},
	{Name: "plan", Kind: schema.Category, Values: []string{"basic", "plus", "gold"}},
	{Name: "paper", Kind: schema.YesNo},
	{Name: "note", Kind: schema.Text},
	{Name: "photo", Kind: schema.Photo},
}

// row makes one row; shift moves the bill up, gold makes gold common.
func row(r *rand.Rand, shift float64, gold bool) []string {
	bill := 20 + r.Float64()*60 + shift
	plan := []string{"basic", "basic", "plus", "gold"}[r.IntN(4)]
	if gold && r.IntN(2) == 0 {
		plan = "gold"
	}
	paper := "no"
	if r.IntN(3) == 0 {
		paper = "yes"
	}
	return []string{strconv.FormatFloat(bill, 'f', 2, 64), plan, paper, "a note", "x.jpg"}
}

func baseline(t *testing.T) []Column {
	t.Helper()
	r := rand.New(rand.NewPCG(7, 7))
	b := NewBuilder(cols)
	for range 5000 {
		b.Add(row(r, 0, false))
	}
	base := b.Columns()
	if len(base) != 4 {
		t.Fatalf("got %d columns, want 4 (no photo)", len(base))
	}
	return base
}

func questions(seed uint64, n int, shift float64, gold bool, drop string) []map[string]string {
	r := rand.New(rand.NewPCG(seed, seed))
	out := make([]map[string]string, n)
	for i := range out {
		cells := row(r, shift, gold)
		q := map[string]string{}
		for j, c := range cols {
			if c.Name != drop {
				q[c.Name] = cells[j]
			}
		}
		out[i] = q
	}
	return out
}

func TestSameDataNoChange(t *testing.T) {
	base := baseline(t)
	for seed := range uint64(20) {
		for _, n := range []int{MinQuestions, 500, 2000} {
			if got := Compare(base, questions(seed+100, n, 0, false, "")); len(got) > 0 {
				t.Errorf("seed %d, %d questions: unchanged data flagged %+v", seed, n, got)
			}
		}
	}
}

func TestTooFewQuestions(t *testing.T) {
	if got := Compare(baseline(t), questions(1, MinQuestions-1, 500, true, "bill")); got != nil {
		t.Fatalf("got %+v with too few questions", got)
	}
}

func TestNumberMoved(t *testing.T) {
	got := Compare(baseline(t), questions(3, 300, 100, false, ""))
	if len(got) != 1 || got[0].Column != "bill" || !got[0].ALot {
		t.Fatalf("got %+v, want bill changed a lot", got)
	}
	if !strings.Contains(got[0].Summary, "Mostly 25.7 to 73.7 in the examples") || !strings.Contains(got[0].Summary, "recent questions") {
		t.Errorf("summary %q", got[0].Summary)
	}
}

func TestCategoryAndMissing(t *testing.T) {
	got := Compare(baseline(t), questions(4, 400, 0, true, "paper"))
	byCol := map[string]Change{}
	for _, c := range got {
		byCol[c.Column] = c
	}
	if c, ok := byCol["paper"]; !ok || !c.ALot || c.Summary != "Recent questions leave it out." {
		t.Errorf("paper: %+v", c)
	}
	if c, ok := byCol["plan"]; !ok || !strings.HasPrefix(c.Summary, `"gold" is in`) {
		t.Errorf("plan: %+v", c)
	}
	if got[0].Column != "paper" {
		t.Errorf("biggest change first: got %s", got[0].Column)
	}
}

func TestNewValues(t *testing.T) {
	qs := questions(5, 200, 0, false, "")
	for i, q := range qs {
		if i%2 == 0 {
			q["plan"] = "platinum"
		}
	}
	got := Compare(baseline(t), qs)
	if len(got) != 1 || got[0].Column != "plan" || !strings.Contains(got[0].Summary, `never saw while learning, such as "platinum"`) {
		t.Fatalf("got %+v", got)
	}
}

func TestNotNumbers(t *testing.T) {
	qs := questions(6, 200, 0, false, "")
	for i, q := range qs {
		if i%3 == 0 {
			q["bill"] = "n/a"
		}
	}
	got := Compare(baseline(t), qs)
	if len(got) != 1 || !strings.Contains(got[0].Summary, "something other than a number") {
		t.Fatalf("got %+v", got)
	}
}

func TestNum(t *testing.T) {
	for f, want := range map[float64]string{12.5: "12.5", 1234567: "1,234,567", -2500: "-2,500", 0.1234567: "0.123"} {
		if got := num(f); got != want {
			t.Errorf("num(%v) = %q, want %q", f, got, want)
		}
	}
	if pct(0.004) != "under 1%" || pct(0.25) != "25%" {
		t.Error(pct(0.004), pct(0.25))
	}
	_ = fmt.Sprint()
}
