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
	"time"

	"github.com/gsoultan/mimir/automl"
	"github.com/gsoultan/mimir/bayes"
	"github.com/gsoultan/mimir/linear"
	"github.com/gsoultan/mimir/neighbors"
	"github.com/gsoultan/mimir/nn"
	"github.com/gsoultan/mimir/svm"
	"github.com/gsoultan/mimir/tabular"
	"github.com/gsoultan/mimir/tree"

	"github.com/gsoultan/mimir-server/internal/schema"
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
	// FitColumns, when set, is called in place of Fit with the input
	// columns the table's numbers come from, for a learner that treats
	// some of them specially, such as trees reading a category column as
	// categories rather than as separate yes-or-no columns.
	FitColumns func(ctx context.Context, d tabular.Data, task tabular.Task, classes int, cols []schema.Column, progress func(done, total int)) (nn.Module, string, error)
	// Cells, set in place of Fit, learns from a table's cells themselves,
	// for a model whose questions aren't the table's rows, or that has no
	// answer column: it says what its questions name and its answers are.
	Cells func(ctx context.Context, c Cells, progress func(done, total int)) (Learned, error)
	// Boxes, set in place of Fit, learns from photos and the boxes around
	// the things in them, in epochs passes.
	Boxes func(ctx context.Context, b Boxed, epochs int, progress func(done, total int)) (Learned, error)
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

// withColumns returns a Learner that fits as fit does, with the columns
// when they are known and without when they are not.
func withColumns(fit func(ctx context.Context, d tabular.Data, task tabular.Task, classes int, cols []schema.Column, progress func(int, int)) (nn.Module, string, error), build func(string) (nn.Module, error)) *Learner {
	return &Learner{
		Fit: func(ctx context.Context, d tabular.Data, task tabular.Task, classes int, progress func(int, int)) (nn.Module, string, error) {
			return fit(ctx, d, task, classes, nil, progress)
		},
		FitColumns: fit,
		Build:      build,
	}
}

// categories returns the category columns of cols as trees read them:
// each one's run of yes-or-no columns, one per value, split on as one
// column of categories.
func categories(cols []schema.Column) []tree.Category {
	var out []tree.Category
	at := 0
	for _, c := range cols {
		if w := c.Width(); c.Kind == schema.Category && w >= 2 && w <= tree.MaxCategories {
			out = append(out, tree.Category{Feature: at, Width: w})
		}
		at += c.Width()
	}
	return out
}

// categoryOption returns the tree option for cols' category columns, or
// nil when there are none or the columns don't match d.
func categoryOption(cols []schema.Column, d tabular.Data) []tree.Option {
	if cats := categories(cols); len(cats) > 0 && schema.Width(cols) == d.Features {
		return []tree.Option{tree.Categorical(cats...)}
	}
	return nil
}

var boosting = withColumns(
	func(ctx context.Context, d tabular.Data, task tabular.Task, classes int, cols []schema.Column, progress func(int, int)) (nn.Module, string, error) {
		opts := []tree.Option{tree.Trees(boostTrees), tree.LearningRate(0.05), tree.MinLeaf(5)}
		opts = append(opts, categoryOption(cols, d)...)
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
	}, buildTree)

// counts learns a number that is never below 0 and often 0, such as
// orders a week or a claim's cost, with boosted trees on its log: Poisson
// for whole numbers, Tweedie for amounts. A plain number model can
// answer below 0 for such numbers, and is pulled about by the few large
// ones.
var counts = withColumns(
	func(ctx context.Context, d tabular.Data, task tabular.Task, classes int, cols []schema.Column, progress func(int, int)) (nn.Module, string, error) {
		whole, any := true, false
		for _, y := range d.Y {
			if y < 0 {
				return nil, "", fmt.Errorf("a count or amount can't be below 0, and one row has %v", y)
			}
			whole = whole && y == float32(math.Trunc(float64(y)))
			any = any || y > 0
		}
		if !any {
			return nil, "", fmt.Errorf("every row's answer is 0, so there is nothing to learn")
		}
		loss := tree.Tweedie(countPower)
		if whole {
			loss = tree.Poisson()
		}
		opts := []tree.Option{tree.Trees(boostTrees), tree.LearningRate(0.05), tree.MinLeaf(5), tree.Loss(loss)}
		opts = append(opts, categoryOption(cols, d)...)
		if progress != nil {
			opts = append(opts, tree.Progress(progress))
		}
		train := d
		if d.Rows() >= 50 {
			var val tabular.Data
			train, val = d.Split(holdBack, 7)
			opts = append(opts, tree.Validation(val, 50))
		}
		m, err := tree.GradientBoosting(ctx, train, tabular.Regression, opts...)
		if err != nil {
			return nil, "", err
		}
		s, err := shapeJSON(m.Shape())
		return m.Module(), s, err
	}, buildTree)

