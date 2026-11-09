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
	"bufio"
	"context"
	"encoding/csv"
	"errors"
	"fmt"
	"net/http"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"

	mimirv1 "github.com/gsoultan/mimir-server/gen/mimir/v1"
	"github.com/gsoultan/mimir-server/gen/mimir/v1/mimirv1connect"
	"github.com/gsoultan/mimir-server/internal/auth"
	"github.com/gsoultan/mimir-server/internal/dbsource"
	"github.com/gsoultan/mimir-server/internal/store"
)

// VisitPlanPath is where a plan's visits are downloaded as a CSV, with
// {id} for the plan's id.
const VisitPlanPath = "/api/visit-plans/{id}/visits.csv"

// VisitPlansAtOnce is how many plans the server makes at a time; each
// already plans several salespeople at once.
const VisitPlansAtOnce = 1

func noVisitPlan() error {
	return connect.NewError(connect.CodeNotFound, errors.New("No such visit plan."))
}

// planSources are a plan's rows as asked for, fetched and checked.
type planSources struct {
	plan        store.VisitPlan
	model       *store.Model
	customers   []byte
	salespeople []store.PlanSalesperson
	// keepSalespeople is the salespeople table to keep for a run, when
	// they came as a table.
	keepSalespeople []byte
	source          string
}

// planSettings reads and checks what a plan is asked to do, fetching its
// rows. most bounds the customers and salespeople.
func (s *Service) planSettings(ctx context.Context, msg *mimirv1.VisitPlanSettings, mostCustomers, mostPeople int) (planSources, error) {
	p, _ := auth.From(ctx)
	if msg == nil {
		return planSources{}, invalid("Say how to make the plan.")
	}
	out := planSources{}
	set := store.VisitPlanSettings{
		Days: int(msg.GetDays()), StartDate: strings.TrimSpace(msg.GetStartDate()), TimeZone: strings.TrimSpace(msg.GetTimeZone()),
		SkipVisitedDays: int(msg.GetSkipVisitedDays()), SpeedKmh: msg.GetSpeedKmh(), RoadFactor: msg.GetRoadFactor(),
		VisitMinutes: int(msg.GetVisitMinutes()),
	}
	switch {
	case set.Days < 1 || set.Days > maxPlanDays:
		return planSources{}, invalid("Plan 1 to %d days ahead.", maxPlanDays)
	case set.SkipVisitedDays < 0 || set.SkipVisitedDays > 90:
		return planSources{}, invalid("Leave out customers visited in the last 0 to 90 days.")
	case set.VisitMinutes < 0 || set.VisitMinutes > 12*60:
		return planSources{}, invalid("A visit takes 0 to 720 minutes.")
	case set.SpeedKmh != 0 && !(set.SpeedKmh > 0 && set.SpeedKmh <= 200):
		return planSources{}, invalid("Use a speed above 0 and up to 200 km/h.")
	case set.RoadFactor != 0 && !(set.RoadFactor >= 1 && set.RoadFactor <= 3):
		return planSources{}, invalid("Use a road factor from 1 to 3.")
	}
	for _, d := range msg.GetSkipWeekdays() {
		if d < 0 || d > 6 {
			return planSources{}, invalid("Weekdays to skip are 0 (Sunday) to 6 (Saturday).")
		}
		if !slices.Contains(set.SkipWeekdays, int(d)) {
			set.SkipWeekdays = append(set.SkipWeekdays, int(d))
		}
	}
	slices.Sort(set.SkipWeekdays)
	if _, err := planDates(set, time.Now()); err != nil {
		return planSources{}, invalid("%v", err)
	}
	if id := msg.GetModelId(); id != "" {
		m, err := s.get(ctx, id)
		if err != nil {
			return planSources{}, err
		}
		if _, err := newPlanModel(m); err != nil {
			return planSources{}, invalid("%v", err)
		}
		out.model, out.plan.ModelID = &m, m.ID
	}

	conn, cq, sq := msg.GetConnection(), strings.TrimSpace(msg.GetCustomersQuery()), strings.TrimSpace(msg.GetSalespeopleQuery())
	sources := 0
	for _, set := range []bool{len(msg.GetCustomersCsv()) > 0, cq != "", msg.GetCustomersDatasetId() != ""} {
		if set {
			sources++
		}
	}
	if sources != 1 {
		return planSources{}, invalid("Give the customers one way: a CSV file, a database query or a dataset.")
	}
	if (cq != "" || sq != "") && conn == nil {
		return planSources{}, invalid("Pick the database to query.")
	}
	if conn != nil && !p.Can(auth.DatabaseQuery) {
		return planSources{}, connect.NewError(connect.CodePermissionDenied, errors.New("Only admins can connect a database."))
	}
	switch {
	case len(msg.GetCustomersCsv()) > 0:
		if len(msg.GetCustomersCsv()) > maxCSVBytes {
			return planSources{}, invalid("That file is too big. Tables up to %d MB work here; for more customers, read them from a database.", maxCSVBytes>>20)
		}
		out.customers, out.source = msg.GetCustomersCsv(), "the uploaded file"
	case cq != "":
		r, err := s.fetchRows(ctx, conn, cq, mostCustomers)
		if err != nil {
			return planSources{}, err
		}
		out.customers, out.source = r, conn.GetDriver()+" query"
		out.plan.Driver, out.plan.DSN, out.plan.CustomersQuery = conn.GetDriver(), strings.TrimSpace(conn.GetDsn()), cq
	default:
		d, err := s.getDataset(ctx, msg.GetCustomersDatasetId())
		if err != nil {
			return planSources{}, err
		}
		if d.Kind != store.DatasetTable {
			return planSources{}, invalid("%s isn't a table; pick a table dataset.", d.Name)
		}
		if out.customers, err = s.store.ReadDataset(d.ID); err != nil {
			return planSources{}, s.internal(ctx, err)
		}
		out.source, out.plan.DatasetID = d.Name, d.ID
	}
	if n := countRows(out.customers); n > mostCustomers {
		return planSources{}, invalid("%d customers; at most %d can be planned here.", n, mostCustomers)
	}

	people := 0
	for _, set := range []bool{len(msg.GetSalespeople()) > 0, len(msg.GetSalespeopleCsv()) > 0, sq != ""} {
		if set {
			people++
		}
	}
	if people != 1 {
		return planSources{}, invalid("Give the salespeople one way: a list, a CSV file or a database query.")
	}
	var err error
	switch {
	case len(msg.GetSalespeople()) > 0:
		for _, sp := range msg.GetSalespeople() {
			set.Salespeople = append(set.Salespeople, store.PlanSalesperson{
				ID: strings.TrimSpace(sp.GetId()), Name: strings.TrimSpace(sp.GetName()), Area: strings.TrimSpace(sp.GetArea()),
				Lat: sp.GetStartLatitude(), Lon: sp.GetStartLongitude(), Start: int(sp.GetDayStartMinute()), End: int(sp.GetDayEndMinute()),
				MaxVisits: int(sp.GetMaxVisits()),
			})
		}
		out.salespeople = set.Salespeople
	case len(msg.GetSalespeopleCsv()) > 0:
		if len(msg.GetSalespeopleCsv()) > maxCSVBytes {
			return planSources{}, invalid("The salespeople file is too big.")
		}
		out.keepSalespeople, set.SalespeopleUpload = msg.GetSalespeopleCsv(), true
		out.salespeople, err = readSalespeople(out.keepSalespeople)
	default:
		if out.keepSalespeople, err = s.fetchRows(ctx, conn, sq, mostPeople); err != nil {
			return planSources{}, err
		}
		out.plan.SalespeopleQuery = sq
		if out.plan.Driver == "" {
			out.plan.Driver, out.plan.DSN = conn.GetDriver(), strings.TrimSpace(conn.GetDsn())
		}
		out.salespeople, err = readSalespeople(out.keepSalespeople)
	}
	if err != nil {
		return planSources{}, invalid("%v", err)
	}
	if out.salespeople, err = checkSalespeople(out.salespeople, mostPeople); err != nil {
		return planSources{}, invalid("%v", err)
	}
	header, _, err := readTable(firstLines(out.customers, 2))
	if err != nil {
		return planSources{}, invalid("%v", err)
	}
	if why := checkCustomerHeader(header, out.model, len(out.salespeople)); why != "" {
		return planSources{}, invalid("%s", why)
	}
	out.plan.Settings = set
	return out, nil
}

