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
	"errors"
	"fmt"
	"io/fs"
	"os"
	"time"

	"github.com/gsoultan/storm"
	"github.com/gsoultan/storm/runtime"

	"github.com/gsoultan/mimir-server/internal/mapfile"
	"github.com/gsoultan/mimir-server/internal/store/db/answer"
	"github.com/gsoultan/mimir-server/internal/store/db/model"
)

// Trial is a version of a model tried on some of its questions before it
// goes live for everyone. With Shadow it answers a copy of every question
// and only its results are kept; otherwise it answers Share percent of
// them, and those askers get its answer.
type Trial struct {
	VersionID string
	Share     int
	Shadow    bool
	Start     time.Time
}

// On reports whether a version is on trial.
func (t Trial) On() bool { return t.VersionID != "" }

// StartTrial puts version versionID of a model on trial, replacing any
// trial it had, and returns the model as it is then. share is the percent
// of questions the version answers, 1 to 99; with shadow it answers a
// copy of every question instead and share is not used.
func (s *Store) StartTrial(ctx context.Context, modelID, versionID string, share int, shadow bool) (Model, error) {
	v, err := s.GetVersion(ctx, modelID, versionID)
	if err != nil {
		return Model{}, err
	}
	if _, err := os.Stat(s.versionPath(v.ID)); err != nil {
		return Model{}, fmt.Errorf("store: version %d's weights: %w", v.Number, err)
	}
	if shadow {
		share = 0
	}
	err = storm.InTx(ctx, s.ex, func(ex storm.Executor) error {
		if _, err := s.lockModel(ctx, ex, modelID); err != nil {
			return err
		}
		u := model.MutateKey(modelID)
		u.SetTrialVersionID(v.ID)
		u.SetTrialShare(int32(share))
		u.SetTrialShadow(shadow)
		u.SetTrialStart(now())
		return u.Update(ctx, ex)
	})
	if err != nil {
		if errors.Is(err, runtime.ErrNoRow) {
			return Model{}, ErrNotFound
		}
		return Model{}, err
	}
	return s.Get(ctx, modelID)
}

// StopTrial ends a model's trial, if it has one, leaving the live version
// answering every question, and returns the model as it is then.
func (s *Store) StopTrial(ctx context.Context, modelID string) (Model, error) {
	u := model.MutateKey(modelID)
	clearTrial(&u)
	if err := u.Update(ctx, s.ex); err != nil {
		if errors.Is(err, runtime.ErrNoRow) {
			return Model{}, ErrNotFound
		}
		return Model{}, fmt.Errorf("store: stop trial: %w", err)
	}
	return s.Get(ctx, modelID)
}

func clearTrial(u *model.Mut) {
	u.SetTrialVersionID("")
	u.SetTrialShare(0)
	u.SetTrialShadow(false)
	u.SetTrialStartNull()
}

// SetNewVersionShare sets the percent of questions a version keep-learning
// makes is tried on before it goes live, 0 to 100; 0 puts it live at once.
func (s *Store) SetNewVersionShare(ctx context.Context, modelID string, share int) (Model, error) {
	u := model.MutateKey(modelID)
	u.SetNewVersionShare(int32(share))
	if err := u.Update(ctx, s.ex); err != nil {
		if errors.Is(err, runtime.ErrNoRow) {
			return Model{}, ErrNotFound
		}
		return Model{}, fmt.Errorf("store: set new version share: %w", err)
	}
	return s.Get(ctx, modelID)
}

// VersionTally is how the answers one version gave since a time did.
type VersionTally struct {
	VersionID string
	// Answers it gave, how many of them apps sent feedback on and how many
	// of those it got right.
	Answers  int64
	Feedback int64
	Correct  int64
	// Shadow is how many of its answers were to copies of questions, and
	// Agree how many of those matched the live version's answer.
	Shadow int64
	Agree  int64
}

// TrialTallies returns, by version, how a model's answers since since did.
func (s *Store) TrialTallies(ctx context.Context, modelID string, since time.Time) ([]VersionTally, error) {
	rows, err := answer.New().
		Where(answer.ModelID.Eq(modelID)).
		Where(answer.CreateTime.Gte(since)).
		Unordered().
		AllPerVersion(ctx, s.ex)
	if err != nil {
		return nil, fmt.Errorf("store: trial tallies: %w", err)
	}
	out := make([]VersionTally, len(rows))
	for i, r := range rows {
		out[i] = VersionTally{VersionID: r.VersionID, Answers: r.Answers, Feedback: r.Feedback, Correct: r.Correct, Shadow: r.Shadow, Agree: r.Agree}
	}
	return out, nil
}

// OpenVersionWeights maps a version's weights file into memory, for
// answering with a version on trial.
func (s *Store) OpenVersionWeights(versionID string) (*mapfile.File, error) {
	f, err := mapfile.Open(s.versionPath(versionID))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, ErrVersionNotFound
	}
	return f, err
}

// VersionWeights returns a version's weights file, read whole, for
// sending it to a worker.
func (s *Store) VersionWeights(versionID string) ([]byte, error) {
	b, err := os.ReadFile(s.versionPath(versionID))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, ErrVersionNotFound
	}
	return b, err
}
