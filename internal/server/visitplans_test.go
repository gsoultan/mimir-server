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
	"database/sql"
	"encoding/csv"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"

	mimirv1 "github.com/gsoultan/mimir-server/gen/mimir/v1"
	"github.com/gsoultan/mimir-server/gen/mimir/v1/mimirv1connect"
)

// runVisitPlans runs the visit plan runner until the test ends.
func runVisitPlans(t *testing.T) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		svc.RunVisitPlans(ctx, 10*time.Millisecond)
	}()
	t.Cleanup(func() {
		cancel()
		<-done
	})
}

func waitVisitPlan(t *testing.T, c mimirv1connect.VisitServiceClient, id string) *mimirv1.VisitPlan {
	t.Helper()
	for range 500 {
		got, err := c.GetVisitPlan(context.Background(), connect.NewRequest(&mimirv1.GetVisitPlanRequest{Id: id}))
		if err != nil {
			t.Fatal(err)
		}
		if got.Msg.GetPlan().GetStatus() >= mimirv1.JobStatus_JOB_STATUS_SUCCEEDED {
			return got.Msg.GetPlan()
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("visit plan %s never finished", id)
	return nil
}

// planCustomers is a customers table for salespeople s-0.., each with
// per customers around their own town.
func planCustomers(people, per int, extra string) string {
	r := rand.New(rand.NewPCG(3, 4))
	var b strings.Builder
	b.WriteString("customer_id,name,latitude,longitude,salesperson_id,priority,days_since_last_visit" + extra + "\n")
	for p := range people {
		for i := range per {
			lat, lon := -6.2+float64(p)*0.5+r.NormFloat64()*0.05, 106.8+r.NormFloat64()*0.05
			fmt.Fprintf(&b, "c-%d-%d,Shop %d-%d,%.5f,%.5f,s-%d,%.2f,%d", p, i, p, i, lat, lon, p, r.Float64(), 5+i%20)
			if extra != "" {
				fmt.Fprintf(&b, ",%.3f,%.3f", r.NormFloat64(), r.NormFloat64())
			}
			b.WriteString("\n")
		}
	}
	return b.String()
}

func planPeople(n int) []*mimirv1.Salesperson {
	var out []*mimirv1.Salesperson
	for p := range n {
		area := "North"
		if p%2 == 1 {
			area = "South"
		}
		out = append(out, &mimirv1.Salesperson{Id: fmt.Sprintf("s-%d", p), Name: fmt.Sprintf("Sales %d", p), Area: area, StartLatitude: -6.2 + float64(p)*0.5, StartLongitude: 106.8})
	}
	return out
}

func TestSimulateVisitPlan(t *testing.T) {
	ctx := context.Background()
	srv := newServer(t)
	own := owner(t, srv)
	vc := mimirv1connect.NewVisitServiceClient(srv.Client(), srv.URL, connect.WithInterceptors(own.jar))

	set := &mimirv1.VisitPlanSettings{CustomersCsv: []byte(planCustomers(2, 60, "")), Salespeople: planPeople(2), Days: 3, StartDate: "2026-11-09", TimeZone: "Asia/Jakarta"}
	res, err := vc.SimulateVisitPlan(ctx, connect.NewRequest(&mimirv1.SimulateVisitPlanRequest{Settings: set}))
	if err != nil {
		t.Fatal(err)
	}
	p := res.Msg.GetPlan()
	if p.GetCustomers() != 120 || p.GetSalespeople() != 2 || p.GetVisits() == 0 || p.GetStartDate() != "2026-11-09" || p.GetEndDate() != "2026-11-11" {
		t.Fatalf("plan %v", p)
	}
	if len(res.Msg.GetSalespeople()) != 2 {
		t.Fatalf("salespeople %d", len(res.Msg.GetSalespeople()))
	}
	seen := map[string]bool{}
	for _, sp := range res.Msg.GetSalespeople() {
		if len(sp.GetDays()) != 3 {
			t.Fatalf("%s has %d days", sp.GetSalesperson().GetId(), len(sp.GetDays()))
		}
		for _, d := range sp.GetDays() {
			for _, v := range d.GetVisits() {
				id := v.GetCustomer().GetId()
				if seen[id] {
					t.Fatalf("%s planned twice", id)
				}
				seen[id] = true
				if !strings.HasPrefix(id, "c-"+strings.TrimPrefix(sp.GetSalesperson().GetId(), "s-")+"-") {
					t.Fatalf("%s planned for %s", id, sp.GetSalesperson().GetId())
				}
			}
		}
	}

	// Customers visited in the last 10 days are left out.
	set.SkipVisitedDays = 10
	skip, err := vc.SimulateVisitPlan(ctx, connect.NewRequest(&mimirv1.SimulateVisitPlanRequest{Settings: set}))
	if err != nil {
		t.Fatal(err)
	}
	if skip.Msg.GetPlan().GetLeftOut() == 0 {
		t.Fatalf("nothing left out: %v", skip.Msg.GetPlan())
	}
	for _, sp := range skip.Msg.GetSalespeople() {
		for _, d := range sp.GetDays() {
			for _, v := range d.GetVisits() {
				var p, i int
				fmt.Sscanf(v.GetCustomer().GetId(), "c-%d-%d", &p, &i)
				if 5+i%20 < 10 {
					t.Fatalf("%s was visited %d days ago", v.GetCustomer().GetId(), 5+i%20)
				}
			}
		}
	}

	// Bad asks are refused with a reason.
	for name, bad := range map[string]*mimirv1.VisitPlanSettings{
		"eight days":     {CustomersCsv: set.CustomersCsv, Salespeople: set.Salespeople, Days: 8},
		"no customers":   {Salespeople: set.Salespeople, Days: 1},
		"no salespeople": {CustomersCsv: set.CustomersCsv, Days: 1},
		"no place":       {CustomersCsv: []byte("customer_id,priority\nc1,1\n"), Salespeople: set.Salespeople[:1], Days: 1},
		"no priority":    {CustomersCsv: []byte("customer_id,latitude,longitude\nc1,1,1\n"), Salespeople: set.Salespeople[:1], Days: 1},
		"bad zone":       {CustomersCsv: set.CustomersCsv, Salespeople: set.Salespeople, Days: 1, TimeZone: "Mars/Base"},
		"query no db":    {CustomersQuery: "select 1", Salespeople: set.Salespeople, Days: 1},
	} {
		if _, err := vc.SimulateVisitPlan(ctx, connect.NewRequest(&mimirv1.SimulateVisitPlanRequest{Settings: bad})); connect.CodeOf(err) != connect.CodeInvalidArgument {
			t.Errorf("%s: %v", name, err)
		}
	}
	// A simulation is for a look; too many salespeople go to a plan.
	if _, err := vc.SimulateVisitPlan(ctx, connect.NewRequest(&mimirv1.SimulateVisitPlanRequest{Settings: &mimirv1.VisitPlanSettings{CustomersCsv: []byte(planCustomers(1, 1, "")), Salespeople: planPeople(101), Days: 1}})); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Errorf("101 salespeople: %v", err)
	}
	list, err := vc.ListVisitPlans(ctx, connect.NewRequest(&mimirv1.ListVisitPlansRequest{}))
	if err != nil || len(list.Msg.GetPlans()) != 0 {
		t.Fatalf("a simulation was kept: %v %v", list, err)
	}
}

func TestVisitPlanJob(t *testing.T) {
	ctx := context.Background()
	srv := newServer(t)
	own := owner(t, srv)
	vc := mimirv1connect.NewVisitServiceClient(srv.Client(), srv.URL, connect.WithInterceptors(own.jar))
	runVisitPlans(t)

	people := "id,name,area,start_latitude,start_longitude\n"
	for _, sp := range planPeople(6) {
		people += fmt.Sprintf("%s,%s,%s,%f,%f\n", sp.GetId(), sp.GetName(), sp.GetArea(), sp.GetStartLatitude(), sp.GetStartLongitude())
	}
	people += "s-9,Nobody,North,-6.2,106.8\n" // has no customers
	set := &mimirv1.VisitPlanSettings{CustomersCsv: []byte(planCustomers(6, 80, "")), SalespeopleCsv: []byte(people), Days: 5, SkipWeekdays: []int32{0}}
	if _, err := vc.CreateVisitPlan(ctx, connect.NewRequest(&mimirv1.CreateVisitPlanRequest{Settings: set, RepeatSeconds: 86400})); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("an upload made again: %v", err)
	}
	made, err := vc.CreateVisitPlan(ctx, connect.NewRequest(&mimirv1.CreateVisitPlanRequest{Name: "Week 46", Settings: set}))
	if err != nil {
		t.Fatal(err)
	}
	id := made.Msg.GetPlan().GetId()
	p := waitVisitPlan(t, vc, id)
	if p.GetStatus() != mimirv1.JobStatus_JOB_STATUS_SUCCEEDED || p.GetSalespeople() != 7 || p.GetVisits() == 0 || p.GetNeedLook() < 1 || p.GetRuns() != 1 || p.GetSource() != "upload" || p.GetSalespeopleSource() != "upload" {
		t.Fatalf("plan %v", p)
	}
	if len(p.GetAreas()) != 2 {
		t.Fatalf("areas %v", p.GetAreas())
	}

	page := func(q *mimirv1.ListPlanSalespeopleRequest) *mimirv1.ListPlanSalespeopleResponse {
		t.Helper()
		q.PlanId = id
		res, err := vc.ListPlanSalespeople(ctx, connect.NewRequest(q))
		if err != nil {
			t.Fatal(err)
		}
		return res.Msg
	}
	if all := page(&mimirv1.ListPlanSalespeopleRequest{}); all.GetTotal() != 7 || len(all.GetSalespeople()) != 7 {
		t.Fatalf("all %v", all)
	}
	if north := page(&mimirv1.ListPlanSalespeopleRequest{Area: "North"}); north.GetTotal() != 4 {
		t.Fatalf("north %d", north.GetTotal())
	}
	if two := page(&mimirv1.ListPlanSalespeopleRequest{PageSize: 2, Offset: 2, Order: "visits"}); two.GetTotal() != 7 || len(two.GetSalespeople()) != 2 {
		t.Fatalf("page %v", two)
	}
	if look := page(&mimirv1.ListPlanSalespeopleRequest{NeedLook: true}); look.GetTotal() < 1 || look.GetSalespeople()[0].GetProblem() == "" {
		t.Fatalf("need look %v", look)
	}
	if found := page(&mimirv1.ListPlanSalespeopleRequest{Search: "sales 3"}); found.GetTotal() != 1 || found.GetSalespeople()[0].GetId() != "s-3" {
		t.Fatalf("search %v", found)
	}
	if found := page(&mimirv1.ListPlanSalespeopleRequest{Search: "100%_"}); found.GetTotal() != 0 {
		t.Fatalf("search with wildcards %v", found)
	}

	one, err := vc.GetSalespersonPlan(ctx, connect.NewRequest(&mimirv1.GetSalespersonPlanRequest{PlanId: id, SalespersonId: "s-2"}))
	if err != nil {
		t.Fatal(err)
	}
	visits := 0
	for _, d := range one.Msg.GetSalesperson().GetDays() {
		if t0, _ := time.Parse(time.DateOnly, d.GetDate()); t0.Weekday() == time.Sunday {
			t.Fatalf("a visit on Sunday %s", d.GetDate())
		}
		visits += len(d.GetVisits())
	}
	if len(one.Msg.GetSalesperson().GetDays()) != 5 || visits != int(one.Msg.GetSalesperson().GetSalesperson().GetVisits()) {
		t.Fatalf("salesperson %v", one.Msg.GetSalesperson())
	}
	if _, err := vc.GetSalespersonPlan(ctx, connect.NewRequest(&mimirv1.GetSalespersonPlanRequest{PlanId: id, SalespersonId: "nope"})); connect.CodeOf(err) != connect.CodeNotFound {
		t.Fatalf("missing salesperson: %v", err)
	}

	// Every visit downloads as one table.
	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/api/visit-plans/"+id+"/visits.csv", nil)
	req.Header.Set("Cookie", own.jar.cookie)
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("download %d %s", resp.StatusCode, body)
	}
	rows, err := csv.NewReader(strings.NewReader(string(body))).ReadAll()
	if err != nil || rows[0][0] != "salesperson_id" || len(rows)-1 != int(p.GetVisits()) {
		t.Fatalf("download %d rows for %d visits: %v", len(rows)-1, p.GetVisits(), err)
	}
	anon, _ := http.Get(srv.URL + "/api/visit-plans/" + id + "/visits.csv")
	if anon.StatusCode != http.StatusUnauthorized {
		t.Fatalf("download without signing in: %d", anon.StatusCode)
	}
	anon.Body.Close()

	// Made again, it keeps one set of results.
	if _, err := vc.RunVisitPlan(ctx, connect.NewRequest(&mimirv1.RunVisitPlanRequest{Id: id})); err != nil {
		t.Fatal(err)
	}
	if p = waitVisitPlan(t, vc, id); p.GetRuns() != 2 || p.GetStatus() != mimirv1.JobStatus_JOB_STATUS_SUCCEEDED {
		t.Fatalf("again %v", p)
	}
	if all := page(&mimirv1.ListPlanSalespeopleRequest{}); all.GetTotal() != 7 {
		t.Fatalf("after again %d", all.GetTotal())
	}

	// A viewer reads plans but can't make them.
	own.users.CreateUser(ctx, connect.NewRequest(&mimirv1.CreateUserRequest{Email: "v@example.com", Name: "V", Password: password, Access: &mimirv1.Access{Role: mimirv1.Role_ROLE_VIEWER, AllModels: true}}))
	v := signIn(t, srv, "v@example.com")
	vv := mimirv1connect.NewVisitServiceClient(srv.Client(), srv.URL, connect.WithInterceptors(v.jar))
	if _, err := vv.GetVisitPlan(ctx, connect.NewRequest(&mimirv1.GetVisitPlanRequest{Id: id})); err != nil {
		t.Fatalf("viewer read: %v", err)
	}
	if _, err := vv.CreateVisitPlan(ctx, connect.NewRequest(&mimirv1.CreateVisitPlanRequest{Settings: set})); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("viewer made a plan: %v", err)
	}
	if _, err := vv.DeleteVisitPlan(ctx, connect.NewRequest(&mimirv1.DeleteVisitPlanRequest{Id: id})); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("viewer deleted a plan: %v", err)
	}

	if _, err := vc.DeleteVisitPlan(ctx, connect.NewRequest(&mimirv1.DeleteVisitPlanRequest{Id: id})); err != nil {
		t.Fatal(err)
	}
	if _, err := vc.GetVisitPlan(ctx, connect.NewRequest(&mimirv1.GetVisitPlanRequest{Id: id})); connect.CodeOf(err) != connect.CodeNotFound {
		t.Fatalf("deleted plan: %v", err)
	}
}

