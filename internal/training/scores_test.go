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

package training

import (
	"testing"

	"github.com/gsoultan/mimir/vision"
)

func TestBoxReport(t *testing.T) {
	cat := func(x float64, score float64) vision.Detection {
		return vision.Detection{Box: vision.Box{X1: x, Y1: 0, X2: x + 10, Y2: 10}, Class: 0, Score: score}
	}
	dog := func(x float64, score float64) vision.Detection {
		d := cat(x, score)
		d.Class = 1
		return d
	}
	truth := [][]vision.Detection{{cat(0, 1), dog(50, 1)}, {cat(0, 1)}}
	found := [][]vision.Detection{
		// The cat and the dog found surely, and a cat where there is none.
		{cat(0, 0.9), dog(51, 0.8), cat(100, 0.6)},
		// The second cat found, but too unsure to be answered with.
		{cat(1, 0.1)},
	}
	r := boxReport(found, truth, []string{"cat", "dog"})
	if r.Kind != "boxes" || r.Examples != 2 || len(r.Classes) != 2 || r.AP50 <= 0 || r.AP50 > 1 || r.AP75 > r.AP50 {
		t.Fatalf("report %+v", r)
	}
	c, d := r.Classes[0], r.Classes[1]
	// Cats: 2 real, 2 answered with (one wrong), 1 right.
	if c.Support != 2 || !near(c.Precision, 0.5) || !near(c.Recall, 0.5) || !near(c.F1, 0.5) {
		t.Fatalf("cat %+v", c)
	}
	if d.Support != 1 || !near(d.Precision, 1) || !near(d.Recall, 1) || !near(d.AP, 1) {
		t.Fatalf("dog %+v", d)
	}
	if boxReport(nil, nil, []string{"cat"}) != nil {
		t.Fatal("a report with no photos kept aside")
	}
}

func TestTextReport(t *testing.T) {
	r := textReport(scored{loss: 1.2, right: 60, total: 100}, &scored{loss: 2.5, right: 30, total: 100}, 8, 72)
	if r.Kind != "text" || r.Examples != 8 || r.OnTraining || !near(r.Accuracy, 0.6) || !near(r.BeforeAccuracy, 0.3) || !near(r.BeforeLoss, 2.5) || r.Tokens != 100 {
		t.Fatalf("report %+v", r)
	}
	r = textReport(scored{loss: 1, right: 5, total: 10}, nil, 0, 20)
	if !r.OnTraining || r.Examples != 20 || r.BeforeLoss != 0 {
		t.Fatalf("nothing kept aside %+v", r)
	}
}
