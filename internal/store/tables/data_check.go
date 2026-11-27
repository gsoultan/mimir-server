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

// DataCheck is how the questions a table model was asked lately compare
// with the examples its live version learned from, column by column. The
// server checks every hour; no feedback is needed, so it warns about
// every table model, not only those whose apps send answers back.
type DataCheck struct {
	Model Model
	// VersionID is the version Baseline describes. When another version
	// goes live the baseline is made again from that version's examples.
	VersionID string
	// Baseline is the []drift.Column the version's examples hold, as
	// jsonb; empty when they can't be read, such as for an uploaded model.
	Baseline  storm.JSON
	CheckTime time.Time
	// Questions is how many recent questions were compared.
	Questions int64
	// Changes is the []drift.Change found, biggest first, as jsonb.
	Changes storm.JSON
	// The columns last alerted about, and when, so one change alerts
	// owners once rather than every hour.
	AlertTime    *time.Time
	AlertColumns []string
}

func (d *DataCheck) Schema(t *storm.Table) {
	t.PrimaryKey(&d.Model)
	t.Col(&d.Model).OnDelete(storm.Cascade).NoIndex()
	t.Col(&d.VersionID).Default("''")
	t.Col(&d.Baseline).Default("'[]'")
	t.Col(&d.Questions).Default("0")
	t.Col(&d.Changes).Default("'[]'")
	t.Col(&d.AlertColumns).Default("'{}'")
	t.Check(storm.RawSQL(`questions >= 0`))
}
