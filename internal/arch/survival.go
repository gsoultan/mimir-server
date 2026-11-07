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

package arch

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"math/rand/v2"
	"regexp"
	"slices"
	"strings"

	"github.com/gsoultan/mimir/nn"
	"github.com/gsoultan/mimir/tabular"
	"github.com/gsoultan/mimir/tensor"
	"github.com/gsoultan/mimir/tree"

	"github.com/gsoultan/mimir-server/internal/schema"
)

// Time until something happens, such as a customer's next order or its
// leaving, learned also from the rows where it hasn't happened yet: a
// discrete-time survival model. The time axis is cut into bins; boosted
// trees learn the chance it happens in each bin given that it hadn't
// before, and those chances multiply into the chance it happens within
// any number of days.

// watchedWords name the column that says how long each row was watched,
// and durationWords, tried after them, a column that may.
var (
	watchedWords  = regexp.MustCompile(`watched|observed|duration|time_to|time to|tenure|elapsed|followed`)
	durationWords = regexp.MustCompile(`days|weeks|months|age`)
)

const (
	// WithinColumn names the question's time, in the duration column's
	// unit.
	WithinColumn = "within_days"
	// survivalBins bounds the bins the time axis is cut into.
	survivalBins = 20
	// survivalRows bounds the rows learned from; more are sampled down,
	// as each becomes up to survivalBins rows.
	survivalRows = 30_000
)

// SurvivalShape sizes a Survival.
type SurvivalShape struct {
	Tree     tree.Shape `json:"tree"`
	Features int        `json:"features"` // the row's numbers, without the time asked
	// Edges are the bins' bounds, from 0 to the longest time seen.
	Edges []float64 `json:"edges"`
}

// Survival answers how likely the event is within a time. Forward maps x,
// [batch, Features+1] (the row, then the time), to [batch, 2]: the log
// chance it happens within the time, then the log chance it doesn't, so
// their softmax is the two chances.
type Survival struct {
	Trees *tree.Module
	shape SurvivalShape
}

// Forward multiplies each bin's chance of not happening, up to the time
// asked; a bin the time ends inside counts in proportion.
func (m *Survival) Forward(x *tensor.Tensor) *tensor.Tensor {
	if err := x.Err(); err != nil {
		return x
	}
	s := m.shape
	if x.Rank() != 2 || x.Shape()[1] != s.Features+1 {
		return tensor.Failed(fmt.Errorf("survival: input %v, want [batch, %d]: %w", x.Shape(), s.Features+1, tensor.ErrShape))
	}
	n := x.Shape()[0]
	f := func(v float64) *tensor.Tensor { return tensor.Scalar[float32](float32(v)) }
	within := x.Shrink([][2]int{{0, n}, {s.Features, s.Features + 1}})
	within = tensor.Where(within.Equal(within), within, f(0))
	row := x.Shrink([][2]int{{0, n}, {0, s.Features}})
	logLive := tensor.Zeros(tensor.Float32, n, 1)
	for k := range len(s.Edges) - 1 {
		lo, hi := s.Edges[k], s.Edges[k+1]
		at := tensor.Full(tensor.Float32, lo, n, 1)
		in := at
		if s.Features > 0 {
			in = tensor.Cat(1, row, at)
		}
		h := m.Trees.Forward(in).Maximum(f(0)).Minimum(f(0.999))
		part := within.Sub(f(lo)).Div(f(hi - lo)).Maximum(f(0)).Minimum(f(1))
		logLive = logLive.Add(f(1).Sub(h.Mul(part)).Log())
	}
	live := logLive.Exp()
	happen := f(1).Sub(live)
	return tensor.Cat(1, happen.Add(f(1e-6)).Log(), live.Add(f(1e-6)).Log())
}

// survivalModel is the learned trees and bins, before it is a module.
type survivalModel struct {
	trees *tree.Model
	edges []float64
	width int
}

// within returns the chance a row of numbers x sees the event by t.
func (m *survivalModel) within(x []float32, t float64) float64 {
	live := 1.0
	in := make([]float32, 0, m.width+1)
	for k := range len(m.edges) - 1 {
		lo, hi := m.edges[k], m.edges[k+1]
		if t <= lo {
			break
		}
		in = append(append(in[:0], x...), float32(lo))
		h := min(max(float64(m.trees.Predict(in)[0]), 0), 0.999)
		live *= 1 - h*min((t-lo)/(hi-lo), 1)
	}
	return 1 - live
}

// expand makes one row per bin a row was watched in: its numbers and the
// bin's start, and 1 if the event happened in that bin. A bin it left
// part way through counts when it was watched for at least half of it.
func expand(x []float32, width int, dur []float64, event []bool, rows []int, edges []float64) tabular.Data {
	d := tabular.Data{Features: width + 1}
	for _, i := range rows {
		for k := range len(edges) - 1 {
			lo, hi := edges[k], edges[k+1]
			if dur[i] <= lo {
				break
			}
			happened := event[i] && dur[i] <= hi
			if !happened && dur[i] < hi && dur[i]-lo < (hi-lo)/2 {
				break
			}
			d.X = append(d.X, x[i*width:(i+1)*width]...)
			d.X = append(d.X, float32(lo))
			y := float32(0)
			if happened {
				y = 1
			}
			d.Y = append(d.Y, y)
			if happened {
				break
			}
		}
	}
	return d
}

