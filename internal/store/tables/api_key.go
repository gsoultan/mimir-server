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
