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

// Package tables is the schema of the server's own PostgreSQL database,
// declared as storm models: one struct per table, one table per file, with a
// Schema method for what the Go type cannot say.
//
// storm generates the typed query packages under internal/store/db from
// these structs (`storm generate internal/store/db`), `storm diff` writes the
// reviewable migrations under internal/store/migrations, and the server
// applies the model at startup with migrate.Auto. After changing a struct or
// a Schema method, run all three steps; CI fails with `storm verify -stale`
// or `storm verify -pending` when one was forgotten.
//
// The shared columns are mixins (mixins.go): Record is the server-made id
// and creation time every top-level row has, TeamOwned the team a resource
// belongs to for good. The small closed sets (roles, statuses, kinds) are
// named integer types (enums.go) that package store uses as its own, so a
// column, its CHECK and the Go constants are declared once.
//
// Ids are UUIDv7 strings made by the server and kept as text. Times are
// timestamptz. Lists are PostgreSQL arrays; documents with a shape of their
// own (a dataset's columns, a run's history) are jsonb.
package tables

// All is every model, for storm.Build and migrate.Auto.
func All() []any {
	return []any{
		&Team{}, &User{}, &Model{}, &UserModel{}, &Session{}, &RecoveryCode{},
		&APIKey{}, &APIKeyModel{}, &AuditEvent{}, &Webhook{}, &Delivery{},
		&Setting{}, &Dataset{}, &TrainingJob{}, &Worker{}, &WorkerSample{}, &Usage{},
		&Architecture{}, &ModelVersion{}, &Benchmark{}, &BenchmarkScore{}, &SignInIdentity{},
		&Answer{}, &AnswerDay{}, &Scoring{}, &Collection{}, &Document{}, &Passage{},
		&VisitPlan{}, &VisitPlanSalesperson{}, &DataCheck{},
	}
}
