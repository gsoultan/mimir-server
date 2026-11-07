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

// Webhook is an endpoint that gets told about events. Events lists the
// event types it wants; empty means every event.
type Webhook struct {
	Record
	Named
	TeamOwned
	URL     string
	Secret  string
	Events  []string
	Enabled bool
}

func (w *Webhook) Schema(t *storm.Table) {
	// Secret never changes once set, but it is not Immutable: the store
	// seals a plaintext one left by an older server in place.
	t.Col(&w.Events).Default("'{}'").ConvertFrom(storm.JSONText)
	t.Col(&w.Enabled).Default("true")
	// A team's webhooks go with it. The list, oldest first, is the team
	// key's index.
	t.Col(&w.Team).OnDelete(storm.Cascade).NoIndex()
	t.Index(&w.Team, &w.CreateTime, &w.ID)
	t.Check(storm.RawSQL(`length(url) BETWEEN 1 AND 2048`))
	t.Check(storm.RawSQL(`secret <> ''`))
}
