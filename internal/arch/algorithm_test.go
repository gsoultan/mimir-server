// Copyright (c) 2026 Gembit Soultan Shirazi
//
// This file is part of mimir-server, a server that trains, serves and manages mimir models,
// created and owned by Gembit Soultan Shirazi.

package arch

import "testing"

func TestEveryModelTypeNamesItsAlgorithm(t *testing.T) {
	for _, a := range All() {
		if a.Algorithm() == "" {
			t.Errorf("%s names no algorithm", a.Name)
		}
	}
}