func TestVisitPlanWithModel(t *testing.T) {
	ctx := context.Background()
	srv := newServer(t)
	own := owner(t, srv)
	vc := mimirv1connect.NewVisitServiceClient(srv.Client(), srv.URL, connect.WithInterceptors(own.jar))

	// A model of whether a customer orders, from two numbers.
	r := rand.New(rand.NewPCG(5, 6))
	var b strings.Builder
	b.WriteString("recency,frequency,orders\n")
	for i := range 200 {
		c, ans := 1.0, "yes"
		if i%2 == 1 {
			c, ans = -1.0, "no"
		}
		fmt.Fprintf(&b, "%.3f,%.3f,%s\n", c+r.NormFloat64()*0.3, c+r.NormFloat64()*0.3, ans)
	}
	ds, err := own.datasets.CreateDataset(ctx, connect.NewRequest(&mimirv1.CreateDatasetRequest{Name: "Orders", Kind: mimirv1.DatasetKind_DATASET_KIND_TABLE, Csv: []byte(b.String())}))
	if err != nil {
		t.Fatal(err)
	}
	j, err := own.training.CreateJob(ctx, connect.NewRequest(&mimirv1.CreateJobRequest{Name: "Who orders", Architecture: "table-mlp", DatasetId: ds.Msg.GetDataset().GetId(), Settings: &mimirv1.TrainingSettings{Epochs: 6, LearningRate: 0.01}}))
	if err != nil {
		t.Fatal(err)
	}
	runner.Tick(ctx)
	job, err := own.training.GetJob(ctx, connect.NewRequest(&mimirv1.GetJobRequest{Id: j.Msg.GetJob().GetId()}))
	if err != nil || job.Msg.GetJob().GetModelId() == "" {
		t.Fatalf("job %v, %v", job, err)
	}
	modelID := job.Msg.GetJob().GetModelId()

	csvText := strings.Replace(planCustomers(1, 40, ""), "priority,", "", 1)
	// Drop the priority values too, and add the model's columns.
	var lines []string
	for i, l := range strings.Split(strings.TrimSpace(csvText), "\n") {
		f := strings.Split(l, ",")
		if i == 0 {
			lines = append(lines, l+",recency,frequency")
			continue
		}
		c := 1.0
		if i%2 == 0 {
			c = -1
		}
		lines = append(lines, strings.Join(append(f[:5], f[6:]...), ",")+fmt.Sprintf(",%.2f,%.2f", c, c))
	}
	set := &mimirv1.VisitPlanSettings{ModelId: modelID, CustomersCsv: []byte(strings.Join(lines, "\n") + "\n"), Salespeople: planPeople(1), Days: 1}
	set.Salespeople[0].MaxVisits = 10
	res, err := vc.SimulateVisitPlan(ctx, connect.NewRequest(&mimirv1.SimulateVisitPlanRequest{Settings: set}))
	if err != nil {
		t.Fatal(err)
	}
	if res.Msg.GetPlan().GetModelName() == "" || res.Msg.GetPlan().GetVisits() != 10 {
		t.Fatalf("plan %v", res.Msg.GetPlan())
	}
	// The model picks the customers who look like they order: the odd rows.
	likely := 0
	for _, v := range res.Msg.GetSalespeople()[0].GetDays()[0].GetVisits() {
		var p, i int
		fmt.Sscanf(v.GetCustomer().GetId(), "c-%d-%d", &p, &i)
		if i%2 == 0 {
			likely++
		}
	}
	if likely < 8 {
		t.Fatalf("only %d of 10 visits are customers likely to order", likely)
	}

	// A model that reads none of the columns is refused before planning.
	set.CustomersCsv = []byte(planCustomers(1, 5, ""))
	if _, err := vc.SimulateVisitPlan(ctx, connect.NewRequest(&mimirv1.SimulateVisitPlanRequest{Settings: set})); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("columns the model doesn't read: %v", err)
	}
}

