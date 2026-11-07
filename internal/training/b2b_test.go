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
	"fmt"
	"math/rand/v2"
	"strings"
	"testing"

	"github.com/gsoultan/mimir-server/internal/arch"
)

func TestLookAlike(t *testing.T) {
	// Best customers are big manufacturers that order often.
	var csv strings.Builder
	csv.WriteString("customer_id,industry,employees,orders_12m,best_customer\n")
	r := rand.New(rand.NewPCG(1, 2))
	for i := range 800 {
		best := i%4 == 0
		ind, emp, ord := []string{"retail", "food", "finance"}[r.IntN(3)], 10+r.IntN(200), r.IntN(6)
		if best {
			ind, emp, ord = "manufacturing", 300+r.IntN(500), 8+r.IntN(10)
		}
		fmt.Fprintf(&csv, "C%d,%s,%d,%d,%v\n", i, ind, emp, ord, map[bool]string{true: "yes", false: "no"}[best])
	}
	res, ask := fitCells(t, "look-alike", "best_customer", csv.String())
	for _, c := range res.Columns {
		if c.Name == "customer_id" {
			t.Fatalf("learns from the id: %v", res.Columns)
		}
	}
	if a := res.History[0].Accuracy; a < 0.9 {
		t.Errorf("AUC %v", a)
	}
	like := ask(map[string]string{"industry": "manufacturing", "employees": "550", "orders_12m": "12"})[0]
	unlike := ask(map[string]string{"industry": "retail", "employees": "20", "orders_12m": "1"})[0]
	t.Logf("alike %.0f, unlike %.0f", like, unlike)
	if !(like > 30 && unlike < 10) {
		t.Errorf("scores off")
	}
}

func TestMultiLabel(t *testing.T) {
	// Cafes buy coffee and milk, bakeries flour and sugar, and both
	// sometimes buy cups.
	var csv strings.Builder
	csv.WriteString("segment,orders_12m,product_lines\n")
	r := rand.New(rand.NewPCG(3, 4))
	for i := range 900 {
		seg := []string{"cafe", "bakery"}[i%2]
		var ls []string
		if seg == "cafe" {
			ls = append(ls, "coffee")
			if r.Float64() < 0.8 {
				ls = append(ls, "milk")
			}
		} else {
			ls = append(ls, "flour")
			if r.Float64() < 0.7 {
				ls = append(ls, "sugar")
			}
		}
		if r.Float64() < 0.3 {
			ls = append(ls, "cups")
		}
		fmt.Fprintf(&csv, "%s,%d,%s\n", seg, r.IntN(20), strings.Join(ls, "; "))
	}
	res, ask := fitCells(t, "multi-label", "product_lines", csv.String())
	if strings.Join(res.Spec.Labels, ",") != "coffee,cups,flour,milk,sugar" {
		t.Fatalf("labels %v", res.Spec.Labels)
	}
	if a := res.History[0].Accuracy; a < 0.75 {
		t.Errorf("AUC %v", a)
	}
	out := ask(map[string]string{"segment": "cafe", "orders_12m": "10"})
	t.Logf("cafe %v", out)
	if !(out[0] > 0.9 && out[2] < 0.1 && out[3] > 0.6 && out[1] > 0.15 && out[1] < 0.45) {
		t.Errorf("chances off")
	}
}

func TestDiscountResponse(t *testing.T) {
	// Each 5% of discount raises the chance of closing, more for small
	// shops; a bigger discount gives away more.
	var csv strings.Builder
	csv.WriteString("segment,deal_size,discount_pct,won\n")
	r := rand.New(rand.NewPCG(5, 6))
	for range 3000 {
		small := r.IntN(2) == 0
		d := float64(5 * r.IntN(5)) // 0 to 20
		p := 0.2 + d/100
		if small {
			p = 0.1 + 0.03*d
		}
		seg := map[bool]string{true: "small", false: "large"}[small]
		fmt.Fprintf(&csv, "%s,%d,%.0f,%v\n", seg, 1000+r.IntN(9000), d, map[bool]string{true: "yes", false: "no"}[r.Float64() < p])
	}
	res, ask := fitCells(t, "discount-response", "won", csv.String())
	if got := strings.Join(res.Spec.Labels, ","); got != "best discount %,chance at the best discount,chance at 0%,chance at 5%,chance at 10%,chance at 15%,chance at 20%" {
		t.Fatalf("labels %v", got)
	}
	small := ask(map[string]string{"segment": "small", "deal_size": "5000"})
	large := ask(map[string]string{"segment": "large", "deal_size": "5000"})
	t.Logf("small %v, large %v", small, large)
	for i := 3; i < len(small); i++ {
		if small[i] < small[i-1] {
			t.Errorf("a bigger discount lowered the chance: %v", small)
		}
	}
	// Small shops: 0.1 at 0% to 0.7 at 20%, so 20% keeps the most
	// (0.56); large: 0.2 to 0.4, so 20% keeps 0.32 and 0% 0.2.
	if !(small[2] < 0.25 && small[6] > 0.5 && small[0] == 20) {
		t.Errorf("small shops off")
	}
	if !(large[2] < large[6] && large[6] < 0.55) {
		t.Errorf("large shops off")
	}
}

