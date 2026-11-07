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

// Package visit plans salespeople's visits: which stops each one makes on
// which day, in what order, and when they arrive, so the most valuable
// stops fit into the working days with the least driving.
//
// It inserts stops one at a time, must-visit and most valuable first,
// where they add the least time to any route that still keeps every
// opening time and day end, then shortens each route by reversing
// stretches of it (2-opt) and tries the stops that didn't fit again. The
// plan is a good one, not the best possible.
package visit

import (
	"cmp"
	"errors"
	"fmt"
	"math"
	"slices"
)

// Limits on a plan.
const (
	MaxStops  = 2000
	MaxDays   = 31
	MaxRoutes = 1000 // salespeople × days
)

// ErrBad reports a request that can't be planned; its message is for
// people.
var ErrBad = errors.New("visit: bad request")

func bad(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrBad, fmt.Sprintf(format, args...))
}

// Stop is a customer or lead to visit.
type Stop struct {
	ID, Name    string
	Lat, Lon    float64 // degrees
	Priority    float64 // higher first; 0 counts as 1
	Minutes     int     // the visit's length; 0 means 30
	Open, Close int     // minutes after midnight a visit may start; both 0 means any time
	Salesperson string  // when set, the only one who may visit
	Must        bool
}

// Salesperson is someone who visits, starting and ending each day at
// Lat, Lon.
type Salesperson struct {
	ID, Name   string
	Lat, Lon   float64
	Start, End int // minutes after midnight; both 0 means 08:00 to 17:00
	MaxVisits  int // 0 means no limit beyond time
}

// Request is what to plan.
type Request struct {
	Stops       []Stop
	Salespeople []Salesperson
	Days        int     // 0 means 1
	SpeedKmh    float64 // 0 means 30
	RoadFactor  float64 // 0 means 1.3
}

// Visit is one stop on a route.
type Visit struct {
	Stop          string
	Arrive, Leave int     // minutes after midnight
	Km            float64 // from the stop before, or the start
}

// Route is one salesperson's day.
type Route struct {
	Salesperson string
	Day         int // from 1
	Visits      []Visit
	Km          float64 // back to the start included
	End         int     // back at the start, minutes after midnight
}

// Plan is the answer.
type Plan struct {
	Routes                 []Route
	Unplanned              []string // most valuable first
	Km                     float64
	Planned, TotalPriority float64
}

const earthKm = 6371.0

func distanceKm(lat1, lon1, lat2, lon2 float64) float64 {
	r := math.Pi / 180
	h := func(v float64) float64 { s := math.Sin(v / 2); return s * s }
	a := h((lat2-lat1)*r) + math.Cos(lat1*r)*math.Cos(lat2*r)*h((lon2-lon1)*r)
	return 2 * earthKm * math.Asin(math.Sqrt(min(1, a)))
}

// planner holds a request's travel times: point 0..len(stops)-1 are the
// stops, then each salesperson's start.
type planner struct {
	stops  []Stop
	people []Salesperson
	km     [][]float64
	mins   [][]float64
}

func (p *planner) home(s int) int { return len(p.stops) + s }

// route is a day being planned: its salesperson and stops in order.
type route struct {
	person, day int
	order       []int
}

// schedule walks order and reports whether it keeps every opening time,
// the day's end and the visit limit, with the minute it ends back home.
func (p *planner) schedule(person int, order []int) (end float64, ok bool) {
	sp := p.people[person]
	if sp.MaxVisits > 0 && len(order) > sp.MaxVisits {
		return 0, false
	}
	at, t := p.home(person), float64(sp.Start)
	for _, i := range order {
		s := p.stops[i]
		t += p.mins[at][i]
		if s.Open != 0 || s.Close != 0 {
			if t > float64(s.Close) {
				return 0, false
			}
			t = max(t, float64(s.Open))
		}
		t += float64(s.Minutes)
		at = i
	}
	t += p.mins[at][p.home(person)]
	return t, t <= float64(sp.End)
}

// length is the minutes a route takes, waits included.
func (p *planner) length(person int, order []int) float64 {
	end, _ := p.schedule(person, order)
	return end - float64(p.people[person].Start)
}

