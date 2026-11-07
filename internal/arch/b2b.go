package arch

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"math"
	"math/rand/v2"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/gsoultan/mimir/nn"
	"github.com/gsoultan/mimir/tabular"
	"github.com/gsoultan/mimir/tensor"
	"github.com/gsoultan/mimir/tree"

	"github.com/gsoultan/mimir-server/internal/schema"
)

// Model types for questions sales teams ask every week: which leads look
// like our best customers, which product lines a customer will buy, how
// a discount changes the chance of closing, how wide an estimate is, and
// which accounts are the same business twice.

// isYes reports whether v means yes.
func isYes(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "yes", "y", "true", "t", "1":
		return true
	}
	return false
}

// yesNoLabels returns how cells spell yes and no.
func yesNoLabels(cells []string) (yes, no string) {
	yes, no = "yes", "no"
	for _, v := range cells {
		switch strings.ToLower(v) {
		case "yes", "y", "true", "t":
			yes = v
		case "no", "n", "false", "f":
			no = v
		}
	}
	return yes, no
}

// idLike reports whether cells name each row apart, as an id or a name
// does: words, nearly all different. Such a column says nothing a model
// can carry over to a new row.
func idLike(cells []string) bool {
	if numbers(cells) {
		return false
	}
	filled := 0
	for _, v := range cells {
		if v != "" {
			filled++
		}
	}
	return filled >= 20 && float64(distinct(cells)) >= 0.9*float64(filled)
}

// situation returns the columns other than skip that describe a row,
// leaving out ids and names.
func (c Cells) situation(skip ...int) []int {
	var out []int
	for j := range c.Header {
		if !slices.Contains(skip, j) && !idLike(c.column(j)) {
			out = append(out, j)
		}
	}
	return out
}

// auc is the chance a random positive scores above a random negative:
// 0.5 is guessing and 1 is always right.
func auc(score []float64, pos []bool) float64 {
	idx := make([]int, len(score))
	for i := range idx {
		idx[i] = i
	}
	slices.SortFunc(idx, func(a, b int) int { return cmp.Compare(score[a], score[b]) })
	var ranks, np, nn float64
	for i := 0; i < len(idx); {
		j := i
		for j < len(idx) && score[idx[j]] == score[idx[i]] {
			j++
		}
		r := float64(i+j+1) / 2 // the average rank, from 1
		for k := i; k < j; k++ {
			if pos[idx[k]] {
				ranks += r
				np++
			} else {
				nn++
			}
		}
		i = j
	}
	if np == 0 || nn == 0 {
		return 0
	}
	return (ranks - np*(np+1)/2) / (np * nn)
}

func buffer(v []float32, shape ...int) *tensor.Param {
	return tensor.NewBuffer(tensor.FromSlice(v, shape...))
}

func zeros(shape ...int) *tensor.Param { return tensor.NewBuffer(tensor.Zeros(tensor.Float32, shape...)) }

func scalar(v float64) *tensor.Tensor { return tensor.Scalar[float32](float32(v)) }

// boostOn grows boosted trees on d, holding back a part to stop when more
// trees stop helping. The trees are shallow, as yes-or-no outcomes such
// as a deal closing are noisy and deep trees learn the noise first.
func boostOn(ctx context.Context, d tabular.Data, task tabular.Task, trees int, progress func(int, int)) (*tree.Model, error) {
	opts := []tree.Option{tree.Trees(trees), tree.LearningRate(0.05), tree.MinLeaf(20), tree.MaxDepth(4)}
	if task == tabular.Classification {
		opts = append(opts, tree.Classes(2))
	}
	if progress != nil {
		opts = append(opts, tree.Progress(progress))
	}
	train := d
	if d.Rows() >= 50 {
		var val tabular.Data
		train, val = d.Split(holdBack, 7)
		opts = append(opts, tree.Validation(val, 200))
	}
	return tree.GradientBoosting(ctx, train, task, opts...)
}

// rowsOf returns the rows of x at idx, width numbers each.
func rowsOf(x []float32, width int, idx []int) []float32 {
	out := make([]float32, 0, len(idx)*width)
	for _, i := range idx {
		out = append(out, x[i*width:(i+1)*width]...)
	}
	return out
}

// ---- Look-alike ----

// lookAlikeRefs bounds the positive rows a look-alike model keeps.
const lookAlikeRefs = 5000

// LookAlikeShape sizes a LookAlike.
type LookAlikeShape struct {
	Features, Refs, Calib, K int
}

// LookAlike scores how alike a row is to the positive rows it learned
// from, 0 to 100. Forward maps x, [batch, Features], to [batch, 1]: the
// share of positive rows that are farther from their own nearest
// positives than x is from its, times 100. 50 is as alike as a typical
// positive row, and 100 as alike as the closest.
type LookAlike struct {
	Mean, Scale *tensor.Param // Float32 [1, Features]
	Refs        *tensor.Param // Float32 [Refs, Features], scaled
	Calib       *tensor.Param // Float32 [1, Calib], the positives' own distances
	shape       LookAlikeShape
}