// concordance is the share of pairs of held rows whose order of events the
// risks get right: 0.5 is guessing and 1 is always right.
func concordance(risk, dur []float64, event []bool, r *rand.Rand) float64 {
	var right, pairs float64
	n := len(risk)
	for range min(200_000, n*n) {
		i, j := r.IntN(n), r.IntN(n)
		if !event[i] || dur[i] >= dur[j] {
			continue // only a pair whose first event is seen can be ordered
		}
		pairs++
		switch {
		case risk[i] > risk[j]:
			right++
		case risk[i] == risk[j]:
			right += 0.5
		}
	}
	if pairs == 0 {
		return 0
	}
	return right / pairs
}

// survival learns the time until the yes of a yes-or-no answer column
// from a column of how long each row was watched: until it happened when
// the answer is yes, or until now when it is no.
var survival = &Learner{
	Cells: func(ctx context.Context, c Cells, progress func(int, int)) (Learned, error) {
		ev := slices.Index(c.Header, c.Target)
		if ev < 0 {
			return Learned{}, cellsError("there is no column called %q", c.Target)
		}
		events := c.column(ev)
		if !yesNo(events) {
			return Learned{}, cellsError("the %q column needs yes or no: yes when it happened, no when it hasn't yet", c.Target)
		}
		dur := -1
		for _, words := range []*regexp.Regexp{watchedWords, durationWords} {
			for j, h := range c.Header {
				if dur < 0 && j != ev && words.MatchString(strings.ToLower(h)) && numbers(c.column(j)) {
					dur = j
				}
			}
		}
		if dur < 0 {
			return Learned{}, cellsError("no column says how long each row was watched; name one like days_watched, in days: until it happened, or until now if it hasn't")
		}
		var keep [][]string
		var times []float64
		var happened []bool
		for _, r := range c.Rows {
			t, ok := schema.ParseNumber(r[dur])
			v := strings.ToLower(strings.TrimSpace(r[ev]))
			if !ok || t <= 0 || v == "" {
				continue
			}
			keep = append(keep, r)
			times = append(times, t)
			happened = append(happened, v == "yes" || v == "y" || v == "true" || v == "t")
		}
		rng := rand.New(rand.NewPCG(19, 23))
		if len(keep) > survivalRows {
			idx := rng.Perm(len(keep))[:survivalRows]
			slices.Sort(idx)
			k2, t2, h2 := make([][]string, 0, len(idx)), make([]float64, 0, len(idx)), make([]bool, 0, len(idx))
			for _, i := range idx {
				k2, t2, h2 = append(k2, keep[i]), append(t2, times[i]), append(h2, happened[i])
			}
			keep, times, happened = k2, t2, h2
		}
		var seen []float64
		for i, t := range times {
			if happened[i] {
				seen = append(seen, t)
			}
		}
		if len(seen) < 20 || len(keep)-len(seen) < 1 && len(seen) < 50 {
			return Learned{}, cellsError("only %d rows say it happened; at least 20 are needed", len(seen))
		}
		// Bins hold about as many events each.
		slices.Sort(seen)
		edges := []float64{0}
		for k := 1; k < survivalBins; k++ {
			e := seen[k*len(seen)/survivalBins]
			if e > edges[len(edges)-1] {
				edges = append(edges, e)
			}
		}
		longest := slices.Max(times)
		if longest > edges[len(edges)-1] {
			edges = append(edges, longest)
		}
		if len(edges) < 2 {
			return Learned{}, cellsError("every row has the same time, so there is nothing to learn about when")
		}

		var situation []int
		for j := range c.Header {
			if j != ev && j != dur {
				situation = append(situation, j)
			}
		}
		sub := Cells{Header: c.Header, Rows: keep, Kinds: c.Kinds}
		var cols []schema.Column
		var x []float32
		if len(situation) > 0 {
			cols, x = sub.encode(situation)
		}
		width := schema.Width(cols)
		order := rng.Perm(len(keep))
		cut := len(order) / 5
		heldRows, trainRows := order[:cut], order[cut:]
		train := expand(x, width, times, happened, trainRows, edges)
		held := expand(x, width, times, happened, heldRows, edges)
		opts := []tree.Option{tree.Trees(boostTrees), tree.LearningRate(0.05), tree.MinLeaf(20)}
		if held.Rows() > 0 {
			opts = append(opts, tree.Validation(held, 50))
		}
		if progress != nil {
			opts = append(opts, tree.Progress(progress))
		}
		trees, err := tree.GradientBoosting(ctx, train, tabular.Regression, opts...)
		if err != nil {
			return Learned{}, err
		}
		m := &survivalModel{trees: trees, edges: edges, width: width}
		median := seen[len(seen)/2]
		risk := make([]float64, len(heldRows))
		hd, he := make([]float64, len(heldRows)), make([]bool, len(heldRows))
		for k, i := range heldRows {
			risk[k] = m.within(x[i*width:(i+1)*width], median)
			hd[k], he[k] = times[i], happened[i]
		}
		score := concordance(risk, hd, he, rng)

		shape := SurvivalShape{Tree: trees.Shape(), Features: width, Edges: edges}
		cols = append(cols, schema.Column{Name: WithinColumn, Kind: schema.Number, Fill: math.Round(median)})
		s, err := shapeJSON(shape)
		yes, no := "yes", "no"
		for _, v := range events {
			switch strings.ToLower(v) {
			case "yes", "y", "true", "t":
				yes = v
			case "no", "n", "false", "f":
				no = v
			}
		}
		return Learned{Module: &Survival{Trees: trees.Module(), shape: shape}, Shape: s, Columns: cols, Labels: []string{yes, no}, Accuracy: score}, err
	},
	Build: func(shape string) (nn.Module, error) {
		var s SurvivalShape
		if err := json.Unmarshal([]byte(shape), &s); err != nil {
			return nil, fmt.Errorf("a survival model's shape: %w", err)
		}
		return &Survival{Trees: tree.NewModule(s.Tree), shape: s}, nil
	},
}
