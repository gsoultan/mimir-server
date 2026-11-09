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

package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/gsoultan/storm"
	"github.com/gsoultan/storm/runtime"

	"github.com/gsoultan/mimir-server/internal/store/db/visitplan"
	"github.com/gsoultan/mimir-server/internal/store/db/visitplansalesperson"
)

// VisitPlan plans salespeople's visits for the coming days, once or on a
// schedule: a model says how likely each customer is to order, and each
// salesperson's days are planned with the likeliest first. Customers come
// from an upload (an input file), a database query (Driver, DSN and
// CustomersQuery) or a table dataset (DatasetID); salespeople from the
// settings, an uploaded file or SalespeopleQuery on the same database.
// DSN is a secret and never leaves the server.
type VisitPlan struct {
	ID               string
	Name             string
	ModelID          string // "" plans by the customers' own priority column
	DatasetID        string
	Driver           string
	DSN              string
	CustomersQuery   string
	SalespeopleQuery string
	Settings         VisitPlanSettings
	Status           JobStatus
	Error            string
	Stage            string
	Done, Total      int64
	Summary          VisitPlanSummary
	CreatedBy        string
	Created          time.Time
	Started          time.Time
	Ended            time.Time
	TeamID           string
	RepeatSeconds    int64
	NextRun          time.Time
	Runs             int64
}

// Upload reports whether the plan's customers were uploaded, so they
// can't be fetched again.
func (p VisitPlan) Upload() bool { return p.Driver == "" && p.DatasetID == "" }

// VisitPlanSettings is how a plan is made, beyond where its rows come
// from.
type VisitPlanSettings struct {
	// Days is how many days ahead to plan, from StartDate (a YYYY-MM-DD
	// date; "" is the day the plan runs, in TimeZone).
	Days      int    `json:"days"`
	StartDate string `json:"start_date,omitempty"`
	TimeZone  string `json:"time_zone,omitempty"`
	// SkipWeekdays are days nobody visits: 0 is Sunday, 6 Saturday.
	SkipWeekdays []int `json:"skip_weekdays,omitempty"`
	// SkipVisitedDays leaves out customers visited fewer than that many
	// days ago, by anyone, unless they must be visited. 0 keeps them.
	SkipVisitedDays int     `json:"skip_visited_days,omitempty"`
	SpeedKmh        float64 `json:"speed_kmh,omitempty"`
	RoadFactor      float64 `json:"road_factor,omitempty"`
	VisitMinutes    int     `json:"visit_minutes,omitempty"`
	// Salespeople were typed in; SalespeopleUpload says they were
	// uploaded as a file instead. Neither, with SalespeopleQuery, reads
	// them from the database.
	Salespeople       []PlanSalesperson `json:"salespeople,omitempty"`
	SalespeopleUpload bool              `json:"salespeople_upload,omitempty"`
}

// PlanSalesperson is a salesperson a plan is made for.
type PlanSalesperson struct {
	ID        string  `json:"id"`
	Name      string  `json:"name,omitempty"`
	Area      string  `json:"area,omitempty"`
	Lat       float64 `json:"lat"`
	Lon       float64 `json:"lon"`
	Start     int     `json:"start,omitempty"` // minutes after midnight
	End       int     `json:"end,omitempty"`
	MaxVisits int     `json:"max_visits,omitempty"`
}

// VisitPlanSummary is what a plan's last run made, for the whole team.
type VisitPlanSummary struct {
	StartDate, EndDate string
	// Customers is how many customers were read; LeftOut how many were
	// left out as visited lately, Unassigned how many have no salesperson
	// on the list, Unplaced how many have no place, Failed how many the
	// model couldn't answer.
	Customers, LeftOut, Unassigned, Unplaced, Failed int64
	Salespeople, Visits                              int64
	Km                                               float64
	// Covered is the share of the customers' total chance the visits
	// reach, from 0 to 1.
	Covered  float64
	NeedLook int64
	Warnings []string
	Areas    []AreaCount
}

// AreaCount is how many salespeople an area has.
type AreaCount struct {
	Area        string `json:"area"`
	Salespeople int    `json:"salespeople"`
}

// SalespersonPlan is one salesperson's part of a plan's last run.
type SalespersonPlan struct {
	SalespersonID string
	Name, Area    string
	Customers     int64
	Visits        int64
	Km            float64
	Covered       float64
	// LikelyLeft is how many likely customers didn't fit.
	LikelyLeft int64
	Problem    string
	NeedLook   bool
	Detail     SalespersonDetail
}