func NewLookAlike(s LookAlikeShape) *LookAlike {
	f := max(s.Features, 1)
	return &LookAlike{Mean: zeros(1, f), Scale: zeros(1, f), Refs: zeros(max(s.Refs, 1), f), Calib: zeros(1, max(s.Calib, 1)), shape: s}
}

func (m *LookAlike) Forward(x *tensor.Tensor) *tensor.Tensor {
	if err := x.Err(); err != nil {
		return x
	}
	s := m.shape
	if x.Rank() != 2 || x.Shape()[1] != s.Features {
		return tensor.Failed(fmt.Errorf("look-alike: input %v, want [batch, %d]: %w", x.Shape(), s.Features, tensor.ErrShape))
	}
	z := x.Sub(m.Mean.Value()).Mul(m.Scale.Value())
	z = tensor.Where(z.Equal(z), z, scalar(0))
	refs := m.Refs.Value()
	d2 := z.Mul(z).Sum(1).Reshape(x.Shape()[0], 1).
		Sub(z.MatMul(refs.Transpose(0, 1)).Mul(scalar(2))).
		Add(refs.Mul(refs).Sum(1).Reshape(1, s.Refs)).
		Maximum(scalar(0))
	near, _ := d2.TopK(s.K, 1, false)
	d := near.Mean(1).Reshape(x.Shape()[0], 1)
	farther := scalar(1).Sub(m.Calib.Value().Less(d))
	return farther.Mean(1).Reshape(x.Shape()[0], 1).Mul(scalar(100))
}

// lookAlike learns what sets the yes rows of a yes-or-no column apart,
// such as best customers, and scores any row, such as a lead, by how
// close it is to them.
var lookAlike = &Learner{
	Cells: func(ctx context.Context, c Cells, progress func(int, int)) (Learned, error) {
		ev := slices.Index(c.Header, c.Target)
		if ev < 0 {
			return Learned{}, cellsError("there is no column called %q", c.Target)
		}
		if !yesNo(c.column(ev)) {
			return Learned{}, cellsError("the %q column needs yes or no: yes for your best customers, no for the others", c.Target)
		}
		var keep [][]string
		var pos []bool
		for _, r := range c.Rows {
			if v := strings.TrimSpace(r[ev]); v != "" {
				keep, pos = append(keep, r), append(pos, isYes(v))
			}
		}
		sub := Cells{Header: c.Header, Rows: keep, Kinds: c.Kinds}
		cols, x := sub.encode(sub.situation(ev))
		f := schema.Width(cols)
		if f == 0 {
			return Learned{}, cellsError("no column describes the rows; add some, like industry, size or region")
		}
		n := len(keep)
		var np int
		for _, p := range pos {
			if p {
				np++
			}
		}
		if np < 10 {
			return Learned{}, cellsError("only %d rows say %s; at least 10 best customers are needed", np, "yes")
		}
		// Standardize, and weigh each number by how far apart the yes
		// and no rows are on it.
		mean, std := make([]float64, f), make([]float64, f)
		for i := range n {
			for j := range f {
				if v := float64(x[i*f+j]); !math.IsNaN(v) {
					mean[j] += v / float64(n)
				}
			}
		}
		for i := range n {
			for j := range f {
				if v := float64(x[i*f+j]); !math.IsNaN(v) {
					std[j] += (v - mean[j]) * (v - mean[j]) / float64(n)
				}
			}
		}
		scale := make([]float32, f)
		mp, mn := make([]float64, f), make([]float64, f)
		for i := range n {
			for j := range f {
				v := float64(x[i*f+j])
				if math.IsNaN(v) {
					continue
				}
				if pos[i] {
					mp[j] += v / float64(np)
				} else if n > np {
					mn[j] += v / float64(n-np)
				}
			}
		}
		var total float64
		w := make([]float64, f)
		for j := range f {
			std[j] = math.Sqrt(std[j])
			if std[j] < 1e-9 {
				std[j] = 1
			}
			w[j] = 0.1
			if n > np {
				w[j] += math.Abs(mp[j]-mn[j]) / std[j]
			}
			total += w[j]
		}
		for j := range f {
			scale[j] = float32(math.Sqrt(w[j]*float64(f)/total) / std[j])
		}
		meanF := make([]float32, f)
		for j := range f {
			meanF[j] = float32(mean[j])
		}
		z := func(i int) []float64 {
			out := make([]float64, f)
			for j := range f {
				v := float64(x[i*f+j])
				if !math.IsNaN(v) {
					out[j] = (v - mean[j]) * float64(scale[j])
				}
			}
			return out
		}
		dist := func(a, b []float64) float64 {
			var s float64
			for j := range a {
				d := a[j] - b[j]
				s += d * d
			}
			return s
		}
		rng := rand.New(rand.NewPCG(29, 31))
		// kNear is the mean of the k smallest distances from q to refs,
		// leaving out self.
		kNear := func(q []float64, refs [][]float64, self, k int) float64 {
			ds := make([]float64, 0, len(refs))
			for r, ref := range refs {
				if r != self {
					ds = append(ds, dist(q, ref))
				}
			}
			slices.Sort(ds)
			k = min(k, len(ds))
			var s float64
			for _, d := range ds[:k] {
				s += d
			}
			return s / float64(max(k, 1))
		}
		build := func(rows []int) ([][]float64, []float64, int) {
			var refs [][]float64
			for _, i := range rows {
				if pos[i] {
					refs = append(refs, z(i))
				}
			}
			if len(refs) > lookAlikeRefs {
				rng.Shuffle(len(refs), func(a, b int) { refs[a], refs[b] = refs[b], refs[a] })
				refs = refs[:lookAlikeRefs]
			}
			k := min(5, len(refs)-1)
			calib := make([]float64, 0, min(len(refs), 1000))
			for r := range refs[:min(len(refs), 1000)] {
				calib = append(calib, kNear(refs[r], refs, r, k))
			}
			return refs, calib, k
		}
		score := func(q []float64, refs [][]float64, calib []float64, k int) float64 {
			d := kNear(q, refs, -1, k)
			far := 0
			for _, c := range calib {
				if c >= d {
					far++
				}
			}
			return 100 * float64(far) / float64(len(calib))
		}

		// Score on rows held back from a first fit.
		order := rng.Perm(n)
		cut := n / 5
		var accuracy float64
		if np >= 20 && n-np >= 10 {
			refs, calib, k := build(order[cut:])
			held := order[:min(cut, 2000)]
			ss, ps := make([]float64, len(held)), make([]bool, len(held))
			for h, i := range held {
				ss[h], ps[h] = score(z(i), refs, calib, k), pos[i]
			}
			accuracy = auc(ss, ps)
		}
		if progress != nil {
			progress(1, 2)
		}
		all := make([]int, n)
		for i := range all {
			all[i] = i
		}
		refs, calib, k := build(all)
		shape := LookAlikeShape{Features: f, Refs: len(refs), Calib: len(calib), K: max(k, 1)}
		m := NewLookAlike(shape)
		flat := make([]float32, 0, len(refs)*f)
		for _, r := range refs {
			for _, v := range r {
				flat = append(flat, float32(v))
			}
		}
		cal := make([]float32, len(calib))
		for i, v := range calib {
			cal[i] = float32(v)
		}
		m.Mean, m.Scale = buffer(meanF, 1, f), buffer(scale, 1, f)
		m.Refs, m.Calib = buffer(flat, len(refs), f), buffer(cal, 1, len(cal))
		if progress != nil {
			progress(2, 2)
		}
		s, err := shapeJSON(shape)
		return Learned{Module: m, Shape: s, Columns: cols, Labels: []string{"how alike, 0 to 100"}, Accuracy: accuracy}, err
	},
	Build: func(shape string) (nn.Module, error) {
		var s LookAlikeShape
		if err := json.Unmarshal([]byte(shape), &s); err != nil {
			return nil, fmt.Errorf("a look-alike model's shape: %w", err)
		}
		return NewLookAlike(s), nil
	},
}

