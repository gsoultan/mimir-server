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
	"math/rand/v2"
	"slices"
)

// Group is a run of a row's numbers that come from one column. Blend
// says they can be mixed between two rows, as a measurement can; others,
// like a category's, are copied whole from one of them.
type Group struct {
	Width int
	Blend bool
}

// Neighbours is how many of a row's nearest rows of the same answer a
// made-up one may be made towards, as SMOTE's k.
const Neighbours = 5

// pool bounds the rows of one answer that neighbours are looked for
// among, so a large answer costs no more than a small one.
const pool = 500

// Balance makes rows of each answer that has fewer rows than the most
// common one, until it has as many, as SMOTE does: each made-up row lies
// between a real row and one of its nearest neighbours with the same
// answer. x holds the rows' numbers, width per row; y their answers;
// rows which of them to learn from and balance. scale weighs each number
// when judging how near rows are (1 over its spread, say). At most limit
// rows are made. It returns the made-up rows' numbers and answers.
func Balance(x []float32, width int, y []int32, rows []int, groups []Group, scale []float32, limit int, seed uint64) ([]float32, []int32) {
	byClass := map[int32][]int{}
	for _, i := range rows {
		byClass[y[i]] = append(byClass[y[i]], i)
	}
	most := 0
	for _, idx := range byClass {
		most = max(most, len(idx))
	}
	classes := make([]int32, 0, len(byClass))
	for c := range byClass {
		classes = append(classes, c)
	}
	slices.Sort(classes)
	blend := make([]bool, width)
	at := 0
	for _, g := range groups {
		for k := range g.Width {
			if at+k < width {
				blend[at+k] = g.Blend
			}
		}
		at += g.Width
	}
	// Where each copied group starts and ends, so it comes whole from one
	// row.
	var copied [][2]int
	at = 0
	for _, g := range groups {
		if !g.Blend && g.Width > 0 {
			copied = append(copied, [2]int{at, min(at+g.Width, width)})
		}
		at += g.Width
	}

	r := rand.New(rand.NewPCG(seed, 0x5307e))
	var outX []float32
	var outY []int32
	for _, c := range classes {
		idx := byClass[c]
		need := min(most-len(idx), limit-len(outY))
		if need <= 0 {
			continue
		}
		base := idx
		if len(base) > pool {
			base = slices.Clone(idx)
			r.Shuffle(len(base), func(i, j int) { base[i], base[j] = base[j], base[i] })
			base = base[:pool]
		}
		near := nearest(x, width, base, scale, blend)
		row := make([]float32, width)
		for range need {
			k := r.IntN(len(base))
			a := base[k]
			b := a
			if len(near[k]) > 0 {
				b = base[near[k][r.IntN(len(near[k]))]]
			}
			gap := r.Float32()
			ra, rb := x[a*width:(a+1)*width], x[b*width:(b+1)*width]
			for j := range width {
				if blend[j] {
					row[j] = ra[j] + gap*(rb[j]-ra[j])
				}
			}
			for _, g := range copied {
				from := ra
				if r.IntN(2) == 1 {
					from = rb
				}
				copy(row[g[0]:g[1]], from[g[0]:g[1]])
			}
			outX = append(outX, row...)
			outY = append(outY, c)
		}
		if len(outY) >= limit {
			break
		}
	}
	return outX, outY
}

// nearest returns, for each of rows, the places in rows of its nearest
// Neighbours others, judged on the numbers that blend; with none, on
// all of them.
func nearest(x []float32, width int, rows []int, scale []float32, blend []bool) [][]int {
	use := make([]int, 0, width)
	for j := range width {
		if blend[j] {
			use = append(use, j)
		}
	}
	if len(use) == 0 {
		for j := range width {
			use = append(use, j)
		}
	}
	w := func(j int) float32 {
		if j < len(scale) && scale[j] > 0 {
			return scale[j]
		}
		return 1
	}
	k := min(Neighbours, len(rows)-1)
	out := make([][]int, len(rows))
	type cand struct {
		at int
		d  float32
	}
	cands := make([]cand, 0, len(rows))
	for i, a := range rows {
		cands = cands[:0]
		ra := x[a*width : (a+1)*width]
		for j, b := range rows {
			if j == i {
				continue
			}
			rb := x[b*width : (b+1)*width]
			var d float32
			for _, f := range use {
				v := (ra[f] - rb[f]) * w(f)
				d += v * v
			}
			cands = append(cands, cand{j, d})
		}
		slices.SortFunc(cands, func(p, q cand) int {
			switch {
			case p.d < q.d:
				return -1
			case p.d > q.d:
				return 1
			}
			return p.at - q.at
		})
		out[i] = make([]int, 0, k)
		for _, c := range cands[:k] {
			out[i] = append(out[i], c.at)
		}
	}
	return out
}