// SalespersonDetail is a salesperson's days of visits and the likely
// customers that didn't fit.
type SalespersonDetail struct {
	Lat   float64    `json:"lat"`
	Lon   float64    `json:"lon"`
	Start int        `json:"start"`
	End   int        `json:"end"`
	Days  []PlanDay  `json:"days"`
	Left  []PlanStop `json:"left,omitempty"`
}

// PlanDay is one day of a salesperson's visits.
type PlanDay struct {
	Day    int         `json:"day"`  // from 1
	Date   string      `json:"date"` // YYYY-MM-DD
	Km     float64     `json:"km"`
	End    int         `json:"end"` // back at the start, minutes after midnight
	Visits []PlanVisit `json:"visits"`
}

// PlanVisit is one visit on a day.
type PlanVisit struct {
	PlanStop
	Arrive int     `json:"arrive"`
	Leave  int     `json:"leave"`
	Km     float64 `json:"km"` // from the stop before, or the start
}

// PlanStop is a customer as a plan sees them.
type PlanStop struct {
	CustomerID string  `json:"id"`
	Name       string  `json:"name,omitempty"`
	Lat        float64 `json:"lat"`
	Lon        float64 `json:"lon"`
	Priority   float64 `json:"priority"`
	Must       bool    `json:"must,omitempty"`
}

func visitPlanContext(id string) string { return "visit_plans.dsn:" + id }

// VisitPlanDir is where plans' uploads live.
func (s *Store) VisitPlanDir() string { return filepath.Join(s.Dir(), "visit_plans") }

// VisitPlanCustomersPath is where a plan's uploaded or fetched customers
// wait for its first run.
func (s *Store) VisitPlanCustomersPath(id string) string {
	return filepath.Join(s.VisitPlanDir(), id+".customers.csv")
}

// VisitPlanSalespeoplePath is where a plan's uploaded or fetched
// salespeople wait for its first run.
func (s *Store) VisitPlanSalespeoplePath(id string) string {
	return filepath.Join(s.VisitPlanDir(), id+".salespeople.csv")
}

func (s *Store) fromVisitPlan(r visitplan.Row) (VisitPlan, error) {
	dsn, err := s.openSecret(r.Dsn, visitPlanContext(r.ID))
	if err != nil {
		return VisitPlan{}, err
	}
	var set VisitPlanSettings
	if err := json.Unmarshal(r.Settings, &set); err != nil {
		return VisitPlan{}, fmt.Errorf("store: visit plan settings: %w", err)
	}
	var areas []AreaCount
	json.Unmarshal(r.Areas, &areas) // a bad one only loses the counts
	return VisitPlan{
		ID: r.ID, Name: r.Name, ModelID: r.ModelID, DatasetID: r.DatasetID,
		Driver: r.Driver, DSN: dsn, CustomersQuery: r.CustomersQuery, SalespeopleQuery: r.SalespeopleQuery,
		Settings: set, Status: JobStatus(r.Status), Error: r.Error, Stage: r.Stage, Done: r.Done, Total: r.Total,
		Summary: VisitPlanSummary{
			StartDate: r.StartDate, EndDate: r.EndDate, Customers: r.Customers, LeftOut: r.LeftOut,
			Unassigned: r.Unassigned, Unplaced: r.Unplaced, Failed: r.Failed, Salespeople: r.Salespeople,
			Visits: r.Visits, Km: r.Km, Covered: r.Covered, NeedLook: r.NeedLook, Warnings: fromStrs(r.Warnings), Areas: areas,
		},
		CreatedBy: r.CreatedBy, Created: r.CreateTime.UTC(), Started: timeOf(r.StartTime), Ended: timeOf(r.EndTime),
		TeamID: r.TeamID.V, RepeatSeconds: r.RepeatSeconds, NextRun: timeOf(r.NextRun), Runs: r.Runs,
	}, nil
}

