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
	"bytes"
	"cmp"
	"context"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"math"
	goruntime "runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
	_ "time/tzdata" // plans read dates in the customers' time zone, on any host

	"github.com/gsoultan/mimir-server/internal/arch"
	"github.com/gsoultan/mimir-server/internal/ask"
	"github.com/gsoultan/mimir-server/internal/runtime"
	"github.com/gsoultan/mimir-server/internal/store"
	"github.com/gsoultan/mimir-server/internal/visit"
)

const (
	// MaxPlanCustomers bounds the customers one plan reads.
	MaxPlanCustomers = 1_000_000
	// MaxPlanSalespeople bounds the salespeople one plan plans for.
	MaxPlanSalespeople = 5000
	// maxPlanDays is how many days ahead a plan looks.
	maxPlanDays = 7
	// maxSimulateCustomers and maxSimulateSalespeople bound a plan made
	// while the caller waits.
	maxSimulateCustomers   = 20_000
	maxSimulateSalespeople = 100
	// planLeft is how many likely customers that didn't fit are kept per
	// salesperson.
	planLeft = 20
	// planNeedLook is how many likely customers left over make a
	// salesperson need a look.
	planNeedLook = 6
	// windowColumn is the template column that says how many days ahead
	// a row looks; the plan sets it to its days.
	windowColumn = "visit_window_days"
)

// planInput is what a plan is made from.
type planInput struct {
	settings    store.VisitPlanSettings
	model       *store.Model // nil plans by the customers' priority column
	customers   []byte       // CSV with a header row
	salespeople []store.PlanSalesperson
	now         time.Time
	// progress, when set, hears what the plan is doing; it returns false
	// to stop.
	progress func(stage string, done, total int64) bool
}

// planOutcome is a made plan.
type planOutcome struct {
	summary store.VisitPlanSummary
	people  []store.SalespersonPlan
}

// errPlanStopped is a plan whose progress said stop.
var errPlanStopped = errors.New("stopped")

// planFailure is a reason a plan can't be made, for people.
type planFailure string

func (f planFailure) Error() string { return string(f) }

// customer is one row of a plan's customers.
type customer struct {
	store.PlanStop
	salesperson string
	minutes     int
	open, close int
	sinceVisit  float64 // -1 when not known
	// worth is what a visit is expected to bring, for ranking: the
	// chance, times the order value when the customers have one.
	worth  float64
	failed bool
	row    []string
}

// header columns a plan reads, by the names people use for them.
var (
	colCustomerID  = []string{"customer_id", "id", "customer", "customer_code"}
	colName        = []string{"name", "customer_name"}
	colLat         = []string{"latitude", "lat"}
	colLon         = []string{"longitude", "lon", "lng", "long"}
	colSalesperson = []string{"salesperson_id", "salesman_id", "sales_id", "salesperson"}
	colMinutes     = []string{"visit_minutes"}
	colOpen        = []string{"open_minute", "open"}
	colClose       = []string{"close_minute", "close"}
	colMust        = []string{"must_visit", "must"}
	colPriority    = []string{"priority"}
	colValue       = []string{"order_value", "avg_order_amount", "average_order_amount", "avg_order_value", "average_order_value", "expected_order_value"}
	colSinceVisit  = []string{"days_since_last_visit"}

	spID    = []string{"id", "salesperson_id", "salesman_id"}
	spName  = []string{"name", "salesperson_name"}
	spArea  = []string{"area", "territory", "region", "branch"}
	spLat   = []string{"start_latitude", "latitude", "lat"}
	spLon   = []string{"start_longitude", "longitude", "lon", "lng"}
	spStart = []string{"day_start_minute", "day_start", "start_minute", "start"}
	spEnd   = []string{"day_end_minute", "day_end", "end_minute", "end"}
	spMax   = []string{"max_visits", "visits_per_day"}
)

// columnIndex finds the first of names in header, ignoring case; -1 when
// none is there.
func columnIndex(header []string, names []string) int {
	for _, n := range names {
		for i, h := range header {
			if strings.EqualFold(strings.TrimSpace(h), n) {
				return i
			}
		}
	}
	return -1
}

