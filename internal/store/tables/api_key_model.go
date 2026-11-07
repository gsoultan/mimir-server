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

// APIKeyModel grants a key one model when the key's AllModels is false.
type APIKeyModel struct {
	Key   APIKey
	Model Model
}

func (g *APIKeyModel) Schema(t *storm.Table) {
	t.PrimaryKey(&g.Key, &g.Model)
	t.Col(&g.Key).OnDelete(storm.Cascade).NoIndex() // the key's first column
	t.Col(&g.Model).OnDelete(storm.Cascade)         // indexed, for the cascade
}
