package arch

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/gsoultan/mimir/cluster"
	"github.com/gsoultan/mimir/forecast"
	"github.com/gsoultan/mimir/nn"
	"github.com/gsoultan/mimir/recommend"
	"github.com/gsoultan/mimir/tensor"
	"github.com/gsoultan/mimir/tree"

	"github.com/gsoultan/mimir-server/internal/schema"
)

// Target says what a dataset's answer column is to an architecture.
type Target string

const (
	// AnswerTarget is the answer a model learns to give.
	AnswerTarget Target = ""
	// NoTarget is nothing: the model reads every column and needs no
	// answers to learn from.
	NoTarget Target = "none"
	// ItemTarget is the item someone picked, bought or rated; another
	// column says who.
	ItemTarget Target = "item"
	// ValueTarget is a value measured over time, to forecast; another
	// column says when.
	ValueTarget Target = "value"
)

// Cells is a table as text, for a Learner that reads the cells
// themselves.
type Cells struct {
	Header []string
	Rows   [][]string // each row's cells, in Header's order
	// Target is the column picked as the answer, which Target says the
	// meaning of.
	Target string
	// Kinds are the column kinds the table was first read with.
	Kinds map[string]schema.Kind
}

// Learned is what a Learner's Cells made.
type Learned struct {
	Module nn.Module
	Shape  string // for Build
	// Columns are what a question to the model names, Labels what it
	// answers.
	Columns []schema.Column
	Labels  []string
	// Accuracy and MAE score the model on rows it didn't learn from,
	// when it has a way to; 0 otherwise.
	Accuracy, MAE float64
}

// errCells reports a table a Cells learner can't learn from; its message
// is for people.
var errCells = errors.New("this table doesn't fit")

func cellsError(format string, args ...any) error {
	return fmt.Errorf("%w: %s", errCells, fmt.Sprintf(format, args...))
}

// column returns the cells of column j.
func (c Cells) column(j int) []string {
	out := make([]string, len(c.Rows))
	for i, r := range c.Rows {
		out[i] = strings.TrimSpace(r[j])
	}
	return out
}

// encode finds the kinds of the columns at idx and turns every row into
// their numbers. A photo column is left out.
func (c Cells) encode(idx []int) ([]schema.Column, []float32) {
	names := make([]string, len(idx))
	for k, j := range idx {
		names[k] = c.Header[j]
	}
	d := schema.NewDetector(names, c.Kinds)
	replay := func(add func(j int, v string)) {
		for _, r := range c.Rows {
			for k, j := range idx {
				add(k, r[j])
			}
		}
	}
	replay(d.Add)
	cols := d.Columns(replay)
	keep := idx[:0:0]
	var kept []schema.Column
	for k, col := range cols {
		if col.Kind != schema.Photo {
			keep, kept = append(keep, idx[k]), append(kept, col)
		}
	}
	x := make([]float32, 0, len(c.Rows)*schema.Width(kept))
	cells := make([]string, len(keep))
	for _, r := range c.Rows {
		for k, j := range keep {
			cells[k] = r[j]
		}
		x = schema.EncodeRow(x, kept, cells, nil)
	}
	return kept, x
}

// all returns the index of every column.
func (c Cells) all() []int {
	out := make([]int, len(c.Header))
	for j := range out {
		out[j] = j
	}
	return out
}

// groups finds groups of similar rows by k-means, picking the number of
// groups, 2 to 8, whose rows sit best in them by their silhouette.
var groups = &Learner{
	Cells: func(ctx context.Context, c Cells, progress func(int, int)) (Learned, error) {
		cols, x := c.encode(c.all())
		f := schema.Width(cols)
		n := len(c.Rows)
		if f == 0 {
			return Learned{}, cellsError("no column holds numbers, categories or text to group by")
		}
		most := min(8, n-1)
		var best *cluster.Means
		score := math.Inf(-1)
		for k := 2; k <= most; k++ {
			m, err := cluster.KMeans(ctx, x, f, k)
			if err != nil {
				return Learned{}, err
			}
			if s := m.Silhouette(x, 1); s > score {
				best, score = m, s
			}
			if progress != nil {
				progress(k-1, most-1)
			}
		}
		if best == nil {
			return Learned{}, cellsError("at least 3 rows are needed to find groups")
		}
		s, err := shapeJSON(best.Shape())
		return Learned{Module: best.Module(), Shape: s, Columns: cols, Labels: groupNames(best, cols)}, err
	},
	Build: func(shape string) (nn.Module, error) {
		var s cluster.Shape
		if err := json.Unmarshal([]byte(shape), &s); err != nil {
			return nil, fmt.Errorf("a grouping's shape: %w", err)
		}
		return cluster.NewModule(s), nil
	},
}

