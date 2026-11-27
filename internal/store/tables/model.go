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

package tables

import (
	"time"

	"github.com/gsoultan/storm"
)

// Model is a stored model's metadata; its weights live on disk.
type Model struct {
	Record
	Named
	TeamOwned
	Description  string
	Architecture string
	SizeBytes    int64
	SHA256       string
	UpdateTime   time.Time
	InputShape   []int64
	Labels       []string
	Features     []string
	// Columns is the []schema.Column a table model reads, as jsonb.
	Columns  storm.JSON
	JobID    string
	Accuracy float64
	WorkerID string // "" runs it wherever there is room
	// How a language model answers when a chat or app doesn't say: its
	// instructions (the system message), temperature and longest answer
	// in tokens. Other models keep the defaults.
	Instructions string
	Temperature  float64
	MaxTokens    int32
	// OnDevice lets people download the model and answer with it in their
	// own browser, offline too.
	OnDevice bool
	// VersionID is the ModelVersion answering now; "" for a model made
	// before versions were kept, until its first version is recorded.
	VersionID string
	// BaseModelID is the language model a fine-tune adapts: its file is
	// then a LoRA adapter, which runs on top of that model's file. "" for
	// every other model.
	BaseModelID string
	// A trial: TrialVersionID, another of the model's versions, answers
	// TrialShare percent of its questions from TrialStart on, or with
	// TrialShadow a copy of every question while the live version's
	// answer is the one sent. "" when no version is on trial.
	TrialVersionID string
	TrialShare     int32
	TrialShadow    bool
	TrialStart     *time.Time
	// NewVersionShare is the share of questions a version keep-learning
	// makes tries first; 0 puts a better version live at once.
	NewVersionShare int32
}

// Aggregates: how many models each team has, and how many are placed on
// each worker.
func (m *Model) Aggregates(a *storm.Aggregates) {
	perTeam := a.Named("PerTeam")
	perTeam.By(&m.Team)
	perTeam.Count("N")

	perWorker := a.Named("PerWorker")
	perWorker.By(&m.WorkerID)
	perWorker.Count("N")
}

func (m *Model) Schema(t *storm.Table) {
	t.Col(&m.Description).Default("''")
	t.Col(&m.InputShape).Default("'{}'").ConvertFrom(storm.JSONText)
	t.Col(&m.Labels).Default("'{}'").ConvertFrom(storm.JSONText)
	t.Col(&m.Features).Default("'{}'").ConvertFrom(storm.JSONText)
	t.Col(&m.Columns).Default("'[]'").ConvertFrom(storm.JSONText)
	t.Col(&m.JobID).Default("''")
	t.Col(&m.Accuracy).Default("0")
	t.Col(&m.WorkerID).Default("''")
	t.Col(&m.Instructions).Default("''")
	t.Col(&m.Temperature).Default("0.7")
	t.Col(&m.MaxTokens).Default("256")
	t.Col(&m.OnDevice).Default("false")
	t.Col(&m.VersionID).Default("''")
	t.Col(&m.BaseModelID).Default("''")
	t.Col(&m.TrialVersionID).Default("''")
	t.Col(&m.TrialShare).Default("0")
	t.Col(&m.TrialShadow).Default("false")
	t.Col(&m.NewVersionShare).Default("0")
	// A team's models, newest first; also the team key's index.
	t.Col(&m.Team).NoIndex()
	t.Index(&m.Team, storm.Desc(&m.CreateTime), storm.Desc(&m.ID))
	// The models placed on a worker, and unplacing them when it goes.
	t.Index(&m.WorkerID)
	// The fine-tunes of a model, which keep it from being deleted.
	// Partial: most models adapt none.
	t.Index(&m.BaseModelID, &m.ID).Where(storm.RawSQL(`base_model_id <> ''`))
	t.Check(storm.RawSQL(`length(description) <= 500`))
	t.Check(storm.RawSQL(`architecture <> ''`))
	t.Check(storm.RawSQL(`size_bytes > 0`))
	t.Check(storm.RawSQL(`length(sha256) = 64`))
	t.Check(storm.RawSQL(`length(instructions) <= 8000`))
	t.Check(storm.RawSQL(`temperature >= 0 AND temperature <= 2`))
	t.Check(storm.RawSQL(`max_tokens >= 1 AND max_tokens <= 1024`))
	t.Check(storm.RawSQL(`trial_share >= 0 AND trial_share <= 100`))
	t.Check(storm.RawSQL(`new_version_share >= 0 AND new_version_share <= 100`))
}
