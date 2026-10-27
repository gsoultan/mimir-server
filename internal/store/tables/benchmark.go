package tables

import "github.com/gsoultan/storm"

// Benchmark is a fixed set of test examples, frozen from a table dataset,
// that every model of that kind is scored on the same way. The rows are a
// CSV file in the data folder (store.BenchmarkPath); training on the
// dataset leaves them out, so no model learns from its own test.
//
// DatasetID is a plain-text reference, not a key: the frozen rows are a
// copy, so the benchmark outlives the dataset it came from.
type Benchmark struct {
	Record
	Named
	TeamOwned
	Description string
	DatasetID   string
	// Target is the answer column; Kind what a model answers: "class",
	// "number" or "ranked".
	Target    string
	Kind      string
	Rows      int64
	CreatedBy string
}

func (b *Benchmark) Schema(t *storm.Table) {
	t.Col(&b.Description).Default("''")
	t.Col(&b.DatasetID).Immutable()
	t.Col(&b.Target).Immutable()
	t.Col(&b.Kind).Immutable()
	t.Col(&b.Rows).Immutable()
	t.Col(&b.CreatedBy).Default("''").Immutable()
	// A team's benchmarks, newest first; also the team key's index.
	t.Col(&b.Team).NoIndex()
	t.Index(&b.Team, storm.Desc(&b.CreateTime), storm.Desc(&b.ID))
	// The benchmarks a dataset's training must leave rows out for.
	t.Index(&b.DatasetID)
	t.Check(storm.RawSQL(`kind IN ('class', 'number', 'ranked')`))
	t.Check(storm.RawSQL(`rows >= 1`))
}
