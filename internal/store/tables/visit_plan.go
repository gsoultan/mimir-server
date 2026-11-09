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

// VisitPlan plans salespeople's visits for the coming days: it asks a
// model (ModelID, a plain-text reference, so deleting the model only makes
// the next run fail) how likely each customer is to order, then plans each
// salesperson's days with the likeliest first. Its customers come from an
// upload (kept as a file in the data folder), a database query (Driver,
// DSN and CustomersQuery, as on a Scoring; DSN is a secret) or a table
// dataset (DatasetID). Settings is the rest of what was asked, as jsonb.
// Each run replaces its VisitPlanSalesperson rows.
type VisitPlan struct {
	Record
	Named
	TeamOwned
	ModelID          string
	DatasetID        string
	Driver           string
	DSN              string
	CustomersQuery   string
	SalespeopleQuery string
	Settings         storm.JSON
	Status           JobStatus
	Error            string
	// Stage is what a running plan is doing ("reading", "scoring",
	// "planning"), Done and Total how far it is.
	Stage string
	Done  int64
	Total int64
	// The last run's summary.
	StartDate   string
	EndDate     string
	Customers   int64
	LeftOut     int64
	Unassigned  int64
	Unplaced    int64
	Failed      int64
	Salespeople int64
	Visits      int64
	Km          float64
	Covered     float64
	NeedLook    int64
	Warnings    []string
	// Areas is how many salespeople each area has, as jsonb.
	Areas     storm.JSON
	CreatedBy string
	StartTime *time.Time
	EndTime   *time.Time
	// RepeatSeconds > 0 plans again that often; NextRun is when.
	RepeatSeconds int64
	// Schedule, as jsonb, sets the time of day and the weekdays a
	// repeating plan is made; '{}' repeats every RepeatSeconds.
	Schedule storm.JSON
	NextRun  *time.Time
	Runs     int64
}

func (p *VisitPlan) Schema(t *storm.Table) {
	t.Col(&p.ModelID).Default("''").Immutable()
	t.Col(&p.DatasetID).Default("''").Immutable()
	t.Col(&p.Driver).Default("''").Immutable()
	// Not Immutable: the store seals a plaintext one in place.
	t.Col(&p.DSN).Named("dsn").Default("''")
	t.Col(&p.CustomersQuery).Default("''").Immutable()
	t.Col(&p.SalespeopleQuery).Default("''").Immutable()
	t.Col(&p.Settings).Default("'{}'").Immutable()
	t.Col(&p.Error).Default("''")
	t.Col(&p.Stage).Default("''")
	t.Col(&p.Done).Default("0")
	t.Col(&p.Total).Default("0")
	t.Col(&p.StartDate).Default("''")
	t.Col(&p.EndDate).Default("''")
	t.Col(&p.Customers).Default("0")
	t.Col(&p.LeftOut).Default("0")
	t.Col(&p.Unassigned).Default("0")
	t.Col(&p.Unplaced).Default("0")
	t.Col(&p.Failed).Default("0")
	t.Col(&p.Salespeople).Default("0")
	t.Col(&p.Visits).Default("0")
	t.Col(&p.Km).Default("0")
	t.Col(&p.Covered).Default("0")
	t.Col(&p.NeedLook).Default("0")
	t.Col(&p.Warnings).Default("'{}'")
	t.Col(&p.Areas).Default("'[]'")
	t.Col(&p.CreatedBy).Default("''").Immutable()
	t.Col(&p.RepeatSeconds).Default("0")
	t.Col(&p.Schedule).Default("'{}'")
	t.Col(&p.Runs).Default("0")
	// A team's plans, newest first; also the team key's index.
	t.Col(&p.Team).NoIndex()
	t.Index(&p.Team, storm.Desc(&p.CreateTime), storm.Desc(&p.ID))
	// The queue, and what a restart left running.
	t.Index(&p.Status, &p.CreateTime, &p.ID)
	// Plans due to run again. Partial: most never repeat.
	t.Index(&p.NextRun, &p.ID).Where(storm.RawSQL(`repeat_seconds > 0`))
	t.Check(storm.RawSQL(between("status", JobQueued, JobCancelled)))
	t.Check(storm.RawSQL(`done >= 0 AND total >= 0 AND repeat_seconds >= 0`))
}
