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

// APIKey is a credential for apps, owned by a user.
type APIKey struct {
	Record
	Named
	User       User
	Prefix     string
	SecretHash string
	Role       Role
	AllModels  bool
	LastUsed   *time.Time
}

func (k *APIKey) Schema(t *storm.Table) {
	t.Col(&k.SecretHash).Unique() // every API call's lookup
	t.Col(&k.Prefix).Immutable()
	// A user's keys, newest first; also the user key's index.
	t.Col(&k.User).OnDelete(storm.Cascade).Immutable().NoIndex()
	t.Index(&k.User, storm.Desc(&k.CreateTime), storm.Desc(&k.ID))
	t.Check(storm.RawSQL(between("role", RoleViewer, RoleOwner)))
}
