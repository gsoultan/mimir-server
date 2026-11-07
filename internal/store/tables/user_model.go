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

// UserModel grants a user one model when the user's AllModels is false.
type UserModel struct {
	User  User
	Model Model
}

func (g *UserModel) Schema(t *storm.Table) {
	t.PrimaryKey(&g.User, &g.Model)
	t.Col(&g.User).OnDelete(storm.Cascade).NoIndex() // the key's first column
	t.Col(&g.Model).OnDelete(storm.Cascade)          // indexed, for the cascade
}