// groupNames names each group after the feature that sets it apart most:
// "Group 2: high temp_c".
func groupNames(m *cluster.Means, cols []schema.Column) []string {
	features := schema.Features(cols)
	kinds := make([]schema.Kind, 0, len(features))
	for _, c := range cols {
		for range c.Width() {
			kinds = append(kinds, c.Kind)
		}
	}
	out := make([]string, m.K())
	for a := range out {
		center := m.Centers[a*m.Features : (a+1)*m.Features]
		best, far := -1, 0.0
		for j, v := range center {
			if kinds[j] != schema.Text && math.Abs(float64(v)) > far {
				best, far = j, math.Abs(float64(v))
			}
		}
		out[a] = fmt.Sprintf("Group %d", a+1)
		if best < 0 || far < 0.25 {
			continue
		}
		name, high := features[best], center[best] > 0
		switch kinds[best] {
		case schema.Category:
			name = strings.Replace(name, "=", " ", 1)
			if !high {
				name = "not " + name
			}
		case schema.YesNo:
			if !high {
				name = "not " + name
			}
		default:
			if high {
				name = "high " + name
			} else {
				name = "low " + name
			}
		}
		out[a] += ": " + name
	}
	return out
}

// oddOne learns what usual rows look like with an isolation forest and
// answers how unusual a row is, from 0 to 1.
var oddOne = &Learner{
	Cells: func(ctx context.Context, c Cells, progress func(int, int)) (Learned, error) {
		cols, x := c.encode(c.all())
		f := schema.Width(cols)
		if f == 0 {
			return Learned{}, cellsError("no column holds numbers, categories or text to compare")
		}
		opts := []tree.Option{tree.Trees(200)}
		if progress != nil {
			opts = append(opts, tree.Progress(progress))
		}
		m, err := tree.IsolationForest(ctx, x, f, opts...)
		if err != nil {
			return Learned{}, err
		}
		s, err := shapeJSON(m.Shape())
		return Learned{Module: m.Module(), Shape: s, Columns: cols, Labels: []string{"how unusual"}}, err
	},
	Build: buildTree,
}

// pick returns the first of the columns at idx whose name holds one of
// words, or -1.
func pick(header []string, idx []int, words *regexp.Regexp) int {
	for _, j := range idx {
		if words.MatchString(strings.ToLower(header[j])) {
			return j
		}
	}
	return -1
}

var (
	whoWords    = regexp.MustCompile(`user|customer|client|member|account|buyer|shopper|visitor|person|people|who|reader|viewer|player|patient|student|guest`)
	ratingWords = regexp.MustCompile(`rating|stars|score|grade`)
	amountWords = regexp.MustCompile(`quantity|qty|count|amount|units|times|clicks|views|plays|minutes`)
)

// numbers reports whether every filled cell of cells is a number, and
// some are.
func numbers(cells []string) bool {
	any := false
	for _, v := range cells {
		if v == "" {
			continue
		}
		if _, ok := schema.ParseNumber(v); !ok {
			return false
		}
		any = true
	}
	return any
}

// recommendTop is how many items a recommender lists.
const recommendTop = 10

