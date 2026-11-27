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

// Answer is one prediction a model made, kept for a while so the app that
// asked can say what the right answer was. Answers with that feedback are
// how the server knows how well a model does in use.
type Answer struct {
	Record
	Model Model
	// VersionID is the ModelVersion that answered.
	VersionID string
	// Label is the answer a model that picks one gave; a number model
	// leaves it empty and answers with Value.
	Label      string
	Value      float64
	Confidence float64
	// Fields is the question by column name (map[string]string) as jsonb,
	// for a model asked that way; empty otherwise.
	Fields storm.JSON
	// What the answer should have been, from feedback, and whether the
	// model got it: the same label, or a number within 10% of it.
	FeedbackTime *time.Time
	TruthLabel   string
	TruthValue   float64
	Correct      bool
	// ShadowOf is, for an answer a version on trial gave to a copy of a
	// question, the id of the live version's answer, which the asker got;
	// Agrees says the two answers were the same. Shadow answers count in
	// a trial's results only, not in the model's answers.
	ShadowOf string
	Agrees   bool
}

// Aggregates: a model's answers by the version that gave them, with the
// feedback on them and, for shadow answers, how many agreed with the
// live version, for a trial's results.
func (a *Answer) Aggregates(g *storm.Aggregates) {
	v := g.Named("PerVersion")
	v.By(&a.VersionID)
	v.Count("Answers")
	v.Count("Feedback").Filter(g.IsNotNull(&a.FeedbackTime))
	v.Count("Correct").Filter(g.Eq(&a.Correct, true))
	v.Count("Agree").Filter(g.Eq(&a.Agrees, true))
	v.Count("Shadow").Filter(g.Ne(&a.ShadowOf, ""))
}

func (a *Answer) Schema(t *storm.Table) {
	// A model's answers, newest first; also the model key's index.
	t.Col(&a.Model).OnDelete(storm.Cascade).NoIndex()
	t.Index(&a.Model, storm.Desc(&a.CreateTime))
	t.Col(&a.VersionID).Default("''")
	t.Col(&a.Label).Default("''")
	t.Col(&a.Value).Default("0")
	t.Col(&a.Confidence).Default("0")
	t.Col(&a.Fields).Default("'{}'")
	t.Col(&a.TruthLabel).Default("''")
	t.Col(&a.TruthValue).Default("0")
	t.Col(&a.Correct).Default("false")
	t.Col(&a.ShadowOf).Default("''")
	t.Col(&a.Agrees).Default("false")
	// The shadow answers to judge with a live answer's feedback. Partial:
	// most answers shadow none.
	t.Index(&a.ShadowOf).Where(storm.RawSQL(`shadow_of <> ''`))
	// The retention trim; rows arrive in time order.
	t.Index(&a.CreateTime).Using(storm.BRIN)
	t.Check(storm.RawSQL(`length(label) <= 200`))
	t.Check(storm.RawSQL(`length(truth_label) <= 200`))
}

// AnswerDay counts a model's answers on one day (UTC), and how many of
// those got feedback and were right, for its charts and accuracy alerts.
// Counters outlive the answers themselves.
type AnswerDay struct {
	Model    Model
	Day      time.Time
	Answers  int64
	Feedback int64
	Correct  int64
}

func (d *AnswerDay) Schema(t *storm.Table) {
	// The key serves a model's chart (model, day range) and its cascade.
	t.PrimaryKey(&d.Model, &d.Day)
	t.Col(&d.Model).OnDelete(storm.Cascade).NoIndex()
	t.Col(&d.Answers).Default("0")
	t.Col(&d.Feedback).Default("0")
	t.Col(&d.Correct).Default("0")
	// Every model's last week, for the accuracy check.
	t.Index(&d.Day)
	t.Check(storm.RawSQL(`answers >= 0 AND feedback >= 0 AND correct >= 0`))
}
