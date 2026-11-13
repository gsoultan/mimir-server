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


// Package drift tells when the questions a table model is asked stop
// looking like the examples it learned from, column by column. It needs
// no feedback: only the questions themselves.
//
// Each column's cells are sorted into a few buckets (empty, a range of
// numbers, one of the values it knows, something it never saw), and the
// share in each bucket of recent questions is weighed against the share
// in the examples with the population stability index (PSI). The score is
// lowered by what chance alone gives a sample that size, so a model asked
// a hundred questions isn't warned about noise.
package drift

import (
	"cmp"
	"fmt"
	"math"
	"math/rand/v2"
	"slices"
	"strconv"
	"strings"

	"github.com/gsoultan/mimir-server/internal/schema"
)

const (
	// MinQuestions is how many recent questions a check needs.
	MinQuestions = 100
	// Little and ALot are the scores at which a column has changed a
	// little (shown) and a lot (alerted). 0.1 and 0.25 are PSI's usual
	// lines.
	Little = 0.1
	ALot   = 0.25
	// bins is how many ranges a number column is cut into.
	bins = 10
	// sampleCap is how many of a number column's cells are kept to find
	// its ranges.
	sampleCap = 20_000
)

// Column is what one input column's cells looked like in the examples.
type Column struct {
	Name string      `json:"name"`
	Kind schema.Kind `json:"kind"`
	// Shares is each bucket's share of the cells; see buckets.
	Shares []float64 `json:"shares"`
	Rows   int       `json:"rows"`
	// Edges cut a number column's cells into ranges of about equal share:
	// a cell up to Edges[i] falls in range i.
	Edges []float64 `json:"edges,omitempty"`
	// Low and High hold the middle 80% of a number column's cells.
	Low  float64 `json:"low,omitempty"`
	High float64 `json:"high,omitempty"`
	// Values are a category or key column's values, as schema has them.
	Values []string `json:"values,omitempty"`
}

// Change is a column whose recent cells differ from the examples'.
type Change struct {
	Column string  `json:"column"`
	Score  float64 `json:"score"`
	// ALot says the change is big enough to alert about.
	ALot bool `json:"a_lot"`
	// Summary says what changed, in plain words.
	Summary string `json:"summary"`
}

// Checked reports whether a column of kind k is compared at all: photos
// are not.
func Checked(k schema.Kind) bool { return k != schema.Photo }

// The buckets of a column, by kind, after bucket 0 (empty):
//
//	number:          one per range, then "not a number"
//	category, key:   one per value, then "a value it never saw"
//	yes or no:       yes, no, then "neither"
//	text:            filled
func size(c Column) int {
	switch c.Kind {
	case schema.Number:
		return len(c.Edges) + 3
	case schema.Category, schema.Key:
		return len(c.Values) + 2
	case schema.YesNo:
		return 4
	}
	return 2
}

// bucket returns which bucket cell v of c falls in, and v as a number
// when it is one.
func bucket(c Column, v string) (int, float64, bool) {
	v = strings.TrimSpace(v)
	if v == "" {
		return 0, 0, false
	}
	switch c.Kind {
	case schema.Number:
		f, ok := schema.ParseNumber(v)
		if !ok {
			return len(c.Edges) + 2, 0, false
		}
		i, _ := slices.BinarySearch(c.Edges, f)
		return 1 + i, f, true
	case schema.Category, schema.Key:
		if i := slices.Index(c.Values, v); i >= 0 {
			return 1 + i, 0, false
		}
		return len(c.Values) + 1, 0, false
	case schema.YesNo:
		switch b, ok := schema.ParseYesNo(v); {
		case !ok:
			return 3, 0, false
		case b:
			return 1, 0, false
		default:
			return 2, 0, false
		}
	}
	return 1, 0, false
}

// Builder makes the []Column of a table's examples from its rows, read
// one at a time.
type Builder struct {
	cols    []schema.Column
	counts  [][]float64 // per column, per bucket; numbers' ranges come later
	samples [][]float64 // per number column, up to sampleCap cells
	seen    []int       // per number column, cells read as numbers
	rows    int
	rnd     *rand.Rand
}

