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

// Package synth makes up examples that look like real ones: rows like a
// table's own, and extra rows of a rare answer while a model learns.
//
// Rows learns a table with a Gaussian copula, as SDV's
// GaussianCopulaSynthesizer does: each column's own spread of values,
// and how the columns move together. Balance makes rows of a rare answer
// between neighbouring real ones, as SMOTE does.
package synth

import (
	"cmp"
	"errors"
	"fmt"
	"math"
	"math/rand/v2"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/gsoultan/mimir-server/internal/schema"
)

// ErrTooFew reports a table too small to learn from.
var ErrTooFew = errors.New("synth: too few rows")

// MinRows is the fewest rows Rows learns from.
const MinRows = 10

// fitRows bounds the rows the columns' movement together is learned
// from; more change it little and cost more.
const fitRows = 20000

// kind is how Rows treats a column.
type kind int

const (
	numeric kind = iota // a number: made between the real values
	date                // a date or time, made as a number and written back
	choice              // one of the real values, as often as they come
	fresh               // a different value in every row, like an ID: made new
)

// column is what Rows learned about one column.
type column struct {
	name  string
	kind  kind
	empty float64 // the share of empty cells
	// numeric and date: the filled values, sorted, and how to write one.
	sorted []float64
	whole  bool
	places int
	layout string
	// choice: the values and where each one's share ends, from 0 to 1.
	values []string
	ends   []float64
	// fresh: what a made-up value starts with.
	prefix string
}

// Model is what Rows learned about a table: each column, and how they
// move together.
type Model struct {
	header []string
	cols   []column
	chol   [][]float64 // the lower Cholesky factor of the columns' correlation
}

// Fit learns a table's columns from its header and rows. kinds, when set,
// say what an input column holds, as the server first read it; the other
// columns are judged from their cells.
func Fit(header []string, rows [][]string, kinds map[string]schema.Kind, seed uint64) (*Model, error) {
	if len(rows) < MinRows {
		return nil, fmt.Errorf("%w: at least %d are needed to learn from", ErrTooFew, MinRows)
	}
	r := rand.New(rand.NewPCG(seed, 0x5eed))
	m := &Model{header: header, cols: make([]column, len(header))}
	cells := make([]string, len(rows))
	for j, name := range header {
		for i, row := range rows {
			if j < len(row) {
				cells[i] = strings.TrimSpace(row[j])
			} else {
				cells[i] = ""
			}
		}
		m.cols[j] = learnColumn(name, cells, kinds[name])
	}

	// How the columns move together, in normal space, from at most
	// fitRows of the rows.
	// Wide tables use fewer rows, so the pairs of columns cost about the
	// same.
	d := max(len(header), 1)
	pick := r.Perm(len(rows))[:min(len(rows), fitRows, max(500, 200_000_000/(d*d)))]
	z := make([][]float64, len(header))
	for j := range header {
		z[j] = make([]float64, len(pick))
		c := &m.cols[j]
		for k, i := range pick {
			v := ""
			if j < len(rows[i]) {
				v = strings.TrimSpace(rows[i][j])
			}
			z[j][k] = c.normal(v, r)
		}
	}
	m.chol = cholesky(correlation(z))
	return m, nil
}

// Rows makes n rows like the ones the model learned from, the same model
// and seed always giving the same rows.
func (m *Model) Rows(n int, seed uint64) [][]string {
	r := rand.New(rand.NewPCG(seed, 0xc0b1a))
	d := len(m.cols)
	out := make([][]string, n)
	e := make([]float64, d)
	for i := range out {
		for j := range e {
			e[j] = r.NormFloat64()
		}
		row := make([]string, d)
		for j := range d {
			z := 0.0
			for k := 0; k <= j; k++ {
				z += m.chol[j][k] * e[k]
			}
			row[j] = m.cols[j].value(phi(z), i, r)
		}
		out[i] = row
	}
	return out
}

// Header returns the columns' names, in order.
func (m *Model) Header() []string { return m.header }

