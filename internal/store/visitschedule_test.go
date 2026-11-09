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
	"testing"
	"time"
)

func TestNextVisitRun(t *testing.T) {
	jkt, _ := time.LoadLocation("Asia/Jakarta") // UTC+7, no DST
	at := func(y int, m time.Month, d, h, min int) time.Time { return time.Date(y, m, d, h, min, 0, 0, jkt) }
	// Monday 9 November 2026, 10:00 in Jakarta.
	now := at(2026, time.November, 9, 10, 0)
	for _, c := range []struct {
		name    string
		seconds int64
		sched   *VisitSchedule
		want    time.Time
	}{
		{"once", 0, nil, time.Time{}},
		{"every day from now", 86400, nil, now.Add(24 * time.Hour)},
		{"05:30 every day, already past today", 86400, &VisitSchedule{AtMinute: 5*60 + 30}, at(2026, time.November, 10, 5, 30)},
		{"18:00 every day, later today", 86400, &VisitSchedule{AtMinute: 18 * 60}, at(2026, time.November, 9, 18, 0)},
		{"06:00 on Mon and Thu", 86400, &VisitSchedule{AtMinute: 6 * 60, Weekdays: []int{1, 4}}, at(2026, time.November, 12, 6, 0)},
		{"06:00 on Mondays only", 86400, &VisitSchedule{AtMinute: 6 * 60, Weekdays: []int{1}}, at(2026, time.November, 16, 6, 0)},
		{"10:00 today is now, so next week", 86400, &VisitSchedule{AtMinute: 10 * 60, Weekdays: []int{1}}, at(2026, time.November, 16, 10, 0)},
	} {
		got := NextVisitRun(c.seconds, c.sched, "Asia/Jakarta", now.UTC())
		if !got.Equal(c.want) {
			t.Errorf("%s: got %v, want %v", c.name, got.In(jkt), c.want)
		}
	}
	// An unknown time zone plans in UTC rather than never.
	if got := NextVisitRun(86400, &VisitSchedule{AtMinute: 60}, "Nowhere/Else", time.Date(2026, 11, 9, 2, 0, 0, 0, time.UTC)); !got.Equal(time.Date(2026, 11, 10, 1, 0, 0, 0, time.UTC)) {
		t.Errorf("unknown zone: %v", got)
	}
}