// NewBuilder returns a Builder for rows of cols, in that order.
func NewBuilder(cols []schema.Column) *Builder {
	b := &Builder{cols: cols, counts: make([][]float64, len(cols)), samples: make([][]float64, len(cols)), seen: make([]int, len(cols)), rnd: rand.New(rand.NewPCG(1, 2))}
	for j, c := range cols {
		switch c.Kind {
		case schema.Number:
			b.counts[j] = make([]float64, 3) // empty, a number, not a number
		default:
			b.counts[j] = make([]float64, size(Column{Kind: c.Kind, Values: c.Values}))
		}
	}
	return b
}

// Add reads one row; cells[j] is column j's cell.
func (b *Builder) Add(cells []string) {
	b.rows++
	for j, c := range b.cols {
		if !Checked(c.Kind) {
			continue
		}
		v := ""
		if j < len(cells) {
			v = cells[j]
		}
		if c.Kind != schema.Number {
			i, _, _ := bucket(Column{Kind: c.Kind, Values: c.Values}, v)
			b.counts[j][i]++
			continue
		}
		v = strings.TrimSpace(v)
		f, ok := schema.ParseNumber(v)
		switch {
		case v == "":
			b.counts[j][0]++
		case !ok:
			b.counts[j][2]++
		default:
			b.counts[j][1]++
			// A reservoir: every number cell has the same chance to be kept.
			if b.seen[j]++; len(b.samples[j]) < sampleCap {
				b.samples[j] = append(b.samples[j], f)
			} else if k := b.rnd.IntN(b.seen[j]); k < sampleCap {
				b.samples[j][k] = f
			}
		}
	}
}

// Columns returns what the rows read so far look like.
func (b *Builder) Columns() []Column {
	var out []Column
	if b.rows == 0 {
		return nil
	}
	n := float64(b.rows)
	for j, c := range b.cols {
		if !Checked(c.Kind) {
			continue
		}
		col := Column{Name: c.Name, Kind: c.Kind, Rows: b.rows}
		if c.Kind == schema.Category || c.Kind == schema.Key {
			col.Values = c.Values
		}
		counts := b.counts[j]
		if c.Kind == schema.Number {
			s := slices.Clone(b.samples[j])
			slices.Sort(s)
			col.Edges = edges(s)
			col.Low, col.High = quantile(s, 0.1), quantile(s, 0.9)
			ranges := make([]float64, len(col.Edges)+1)
			for _, f := range s {
				i, _ := slices.BinarySearch(col.Edges, f)
				ranges[i]++
			}
			numbers := counts[1]
			counts = append([]float64{counts[0]}, ranges...)
			for i := 1; i < len(counts); i++ {
				counts[i] = numbers * counts[i] / float64(max(len(s), 1))
			}
			counts = append(counts, b.counts[j][2])
		}
		col.Shares = make([]float64, len(counts))
		for i, k := range counts {
			col.Shares[i] = k / n
		}
		out = append(out, col)
	}
	return out
}

// edges cuts sorted numbers into bins ranges of about equal share; a
// value that fills more than one range keeps one edge.
func edges(s []float64) []float64 {
	var out []float64
	for i := 1; i < bins; i++ {
		e := quantile(s, float64(i)/bins)
		if len(s) > 0 && (len(out) == 0 || e > out[len(out)-1]) {
			out = append(out, e)
		}
	}
	return out
}

// quantile is the value below which share q of sorted s falls.
func quantile(s []float64, q float64) float64 {
	if len(s) == 0 {
		return 0
	}
	i := int(math.Round(q * float64(len(s)-1)))
	return s[i]
}

// Compare weighs recent questions, each a cell by column name, against
// the examples' columns, and returns the columns that changed at least a
// little, biggest change first. It returns nil for fewer than
// MinQuestions questions.
func Compare(base []Column, questions []map[string]string) []Change {
	if len(questions) < MinQuestions {
		return nil
	}
	var out []Change
	n := float64(len(questions))
	for _, c := range base {
		if len(c.Shares) != size(c) || c.Rows == 0 {
			continue // written by another version of this package
		}
		counts := make([]float64, size(c))
		var nums []float64
		unseen := map[string]int{}
		for _, q := range questions {
			v := q[c.Name]
			i, f, isNum := bucket(c, v)
			counts[i]++
			if isNum {
				nums = append(nums, f)
			}
			if (c.Kind == schema.Category || c.Kind == schema.Key) && i == len(counts)-1 {
				unseen[strings.TrimSpace(v)]++
			}
		}
		score, parts := psi(c.Shares, float64(c.Rows), counts, n)
		if score < Little {
			continue
		}
		recent := make([]float64, len(counts))
		for i, k := range counts {
			recent[i] = k / n
		}
		slices.Sort(nums)
		out = append(out, Change{
			Column:  c.Name,
			Score:   math.Round(score*1000) / 1000,
			ALot:    score >= ALot,
			Summary: summary(c, recent, parts, nums, unseen),
		})
	}
	slices.SortStableFunc(out, func(a, b Change) int { return cmp.Compare(b.Score, a.Score) })
	return out
}