// learnColumn learns one column from its cells.
func learnColumn(name string, cells []string, hint schema.Kind) column {
	c := column{name: name}
	var filled []string
	for _, v := range cells {
		if v != "" {
			filled = append(filled, v)
		}
	}
	c.empty = 1 - float64(len(filled))/float64(len(cells))
	if len(filled) == 0 {
		c.kind = choice
		return c
	}
	distinct := map[string]int{}
	for _, v := range filled {
		distinct[v]++
	}
	asNumbers := hint == "" || hint == schema.Number
	if nums, whole, places, ok := numbers(filled); ok && asNumbers && (hint == schema.Number || len(distinct) > 2) {
		c.kind, c.sorted, c.whole, c.places = numeric, nums, whole, places
		// A number in every row that is never the same twice, written as
		// a whole number, may still be an ID; its name tells.
		if whole && len(distinct) == len(filled) && idName(name) {
			c.kind, c.prefix = fresh, ""
		}
		return c
	}
	if hint != schema.Category && hint != schema.YesNo {
		if layout, secs, ok := dates(filled); ok {
			c.kind, c.layout, c.sorted = date, layout, secs
			return c
		}
	}
	// A different value in every row that names something, like a
	// customer, is made new: copying real ones would give away who they
	// are, and the model can't learn from it anyway.
	if hint != schema.Text && len(distinct) == len(filled) && len(filled) >= MinRows && (idName(name) || hint == "" || hint == schema.Key) {
		c.kind, c.prefix = fresh, prefixOf(filled[0])
		return c
	}
	c.kind = choice
	c.values = make([]string, 0, len(distinct))
	for v := range distinct {
		c.values = append(c.values, v)
	}
	// Most common first, so the shares are laid out the same way each
	// time.
	slices.SortFunc(c.values, func(a, b string) int {
		if n := cmp.Compare(distinct[b], distinct[a]); n != 0 {
			return n
		}
		return cmp.Compare(a, b)
	})
	c.ends = make([]float64, len(c.values))
	sum := 0
	for i, v := range c.values {
		sum += distinct[v]
		c.ends[i] = float64(sum) / float64(len(filled))
	}
	c.ends[len(c.ends)-1] = 1
	return c
}

// normal places a cell in normal space: where it falls among the
// column's values, as a standard normal score. An empty cell is the
// middle.
func (c *column) normal(v string, r *rand.Rand) float64 {
	if v == "" {
		return 0
	}
	switch c.kind {
	case numeric, date:
		var x float64
		var ok bool
		if c.kind == numeric {
			x, ok = schema.ParseNumber(v)
		} else {
			var t time.Time
			t, ok = parseIn(c.layout, v)
			x = float64(t.Unix())
		}
		if !ok || len(c.sorted) == 0 {
			return 0
		}
		// The share of values below, with ties spread evenly.
		lo, _ := slices.BinarySearch(c.sorted, x)
		hi := lo
		for hi < len(c.sorted) && c.sorted[hi] == x {
			hi++
		}
		u := (float64(lo) + r.Float64()*float64(max(hi-lo, 1))) / float64(len(c.sorted))
		return phiInv(clamp(u))
	case choice:
		i := slices.Index(c.values, v)
		if i < 0 {
			return 0
		}
		start := 0.0
		if i > 0 {
			start = c.ends[i-1]
		}
		return phiInv(clamp(start + r.Float64()*(c.ends[i]-start)))
	}
	return r.NormFloat64() // fresh: unrelated to the others
}

// value writes the cell at u, a share from 0 to 1, for the n-th made-up
// row.
func (c *column) value(u float64, n int, r *rand.Rand) string {
	if c.empty > 0 && r.Float64() < c.empty {
		return ""
	}
	switch c.kind {
	case numeric:
		return c.number(quantile(c.sorted, u))
	case date:
		t := time.Unix(int64(math.Round(quantile(c.sorted, u))), 0).UTC()
		return t.Format(c.layout)
	case choice:
		if len(c.values) == 0 {
			return ""
		}
		i, _ := slices.BinarySearch(c.ends, u)
		return c.values[min(i, len(c.values)-1)]
	}
	return fmt.Sprintf("%sS%06d", c.prefix, n+1)
}