// countPower is the Tweedie power for amounts: midway between Poisson
// counts and amounts that are never 0, which suits most.
const countPower = 1.5

var forest = withColumns(
	func(ctx context.Context, d tabular.Data, task tabular.Task, classes int, cols []schema.Column, progress func(int, int)) (nn.Module, string, error) {
		opts := []tree.Option{tree.Trees(200), tree.MinLeaf(2)}
		opts = append(opts, categoryOption(cols, d)...)
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
	}, buildTree)

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

// svmRows is the most rows a support vector machine learns from: a
// larger table is sampled down to it, as a kernel machine's fit grows
// with the square of the rows.
const svmRows = 10_000

var kernelMachine = &Learner{
	Fit: func(ctx context.Context, d tabular.Data, task tabular.Task, classes int, progress func(int, int)) (nn.Module, string, error) {
		if d.Rows() > svmRows {
			d, _ = d.Split(1-float64(svmRows)/float64(d.Rows()), 11)
		}
		opts := []svm.Option{svm.C(1)}
		if classes > 0 {
			opts = append(opts, svm.Classes(classes))
		}
		m, err := svm.Fit(ctx, d, task, opts...)
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
		var s svm.Shape
		if err := json.Unmarshal([]byte(shape), &s); err != nil {
			return nil, fmt.Errorf("a support vector machine's shape: %w", err)
		}
		return svm.NewModule(s), nil
	},
}

// neighborRows is the most rows nearest neighbours keep, sampled down
// from a larger table, since every answer compares a row with each.
const neighborRows = 100_000

var nearest = &Learner{
	Fit: func(ctx context.Context, d tabular.Data, task tabular.Task, classes int, progress func(int, int)) (nn.Module, string, error) {
		if d.Rows() > neighborRows {
			d, _ = d.Split(1-float64(neighborRows)/float64(d.Rows()), 13)
		}
		opts := []neighbors.Option{neighbors.K(7), neighbors.ByDistance()}
		if classes > 0 {
			opts = append(opts, neighbors.Classes(classes))
		}
		m, err := neighbors.Fit(ctx, d, task, opts...)
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
		var s neighbors.Shape
		if err := json.Unmarshal([]byte(shape), &s); err != nil {
			return nil, fmt.Errorf("a nearest neighbours model's shape: %w", err)
		}
		return neighbors.NewModule(s), nil
	},
}

var naiveBayes = &Learner{
	Fit: func(ctx context.Context, d tabular.Data, task tabular.Task, classes int, progress func(int, int)) (nn.Module, string, error) {
		if task != tabular.Classification {
			return nil, "", fmt.Errorf("naive Bayes picks a category, not a number")
		}
		m, err := bayes.Gaussian(ctx, d, bayes.Classes(classes))
		if err != nil {
			return nil, "", err
		}
		if progress != nil {
			progress(1, 1)
		}
		s, err := shapeJSON(m.Shape())
		return m.Module(), s, err
	},
	Build: buildBayes,
}

func buildBayes(shape string) (nn.Module, error) {
	var s bayes.Shape
	if err := json.Unmarshal([]byte(shape), &s); err != nil {
		return nil, fmt.Errorf("a naive Bayes model's shape: %w", err)
	}
	return bayes.NewModule(s), nil
}

// autoTrials and autoBudget bound the candidates auto scores.
const (
	autoTrials = 10
	autoBudget = 10 * time.Minute
)