func cell(row []string, i int) string {
	if i < 0 || i >= len(row) {
		return ""
	}
	return strings.TrimSpace(row[i])
}

// minuteOf reads minutes after midnight, as a number or HH:MM; -1 when
// it can't.
func minuteOf(s string) int {
	if s == "" {
		return -1
	}
	if h, m, ok := strings.Cut(s, ":"); ok {
		hh, err1 := strconv.Atoi(h)
		mm, err2 := strconv.Atoi(m[:min(len(m), 2)])
		if err1 != nil || err2 != nil || hh < 0 || hh > 24 || mm < 0 || mm > 59 {
			return -1
		}
		return hh*60 + mm
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil || f < 0 || f > 24*60 {
		return -1
	}
	return int(f)
}

func yes(s string) bool {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "yes", "y", "true", "1", "t", "ya":
		return true
	}
	return false
}

// onEarth reports whether lat, lon is a place; 0, 0 is read as none.
func onEarth(lat, lon float64) bool {
	return lat >= -90 && lat <= 90 && lon >= -180 && lon <= 180 && !(lat == 0 && lon == 0) &&
		!math.IsNaN(lat) && !math.IsNaN(lon)
}

// readTable reads a CSV with a header row.
func readTable(b []byte) ([]string, [][]string, error) {
	r := csv.NewReader(bytes.NewReader(b))
	r.FieldsPerRecord, r.LazyQuotes, r.ReuseRecord = -1, true, false
	header, err := r.Read()
	if err != nil {
		return nil, nil, planFailure("The rows can't be read: send a CSV with a header row.")
	}
	for i := range header {
		header[i] = cleanHeader(header[i])
	}
	var rows [][]string
	for {
		row, err := r.Read()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			var pe *csv.ParseError
			if errors.As(err, &pe) {
				continue // a broken line; the rest still reads
			}
			return nil, nil, planFailure("The rows can't be read.")
		}
		rows = append(rows, row)
	}
	return header, rows, nil
}

// checkCustomerHeader says what a customers table lacks; "" when it has
// what a plan needs.
func checkCustomerHeader(header []string, model *store.Model, people int) string {
	var missing []string
	if columnIndex(header, colCustomerID) < 0 {
		missing = append(missing, "customer_id")
	}
	if columnIndex(header, colLat) < 0 {
		missing = append(missing, "latitude")
	}
	if columnIndex(header, colLon) < 0 {
		missing = append(missing, "longitude")
	}
	if people > 1 && columnIndex(header, colSalesperson) < 0 {
		missing = append(missing, "salesperson_id (there are several salespeople)")
	}
	if model == nil && columnIndex(header, colPriority) < 0 && columnIndex(header, colValue) < 0 {
		missing = append(missing, "priority or order_value (there is no model to rank them)")
	}
	if len(missing) > 0 {
		return "The customers have no " + strings.Join(missing, ", ") + " column."
	}
	if model != nil && !slices.ContainsFunc(header, func(h string) bool { return reads(*model, h) }) {
		return fmt.Sprintf("None of the customers' columns is one %s reads. It reads %s.", model.Name, strings.Join(modelColumns(*model, 8), ", "))
	}
	return ""
}

