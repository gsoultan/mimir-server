package arch

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/gsoultan/mimir/automl"
	"github.com/gsoultan/mimir/bayes"
	"github.com/gsoultan/mimir/linear"
	"github.com/gsoultan/mimir/neighbors"
	"github.com/gsoultan/mimir/nn"
	"github.com/gsoultan/mimir/svm"
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
	// Cells, set in place of Fit, learns from a table's cells themselves,
	// for a model whose questions aren't the table's rows, or that has no
	// answer column: it says what its questions name and its answers are.
	Cells func(ctx context.Context, c Cells, progress func(done, total int)) (Learned, error)
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
		opts := []automl.Option{automl.Trials(autoTrials), automl.Budget(autoBudget)}
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
		var mod nn.Module
		var shape any
		switch m := r.Best.(type) {
		case *tree.Model:
			mod, shape = m.Module(), autoShape{Kind: "tree", Tree: ptr(m.Shape())}
		case *linear.Model:
			mod, shape = m.Module(), autoShape{Kind: "linear", Linear: ptr(m.Shape())}
		case *svm.Model:
			mod, shape = m.Module(), autoShape{Kind: "svm", SVM: ptr(m.Shape())}
		case *neighbors.Model:
			mod, shape = m.Module(), autoShape{Kind: "knn", Neighbors: ptr(m.Shape())}
		case *bayes.Model:
			mod, shape = m.Module(), autoShape{Kind: "bayes", Bayes: ptr(m.Shape())}
		default:
			return nil, "", fmt.Errorf("auto: a model of type %T", r.Best)
		}
		s, err := shapeJSON(shape)
		return mod, s, err
	},
	Build: func(shape string) (nn.Module, error) {
		var s autoShape
		if err := json.Unmarshal([]byte(shape), &s); err != nil {
			return nil, fmt.Errorf("an automatic model's shape: %w", err)
		}
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
		}
		return nil, fmt.Errorf("an automatic model of kind %q", s.Kind)
	},
}

// autoShape is the shape of the model auto picked.
type autoShape struct {
	Kind      string           `json:"kind"` // "tree", "linear", "svm", "knn" or "bayes"
	Tree      *tree.Shape      `json:"tree,omitempty"`
	Linear    *linear.Shape    `json:"linear,omitempty"`
	SVM       *svm.Shape       `json:"svm,omitempty"`
	Neighbors *neighbors.Shape `json:"knn,omitempty"`
	Bayes     *bayes.Shape     `json:"bayes,omitempty"`
}

func ptr[T any](v T) *T { return &v }

// learned are the architectures of Learners, for tables: each answers a
// category or a number.
var learned = []Architecture{
	{
		Name:        "table-auto",
		Title:       "Best of several (automatic)",
		Description: "Tries boosted trees, random forests, logistic regression, a support vector machine, nearest neighbours and naive Bayes with a range of settings, scores each on rows it didn't learn from, and keeps the best. Takes longer than one of them alone.",
		Input:       Table, AnyText: true, Learner: auto,
	},
	{
		Name:        "number-auto",
		Title:       "Best of several for a number (automatic)",
		Description: "Tries boosted trees, random forests, linear regression, a support vector machine and nearest neighbours with a range of settings, scores each on rows it didn't learn from, and keeps the best. Takes longer than one of them alone.",
		Input:       Table, AnyText: true, Output: Number, Learner: auto,
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
		Name:        "forecast",
		Title:       "Forecast over time",
		Description: "Learns from a value's history, one row per day, hour, week or month, and says what comes next, up to a month of days ahead. With a column like store, it forecasts each one, and their total.",
		Input:       Table, AnyText: true, Output: Number, Target: ValueTarget, Learner: forecaster,
	},
}
