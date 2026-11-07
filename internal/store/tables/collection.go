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
	"github.com/gsoultan/storm"
)

// Collection is a team's set of documents to answer questions from. Its
// passages' embeddings are made by EmbeddingModel, so deleting that model
// deletes the collection; LanguageModelID, which writes the answers, is a
// plain-text reference that can be changed, and asking fails while it
// names a deleted model.
type Collection struct {
	Record
	Named
	TeamOwned
	EmbeddingModel  Model
	LanguageModelID string
	Instructions    string
	CreatedBy       string
}

func (c *Collection) Schema(t *storm.Table) {
	t.Col(&c.EmbeddingModel).OnDelete(storm.Cascade).Immutable() // indexed, for the cascade
	t.Col(&c.LanguageModelID).Default("''")
	t.Col(&c.Instructions).Default("''")
	t.Col(&c.CreatedBy).Default("''").Immutable()
	// A team's collections, by name; also the team key's index.
	t.Col(&c.Team).NoIndex()
	t.Index(&c.Team, &c.Name, &c.ID)
}