// number writes x as the column writes its numbers.
func (c *column) number(x float64) string {
	if c.whole {
		return strconv.FormatInt(int64(math.Round(x)), 10)
	}
	return strconv.FormatFloat(x, 'f', c.places, 64)
}

// quantile is the value a share u of the sorted values lies below,
// between the real values.
func quantile(sorted []float64, u float64) float64 {
	if len(sorted) == 0 {
		return 0
	}
	p := u * float64(len(sorted)-1)
	i := int(p)
	if i >= len(sorted)-1 {
		return sorted[len(sorted)-1]
	}
	f := p - float64(i)
	return sorted[i] + f*(sorted[i+1]-sorted[i])
}

// numbers reads every filled cell as a number: the values sorted,
// whether all are whole, and the most decimal places written.
func numbers(filled []string) (sorted []float64, whole bool, places int, ok bool) {
	sorted = make([]float64, len(filled))
	whole = true
	for i, v := range filled {
		x, ok := schema.ParseNumber(v)
		if !ok {
			return nil, false, 0, false
		}
		sorted[i] = x
		if x != math.Trunc(x) {
			whole = false
		}
		if _, frac, found := strings.Cut(v, "."); found {
			places = max(places, min(len(strings.TrimRight(frac, "0")), 6))
		}
	}
	if !whole && places == 0 {
		places = 2
	}
	slices.Sort(sorted)
	return sorted, whole, places, true
}

// dateLayouts are the ways a date or time cell may be written.
var dateLayouts = []string{
	time.RFC3339, "2006-01-02T15:04:05", "2006-01-02T15:04", "2006-01-02 15:04:05", "2006-01-02 15:04",
	"2006-01-02", "2006/01/02", "02/01/2006", "01/02/2006", "02.01.2006", "2006-01", "2 Jan 2006", "Jan 2006",
}

// dates finds the one layout every filled cell is written in, and the
// times, sorted, in seconds.
func dates(filled []string) (layout string, secs []float64, ok bool) {
	for _, l := range dateLayouts {
		secs = secs[:0]
		good := true
		for _, v := range filled {
			t, err := time.Parse(l, v)
			if err != nil {
				good = false
				break
			}
			secs = append(secs, float64(t.Unix()))
		}
		if good {
			slices.Sort(secs)
			return l, secs, true
		}
	}
	return "", nil, false
}

func parseIn(layout, v string) (time.Time, bool) {
	t, err := time.Parse(layout, v)
	return t, err == nil
}

// idName reports whether a column's name says it names one thing per row.
func idName(name string) bool {
	n := strings.TrimSpace(name)
	if strings.HasSuffix(n, "Id") || strings.HasSuffix(n, "ID") {
		return true
	}
	n = strings.ToLower(n)
	for _, w := range []string{"uuid", "email", "e-mail", "phone", "telp"} {
		if strings.Contains(n, w) {
			return true
		}
	}
	return n == "id" || n == "nik" || strings.HasPrefix(n, "id_") || strings.HasPrefix(n, "id ") ||
		strings.HasSuffix(n, "_id") || strings.HasSuffix(n, " id") || strings.HasSuffix(n, "-id")
}

// prefixOf keeps the letters an ID starts with, like "CUST-" of
// "CUST-0042", so made-up ones look the same.
func prefixOf(v string) string {
	i := strings.IndexFunc(v, func(r rune) bool { return r >= '0' && r <= '9' })
	if i <= 0 || i > 8 {
		return "made-"
	}
	return v[:i]
}

