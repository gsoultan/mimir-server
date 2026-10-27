package tables

import (
	"time"

	"github.com/gsoultan/storm"
)

// BenchmarkScore is one model's result on one benchmark: the latest, for
// the weights the model had then (WeightsSHA). Report is the store.Report,
// as jsonb; {} until it is scored.
type BenchmarkScore struct {
	Record
	Benchmark  Benchmark
	Model      Model
	WeightsSHA string
	Status     JobStatus
	Error      string
	Report     storm.JSON
	// Headline is the number the leaderboard sorts by, for the kind:
	// accuracy or macro F1, MAE, or hit rate at k.
	Headline float64
	ScoredAt *time.Time
}

func (s *BenchmarkScore) Schema(t *storm.Table) {
	// One score per model on a benchmark; also the benchmark key's index.
	t.Col(&s.Benchmark).OnDelete(storm.Cascade).Immutable().NoIndex()
	t.Unique(&s.Benchmark, &s.Model)
	t.Col(&s.Model).OnDelete(storm.Cascade).Immutable() // indexed, for the cascade
	t.Col(&s.WeightsSHA).Default("''")
	t.Col(&s.Error).Default("''")
	t.Col(&s.Report).Default("'{}'")
	t.Col(&s.Headline).Default("0")
	t.Check(storm.RawSQL(between("status", JobQueued, JobCancelled)))
}
