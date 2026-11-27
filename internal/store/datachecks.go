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
	"fmt"
	"time"

	"github.com/gsoultan/mimir-server/internal/drift"
	"github.com/gsoultan/mimir-server/internal/store/db/answer"
	"github.com/gsoultan/mimir-server/internal/store/db/datacheck"
)

// DataCheck is how a table model's recent questions compare with the
// examples its live version learned from.
type DataCheck struct {
	ModelID string
	// VersionID is the version Baseline describes; Baseline is empty when
	// its examples couldn't be read.
	VersionID string
	Baseline  []drift.Column
	CheckTime time.Time
	// Questions is how many recent questions were compared, and Changes
	// the columns that changed, biggest first.
	Questions int64
	Changes   []drift.Change
	// The columns last alerted about, and when; zero when never.
	AlertTime    time.Time
	AlertColumns []string
}

// GetDataCheck returns a model's last data check; ok is false when it has
// none yet.
func (s *Store) GetDataCheck(ctx context.Context, modelID string) (DataCheck, bool, error) {
	r, ok, err := datacheck.New().Where(datacheck.ModelID.Eq(modelID)).One(ctx, s.ex)
	if err != nil || !ok {
		if err != nil {
			err = fmt.Errorf("store: get data check: %w", err)
		}
		return DataCheck{}, false, err
	}
	d := DataCheck{
		ModelID: r.ModelID, VersionID: r.VersionID, CheckTime: r.CheckTime.UTC(), Questions: r.Questions,
		Baseline: fromDoc[drift.Column](r.Baseline), Changes: fromDoc[drift.Change](r.Changes), AlertColumns: r.AlertColumns,
	}
	if r.AlertTime.Valid {
		d.AlertTime = r.AlertTime.V.UTC()
	}
	return d, true, nil
}

// SaveDataCheck writes a model's data check over its last one.
func (s *Store) SaveDataCheck(ctx context.Context, d DataCheck) error {
	n := datacheck.Create()
	n.SetModelID(d.ModelID)
	n.SetVersionID(d.VersionID)
	n.SetBaseline(doc(d.Baseline))
	n.SetCheckTime(d.CheckTime)
	n.SetQuestions(d.Questions)
	n.SetChanges(doc(d.Changes))
	if d.AlertTime.IsZero() {
		n.SetAlertTimeNull()
	} else {
		n.SetAlertTime(d.AlertTime)
	}
	cols := d.AlertColumns
	if cols == nil {
		cols = []string{}
	}
	n.SetAlertColumns(cols)
	if _, err := n.OnConflictModelID().Insert(ctx, s.ex); err != nil {
		return fmt.Errorf("store: save data check: %w", err)
	}
	return nil
}

// RecentQuestions returns the questions, by column name, that a model's
// version was asked since a time, newest first and at most limit of
// them. Questions asked without column names, or too large to keep, are
// left out.
func (s *Store) RecentQuestions(ctx context.Context, modelID, versionID string, since time.Time, limit int) ([]map[string]string, error) {
	rows, err := answer.New().
		Where(answer.ModelID.Eq(modelID), answer.CreateTime.Gte(since)).
		WhereIf(versionID != "", answer.VersionID.Eq(versionID)).
		Order(answer.CreateTime.Desc()).
		Limit(int64(limit)).
		All(ctx, s.ex, nil)
	if err != nil {
		return nil, fmt.Errorf("store: recent questions: %w", err)
	}
	out := make([]map[string]string, 0, len(rows))
	for _, r := range rows {
		var f map[string]string
		if json.Unmarshal(r.Fields, &f) == nil && len(f) > 0 {
			out = append(out, f)
		}
	}
	return out, nil
}
