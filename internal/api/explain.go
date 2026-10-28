package api

import (
	"bytes"
	"cmp"
	"context"
	"encoding/csv"
	"errors"
	"fmt"
	"hash/fnv"
	"maps"
	"math"
	"math/bits"
	"math/rand/v2"
	"slices"
	"strconv"
	"sync"

	"connectrpc.com/connect"

	mimirv1 "github.com/gsoultan/mimir-server/gen/mimir/v1"
	"github.com/gsoultan/mimir-server/internal/arch"
	"github.com/gsoultan/mimir-server/internal/auth"
	"github.com/gsoultan/mimir-server/internal/schema"
	"github.com/gsoultan/mimir-server/internal/store"
)

// Explaining an answer. Each column's effect is its Shapley value: the
// average change it makes to the answer when it is added to the typical
// row, over every order the columns could be added in. With few columns
// that differ from the typical row, every mix of them is asked; with
// more, a sample of orders is. The effects then add up to the answer
// less the typical row's.

const (
	// exactColumns is how many differing columns are explained exactly:
	// 2^10 rows to ask.
	exactColumns = 10
	// explainBudget bounds the rows asked for one estimated explanation;
	// overviewBudget for each row of a model's overview.
	explainBudget  = 512
	overviewBudget = 96
	// overviewRows is how many rows a model's overview averages.
	overviewRows = 24
	// explainAsk is how many rows are asked at once.
	explainAsk = 16
)

// explainer asks one model about mixes of a row and the typical row.
type explainer struct {
	s       *Service
	m       store.Model
	a       arch.Architecture
	spec    arch.Spec
	cols    []schema.Column
	typical map[string]string
}

// newExplainer checks that model m's answers can be explained: it reads
// table rows with named columns and picks a label or answers with a
// number.
func (s *Service) newExplainer(m store.Model) (*explainer, error) {
	a, ok := arch.Lookup(m.Architecture)
	switch {
	case !ok:
		return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("This model's type (%s) isn't available on this server anymore.", m.Architecture))
	case a.Input != arch.Table || a.Fixed() || (a.Answers() != arch.Class && a.Answers() != arch.Number):
		return nil, invalid("%s doesn't read rows of a table and answer with a label or a number, so there's no column to explain its answers by.", m.Name)
	case len(m.Columns) == 0:
		return nil, invalid("%s was taught before the server kept its columns' kinds; teach it again to explain its answers.", m.Name)
	}
	e := &explainer{s: s, m: m, a: a, spec: arch.Resolve(a, arch.Spec{InputShape: m.InputShape, Labels: m.Labels}), typical: map[string]string{}}
	for _, c := range m.Columns {
		if c.Kind == schema.Photo {
			continue
		}
		e.cols = append(e.cols, c)
		e.typical[c.Name] = typicalCell(c)
	}
	return e, nil
}

// typicalCell is a column's cell in the typical row: a number column's
// average, a category's most common value, and empty for the rest, which
// reads as "no" or no text.
func typicalCell(c schema.Column) string {
	switch c.Kind {
	case schema.Number:
		return strconv.FormatFloat(c.Fill, 'g', 4, 64)
	case schema.Category:
		if len(c.Values) > 0 {
			return c.Values[0]
		}
	}
	return ""
}

// shownTypical is a typical cell as people read it.
func shownTypical(c schema.Column, cell string) string {
	switch {
	case c.Kind == schema.YesNo:
		return "no"
	case cell == "":
		return "empty"
	}
	return cell
}

// cellDiffers reports whether a row's cell for column c reads differently
// from the typical row's.
func cellDiffers(c schema.Column, cell, typical string) bool {
	one := []schema.Column{c}
	a := schema.EncodeRow(nil, one, []string{cell}, nil)
	b := schema.EncodeRow(nil, one, []string{typical}, nil)
	return !slices.Equal(a, b)
}

// value asks the model about a row and returns the answer explained:
// the chance of label k, or the number.
func (e *explainer) value(ctx context.Context, fields map[string]string, k int) (float64, []float32, error) {
	input, err := encode(e.m, e.a, fields, nil, nil)
	if err != nil {
		return 0, nil, err
	}
	load := func() ([]byte, error) { return e.s.store.Weights(e.m.ID) }
	r, err := e.s.answer(ctx, e.m, e.a, e.spec, load, input)
	if err != nil {
		return 0, nil, err
	}
	if e.a.Answers() == arch.Number {
		if len(r.Output) == 0 {
			return 0, nil, errors.New("the model gave no number")
		}
		return float64(r.Output[0]), nil, nil
	}
	if k < 0 || k >= len(r.Probability) {
		return 0, r.Probability, nil
	}
	return float64(r.Probability[k]), r.Probability, nil
}

// explained is one row's explanation.
type explained struct {
	label   string
	value   float64
	typical float64
	effects map[string]float64 // by column, for those that differ
	exact   bool
}

