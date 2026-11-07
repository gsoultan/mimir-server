// Copyright (c) 2026 Gembit Soultan Shirazi
// SPDX-License-Identifier: AGPL-3.0-only OR LicenseRef-Commercial
//
// This file is part of mimir-server, a server that trains, serves and manages mimir models,
// created and owned by Gembit Soultan Shirazi.
// It is dual-licensed: you may use it under the GNU Affero General Public
// License, version 3, as published by the Free Software Foundation, or
// under a commercial license from Gembit Soultan Shirazi. See the LICENSE and
// LICENSING.md files at the root of this repository.
//
// This program is distributed in the hope that it will be useful, but
// WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the GNU Affero
// General Public License for more details.

package server_test

import (
	"context"
	"testing"

	"connectrpc.com/connect"

	mimirv1 "github.com/gsoultan/mimir-server/gen/mimir/v1"
	"github.com/gsoultan/mimir-server/internal/auth"
)

// sessionIPs lists the addresses on the caller's sessions.
func sessionIPs(t *testing.T, c clients) map[string]bool {
	t.Helper()
	ls, err := c.auth.ListSessions(context.Background(), connect.NewRequest(&mimirv1.ListSessionsRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	ips := map[string]bool{}
	for _, se := range ls.Msg.GetSessions() {
		ips[se.GetIp()] = true
	}
	return ips
}

// TestForwardedForTrust checks that X-Forwarded-For only names the client
// when a trusted proxy sent it, so nobody can pick the address that goes
// in the audit log and keys the rate limits.
func TestForwardedForTrust(t *testing.T) {
	ctx := context.Background()
	srv := newServer(t)
	own := owner(t, srv)
	signInFrom := func(xff string) {
		t.Helper()
		c := newClients(srv, withHeader("X-Forwarded-For", xff))
		if _, err := c.auth.SignIn(ctx, connect.NewRequest(&mimirv1.SignInRequest{Email: "owner@example.com", Password: password})); err != nil {
			t.Fatal(err)
		}
	}

	// By default no proxy is trusted: the header is ignored.
	signInFrom("192.0.2.66")
	if ips := sessionIPs(t, own); ips["192.0.2.66"] || !ips["127.0.0.1"] {
		t.Fatalf("spoofed X-Forwarded-For was believed: %v", ips)
	}

	// Behind a trusted proxy (the test client is on loopback), it counts.
	proxies, err := auth.ParseProxies("127.0.0.1, ::1")
	if err != nil {
		t.Fatal(err)
	}
	svc.Proxies = proxies
	signInFrom("192.0.2.77")
	if ips := sessionIPs(t, own); !ips["192.0.2.77"] {
		t.Fatalf("trusted proxy's X-Forwarded-For ignored: %v", ips)
	}
}
