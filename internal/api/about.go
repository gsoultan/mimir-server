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
	"context"
	"runtime/debug"
	"slices"
	"strings"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"

	mimirv1 "github.com/gsoultan/mimir-server/gen/mimir/v1"
	"github.com/gsoultan/mimir-server/internal/store"
)

// parts are the mimir projects the server is built on, in the order the
// About page lists them.
var parts = []string{"github.com/gsoultan/mimir", "github.com/gsoultan/mimir-model", "github.com/gsoultan/storm"}

// started is when this process started, close enough for "running for".
var started = time.Now()

// GetAbout says which version runs and what it is built on.
func (s *Service) GetAbout(_ context.Context, _ *connect.Request[mimirv1.GetAboutRequest]) (*connect.Response[mimirv1.GetAboutResponse], error) {
	res := about(s.Version, debugInfo())
	res.Started = timestamppb.New(started)
	res.SchemaVersion = int32(store.LatestSchemaVersion())
	if s.Pool != nil {
		for _, st := range s.Pool.Statuses() {
			res.WorkerVersions = append(res.WorkerVersions, st.Hello.GetVersion())
		}
	}
	return connect.NewResponse(res), nil
}

func debugInfo() *debug.BuildInfo {
	bi, _ := debug.ReadBuildInfo()
	return bi
}

// about reads the version and modules from the build. version is what the
// build was stamped with; empty means "dev".
func about(version string, bi *debug.BuildInfo) *mimirv1.GetAboutResponse {
	res := &mimirv1.GetAboutResponse{Version: strings.TrimPrefix(version, "v")}
	if res.Version == "" {
		res.Version = "dev"
	}
	if bi == nil {
		return res
	}
	res.GoVersion = strings.TrimPrefix(bi.GoVersion, "go")
	for _, kv := range bi.Settings {
		switch kv.Key {
		case "vcs.revision":
			res.Commit = kv.Value
		case "vcs.modified":
			res.Modified = kv.Value == "true"
		case "vcs.time":
			if t, err := time.Parse(time.RFC3339, kv.Value); err == nil {
				res.Built = timestamppb.New(t)
			}
		}
	}
	for _, path := range parts {
		for _, d := range bi.Deps {
			if d.Path == path {
				res.Parts = append(res.Parts, &mimirv1.AboutModule{Path: d.Path, Version: moduleVersion(d)})
			}
		}
	}
	for _, d := range bi.Deps {
		if !slices.Contains(parts, d.Path) {
			res.Dependencies = append(res.Dependencies, &mimirv1.AboutModule{Path: d.Path, Version: moduleVersion(d)})
		}
	}
	return res
}

// moduleVersion is the version actually compiled in, after any replace.
func moduleVersion(m *debug.Module) string {
	if m.Replace != nil {
		m = m.Replace
	}
	return strings.TrimPrefix(m.Version, "v")
}
