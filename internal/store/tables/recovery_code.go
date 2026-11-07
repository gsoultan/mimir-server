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

// RecoveryCode is a hashed one-time code for when the phone is gone.
type RecoveryCode struct {
	User     User
	CodeHash string
}

func (r *RecoveryCode) Schema(t *storm.Table) {
	t.PrimaryKey(&r.User, &r.CodeHash)
	t.Col(&r.User).OnDelete(storm.Cascade).NoIndex() // the key's first column
}
