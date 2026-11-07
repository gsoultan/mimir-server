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
	"github.com/gsoultan/mimir-server/gen/mimir/v1/mimirv1connect"
)

func TestPlanVisits(t *testing.T) {
	ctx := context.Background()
	srv := newServer(t)
	own := owner(t, srv)
	vc := mimirv1connect.NewVisitServiceClient(srv.Client(), srv.URL, connect.WithInterceptors(own.jar))
	req := &mimirv1.PlanVisitsRequest{
		Stops: []*mimirv1.VisitStop{
			{Id: "c-1", Latitude: -6.20, Longitude: 106.85, Priority: 0.9},
			{Id: "c-2", Latitude: -6.25, Longitude: 106.80, Priority: 0.4, OpenMinute: 13 * 60, CloseMinute: 15 * 60},
			{Id: "c-3", Latitude: -6.18, Longitude: 106.82, Priority: 0.7, MustVisit: true},
		},
		Salespeople: []*mimirv1.Salesperson{{Id: "ana", StartLatitude: -6.21, StartLongitude: 106.82}},
		Days:        1,
	}
	res, err := vc.PlanVisits(ctx, connect.NewRequest(req))
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Msg.GetRoutes()) != 1 || len(res.Msg.GetRoutes()[0].GetVisits()) != 3 || len(res.Msg.GetUnplannedStopIds()) != 0 {
		t.Fatalf("plan %v", res.Msg)
	}
	for _, v := range res.Msg.GetRoutes()[0].GetVisits() {
		if v.GetStopId() == "c-2" && v.GetLeaveMinute()-30 < 13*60 {
			t.Errorf("c-2 visited before it opens: %v", v)
		}
	}
	req.Stops[0].Latitude = 200
	if _, err := vc.PlanVisits(ctx, connect.NewRequest(req)); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("a stop off the Earth: %v", err)
	}
}