// Make plans req.
func Make(req Request) (Plan, error) {
	days := cmp.Or(req.Days, 1)
	speed := cmp.Or(req.SpeedKmh, 30)
	road := cmp.Or(req.RoadFactor, 1.3)
	switch {
	case len(req.Stops) == 0:
		return Plan{}, bad("there are no stops to plan")
	case len(req.Stops) > MaxStops:
		return Plan{}, bad("%d stops; at most %d can be planned at once", len(req.Stops), MaxStops)
	case len(req.Salespeople) == 0:
		return Plan{}, bad("there is nobody to visit; add a salesperson")
	case days < 1 || days > MaxDays:
		return Plan{}, bad("%d days; plan 1 to %d", days, MaxDays)
	case len(req.Salespeople)*days > MaxRoutes:
		return Plan{}, bad("%d salespeople for %d days are too many routes; at most %d", len(req.Salespeople), days, MaxRoutes)
	case !(speed > 0 && speed <= 200):
		return Plan{}, bad("a speed of %g km/h; use one above 0 and up to 200", speed)
	case !(road >= 1 && road <= 3):
		return Plan{}, bad("a road factor of %g; use 1 to 3", road)
	}
	p := &planner{}
	people := map[string]int{}
	for i, s := range req.Salespeople {
		if s.ID == "" {
			return Plan{}, bad("salesperson %d has no id", i+1)
		}
		if _, ok := people[s.ID]; ok {
			return Plan{}, bad("salesperson %q is there twice", s.ID)
		}
		if !place(s.Lat, s.Lon) {
			return Plan{}, bad("salesperson %q starts at %g, %g, which isn't a place on Earth", s.ID, s.Lat, s.Lon)
		}
		if s.Start == 0 && s.End == 0 {
			s.Start, s.End = 8*60, 17*60
		}
		if s.Start < 0 || s.End > 24*60 || s.End <= s.Start {
			return Plan{}, bad("salesperson %q works from minute %d to %d; the day must end after it starts, by midnight", s.ID, s.Start, s.End)
		}
		people[s.ID] = i
		p.people = append(p.people, s)
	}
	ids := map[string]bool{}
	for i, s := range req.Stops {
		switch {
		case s.ID == "":
			return Plan{}, bad("stop %d has no id", i+1)
		case ids[s.ID]:
			return Plan{}, bad("stop %q is there twice", s.ID)
		case !place(s.Lat, s.Lon):
			return Plan{}, bad("stop %q is at %g, %g, which isn't a place on Earth", s.ID, s.Lat, s.Lon)
		case s.Minutes < 0 || s.Minutes > 24*60:
			return Plan{}, bad("stop %q takes %d minutes", s.ID, s.Minutes)
		case (s.Open != 0 || s.Close != 0) && (s.Open < 0 || s.Close > 24*60 || s.Close < s.Open):
			return Plan{}, bad("stop %q opens at minute %d and closes at %d", s.ID, s.Open, s.Close)
		case s.Salesperson != "" && !has(people, s.Salesperson):
			return Plan{}, bad("stop %q is for salesperson %q, who isn't in the request", s.ID, s.Salesperson)
		case math.IsNaN(s.Priority) || math.IsInf(s.Priority, 0) || s.Priority < 0:
			return Plan{}, bad("stop %q has priority %g; use 0 or more", s.ID, s.Priority)
		}
		ids[s.ID] = true
		s.Minutes = cmp.Or(s.Minutes, 30)
		if s.Priority == 0 {
			s.Priority = 1
		}
		p.stops = append(p.stops, s)
	}
	pts := make([][2]float64, 0, len(p.stops)+len(p.people))
	for _, s := range p.stops {
		pts = append(pts, [2]float64{s.Lat, s.Lon})
	}
	for _, s := range p.people {
		pts = append(pts, [2]float64{s.Lat, s.Lon})
	}
	p.km, p.mins = make([][]float64, len(pts)), make([][]float64, len(pts))
	for i := range pts {
		p.km[i], p.mins[i] = make([]float64, len(pts)), make([]float64, len(pts))
		for j := range pts {
			k := distanceKm(pts[i][0], pts[i][1], pts[j][0], pts[j][1]) * road
			p.km[i][j], p.mins[i][j] = k, k/speed*60
		}
	}

	var routes []*route
	for d := range days {
		for s := range p.people {
			routes = append(routes, &route{person: s, day: d + 1})
		}
	}
	order := make([]int, len(p.stops))
	for i := range order {
		order[i] = i
	}
	slices.SortStableFunc(order, func(a, b int) int {
		sa, sb := p.stops[a], p.stops[b]
		if sa.Must != sb.Must {
			if sa.Must {
				return -1
			}
			return 1
		}
		return cmp.Compare(sb.Priority, sa.Priority)
	})
	left := p.insertAll(routes, order)
	for _, r := range routes {
		p.improve(r)
	}
	left = p.insertAll(routes, left)

	var out Plan
	for _, s := range p.stops {
		out.TotalPriority += s.Priority
	}
	for _, r := range routes {
		if len(r.order) == 0 {
			continue
		}
		sp := p.people[r.person]
		at, t := p.home(r.person), float64(sp.Start)
		rt := Route{Salesperson: sp.ID, Day: r.day}
		for _, i := range r.order {
			s := p.stops[i]
			t += p.mins[at][i]
			arrive := t
			if s.Open != 0 || s.Close != 0 {
				t = max(t, float64(s.Open))
			}
			t += float64(s.Minutes)
			rt.Visits = append(rt.Visits, Visit{Stop: s.ID, Arrive: int(math.Round(arrive)), Leave: int(math.Round(t)), Km: round1(p.km[at][i])})
			rt.Km += p.km[at][i]
			out.Planned += s.Priority
			at = i
		}
		rt.Km += p.km[at][p.home(r.person)]
		t += p.mins[at][p.home(r.person)]
		rt.Km, rt.End = round1(rt.Km), int(math.Round(t))
		out.Km += rt.Km
		out.Routes = append(out.Routes, rt)
	}
	for _, i := range left {
		out.Unplanned = append(out.Unplanned, p.stops[i].ID)
	}
	out.Km = round1(out.Km)
	return out, nil
}

