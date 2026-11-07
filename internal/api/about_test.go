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


package api

import (
	"runtime/debug"
	"testing"
)

func TestAboutReadsTheBuild(t *testing.T) {
	bi := &debug.BuildInfo{
		GoVersion: "go1.27.3",
		Settings: []debug.BuildSetting{
			{Key: "vcs.revision", Value: "7f3c2a1e9b"},
			{Key: "vcs.time", Value: "2026-11-04T09:30:00Z"},
			{Key: "vcs.modified", Value: "true"},
		},
		Deps: []*debug.Module{
			{Path: "github.com/gsoultan/storm", Version: "v1.4.0"},
			{Path: "connectrpc.com/connect", Version: "v1.21.1"},
			{Path: "github.com/gsoultan/mimir", Version: "v0.4.2", Replace: &debug.Module{Path: "../mimir", Version: "v0.4.3"}},
		},
	}
	got := about("v0.2.0", bi)
	if got.Version != "0.2.0" || got.GoVersion != "1.27.3" || got.Commit != "7f3c2a1e9b" || !got.Modified {
		t.Fatalf("about = %+v", got)
	}
	if got.GetBuilt().AsTime().Day() != 4 {
		t.Errorf("built = %v, want 4 November", got.GetBuilt().AsTime())
	}
	// Parts come in the fixed order, mimir first, with the replaced version.
	if len(got.Parts) != 2 || got.Parts[0].Path != "github.com/gsoultan/mimir" || got.Parts[0].Version != "0.4.3" || got.Parts[1].Path != "github.com/gsoultan/storm" {
		t.Errorf("parts = %v", got.Parts)
	}
	if len(got.Dependencies) != 1 || got.Dependencies[0].Path != "connectrpc.com/connect" {
		t.Errorf("dependencies = %v", got.Dependencies)
	}
}

func TestAboutWithoutBuildInfo(t *testing.T) {
	got := about("", nil)
	if got.Version != "dev" || got.Commit != "" || len(got.Parts) != 0 {
		t.Fatalf("about = %+v", got)
	}
}