// correlation is the Pearson correlation of each pair of rows of z.
func correlation(z [][]float64) [][]float64 {
	d := len(z)
	sd := make([]float64, d)
	for j, col := range z {
		m := 0.0
		for _, v := range col {
			m += v
		}
		m /= float64(max(len(col), 1))
		s := 0.0
		for i := range col {
			col[i] -= m
			s += col[i] * col[i]
		}
		sd[j] = math.Sqrt(s)
	}
	c := make([][]float64, d)
	for a := range d {
		c[a] = make([]float64, d)
		c[a][a] = 1
		for b := range a {
			v := 0.0
			if sd[a] > 0 && sd[b] > 0 {
				for i := range z[a] {
					v += z[a][i] * z[b][i]
				}
				v /= sd[a] * sd[b]
			}
			c[a][b], c[b][a] = v, v
		}
	}
	return c
}

// cholesky returns the lower factor L of c = L·Lᵀ, pulling c towards no
// correlation at all until it has one.
func cholesky(c [][]float64) [][]float64 {
	for shrink := 1e-6; ; shrink *= 10 {
		l, ok := tryCholesky(c, min(shrink, 1))
		if ok || shrink >= 1 {
			return l
		}
	}
}

func tryCholesky(c [][]float64, shrink float64) ([][]float64, bool) {
	d := len(c)
	l := make([][]float64, d)
	for i := range d {
		l[i] = make([]float64, d)
		for j := 0; j <= i; j++ {
			v := c[i][j] * (1 - shrink)
			if i == j {
				v = 1
			}
			for k := range j {
				v -= l[i][k] * l[j][k]
			}
			if i == j {
				if v <= 1e-12 {
					return nil, false
				}
				l[i][i] = math.Sqrt(v)
			} else {
				l[i][j] = v / l[j][j]
			}
		}
	}
	return l, true
}

// phi is the standard normal's cumulative share.
func phi(z float64) float64 { return 0.5 * math.Erfc(-z/math.Sqrt2) }

// clamp keeps a share off 0 and 1, where phiInv is infinite.
func clamp(u float64) float64 { return min(max(u, 1e-9), 1-1e-9) }

// phiInv is phi's inverse, by Acklam's approximation refined with one
// Halley step.
func phiInv(p float64) float64 {
	const (
		a1, a2, a3, a4, a5, a6 = -3.969683028665376e+01, 2.209460984245205e+02, -2.759285104469687e+02, 1.383577518672690e+02, -3.066479806614716e+01, 2.506628277459239e+00
		b1, b2, b3, b4, b5     = -5.447609879822406e+01, 1.615858368580409e+02, -1.556989798598866e+02, 6.680131188771972e+01, -1.328068155288572e+01
		c1, c2, c3, c4, c5, c6 = -7.784894002430293e-03, -3.223964580411365e-01, -2.400758277161838e+00, -2.549732539343734e+00, 4.374664141464968e+00, 2.938163982698783e+00
		d1, d2, d3, d4         = 7.784695709041462e-03, 3.224671290700398e-01, 2.445134137142996e+00, 3.754408661907416e+00
		low                    = 0.02425
	)
	var x float64
	switch {
	case p < low:
		q := math.Sqrt(-2 * math.Log(p))
		x = (((((c1*q+c2)*q+c3)*q+c4)*q+c5)*q + c6) / ((((d1*q+d2)*q+d3)*q+d4)*q + 1)
	case p <= 1-low:
		q := p - 0.5
		r := q * q
		x = (((((a1*r+a2)*r+a3)*r+a4)*r+a5)*r + a6) * q / (((((b1*r+b2)*r+b3)*r+b4)*r+b5)*r + 1)
	default:
		q := math.Sqrt(-2 * math.Log(1-p))
		x = -(((((c1*q+c2)*q+c3)*q+c4)*q+c5)*q + c6) / ((((d1*q+d2)*q+d3)*q+d4)*q + 1)
	}
	e := phi(x) - p
	u := e * math.Sqrt(2*math.Pi) * math.Exp(x*x/2)
	return x - u/(1+x*u/2)
}
