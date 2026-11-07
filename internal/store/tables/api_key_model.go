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