// recommender learns which items each person will want from who picked
// what, by matrix factorization. The answer column is the item; the who
// column is found by its name, or is the first other column whose
// values repeat.
var recommender = &Learner{
	Cells: func(ctx context.Context, c Cells, progress func(int, int)) (Learned, error) {
		item := slices.Index(c.Header, c.Target)
		if item < 0 {
			return Learned{}, cellsError("there is no column called %q", c.Target)
		}
		var others []int
		for j := range c.Header {
			if j != item {
				others = append(others, j)
			}
		}
		who := pick(c.Header, others, whoWords)
		if who < 0 {
			for _, j := range others {
				cells := c.column(j)
				if !numbers(cells) && !dates(cells) && distinct(cells) < len(cells) {
					who = j
					break
				}
			}
		}
		if who < 0 {
			return Learned{}, cellsError("no column says who picked each item; name one customer, user or member")
		}
		var rest []int
		for _, j := range others {
			if j != who {
				rest = append(rest, j)
			}
		}
		value, ratings := -1, false
		if j := pick(c.Header, rest, ratingWords); j >= 0 && numbers(c.column(j)) {
			value, ratings = j, true
		} else if j := pick(c.Header, rest, amountWords); j >= 0 && numbers(c.column(j)) {
			value = j
		}

		users, items := index(c.column(who)), index(c.column(item))
		uid, iid := map[string]int{}, map[string]int{}
		for i, u := range users {
			uid[u] = i
		}
		for i, v := range items {
			iid[v] = i
		}
		events := make([]recommend.Event, 0, len(c.Rows))
		for _, r := range c.Rows {
			u, v := strings.TrimSpace(r[who]), strings.TrimSpace(r[item])
			if u == "" || v == "" {
				continue
			}
			e := recommend.Event{User: uid[u], Item: iid[v], Value: 1}
			if value >= 0 {
				f, ok := schema.ParseNumber(r[value])
				if !ok || (!ratings && f <= 0) {
					continue
				}
				e.Value = float32(f)
			}
			events = append(events, e)
		}
		if len(items) < 2 {
			return Learned{}, cellsError("the %q column names only one item, so there is nothing to recommend", c.Target)
		}
		// Fewer factors than items, so a person's few picks speak for
		// items like them rather than only for themselves.
		opts := []recommend.Option{recommend.Factors(min(32, max(2, len(items)/4)))}
		if ratings {
			opts = append(opts, recommend.Ratings())
		} else {
			// Picks weigh 1+alpha each; a firmer penalty keeps a person of
			// a few picks from fitting only those.
			opts = append(opts, recommend.L2(10))
		}
		// Score it by hiding each person's last event, then learn from
		// everything.
		train, held := holdLast(events)
		hit := 0.0
		if len(held) > 0 {
			m, err := recommend.Fit(ctx, len(users), len(items), train, opts...)
			if err != nil {
				return Learned{}, err
			}
			hit = m.HitRate(train, held, recommendTop)
		}
		if progress != nil {
			progress(1, 2)
		}
		m, err := recommend.Fit(ctx, len(users), len(items), events, opts...)
		if err != nil {
			return Learned{}, err
		}
		// Someone it has never seen gets the average person's taste: the
		// items most people like.
		avg := make([]float32, m.Factors)
		for u := range m.Users {
			for k, v := range m.UserFactors[u*m.Factors : (u+1)*m.Factors] {
				avg[k] += v / float32(m.Users)
			}
		}
		m.UserFactors = append(m.UserFactors, avg...)
		m.Users++
		s, err := shapeJSON(m.Shape())
		cols := []schema.Column{{Name: c.Header[who], Kind: schema.Key, Values: users}}
		return Learned{Module: m.Module(), Shape: s, Columns: cols, Labels: items, Accuracy: hit}, err
	},
	Build: func(shape string) (nn.Module, error) {
		var s recommend.Shape
		if err := json.Unmarshal([]byte(shape), &s); err != nil {
			return nil, fmt.Errorf("a recommender's shape: %w", err)
		}
		return recommend.NewModule(s), nil
	},
}

// index returns the distinct filled cells, most common first.
func index(cells []string) []string {
	count := map[string]int{}
	for _, v := range cells {
		if v != "" {
			count[v]++
		}
	}
	out := make([]string, 0, len(count))
	for v := range count {
		out = append(out, v)
	}
	slices.SortFunc(out, func(a, b string) int { return cmp.Or(cmp.Compare(count[b], count[a]), cmp.Compare(a, b)) })
	return out
}

func distinct(cells []string) int { return len(index(cells)) }

