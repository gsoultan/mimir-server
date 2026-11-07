// Copyright (c) 2026 Gembit Soultan Shirazi
//
// This file is part of mimir-server, a server that trains, serves and manages mimir models,
// created and owned by Gembit Soultan Shirazi.

package tables

import "github.com/gsoultan/storm"

// Setting is a small key/value pair, such as alert and retention choices.
type Setting struct {
	Key   string
	Value string
}

func (s *Setting) Schema(t *storm.Table) { t.PrimaryKey(&s.Key) }