// auto scores boosted trees, random forests, linear models, support
// vector machines, nearest neighbours and naive Bayes over a range of
// settings and keeps the best, refit to the whole table. Its shape names
// which kind won.
var auto = &Learner{
	Fit: func(ctx context.Context, d tabular.Data, task tabular.Task, classes int, progress func(int, int)) (nn.Module, string, error) {
		opts := []automl.Option{automl.Trials(autoTrials), automl.Budget(autoBudget), automl.Stack(autoStack)}
		if classes > 0 {
			opts = append(opts, automl.Classes(classes))
		}
		if progress != nil {
			opts = append(opts, automl.Progress(progress))
		}
		r, err := automl.Search(ctx, d, task, opts...)
		if err != nil {
			return nil, "", err
		}
		mod, shape, err := autoModule(r.Best, classes)
		if err != nil {
			return nil, "", err
		}
		s, err := shapeJSON(shape)
		return mod, s, err
	},
	Build: func(shape string) (nn.Module, error) {
		var s autoShape
		if err := json.Unmarshal([]byte(shape), &s); err != nil {
			return nil, fmt.Errorf("an automatic model's shape: %w", err)
		}
		return buildAuto(s)
	},
}

// autoStack is how many of the best candidates a search may blend.
const autoStack = 3

// autoModule returns m, a model an automatic search kept, as a module and
// its shape.
func autoModule(best tabular.Model, classes int) (nn.Module, autoShape, error) {
	switch m := best.(type) {
	case *tree.Model:
		return m.Module(), autoShape{Kind: "tree", Tree: ptr(m.Shape())}, nil
	case *linear.Model:
		return m.Module(), autoShape{Kind: "linear", Linear: ptr(m.Shape())}, nil
	case *svm.Model:
		return m.Module(), autoShape{Kind: "svm", SVM: ptr(m.Shape())}, nil
	case *neighbors.Model:
		return m.Module(), autoShape{Kind: "knn", Neighbors: ptr(m.Shape())}, nil
	case *bayes.Model:
		return m.Module(), autoShape{Kind: "bayes", Bayes: ptr(m.Shape())}, nil
	case *automl.Blend:
		s := autoShape{Kind: "stack", Classes: classes}
		var parts []nn.Module
		for _, p := range m.Models {
			mod, ps, err := autoModule(p, classes)
			if err != nil {
				return nil, s, err
			}
			parts, s.Parts = append(parts, mod), append(s.Parts, ps)
		}
		mod, err := automl.StackOf(parts, m.Weights, classes)
		return mod, s, err
	}
	return nil, autoShape{}, fmt.Errorf("auto: a model of type %T", best)
}

// buildAuto returns an empty module of shape s, for saved weights to load
// into.
func buildAuto(s autoShape) (nn.Module, error) {
	switch {
	case s.Kind == "tree" && s.Tree != nil:
		return tree.NewModule(*s.Tree), nil
	case s.Kind == "linear" && s.Linear != nil:
		return linear.NewModule(*s.Linear), nil
	case s.Kind == "svm" && s.SVM != nil:
		return svm.NewModule(*s.SVM), nil
	case s.Kind == "knn" && s.Neighbors != nil:
		return neighbors.NewModule(*s.Neighbors), nil
	case s.Kind == "bayes" && s.Bayes != nil:
		return bayes.NewModule(*s.Bayes), nil
	case s.Kind == "stack" && len(s.Parts) > 0:
		parts := make([]nn.Module, len(s.Parts))
		for i, p := range s.Parts {
			if p.Kind == "stack" {
				return nil, fmt.Errorf("an automatic blend within a blend")
			}
			var err error
			if parts[i], err = buildAuto(p); err != nil {
				return nil, err
			}
		}
		return automl.NewStackModule(parts, s.Classes), nil
	}
	return nil, fmt.Errorf("an automatic model of kind %q", s.Kind)
}

type autoShape struct {
	Kind      string           `json:"kind"` // "tree", "linear", "svm", "knn", "bayes" or "stack"
	Tree      *tree.Shape      `json:"tree,omitempty"`
	Linear    *linear.Shape    `json:"linear,omitempty"`
	SVM       *svm.Shape       `json:"svm,omitempty"`
	Neighbors *neighbors.Shape `json:"knn,omitempty"`
	Bayes     *bayes.Shape     `json:"bayes,omitempty"`
	// Parts, for a blend of the best few, are each part's shape, and
	// Classes the categories it picks between, 0 for a number.
	Parts   []autoShape `json:"parts,omitempty"`
	Classes int         `json:"classes,omitempty"`
}