func TestNumberRange(t *testing.T) {
	// Units grow with stores; the spread is wider for big chains.
	var csv strings.Builder
	csv.WriteString("chain,stores,units\n")
	r := rand.New(rand.NewPCG(7, 8))
	for range 2000 {
		big := r.IntN(2) == 0
		stores := 1 + r.IntN(5)
		mean, sd := 10*float64(stores), 2.0
		if big {
			stores += 20
			mean, sd = 10*float64(stores), 30
		}
		fmt.Fprintf(&csv, "%v,%d,%.1f\n", map[bool]string{true: "big", false: "small"}[big], stores, max(0, mean+sd*r.NormFloat64()))
	}
	res, ask := fitCells(t, "number-range", "units", csv.String())
	if c := res.History[0].Accuracy; c < 0.72 || c > 0.88 {
		t.Errorf("%.2f of held rows in their range, want about 0.8", c)
	}
	small := ask(map[string]string{"chain": "small", "stores": "3"})
	big := ask(map[string]string{"chain": "big", "stores": "23"})
	t.Logf("small %v, big %v, typical error %.1f", small, big, res.History[0].MAE)
	if !(small[1] < small[0] && small[0] < small[2] && small[2]-small[1] < 15) {
		t.Errorf("small range off")
	}
	if !(big[2]-big[1] > 40 && big[0] > 200 && big[0] < 260) {
		t.Errorf("big range off")
	}
}

func TestFindDuplicates(t *testing.T) {
	var csv strings.Builder
	csv.WriteString("customer_id,customer_name,city\n")
	rows := [][2]string{
		{"PT Maju Jaya", "Jakarta"}, {"Maju Jaya, PT", "Jakarta"}, {"CV Sumber Rejeki", "Bandung"},
		{"Toko Sinar Abadi", "Surabaya"}, {"Sinar Abadi Toko", "Surabaya"}, {"PT Maju Bersama", "Medan"},
	}
	for i := range 40 {
		rows = append(rows, [2]string{fmt.Sprintf("Toko Aneka %d", i), "Jakarta"})
	}
	for i, r := range rows {
		fmt.Fprintf(&csv, "C%d,%s,%s\n", i, strings.ReplaceAll(r[0], ",", ""), r[1])
	}
	res, ask := fitCells(t, "find-duplicates", "city", csv.String())
	labels := res.Spec.Labels
	if labels[len(labels)-1] != arch.NoMatch || len(res.Columns) != 3 {
		t.Fatalf("labels %v, columns %v", labels[len(labels)-5:], res.Columns)
	}
	best := func(fields map[string]string) string {
		out := ask(fields)
		at := 0
		for i, v := range out {
			if v > out[at] {
				at = i
			}
		}
		return labels[at]
	}
	if got := best(map[string]string{"customer_id": "C0", "customer_name": "PT Maju Jaya", "city": "Jakarta"}); got != "C1 Maju Jaya PT" {
		t.Errorf("PT Maju Jaya matched %q", got)
	}
	if got := best(map[string]string{"customer_name": "maju jaya", "city": "jakarta"}); got != "C0 PT Maju Jaya" && got != "C1 Maju Jaya PT" {
		t.Errorf("a new lead matched %q", got)
	}
	if got := best(map[string]string{"customer_id": "C2", "customer_name": "CV Sumber Rejeki", "city": "Bandung"}); got != arch.NoMatch {
		t.Errorf("a one-off matched %q", got)
	}
	if got := best(map[string]string{"customer_name": "Sinar Abadi", "city": "Surabaya"}); !strings.Contains(got, "Sinar Abadi") {
		t.Errorf("Sinar Abadi matched %q", got)
	}
}