// ---- Several answers at once ----

const (
	// maxLabels bounds the answers a multi-label model learns.
	maxLabels = 25
	// minLabelRows is the fewest rows an answer needs to be learned.
	minLabelRows = 10
)

// labelSplit cuts a cell of several answers.
var labelSplit = regexp.MustCompile(`\s*[;,|]\s*`)

// MultiLabelShape sizes a MultiLabel.
type MultiLabelShape struct {
	Trees    []tree.Shape `json:"trees"`
	Features int          `json:"features"`
}

// MultiLabel says how likely each of several answers is, apart from the
// others: every product line a customer will buy, say. Forward maps x,
// [batch, Features], to [batch, labels] chances from 0 to 1.
type MultiLabel struct {
	Trees []*tree.Module
	shape MultiLabelShape
}

func NewMultiLabel(s MultiLabelShape) *MultiLabel {
	m := &MultiLabel{shape: s}
	for _, t := range s.Trees {
		m.Trees = append(m.Trees, tree.NewModule(t))
	}
	return m
}

func (m *MultiLabel) Forward(x *tensor.Tensor) *tensor.Tensor {
	if err := x.Err(); err != nil {
		return x
	}
	if x.Rank() != 2 || x.Shape()[1] != m.shape.Features {
		return tensor.Failed(fmt.Errorf("multi-label: input %v, want [batch, %d]: %w", x.Shape(), m.shape.Features, tensor.ErrShape))
	}
	n := x.Shape()[0]
	out := make([]*tensor.Tensor, len(m.Trees))
	for i, t := range m.Trees {
		out[i] = t.Forward(x).Softmax(1).Shrink([][2]int{{0, n}, {1, 2}})
	}
	return tensor.Cat(1, out...)
}