// readSalespeople reads salespeople from a table.
func readSalespeople(b []byte) ([]store.PlanSalesperson, error) {
	header, rows, err := readTable(b)
	if err != nil {
		return nil, err
	}
	id, lat, lon := columnIndex(header, spID), columnIndex(header, spLat), columnIndex(header, spLon)
	if id < 0 || lat < 0 || lon < 0 {
		return nil, planFailure("The salespeople need id, start_latitude and start_longitude columns.")
	}
	name, area, start, end, maxv := columnIndex(header, spName), columnIndex(header, spArea), columnIndex(header, spStart), columnIndex(header, spEnd), columnIndex(header, spMax)
	var out []store.PlanSalesperson
	for _, row := range rows {
		sp := store.PlanSalesperson{ID: cell(row, id), Name: cell(row, name), Area: cell(row, area)}
		if sp.ID == "" {
			continue
		}
		sp.Lat, _ = strconv.ParseFloat(cell(row, lat), 64)
		sp.Lon, _ = strconv.ParseFloat(cell(row, lon), 64)
		sp.Start, sp.End = max(minuteOf(cell(row, start)), 0), max(minuteOf(cell(row, end)), 0)
		sp.MaxVisits, _ = strconv.Atoi(cell(row, maxv))
		out = append(out, sp)
	}
	if len(out) == 0 {
		return nil, planFailure("There are no salespeople in the table.")
	}
	return out, nil
}

// checkSalespeople cleans salespeople: no id twice, at most the bound.
func checkSalespeople(people []store.PlanSalesperson, most int) ([]store.PlanSalesperson, error) {
	if len(people) == 0 {
		return nil, planFailure("Add at least one salesperson.")
	}
	if len(people) > most {
		return nil, planFailure(fmt.Sprintf("%d salespeople; at most %d can be planned at once.", len(people), most))
	}
	seen := map[string]bool{}
	for _, sp := range people {
		if sp.ID == "" {
			return nil, planFailure("Every salesperson needs an id.")
		}
		if seen[sp.ID] {
			return nil, planFailure(fmt.Sprintf("Salesperson %q is there twice.", sp.ID))
		}
		seen[sp.ID] = true
	}
	return people, nil
}

// planDates are the dates a plan visits on: days calendar days from its
// start, without the skipped weekdays.
func planDates(set store.VisitPlanSettings, now time.Time) ([]time.Time, error) {
	loc := time.UTC
	if set.TimeZone != "" {
		l, err := time.LoadLocation(set.TimeZone)
		if err != nil {
			return nil, planFailure(fmt.Sprintf("%q isn't a time zone.", set.TimeZone))
		}
		loc = l
	}
	t := now.In(loc)
	start := time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
	if set.StartDate != "" {
		d, err := time.Parse(time.DateOnly, set.StartDate)
		if err != nil {
			return nil, planFailure(fmt.Sprintf("%q isn't a date; use YYYY-MM-DD.", set.StartDate))
		}
		start = d
	}
	var out []time.Time
	for i := range set.Days {
		d := start.AddDate(0, 0, i)
		if !slices.Contains(set.SkipWeekdays, int(d.Weekday())) {
			out = append(out, d)
		}
	}
	if len(out) == 0 {
		return nil, planFailure("Every day of the plan is skipped; plan more days or skip fewer.")
	}
	return out, nil
}

// positiveLabel is the label of a yes-or-no model that means they order.
func positiveLabel(labels []string) (int, bool) {
	for i, l := range labels {
		switch strings.ToLower(strings.TrimSpace(l)) {
		case "yes", "y", "true", "1", "ya", "order", "ordered", "buy", "buys", "bought":
			return i, true
		}
	}
	if len(labels) == 2 {
		for i, l := range labels {
			switch strings.ToLower(strings.TrimSpace(l)) {
			case "no", "n", "false", "0", "tidak":
				return 1 - i, true
			}
		}
	}
	return 0, false
}

// planModel checks model m can rank customers and says how.
type planModel struct {
	m        store.Model
	a        arch.Architecture
	spec     arch.Spec
	number   bool // answers days until the next order
	positive int  // the label that means they order
	window   bool // reads visit_window_days
}

