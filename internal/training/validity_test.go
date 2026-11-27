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
	"math"
	"testing"

	"github.com/gsoultan/mimir/tensor"

	"github.com/gsoultan/mimir-server/internal/arch"
)

func TestWilson(t *testing.T) {
	// 90 of 100 right: the textbook Wilson interval is 0.8256 to 0.9448.
	lo, hi := wilson(90, 100)
	if math.Abs(lo-0.8256) > 1e-3 || math.Abs(hi-0.9448) > 1e-3 {
		t.Fatalf("90/100: %v to %v", lo, hi)
	}
	// Fewer examples, wider range; it never leaves 0 to 1.
	slo, shi := wilson(9, 10)
	if shi-slo <= hi-lo || slo < 0 || shi > 1 {
		t.Fatalf("9/10: %v to %v", slo, shi)
	}
	if lo, hi := wilson(10, 10); hi < 1-1e-9 || lo <= 0.6 {
		t.Fatalf("10/10: %v to %v", lo, hi)
	}
	if lo, hi := wilson(0, 0); lo != 0 || hi != 0 {
		t.Fatalf("none: %v to %v", lo, hi)
	}
}

func TestScorerBaselineAndRange(t *testing.T) {
	ctx := context.Background()
	s := &scorer{kind: string(arch.Class), labels: []string{"no", "yes"}, confusion: [][]int64{{0, 0}, {0, 0}}}
	// Three "yes", one "no"; it says "yes" every time.
	pred := tensor.FromSlice([]float32{0, 1, 0, 1, 0, 1, 0, 1}, 4, 2)
	target := tensor.FromSlice([]int32{0, 1, 1, 1}, 4)
	if err := s.Update(ctx, pred, target); err != nil {
		t.Fatal(err)
	}
	r := s.report()
	if r.BaselineLabel != "yes" || !near(r.Baseline, 0.75) || !near(r.Accuracy, r.Baseline) {
		t.Fatalf("baseline %q %v, accuracy %v", r.BaselineLabel, r.Baseline, r.Accuracy)
	}
	if r.AccuracyLow >= r.Accuracy || r.AccuracyHigh <= r.Accuracy {
		t.Fatalf("range %v to %v around %v", r.AccuracyLow, r.AccuracyHigh, r.Accuracy)
	}

	n := &scorer{kind: string(arch.Number)}
	// Misses of 1, 3, 1, 3: MAE 2, spread 1, so 2 ± 1.96/2.
	if err := n.Update(ctx, tensor.FromSlice([]float32{11, 13, 21, 23}, 4, 1), tensor.FromSlice([]float32{10, 10, 20, 20}, 4, 1)); err != nil {
		t.Fatal(err)
	}
	nr := n.report()
	if !near(nr.MAE, 2) || math.Abs(nr.MAELow-(2-z95/2)) > 1e-6 || math.Abs(nr.MAEHigh-(2+z95/2)) > 1e-6 {
		t.Fatalf("MAE %v, %v to %v", nr.MAE, nr.MAELow, nr.MAEHigh)
	}
}

func TestSplitKeepsCopiesTogether(t *testing.T) {
	// 100 rows, the last 10 copies of the first 10, as a row exported
	// twice: none may be both scored and learned from, and what it learns
	// from stays the same.
	const n = 100
	of := func(i int) int {
		if i >= 90 {
			return i - 90
		}
		return i
	}
	cls := make([]int32, n)
	proto := &examples{shape: []int{1}, input: func(dst []float32, i int) []float32 { return append(dst, float32(of(i))) }, cls: cls}
	moved := 0
	for seed := range uint64(20) {
		train, test, err := split(n, 0.2, seed, 8, proto)
		if err != nil {
			t.Fatal(err)
		}
		learned := map[int]bool{}
		for _, i := range train.(*examples).rows {
			learned[of(i)] = true
		}
		for _, i := range test.(*examples).rows {
			if learned[of(i)] {
				t.Fatalf("seed %d: row %d is held out and learned from", seed, i)
			}
		}
		if train.(*examples).Len() != 80 || test.(*examples).Len()+Copies(train) != 20 {
			t.Fatalf("seed %d: %d learned, %d held, %d moved", seed, train.(*examples).Len(), test.(*examples).Len(), Copies(train))
		}
		moved += Copies(train)
	}
	if moved == 0 {
		t.Fatal("no copies moved")
	}

	// Distinct rows: nothing moves.
	proto.input = func(dst []float32, i int) []float32 { return append(dst, float32(i)) }
	if train, test, _ := split(n, 0.2, 3, 8, proto); Copies(train) != 0 || test.(*examples).Len() != 20 {
		t.Fatalf("distinct rows moved: %d", Copies(train))
	}
	// Data that simply repeats, 5 rows over and over, is split at random.
	proto.input = func(dst []float32, i int) []float32 { return append(dst, float32(i%5)) }
	if train, test, _ := split(n, 0.2, 3, 8, proto); Copies(train) != 0 || test.(*examples).Len() != 20 {
		t.Fatalf("repeating rows moved: %d", Copies(train))
	}
	// The same input with another answer isn't a copy.
	proto.input = func(dst []float32, i int) []float32 { return append(dst, float32(of(i))) }
	for i := range cls {
		cls[i] = int32(i / 90)
	}
	if train, _, _ := split(n, 0.2, 3, 8, proto); Copies(train) != 0 {
		t.Fatalf("different answers moved: %d", Copies(train))
	}
}
