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

package store

import (
	"context"
	"encoding/json"

	"github.com/gsoultan/mimir-server/internal/store/db/trainingjob"
)

// Report is how a finished run's model did on the examples kept aside,
// in more detail than each pass's accuracy: what someone needs to judge
// it. Which fields are set depends on Kind.
type Report struct {
	// Kind is "class" (picks a label), "number" or "ranked" (lists the
	// labels that fit best, as a recommender does).
	Kind string `json:"kind"`
	// Examples is how many were scored. OnTraining says nothing was kept
	// aside, so these are the examples it learned from.
	Examples   int  `json:"examples"`
	OnTraining bool `json:"on_training,omitempty"`
	// MillisPerExample is how long an answer took while scoring, in
	// batches, on the machine that trained it. SizeBytes is the model file.
	MillisPerExample float64 `json:"ms_per_example"`
	SizeBytes        int64   `json:"size_bytes"`
	// Made counts the made-up rows of rare answers it learned from beside
	// the real ones (see Settings.Balance); none of them were scored.
	Made int `json:"made,omitempty"`

	// Class and ranked models.
	Accuracy float64 `json:"accuracy,omitempty"`
	// MacroF1 averages F1 over the labels, so a rare label counts as much
	// as a common one. LogLoss is how sure it was of the right answers.
	MacroF1 float64      `json:"macro_f1,omitempty"`
	LogLoss float64      `json:"log_loss,omitempty"`
	Classes []ClassScore `json:"classes,omitempty"`
	// Confusion counts each real label (row) against each guess (column),
	// in Classes' order; left out when there are too many labels to show.
	Confusion [][]int64 `json:"confusion,omitempty"`

	// Number models, in the answer's units.
	MAE      float64 `json:"mae,omitempty"`
	RMSE     float64 `json:"rmse,omitempty"`
	R2       float64 `json:"r2,omitempty"`
	Within10 float64 `json:"within10,omitempty"`
	// Points are some real answers and the model's guesses, for a chart.
	Points []Point `json:"points,omitempty"`

	// Ranked models: HitAt[i] is the share whose right answer was in the
	// first i+1 suggestions; MRR and NDCG reward it nearer the top.
	K     int       `json:"k,omitempty"`
	HitAt []float64 `json:"hit_at,omitempty"`
	MRR   float64   `json:"mrr,omitempty"`
	NDCG  float64   `json:"ndcg,omitempty"`

	// AccuracyLow and AccuracyHigh (class and ranked models), and MAELow
	// and MAEHigh (number models), are the range the score on new examples
	// likely falls in, 95% of the time: narrow with many examples scored,
	// wide with few.
	AccuracyLow  float64 `json:"accuracy_low,omitempty"`
	AccuracyHigh float64 `json:"accuracy_high,omitempty"`
	MAELow       float64 `json:"mae_low,omitempty"`
	MAEHigh      float64 `json:"mae_high,omitempty"`
	// Baseline is the share always giving the most common answer,
	// BaselineLabel, gets right (class and ranked models): the score to
	// beat.
	Baseline      float64 `json:"baseline,omitempty"`
	BaselineLabel string  `json:"baseline_label,omitempty"`
	// Copies counts examples picked for scoring that were exact copies of
	// ones it learned from; they were moved to learning instead, so they
	// don't flatter the score.
	Copies int `json:"copies,omitempty"`

	// Models that find things in photos ("boxes"): AP50 and AP75 average,
	// over the labels, how well it finds them and how sure it is, counting
	// a box right when it overlaps the real one by half (AP50) or three
	// quarters (AP75). Classes score each label at the least score it
	// answers with; Support there counts the real boxes.
	AP50 float64 `json:"ap50,omitempty"`
	AP75 float64 `json:"ap75,omitempty"`

	// Fine-tuned language models ("text"): Accuracy is the share of word
	// pieces of the answers it guessed right as its first choice, LogLoss
	// how surprised it was by them on average (perplexity is its exp).
	// BeforeAccuracy and BeforeLoss are the same scores before this run
	// taught it, on the same examples, when it started fresh. Tokens counts
	// the word pieces scored.
	Tokens         int64   `json:"tokens,omitempty"`
	BeforeAccuracy float64 `json:"before_accuracy,omitempty"`
	BeforeLoss     float64 `json:"before_loss,omitempty"`
}

// ClassScore is how a model did on one label.
type ClassScore struct {
	Label     string  `json:"label"`
	Precision float64 `json:"precision"`
	Recall    float64 `json:"recall"`
	F1        float64 `json:"f1"`
	// AP is a box-finding model's average precision on this label, a box
	// counting right when it overlaps the real one by half.
	AP float64 `json:"ap,omitempty"`
	// Support is how many examples really had this label.
	Support int64 `json:"support"`
}

// Point is one real answer and the model's guess.
type Point struct {
	Real  float64 `json:"real"`
	Guess float64 `json:"guess"`
}

// SetJobReport records a finished run's report.
func (s *Store) SetJobReport(ctx context.Context, id string, r *Report) error {
	m := trainingjob.MutateKey(id)
	m.SetReport(reportDoc(r))
	return s.updateJob(ctx, m)
}

func reportDoc(r *Report) []byte {
	if r == nil {
		return []byte("{}")
	}
	b, err := json.Marshal(r)
	if err != nil { // only unsupported values fail, and these have none
		return []byte("{}")
	}
	return b
}

// reportOf reads a stored report; nil when there is none.
func reportOf(b []byte) *Report {
	var r Report
	if err := json.Unmarshal(b, &r); err != nil || r.Kind == "" {
		return nil
	}
	return &r
}