// explain works out each column's effect on the model's answer to row
// fields: on the chance of label (its first guess when empty), or on its
// number. budget bounds how many rows an estimate asks.
func (e *explainer) explain(ctx context.Context, fields map[string]string, label string, budget int) (explained, error) {
	row := map[string]string{}
	for _, c := range e.cols {
		row[c.Name] = fields[c.Name]
	}
	k := -1
	if e.a.Answers() == arch.Class {
		_, probs, err := e.value(ctx, row, -1)
		if err != nil {
			return explained{}, err
		}
		if label == "" {
			k = argmax(probs)
		} else if k = slices.Index(e.spec.Labels, label); k < 0 {
			return explained{}, invalid("%s doesn't answer %q; it answers %s.", e.m.Name, label, joinFew(e.spec.Labels, 8))
		}
		if k < len(e.spec.Labels) {
			label = e.spec.Labels[k]
		} else {
			label = strconv.Itoa(k)
		}
	}
	var players []schema.Column
	for _, c := range e.cols {
		if cellDiffers(c, row[c.Name], e.typical[c.Name]) {
			players = append(players, c)
		}
	}
	n := len(players)
	if n > 60 {
		return explained{}, invalid("This row differs from the typical one in %d columns; at most 60 can be explained.", n)
	}
	// mix is the row with the players in mask from it and the rest from
	// the typical row.
	mix := func(mask uint64) map[string]string {
		out := maps.Clone(e.typical)
		for i, c := range players {
			if mask&(1<<i) != 0 {
				out[c.Name] = row[c.Name]
			}
		}
		return out
	}
	exact := n <= exactColumns
	var orders [][]int
	masks := map[uint64]bool{0: true, 1<<n - 1: true}
	if exact {
		for m := range uint64(1) << n {
			masks[m] = true
		}
	} else {
		// Orders and their reverses, from a seed the row sets, so the
		// same row gets the same explanation.
		h := fnv.New64a()
		for _, c := range players {
			fmt.Fprintf(h, "%s=%s\x00", c.Name, row[c.Name])
		}
		rng := rand.New(rand.NewPCG(h.Sum64(), uint64(n)))
		for len(orders) < max(2, 2*(budget/(2*n))) {
			o := rng.Perm(n)
			orders = append(orders, o, reversed(o))
		}
		for _, o := range orders {
			var m uint64
			for _, p := range o {
				m |= 1 << p
				masks[m] = true
			}
		}
	}
	values, err := e.values(ctx, slices.Collect(maps.Keys(masks)), mix, k)
	if err != nil {
		return explained{}, err
	}
	out := explained{label: label, value: values[1<<n-1], typical: values[0], effects: map[string]float64{}, exact: exact}
	phi := make([]float64, n)
	if exact {
		// φ_j = Σ over masks S without j of |S|!(n-|S|-1)!/n! (f(S+j) - f(S)).
		weights := make([]float64, n)
		for s := range n {
			weights[s] = math.Exp(lgamma(s+1) + lgamma(n-s) - lgamma(n+1))
		}
		for m, v := range values {
			for j := range n {
				if m&(1<<j) == 0 {
					phi[j] += weights[bits.OnesCount64(m)] * (values[m|1<<j] - v)
				}
			}
		}
	} else {
		for _, o := range orders {
			var m uint64
			for _, p := range o {
				next := m | 1<<p
				phi[p] += values[next] - values[m]
				m = next
			}
		}
		for j := range phi {
			phi[j] /= float64(len(orders))
		}
	}
	for j, c := range players {
		out.effects[c.Name] = phi[j]
	}
	return out, nil
}

// values asks the model about each mask's mix, explainAsk at a time.
func (e *explainer) values(ctx context.Context, masks []uint64, mix func(uint64) map[string]string, k int) (map[uint64]float64, error) {
	out := make(map[uint64]float64, len(masks))
	var mu sync.Mutex
	var first error
	work := make(chan uint64)
	var wg sync.WaitGroup
	for range min(explainAsk, len(masks)) {
		wg.Go(func() {
			for m := range work {
				v, _, err := e.value(ctx, mix(m), k)
				mu.Lock()
				if err != nil && first == nil {
					first = err
				}
				out[m] = v
				mu.Unlock()
			}
		})
	}
	for _, m := range masks {
		mu.Lock()
		failed := first != nil
		mu.Unlock()
		if failed || ctx.Err() != nil {
			break
		}
		work <- m
	}
	close(work)
	wg.Wait()
	if first == nil && ctx.Err() != nil {
		first = ctx.Err()
	}
	return out, first
}

