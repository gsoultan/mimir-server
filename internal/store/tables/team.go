// Copyright (c) 2026 Gembit Soultan Shirazi
//
// This file is part of mimir-server, a server that trains, serves and manages mimir models,
// created and owned by Gembit Soultan Shirazi.

package tables

import "github.com/gsoultan/storm"

// Team keeps a group of people and their things apart from other teams.
type Team struct {
	Record
	Named
}

func (t *Team) Schema(s *storm.Table) {
	s.Col(&t.Name).Unique() // also serves the list, ordered by name
}