// CreateVisitPlan queues a plan, filling in ID, Created and Status.
// customers and salespeople, when set, are rows kept as files for its
// first run (and for good, when they were uploaded).
func (s *Store) CreateVisitPlan(ctx context.Context, p VisitPlan, customers, salespeople []byte) (VisitPlan, error) {
	id, err := newID()
	if err != nil {
		return VisitPlan{}, err
	}
	p.ID, p.Created, p.Status = id, now(), JobQueued
	if p.TeamID == "" {
		p.TeamID = DefaultTeam
	}
	set, err := json.Marshal(p.Settings)
	if err != nil {
		return VisitPlan{}, fmt.Errorf("store: visit plan settings: %w", err)
	}
	if err := os.MkdirAll(s.VisitPlanDir(), 0o700); err != nil {
		return VisitPlan{}, fmt.Errorf("store: visit plan folder: %w", err)
	}
	for path, b := range map[string][]byte{s.VisitPlanCustomersPath(id): customers, s.VisitPlanSalespeoplePath(id): salespeople} {
		if b == nil {
			continue
		}
		if err := writeFileAtomic(path, b); err != nil {
			s.removeVisitPlanFiles(id)
			return VisitPlan{}, fmt.Errorf("store: write visit plan rows: %w", err)
		}
	}
	dsn, err := s.box.Seal(p.DSN, visitPlanContext(id))
	if err != nil {
		s.removeVisitPlanFiles(id)
		return VisitPlan{}, err
	}
	n := visitplan.Create()
	n.SetID(id)
	n.SetCreateTime(p.Created)
	n.SetName(p.Name)
	n.SetTeamID(p.TeamID)
	n.SetModelID(p.ModelID)
	n.SetDatasetID(p.DatasetID)
	n.SetDriver(p.Driver)
	n.SetDsn(dsn)
	n.SetCustomersQuery(p.CustomersQuery)
	n.SetSalespeopleQuery(p.SalespeopleQuery)
	n.SetSettings(set)
	n.SetStatus(int32(p.Status))
	n.SetCreatedBy(p.CreatedBy)
	n.SetRepeatSeconds(p.RepeatSeconds)
	if p.RepeatSeconds > 0 {
		next := p.Created.Add(time.Duration(p.RepeatSeconds) * time.Second)
		n.SetNextRun(next)
		p.NextRun = next
	}
	if _, err := n.Insert(ctx, s.ex); err != nil {
		s.removeVisitPlanFiles(id)
		return VisitPlan{}, fmt.Errorf("store: insert visit plan: %w", err)
	}
	return p, nil
}

// GetVisitPlan returns one plan.
func (s *Store) GetVisitPlan(ctx context.Context, id string) (VisitPlan, error) {
	r, ok, err := visitplan.New().Where(visitplan.ID.Eq(id)).One(ctx, s.ex)
	if err != nil {
		return VisitPlan{}, err
	}
	if !ok {
		return VisitPlan{}, ErrNotFound
	}
	return s.fromVisitPlan(r)
}

