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
	"math/rand/v2"
	"strings"
	"testing"

	"connectrpc.com/connect"

	mimirv1 "github.com/gsoultan/mimir-server/gen/mimir/v1"
)

// rareFruit is n rows where one in ten is a pear.
func rareFruit(n int) string {
	r := rand.New(rand.NewPCG(5, 6))
	var b strings.Builder
	b.WriteString("id,width,height,colour,kind\n")
	for i := range n {
		cx, cy, colour, kind := 1.0, 1.0, "red", "apple"
		if i%10 == 0 {
			cx, cy, colour, kind = -1.0, -1.0, "green", "pear"
		}
		fmt.Fprintf(&b, "F%04d,%.3f,%.3f,%s,%s\n", i, cx+r.NormFloat64()*0.3, cy+r.NormFloat64()*0.3, colour, kind)
	}
	return b.String()
}

func TestBalanceRareAnswers(t *testing.T) {
	ctx := context.Background()
	srv := newServer(t)
	own := owner(t, srv)
	ds, err := own.datasets.CreateDataset(ctx, connect.NewRequest(&mimirv1.CreateDatasetRequest{Name: "Fruit", Kind: mimirv1.DatasetKind_DATASET_KIND_TABLE, Csv: []byte(rareFruit(300)), Target: "kind"}))
	if err != nil {
		t.Fatal(err)
	}
	id := ds.Msg.GetDataset().GetId()
	// A network and a model that learns in one go both balance. The ID
	// column reads as text, which is copied, not blended.
	for _, a := range []string{"text-mlp", "table-forest"} {
		j, err := own.training.CreateJob(ctx, connect.NewRequest(&mimirv1.CreateJobRequest{Name: "Fruit", Architecture: a, DatasetId: id, Settings: &mimirv1.TrainingSettings{Epochs: 4, LearningRate: 0.01, Balance: true}}))
		if err != nil {
			t.Fatal(err)
		}
		if !j.Msg.GetJob().GetSettings().GetBalance() {
			t.Fatal("the run doesn't keep the balance setting")
		}
		runner.Tick(ctx)
		got, err := own.training.GetJob(ctx, connect.NewRequest(&mimirv1.GetJobRequest{Id: j.Msg.GetJob().GetId()}))
		if err != nil || got.Msg.GetJob().GetStatus() != mimirv1.JobStatus_JOB_STATUS_SUCCEEDED {
			t.Fatalf("%s: %v, %v", a, got, err)
		}
		r := got.Msg.GetJob().GetReport()
		// About 240 rows learned from, a tenth of them pears: about 190
		// made-up pears even them out. Scoring is on the 60 real rows
		// kept aside.
		if r.GetMade() < 150 || r.GetMade() > 230 || r.GetExamples() != 60 {
			t.Fatalf("%s: made %d, scored %d", a, r.GetMade(), r.GetExamples())
		}
	}

	// Only a table model that picks an answer balances.
	if _, err := own.training.CreateJob(ctx, connect.NewRequest(&mimirv1.CreateJobRequest{Name: "Groups", Architecture: "find-groups", DatasetId: id, Settings: &mimirv1.TrainingSettings{Balance: true}})); code(err) != connect.CodeInvalidArgument {
		t.Fatalf("balance on groups: %v", err)
	}
}

func TestMakeRowsLikeMine(t *testing.T) {
	ctx := context.Background()
	srv := newServer(t)
	own := owner(t, srv)
	ds, err := own.datasets.CreateDataset(ctx, connect.NewRequest(&mimirv1.CreateDatasetRequest{Name: "Fruit", Kind: mimirv1.DatasetKind_DATASET_KIND_TABLE, Csv: []byte(rareFruit(60)), Target: "kind"}))
	if err != nil {
		t.Fatal(err)
	}
	real := ds.Msg.GetDataset()
	res, err := own.datasets.MakeRows(ctx, connect.NewRequest(&mimirv1.MakeRowsRequest{DatasetId: real.GetId(), Rows: 500}))
	if err != nil {
		t.Fatal(err)
	}
	made := res.Msg.GetDataset()
	if made.GetRows() != 500 || made.GetMadeFrom() != real.GetId() || made.GetName() != "Fruit (made up)" || made.GetTarget() != "kind" || len(made.GetLabels()) != 2 {
		t.Fatalf("made %v", made)
	}
	got, err := own.datasets.GetDataset(ctx, connect.NewRequest(&mimirv1.GetDatasetRequest{Id: made.GetId()}))
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range got.Msg.GetSample()[1:] { // after the header
		v := row.GetValues()
		if strings.HasPrefix(v[0], "F0") {
			t.Errorf("made-up row %v copies a real ID", v)
		}
		if v[3] != "red" && v[3] != "green" {
			t.Errorf("made-up row %v has a colour the real rows don't", v)
		}
	}
	// A model learns from the made-up rows like any others.
	j, err := own.training.CreateJob(ctx, connect.NewRequest(&mimirv1.CreateJobRequest{Name: "Made", Architecture: "text-mlp", DatasetId: made.GetId(), Settings: &mimirv1.TrainingSettings{Epochs: 4, LearningRate: 0.01}}))
	if err != nil {
		t.Fatal(err)
	}
	runner.Tick(ctx)
	job, err := own.training.GetJob(ctx, connect.NewRequest(&mimirv1.GetJobRequest{Id: j.Msg.GetJob().GetId()}))
	if err != nil || job.Msg.GetJob().GetStatus() != mimirv1.JobStatus_JOB_STATUS_SUCCEEDED || job.Msg.GetJob().GetAccuracy() < 0.8 {
		t.Fatalf("%v, %v", job, err)
	}

	for _, n := range []int64{0, 5, 2_000_000} {
		if _, err := own.datasets.MakeRows(ctx, connect.NewRequest(&mimirv1.MakeRowsRequest{DatasetId: real.GetId(), Rows: n})); code(err) != connect.CodeInvalidArgument {
			t.Errorf("%d rows: %v", n, err)
		}
	}
	if _, err := own.datasets.MakeRows(ctx, connect.NewRequest(&mimirv1.MakeRowsRequest{DatasetId: "nope", Rows: 100})); code(err) != connect.CodeNotFound {
		t.Errorf("no dataset: %v", err)
	}
}
