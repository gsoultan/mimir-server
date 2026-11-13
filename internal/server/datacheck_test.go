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

// TestDataCheck asks a table model questions like its examples, then
// unlike them, and reads back the warning, with no feedback sent.
func TestDataCheck(t *testing.T) {
	ctx := context.Background()
	srv := newServer(t)
	own := owner(t, srv)
	fruit, _ := trainFruit(t, own)
	svc.AllowPrivateWebhooks = true
	hook, err := own.hooks.CreateWebhook(ctx, connect.NewRequest(&mimirv1.CreateWebhookRequest{Name: "h", Url: "http://127.0.0.1:1/h", Events: []string{"model.data_changed"}}))
	if err != nil {
		t.Fatal(err)
	}

	r := rand.New(rand.NewPCG(5, 6))
	ask := func(n int, cx float64) {
		t.Helper()
		for range n {
			c := []float64{1, -1}[r.IntN(2)]
			fields := map[string]string{
				"width":  fmt.Sprintf("%.3f", c+cx+r.NormFloat64()*0.3),
				"height": fmt.Sprintf("%.3f", c+r.NormFloat64()*0.3),
			}
			if _, err := own.predict.Predict(ctx, connect.NewRequest(&mimirv1.PredictRequest{ModelId: fruit, Fields: fields})); err != nil {
				t.Fatal(err)
			}
		}
		if err := svc.FlushAnswers(ctx); err != nil {
			t.Fatal(err)
		}
		if err := svc.CheckData(ctx); err != nil {
			t.Fatal(err)
		}
	}
	quality := func() *mimirv1.GetModelQualityResponse {
		t.Helper()
		q, err := own.models.GetModelQuality(ctx, connect.NewRequest(&mimirv1.GetModelQualityRequest{ModelId: fruit}))
		if err != nil {
			t.Fatal(err)
		}
		return q.Msg
	}
	sent := func() int {
		t.Helper()
		l, err := own.hooks.ListDeliveries(ctx, connect.NewRequest(&mimirv1.ListDeliveriesRequest{WebhookId: hook.Msg.GetWebhook().GetId()}))
		if err != nil {
			t.Fatal(err)
		}
		return len(l.Msg.GetDeliveries())
	}

	// Too few questions to tell: checked, nothing reported.
	ask(20, 0)
	if q := quality(); !q.GetDataChecked() || q.GetDataQuestions() != 20 || len(q.GetDataChanges()) > 0 || q.GetDataChanged() {
		t.Fatalf("after 20 questions: %v", q)
	}
	// Questions like the examples: no change.
	ask(180, 0)
	if q := quality(); q.GetDataQuestions() != 200 || len(q.GetDataChanges()) > 0 || q.GetDataChanged() || q.GetDataCheckTime() == nil {
		t.Fatalf("after 200 like the examples: %v", q)
	}
	// Wider fruit: width changed a lot, height not.
	ask(300, 4)
	q := quality()
	if !q.GetDataChanged() || len(q.GetDataChanges()) != 1 {
		t.Fatalf("after wider fruit: %v", q)
	}
	c := q.GetDataChanges()[0]
	if c.GetColumn() != "width" || !c.GetALot() || !strings.Contains(c.GetSummary(), "in recent questions") {
		t.Fatalf("change %v", c)
	}
	// One webhook for the change, however often it is checked.
	if n := sent(); n != 1 {
		t.Fatalf("%d deliveries, want 1", n)
	}
	if err := svc.CheckData(ctx); err != nil {
		t.Fatal(err)
	}
	if n := sent(); n != 1 {
		t.Fatalf("%d deliveries after checking again, want 1", n)
	}

	// A model with no table isn't checked.
	digits := createModel(t, own, "Digits", "mnist-mlp")
	if q, err := own.models.GetModelQuality(ctx, connect.NewRequest(&mimirv1.GetModelQualityRequest{ModelId: digits.GetId()})); err != nil || q.Msg.GetDataChecked() {
		t.Fatalf("photo model: %v, %v", q, err)
	}
}