// firstLines returns the start of a CSV, up to n lines.
func firstLines(b []byte, n int) []byte {
	end := 0
	for range n {
		i := strings.IndexByte(string(b[end:]), '\n')
		if i < 0 {
			return b
		}
		end += i + 1
	}
	return b[:end]
}

// fetchRows runs a query on a database people connected, as a CSV.
func (s *Service) fetchRows(ctx context.Context, conn *mimirv1.Connection, query string, limit int) ([]byte, error) {
	r, err := s.fetchUpTo(ctx, conn, query, limit+1)
	if err != nil {
		return nil, err
	}
	return r.CSV(), nil
}

func (s *Service) SimulateVisitPlan(ctx context.Context, req *connect.Request[mimirv1.SimulateVisitPlanRequest]) (*connect.Response[mimirv1.SimulateVisitPlanResponse], error) {
	src, err := s.planSettings(ctx, req.Msg.GetSettings(), maxSimulateCustomers, maxSimulateSalespeople)
	if err != nil {
		return nil, err
	}
	out, err := s.makePlan(ctx, planInput{settings: src.plan.Settings, model: src.model, customers: src.customers, salespeople: src.salespeople, now: time.Now()})
	var pf planFailure
	switch {
	case errors.As(err, &pf):
		return nil, invalid("%s", pf)
	case err != nil && ctx.Err() != nil:
		return nil, connect.NewError(connect.CodeCanceled, errors.New("The plan was stopped."))
	case err != nil:
		return nil, s.internal(ctx, err)
	}
	p := src.plan
	p.Status, p.Summary, p.Created = store.JobSucceeded, out.summary, time.Now()
	res := &mimirv1.SimulateVisitPlanResponse{Plan: visitPlanProto(p, modelName(src.model), src.source)}
	for _, sp := range out.people {
		res.Salespeople = append(res.Salespeople, salespersonPlanProto(sp))
	}
	return connect.NewResponse(res), nil
}

