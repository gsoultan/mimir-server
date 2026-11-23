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


package server_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"connectrpc.com/connect"

	mimirv1 "github.com/gsoultan/mimir-server/gen/mimir/v1"
	"github.com/gsoultan/mimir-server/internal/update"
)

// TestUpdates checks for a new version against a fake GitHub, turns
// automatic updates on, and checks only owners reach any of it.
func TestUpdates(t *testing.T) {
	srv := newServer(t)
	ctx := context.Background()
	own := owner(t, srv)

	// Turned off with -updates=false: nothing to check.
	svc.Updater = nil
	got, err := own.settings.GetUpdates(ctx, connect.NewRequest(&mimirv1.GetUpdatesRequest{}))
	if err != nil || got.Msg.GetUpdates().GetBlocked() != mimirv1.UpdateBlock_UPDATE_BLOCK_TURNED_OFF {
		t.Fatalf("GetUpdates turned off = %v, %v", got, err)
	}
	if _, err := own.settings.CheckForUpdates(ctx, connect.NewRequest(&mimirv1.CheckForUpdatesRequest{})); code(err) != connect.CodeFailedPrecondition {
		t.Fatalf("CheckForUpdates turned off: %v", err)
	}

	github := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/repos/gsoultan/mimir-server/releases/latest" {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"tag_name": "v0.4.0", "html_url": "https://github.com/gsoultan/mimir-server/releases/tag/v0.4.0", "assets": []any{}})
	}))
	defer github.Close()
	svc.Updater = &update.Updater{Current: "0.3.0", DataDir: t.TempDir(), API: github.URL, Client: github.Client()}
	t.Cleanup(func() { svc.Updater = nil })

	got, err = own.settings.GetUpdates(ctx, connect.NewRequest(&mimirv1.GetUpdatesRequest{}))
	if err != nil || got.Msg.GetUpdates().GetLatestVersion() != "" || got.Msg.GetUpdates().GetChecked() != nil {
		t.Fatalf("GetUpdates before a check = %v, %v", got, err)
	}
	checked, err := own.settings.CheckForUpdates(ctx, connect.NewRequest(&mimirv1.CheckForUpdatesRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	u := checked.Msg.GetUpdates()
	if !u.GetAvailable() || u.GetLatestVersion() != "0.4.0" || u.GetCurrentVersion() != "0.3.0" || u.GetChecked() == nil {
		t.Fatalf("CheckForUpdates = %v", u)
	}
	// The fake release has no files, so this machine has nothing to install.
	if u.GetBlocked() != mimirv1.UpdateBlock_UPDATE_BLOCK_NO_DOWNLOAD {
		t.Fatalf("blocked = %v", u.GetBlocked())
	}
	if _, err := own.settings.InstallUpdate(ctx, connect.NewRequest(&mimirv1.InstallUpdateRequest{})); code(err) != connect.CodeFailedPrecondition {
		t.Fatalf("InstallUpdate with no download: %v", err)
	}

	set, err := own.settings.UpdateAutoUpdates(ctx, connect.NewRequest(&mimirv1.UpdateAutoUpdatesRequest{Auto: true}))
	if err != nil || !set.Msg.GetUpdates().GetAuto() {
		t.Fatalf("UpdateAutoUpdates = %v, %v", set, err)
	}
	if err := svc.LoadUpdatesSetting(ctx); err != nil || !svc.AutoUpdates() {
		t.Fatalf("automatic updates weren't kept: %v", err)
	}
	if _, err := own.settings.UpdateAutoUpdates(ctx, connect.NewRequest(&mimirv1.UpdateAutoUpdatesRequest{Auto: false})); err != nil {
		t.Fatal(err)
	}

	_, err = own.users.CreateUser(ctx, connect.NewRequest(&mimirv1.CreateUserRequest{
		Email: "viewer@example.com", Name: "Vee", Password: password,
		Access: &mimirv1.Access{Role: mimirv1.Role_ROLE_VIEWER},
	}))
	if err != nil {
		t.Fatal(err)
	}
	v := signIn(t, srv, "viewer@example.com")
	if _, err := v.settings.GetUpdates(ctx, connect.NewRequest(&mimirv1.GetUpdatesRequest{})); code(err) != connect.CodePermissionDenied {
		t.Fatalf("viewer GetUpdates: %v", err)
	}
	if _, err := v.settings.InstallUpdate(ctx, connect.NewRequest(&mimirv1.InstallUpdateRequest{})); code(err) != connect.CodePermissionDenied {
		t.Fatalf("viewer InstallUpdate: %v", err)
	}
}
