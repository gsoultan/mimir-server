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

import "github.com/gsoultan/storm"

// Usage counts predictions per month, person and key ("" for the web app).
type Usage struct {
	Month       string
	User        User
	KeyID       string
	KeyName     string
	Predictions int64
}

// Joins: counters with their owner's name, busiest first.
func (u *Usage) Joins(j *storm.Joins) {
	var p User
	j.Named("WithUser").
		Inner(&p, &u.User).
		Take(&u.Month, "Month").
		Take(&u.User, "UserID").
		Take(&u.KeyID, "KeyID").
		Take(&u.KeyName, "KeyName").
		Take(&u.Predictions, "Predictions").
		Take(&p.Name, "UserName").
		Take(&p.Email, "Email").
		OrderDesc(&u.Predictions).
		OrderAsc(&p.Email)
}

func (u *Usage) Schema(t *storm.Table) {
	t.Name("usage")
	// The key serves a month's counters, and one person's in a month.
	t.PrimaryKey(&u.Month, &u.User, &u.KeyID)
	t.Col(&u.User).OnDelete(storm.Cascade) // indexed, for the cascade
	t.Col(&u.KeyID).Default("''")
	t.Col(&u.KeyName).Default("''")
	t.Col(&u.Predictions).Default("0")
	t.Check(storm.RawSQL(`length(month) = 7`))
}