func modelName(m *store.Model) string {
	if m == nil {
		return ""
	}
	return m.Name
}

func (s *Service) CreateVisitPlan(ctx context.Context, req *connect.Request[mimirv1.CreateVisitPlanRequest]) (*connect.Response[mimirv1.CreateVisitPlanResponse], error) {
	p, _ := auth.From(ctx)
	msg := req.Msg
	src, err := s.planSettings(ctx, msg.GetSettings(), MaxPlanCustomers, MaxPlanSalespeople)
	if err != nil {
		return nil, err
	}
	plan := src.plan
	plan.CreatedBy, plan.TeamID = p.Actor(), p.Team()
	if plan.RepeatSeconds, plan.Schedule, err = planRepeat(plan, msg.GetRepeatSeconds(), msg.GetSchedule()); err != nil {
		return nil, err
	}
	name := strings.TrimSpace(msg.GetName())
	if name == "" {
		name = fmt.Sprintf("Visits for %d days", plan.Settings.Days)
		if plan.Settings.StartDate != "" {
			if d, err := time.Parse(time.DateOnly, plan.Settings.StartDate); err == nil {
				name = "Visits from " + d.Format("Mon 2 Jan 2006")
			}
		} else if plan.RepeatSeconds > 0 {
			name = fmt.Sprintf("Visits, next %d days", plan.Settings.Days)
		}
	}
	if plan.Name, err = cleanName("plan", name); err != nil {
		return nil, err
	}
	if plan, err = s.store.CreateVisitPlan(ctx, plan, src.customers, src.keepSalespeople); err != nil {
		return nil, s.internal(ctx, err)
	}
	s.audit(ctx, p.Actor(), "visit_plan.created", plan.Name, fmt.Sprintf("%s, %d salespeople", src.source, len(src.salespeople)))
	s.KickVisitPlans()
	if plan, err = s.store.GetVisitPlan(ctx, plan.ID); err != nil {
		return nil, s.internal(ctx, err)
	}
	return connect.NewResponse(&mimirv1.CreateVisitPlanResponse{Plan: s.visitPlanProto(ctx, plan)}), nil
}

func (s *Service) ListVisitPlans(ctx context.Context, req *connect.Request[mimirv1.ListVisitPlansRequest]) (*connect.Response[mimirv1.ListVisitPlansResponse], error) {
	p, _ := auth.From(ctx)
	plans, err := s.store.ListVisitPlans(ctx, p.Team())
	if err != nil {
		return nil, s.internal(ctx, err)
	}
	names, err := s.modelNames(ctx, p)
	if err != nil {
		return nil, s.internal(ctx, err)
	}
	datasets, err := s.datasetNames(ctx, p.Team())
	if err != nil {
		return nil, s.internal(ctx, err)
	}
	res := &mimirv1.ListVisitPlansResponse{}
	for _, plan := range plans {
		name, ok := names[plan.ModelID]
		if plan.ModelID != "" && !ok {
			continue // a model outside the caller's access
		}
		res.Plans = append(res.Plans, visitPlanProto(plan, name, planSource(plan, datasets[plan.DatasetID])))
	}
	return connect.NewResponse(res), nil
}

func (s *Service) GetVisitPlan(ctx context.Context, req *connect.Request[mimirv1.GetVisitPlanRequest]) (*connect.Response[mimirv1.GetVisitPlanResponse], error) {
	plan, err := s.getVisitPlan(ctx, req.Msg.GetId())
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&mimirv1.GetVisitPlanResponse{Plan: s.visitPlanProto(ctx, plan)}), nil
}

func (s *Service) ListPlanSalespeople(ctx context.Context, req *connect.Request[mimirv1.ListPlanSalespeopleRequest]) (*connect.Response[mimirv1.ListPlanSalespeopleResponse], error) {
	msg := req.Msg
	plan, err := s.getVisitPlan(ctx, msg.GetPlanId())
	if err != nil {
		return nil, err
	}
	if o := msg.GetOrder(); o != "" && !slices.Contains([]string{"name", "covered", "visits", "left"}, o) {
		return nil, invalid("Order by name, covered, visits or left.")
	}
	if msg.GetPageSize() < 0 || msg.GetPageSize() > 200 || msg.GetOffset() < 0 {
		return nil, invalid("Ask for up to 200 salespeople a page.")
	}
	people, total, err := s.store.ListPlanSalespeople(ctx, plan.ID, store.PlanSalespeopleQuery{
		Area: msg.GetArea(), Search: msg.GetSearch(), NeedLook: msg.GetNeedLook(), Order: msg.GetOrder(),
		Limit: int(msg.GetPageSize()), Offset: int(msg.GetOffset()),
	})
	if err != nil {
		return nil, s.internal(ctx, err)
	}
	res := &mimirv1.ListPlanSalespeopleResponse{Total: int32(total)}
	for _, sp := range people {
		res.Salespeople = append(res.Salespeople, planSalespersonProto(sp))
	}
	return connect.NewResponse(res), nil
}

