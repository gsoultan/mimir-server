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

// RecoveryCode is a hashed one-time code for when the phone is gone.
type RecoveryCode struct {
	User     User
	CodeHash string
}

func (r *RecoveryCode) Schema(t *storm.Table) {
	t.PrimaryKey(&r.User, &r.CodeHash)
	t.Col(&r.User).OnDelete(storm.Cascade).NoIndex() // the key's first column
}
