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
	"testing"
	"time"

	"connectrpc.com/connect"

	mimirv1 "github.com/gsoultan/mimir-server/gen/mimir/v1"
)

func TestTryNewVersionOnPartOfTheQuestions(t *testing.T) {
	srv := newServer(t)
	ctx := context.Background()
	own := owner(t, srv)
	path := fruitDB(t, 200)
	conn := &mimirv1.Connection{Driver: "sqlite", Dsn: path}
	ds, err := own.datasets.CreateDataset(ctx, connect.NewRequest(&mimirv1.CreateDatasetRequest{Name: "Shop fruit", Kind: mimirv1.DatasetKind_DATASET_KIND_TABLE, Connection: conn, Query: "SELECT width, height, kind FROM fruit", Target: "kind"}))
	if err != nil {
		t.Fatal(err)
	}
	job, err := own.training.CreateJob(ctx, connect.NewRequest(&mimirv1.CreateJobRequest{Name: "Sorter", Architecture: "table-mlp", DatasetId: ds.Msg.GetDataset().GetId(), RepeatSeconds: 86400, Settings: &mimirv1.TrainingSettings{Epochs: 8, LearningRate: 0.01}}))
	if err != nil {
		t.Fatal(err)
	}
	runner.Tick(ctx)
	j1, _ := own.training.GetJob(ctx, connect.NewRequest(&mimirv1.GetJobRequest{Id: job.Msg.GetJob().GetId()}))
	modelID := j1.Msg.GetJob().GetModelId()
	if modelID == "" {
		t.Fatalf("first pass %v", j1.Msg.GetJob())
	}

	// Set to try new versions on 50% of questions first.
	if _, err := own.models.SetNewVersionShare(ctx, connect.NewRequest(&mimirv1.SetNewVersionShareRequest{ModelId: modelID, Share: 100})); code(err) != connect.CodeInvalidArgument {
		t.Fatalf("share 100: %v", err)
	}
	set, err := own.models.SetNewVersionShare(ctx, connect.NewRequest(&mimirv1.SetNewVersionShareRequest{ModelId: modelID, Share: 50}))
	if err != nil || set.Msg.GetModel().GetNewVersionShare() != 50 {
		t.Fatalf("SetNewVersionShare = %v, %v", set, err)
	}
	if g, _ := own.models.GetModelTrial(ctx, connect.NewRequest(&mimirv1.GetModelTrialRequest{ModelId: modelID})); g.Msg.GetTrial() != nil {
		t.Fatalf("trial before any: %v", g.Msg)
	}

	// The next keep-learning pass is kept and put on trial, not live.
	addFruit(t, path, 100)
	runner.Now = func() time.Time { return time.Now().Add(25 * time.Hour) }
	defer func() { runner.Now = time.Now }()
	runner.Tick(ctx)
	vs, err := own.models.ListModelVersions(ctx, connect.NewRequest(&mimirv1.ListModelVersionsRequest{ModelId: modelID}))
	if err != nil || len(vs.Msg.GetVersions()) != 2 {
		t.Fatalf("ListModelVersions = %v, %v", vs, err)
	}
	v2, v1 := vs.Msg.GetVersions()[0], vs.Msg.GetVersions()[1]
	if v2.GetLive() || !v1.GetLive() {
		t.Fatalf("the new pass went live: %v", vs.Msg.GetVersions())
	}
	m, _ := own.models.GetModel(ctx, connect.NewRequest(&mimirv1.GetModelRequest{Id: modelID}))
	if tr := m.Msg.GetModel().GetTrial(); tr.GetVersionId() != v2.GetId() || tr.GetShare() != 50 || tr.GetShadow() {
		t.Fatalf("trial %v", tr)
	}

	// About half the answers come from each version, and feedback counts
	// for the version that answered.
	ask := func() *mimirv1.PredictResponse {
		t.Helper()
		p, err := own.predict.Predict(ctx, connect.NewRequest(&mimirv1.PredictRequest{ModelId: modelID, Fields: map[string]string{"width": "-1", "height": "-1"}}))
		if err != nil || p.Msg.GetPredictionId() == "" {
			t.Fatalf("Predict = %v, %v", p, err)
		}
		return p.Msg
	}
	var ids []string
	for range 100 {
		p := ask()
		ids = append(ids, p.GetPredictionId())
		if _, err := own.predict.SendFeedback(ctx, connect.NewRequest(&mimirv1.SendFeedbackRequest{PredictionId: p.GetPredictionId(), Label: p.GetGuesses()[0].GetLabel()})); err != nil {
			t.Fatal(err)
		}
	}
	if err := svc.FlushAnswers(ctx); err != nil {
		t.Fatal(err)
	}
	g, err := own.models.GetModelTrial(ctx, connect.NewRequest(&mimirv1.GetModelTrialRequest{ModelId: modelID}))
	if err != nil {
		t.Fatal(err)
	}
	live, tried := g.Msg.GetLive(), g.Msg.GetTried()
	if live.GetVersionNumber() != 1 || tried.GetVersionNumber() != 2 || live.GetAnswers()+tried.GetAnswers() != 100 ||
		live.GetAnswers() < 20 || tried.GetAnswers() < 20 || tried.GetFeedback() != tried.GetAnswers() || tried.GetAccuracy() != 1 ||
		!g.Msg.GetEnoughFeedback() {
		t.Fatalf("GetModelTrial = %v", g.Msg)
	}

	// A version can't be tried against itself, and a share is needed.
	if _, err := own.models.StartModelTrial(ctx, connect.NewRequest(&mimirv1.StartModelTrialRequest{ModelId: modelID, VersionId: v1.GetId(), Share: 10})); code(err) != connect.CodeInvalidArgument {
		t.Fatalf("try the live version: %v", err)
	}
	if _, err := own.models.StartModelTrial(ctx, connect.NewRequest(&mimirv1.StartModelTrialRequest{ModelId: modelID, VersionId: v2.GetId()})); code(err) != connect.CodeInvalidArgument {
		t.Fatalf("no share: %v", err)
	}

	// A shadow trial answers a copy of every question; askers get the
	// live version's answer, and feedback judges both.
	st, err := own.models.StartModelTrial(ctx, connect.NewRequest(&mimirv1.StartModelTrialRequest{ModelId: modelID, VersionId: v2.GetId(), Shadow: true}))
	if err != nil || !st.Msg.GetModel().GetTrial().GetShadow() {
		t.Fatalf("StartModelTrial shadow = %v, %v", st, err)
	}
	var shadowed []*mimirv1.PredictResponse
	for range 10 {
		shadowed = append(shadowed, ask())
	}
	// Shadow answers run after the reply; wait for them to be kept.
	deadline := time.Now().Add(20 * time.Second)
	for {
		if err := svc.FlushAnswers(ctx); err != nil {
			t.Fatal(err)
		}
		g, _ = own.models.GetModelTrial(ctx, connect.NewRequest(&mimirv1.GetModelTrialRequest{ModelId: modelID}))
		if g.Msg.GetTried().GetAnswers() == 10 || time.Now().After(deadline) {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if g.Msg.GetLive().GetAnswers() != 10 || g.Msg.GetTried().GetAnswers() != 10 || g.Msg.GetAgree() == 0 || g.Msg.GetAgreeShare() <= 0 {
		t.Fatalf("shadow trial = %v", g.Msg)
	}
	for _, p := range shadowed {
		if _, err := own.predict.SendFeedback(ctx, connect.NewRequest(&mimirv1.SendFeedbackRequest{PredictionId: p.GetPredictionId(), Label: p.GetGuesses()[0].GetLabel()})); err != nil {
			t.Fatal(err)
		}
	}
	g, _ = own.models.GetModelTrial(ctx, connect.NewRequest(&mimirv1.GetModelTrialRequest{ModelId: modelID}))
	if g.Msg.GetLive().GetFeedback() != 10 || g.Msg.GetTried().GetFeedback() != 10 {
		t.Fatalf("shadow feedback = %v", g.Msg)
	}
	// Shadow answers are not the model's answers: they stay out of its
	// daily counts.
	q, _ := own.models.GetModelQuality(ctx, connect.NewRequest(&mimirv1.GetModelQualityRequest{ModelId: modelID}))
	var n int64
	for _, d := range q.Msg.GetDays() {
		n += d.GetAnswers()
	}
	if n != 110 {
		t.Fatalf("quality counts %d answers, want 110: %v", n, q.Msg)
	}

	// Stopping leaves the live version answering everything.
	stop, err := own.models.StopModelTrial(ctx, connect.NewRequest(&mimirv1.StopModelTrialRequest{ModelId: modelID}))
	if err != nil || stop.Msg.GetModel().GetTrial() != nil {
		t.Fatalf("StopModelTrial = %v, %v", stop, err)
	}
	for range 20 {
		ask()
	}
	if err := svc.FlushAnswers(ctx); err != nil {
		t.Fatal(err)
	}

	// Putting the version tried live ends its trial.
	if _, err := own.models.StartModelTrial(ctx, connect.NewRequest(&mimirv1.StartModelTrialRequest{ModelId: modelID, VersionId: v2.GetId(), Share: 10})); err != nil {
		t.Fatal(err)
	}
	use, err := own.models.UseModelVersion(ctx, connect.NewRequest(&mimirv1.UseModelVersionRequest{ModelId: modelID, VersionId: v2.GetId()}))
	if err != nil || use.Msg.GetModel().GetVersionId() != v2.GetId() || use.Msg.GetModel().GetTrial() != nil {
		t.Fatalf("UseModelVersion = %v, %v", use, err)
	}
}