func (s *Service) GetSalespersonPlan(ctx context.Context, req *connect.Request[mimirv1.GetSalespersonPlanRequest]) (*connect.Response[mimirv1.GetSalespersonPlanResponse], error) {
	who := strings.TrimSpace(req.Msg.GetSalespersonId())
	if who == "" {
		return nil, invalid("Say which salesperson.")
	}
	if req.Msg.GetPlanId() == "" {
		return s.latestSalespersonPlan(ctx, who)
	}
	plan, err := s.getVisitPlan(ctx, req.Msg.GetPlanId())
	if err != nil {
		return nil, err
	}
	sp, err := s.store.GetPlanSalesperson(ctx, plan.ID, who)
	if errors.Is(err, store.ErrNotFound) {
		return nil, connect.NewError(connect.CodeNotFound, errors.New("That salesperson isn't in this plan."))
	}
	if err != nil {
		return nil, s.internal(ctx, err)
	}
	return connect.NewResponse(&mimirv1.GetSalespersonPlanResponse{Plan: s.visitPlanProto(ctx, plan), Salesperson: salespersonPlanProto(sp)}), nil
}

// latestSalespersonPlan answers a salesperson's plan asked for without a
// plan: the one of the caller's team made most recently that has them.
func (s *Service) latestSalespersonPlan(ctx context.Context, who string) (*connect.Response[mimirv1.GetSalespersonPlanResponse], error) {
	p, _ := auth.From(ctx)
	plans, err := s.store.ListVisitPlans(ctx, p.Team())
	if err != nil {
		return nil, s.internal(ctx, err)
	}
	plans = slices.DeleteFunc(plans, func(v store.VisitPlan) bool { return v.Runs == 0 || v.Ended.IsZero() })
	slices.SortFunc(plans, func(a, b store.VisitPlan) int { return b.Ended.Compare(a.Ended) })
	for _, v := range plans {
		sp, err := s.store.GetPlanSalesperson(ctx, v.ID, who)
		if errors.Is(err, store.ErrNotFound) {
			continue
		}
		if err != nil {
			return nil, s.internal(ctx, err)
		}
		plan, err := s.getVisitPlan(ctx, v.ID)
		if err != nil {
			continue // a plan whose model the caller may not see
		}
		return connect.NewResponse(&mimirv1.GetSalespersonPlanResponse{Plan: s.visitPlanProto(ctx, plan), Salesperson: salespersonPlanProto(sp)}), nil
	}
	return nil, connect.NewError(connect.CodeNotFound, errors.New("No plan made so far has that salesperson."))
}

func (s *Service) RunVisitPlan(ctx context.Context, req *connect.Request[mimirv1.RunVisitPlanRequest]) (*connect.Response[mimirv1.RunVisitPlanResponse], error) {
	plan, err := s.getVisitPlan(ctx, req.Msg.GetId())
	if err != nil {
		return nil, err
	}
	ok, err := s.store.RequeueVisitPlan(ctx, plan.ID)
	if err != nil {
		return nil, s.internal(ctx, err)
	}
	if !ok {
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("This plan is already waiting or being made."))
	}
	p, _ := auth.From(ctx)
	s.audit(ctx, p.Actor(), "visit_plan.run", plan.Name, "")
	s.KickVisitPlans()
	if plan, err = s.store.GetVisitPlan(ctx, plan.ID); err != nil {
		return nil, s.internal(ctx, err)
	}
	return connect.NewResponse(&mimirv1.RunVisitPlanResponse{Plan: s.visitPlanProto(ctx, plan)}), nil
}

func (s *Service) CancelVisitPlan(ctx context.Context, req *connect.Request[mimirv1.CancelVisitPlanRequest]) (*connect.Response[mimirv1.CancelVisitPlanResponse], error) {
	plan, err := s.getVisitPlan(ctx, req.Msg.GetId())
	if err != nil {
		return nil, err
	}
	ok, err := s.store.CancelVisitPlan(ctx, plan.ID)
	if err != nil {
		return nil, s.internal(ctx, err)
	}
	if !ok {
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("This plan isn't waiting or being made."))
	}
	s.planner.cancel(plan.ID)
	p, _ := auth.From(ctx)
	s.audit(ctx, p.Actor(), "visit_plan.cancelled", plan.Name, "")
	if plan, err = s.store.GetVisitPlan(ctx, plan.ID); err != nil {
		return nil, s.internal(ctx, err)
	}
	return connect.NewResponse(&mimirv1.CancelVisitPlanResponse{Plan: s.visitPlanProto(ctx, plan)}), nil
}

func (s *Service) SetVisitPlanRepeat(ctx context.Context, req *connect.Request[mimirv1.SetVisitPlanRepeatRequest]) (*connect.Response[mimirv1.SetVisitPlanRepeatResponse], error) {
	plan, err := s.getVisitPlan(ctx, req.Msg.GetId())
	if err != nil {
		return nil, err
	}
	seconds, sched, err := planRepeat(plan, req.Msg.GetRepeatSeconds(), req.Msg.GetSchedule())
	if err != nil {
		return nil, err
	}
	if err := s.store.SetVisitPlanRepeat(ctx, plan.ID, seconds, sched, plan.Settings.TimeZone); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil, noVisitPlan()
		}
		return nil, s.internal(ctx, err)
	}
	detail := "stopped"
	switch {
	case sched != nil:
		detail = scheduleText(sched)
	case seconds > 0:
		detail = fmt.Sprintf("every %s", time.Duration(seconds)*time.Second)
	}
	p, _ := auth.From(ctx)
	s.audit(ctx, p.Actor(), "visit_plan.repeat", plan.Name, detail)
	if plan, err = s.store.GetVisitPlan(ctx, plan.ID); err != nil {
		return nil, s.internal(ctx, err)
	}
	return connect.NewResponse(&mimirv1.SetVisitPlanRepeatResponse{Plan: s.visitPlanProto(ctx, plan)}), nil
}

