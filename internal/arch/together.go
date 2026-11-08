package arch

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"sort"
	"strings"

	"github.com/gsoultan/mimir/nn"
	"github.com/gsoultan/mimir/rules"
	"github.com/gsoultan/mimir/tensor"

	"github.com/gsoultan/mimir-server/internal/schema"
)

// maxTogetherItems is the most items a goes-together model keeps, the
// most often bought: its table has a score for every pair.
const maxTogetherItems = 2000

// TogetherShape sizes a Together.
type TogetherShape struct{ Items int }

// Together answers, for an item, how strongly every other item goes
// with it in the same orders, from a table worked out when it was
// taught. Forward maps x, shape [batch, 1], each an item's index, to
// [batch, Items] scores; the index Items, an item it hasn't seen, gets
// the items most orders hold.
type Together struct {
	Table *tensor.Param // Float32 [Items+1, Items]
	shape TogetherShape
}

// NewTogether returns a Together of shape s whose table is zero, for
// saved weights to be loaded into.
func NewTogether(s TogetherShape) *Together {
	n := max(s.Items, 1)
	return &Together{Table: tensor.NewBuffer(tensor.Zeros(tensor.Float32, n+1, n)), shape: s}
}

// Shape returns the shape m was built with.
func (m *Together) Shape() TogetherShape { return m.shape }

// Forward looks up each item's row of scores.
func (m *Together) Forward(x *tensor.Tensor) *tensor.Tensor {
	if err := x.Err(); err != nil {
		return x
	}
	if x.Rank() != 2 || x.Shape()[1] != 1 {
		return tensor.Failed(fmt.Errorf("together: input %v, want [batch, 1] item indices: %w", x.Shape(), tensor.ErrShape))
	}
	n, items := x.Shape()[0], m.shape.Items
	// NaN, from an empty cell, reads as an unseen item.
	idx := tensor.Where(x.Equal(x), x, scalar(float64(items))).Maximum(scalar(0)).Minimum(scalar(float64(items)))
	return m.Table.Value().Gather(0, idx.Cast(tensor.Int32).Expand(n, items))
}

var orderWords = regexp.MustCompile(`order|invoice|transaction|receipt|basket|cart|ticket|session|visit|trx|nota|faktur|bill|checkout`)

// together learns which items are bought in the same orders and lists,
// for an item, the ones that go with it most, by association rules.
var together = &Learner{
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
		// The order each item belongs to, or failing that the customer,
		// whose purchases then make one basket.
		order := pick(c.Header, others, orderWords)
		if order < 0 {
			order = pick(c.Header, others, whoWords)
		}
		if order < 0 {
			for _, j := range others {
				cells := c.column(j)
				if !numbers(cells) && !dates(cells) && distinct(cells) < len(cells) {
					order = j
					break
				}
			}
		}
		if order < 0 {
			return Learned{}, cellsError("no column says which order each item was in; name one order_id, invoice or customer")
		}
		items := index(c.column(item))
		if len(items) > maxTogetherItems {
			items = items[:maxTogetherItems]
		}
		if len(items) < 2 {
			return Learned{}, cellsError("the %q column names only one item, so nothing goes with anything", c.Target)
		}
		iid := map[string]int{}
		for i, v := range items {
			iid[v] = i
		}
		byOrder := map[string][]string{}
		var keys []string
		for _, r := range c.Rows {
			o, v := strings.TrimSpace(r[order]), strings.TrimSpace(r[item])
			if o == "" || v == "" {
				continue
			}
			if _, ok := iid[v]; !ok {
				continue
			}
			if _, seen := byOrder[o]; !seen {
				keys = append(keys, o)
			}
			byOrder[o] = append(byOrder[o], v)
		}
		baskets := make([][]string, len(keys))
		for i, k := range keys {
			baskets[i] = byOrder[k]
		}
		if len(baskets) < 2 {
			return Learned{}, cellsError("fewer than 2 orders hold an item")
		}
		// Score it by hiding the last item of each order of two or more
		// and asking with the first, then learn from everything.
		var train [][]string
		var held [][2]string
		for _, b := range baskets {
			if len(b) >= 2 && b[0] != b[len(b)-1] {
				held = append(held, [2]string{b[0], b[len(b)-1]})
				train = append(train, b[:len(b)-1])
			} else {
				train = append(train, b)
			}
		}
		hit := 0.0
		if len(held) > 0 {
			table, err := togetherTable(ctx, train, items, iid)
			if err != nil {
				return Learned{}, err
			}
			hits := 0
			for _, h := range held {
				row := table[iid[h[0]]*len(items) : (iid[h[0]]+1)*len(items)]
				top := make([]int, len(items))
				for i := range top {
					top[i] = i
				}
				sort.SliceStable(top, func(a, b int) bool { return row[top[a]] > row[top[b]] })
				if slices.Contains(top[:min(recommendTop, len(top))], iid[h[1]]) {
					hits++
				}
			}
			hit = float64(hits) / float64(len(held))
		}
		if progress != nil {
			progress(1, 2)
		}
		table, err := togetherTable(ctx, baskets, items, iid)
		if err != nil {
			return Learned{}, err
		}
		shape := TogetherShape{Items: len(items)}
		m := &Together{Table: buffer(table, len(items)+1, len(items)), shape: shape}
		s, err := shapeJSON(shape)
		cols := []schema.Column{{Name: c.Header[item], Kind: schema.Key, Values: items}}
		return Learned{Module: m, Shape: s, Columns: cols, Labels: items, Accuracy: hit}, err
	},
	Build: func(shape string) (nn.Module, error) {
		var s TogetherShape
		if err := json.Unmarshal([]byte(shape), &s); err != nil {
			return nil, fmt.Errorf("a goes-together model's shape: %w", err)
		}
		return NewTogether(s), nil
	},
}

// togetherTable returns, row by row, each item's score for every other
// item: the lift of the rule "orders with this hold that" when enough
// orders hold both, and a last row, for an item it hasn't seen, of the
// share of orders holding each item. An item never scores itself.
func togetherTable(ctx context.Context, baskets [][]string, items []string, iid map[string]int) ([]float32, error) {
	n := len(items)
	table := make([]float32, (n+1)*n)
	for i := range n {
		table[i*n+i] = -1e4
	}
	count := make([]int, n)
	for _, b := range baskets {
		for _, v := range slices.Compact(slices.Sorted(slices.Values(b))) {
			count[iid[v]]++
		}
	}
	for i, k := range count {
		table[n*n+i] = float32(k) / float32(len(baskets))
	}
	// A pair in at least 3 orders, and 1 in 500, says something.
	support := max(0.002, 3/float64(len(baskets)))
	found, err := rules.Mine(ctx, baskets, rules.MinSupport(support), rules.MinConfidence(0), rules.MaxItems(2))
	if err != nil {
		return nil, err
	}
	for _, r := range found {
		a, b := iid[r.If[0]], iid[r.Then]
		// Lift says how much likelier, confidence breaks ties.
		table[a*n+b] = float32(r.Lift + r.Confidence/1e3)
	}
	return table, nil
}
