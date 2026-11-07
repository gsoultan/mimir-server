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