func newPlanModel(m store.Model) (planModel, error) {
	a, err := scorable(m)
	if err != nil {
		return planModel{}, planFailure(fmt.Sprintf("%s can't plan visits: %v.", m.Name, err))
	}
	pm := planModel{m: m, a: a, spec: arch.Resolve(a, arch.Spec{InputShape: m.InputShape, Labels: m.Labels}), window: reads(m, windowColumn)}
	switch a.Answers() {
	case arch.Number:
		pm.number = true
	case arch.Class:
		i, ok := positiveLabel(pm.spec.Labels)
		if !ok {
			return planModel{}, planFailure(fmt.Sprintf("%s answers %s; pick a model that answers yes or no, or a number of days.", m.Name, strings.Join(pm.spec.Labels, ", ")))
		}
		pm.positive = i
	default:
		return planModel{}, planFailure(fmt.Sprintf("%s doesn't answer yes or no, or a number of days; pick another model.", m.Name))
	}
	return pm, nil
}

// likely is the chance from which a customer that didn't fit counts as
// likely: a 60% chance, or for a model of days, due within the plan.
func (pm *planModel) likely() float64 {
	if pm != nil && pm.number {
		return 0.5
	}
	return 0.6
}

// chances asks the model about every customer, filling in Priority, or
// marking failed.
func (s *Service) chances(ctx context.Context, pm planModel, header []string, cs []customer, days int, progress func(int64) bool) error {
	use := []int{}
	for i, h := range header {
		if reads(pm.m, h) && !slices.Contains(header[:i], h) && h != windowColumn {
			use = append(use, i)
		}
	}
	load := func() ([]byte, error) { return s.store.Weights(pm.m.ID) }
	open := func() (runtime.Weights, error) { return s.store.OpenWeights(pm.m.ID) }
	timeout := cmp.Or(s.PredictTimeout, DefaultPredictTimeout)
	window := strconv.Itoa(days)
	ask1 := func(c *customer) {
		fields := make(map[string]string, len(use)+1)
		for _, i := range use {
			if i < len(c.row) {
				fields[header[i]] = c.row[i]
			}
		}
		if pm.window {
			fields[windowColumn] = window
		}
		input, err := ask.Encode(pm.a, pm.m.Columns, pm.m.Features, fields, nil, nil)
		if err != nil {
			c.failed = true
			return
		}
		actx, cancel := context.WithTimeout(ctx, timeout)
		defer cancel()
		out, err := s.answer(actx, pm.m, pm.a, pm.spec, load, open, input)
		if err != nil {
			c.failed = true
			return
		}
		if pm.number {
			if len(out.Output) == 0 {
				c.failed = true
				return
			}
			// Days until the next order: due on the first day is 1, at
			// the end of the plan 0.5, and less the later after it.
			d := math.Max(float64(out.Output[0]), 0)
			c.Priority = float64(days) / (float64(days) + d)
			return
		}
		if pm.positive >= len(out.Probability) {
			c.failed = true
			return
		}
		c.Priority = float64(out.Probability[pm.positive])
	}
	const chunk = 512
	for start := 0; start < len(cs); start += chunk {
		end := min(start+chunk, len(cs))
		next := make(chan int)
		var wg sync.WaitGroup
		for range min(scoringAsk, end-start) {
			wg.Go(func() {
				for i := range next {
					ask1(&cs[i])
				}
			})
		}
		for i := start; i < end; i++ {
			next <- i
		}
		close(next)
		wg.Wait()
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if progress != nil && !progress(int64(end)) {
			return errPlanStopped
		}
	}
	return nil
}