// ListVisitPlans returns a team's plans, newest first.
func (s *Store) ListVisitPlans(ctx context.Context, teamID string) ([]VisitPlan, error) {
	rows, err := visitplan.New().
		Where(visitplan.TeamID.Eq(teamID)).
		Order(visitplan.CreateTime.Desc(), visitplan.ID.Desc()).
		Limit(1000).
		All(ctx, s.ex, nil)
	if err != nil {
		return nil, fmt.Errorf("store: list visit plans: %w", err)
	}
	out := make([]VisitPlan, 0, len(rows))
	for _, r := range rows {
		p, err := s.fromVisitPlan(r)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, nil
}

// ClaimVisitPlan takes the oldest queued plan not in skip and marks it
// running. It returns ErrNotFound when there is none.
func (s *Store) ClaimVisitPlan(ctx context.Context, skip ...string) (VisitPlan, error) {
	var p VisitPlan
	err := storm.InTx(ctx, s.ex, func(ex storm.Executor) error {
		r, ok, err := visitplan.New().
			Where(visitplan.Status.Eq(int32(JobQueued))).
			WhereIf(len(skip) > 0, visitplan.ID.NotIn(skip...)).
			Order(visitplan.CreateTime.Asc(), visitplan.ID.Asc()).
			ForUpdateSkipLocked().
			One(ctx, ex)
		if err != nil {
			return err
		}
		if !ok {
			return ErrNotFound
		}
		t := now()
		m := visitplan.Mutate(r)
		m.SetStatus(int32(JobRunning))
		m.SetStartTime(t)
		m.SetEndTimeNull()
		m.SetError("")
		m.SetStage("")
		m.SetDone(0)
		m.SetTotal(0)
		if err := m.Update(ctx, ex); err != nil {
			return err
		}
		if p, err = s.fromVisitPlan(r); err != nil {
			return err
		}
		p.Status, p.Started, p.Ended, p.Error, p.Stage, p.Done, p.Total = JobRunning, t, time.Time{}, "", "", 0, 0
		return nil
	})
	if err != nil {
		return VisitPlan{}, err
	}
	return p, nil
}

// VisitPlanProgress records what a running plan is doing and how far it
// is. A plan deleted meanwhile is not an error.
func (s *Store) VisitPlanProgress(ctx context.Context, id, stage string, done, total int64) error {
	m := visitplan.MutateKey(id)
	m.SetStage(stage)
	m.SetDone(done)
	m.SetTotal(total)
	if err := m.Update(ctx, s.ex); err != nil && !errors.Is(err, runtime.ErrNoRow) {
		return fmt.Errorf("store: visit plan progress: %w", err)
	}
	return nil
}

// VisitPlanStatus returns just a plan's status, for a runner polling for
// cancel.
func (s *Store) VisitPlanStatus(ctx context.Context, id string) (JobStatus, error) {
	r, ok, err := visitplan.New().Where(visitplan.ID.Eq(id)).One(ctx, s.ex)
	if err != nil {
		return 0, err
	}
	if !ok {
		return 0, ErrNotFound
	}
	return JobStatus(r.Status), nil
}

// FinishVisitPlan ends a running plan: with what it made, replacing the
// last run's salespeople, or failed with why; and schedules its next run
// when it repeats. A plan cancelled meanwhile stays cancelled, and
// FinishVisitPlan reports false.
func (s *Store) FinishVisitPlan(ctx context.Context, id string, sum VisitPlanSummary, people []SalespersonPlan, failure string) (bool, error) {
	finished := false
	err := storm.InTx(ctx, s.ex, func(ex storm.Executor) error {
		r, ok, err := visitplan.New().
			Where(visitplan.ID.Eq(id), visitplan.Status.Eq(int32(JobRunning))).
			ForUpdate().
			One(ctx, ex)
		if err != nil || !ok {
			return err
		}
		t := now()
		m := visitplan.Mutate(r)
		m.SetEndTime(t)
		m.SetStage("")
		if failure != "" {
			m.SetStatus(int32(JobFailed))
			m.SetError(failure)
		} else {
			if _, err := visitplansalesperson.New().Where(visitplansalesperson.PlanID.Eq(id)).DeleteAll(ctx, ex); err != nil {
				return err
			}
			rows := make([]visitplansalesperson.Row, 0, len(people))
			for _, sp := range people {
				rid, err := newID()
				if err != nil {
					return err
				}
				detail, err := json.Marshal(sp.Detail)
				if err != nil {
					return err
				}
				rows = append(rows, visitplansalesperson.Row{
					ID: rid, CreateTime: t, PlanID: id, SalespersonID: sp.SalespersonID, Name: sp.Name, Area: sp.Area,
					SearchText: strings.ToLower(sp.SalespersonID + " " + sp.Name + " " + sp.Area),
					Customers:  sp.Customers, Visits: sp.Visits, Km: sp.Km, Covered: sp.Covered,
					LikelyLeft: sp.LikelyLeft, Problem: sp.Problem, NeedLook: sp.NeedLook, Detail: detail,
				})
			}
			for len(rows) > 0 {
				n := min(len(rows), 500)
				if _, err := visitplansalesperson.InsertAll(ctx, ex, rows[:n]); err != nil {
					return err
				}
				rows = rows[n:]
			}
			areas, err := json.Marshal(sum.Areas)
			if err != nil {
				return err
			}
			if sum.Areas == nil {
				areas = []byte("[]")
			}
			m.SetStatus(int32(JobSucceeded))
			m.SetError("")
			m.SetStartDate(sum.StartDate)
			m.SetEndDate(sum.EndDate)
			m.SetCustomers(sum.Customers)
			m.SetLeftOut(sum.LeftOut)
			m.SetUnassigned(sum.Unassigned)
			m.SetUnplaced(sum.Unplaced)
			m.SetFailed(sum.Failed)
			m.SetSalespeople(sum.Salespeople)
			m.SetVisits(sum.Visits)
			m.SetKm(sum.Km)
			m.SetCovered(sum.Covered)
			m.SetNeedLook(sum.NeedLook)
			m.SetWarnings(strs(sum.Warnings))
			m.SetAreas(areas)
			m.SetRuns(r.Runs + 1)
		}
		if r.RepeatSeconds > 0 {
			m.SetNextRun(t.Add(time.Duration(r.RepeatSeconds) * time.Second))
		}
		finished = true
		return m.Update(ctx, ex)
	})
	if err != nil {
		return false, fmt.Errorf("store: finish visit plan: %w", err)
	}
	return finished, nil
}

// PlanSalespeopleQuery picks a plan's salespeople to list.
type PlanSalespeopleQuery struct {
	Area     string
	Search   string // in their id, name or area
	NeedLook bool
	// Order is "name" (the default), "covered" (least first), "visits"
	// (most first) or "left" (most likely customers left over first).
	Order         string
	Limit, Offset int
}

// ListPlanSalespeople returns one page of a plan's salespeople and how
// many match in all.
func (s *Store) ListPlanSalespeople(ctx context.Context, planID string, q PlanSalespeopleQuery) ([]SalespersonPlan, int64, error) {
	pattern := ""
	if t := strings.ToLower(strings.TrimSpace(q.Search)); t != "" {
		r := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
		pattern = "%" + r.Replace(t) + "%"
	}
	base := visitplansalesperson.New().
		Where(visitplansalesperson.PlanID.Eq(planID)).
		WhereIf(q.Area != "", visitplansalesperson.Area.Eq(q.Area)).
		WhereIf(pattern != "", visitplansalesperson.SearchText.Like(pattern)).
		WhereIf(q.NeedLook, visitplansalesperson.NeedLook.Eq(true))
	total, err := base.Count(ctx, s.ex)
	if err != nil {
		return nil, 0, fmt.Errorf("store: count plan salespeople: %w", err)
	}
	var order []visitplansalesperson.Sort
	switch q.Order {
	case "covered":
		order = []visitplansalesperson.Sort{visitplansalesperson.NeedLook.Desc(), visitplansalesperson.Covered.Asc()}
	case "visits":
		order = []visitplansalesperson.Sort{visitplansalesperson.Visits.Desc()}
	case "left":
		order = []visitplansalesperson.Sort{visitplansalesperson.LikelyLeft.Desc()}
	}
	order = append(order, visitplansalesperson.Name.Asc(), visitplansalesperson.SalespersonID.Asc())
	limit := q.Limit
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := base.Order(order...).Limit(int64(limit)).Offset(int64(max(q.Offset, 0))).All(ctx, s.ex, nil)
	if err != nil {
		return nil, 0, fmt.Errorf("store: list plan salespeople: %w", err)
	}
	out := make([]SalespersonPlan, 0, len(rows))
	for _, r := range rows {
		out = append(out, fromPlanSalesperson(r, false))
	}
	return out, total, nil
}

// GetPlanSalesperson returns one salesperson's part of a plan, with
// their days.
func (s *Store) GetPlanSalesperson(ctx context.Context, planID, salespersonID string) (SalespersonPlan, error) {
	r, ok, err := visitplansalesperson.New().
		Where(visitplansalesperson.PlanID.Eq(planID), visitplansalesperson.SalespersonID.Eq(salespersonID)).
		One(ctx, s.ex)
	if err != nil {
		return SalespersonPlan{}, err
	}
	if !ok {
		return SalespersonPlan{}, ErrNotFound
	}
	return fromPlanSalesperson(r, true), nil
}

// AllPlanSalespeople returns every salesperson of a plan, with their
// days, a page at a time to fn, in name order; for downloads.
func (s *Store) AllPlanSalespeople(ctx context.Context, planID string, fn func(SalespersonPlan) error) error {
	q := visitplansalesperson.New().
		Where(visitplansalesperson.PlanID.Eq(planID)).
		Order(visitplansalesperson.Name.Asc(), visitplansalesperson.SalespersonID.Asc())
	for offset := int64(0); ; offset += 200 {
		rows, err := q.Limit(200).Offset(offset).All(ctx, s.ex, nil)
		if err != nil {
			return fmt.Errorf("store: plan salespeople: %w", err)
		}
		for _, r := range rows {
			if err := fn(fromPlanSalesperson(r, true)); err != nil {
				return err
			}
		}
		if len(rows) < 200 {
			return nil
		}
	}
}

func fromPlanSalesperson(r visitplansalesperson.Row, detail bool) SalespersonPlan {
	sp := SalespersonPlan{
		SalespersonID: r.SalespersonID, Name: r.Name, Area: r.Area, Customers: r.Customers, Visits: r.Visits,
		Km: r.Km, Covered: r.Covered, LikelyLeft: r.LikelyLeft, Problem: r.Problem, NeedLook: r.NeedLook,
	}
	if detail {
		json.Unmarshal(r.Detail, &sp.Detail) // a bad one reads as no days
	}
	return sp
}

// CancelVisitPlan stops a queued or running plan and reports whether it
// did.
func (s *Store) CancelVisitPlan(ctx context.Context, id string) (bool, error) {
	var m visitplan.Mut
	m.SetStatus(int32(JobCancelled))
	m.SetEndTime(now())
	m.SetStage("")
	n, err := visitplan.New().
		Where(visitplan.ID.Eq(id), visitplan.Status.In(int32(JobQueued), int32(JobRunning))).
		UpdateAll(ctx, s.ex, m)
	if err != nil {
		return false, fmt.Errorf("store: cancel visit plan: %w", err)
	}
	return n > 0, nil
}

// RequeueVisitPlan queues a plan that isn't queued or running, and
// reports whether it did.
func (s *Store) RequeueVisitPlan(ctx context.Context, id string) (bool, error) {
	var m visitplan.Mut
	m.SetStatus(int32(JobQueued))
	m.SetError("")
	m.SetEndTimeNull()
	n, err := visitplan.New().
		Where(visitplan.ID.Eq(id), visitplan.Status.Gte(int32(JobSucceeded))).
		UpdateAll(ctx, s.ex, m)
	if err != nil {
		return false, fmt.Errorf("store: requeue visit plan: %w", err)
	}
	return n > 0, nil
}

// SetVisitPlanRepeat changes how often a plan runs again; 0 stops it.
// The next run is due one interval from now.
func (s *Store) SetVisitPlanRepeat(ctx context.Context, id string, seconds int64) error {
	m := visitplan.MutateKey(id)
	m.SetRepeatSeconds(seconds)
	if seconds > 0 {
		m.SetNextRun(now().Add(time.Duration(seconds) * time.Second))
	} else {
		m.SetNextRunNull()
	}
	if err := m.Update(ctx, s.ex); err != nil {
		if errors.Is(err, runtime.ErrNoRow) {
			return ErrNotFound
		}
		return fmt.Errorf("store: set visit plan repeat: %w", err)
	}
	return nil
}

// QueueDueVisitPlans queues the repeating plans whose next run is due at
// at and returns how many it queued.
func (s *Store) QueueDueVisitPlans(ctx context.Context, at time.Time) (int64, error) {
	var m visitplan.Mut
	m.SetStatus(int32(JobQueued))
	m.SetError("")
	m.SetEndTimeNull()
	m.SetNextRunNull()
	n, err := visitplan.New().
		Where(
			visitplan.RepeatSeconds.Gt(0),
			visitplan.NextRun.IsNotNull(),
			visitplan.NextRun.Lte(at),
			visitplan.Status.Gte(int32(JobSucceeded)),
		).
		UpdateAll(ctx, s.ex, m)
	if err != nil {
		return 0, fmt.Errorf("store: queue due visit plans: %w", err)
	}
	return n, nil
}

// DeleteVisitPlan removes a plan, its salespeople and its files. A
// running one can't go.
func (s *Store) DeleteVisitPlan(ctx context.Context, id string) error {
	n, err := visitplan.New().
		Where(visitplan.ID.Eq(id), visitplan.Status.NotEq(int32(JobRunning))).
		DeleteAll(ctx, s.ex)
	if err != nil {
		return fmt.Errorf("store: delete visit plan: %w", err)
	}
	if n == 0 {
		return ErrNotFound
	}
	return s.removeVisitPlanFiles(id)
}

func (s *Store) removeVisitPlanFiles(id string) error {
	for _, p := range []string{s.VisitPlanCustomersPath(id), s.VisitPlanSalespeoplePath(id)} {
		if err := os.Remove(p); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
	}
	return nil
}

// ResetRunningVisitPlans queues again the plans a previous process left
// running. They start over.
func (s *Store) ResetRunningVisitPlans(ctx context.Context) error {
	var m visitplan.Mut
	m.SetStatus(int32(JobQueued))
	if _, err := visitplan.New().Where(visitplan.Status.Eq(int32(JobRunning))).UpdateAll(ctx, s.ex, m); err != nil {
		return fmt.Errorf("store: reset running visit plans: %w", err)
	}
	return nil
}
