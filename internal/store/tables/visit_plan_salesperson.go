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

import "github.com/gsoultan/storm"

// VisitPlanSalesperson is one salesperson's part of a visit plan's last
// run: a summary to list, filter and sort by, and Detail, their days of
// visits and the likely customers that didn't fit, as jsonb. SearchText
// is their id, name and area in lower case, for finding them.
type VisitPlanSalesperson struct {
	Record
	Plan          VisitPlan
	SalespersonID string
	Name          string
	Area          string
	SearchText    string
	Customers     int64
	Visits        int64
	Km            float64
	Covered       float64
	LikelyLeft    int64
	Problem       string
	NeedLook      bool
	Detail        storm.JSON
}

func (s *VisitPlanSalesperson) Schema(t *storm.Table) {
	// A plan's salespeople go with it; one row per salesperson a run.
	t.Col(&s.Plan).OnDelete(storm.Cascade).Immutable().NoIndex()
	t.Unique(&s.Plan, &s.SalespersonID)
	t.Index(&s.Plan, &s.Area, &s.Name)
	t.Col(&s.Name).Default("''")
	t.Col(&s.Area).Default("''")
	t.Col(&s.SearchText).Default("''")
	t.Col(&s.Problem).Default("''")
	t.Col(&s.Detail).Default("'{}'")
	t.Check(storm.RawSQL(`customers >= 0 AND visits >= 0 AND likely_left >= 0`))
}