// planDB is a SQLite file with customers and salespeople tables.
func planDB(t *testing.T, people, per int) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "sales.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, q := range []string{
		"CREATE TABLE salespeople (id TEXT, name TEXT, area TEXT, start_latitude REAL, start_longitude REAL)",
		"CREATE TABLE customers (customer_id TEXT, name TEXT, latitude REAL, longitude REAL, salesperson_id TEXT, priority REAL, days_since_last_visit INTEGER)",
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	for _, sp := range planPeople(people) {
		if _, err := db.Exec("INSERT INTO salespeople VALUES (?, ?, ?, ?, ?)", sp.GetId(), sp.GetName(), sp.GetArea(), sp.GetStartLatitude(), sp.GetStartLongitude()); err != nil {
			t.Fatal(err)
		}
	}
	addPlanCustomers(t, path, people, per)
	return path
}

func addPlanCustomers(t *testing.T, path string, people, per int) {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var n int
	db.QueryRow("SELECT count(*) FROM customers").Scan(&n)
	for _, line := range strings.Split(strings.TrimSpace(planCustomers(people, per, "")), "\n")[1:] {
		c := strings.Split(line, ",")
		c[0] = fmt.Sprintf("%s-%d", c[0], n)
		if _, err := db.Exec("INSERT INTO customers VALUES (?, ?, ?, ?, ?, ?, ?)", c[0], c[1], c[2], c[3], c[4], c[5], c[6]); err != nil {
			t.Fatal(err)
		}
	}
}