// makePlan makes a plan from in.
func (s *Service) makePlan(ctx context.Context, in planInput) (planOutcome, error) {
	set := in.settings
	report := func(stage string, done, total int64) bool {
		return in.progress == nil || in.progress(stage, done, total)
	}
	dates, err := planDates(set, in.now)
	if err != nil {
		return planOutcome{}, err
	}
	people, err := checkSalespeople(in.salespeople, MaxPlanSalespeople)
	if err != nil {
		return planOutcome{}, err
	}
	if !report("reading", 0, 0) {
		return planOutcome{}, errPlanStopped
	}
	header, rows, err := readTable(in.customers)
	if err != nil {
		return planOutcome{}, err
	}
	if len(rows) == 0 {
		return planOutcome{}, planFailure("There are no customers to plan.")
	}
	if len(rows) > MaxPlanCustomers {
		return planOutcome{}, planFailure(fmt.Sprintf("%d customers; at most %d can be planned at once. Make a plan for each area.", len(rows), MaxPlanCustomers))
	}
	if why := checkCustomerHeader(header, in.model, len(people)); why != "" {
		return planOutcome{}, planFailure(why)
	}
	var pm *planModel
	if in.model != nil {
		m, err := newPlanModel(*in.model)
		if err != nil {
			return planOutcome{}, err
		}
		pm = &m
	}

	idx := map[string]int{}
	for i, sp := range people {
		idx[sp.ID] = i
	}
	cID, cName := columnIndex(header, colCustomerID), columnIndex(header, colName)
	cLat, cLon := columnIndex(header, colLat), columnIndex(header, colLon)
	cSP, cMin := columnIndex(header, colSalesperson), columnIndex(header, colMinutes)
	cOpen, cClose := columnIndex(header, colOpen), columnIndex(header, colClose)
	cMust, cPri, cSince := columnIndex(header, colMust), columnIndex(header, colPriority), columnIndex(header, colSinceVisit)
	cValue := columnIndex(header, colValue)
	sum := store.VisitPlanSummary{
		StartDate: dates[0].Format(time.DateOnly), EndDate: dates[len(dates)-1].Format(time.DateOnly),
		Customers: int64(len(rows)), Salespeople: int64(len(people)),
	}
	seen := make(map[string]bool, len(rows))
	twice := 0
	var cs []customer
	for _, row := range rows {
		c := customer{row: row, sinceVisit: -1, open: -1, close: -1}
		c.CustomerID, c.Name = cell(row, cID), cell(row, cName)
		if c.CustomerID == "" {
			sum.Unplaced++
			continue
		}
		if seen[c.CustomerID] {
			twice++
			continue
		}
		seen[c.CustomerID] = true
		c.Lat, err = strconv.ParseFloat(cell(row, cLat), 64)
		if err == nil {
			c.Lon, err = strconv.ParseFloat(cell(row, cLon), 64)
		}
		if err != nil || !onEarth(c.Lat, c.Lon) {
			sum.Unplaced++
			continue
		}
		c.salesperson = cell(row, cSP)
		if len(people) == 1 && cSP < 0 {
			c.salesperson = people[0].ID
		}
		if _, ok := idx[c.salesperson]; !ok {
			sum.Unassigned++
			continue
		}
		c.Must = yes(cell(row, cMust))
		if v := cell(row, cSince); v != "" {
			if f, err := strconv.ParseFloat(v, 64); err == nil && f >= 0 {
				c.sinceVisit = f
			}
		}
		if set.SkipVisitedDays > 0 && !c.Must && c.sinceVisit >= 0 && c.sinceVisit < float64(set.SkipVisitedDays) {
			sum.LeftOut++
			continue
		}
		c.minutes = cmp.Or(max(minuteOf(cell(row, cMin)), 0), set.VisitMinutes, 30)
		c.open, c.close = minuteOf(cell(row, cOpen)), minuteOf(cell(row, cClose))
		if pm == nil {
			c.Priority = 1 // ranked by order value alone
			if cPri >= 0 {
				c.Priority, _ = strconv.ParseFloat(cell(row, cPri), 64)
			}
			if math.IsNaN(c.Priority) || math.IsInf(c.Priority, 0) || c.Priority < 0 {
				c.Priority = 0
			}
		}
		if cValue >= 0 {
			// A blank or bad value counts as nothing to order.
			if v, err := strconv.ParseFloat(cell(row, cValue), 64); err == nil && v > 0 && !math.IsInf(v, 0) {
				c.Value = v
			}
		}
		cs = append(cs, c)
	}
	if twice > 0 {
		sum.Warnings = append(sum.Warnings, plural(twice, "customer id is", "customer ids are")+" there more than once; the first row of each was used.")
	}
	if sum.Unplaced > 0 {
		sum.Warnings = append(sum.Warnings, plural(int(sum.Unplaced), "customer has", "customers have")+" no id or no place (latitude and longitude) and were left out.")
	}
	if sum.Unassigned > 0 {
		sum.Warnings = append(sum.Warnings, plural(int(sum.Unassigned), "customer's salesperson isn't", "customers' salespeople aren't")+" on the list of salespeople; they were left out.")
	}

	if pm != nil {
		total := int64(len(cs))
		if !report("scoring", 0, total) {
			return planOutcome{}, errPlanStopped
		}
		if err := s.chances(ctx, *pm, header, cs, set.Days, func(done int64) bool { return report("scoring", done, total) }); err != nil {
			return planOutcome{}, err
		}
		for _, c := range cs {
			if c.failed {
				sum.Failed++
			}
		}
		if sum.Failed > 0 {
			sum.Warnings = append(sum.Warnings, fmt.Sprintf("%s couldn't answer for %s; they are planned last.", pm.m.Name, plural(int(sum.Failed), "customer", "customers")))
		}
	}

	byPerson := make([][]customer, len(people))
	for _, c := range cs {
		i := idx[c.salesperson]
		byPerson[i] = append(byPerson[i], c)
	}
	cs = nil
	out := make([]store.SalespersonPlan, len(people))
	chance := make([][2]float64, len(people)) // each one's planned and total chance
	total := int64(len(people))
	if !report("planning", 0, total) {
		return planOutcome{}, errPlanStopped
	}
	var done sync.WaitGroup
	next := make(chan int)
	var mu sync.Mutex
	finished := int64(0)
	stopped := false
	for range min(max(goruntime.NumCPU(), 1), 4, len(people)) {
		done.Go(func() {
			for i := range next {
				out[i], chance[i][0], chance[i][1] = planOne(people[i], byPerson[i], dates, set, pm.likely(), cValue >= 0)
				byPerson[i] = nil
				mu.Lock()
				finished++
				if finished%20 == 0 && !stopped && !report("planning", finished, total) {
					stopped = true
				}
				mu.Unlock()
			}
		})
	}
	for i := range people {
		if ctx.Err() != nil || stopped {
			break
		}
		next <- i
	}
	close(next)
	done.Wait()
	if ctx.Err() != nil {
		return planOutcome{}, ctx.Err()
	}
	if stopped {
		return planOutcome{}, errPlanStopped
	}

	var planned, all float64
	areas := map[string]int{}
	for i, sp := range out {
		sum.Visits += sp.Visits
		sum.Km += sp.Km
		if sp.NeedLook {
			sum.NeedLook++
		}
		areas[sp.Area]++
		planned += chance[i][0]
		all += chance[i][1]
	}
	if all > 0 {
		sum.Covered = planned / all
	}
	for a, n := range areas {
		sum.Areas = append(sum.Areas, store.AreaCount{Area: a, Salespeople: n})
	}
	slices.SortFunc(sum.Areas, func(a, b store.AreaCount) int { return cmp.Compare(a.Area, b.Area) })
	sum.Km = math.Round(sum.Km*10) / 10
	return planOutcome{summary: sum, people: out}, nil
}