func (s *Service) DeleteVisitPlan(ctx context.Context, req *connect.Request[mimirv1.DeleteVisitPlanRequest]) (*connect.Response[mimirv1.DeleteVisitPlanResponse], error) {
	plan, err := s.getVisitPlan(ctx, req.Msg.GetId())
	if err != nil {
		return nil, err
	}
	if err := s.store.DeleteVisitPlan(ctx, plan.ID); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("Cancel this plan before deleting it."))
		}
		return nil, s.internal(ctx, err)
	}
	p, _ := auth.From(ctx)
	s.audit(ctx, p.Actor(), "visit_plan.deleted", plan.Name, "")
	return connect.NewResponse(&mimirv1.DeleteVisitPlanResponse{}), nil
}

// planRepeat checks how a plan is to be made again: every seconds, or on
// a schedule (which wins; it is kept with a day as its seconds). Neither
// makes it once.
func planRepeat(plan store.VisitPlan, seconds int64, msg *mimirv1.PlanSchedule) (int64, *store.VisitSchedule, error) {
	var sched *store.VisitSchedule
	if msg != nil {
		if msg.GetAtMinute() < 0 || msg.GetAtMinute() >= 24*60 {
			return 0, nil, invalid("Pick a time of day from 00:00 to 23:59.")
		}
		sched = &store.VisitSchedule{AtMinute: int(msg.GetAtMinute())}
		for _, d := range msg.GetWeekdays() {
			if d < 0 || d > 6 {
				return 0, nil, invalid("Weekdays are 0 (Sunday) to 6 (Saturday).")
			}
			if !slices.Contains(sched.Weekdays, int(d)) {
				sched.Weekdays = append(sched.Weekdays, int(d))
			}
		}
		slices.Sort(sched.Weekdays)
		if len(sched.Weekdays) == 7 {
			sched.Weekdays = nil
		}
		seconds = 86400
	}
	if seconds == 0 {
		return 0, nil, nil
	}
	if plan.CustomersQuery == "" {
		return 0, nil, invalid("Only a plan whose customers come from a database can be made again on a schedule; an upload or a dataset stays the same.")
	}
	if seconds < minScoringRepeat || seconds > maxScoringRepeat {
		return 0, nil, invalid("Plan again every hour at most often, and at least every 30 days.")
	}
	if plan.Settings.StartDate != "" {
		return 0, nil, invalid("A plan made again on a schedule starts on the day it is made; leave the start date empty.")
	}
	return seconds, sched, nil
}

// scheduleText says when a schedule makes a plan: "06:00 on Mon, Tue".
func scheduleText(sc *store.VisitSchedule) string {
	at := fmt.Sprintf("%02d:%02d", sc.AtMinute/60, sc.AtMinute%60)
	if len(sc.Weekdays) == 0 {
		return at + " every day"
	}
	names := []string{"Sun", "Mon", "Tue", "Wed", "Thu", "Fri", "Sat"}
	var days []string
	for _, d := range sc.Weekdays {
		days = append(days, names[d])
	}
	return at + " on " + strings.Join(days, ", ")
}

// getVisitPlan returns a plan the caller may see; others read as missing.
func (s *Service) getVisitPlan(ctx context.Context, id string) (store.VisitPlan, error) {
	if !store.ValidID(id) {
		return store.VisitPlan{}, noVisitPlan()
	}
	plan, err := s.store.GetVisitPlan(ctx, id)
	if errors.Is(err, store.ErrNotFound) {
		return store.VisitPlan{}, noVisitPlan()
	}
	if err != nil {
		return store.VisitPlan{}, s.internal(ctx, err)
	}
	if p, _ := auth.From(ctx); plan.TeamID != p.Team() {
		return store.VisitPlan{}, noVisitPlan()
	}
	if plan.ModelID != "" {
		if _, err := s.get(ctx, plan.ModelID); err != nil && connect.CodeOf(err) == connect.CodeNotFound {
			// The model is gone or outside the caller's access. A
			// deleted model leaves the plan to see; one they may not
			// see hides it.
			if _, gone := s.store.Get(ctx, plan.ModelID); !errors.Is(gone, store.ErrNotFound) {
				return store.VisitPlan{}, noVisitPlan()
			}
		}
	}
	return plan, nil
}

func (s *Service) visitPlanProto(ctx context.Context, plan store.VisitPlan) *mimirv1.VisitPlan {
	name := ""
	if plan.ModelID != "" {
		if m, err := s.store.Get(ctx, plan.ModelID); err == nil {
			name = m.Name
		}
	}
	dataset := ""
	if plan.DatasetID != "" {
		if d, err := s.store.GetDataset(ctx, plan.DatasetID); err == nil {
			dataset = d.Name
		}
	}
	return visitPlanProto(plan, name, planSource(plan, dataset))
}

