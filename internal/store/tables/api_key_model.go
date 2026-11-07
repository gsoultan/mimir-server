// Copyright (c) 2026 Gembit Soultan Shirazi
//
// This file is part of mimir-server, a server that trains, serves and manages mimir models,
// created and owned by Gembit Soultan Shirazi.

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
