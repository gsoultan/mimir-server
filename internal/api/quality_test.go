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

package api

import (
	"testing"
	"time"

	"github.com/gsoultan/mimir-server/internal/store"
)

func TestWeekAndDropped(t *testing.T) {
	now := time.Date(2026, 10, 26, 13, 0, 0, 0, time.UTC)
	day := func(back int, fb, ok int64) store.AnswerDay {
		return store.AnswerDay{Day: store.Day(now.AddDate(0, 0, -back)), Answers: fb * 2, Feedback: fb, Correct: ok}
	}
	// Day 7 back is outside the week; days 0 to 6 count.
	w := week([]store.AnswerDay{day(7, 100, 0), day(6, 10, 7), day(0, 10, 7)}, now)
	if w.answers != 40 || w.feedback != 20 || w.correct != 14 || w.accuracy() != 0.7 {
		t.Fatalf("week = %+v", w)
	}
	for _, c := range []struct {
		t       tally
		trained float64
		want    bool
	}{
		{w, 0.9, true},
		{w, 0.8, false}, // within 10 points
		{w, 0, false},   // no held-out score to compare with
		{tally{feedback: 19, correct: 0}, 0.9, false}, // too little feedback
	} {
		if got := c.t.dropped(c.trained); got != c.want {
			t.Errorf("%+v dropped(%v) = %v", c.t, c.trained, got)
		}
	}
}