// psi is the population stability index of recent counts (out of n)
// against base shares (out of rows), less what sampling alone gives, and
// each bucket's part of it. Counts are smoothed so an empty bucket on one
// side doesn't read as an endless change.
func psi(base []float64, rows float64, counts []float64, n float64) (float64, []float64) {
	k := float64(len(base))
	parts := make([]float64, len(base))
	total, used := 0.0, 0
	for i := range base {
		if base[i] == 0 && counts[i] == 0 {
			continue
		}
		used++
		p := (base[i]*rows + 0.5) / (rows + 0.5*k)
		q := (counts[i] + 0.5) / (n + 0.5*k)
		parts[i] = (q - p) * math.Log(q/p)
		total += parts[i]
	}
	// Two samples of one source differ by about (buckets-1)(1/n + 1/rows),
	// give or take sqrt(2(buckets-1)) times that unit: take off three of
	// those too, so chance alone almost never reads as a change.
	df := float64(max(used-1, 0))
	noise := (df + 3*math.Sqrt(2*df)) * (1/n + 1/rows)
	return max(total-noise, 0), parts
}

// summary says in plain words what changed most in column c.
func summary(c Column, recent, parts []float64, nums []float64, unseen map[string]int) string {
	top := 0
	for i := range parts {
		if parts[i] > parts[top] {
			top = i
		}
	}
	was, now := c.Shares[top], recent[top]
	last := len(parts) - 1
	switch {
	case top == 0 && now >= 0.999:
		return "Recent questions leave it out."
	case top == 0:
		return fmt.Sprintf("Empty in %s of recent questions, against %s of the examples it learned from.", pct(now), pct(was))
	case c.Kind == schema.Number && top == last:
		return fmt.Sprintf("%s of recent questions have something other than a number here, against %s of the examples.", upper(pct(now)), pct(was))
	case c.Kind == schema.Number:
		if len(nums) == 0 {
			break
		}
		return fmt.Sprintf("Mostly %s to %s in the examples it learned from; mostly %s to %s in recent questions.", num(c.Low), num(c.High), num(quantile(nums, 0.1)), num(quantile(nums, 0.9)))
	case (c.Kind == schema.Category || c.Kind == schema.Key) && top == last:
		s := fmt.Sprintf("%s of recent questions have a value it never saw while learning", upper(pct(now)))
		if v := common(unseen); v != "" {
			s += fmt.Sprintf(", such as %q", v)
		}
		return s + "."
	case c.Kind == schema.Category || c.Kind == schema.Key:
		return fmt.Sprintf("%q is in %s of recent questions, against %s of the examples it learned from.", c.Values[top-1], pct(now), pct(was))
	case c.Kind == schema.YesNo && top == last:
		return fmt.Sprintf("%s of recent questions have something other than yes or no here.", upper(pct(now)))
	case c.Kind == schema.YesNo:
		word := "Yes"
		if top == 2 {
			word = "No"
		}
		return fmt.Sprintf("%s in %s of recent questions, against %s of the examples it learned from.", word, pct(now), pct(was))
	}
	return "Recent questions look different here from the examples it learned from."
}

// common is the most frequent of the values, the first by name on a tie.
func common(m map[string]int) string {
	best, n := "", 0
	for v, k := range m {
		if k > n || (k == n && v < best) {
			best, n = v, k
		}
	}
	return best
}

func pct(f float64) string {
	switch p := f * 100; {
	case p > 0 && p < 1:
		return "under 1%"
	default:
		return strconv.Itoa(int(math.Round(p))) + "%"
	}
}

func upper(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

// num writes a number short: 3 significant digits, thousands grouped.
func num(f float64) string {
	if math.Abs(f) >= 1000 {
		s := strconv.FormatInt(int64(math.Round(math.Abs(f))), 10)
		for i := len(s) - 3; i > 0; i -= 3 {
			s = s[:i] + "," + s[i:]
		}
		if f < 0 {
			s = "-" + s
		}
		return s
	}
	return strconv.FormatFloat(f, 'g', 3, 64)
}
