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

// Passage is a piece of a document, a few paragraphs long: its text, kept
// searchable by words (Search, which PostgreSQL maintains), and its
// embedding (Vector: a float32 scale, then one signed byte per number).
// CollectionID repeats the document's collection so a search reads one
// index.
type Passage struct {
	Record
	Document   Document
	Collection Collection
	Number     int32
	Text       string
	Vector     []byte
	// Search is last: a zero-size field anywhere else shares its address
	// with the next one, and Schema tells columns apart by address.
	Search storm.TSVector
}

func (p *Passage) Schema(t *storm.Table) {
	t.Col(&p.Document).OnDelete(storm.Cascade).Immutable()   // indexed, for the cascade
	t.Col(&p.Collection).OnDelete(storm.Cascade).Immutable() // indexed, for the cascade and the vectors' load
	t.Col(&p.Number).Immutable()
	t.Col(&p.Text).Immutable()
	t.Col(&p.Vector).Immutable()
	// English, as PostgreSQL reads a search by default: a query's words
	// are cut to their stems by the same rules as the passage's.
	t.Col(&p.Search).Generated(storm.RawSQL(`to_tsvector('english', text)`))
	t.Index(&p.Search).Using("gin")
	t.Check(storm.RawSQL(`number >= 1`))
}
