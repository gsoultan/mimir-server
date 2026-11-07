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

// Worker is a process that runs models for this server. Only its token's
// hash is kept; PublicKey is the Ed25519 key it joined with.
type Worker struct {
	Record
	Named
	TokenHash   string
	CreatedBy   string
	LastSeen    *time.Time
	PublicKey   []byte
	LastAddress string
	// Tasks is the work the console gives the worker, set from its page:
	// "all", "train" or "serve"; "" leaves it to the worker's own -tasks.
	Tasks string
}

func (w *Worker) Schema(t *storm.Table) {
	t.Col(&w.TokenHash).Unique() // every connection's lookup; replaced when the worker is re-registered
	t.Col(&w.CreatedBy).Immutable()
	t.Col(&w.LastAddress).Default("''")
	t.Col(&w.Tasks).Default("''")
}
