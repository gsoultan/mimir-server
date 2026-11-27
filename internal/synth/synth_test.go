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

package synth

import (
	"errors"
	"fmt"
	"math"
	"math/rand/v2"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gsoultan/mimir-server/internal/schema"
)

// table is 400 rows where price rises with size, the plan follows size,
// a customer ID differs in every row and one in ten cells of note is
// empty.
func table() ([]string, [][]string) {
	r := rand.New(rand.NewPCG(1, 2))
	header := []string{"customer_id", "size", "price", "plan", "joined", "note"}
	var rows [][]string
	day := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	for i := range 400 {
		size := 10 + r.Float64()*90
		price := size*3 + r.NormFloat64()*5
		plan := "basic"
		if size > 60 {
			plan = "pro"
		}
		note := "ok"
		if r.IntN(10) == 0 {
			note = ""
		}
		rows = append(rows, []string{
			fmt.Sprintf("CUST-%04d", i), strconv.FormatFloat(size, 'f', 1, 64), strconv.FormatFloat(price, 'f', 2, 64),
			plan, day.AddDate(0, 0, r.IntN(300)).Format("2006-01-02"), note,
		})
	}
	return header, rows
}

func TestRowsKeepColumnsAndHowTheyMoveTogether(t *testing.T) {
	header, real := table()
	m, err := Fit(header, real, map[string]schema.Kind{"size": schema.Number, "plan": schema.Category}, 7)
	if err != nil {
		t.Fatal(err)
	}
	made := m.Rows(2000, 3)
	if len(made) != 2000 || len(made[0]) != len(header) {
		t.Fatalf("got %d rows of %d cells", len(made), len(made[0]))
	}
	realIDs := map[string]bool{}
	for _, r := range real {
		realIDs[r[0]] = true
	}
	var sizes, prices []float64
	empty, pro := 0, 0
	for _, row := range made {
		if realIDs[row[0]] {
			t.Fatalf("made-up row copies the real ID %q", row[0])
		}
		if !strings.HasPrefix(row[0], "CUST-") {
			t.Errorf("made-up ID %q doesn't look like the real ones", row[0])
		}
		s, err1 := strconv.ParseFloat(row[1], 64)
		p, err2 := strconv.ParseFloat(row[2], 64)
		if err1 != nil || err2 != nil {
			t.Fatalf("not numbers: %q %q", row[1], row[2])
		}
		if s < 10 || s > 100 {
			t.Errorf("size %v outside the real range", s)
		}
		if row[3] != "basic" && row[3] != "pro" {
			t.Errorf("plan %q isn't a real one", row[3])
		}
		if row[3] == "pro" {
			pro++
		}
		if _, err := time.Parse("2006-01-02", row[4]); err != nil {
			t.Errorf("joined %q isn't written as the real dates are", row[4])
		}
		if row[5] == "" {
			empty++
		}
		sizes, prices = append(sizes, s), append(prices, p)
	}
	if c := pearson(sizes, prices); c < 0.9 {
		t.Errorf("size and price correlate %.2f in the made-up rows; the real ones are near 1", c)
	}
	if share := float64(empty) / 2000; share < 0.05 || share > 0.15 {
		t.Errorf("%.0f%% of notes are empty; the real ones about 10%%", share*100)
	}
	if share := float64(pro) / 2000; share < 0.3 || share > 0.6 {
		t.Errorf("%.0f%% are pro; the real ones about 44%%", share*100)
	}
	// The plan follows size, as it does in the real rows; a copula keeps
	// such a step only roughly, so most rows, not all, agree.
	agree := 0
	for i, row := range made {
		if (sizes[i] > 60) == (row[3] == "pro") {
			agree++
		}
	}
	if float64(agree)/2000 < 0.65 {
		t.Errorf("plan follows size in %d of 2000 made-up rows", agree)
	}
	if again := m.Rows(2000, 3); !slices.EqualFunc(again, made, slices.Equal) {
		t.Error("the same seed made other rows")
	}
}

func TestFitNeedsRows(t *testing.T) {
	header, rows := table()
	if _, err := Fit(header, rows[:MinRows-1], nil, 1); !errors.Is(err, ErrTooFew) {
		t.Fatalf("err %v", err)
	}
}

func TestBalanceFillsTheRareAnswer(t *testing.T) {
	// Two numbers and a category of three, one-hot. Answer 1 is rare and
	// sits where both numbers are high.
	const width = 5
	r := rand.New(rand.NewPCG(3, 4))
	var x []float32
	var y []int32
	for i := range 220 {
		c := int32(0)
		lo := float32(0)
		if i%11 == 0 {
			c, lo = 1, 5
		}
		hot := make([]float32, 3)
		hot[r.IntN(3)] = 1
		x = append(x, lo+r.Float32(), lo+r.Float32())
		x = append(x, hot...)
		y = append(y, c)
	}
	rows := make([]int, len(y))
	for i := range rows {
		rows[i] = i
	}
	groups := []Group{{1, true}, {1, true}, {3, false}}
	mx, my := Balance(x, width, y, rows, groups, nil, 1_000_000, 9)
	if len(my) != 200-20 || len(mx) != len(my)*width {
		t.Fatalf("made %d rows (%d numbers); want 180", len(my), len(mx))
	}
	for i, c := range my {
		if c != 1 {
			t.Fatalf("made a row of answer %d", c)
		}
		row := mx[i*width : (i+1)*width]
		if row[0] < 5 || row[0] > 6 || row[1] < 5 || row[1] > 6 {
			t.Fatalf("made-up row %v isn't between the rare rows", row)
		}
		if row[2]+row[3]+row[4] != 1 {
			t.Fatalf("made-up row's category %v isn't one of the real ones", row[2:])
		}
	}
	if _, my := Balance(x, width, y, rows, groups, nil, 10, 9); len(my) != 10 {
		t.Errorf("limit 10 made %d", len(my))
	}
}

func TestPhiInv(t *testing.T) {
	for _, p := range []float64{1e-6, 0.01, 0.2, 0.5, 0.9, 0.999} {
		if got := phi(phiInv(p)); math.Abs(got-p) > 1e-9 {
			t.Errorf("phi(phiInv(%v)) = %v", p, got)
		}
	}
}

func pearson(a, b []float64) float64 {
	var ma, mb float64
	for i := range a {
		ma += a[i]
		mb += b[i]
	}
	ma /= float64(len(a))
	mb /= float64(len(b))
	var sab, saa, sbb float64
	for i := range a {
		sab += (a[i] - ma) * (b[i] - mb)
		saa += (a[i] - ma) * (a[i] - ma)
		sbb += (b[i] - mb) * (b[i] - mb)
	}
	return sab / math.Sqrt(saa*sbb)
}