func ptr[T any](v T) *T { return &v }

// learned are the architectures of Learners, for tables: each answers a
// category or a number.
var learned = []Architecture{
	{
		Name:        "table-auto",
		Title:       "Best of several (automatic)",
		Description: "Tries boosted trees, random forests, logistic regression, a support vector machine, nearest neighbours and naive Bayes with a range of settings, scores each on rows it didn't learn from, and keeps the best, or a mix of the best three when the mix does better. Takes longer than one of them alone.",
		Input:       Table, AnyText: true, Learner: auto,
	},
	{
		Name:        "number-auto",
		Title:       "Best of several for a number (automatic)",
		Description: "Tries boosted trees, random forests, linear regression, a support vector machine and nearest neighbours with a range of settings, scores each on rows it didn't learn from, and keeps the best, or a mix of the best three when the mix does better. Takes longer than one of them alone.",
		Input:       Table, AnyText: true, Output: Number, Learner: auto,
	},
	{
		Name:        "number-counts",
		Title:       "Counts and amounts with many zeros",
		Description: "Boosted trees for a number that is never below 0 and is often exactly 0, like orders a week, visits a month or the cost of a claim. It never answers below 0, and a few very large values don't pull the other answers about.",
		Input:       Table, AnyText: true, Output: Number, Learner: counts,
	},
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
	{
		Name:        "table-svm",
		Title:       "Support vector machine",
		Description: "Draws the widest possible border between the categories, bending around them where it must. Strong on small and medium tables with numbers in many columns; it learns from up to 10,000 rows.",
		Input:       Table, AnyText: true, Learner: kernelMachine,
	},
	{
		Name:        "number-svm",
		Title:       "Support vector machine for a number",
		Description: "Fits a smooth curve through the rows that ignores small errors, answering with a number. Strong on small and medium tables; it learns from up to 10,000 rows.",
		Input:       Table, AnyText: true, Output: Number, Learner: kernelMachine,
	},
	{
		Name:        "table-knn",
		Title:       "Nearest neighbours",
		Description: "Answers like the 7 rows most similar to the question, the nearer ones counting more. Nothing to teach, so it is ready at once, and you can see why it answered as it did.",
		Input:       Table, AnyText: true, Learner: nearest,
	},
	{
		Name:        "number-knn",
		Title:       "Nearest neighbours for a number",
		Description: "Answers with the average of the 7 rows most similar to the question, the nearer ones counting more. Nothing to teach, so it is ready at once.",
		Input:       Table, AnyText: true, Output: Number, Learner: nearest,
	},
	{
		Name:        "table-bayes",
		Title:       "Naive Bayes",
		Description: "Learns how each column is spread within each category and combines them, as if the columns were independent. Taught in one pass and good with very few rows, though rarely the most accurate.",
		Input:       Table, AnyText: true, Learner: naiveBayes,
	},
	{
		Name:        "find-groups",
		Title:       "Find groups",
		Description: "Sorts the rows into a few groups of similar ones, like kinds of customers, with no answers to learn from. It picks how many groups fit best, from 2 to 8, and names each after what sets it apart.",
		Input:       Table, AnyText: true, Target: NoTarget, Learner: groups,
	},
	{
		Name:        "odd-one",
		Title:       "Spot the odd one",
		Description: "Learns what usual rows look like, with no answers to learn from, then scores how unusual a new one is from 0 to 1: about 0.5 or less is usual, and near 1 is unlike anything it has seen.",
		Input:       Table, AnyText: true, Output: Number, Target: NoTarget, Learner: oddOne,
	},
	{
		Name:        "recommend",
		Title:       "Recommend",
		Description: "Learns from who picked which item, such as purchases, clicks or ratings, and lists the items each person is likeliest to want. Someone it hasn't seen gets the items most people like.",
		Input:       Table, AnyText: true, Output: Ranked, Items: recommendTop, Target: ItemTarget, Learner: recommender,
	},
	{
		Name:        "goes-together",
		Title:       "What goes together",
		Description: "Learns from which items were in the same orders, and lists for any item the ones bought with it far more often than chance, best first: what to offer next to a basket, or place side by side. An item it hasn't seen gets the items most orders hold.",
		Input:       Table, AnyText: true, Output: Ranked, Items: recommendTop, Target: ItemTarget, Learner: together,
	},
	{
		Name:        "recommend-nearby",
		Title:       "Recommend from nearby customers",
		Description: "Learns which products customers buy, where they are and what kind of business they are, and lists what customers of the same kind within a distance buy that this one doesn't yet. Ask with a customer, or a place and a category for a lead, and how many kilometres to look.",
		Input:       Table, AnyText: true, Output: Ranked, Items: recommendTop, Target: ItemTarget, Learner: nearbyRecommender,
	},
	{
		Name:        "best-action",
		Title:       "Pick the best action",
		Description: "Learns from a log of what was done in each situation and how it turned out, such as which offer each customer saw and whether they bought, and says which action should turn out best in a new situation, with the result each would likely bring.",
		Input:       Table, AnyText: true, Output: Ranked, Items: decideTop, Target: ActionTarget, Learner: decider,
	},
	{
		Name:        "time-to-event",
		Title:       "Time until it happens",
		Description: "Learns how soon something happens, such as a customer's next order or its leaving, from a yes or no answer and a column of how many days each row was watched, counting the rows where it hasn't happened yet. Says how likely it is within any number of days you ask. It is scored by how often it puts which of two rows happens first in the right order: 0.5 is guessing and 1 is always right.",
		Input:       Table, AnyText: true, Learner: survival,
	},
	{
		Name:        "look-alike",
		Title:       "Look-alike leads",
		Description: "Learns what sets your best customers apart, from a yes or no column that marks them, and scores any customer or lead from 0 to 100 by how alike it is: 50 is as alike as a typical best customer, 100 as alike as the closest. Score a list of leads to visit the most alike first. It is scored by how often a best customer it didn't learn from scores above another: 0.5 is guessing and 1 is always right.",
		Input:       Table, AnyText: true, Output: Several, Learner: lookAlike,
	},
	{
		Name:        "multi-label",
		Title:       "Several answers at once",
		Description: "Learns from a column that lists each row's answers, like snacks; drinks; dairy, the chance of each one apart from the others: every product line a customer will likely buy this quarter, say. It is scored by how well it ranks the rows that have each answer above those that don't: 0.5 is guessing and 1 is always right.",
		Input:       Table, AnyText: true, Output: Several, Learner: multiLabel,
	},
	{
		Name:        "discount-response",
		Title:       "Chance of closing at each discount",
		Description: "Learns from past deals, their discount and whether each closed, how the chance of closing moves with the discount, and gives it for every discount level, with the discount that keeps the most revenue. It learns from discounts given in the past, so it shows how deals like this went, not a controlled test. It is scored by how well it ranks deals that closed above those that didn't: 0.5 is guessing and 1 is always right.",
		Input:       Table, AnyText: true, Output: Several, Learner: discountResponse,
	},
	{
		Name:        "number-range",
		Title:       "A range for a number",
		Description: "Answers a number with a range it likely falls in, like 40 to 70 units, most likely 55, and the range is wider where the data is less sure. It aims for 8 in 10 real values inside the range, and is scored by the share of rows it didn't learn from that were.",
		Input:       Table, AnyText: true, Output: Several, NumberTarget: true, Learner: numberRange,
	},
	{
		Name:        "find-duplicates",
		Title:       "Find duplicate accounts",
		Description: "Reads a table of accounts, each with an id and a name, and finds for any account or new lead the ones that are likely the same business written another way, like PT Maju Jaya and Maju Jaya, PT. It ignores case, word order, punctuation and legal forms, counts rare words more than common ones, and compares addresses, phones and emails too when there are columns for them.",
		Input:       Table, AnyText: true, Output: Ranked, Items: recommendTop, Target: NoTarget, Learner: duplicates,
	},
	{
		Name:        "forecast",
		Title:       "Forecast over time",
		Description: "Learns from a value's history, one row per day, hour, week or month, and says what comes next, up to a month of days ahead. With a column like store, it forecasts each one, and their total. Something that sells only now and then, like a spare part, gets a steady amount per day to plan stock for.",
		Input:       Table, AnyText: true, Output: Number, Target: ValueTarget, Learner: forecaster,
	},
}