// planSource says where a plan's customers come from, in words.
func planSource(plan store.VisitPlan, datasetName string) string {
	switch {
	case plan.CustomersQuery != "":
		return plan.Driver + ": " + plan.CustomersQuery
	case plan.DatasetID != "":
		if datasetName == "" {
			return "a deleted dataset"
		}
		return datasetName
	}
	return "an uploaded file"
}

func visitPlanProto(plan store.VisitPlan, modelName, sourceDetail string) *mimirv1.VisitPlan {
	set, sum := plan.Settings, plan.Summary
	out := &mimirv1.VisitPlan{
		Id: plan.ID, Name: plan.Name, ModelId: plan.ModelID, ModelName: modelName, SourceDetail: sourceDetail,
		Status: mimirv1.JobStatus(plan.Status), Error: plan.Error, Stage: plan.Stage, Done: plan.Done, Total: plan.Total,
		Days: int32(set.Days), SkipVisitedDays: int32(set.SkipVisitedDays), TimeZone: set.TimeZone,
		StartDate: sum.StartDate, EndDate: sum.EndDate, Customers: sum.Customers, LeftOut: sum.LeftOut,
		Unassigned: sum.Unassigned, Unplaced: sum.Unplaced, Failed: sum.Failed, Salespeople: sum.Salespeople,
		Visits: sum.Visits, Km: sum.Km, Covered: sum.Covered, NeedLook: sum.NeedLook, Warnings: sum.Warnings,
		CreatedBy: plan.CreatedBy, CreateTime: timestamppb.New(plan.Created), RepeatSeconds: plan.RepeatSeconds, Runs: int32(plan.Runs),
	}
	for _, d := range set.SkipWeekdays {
		out.SkipWeekdays = append(out.SkipWeekdays, int32(d))
	}
	for _, a := range sum.Areas {
		out.Areas = append(out.Areas, &mimirv1.AreaCount{Area: a.Area, Salespeople: int32(a.Salespeople)})
	}
	switch {
	case plan.CustomersQuery != "":
		out.Source = "database"
	case plan.DatasetID != "":
		out.Source = "dataset"
	default:
		out.Source = "upload"
	}
	switch {
	case plan.SalespeopleQuery != "":
		out.SalespeopleSource = "database"
	case set.SalespeopleUpload:
		out.SalespeopleSource = "upload"
	default:
		out.SalespeopleSource = "list"
	}
	if !plan.Started.IsZero() {
		out.StartTime = timestamppb.New(plan.Started)
	}
	if !plan.Ended.IsZero() {
		out.EndTime = timestamppb.New(plan.Ended)
	}
	if sc := plan.Schedule; sc != nil && plan.RepeatSeconds > 0 {
		out.Schedule = &mimirv1.PlanSchedule{AtMinute: int32(sc.AtMinute)}
		for _, d := range sc.Weekdays {
			out.Schedule.Weekdays = append(out.Schedule.Weekdays, int32(d))
		}
	}
	if plan.RepeatSeconds > 0 && !plan.NextRun.IsZero() {
		out.NextRunTime = timestamppb.New(plan.NextRun)
	}
	return out
}

func planSalespersonProto(sp store.SalespersonPlan) *mimirv1.PlanSalesperson {
	return &mimirv1.PlanSalesperson{
		Id: sp.SalespersonID, Name: sp.Name, Area: sp.Area, Customers: int32(sp.Customers), Visits: int32(sp.Visits),
		Km: sp.Km, Covered: sp.Covered, LikelyLeft: int32(sp.LikelyLeft), Problem: sp.Problem,
	}
}

func plannedCustomer(c store.PlanStop) *mimirv1.PlannedCustomer {
	return &mimirv1.PlannedCustomer{Id: c.CustomerID, Name: c.Name, Latitude: c.Lat, Longitude: c.Lon, Chance: c.Priority, MustVisit: c.Must}
}

func salespersonPlanProto(sp store.SalespersonPlan) *mimirv1.SalespersonPlan {
	d := sp.Detail
	out := &mimirv1.SalespersonPlan{
		Salesperson: planSalespersonProto(sp), StartLatitude: d.Lat, StartLongitude: d.Lon,
		DayStartMinute: int32(d.Start), DayEndMinute: int32(d.End),
	}
	for _, day := range d.Days {
		pd := &mimirv1.PlanDay{Day: int32(day.Day), Date: day.Date, Km: day.Km, EndMinute: int32(day.End)}
		for _, v := range day.Visits {
			pd.Visits = append(pd.Visits, &mimirv1.DayVisit{Customer: plannedCustomer(v.PlanStop), ArriveMinute: int32(v.Arrive), LeaveMinute: int32(v.Leave), KmFromPrevious: v.Km})
		}
		out.Days = append(out.Days, pd)
	}
	for _, c := range d.Left {
		out.LikelyLeft = append(out.LikelyLeft, plannedCustomer(c))
	}
	return out
}

