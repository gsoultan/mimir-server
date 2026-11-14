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
	"fmt"
	"math"
	"math/rand/v2"
	"strings"
	"testing"

	"connectrpc.com/connect"

	mimirv1 "github.com/gsoultan/mimir-server/gen/mimir/v1"
)

// shopSales is a table of a shop's daily sales, which fall as the price
// rises with wiggles and noise a free model follows, and whether the day
// was busy.
func shopSales(n int) string {
	r := rand.New(rand.NewPCG(7, 7))
	var b strings.Builder
	b.WriteString("price,region,sales,busy\n")
	for range n {
		price := 1 + 9*r.Float64()
		region := []string{"north", "south", "east"}[r.IntN(3)]
		sales := 60 - 4*price + 4*math.Sin(3*price) + 3*r.NormFloat64()
		if region == "south" {
			sales += 5
		}
		busy := "no"
		if sales+4*r.NormFloat64() > 40 {
			busy = "yes"
		}
		fmt.Fprintf(&b, "%.2f,%s,%.1f,%s\n", price, region, sales, busy)
	}
	return b.String()
}

// TestColumnRules teaches boosted trees that sales only go down as the
// price rises, checks the rule is kept with the run and holds in the
// model's answers, and that rules it can't keep are refused in plain
// words.
func TestColumnRules(t *testing.T) {
	srv := newServer(t)
	ctx := context.Background()
	own := owner(t, srv)
	data := func(name, target, drop string) string {
		t.Helper()
		csv := shopSales(600)
		var b strings.Builder
		for _, line := range strings.Split(strings.TrimSpace(csv), "\n") {
			f := strings.Split(line, ",")
			keep := f[:2]
			if drop == "busy" {
				keep = append(keep, f[2])
			} else {
				keep = append(keep, f[3])
			}
			b.WriteString(strings.Join(keep, ",") + "\n")
		}
		ds, err := own.datasets.CreateDataset(ctx, connect.NewRequest(&mimirv1.CreateDatasetRequest{Name: name, Kind: mimirv1.DatasetKind_DATASET_KIND_TABLE, Csv: []byte(b.String()), Target: target}))
		if err != nil {
			t.Fatal(err)
		}
		return ds.Msg.GetDataset().GetId()
	}
	sales, busy := data("Sales", "sales", "busy"), data("Busy days", "busy", "sales")
	down := []*mimirv1.ColumnRule{{Column: "price", Direction: "down"}}
	job := func(arch, ds string, rules []*mimirv1.ColumnRule) (*mimirv1.Job, error) {
		res, err := own.training.CreateJob(ctx, connect.NewRequest(&mimirv1.CreateJobRequest{Name: "Ruled", Architecture: arch, DatasetId: ds, Settings: &mimirv1.TrainingSettings{Rules: rules}}))
		if err != nil {
			return nil, err
		}
		return res.Msg.GetJob(), nil
	}
	for _, bad := range []struct {
		arch, ds string
		rules    []*mimirv1.ColumnRule
	}{
		{"number-mlp", sales, down},
		{"number-forest", sales, down},
		{"number-boost", sales, []*mimirv1.ColumnRule{{Column: "region", Direction: "up"}}},
		{"number-boost", sales, []*mimirv1.ColumnRule{{Column: "weather", Direction: "up"}}},
		{"number-boost", sales, []*mimirv1.ColumnRule{{Column: "price", Direction: "sideways"}}},
		{"number-boost", sales, []*mimirv1.ColumnRule{{Column: "price", Direction: "up"}, {Column: "price", Direction: "down"}}},
	} {
		if _, err := job(bad.arch, bad.ds, bad.rules); code(err) != connect.CodeInvalidArgument {
			t.Errorf("%s %v: %v", bad.arch, bad.rules, err)
		}
	}

	// teach runs a job and returns its model's answers along the price.
	teach := func(arch, ds string, rules []*mimirv1.ColumnRule, answer func(*mimirv1.PredictResponse) float64) []float64 {
		t.Helper()
		j, err := job(arch, ds, rules)
		if err != nil {
			t.Fatal(err)
		}
		if got := j.GetSettings().GetRules(); len(got) != len(rules) {
			t.Fatalf("job rules = %v", got)
		}
		runner.Tick(ctx)
		res, _ := own.training.GetJob(ctx, connect.NewRequest(&mimirv1.GetJobRequest{Id: j.GetId()}))
		if res.Msg.GetJob().GetStatus() != mimirv1.JobStatus_JOB_STATUS_SUCCEEDED {
			t.Fatalf("job = %v", res.Msg.GetJob())
		}
		if got := res.Msg.GetJob().GetSettings().GetRules(); len(rules) > 0 && (len(got) != 1 || got[0].GetColumn() != "price" || got[0].GetDirection() != rules[0].GetDirection()) {
			t.Fatalf("saved rules = %v", got)
		}
		var out []float64
		for p := 1.0; p <= 10; p += 0.1 {
			r, err := own.predict.Predict(ctx, connect.NewRequest(&mimirv1.PredictRequest{ModelId: res.Msg.GetJob().GetModelId(), Fields: map[string]string{"price": fmt.Sprintf("%.2f", p), "region": "north"}}))
			if err != nil {
				t.Fatal(err)
			}
			out = append(out, answer(r.Msg))
		}
		return out
	}
	value := func(r *mimirv1.PredictResponse) float64 { return float64(r.GetValue()) }
	chanceYes := func(r *mimirv1.PredictResponse) float64 {
		for _, g := range r.GetGuesses() {
			if g.GetLabel() == "yes" {
				return float64(g.GetProbability())
			}
		}
		return 0
	}
	rises := func(xs []float64) int {
		n := 0
		for i := 1; i < len(xs); i++ {
			if xs[i] > xs[i-1]+1e-4 {
				n++
			}
		}
		return n
	}
	if free := teach("number-boost", sales, nil, value); rises(free) == 0 {
		t.Log("the free model happens to never rise with the price")
	}
	for _, tc := range []struct {
		arch, ds string
		answer   func(*mimirv1.PredictResponse) float64
	}{{"number-boost", sales, value}, {"number-counts", sales, value}, {"table-boost", busy, chanceYes}} {
		got := teach(tc.arch, tc.ds, down, tc.answer)
		if n := rises(got); n > 0 {
			t.Errorf("%s: the answer rises %d times as the price rises: %v", tc.arch, n, got)
		}
		if !(got[0] > got[len(got)-1]) {
			t.Errorf("%s: the answer doesn't fall with the price: %v", tc.arch, got)
		}
	}
	// A worker keeps the rule too.
	svc.Local, runner.Local = false, false
	joinWorker(t, own, srv.URL, "ruler")
	if n := rises(teach("number-boost", sales, down, value)); n > 0 {
		t.Errorf("on a worker, the answer rises %d times as the price rises", n)
	}
}