// multiLabel learns which of several answers apply to a row, from a column
// that lists them, such as "snacks; drinks": one boosted-tree classifier
// for each.
var multiLabel = &Learner{
	Cells: func(ctx context.Context, c Cells, progress func(int, int)) (Learned, error) {
		ans := slices.Index(c.Header, c.Target)
		if ans < 0 {
			return Learned{}, cellsError("there is no column called %q", c.Target)
		}
		count := map[string]int{}
		sets := make([][]string, len(c.Rows))
		for i, r := range c.Rows {
			for _, v := range labelSplit.Split(strings.TrimSpace(r[ans]), -1) {
				if v != "" && !slices.Contains(sets[i], v) {
					sets[i] = append(sets[i], v)
					count[v]++
				}
			}
		}
		var labels []string
		for v, k := range count {
			if k >= minLabelRows && k <= len(c.Rows)-minLabelRows {
				labels = append(labels, v)
			}
		}
		slices.SortFunc(labels, func(a, b string) int { return cmp.Or(cmp.Compare(count[b], count[a]), cmp.Compare(a, b)) })
		labels = labels[:min(len(labels), maxLabels)]
		if len(labels) < 2 {
			return Learned{}, cellsError("the %q column needs at least 2 answers that each show up in %d rows or more, and not in every row; list a row's answers like snacks; drinks", c.Target, minLabelRows)
		}
		slices.Sort(labels)
		cols, x := c.encode(c.situation(ans))
		f := schema.Width(cols)
		if f == 0 {
			return Learned{}, cellsError("no column describes the rows; add some, like segment, region or past orders")
		}
		n := len(c.Rows)
		order := rand.New(rand.NewPCG(37, 41)).Perm(n)
		cut := n / 5
		held, train := order[:cut], order[cut:]
		has := func(i int, l string) float32 {
			if slices.Contains(sets[i], l) {
				return 1
			}
			return 0
		}
		shape := MultiLabelShape{Features: f}
		m := &MultiLabel{}
		var aucs float64
		scored := 0
		for li, l := range labels {
			fit := func(rows []int) (*tree.Model, error) {
				d := tabular.Data{Features: f, X: rowsOf(x, f, rows)}
				for _, i := range rows {
					d.Y = append(d.Y, has(i, l))
				}
				return boostOn(ctx, d, tabular.Classification, 300, nil)
			}
			if len(held) >= 20 {
				t, err := fit(train)
				if err != nil {
					return Learned{}, err
				}
				p := t.Predict(rowsOf(x, f, held))
				ss, ps := make([]float64, len(held)), make([]bool, len(held))
				for h, i := range held {
					ss[h], ps[h] = float64(p[h*2+1]), has(i, l) == 1
				}
				if a := auc(ss, ps); a > 0 {
					aucs += a
					scored++
				}
			}
			all := append(slices.Clone(train), held...)
			t, err := fit(all)
			if err != nil {
				return Learned{}, err
			}
			shape.Trees = append(shape.Trees, t.Shape())
			m.Trees = append(m.Trees, t.Module())
			if progress != nil {
				progress(li+1, len(labels))
			}
		}
		m.shape = shape
		var accuracy float64
		if scored > 0 {
			accuracy = aucs / float64(scored)
		}
		s, err := shapeJSON(shape)
		return Learned{Module: m, Shape: s, Columns: cols, Labels: labels, Accuracy: accuracy}, err
	},
	Build: func(shape string) (nn.Module, error) {
		var s MultiLabelShape
		if err := json.Unmarshal([]byte(shape), &s); err != nil {
			return nil, fmt.Errorf("a multi-label model's shape: %w", err)
		}
		return NewMultiLabel(s), nil
	},
}

// ---- Discount response ----

// discountWords name the column of the discount given.
var discountWords = regexp.MustCompile(`discount|disc_|_disc|markdown|price_cut|rebate|diskon|potongan`)

// discountLevels bounds the discounts a discount-response model answers
// for.
const discountLevels = 8

// DiscountShape sizes a Discount.
type DiscountShape struct {
	Tree     tree.Shape `json:"tree"`
	Features int        `json:"features"` // the situation's numbers
	Levels   []float64  `json:"levels"`
	// Percent says the discount is a percentage, so the best one weighs
	// the chance against what it gives away.
	Percent bool `json:"percent"`
}

// Discount answers how likely a deal is to close at each discount level.
// Forward maps x, [batch, Features], to [batch, 2+levels] when Percent
// (the best discount, the chance at it, then the chance at each level),
// and to [batch, levels] otherwise. A larger discount never lowers the
// chance.
type Discount struct {
	Trees *tree.Module
	shape DiscountShape
}

func (m *Discount) Forward(x *tensor.Tensor) *tensor.Tensor {
	if err := x.Err(); err != nil {
		return x
	}
	s := m.shape
	if x.Rank() != 2 || x.Shape()[1] != s.Features {
		return tensor.Failed(fmt.Errorf("discount: input %v, want [batch, %d]: %w", x.Shape(), s.Features, tensor.ErrShape))
	}
	n, k := x.Shape()[0], len(s.Levels)
	chance := make([]*tensor.Tensor, k)
	for i, l := range s.Levels {
		in := tensor.Cat(1, x, tensor.Full(tensor.Float32, l, n, 1))
		chance[i] = m.Trees.Forward(in).Softmax(1).Shrink([][2]int{{0, n}, {1, 2}})
	}
	p, _ := tensor.Cat(1, chance...).CumMax(1)
	if !s.Percent {
		return p
	}
	lv := make([]float32, k)
	for i, l := range s.Levels {
		lv[i] = float32(l)
	}
	levels := tensor.FromSlice(lv, 1, k)
	kept := p.Mul(scalar(1).Sub(levels.Div(scalar(100))))
	best := kept.Argmax(1).Reshape(n, 1)
	at := levels.Expand(n, k).Gather(1, best)
	return tensor.Cat(1, at, p.Gather(1, best), p)
}