func TestVisitPlanFromDatabase(t *testing.T) {
	ctx := context.Background()
	srv := newServer(t)
	own := owner(t, srv)
	vc := mimirv1connect.NewVisitServiceClient(srv.Client(), srv.URL, connect.WithInterceptors(own.jar))
	runVisitPlans(t)

	path := planDB(t, 4, 30)
	set := &mimirv1.VisitPlanSettings{
		Connection:       &mimirv1.Connection{Driver: "sqlite", Dsn: path},
		CustomersQuery:   "SELECT * FROM customers",
		SalespeopleQuery: "SELECT * FROM salespeople",
		Days:             7,
		SkipVisitedDays:  3,
	}
	sim, err := vc.SimulateVisitPlan(ctx, connect.NewRequest(&mimirv1.SimulateVisitPlanRequest{Settings: set}))
	if err != nil {
		t.Fatal(err)
	}
	if sim.Msg.GetPlan().GetCustomers() != 120 || sim.Msg.GetPlan().GetSalespeople() != 4 {
		t.Fatalf("simulated %v", sim.Msg.GetPlan())
	}
	made, err := vc.CreateVisitPlan(ctx, connect.NewRequest(&mimirv1.CreateVisitPlanRequest{Settings: set, RepeatSeconds: 86400}))
	if err != nil {
		t.Fatal(err)
	}
	id := made.Msg.GetPlan().GetId()
	p := waitVisitPlan(t, vc, id)
	if p.GetStatus() != mimirv1.JobStatus_JOB_STATUS_SUCCEEDED || p.GetCustomers() != 120 || p.GetSource() != "database" || p.GetSalespeopleSource() != "database" ||
		p.GetRepeatSeconds() != 86400 || p.GetNextRunTime() == nil || strings.Contains(p.String(), path) {
		t.Fatalf("plan %v", p)
	}

	// The daily re-plan reads the customers afresh.
	addPlanCustomers(t, path, 4, 10)
	if _, err := vc.RunVisitPlan(ctx, connect.NewRequest(&mimirv1.RunVisitPlanRequest{Id: id})); err != nil {
		t.Fatal(err)
	}
	if p = waitVisitPlan(t, vc, id); p.GetCustomers() != 160 || p.GetRuns() != 2 {
		t.Fatalf("second run %v", p)
	}
	if _, err := vc.SetVisitPlanRepeat(ctx, connect.NewRequest(&mimirv1.SetVisitPlanRepeatRequest{Id: id, RepeatSeconds: 60})); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("every minute: %v", err)
	}
	stop, err := vc.SetVisitPlanRepeat(ctx, connect.NewRequest(&mimirv1.SetVisitPlanRepeatRequest{Id: id}))
	if err != nil || stop.Msg.GetPlan().GetRepeatSeconds() != 0 || stop.Msg.GetPlan().GetNextRunTime() != nil {
		t.Fatalf("stop repeating %v %v", stop, err)
	}

	// A query that gives no rows is refused before a plan is made.
	set.CustomersQuery = "SELECT * FROM nowhere"
	if _, err := vc.CreateVisitPlan(ctx, connect.NewRequest(&mimirv1.CreateVisitPlanRequest{Settings: set})); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("bad query: %v", err)
	}
}
