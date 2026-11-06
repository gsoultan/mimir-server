package visit

import (
	"errors"
	"fmt"
	"math/rand/v2"
	"slices"
	"testing"
)

func TestPlan(t *testing.T) {
	r := rand.New(rand.NewPCG(1, 2))
	var stops []Stop
	for i := range 60 {
		stops = append(stops, Stop{
			ID: fmt.Sprintf("c-%d", i), Lat: -6.2 + 0.05*r.NormFloat64(), Lon: 106.8 + 0.05*r.NormFloat64(),
			Priority: float64(60 - i), Minutes: 30,
		})
	}
	// One shop only opens in the afternoon.
	stops[5].Open, stops[5].Close = 14*60, 15*60
	req := Request{
		Stops:       stops,
		Salespeople: []Salesperson{{ID: "ana", Lat: -6.2, Lon: 106.8}, {ID: "budi", Lat: -6.21, Lon: 106.82, MaxVisits: 6}},
		Days:        2,
	}
	plan, err := Make(req)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, rt := range plan.Routes {
		if rt.End > 17*60 {
			t.Errorf("%s day %d ends at minute %d", rt.Salesperson, rt.Day, rt.End)
		}
		if rt.Salesperson == "budi" && len(rt.Visits) > 6 {
			t.Errorf("budi makes %d visits", len(rt.Visits))
		}
		last := 0
		for _, v := range rt.Visits {
			if seen[v.Stop] {
				t.Errorf("%s visited twice", v.Stop)
			}
			seen[v.Stop] = true
			if v.Arrive < last || v.Leave < v.Arrive+30 {
				t.Errorf("visit %+v after minute %d", v, last)
			}
			last = v.Leave
			if v.Stop == "c-5" && (v.Leave-30 < 14*60 || v.Leave-30 > 15*60) {
				t.Errorf("c-5 visited at %d, outside its opening", v.Leave-30)
			}
		}
	}
	if len(seen)+len(plan.Unplanned) != len(stops) {
		t.Fatalf("%d planned and %d not, of %d", len(seen), len(plan.Unplanned), len(stops))
	}
	// The most valuable stops are planned, and on the first day.
	for _, id := range []string{"c-0", "c-1", "c-2"} {
		if slices.Contains(plan.Unplanned, id) {
			t.Errorf("%s isn't planned", id)
		}
	}
	for _, rt := range plan.Routes {
		for _, v := range rt.Visits {
			if (v.Stop == "c-0" || v.Stop == "c-1") && rt.Day != 1 {
				t.Errorf("%s is on day %d", v.Stop, rt.Day)
			}
		}
	}
	t.Logf("%d routes, %.1f km, %d not planned, priority %.0f of %.0f", len(plan.Routes), plan.Km, len(plan.Unplanned), plan.Planned, plan.TotalPriority)
}

func TestPlanLeavesOutTheLeastValuable(t *testing.T) {
	// Eight hours fit about eight 45-minute visits nearby.
	var stops []Stop
	for i := range 20 {
		stops = append(stops, Stop{ID: fmt.Sprintf("s-%02d", i), Lat: -6.2 + 0.001*float64(i), Lon: 106.8, Priority: float64(20 - i), Minutes: 45})
	}
	plan, err := Make(Request{Stops: stops, Salespeople: []Salesperson{{ID: "a", Lat: -6.2, Lon: 106.8}}})
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Unplanned) == 0 || plan.Unplanned[0] < "s-08" {
		t.Errorf("left out %v", plan.Unplanned)
	}
}

func TestPlanRefuses(t *testing.T) {
	ok := Salesperson{ID: "a", Lat: 1, Lon: 1}
	for _, req := range []Request{
		{},
		{Stops: []Stop{{ID: "x", Lat: 1, Lon: 1}}},
		{Stops: []Stop{{ID: "x", Lat: 91, Lon: 1}}, Salespeople: []Salesperson{ok}},
		{Stops: []Stop{{ID: "x", Lat: 1, Lon: 1}, {ID: "x", Lat: 1, Lon: 1}}, Salespeople: []Salesperson{ok}},
		{Stops: []Stop{{ID: "x", Lat: 1, Lon: 1, Salesperson: "nobody"}}, Salespeople: []Salesperson{ok}},
		{Stops: []Stop{{ID: "x", Lat: 1, Lon: 1}}, Salespeople: []Salesperson{ok}, Days: 40},
	} {
		if _, err := Make(req); !errors.Is(err, ErrBad) {
			t.Errorf("%+v: %v", req, err)
		}
	}
}
