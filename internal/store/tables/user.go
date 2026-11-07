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

// User is an account. Its team is a membership, not ownership: people move
// between teams, so unlike TeamOwned the column can be updated.
type User struct {
	Record
	Named
	Email        string
	PasswordHash string
	Role         Role
	AllModels    bool
	Disabled     bool
	LastSignIn   *time.Time
	// TOTPSecret is the confirmed authenticator key; TOTPPending one shown
	// but not yet confirmed. TOTPLastStep refuses a code used before.
	TOTPSecret   string
	TOTPPending  string
	TOTPLastStep int64
	Team         *Team
}

// Aggregates: how many people each team has.
func (u *User) Aggregates(a *storm.Aggregates) {
	perTeam := a.Named("PerTeam")
	perTeam.By(&u.Team)
	perTeam.Count("N")
}

func (u *User) Schema(t *storm.Table) {
	t.Col(&u.Email).Unique()
	t.Col(&u.Disabled).Default("false")
	t.Col(&u.TOTPSecret).Named("totp_secret").Default("''")
	t.Col(&u.TOTPPending).Named("totp_pending").Default("''")
	t.Col(&u.TOTPLastStep).Named("totp_last_step").Default("0")
	// A team's people, in the order they joined; also the key's index.
	t.Col(&u.Team).NoIndex()
	t.Index(&u.Team, &u.CreateTime, &u.ID)
	t.Check(storm.RawSQL(`length(email) BETWEEN 3 AND 254`))
	t.Check(storm.RawSQL(`password_hash <> ''`))
	t.Check(storm.RawSQL(between("role", RoleViewer, RoleOwner)))
}
