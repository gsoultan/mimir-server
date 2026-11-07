// Copyright (c) 2026 Gembit Soultan Shirazi
//
// This file is part of mimir-server, a server that trains, serves and manages mimir models,
// created and owned by Gembit Soultan Shirazi.

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
