// Copyright (c) 2026 Gembit Soultan Shirazi
// SPDX-License-Identifier: AGPL-3.0-only OR LicenseRef-Commercial
//
// This file is part of mimir-server, a server that trains, serves and manages mimir models,
// created and owned by Gembit Soultan Shirazi.
// It is dual-licensed: you may use it under the GNU Affero General Public
// License, version 3, as published by the Free Software Foundation, or
// under a commercial license from Gembit Soultan Shirazi. See the LICENSE and
// LICENSING.md files at the root of this repository.
//
// This program is distributed in the hope that it will be useful, but
// WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the GNU Affero
// General Public License for more details.

package tables

import (
	"time"

	"github.com/gsoultan/storm"
)

// AuditEvent is one line of who did what. Rows are never updated, only
// trimmed by age, so every column but the team (which a team's deletion
// clears) is Immutable. Its time column predates Record and keeps its name.
type AuditEvent struct {
	ID     string
	Time   time.Time
	Actor  string
	Action string
	Target string
	Detail string
	// Team is the team it happened in, if any; the line outlives the team.
	Team *Team
}

func (e *AuditEvent) Schema(t *storm.Table) {
	t.Col(&e.Time).Immutable()
	t.Col(&e.Actor).Immutable()
	t.Col(&e.Action).Immutable()
	t.Col(&e.Target).Default("''").Immutable()
	t.Col(&e.Detail).Default("''").Immutable()
	// A team's log, newest (highest UUIDv7) first, paged by id; also the
	// team key's index. Every team's log pages along the primary key.
	t.Col(&e.Team).OnDelete(storm.SetNull).NoIndex()
	t.Index(&e.Team, storm.Desc(&e.ID))
	// A worker's recent events.
	t.Index(&e.Target, storm.Desc(&e.Time), storm.Desc(&e.ID))
	// The retention trim. Rows arrive in time order and are never updated,
	// so a block-range index answers it at a fraction of a btree's size.
	t.Index(&e.Time).Using(storm.BRIN)
}
