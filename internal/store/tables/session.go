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

// Session is one signed-in browser. Only the token's hash is kept.
type Session struct {
	Record
	TokenHash  string
	User       User
	AgentHash  string
	Device     string
	IP         string
	Pending    bool
	LastSeen   time.Time
	ExpireTime time.Time
}

func (s *Session) Schema(t *storm.Table) {
	t.Col(&s.TokenHash).Unique() // every request's lookup
	t.Col(&s.User).OnDelete(storm.Cascade).Immutable()
	t.Col(&s.AgentHash).Immutable()
	t.Col(&s.Device).Default("''")
	t.Col(&s.IP).Default("''")
	t.Col(&s.Pending).Default("false")
	// The expiry sweep. LastSeen is deliberately not indexed: it changes on
	// every request, and an index on it would stop those updates being HOT.
	t.Index(&s.ExpireTime)
}