// holdLast splits events into each user's last one, for users with more
// than one, and the rest.
func holdLast(events []recommend.Event) (train, held []recommend.Event) {
	last, count := map[int]int{}, map[int]int{}
	for i, e := range events {
		last[e.User] = i
		count[e.User]++
	}
	for i, e := range events {
		if count[e.User] > 1 && last[e.User] == i {
			held = append(held, e)
		} else {
			train = append(train, e)
		}
	}
	return train, held
}

// dateLayouts are the ways a date or time cell may be written.
var dateLayouts = []string{
	time.RFC3339, "2006-01-02T15:04:05", "2006-01-02T15:04", "2006-01-02 15:04:05", "2006-01-02 15:04",
	"2006-01-02", "2006/01/02", "2006-01", "2006/01", "02.01.2006", "Jan 2006", "January 2006", "2 Jan 2006",
}

// parseDate reads a date or time cell.
func parseDate(v string) (time.Time, bool) {
	for _, l := range dateLayouts {
		if t, err := time.Parse(l, v); err == nil {
			return t, true
		}
	}
	return time.Time{}, false
}

// dates reports whether nine in ten filled cells, and at least one, are
// dates.
func dates(cells []string) bool {
	n, ok := 0, 0
	for _, v := range cells {
		if v == "" {
			continue
		}
		if n++; n > 200 {
			break
		}
		if _, d := parseDate(v); d {
			ok++
		}
	}
	return n > 0 && ok*10 >= n*9
}

// cadence is how far apart a series' steps are.
type cadence struct {
	name    string // "days", for "days ahead"
	season  int    // steps in a cycle
	horizon int    // steps to forecast
	slot    func(t time.Time) int
}

var cadences = []struct {
	every time.Duration
	cadence
}{
	{time.Hour, cadence{name: "hours", season: 24, horizon: 48, slot: func(t time.Time) int { return int(t.Unix() / 3600) }}},
	{24 * time.Hour, cadence{name: "days", season: 7, horizon: 30, slot: func(t time.Time) int { return int(t.Unix() / 86400) }}},
	{7 * 24 * time.Hour, cadence{name: "weeks", season: 52, horizon: 13, slot: func(t time.Time) int { return int(t.Unix() / (7 * 86400)) }}},
	{30 * 24 * time.Hour, cadence{name: "months", season: 12, horizon: 12, slot: func(t time.Time) int { return t.Year()*12 + int(t.Month()) }}},
}

// cadenceOf returns the cadence of times: the one nearest their usual
// gap.
func cadenceOf(times []time.Time) cadence {
	ts := slices.Clone(times)
	slices.SortFunc(ts, func(a, b time.Time) int { return a.Compare(b) })
	var gaps []float64
	for i := 1; i < len(ts); i++ {
		if d := ts[i].Sub(ts[i-1]); d > 0 {
			gaps = append(gaps, d.Hours())
		}
	}
	if len(gaps) == 0 {
		return cadences[1].cadence
	}
	slices.Sort(gaps)
	median := gaps[len(gaps)/2]
	best, far := 0, math.Inf(1)
	for i, c := range cadences {
		if d := math.Abs(math.Log(median / c.every.Hours())); d < far {
			best, far = i, d
		}
	}
	return cadences[best].cadence
}

var seriesWords = regexp.MustCompile(`store|shop|branch|series|product|item|sku|region|location|site|city|country|machine|sensor|device|account|customer|channel|category`)

