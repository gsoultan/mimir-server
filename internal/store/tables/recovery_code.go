// Copyright (c) 2026 Gembit Soultan Shirazi
//
// This file is part of mimir-server, a server that trains, serves and manages mimir models,
// created and owned by Gembit Soultan Shirazi.

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
