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

package api

import (
	"context"
	"errors"
	"strings"

	"connectrpc.com/connect"

	mimirv1 "github.com/gsoultan/mimir-server/gen/mimir/v1"
	"github.com/gsoultan/mimir-server/gen/mimir/v1/mimirv1connect"
	"github.com/gsoultan/mimir-server/internal/visit"
)

var _ mimirv1connect.VisitServiceHandler = (*Service)(nil)

// PlanVisits plans salespeople's visit routes.
func (s *Service) PlanVisits(ctx context.Context, req *connect.Request[mimirv1.PlanVisitsRequest]) (*connect.Response[mimirv1.PlanVisitsResponse], error) {
	m := req.Msg
	in := visit.Request{Days: int(m.GetDays()), SpeedKmh: m.GetSpeedKmh(), RoadFactor: m.GetRoadFactor()}
	for _, st := range m.GetStops() {
		in.Stops = append(in.Stops, visit.Stop{
			ID: st.GetId(), Name: st.GetName(), Lat: st.GetLatitude(), Lon: st.GetLongitude(), Priority: st.GetPriority(),
			Minutes: int(st.GetVisitMinutes()), Open: int(st.GetOpenMinute()), Close: int(st.GetCloseMinute()),
			Salesperson: st.GetSalespersonId(), Must: st.GetMustVisit(),
		})
	}
	for _, sp := range m.GetSalespeople() {
		in.Salespeople = append(in.Salespeople, visit.Salesperson{
			ID: sp.GetId(), Name: sp.GetName(), Lat: sp.GetStartLatitude(), Lon: sp.GetStartLongitude(),
			Start: int(sp.GetDayStartMinute()), End: int(sp.GetDayEndMinute()), MaxVisits: int(sp.GetMaxVisits()),
		})
	}
	plan, err := visit.Make(in)
	if errors.Is(err, visit.ErrBad) {
		msg := strings.TrimPrefix(err.Error(), "visit: bad request: ")
		return nil, invalid("%s.", strings.ToUpper(msg[:1])+msg[1:])
	}
	if err != nil {
		return nil, s.internal(ctx, err)
	}
	res := &mimirv1.PlanVisitsResponse{UnplannedStopIds: plan.Unplanned, TotalKm: plan.Km, PlannedPriority: plan.Planned, TotalPriority: plan.TotalPriority}
	for _, r := range plan.Routes {
		pr := &mimirv1.Route{SalespersonId: r.Salesperson, Day: int32(r.Day), Km: r.Km, EndMinute: int32(r.End)}
		for _, v := range r.Visits {
			pr.Visits = append(pr.Visits, &mimirv1.PlannedVisit{StopId: v.Stop, ArriveMinute: int32(v.Arrive), LeaveMinute: int32(v.Leave), KmFromPrevious: v.Km})
		}
		res.Routes = append(res.Routes, pr)
	}
	return connect.NewResponse(res), nil
}
