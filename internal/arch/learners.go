package arch

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/gsoultan/mimir/linear"
	"github.com/gsoultan/mimir/nn"
	"github.com/gsoultan/mimir/tabular"
	"github.com/gsoultan/mimir/tree"
)

// ShapeKey is the metadata key of a Mimir Model file that holds a learned
// model's shape, as JSON, so an empty module of that shape can take its
// weights.
const ShapeKey = "mimir.shape"

// A Learner fits a model to a whole table at once, as trees and linear
// models do, rather than training a network by gradient descent.
type Learner struct {
	// Fit learns from d a model answering task with classes classes (0
	// for a number). It returns the model as a module and its shape for
	// Build. progress, when not nil, is told how far it is.
	Fit func(ctx context.Context, d tabular.Data, task tabular.Task, classes int, progress func(done, total int)) (nn.Module, string, error)
	// Build returns an empty module of the shape Fit returned, for its
	// saved weights to load into.
	Build func(shape string) (nn.Module, error)
}

// Task returns what a model of a answers, as package tabular names it.
func (a Architecture) Task() tabular.Task {
	if a.Answers() == Number {
		return tabular.Regression
	}
	return tabular.Classification
}

// holdBack is the share of a table boosting keeps aside to know when
// more trees stop helping.
const holdBack = 0.1

// boostTrees is the most trees boosting grows; it stops sooner once
// the rows held back stop improving.
const boostTrees = 1000

func shapeJSON(v any) (string, error) {
	b, err := json.Marshal(v)
	return string(b), err
}

var boosting = &Learner{
	Fit: func(ctx context.Context, d tabular.Data, task tabular.Task, classes int, progress func(int, int)) (nn.Module, string, error) {
		opts := []tree.Option{tree.Trees(boostTrees), tree.LearningRate(0.05), tree.MinLeaf(5)}
		if classes > 0 {
			opts = append(opts, tree.Classes(classes))
		}
		if progress != nil {
			opts = append(opts, tree.Progress(progress))
		}
		train := d
		if d.Rows() >= 50 {
			var val tabular.Data
			train, val = d.Split(holdBack, 7)
			opts = append(opts, tree.Validation(val, 50))
		}
		m, err := tree.GradientBoosting(ctx, train, task, opts...)
		if err != nil {
			return nil, "", err
		}
		s, err := shapeJSON(m.Shape())
		return m.Module(), s, err
	},
	Build: buildTree,
}

var forest = &Learner{
	Fit: func(ctx context.Context, d tabular.Data, task tabular.Task, classes int, progress func(int, int)) (nn.Module, string, error) {
		opts := []tree.Option{tree.Trees(200), tree.MinLeaf(2)}
		if classes > 0 {
			opts = append(opts, tree.Classes(classes))
		}
		if progress != nil {
			opts = append(opts, tree.Progress(progress))
		}
		m, err := tree.RandomForest(ctx, d, task, opts...)
		if err != nil {
			return nil, "", err
		}
		s, err := shapeJSON(m.Shape())
		return m.Module(), s, err
	},
	Build: buildTree,
}

func buildTree(shape string) (nn.Module, error) {
	var s tree.Shape
	if err := json.Unmarshal([]byte(shape), &s); err != nil {
		return nil, fmt.Errorf("a tree model's shape: %w", err)
	}
	return tree.NewModule(s), nil
}

var straight = &Learner{
	Fit: func(ctx context.Context, d tabular.Data, task tabular.Task, classes int, progress func(int, int)) (nn.Module, string, error) {
		var m *linear.Model
		var err error
		if task == tabular.Regression {
			m, err = linear.Regression(ctx, d, linear.L2(1e-4))
		} else {
			m, err = linear.Logistic(ctx, d, linear.Classes(classes))
		}
		if err != nil {
			return nil, "", err
		}
		if progress != nil {
			progress(1, 1)
		}
		s, err := shapeJSON(m.Shape())
		return m.Module(), s, err
	},
	Build: func(shape string) (nn.Module, error) {
		var s linear.Shape
		if err := json.Unmarshal([]byte(shape), &s); err != nil {
			return nil, fmt.Errorf("a linear model's shape: %w", err)
		}
		return linear.NewModule(s), nil
	},
}

// learned are the architectures of Learners, for tables: each answers a
// category or a number.
var learned = []Architecture{
	{
		Name:        "table-boost",
		Title:       "Boosted trees",
		Description: "Hundreds of small decision trees, each fixing the mistakes of the ones before, pick a category. Usually the most accurate choice for a table, and quick to teach.",
		Input:       Table, AnyText: true, Learner: boosting,
	},
	{
		Name:        "number-boost",
		Title:       "Boosted trees for a number",
		Description: "Hundreds of small decision trees, each fixing the mistakes of the ones before, answer with a number. Usually the most accurate choice for a table.",
		Input:       Table, AnyText: true, Output: Number, Learner: boosting,
	},
	{
		Name:        "table-forest",
		Title:       "Random forest",
		Description: "Many decision trees, each taught from a random part of the table, vote on a category. Hard to get wrong, even on small or messy tables.",
		Input:       Table, AnyText: true, Learner: forest,
	},
	{
		Name:        "number-forest",
		Title:       "Random forest for a number",
		Description: "Many decision trees, each taught from a random part of the table, average their answers into a number. Hard to get wrong, even on small or messy tables.",
		Input:       Table, AnyText: true, Output: Number, Learner: forest,
	},
	{
		Name:        "table-linear",
		Title:       "Logistic regression",
		Description: "Weighs each column up or down and adds them up to pick a category. The simplest model, taught in a moment, and each column's weight says how much it matters.",
		Input:       Table, AnyText: true, Learner: straight,
	},
	{
		Name:        "number-linear",
		Title:       "Linear regression",
		Description: "Weighs each column up or down and adds them up into a number. The simplest model, taught in a moment, and each column's weight says how much it matters.",
		Input:       Table, AnyText: true, Output: Number, Learner: straight,
	},
}
