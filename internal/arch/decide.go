package arch

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"slices"
	"strings"

	"github.com/gsoultan/mimir/nn"
	"github.com/gsoultan/mimir/tabular"
	"github.com/gsoultan/mimir/tensor"
	"github.com/gsoultan/mimir/tree"

	"github.com/gsoultan/mimir-server/internal/schema"
)

// ActionTarget is the action taken, such as the offer shown or the
// treatment given; another column says how well it turned out.
const ActionTarget Target = "action"

// rewardWords name a column that says how well an action turned out.
var rewardWords = regexp.MustCompile(`reward|revenue|profit|return|gain|converted|conversion|bought|purchased|clicked|click|accepted|success|outcome|result|won|sales|spend|value|score`)

// decider learns, from a log of what was done in each situation and how
// it turned out, which action turns out best in a new one: a contextual
// bandit, by the direct method. One boosted-tree model learns the reward
// of each situation and action together, and the model scores every
// action for a question, the best first.
var decider = &Learner{
	Cells: func(ctx context.Context, c Cells, progress func(int, int)) (Learned, error) {
		act := slices.Index(c.Header, c.Target)
		if act < 0 {
			return Learned{}, cellsError("there is no column called %q", c.Target)
		}
		var others []int
		for j := range c.Header {
			if j != act {
				others = append(others, j)
			}
		}
		scored := func(j int) bool { cells := c.column(j); return numbers(cells) || yesNo(cells) }
		reward := -1
		for _, j := range others {
			if rewardWords.MatchString(strings.ToLower(c.Header[j])) && scored(j) {
				reward = j
				break
			}
		}
		if reward < 0 {
			return Learned{}, cellsError("no column says how each action turned out; name one reward, revenue, converted or outcome, holding numbers or yes and no")
		}
		var situation []int
		for _, j := range others {
			if j != reward {
				situation = append(situation, j)
			}
		}
		// Rows with an action and a reward.
		var keep [][]string
		var rewards []float32
		for _, r := range c.Rows {
			a, v := strings.TrimSpace(r[act]), strings.TrimSpace(r[reward])
			y, ok := schema.ParseNumber(v)
			if !ok {
				switch strings.ToLower(v) {
				case "yes", "y", "true", "t":
					y, ok = 1, true
				case "no", "n", "false", "f":
					y, ok = 0, true
				}
			}
			if a == "" || !ok {
				continue
			}
			keep = append(keep, r)
			rewards = append(rewards, float32(y))
		}
		sub := Cells{Header: c.Header, Rows: keep, Kinds: c.Kinds}
		actions := index(sub.column(act))
		if len(actions) < 2 {
			return Learned{}, cellsError("the %q column names only one action, so there is nothing to choose between", c.Target)
		}
		if len(actions) > MaxActions {
			return Learned{}, cellsError("the %q column names %d actions; at most %d can be compared", c.Target, len(actions), MaxActions)
		}
		if len(keep) < 20 {
			return Learned{}, cellsError("only %d rows have both an action and how it turned out; at least 20 are needed", len(keep))
		}
		id := map[string]int{}
		for i, a := range actions {
			id[a] = i
		}
		var cols []schema.Column
		var x []float32
		if len(situation) > 0 {
			cols, x = sub.encode(situation)
		}
		f := schema.Width(cols)
		k := len(actions)
		d := tabular.Data{Features: f + k, X: make([]float32, 0, len(keep)*(f+k)), Y: rewards}
		for i, r := range keep {
			d.X = append(d.X, x[i*f:(i+1)*f]...)
			for a := range k {
				v := float32(0)
				if id[strings.TrimSpace(r[act])] == a {
					v = 1
				}
				d.X = append(d.X, v)
			}
		}
		train, held := d.Split(0.2, 17)
		opts := []tree.Option{tree.Trees(boostTrees), tree.LearningRate(0.05), tree.MinLeaf(5), tree.Validation(held, 50)}
		if progress != nil {
			opts = append(opts, tree.Progress(progress))
		}
		m, err := tree.GradientBoosting(ctx, train, tabular.Regression, opts...)
		if err != nil {
			return Learned{}, err
		}
		var mae float64
		for i, p := range m.Predict(held.X) {
			mae += math.Abs(float64(p - held.Y[i]))
		}
		mae /= float64(max(held.Rows(), 1))
		shape := DecideShape{Tree: m.Shape(), Features: f, Actions: k}
		s, err := shapeJSON(shape)
		return Learned{Module: &Decide{Trees: m.Module(), shape: shape}, Shape: s, Columns: cols, Labels: actions, MAE: mae}, err
	},
	Build: func(shape string) (nn.Module, error) {
		var s DecideShape
		if err := json.Unmarshal([]byte(shape), &s); err != nil {
			return nil, fmt.Errorf("a decision model's shape: %w", err)
		}
		return &Decide{Trees: tree.NewModule(s.Tree), shape: s}, nil
	},
}

// MaxActions is the most actions a decision model compares, and
// decideTop how many an answer lists.
const (
	MaxActions = 100
	decideTop  = 5
)

// DecideShape sizes a Decide.
type DecideShape struct {
	Tree     tree.Shape `json:"tree"`
	Features int        `json:"features"` // the situation's numbers
	Actions  int        `json:"actions"`
}

// Decide scores every action of a situation: Forward maps x, [batch,
// Features], to [batch, Actions], the reward its trees expect of each
// action there.
type Decide struct {
	Trees *tree.Module
	shape DecideShape
}

// Forward asks the trees about each action in turn.
func (d *Decide) Forward(x *tensor.Tensor) *tensor.Tensor {
	if err := x.Err(); err != nil {
		return x
	}
	s := d.shape
	if x.Rank() != 2 || x.Shape()[1] != s.Features {
		return tensor.Failed(fmt.Errorf("arch: decision of %v for situations of %d numbers: %w", x.Shape(), s.Features, tensor.ErrShape))
	}
	b := x.Shape()[0]
	outs := make([]*tensor.Tensor, s.Actions)
	for a := range s.Actions {
		hot := make([]float32, s.Actions)
		hot[a] = 1
		one := tensor.FromSlice(hot, 1, s.Actions).Expand(b, s.Actions)
		in := one
		if s.Features > 0 {
			in = tensor.Cat(1, x, one)
		}
		outs[a] = d.Trees.Forward(in)
	}
	return tensor.Cat(1, outs...)
}