func visitPlanEvent(plan store.VisitPlan) map[string]any {
	return map[string]any{
		"id": plan.ID, "name": plan.Name, "model_id": plan.ModelID, "status": plan.Status.String(), "error": plan.Error,
		"start_date": plan.Summary.StartDate, "end_date": plan.Summary.EndDate, "salespeople": plan.Summary.Salespeople,
		"visits": plan.Summary.Visits, "covered": plan.Summary.Covered, "need_look": plan.Summary.NeedLook,
	}
}

// VisitPlanHandler serves GET VisitPlanPath: every visit of a plan's last
// run, as a CSV file, for a caller who may see the plan.
func (s *Service) VisitPlanHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx, err := auth.Check(r.Context(), s.store, s.MustEnroll, mimirv1connect.VisitServiceGetVisitPlanProcedure, r.Header, r.RemoteAddr)
		if err != nil {
			uploadError(w, err)
			return
		}
		plan, err := s.getVisitPlan(ctx, r.PathValue("id"))
		if err != nil {
			uploadError(w, err)
			return
		}
		if plan.Runs == 0 {
			uploadError(w, connect.NewError(connect.CodeNotFound, errors.New("This plan hasn't been made yet.")))
			return
		}
		w.Header().Set("Content-Type", "text/csv; charset=utf-8")
		w.Header().Set("Content-Disposition", `attachment; filename="`+downloadName(plan.Name)+`.csv"`)
		w.Header().Set("Cache-Control", "no-store")
		bw := bufio.NewWriterSize(w, 1<<16)
		cw := csv.NewWriter(bw)
		cw.Write([]string{"salesperson_id", "salesperson", "area", "date", "day", "order", "customer_id", "customer", "latitude", "longitude", "arrive", "leave", "km_from_previous", "chance", "must_visit"})
		f := func(v float64) string { return strconv.FormatFloat(v, 'f', -1, 64) }
		hm := func(m int) string { return fmt.Sprintf("%02d:%02d", m/60, m%60) }
		err = s.store.AllPlanSalespeople(ctx, plan.ID, func(sp store.SalespersonPlan) error {
			for _, d := range sp.Detail.Days {
				for i, v := range d.Visits {
					must := "no"
					if v.Must {
						must = "yes"
					}
					cw.Write([]string{sp.SalespersonID, sp.Name, sp.Area, d.Date, strconv.Itoa(d.Day), strconv.Itoa(i + 1), v.CustomerID, v.Name,
						f(v.Lat), f(v.Lon), hm(v.Arrive), hm(v.Leave), f(v.Km), strconv.FormatFloat(v.Priority, 'f', 4, 64), must})
				}
			}
			return cw.Error()
		})
		cw.Flush()
		if err == nil {
			err = bw.Flush()
		}
		if err != nil {
			s.log.WarnContext(ctx, "visit plan download", "plan_id", plan.ID, "error", err)
		}
	})
}

// KickVisitPlans asks the plan runner to look at the queue now.
func (s *Service) KickVisitPlans() {
	s.planner.init()
	select {
	case s.planner.kick <- struct{}{}:
	default:
	}
}

// RunVisitPlans makes queued plans, VisitPlansAtOnce at a time, and
// queues repeating ones when they are due, until ctx ends. A plan cut
// short goes back in the queue at the next start.
func (s *Service) RunVisitPlans(ctx context.Context, every time.Duration) {
	s.planner.init()
	var wg sync.WaitGroup
	defer wg.Wait()
	t := time.NewTicker(every)
	defer t.Stop()
	slots := make(chan struct{}, VisitPlansAtOnce)
	for {
		if _, err := s.store.QueueDueVisitPlans(ctx, time.Now()); err != nil && ctx.Err() == nil {
			s.log.ErrorContext(ctx, "visit plan: queue due", "error", err)
		}
	claim:
		for {
			select {
			case slots <- struct{}{}:
			default:
				break claim
			}
			s.planner.mu.Lock()
			skip := make([]string, 0, len(s.planner.running))
			for id := range s.planner.running {
				skip = append(skip, id)
			}
			s.planner.mu.Unlock()
			plan, err := s.store.ClaimVisitPlan(ctx, skip...)
			if err != nil {
				<-slots
				if !errors.Is(err, store.ErrNotFound) && ctx.Err() == nil {
					s.log.ErrorContext(ctx, "visit plan: claim", "error", err)
				}
				break
			}
			rctx, cancel := context.WithCancel(ctx)
			s.planner.mu.Lock()
			s.planner.running[plan.ID] = cancel
			s.planner.mu.Unlock()
			wg.Go(func() {
				defer func() {
					s.planner.mu.Lock()
					delete(s.planner.running, plan.ID)
					s.planner.mu.Unlock()
					cancel()
					<-slots
					s.KickVisitPlans()
				}()
				s.runVisitPlan(rctx, plan)
			})
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		case <-s.planner.kick:
		}
	}
}