// planOne plans one salesperson's days. It also returns the chance the
// visits reach and all their customers' chance.
//
// valued ranks the customers by chance times order value, rather than
// by chance alone.
func planOne(sp store.PlanSalesperson, cs []customer, dates []time.Time, set store.VisitPlanSettings, likely float64, valued bool) (_ store.SalespersonPlan, covered, total float64) {
	out := store.SalespersonPlan{
		SalespersonID: sp.ID, Name: cmp.Or(sp.Name, sp.ID), Area: sp.Area, Customers: int64(len(cs)),
		Detail: store.SalespersonDetail{Lat: sp.Lat, Lon: sp.Lon, Start: sp.Start, End: sp.End, Days: []store.PlanDay{}},
	}
	if out.Detail.Start == 0 && out.Detail.End == 0 {
		out.Detail.Start, out.Detail.End = 8*60, 17*60
	}
	for i := range cs {
		cs[i].worth = cs[i].Priority
		if valued {
			cs[i].worth *= cs[i].Value
		}
		total += cs[i].worth
	}
	switch {
	case len(cs) == 0:
		out.Problem, out.NeedLook = "No customers with this salesperson's id", true
		return out, 0, total
	case !onEarth(sp.Lat, sp.Lon):
		out.Problem, out.NeedLook = "No start place: add their start latitude and longitude", true
		return out, 0, total
	case out.Detail.End <= out.Detail.Start:
		out.Problem, out.NeedLook = "Their working day ends before it starts", true
		return out, 0, total
	}
	// Must-visit first, then likeliest; the planner takes at most
	// visit.MaxStops at once, and the rest wouldn't fit in a week anyway.
	slices.SortStableFunc(cs, func(a, b customer) int {
		if a.Must != b.Must {
			if a.Must {
				return -1
			}
			return 1
		}
		return cmp.Compare(b.worth, a.worth)
	})
	consider := cs[:min(len(cs), visit.MaxStops)]
	req := visit.Request{
		Salespeople: []visit.Salesperson{{ID: sp.ID, Name: sp.Name, Lat: sp.Lat, Lon: sp.Lon, Start: out.Detail.Start, End: out.Detail.End, MaxVisits: sp.MaxVisits}},
		Days:        len(dates), SpeedKmh: set.SpeedKmh, RoadFactor: set.RoadFactor,
	}
	// The planner weighs stops from 0 to 1.
	top := 0.0
	for _, c := range consider {
		top = max(top, c.worth)
	}
	if top == 0 {
		top = 1
	}
	byID := make(map[string]*customer, len(consider))
	for i := range consider {
		c := &consider[i]
		byID[c.CustomerID] = c
		st := visit.Stop{ID: c.CustomerID, Name: c.Name, Lat: c.Lat, Lon: c.Lon, Minutes: min(c.minutes, 12*60), Salesperson: sp.ID, Must: c.Must,
			// The planner reads 0 as 1; a customer with no chance comes last.
			Priority: math.Max(c.worth/top, 1e-9)}
		if c.open >= 0 && c.close >= c.open {
			st.Open, st.Close = c.open, c.close
		}
		req.Stops = append(req.Stops, st)
	}
	plan, err := visit.Make(req)
	if err != nil {
		out.Problem, out.NeedLook = "It couldn't be planned: "+strings.TrimPrefix(err.Error(), "visit: bad request: "), true
		return out, 0, total
	}
	planned := map[string]bool{}
	for _, r := range plan.Routes {
		d := store.PlanDay{Day: r.Day, Date: dates[r.Day-1].Format(time.DateOnly), Km: math.Round(r.Km*10) / 10, End: r.End, Visits: []store.PlanVisit{}}
		for _, v := range r.Visits {
			c := byID[v.Stop]
			planned[v.Stop] = true
			covered += c.worth
			d.Visits = append(d.Visits, store.PlanVisit{PlanStop: c.PlanStop, Arrive: v.Arrive, Leave: v.Leave, Km: math.Round(v.Km*10) / 10})
		}
		out.Visits += int64(len(r.Visits))
		out.Km += r.Km
		out.Detail.Days = append(out.Detail.Days, d)
	}
	slices.SortFunc(out.Detail.Days, func(a, b store.PlanDay) int { return cmp.Compare(a.Day, b.Day) })
	out.Km = math.Round(out.Km*10) / 10
	if total > 0 {
		out.Covered = covered / total
	}
	for _, c := range cs {
		if planned[c.CustomerID] || c.Priority < likely {
			continue
		}
		out.LikelyLeft++
		if len(out.Detail.Left) < planLeft {
			out.Detail.Left = append(out.Detail.Left, c.PlanStop)
		}
	}
	if out.LikelyLeft >= planNeedLook {
		out.Problem, out.NeedLook = "Many likely customers left over", true
	}
	return out, covered, total
}

// plural is "1 thing is" or "n things are", from the two forms.
func plural(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return strconv.Itoa(n) + " " + many
}