// discountResponse learns, from past deals and whether each closed, how
// the chance of closing moves with the discount, and answers it for every
// discount level with the one that keeps the most revenue.
var discountResponse = &Learner{
	Cells: func(ctx context.Context, c Cells, progress func(int, int)) (Learned, error) {
		ev := slices.Index(c.Header, c.Target)
		if ev < 0 {
			return Learned{}, cellsError("there is no column called %q", c.Target)
		}
		events := c.column(ev)
		if !yesNo(events) {
			return Learned{}, cellsError("the %q column needs yes or no: yes when the deal closed", c.Target)
		}
		disc := -1
		for j, h := range c.Header {
			if disc < 0 && j != ev && discountWords.MatchString(strings.ToLower(h)) && numbers(c.column(j)) {
				disc = j
			}
		}
		if disc < 0 {
			return Learned{}, cellsError("no column says the discount given; name one like discount_pct, in %% or as an amount")
		}
		var keep [][]string
		var won []bool
		var given []float64
		for _, r := range c.Rows {
			d, ok := schema.ParseNumber(r[disc])
			if v := strings.TrimSpace(r[ev]); ok && v != "" {
				keep, won, given = append(keep, r), append(won, isYes(v)), append(given, d)
			}
		}
		if len(keep) < 50 {
			return Learned{}, cellsError("only %d deals have both a discount and an outcome; at least 50 are needed", len(keep))
		}
		sub := Cells{Header: c.Header, Rows: keep, Kinds: c.Kinds}
		var cols []schema.Column
		var x []float32
		if idx := sub.situation(ev, disc); len(idx) > 0 {
			cols, x = sub.encode(idx)
		}
		f := schema.Width(cols)
		n := len(keep)
		d := tabular.Data{Features: f + 1}
		for i := range n {
			d.X = append(append(d.X, x[i*f:(i+1)*f]...), float32(given[i]))
			y := float32(0)
			if won[i] {
				y = 1
			}
			d.Y = append(d.Y, y)
		}
		// Score on deals held back from a first fit.
		var accuracy float64
		train, held := d.Split(0.2, 43)
		if held.Rows() >= 20 {
			t, err := boostOn(ctx, train, tabular.Classification, boostTrees, nil)
			if err != nil {
				return Learned{}, err
			}
			p := t.Predict(held.X)
			ss, ps := make([]float64, held.Rows()), make([]bool, held.Rows())
			for i := range ss {
				ss[i], ps[i] = float64(p[i*2+1]), held.Y[i] == 1
			}
			accuracy = auc(ss, ps)
		}
		t, err := boostOn(ctx, d, tabular.Classification, boostTrees, progress)
		if err != nil {
			return Learned{}, err
		}

		// The levels: every discount given, or evenly spread ones.
		vals := slices.Clone(given)
		slices.Sort(vals)
		vals = slices.Compact(vals)
		percent := vals[0] >= 0 && vals[len(vals)-1] <= 100
		levels := vals
		if len(vals) > discountLevels {
			levels = nil
			lo, hi := vals[0], vals[len(vals)-1]
			step := niceStep((hi - lo) / float64(discountLevels-1))
			for v := math.Floor(lo/step) * step; v <= hi+1e-9 && len(levels) < discountLevels+1; v += step {
				levels = append(levels, math.Round(v*1000)/1000)
			}
		}
		shape := DiscountShape{Tree: t.Shape(), Features: f, Levels: levels, Percent: percent}
		var labels []string
		if percent {
			labels = append(labels, "best discount %", "chance at the best discount")
		}
		for _, l := range levels {
			v := strconv.FormatFloat(l, 'f', -1, 64)
			if percent {
				v += "%"
			}
			labels = append(labels, "chance at "+v)
		}
		s, err := shapeJSON(shape)
		return Learned{Module: &Discount{Trees: t.Module(), shape: shape}, Shape: s, Columns: cols, Labels: labels, Accuracy: accuracy}, err
	},
	Build: func(shape string) (nn.Module, error) {
		var s DiscountShape
		if err := json.Unmarshal([]byte(shape), &s); err != nil {
			return nil, fmt.Errorf("a discount-response model's shape: %w", err)
		}
		return &Discount{Trees: tree.NewModule(s.Tree), shape: s}, nil
	},
}

// niceStep rounds v up to 1, 2 or 5 times a power of ten.
func niceStep(v float64) float64 {
	if v <= 0 {
		return 1
	}
	p := math.Pow(10, math.Floor(math.Log10(v)))
	for _, m := range []float64{1, 2, 5, 10} {
		if v <= m*p {
			return m * p
		}
	}
	return 10 * p
}

// ---- A range for a number ----

// rangeCover is the share of real values a range aims to hold.
const rangeCover = 0.8

// RangeShape sizes a Range.
type RangeShape struct {
	Point, Spread tree.Shape
	Features      int
	// Low and High scale the spread below and above the likeliest value.
	Low, High, Floor float64
	NonNegative      bool
}