// dayCost is the minutes a day later costs, so the most valuable stops,
// placed first, land on the earliest days.
const dayCost = 20

// insertAll puts each stop of order, in turn, where it adds the least
// time to a route it fits, and returns those that fit nowhere.
func (p *planner) insertAll(routes []*route, order []int) (left []int) {
	for _, i := range order {
		s := p.stops[i]
		best, bestAt, bestCost := (*route)(nil), 0, math.Inf(1)
		for _, r := range routes {
			if s.Salesperson != "" && p.people[r.person].ID != s.Salesperson {
				continue
			}
			before := p.length(r.person, r.order)
			for at := 0; at <= len(r.order); at++ {
				try := slices.Insert(slices.Clone(r.order), at, i)
				end, ok := p.schedule(r.person, try)
				if !ok {
					continue
				}
				cost := end - float64(p.people[r.person].Start) - before + dayCost*float64(r.day-1)
				if cost < bestCost {
					best, bestAt, bestCost = r, at, cost
				}
			}
		}
		if best == nil {
			left = append(left, i)
			continue
		}
		best.order = slices.Insert(best.order, bestAt, i)
	}
	return left
}

// improve shortens r by reversing stretches of it while that keeps every
// opening time.
func (p *planner) improve(r *route) {
	for better := true; better; {
		better = false
		cur := p.length(r.person, r.order)
		for i := 0; i < len(r.order)-1; i++ {
			for j := i + 1; j < len(r.order); j++ {
				try := slices.Clone(r.order)
				slices.Reverse(try[i : j+1])
				if _, ok := p.schedule(r.person, try); !ok {
					continue
				}
				if l := p.length(r.person, try); l < cur-1e-9 {
					r.order, cur, better = try, l, true
				}
			}
		}
	}
}

func place(lat, lon float64) bool {
	return math.Abs(lat) <= 90 && math.Abs(lon) <= 180 && !math.IsNaN(lat) && !math.IsNaN(lon)
}

func has(m map[string]int, k string) bool { _, ok := m[k]; return ok }

func round1(v float64) float64 { return math.Round(v*10) / 10 }