// forecaster learns the values to come from the values so far with
// boosted trees over recent values. The answer column is the value; a
// date column says when, and a category column, when there is one and
// dates repeat, which series each row is of.
var forecaster = &Learner{
	Cells: func(ctx context.Context, c Cells, progress func(int, int)) (Learned, error) {
		value := slices.Index(c.Header, c.Target)
		if value < 0 {
			return Learned{}, cellsError("there is no column called %q", c.Target)
		}
		if !numbers(c.column(value)) {
			return Learned{}, cellsError("the %q column needs a number in every row to forecast", c.Target)
		}
		var others []int
		for j := range c.Header {
			if j != value {
				others = append(others, j)
			}
		}
		when := -1
		for _, j := range others {
			if dates(c.column(j)) {
				when = j
				break
			}
		}
		// The series column: needed only when times repeat.
		series := -1
		if when >= 0 && distinct(c.column(when)) < len(c.Rows)*95/100 {
			var cands []int
			for _, j := range others {
				if cells := c.column(j); j != when && !numbers(cells) && !yesNo(cells) {
					cands = append(cands, j)
				}
			}
			series = pick(c.Header, cands, seriesWords)
			if series < 0 && len(cands) > 0 {
				series = cands[0]
			}
		}
		cad := cadence{name: "steps", horizon: 30}
		var times []time.Time
		if when >= 0 {
			for _, v := range c.column(when) {
				t, _ := parseDate(v)
				times = append(times, t)
			}
			cad = cadenceOf(times)
		}
		var names []string
		if series >= 0 {
			names = index(c.column(series))
			slices.Sort(names)
		}
		hist := histories(c, value, when, series, names, times, cad)
		longest := 0
		for _, h := range hist {
			longest = max(longest, len(h))
		}
		if longest < 12 {
			return Learned{}, cellsError("a forecast needs at least 12 steps of history; the longest series has %d", longest)
		}
		season := cad.season
		if longest < 3*season {
			season = 0
		}
		lags := max(4, min(max(2*season, 14), longest/3))
		opts := []forecast.Option{forecast.Lags(lags), forecast.Season(season)}
		fit := func(series [][]float32) (*forecast.Model, error) {
			var long [][]float32
			for _, s := range series {
				if len(s) >= lags+2 {
					long = append(long, s)
				}
			}
			if len(long) == 0 {
				return nil, cellsError("each series needs at least %d steps of history", lags+2)
			}
			return forecast.Fit(ctx, long, opts...)
		}
		// Score it on each series' last steps, learning from the rest,
		// then learn from everything.
		back := min(cad.horizon, longest/5)
		var mae float64
		if back > 0 {
			short := make([][]float32, len(hist))
			for i, h := range hist {
				short[i] = h[:max(len(h)-back, 0)]
			}
			if m, err := fit(short); err == nil {
				var sum float64
				n := 0
				for i, h := range hist {
					if len(short[i]) < lags+2 {
						continue
					}
					for k, v := range m.Forecast(short[i], len(h)-len(short[i])) {
						if want := h[len(short[i])+k]; want == want {
							sum += math.Abs(float64(v - want))
							n++
						}
					}
				}
				if n > 0 {
					mae = sum / float64(n)
				}
			}
		}
		if progress != nil {
			progress(1, 2)
		}
		m, err := fit(hist)
		if err != nil {
			return Learned{}, err
		}
		h := cad.horizon
		table := make([]float32, 0, (len(hist)+1)*h)
		total := make([]float32, h)
		for _, s := range hist {
			f := m.Forecast(s, h)
			for k, v := range f {
				total[k] += v
			}
			table = append(table, f...)
		}
		var cols []schema.Column
		if series >= 0 {
			// A series it hasn't seen, or none named, gets the total.
			table = append(table, total...)
			cols = append(cols, schema.Column{Name: c.Header[series], Kind: schema.Key, Values: names})
		}
		ahead := cad.name + " ahead"
		cols = append(cols, schema.Column{Name: ahead, Kind: schema.Number, Fill: 1})
		rows := len(table) / h
		mod := &Ahead{Table: tensor.NewBuffer(tensor.FromSlice(table, rows*h, 1)), shape: AheadShape{Rows: rows, Steps: h, Keyed: series >= 0}}
		s, err := shapeJSON(mod.shape)
		return Learned{Module: mod, Shape: s, Columns: cols, Labels: []string{c.Target}, MAE: mae}, err
	},
	Build: func(shape string) (nn.Module, error) {
		var s AheadShape
		if err := json.Unmarshal([]byte(shape), &s); err != nil {
			return nil, fmt.Errorf("a forecast's shape: %w", err)
		}
		return NewAhead(s), nil
	},
}

// yesNo reports whether every filled cell is a yes or a no.
func yesNo(cells []string) bool {
	for _, v := range cells {
		switch strings.ToLower(v) {
		case "", "yes", "no", "y", "n", "true", "false", "t", "f":
		default:
			return false
		}
	}
	return true
}