// Range answers a number with a range it is likely to fall in. Forward
// maps x, [batch, Features], to [batch, 3]: the likeliest value, then the
// low and high ends of a range that holds about 8 in 10 real values.
type Range struct {
	Point, Spread *tree.Module
	shape         RangeShape
}

func (m *Range) Forward(x *tensor.Tensor) *tensor.Tensor {
	if err := x.Err(); err != nil {
		return x
	}
	s := m.shape
	if x.Rank() != 2 || x.Shape()[1] != s.Features {
		return tensor.Failed(fmt.Errorf("range: input %v, want [batch, %d]: %w", x.Shape(), s.Features, tensor.ErrShape))
	}
	p := m.Point.Forward(x)
	g := m.Spread.Forward(x).Maximum(scalar(s.Floor))
	lo, hi := p.Add(g.Mul(scalar(s.Low))), p.Add(g.Mul(scalar(s.High)))
	if s.NonNegative {
		p, lo, hi = p.Maximum(scalar(0)), lo.Maximum(scalar(0)), hi.Maximum(scalar(0))
	}
	return tensor.Cat(1, p, lo, hi)
}

// quantile returns the q quantile of sorted vs.
func quantile(vs []float64, q float64) float64 {
	if len(vs) == 0 {
		return 0
	}
	i := q * float64(len(vs)-1)
	lo := int(math.Floor(i))
	hi := min(lo+1, len(vs)-1)
	return vs[lo] + (vs[hi]-vs[lo])*(i-float64(lo))
}

// numberRange learns a number with boosted trees, and how far off they
// are for each row with a second model, scaled so the range holds 8 in
// 10 real values of rows it didn't learn from: normalized conformal
// prediction.
var numberRange = &Learner{
	Cells: func(ctx context.Context, c Cells, progress func(int, int)) (Learned, error) {
		ans := slices.Index(c.Header, c.Target)
		if ans < 0 {
			return Learned{}, cellsError("there is no column called %q", c.Target)
		}
		var keep [][]string
		var y []float64
		for _, r := range c.Rows {
			if v, ok := schema.ParseNumber(r[ans]); ok {
				keep, y = append(keep, r), append(y, v)
			}
		}
		if len(keep) < 100 {
			return Learned{}, cellsError("only %d rows have a number in %q; at least 100 are needed for a range", len(keep), c.Target)
		}
		sub := Cells{Header: c.Header, Rows: keep, Kinds: c.Kinds}
		cols, x := sub.encode(sub.situation(ans))
		f := schema.Width(cols)
		if f == 0 {
			return Learned{}, cellsError("no column describes the rows; add some the number depends on")
		}
		n := len(keep)
		data := func(rows []int, target func(i int) float64) tabular.Data {
			d := tabular.Data{Features: f, X: rowsOf(x, f, rows)}
			for _, i := range rows {
				d.Y = append(d.Y, float32(target(i)))
			}
			return d
		}
		order := rand.New(rand.NewPCG(47, 53)).Perm(n)
		cut := n / 5
		held, rest := order[:cut], order[cut:]
		step, steps := 0, 6
		tick := func() {
			if step++; progress != nil {
				progress(step, steps)
			}
		}
		// Each row's error from trees that didn't learn it.
		resid := make([]float64, n)
		const folds = 3
		for k := range folds {
			var in, out []int
			for i, r := range rest {
				if i%folds == k {
					out = append(out, r)
				} else {
					in = append(in, r)
				}
			}
			t, err := boostOn(ctx, data(in, func(i int) float64 { return y[i] }), tabular.Regression, 300, nil)
			if err != nil {
				return Learned{}, err
			}
			p := t.Predict(rowsOf(x, f, out))
			for o, i := range out {
				resid[i] = y[i] - float64(p[o])
			}
			tick()
		}
		spreadT, err := boostOn(ctx, data(rest, func(i int) float64 { return math.Abs(resid[i]) }), tabular.Regression, 200, nil)
		if err != nil {
			return Learned{}, err
		}
		tick()
		var typical []float64
		for _, i := range rest {
			typical = append(typical, math.Abs(resid[i]))
		}
		slices.Sort(typical)
		floor := max(quantile(typical, 0.1), 1e-6)
		g := spreadT.Predict(rowsOf(x, f, rest))
		norm := make([]float64, len(rest))
		for k, i := range rest {
			norm[k] = resid[i] / max(float64(g[k]), floor)
		}
		slices.Sort(norm)
		low, high := quantile(norm, (1-rangeCover)/2), quantile(norm, (1+rangeCover)/2)
		pointT, err := boostOn(ctx, data(rest, func(i int) float64 { return y[i] }), tabular.Regression, boostTrees, nil)
		if err != nil {
			return Learned{}, err
		}
		tick()
		nonNeg := slices.Min(y) >= 0
		// How often held rows fall in their range, and how far off the
		// likeliest value is.
		var inside, mae float64
		if len(held) > 0 {
			hx := rowsOf(x, f, held)
			p, s := pointT.Predict(hx), spreadT.Predict(hx)
			for k, i := range held {
				gs := max(float64(s[k]), floor)
				lo, hi := float64(p[k])+low*gs, float64(p[k])+high*gs
				if nonNeg {
					lo = max(lo, 0)
				}
				if y[i] >= lo && y[i] <= hi {
					inside++
				}
				mae += math.Abs(y[i] - float64(p[k]))
			}
			inside /= float64(len(held))
			mae /= float64(len(held))
		}
		tick()
		shape := RangeShape{Point: pointT.Shape(), Spread: spreadT.Shape(), Features: f, Low: low, High: high, Floor: floor, NonNegative: nonNeg}
		s, err := shapeJSON(shape)
		tick()
		return Learned{
			Module: &Range{Point: pointT.Module(), Spread: spreadT.Module(), shape: shape}, Shape: s, Columns: cols,
			Labels: []string{"likely", "low", "high"}, Accuracy: inside, MAE: mae,
		}, err
	},
	Build: func(shape string) (nn.Module, error) {
		var s RangeShape
		if err := json.Unmarshal([]byte(shape), &s); err != nil {
			return nil, fmt.Errorf("a range model's shape: %w", err)
		}
		return &Range{Point: tree.NewModule(s.Point), Spread: tree.NewModule(s.Spread), shape: s}, nil
	},
}

