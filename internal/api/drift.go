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
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/gsoultan/mimir-server/internal/drift"
	"github.com/gsoultan/mimir-server/internal/store"
	"github.com/gsoultan/mimir-server/internal/training"
)

// The data check compares the last driftWindow of questions, at most
// driftSample of the newest, with the examples a model learned from. A
// change that lasts alerts again after driftRealert.
const (
	driftWindow  = 7 * 24 * time.Hour
	driftSample  = 2000
	driftRealert = 7 * 24 * time.Hour
)

// CheckData compares the questions each table model was asked in the last
// 7 days with the examples its live version learned from, and keeps the
// result for the model's page. When a column changed a lot it alerts
// owners and sends a model.data_changed webhook: once for each change,
// and again after a week if it lasts. Unlike CheckQuality it needs no
// feedback. The server calls it every hour.
func (s *Service) CheckData(ctx context.Context) error {
	now := time.Now()
	days, err := s.store.AnswerDays(ctx, "", now.Add(-driftWindow))
	if err != nil {
		return err
	}
	var ids []string
	for _, d := range days {
		if !slices.Contains(ids, d.ModelID) {
			ids = append(ids, d.ModelID)
		}
	}
	for _, id := range ids {
		if err := s.checkData(ctx, id, now); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			s.log.WarnContext(ctx, "data check", "model_id", id, "error", err)
		}
	}
	return nil
}

// checkData checks one model's recent questions.
func (s *Service) checkData(ctx context.Context, id string, now time.Time) error {
	m, err := s.store.Get(ctx, id)
	if err != nil || len(m.Columns) == 0 {
		return nil // deleted since, or not a table model
	}
	d, ok, err := s.store.GetDataCheck(ctx, id)
	if err != nil {
		return err
	}
	if !ok || d.VersionID != m.VersionID {
		// First check, or another version went live: what it learned from.
		d = store.DataCheck{ModelID: id, VersionID: m.VersionID, AlertTime: d.AlertTime, AlertColumns: d.AlertColumns}
		d.Baseline = s.baseline(ctx, m)
	}
	d.CheckTime = now
	d.Questions, d.Changes = 0, nil
	if len(d.Baseline) > 0 {
		qs, err := s.store.RecentQuestions(ctx, id, m.VersionID, now.Add(-driftWindow), driftSample)
		if err != nil {
			return err
		}
		d.Questions, d.Changes = int64(len(qs)), drift.Compare(d.Baseline, qs)
	}
	var big []drift.Change
	for _, c := range d.Changes {
		if c.ALot {
			big = append(big, c)
		}
	}
	alert := false
	switch {
	case len(d.Changes) == 0:
		// Back to normal: the next change alerts again.
		d.AlertColumns = nil
	case len(big) > 0:
		fresh := slices.ContainsFunc(big, func(c drift.Change) bool { return !slices.Contains(d.AlertColumns, c.Column) })
		if fresh || now.Sub(d.AlertTime) >= driftRealert {
			alert = true
			d.AlertTime, d.AlertColumns = now, nil
			for _, c := range big {
				d.AlertColumns = append(d.AlertColumns, c.Column)
			}
		}
	}
	if err := s.store.SaveDataCheck(ctx, d); err != nil {
		return err
	}
	if alert {
		s.alertData(ctx, m, d.Questions, big)
	}
	return nil
}

// baseline describes the examples m's live version learned from, or nil
// when they can't be read: an uploaded model, or its examples are gone.
func (s *Service) baseline(ctx context.Context, m store.Model) []drift.Column {
	if m.JobID == "" {
		return nil
	}
	j, err := s.store.GetJob(ctx, m.JobID)
	if err != nil {
		return nil
	}
	ds, err := s.store.GetDataset(ctx, j.DatasetID)
	if err != nil {
		return nil
	}
	src, err := training.SourceOf(s.store, ds)
	if err != nil {
		return nil
	}
	b, err := training.Baseline(src, m.Columns)
	if err != nil {
		s.log.WarnContext(ctx, "data check: couldn't read the examples", "model_id", m.ID, "error", err)
		return nil
	}
	return b
}

// alertData tells owners, and webhooks, that m's questions changed.
func (s *Service) alertData(ctx context.Context, m store.Model, questions int64, big []drift.Change) {
	if s.Notifier != nil {
		var lines []string
		for _, c := range big {
			lines = append(lines, fmt.Sprintf("- %s: %s", c.Column, c.Summary))
		}
		s.Notifier.Notify(ctx, "Questions look different: "+m.Name, fmt.Sprintf(
			"The last %d questions sent to %s look different from the examples it learned from:\n\n%s\n\nIts answers may be less reliable. Check that the app asking sends what it used to, or teach the model again with newer examples.",
			questions, m.Name, strings.Join(lines, "\n")))
	}
	cols := make([]map[string]any, len(big))
	for i, c := range big {
		cols[i] = map[string]any{"column": c.Column, "score": c.Score, "summary": c.Summary}
	}
	s.emitIn(ctx, m.TeamID, "model.data_changed", map[string]any{
		"model":     map[string]any{"id": m.ID, "name": m.Name, "architecture": m.Architecture, "version_id": m.VersionID},
		"questions": questions,
		"columns":   cols,
	})
	s.log.WarnContext(ctx, "model questions changed", "model_id", m.ID, "columns", len(big), "questions", questions)
}