// planRows reads a plan's customers and salespeople: kept from an upload
// or from when it was made, or fetched again.
func (s *Service) planRows(ctx context.Context, plan store.VisitPlan) ([]byte, []store.PlanSalesperson, string) {
	var customers []byte
	kept := s.store.VisitPlanCustomersPath(plan.ID)
	if b, err := os.ReadFile(kept); err == nil {
		if !plan.Upload() {
			os.Remove(kept) // fetched once, for the first run
		}
		customers = b
	} else {
		switch {
		case plan.Upload():
			return nil, nil, "The uploaded customers are gone. Make a new plan with the file."
		case plan.DatasetID != "":
			if customers, err = s.store.ReadDataset(plan.DatasetID); err != nil {
				return nil, nil, "The dataset the customers came from was deleted."
			}
		default:
			r, err := dbsource.Fetch(ctx, plan.Driver, plan.DSN, plan.CustomersQuery, MaxPlanCustomers+1, s.store.RefuseOwnDatabase)
			if err != nil {
				return nil, nil, fmt.Sprintf("The customers query didn't give rows: %v", strings.TrimPrefix(err.Error(), "dbsource: bad query: "))
			}
			customers = r.CSV()
		}
	}
	if len(plan.Settings.Salespeople) > 0 {
		return customers, plan.Settings.Salespeople, ""
	}
	var table []byte
	kept = s.store.VisitPlanSalespeoplePath(plan.ID)
	if b, err := os.ReadFile(kept); err == nil {
		if !plan.Settings.SalespeopleUpload {
			os.Remove(kept)
		}
		table = b
	} else {
		if plan.Settings.SalespeopleUpload || plan.SalespeopleQuery == "" {
			return nil, nil, "The uploaded salespeople are gone. Make a new plan with the file."
		}
		r, err := dbsource.Fetch(ctx, plan.Driver, plan.DSN, plan.SalespeopleQuery, MaxPlanSalespeople+1, s.store.RefuseOwnDatabase)
		if err != nil {
			return nil, nil, fmt.Sprintf("The salespeople query didn't give rows: %v", strings.TrimPrefix(err.Error(), "dbsource: bad query: "))
		}
		table = r.CSV()
	}
	people, err := readSalespeople(table)
	if err != nil {
		return nil, nil, err.Error()
	}
	return customers, people, ""
}

// runVisitPlan makes one claimed plan and records how it went.
func (s *Service) runVisitPlan(ctx context.Context, plan store.VisitPlan) {
	start := time.Now()
	failure := ""
	var out planOutcome
	customers, people, why := s.planRows(ctx, plan)
	if why != "" {
		failure = why
	} else {
		var model *store.Model
		if plan.ModelID != "" {
			m, err := s.store.Get(ctx, plan.ModelID)
			if err != nil {
				failure = "The model was deleted."
			} else {
				model = &m
			}
		}
		if failure == "" {
			last := time.Now()
			progress := func(stage string, done, total int64) bool {
				if time.Since(last) < scoringCheck && done != 0 {
					return true
				}
				last = time.Now()
				if st, err := s.store.VisitPlanStatus(ctx, plan.ID); err == nil && st != store.JobRunning {
					return false // cancelled, maybe on another server
				}
				if err := s.store.VisitPlanProgress(ctx, plan.ID, stage, done, total); err != nil && ctx.Err() == nil {
					s.log.WarnContext(ctx, "visit plan: progress", "plan_id", plan.ID, "error", err)
				}
				return true
			}
			var err error
			out, err = s.makePlan(ctx, planInput{settings: plan.Settings, model: model, customers: customers, salespeople: people, now: time.Now(), progress: progress})
			var pf planFailure
			switch {
			case errors.As(err, &pf):
				failure = string(pf)
			case err != nil:
				if ctx.Err() != nil || errors.Is(err, errPlanStopped) {
					return // cancelled, or the server is stopping: queued again at start
				}
				s.log.ErrorContext(ctx, "visit plan", "plan_id", plan.ID, "error", err)
				failure = "Something went wrong making the plan."
			}
		}
	}
	if ctx.Err() != nil {
		return
	}
	done := context.WithoutCancel(ctx)
	finished, err := s.store.FinishVisitPlan(done, plan.ID, out.summary, out.people, failure)
	if err != nil {
		s.log.ErrorContext(done, "visit plan: finish", "plan_id", plan.ID, "error", err)
		return
	}
	if !finished {
		return // cancelled meanwhile
	}
	if plan, err = s.store.GetVisitPlan(done, plan.ID); err != nil {
		return
	}
	if failure == "" {
		s.log.InfoContext(done, "visit plan made", "plan_id", plan.ID, "salespeople", plan.Summary.Salespeople, "visits", plan.Summary.Visits, "took", time.Since(start).Round(time.Millisecond))
		s.auditIn(done, plan.TeamID, "server", "visit_plan.finished", plan.Name, fmt.Sprintf("%d visits for %d salespeople", plan.Summary.Visits, plan.Summary.Salespeople))
		s.emitIn(done, plan.TeamID, "visit_plan.finished", visitPlanEvent(plan))
		return
	}
	s.log.WarnContext(done, "visit plan failed", "plan_id", plan.ID, "error", failure)
	s.auditIn(done, plan.TeamID, "server", "visit_plan.failed", plan.Name, failure)
	s.emitIn(done, plan.TeamID, "visit_plan.failed", visitPlanEvent(plan))
	s.Notifier.Notify(done, "Visit plan failed: "+plan.Name, failure)
}