// ---- Duplicate accounts ----

var (
	nameWords   = regexp.MustCompile(`name|nama|company|customer|account|outlet|shop|store|business|client|lead|toko`)
	idWords     = regexp.MustCompile(`(^|_| )(id|code|kode|no|number|nomor)$|_id_|^id_`)
	detailWords = regexp.MustCompile(`address|alamat|street|jalan|city|kota|phone|telp|mobile|hp|whatsapp|email|tax|npwp|website|zip|postal`)
	// legalForms are words that say what kind of company, not which.
	legalForms = []string{"pt", "cv", "tbk", "ud", "pd", "fa", "koperasi", "persero", "ltd", "limited", "inc", "incorporated", "llc", "co", "corp", "corporation", "company", "gmbh", "bv", "sa", "pte", "plc", "sdn", "bhd", "the", "and", "dan", "of"}
)

const (
	// maxAccounts bounds the accounts a duplicate finder keeps.
	maxAccounts = 20_000
	// sameScale and sameAt set how surely accounts are the same: at a
	// similarity of sameAt, a match is as likely as no match.
	sameScale, sameAt = 25.0, 0.7
	// NoMatch is the answer when no account looks the same.
	NoMatch = "(no match)"
)

// DuplicateShape sizes a Duplicates.
type DuplicateShape struct {
	Accounts int
	Details  int // other text columns, after the name
}

// Duplicates answers which known accounts a name, with its other
// details, is likely the same business as. Forward maps x, [batch,
// TextDims*(1+Details)+1] (the name's words, each detail's words, then
// the account's own key), to [batch, Accounts+1] scores whose softmax
// says how likely each account is the one, and the last how likely none
// is. An account isn't matched with itself.
type Duplicates struct {
	// Weight is each word's inverse document frequency, 0 for legal
	// forms like PT or Ltd.
	Weight *tensor.Param // Float32 [1+Details, TextDims]
	// Names and Details are the accounts' weighted words, of length 1.
	Names   *tensor.Param   // Float32 [Accounts, TextDims]
	Details []*tensor.Param // Float32 [Accounts, TextDims] each
	shape   DuplicateShape
}

func NewDuplicates(s DuplicateShape) *Duplicates {
	m := &Duplicates{Weight: zeros(1+s.Details, schema.TextDims), Names: zeros(max(s.Accounts, 1), schema.TextDims), shape: s}
	for range s.Details {
		m.Details = append(m.Details, zeros(max(s.Accounts, 1), schema.TextDims))
	}
	return m
}

func (m *Duplicates) Forward(x *tensor.Tensor) *tensor.Tensor {
	if err := x.Err(); err != nil {
		return x
	}
	s, td := m.shape, schema.TextDims
	want := td*(1+s.Details) + 1
	if x.Rank() != 2 || x.Shape()[1] != want {
		return tensor.Failed(fmt.Errorf("duplicates: input %v, want [batch, %d]: %w", x.Shape(), want, tensor.ErrShape))
	}
	n, a := x.Shape()[0], s.Accounts
	w := m.Weight.Value()
	// cos is the cosine of text part k with every account's.
	cos := func(k int, known *tensor.Tensor) (*tensor.Tensor, *tensor.Tensor) {
		q := x.Shrink([][2]int{{0, n}, {k * td, (k + 1) * td}}).Mul(w.Shrink([][2]int{{k, k + 1}, {0, td}}))
		size := q.Mul(q).Sum(1).Reshape(n, 1).Sqrt()
		has := scalar(0).Less(size)
		return q.Div(size.Maximum(scalar(1e-9))).MatMul(known.Transpose(0, 1)), has
	}
	name, _ := cos(0, m.Names.Value())
	sum, weight := name.Mul(scalar(2)), tensor.Full(tensor.Float32, 2, n, a)
	for k, d := range m.Details {
		known := d.Value()
		c, has := cos(k+1, known)
		there := scalar(0).Less(known.Mul(known).Sum(1).Reshape(1, a))
		both := has.Mul(there)
		sum, weight = sum.Add(c.Mul(both)), weight.Add(both.Expand(n, a))
	}
	sim := sum.Div(weight)
	key := x.Shrink([][2]int{{0, n}, {want - 1, want}})
	self := tensor.Arange[float32](0, float32(a), 1).Reshape(1, a).Equal(key)
	score := sim.Mul(scalar(sameScale)).Sub(self.Mul(scalar(1e4)))
	return tensor.Cat(1, score, tensor.Full(tensor.Float32, sameScale*sameAt, n, 1))
}