func (s *Service) Explain(ctx context.Context, req *connect.Request[mimirv1.ExplainRequest]) (*connect.Response[mimirv1.ExplainResponse], error) {
	msg := req.Msg
	m, err := s.get(ctx, msg.GetModelId())
	if err != nil {
		return nil, err
	}
	e, err := s.newExplainer(m)
	if err != nil {
		return nil, err
	}
	if p, ok := auth.From(ctx); ok {
		if err := s.admit(ctx, p); err != nil {
			return nil, err
		}
	}
	x, err := e.explain(ctx, msg.GetFields(), msg.GetLabel(), explainBudget)
	if err != nil {
		return nil, s.explainError(ctx, err)
	}
	res := &mimirv1.ExplainResponse{Label: x.label, Number: e.a.Answers() == arch.Number, Value: x.value, Typical: x.typical, Exact: x.exact}
	for _, c := range e.cols {
		effect, ok := x.effects[c.Name]
		if !ok {
			continue
		}
		res.Contributions = append(res.Contributions, &mimirv1.Contribution{
			Column: c.Name, Value: msg.GetFields()[c.Name], Typical: shownTypical(c, e.typical[c.Name]), Effect: effect,
		})
	}
	slices.SortStableFunc(res.Contributions, func(a, b *mimirv1.Contribution) int {
		return cmp.Compare(math.Abs(b.Effect), math.Abs(a.Effect))
	})
	return connect.NewResponse(res), nil
}

func (s *Service) ExplainModel(ctx context.Context, req *connect.Request[mimirv1.ExplainModelRequest]) (*connect.Response[mimirv1.ExplainModelResponse], error) {
	m, err := s.get(ctx, req.Msg.GetModelId())
	if err != nil {
		return nil, err
	}
	e, err := s.newExplainer(m)
	if err != nil {
		return nil, err
	}
	if p, ok := auth.From(ctx); ok {
		if err := s.admit(ctx, p); err != nil {
			return nil, err
		}
	}
	rows, source, err := s.overviewRows(ctx, m)
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("There are no rows to work it out from yet: ask %s a few questions first.", m.Name))
	}
	total := map[string]float64{}
	for _, row := range rows {
		x, err := e.explain(ctx, row, "", overviewBudget)
		if err != nil {
			return nil, s.explainError(ctx, err)
		}
		for col, effect := range x.effects {
			total[col] += math.Abs(effect)
		}
	}
	res := &mimirv1.ExplainModelResponse{Rows: int32(len(rows)), Source: source, Number: e.a.Answers() == arch.Number}
	var sum float64
	for _, c := range e.cols {
		imp := total[c.Name] / float64(len(rows))
		sum += imp
		res.Columns = append(res.Columns, &mimirv1.ColumnImportance{Column: c.Name, Importance: imp})
	}
	for _, c := range res.Columns {
		if sum > 0 {
			c.Share = c.Importance / sum
		}
	}
	slices.SortStableFunc(res.Columns, func(a, b *mimirv1.ColumnImportance) int { return cmp.Compare(b.Importance, a.Importance) })
	return connect.NewResponse(res), nil
}

// overviewRows are the rows a model's overview averages: the questions it
// was asked lately, or before any, the first examples it learned from.
func (s *Service) overviewRows(ctx context.Context, m store.Model) ([]map[string]string, string, error) {
	answers, err := s.store.ListAnswers(ctx, m.ID, 200)
	if err != nil {
		return nil, "", s.internal(ctx, err)
	}
	var rows []map[string]string
	for _, a := range answers {
		if len(a.Fields) > 0 {
			rows = append(rows, a.Fields)
			if len(rows) == overviewRows {
				break
			}
		}
	}
	if len(rows) > 0 {
		return rows, "answers", nil
	}
	if m.JobID == "" {
		return nil, "", nil
	}
	j, err := s.store.GetJob(ctx, m.JobID)
	if err != nil || j.DatasetID == "" {
		return nil, "", nil
	}
	data, err := s.store.ReadDataset(j.DatasetID)
	if err != nil {
		return nil, "", nil
	}
	r := csv.NewReader(bytes.NewReader(data))
	r.FieldsPerRecord, r.LazyQuotes = -1, true
	header, err := r.Read()
	if err != nil {
		return nil, "", nil
	}
	for i := range header {
		header[i] = cleanHeader(header[i])
	}
	for len(rows) < overviewRows {
		rec, err := r.Read()
		if err != nil {
			break
		}
		row := map[string]string{}
		for i, h := range header {
			if i < len(rec) {
				row[h] = rec[i]
			}
		}
		rows = append(rows, row)
	}
	return rows, "examples", nil
}

func (s *Service) explainError(ctx context.Context, err error) error {
	var ce *connect.Error
	switch {
	case errors.As(err, &ce):
		return err
	case ctx.Err() != nil:
		return ctx.Err()
	case tooBig(err):
		return tooBigError(err)
	}
	return connect.NewError(connect.CodeUnavailable, fmt.Errorf("The model couldn't answer: %v", err))
}

func argmax(p []float32) int {
	best := 0
	for i, v := range p {
		if v > p[best] {
			best = i
		}
	}
	return best
}

func reversed(o []int) []int {
	r := slices.Clone(o)
	slices.Reverse(r)
	return r
}

func lgamma(n int) float64 {
	v, _ := math.Lgamma(float64(n))
	return v
}

func joinFew(xs []string, n int) string {
	if len(xs) > n {
		xs = append(xs[:n:n], "…")
	}
	return fmt.Sprintf("%q", xs)
}