// histories returns each series' values at every step from its first to
// the table's last, NaN where a step has none and summed where it has
// several: the rows in order when there is no date column.
func histories(c Cells, value, when, series int, names []string, times []time.Time, cad cadence) [][]float32 {
	if when < 0 {
		var s []float32
		for _, r := range c.Rows {
			f, ok := schema.ParseNumber(r[value])
			if !ok {
				f = math.NaN()
			}
			s = append(s, float32(f))
		}
		return [][]float32{s}
	}
	of := func(i int) int { return 0 }
	n := 1
	if series >= 0 {
		at := map[string]int{}
		for k, v := range names {
			at[v] = k
		}
		of = func(i int) int {
			if k, ok := at[strings.TrimSpace(c.Rows[i][series])]; ok {
				return k
			}
			return -1
		}
		n = len(names)
	}
	first := make([]int, n)
	for k := range first {
		first[k] = math.MaxInt
	}
	last := math.MinInt
	for i, t := range times {
		if k := of(i); k >= 0 && !t.IsZero() {
			s := cad.slot(t)
			first[k], last = min(first[k], s), max(last, s)
		}
	}
	out := make([][]float32, n)
	for k := range out {
		if first[k] == math.MaxInt {
			continue
		}
		out[k] = make([]float32, last-first[k]+1)
		for i := range out[k] {
			out[k][i] = float32(math.NaN())
		}
	}
	for i, t := range times {
		k := of(i)
		f, ok := schema.ParseNumber(c.Rows[i][value])
		if k < 0 || t.IsZero() || !ok {
			continue
		}
		at := cad.slot(t) - first[k]
		if v := out[k][at]; v != v {
			out[k][at] = float32(f)
		} else {
			out[k][at] = v + float32(f)
		}
	}
	return out
}

// AheadShape is what sizes an Ahead.
type AheadShape struct {
	Rows, Steps int
	// Keyed says the input leads with the row's index; otherwise there
	// is one row.
	Keyed bool
}

// Ahead answers from a table of values worked out when it was taught:
// a forecast's value for a series and a number of steps ahead. Forward
// maps x, shape [batch, 2] (the series' index, the steps ahead) or
// [batch, 1] (the steps ahead) for one row, to [batch, 1]. Steps ahead
// are rounded and kept between 1 and Steps.
type Ahead struct {
	Table *tensor.Param // Float32 [Rows*Steps, 1]
	shape AheadShape
}

// NewAhead returns an Ahead of shape s whose table is zero, for saved
// weights to be loaded into.
func NewAhead(s AheadShape) *Ahead {
	return &Ahead{Table: tensor.NewBuffer(tensor.Zeros(tensor.Float32, max(s.Rows*s.Steps, 1), 1)), shape: s}
}

// Forward looks up each row of x.
func (l *Ahead) Forward(x *tensor.Tensor) *tensor.Tensor {
	if err := x.Err(); err != nil {
		return x
	}
	s := l.shape
	in := 1
	if s.Keyed {
		in = 2
	}
	if x.Rank() != 2 || x.Shape()[1] != in {
		return tensor.Failed(fmt.Errorf("ahead: input %v, want [batch, %d]: %w", x.Shape(), in, tensor.ErrShape))
	}
	n := x.Shape()[0]
	f := func(v float64) *tensor.Tensor { return tensor.Scalar[float32](float32(v)) }
	step := x.Shrink([][2]int{{0, n}, {in - 1, in}})
	// NaN, from an empty cell, reads as the first step.
	step = tensor.Where(step.Equal(step), step, f(1))
	idx := step.Maximum(f(1)).Minimum(f(float64(s.Steps))).Sub(f(0.5))
	if s.Keyed {
		row := x.Shrink([][2]int{{0, n}, {0, 1}}).Maximum(f(0)).Minimum(f(float64(s.Rows - 1)))
		idx = idx.Add(row.Mul(f(float64(s.Steps))))
	}
	return l.Table.Value().Gather(0, idx.Cast(tensor.Int32))
}
