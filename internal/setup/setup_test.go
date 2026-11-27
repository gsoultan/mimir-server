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

package setup

import "testing"

func TestCheckListen(t *testing.T) {
	for addr, ok := range map[string]bool{
		"127.0.0.1:0":     true,
		":0":              true,
		"8080":            false,
		"example.com:80":  false,
		"10.255.255.1:80": false, // not this machine's
	} {
		if err := CheckListen(addr); (err == nil) != ok {
			t.Errorf("CheckListen(%q) = %v, want ok %v", addr, err, ok)
		}
	}
}