// duplicates learns a table of accounts and finds, for any account or
// new lead, the accounts that are likely the same business written
// another way: "PT Maju Jaya" and "Maju Jaya, PT".
var duplicates = &Learner{
	Cells: func(ctx context.Context, c Cells, progress func(int, int)) (Learned, error) {
		textual := func(j int) bool { return !numbers(c.column(j)) }
		id := -1
		for j, h := range c.Header {
			if id < 0 && idWords.MatchString(strings.ToLower(h)) && distinct(c.column(j)) >= len(c.Rows)*9/10 {
				id = j
			}
		}
		if id < 0 {
			return Learned{}, cellsError("no column gives each account its own id; add one like customer_id, so an account isn't matched with itself")
		}
		name := -1
		for _, prefer := range []bool{true, false} {
			for j, h := range c.Header {
				l := strings.ToLower(h)
				if name < 0 && j != id && textual(j) && nameWords.MatchString(l) && (!prefer || strings.Contains(l, "name") || strings.Contains(l, "nama")) {
					name = j
				}
			}
		}
		if name < 0 {
			return Learned{}, cellsError("no column holds the account's name; name one like customer_name")
		}
		var details []int
		for j, h := range c.Header {
			if j != id && j != name && textual(j) && detailWords.MatchString(strings.ToLower(h)) {
				details = append(details, j)
			}
		}
		var rows [][]string
		seen := map[string]bool{}
		for _, r := range c.Rows {
			k := strings.TrimSpace(r[id])
			if k == "" || seen[k] || strings.TrimSpace(r[name]) == "" {
				continue
			}
			seen[k] = true
			rows = append(rows, r)
			if len(rows) == maxAccounts {
				break
			}
		}
		if len(rows) < 2 {
			return Learned{}, cellsError("there are fewer than 2 accounts with an id and a name")
		}
		texts := append([]int{name}, details...)
		td, a := schema.TextDims, len(rows)
		text := schema.Column{Kind: schema.Text}
		bags := make([][][]float32, len(texts)) // [part][account][word]
		weight := make([]float32, 0, len(texts)*td)
		legal := map[int]bool{}
		for _, w := range legalForms {
			v := schema.EncodeRow(nil, []schema.Column{text}, []string{w}, nil)
			for b, x := range v {
				if x > 0 {
					legal[b] = true
				}
			}
		}
		for k, j := range texts {
			df := make([]float64, td)
			bags[k] = make([][]float32, a)
			for i, r := range rows {
				v := schema.EncodeRow(nil, []schema.Column{text}, []string{r[j]}, nil)
				bags[k][i] = v
				for b, x := range v {
					if x > 0 {
						df[b]++
					}
				}
			}
			for b := range td {
				w := float32(math.Log(float64(a+1)/(df[b]+1)) + 1)
				if k == 0 && legal[b] {
					w = 0
				}
				weight = append(weight, w)
			}
		}
		flat := func(k int) []float32 {
			out := make([]float32, 0, a*td)
			for i := range a {
				v := make([]float32, td)
				var size float64
				for b := range td {
					v[b] = bags[k][i][b] * weight[k*td+b]
					size += float64(v[b] * v[b])
				}
				if size > 0 {
					for b := range v {
						v[b] /= float32(math.Sqrt(size))
					}
				}
				out = append(out, v...)
			}
			return out
		}
		shape := DuplicateShape{Accounts: a, Details: len(details)}
		m := &Duplicates{Weight: buffer(weight, 1+len(details), td), Names: buffer(flat(0), a, td), shape: shape}
		for k := range details {
			m.Details = append(m.Details, buffer(flat(k+1), a, td))
		}
		ids := make([]string, a)
		labels := make([]string, 0, a+1)
		used := map[string]bool{}
		for i, r := range rows {
			ids[i] = strings.TrimSpace(r[id])
			l := ids[i] + " " + strings.TrimSpace(r[name])
			for used[l] {
				l += "'"
			}
			used[l] = true
			labels = append(labels, l)
		}
		labels = append(labels, NoMatch)
		cols := []schema.Column{{Name: c.Header[name], Kind: schema.Text}}
		for _, j := range details {
			cols = append(cols, schema.Column{Name: c.Header[j], Kind: schema.Text})
		}
		cols = append(cols, schema.Column{Name: c.Header[id], Kind: schema.Key, Values: ids})
		if progress != nil {
			progress(1, 1)
		}
		s, err := shapeJSON(shape)
		return Learned{Module: m, Shape: s, Columns: cols, Labels: labels}, err
	},
	Build: func(shape string) (nn.Module, error) {
		var s DuplicateShape
		if err := json.Unmarshal([]byte(shape), &s); err != nil {
			return nil, fmt.Errorf("a duplicate finder's shape: %w", err)
		}
		return NewDuplicates(s), nil
	},
}
