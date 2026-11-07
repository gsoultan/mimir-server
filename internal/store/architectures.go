// Copyright (c) 2026 Gembit Soultan Shirazi
// SPDX-License-Identifier: AGPL-3.0-only OR LicenseRef-Commercial
//
// This file is part of mimir-server, a server that trains, serves and manages mimir models,
// created and owned by Gembit Soultan Shirazi.
// It is dual-licensed: you may use it under the GNU Affero General Public
// License, version 3, as published by the Free Software Foundation, or
// under a commercial license from Gembit Soultan Shirazi. See the LICENSE and
// LICENSING.md files at the root of this repository.
//
// This program is distributed in the hope that it will be useful, but
// WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the GNU Affero
// General Public License for more details.

package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/gsoultan/storm/runtime"

	"github.com/gsoultan/mimir-server/internal/store/db/architecture"
	"github.com/gsoultan/mimir-server/internal/store/db/model"
)

// Architecture is a model type written in someone's own code and run on
// workers, as described to the server.
type Architecture struct {
	ID          string
	Name        string
	Title       string
	Description string
	Input       string // "table", "image" or "tensor"
	Output      string // "class", "number" or "ranked"
	Items       int    // how many a ranked answer lists
	CreatedBy   string
	Created     time.Time
}

// ErrArchitectureTaken is what saving a new architecture under a name in
// use returns.
var ErrArchitectureTaken = errors.New("store: architecture name in use")

// ErrArchitectureInUse is what deleting an architecture that models still
// have returns.
var ErrArchitectureInUse = errors.New("store: architecture has models")

func fromArchitecture(r architecture.Row) Architecture {
	return Architecture{
		ID: r.ID, Name: r.Name, Title: r.Title, Description: r.Description,
		Input: r.Input, Output: r.Output, Items: int(r.Items),
		CreatedBy: r.CreatedBy, Created: r.CreateTime.UTC(),
	}
}

// ListArchitectures returns the described architectures by name.
func (s *Store) ListArchitectures(ctx context.Context) ([]Architecture, error) {
	rows, err := architecture.New().Order(architecture.Name.Asc()).Limit(1000).All(ctx, s.ex, nil)
	if err != nil {
		return nil, fmt.Errorf("store: list architectures: %w", err)
	}
	out := make([]Architecture, len(rows))
	for i, r := range rows {
		out[i] = fromArchitecture(r)
	}
	return out, nil
}

// GetArchitecture returns the described architecture called name.
func (s *Store) GetArchitecture(ctx context.Context, name string) (Architecture, error) {
	r, ok, err := architecture.New().Where(architecture.Name.Eq(name)).One(ctx, s.ex)
	if err != nil {
		return Architecture{}, err
	}
	if !ok {
		return Architecture{}, ErrNotFound
	}
	return fromArchitecture(r), nil
}

// CreateArchitecture saves a new architecture, filling in ID and Created.
func (s *Store) CreateArchitecture(ctx context.Context, a Architecture) (Architecture, error) {
	id, err := newID()
	if err != nil {
		return Architecture{}, err
	}
	a.ID, a.Created = id, now()
	n := architecture.Create()
	n.SetID(a.ID)
	n.SetCreateTime(a.Created)
	n.SetName(a.Name)
	n.SetTitle(a.Title)
	n.SetDescription(a.Description)
	n.SetInput(a.Input)
	n.SetOutput(a.Output)
	n.SetItems(int32(a.Items))
	n.SetCreatedBy(a.CreatedBy)
	if _, err := n.Insert(ctx, s.ex); err != nil {
		if errors.Is(err, runtime.ErrUniqueViolation) {
			return Architecture{}, ErrArchitectureTaken
		}
		return Architecture{}, fmt.Errorf("store: insert architecture: %w", err)
	}
	return a, nil
}

// UpdateArchitecture changes what an architecture is called and what it
// reads and answers; its name stays.
func (s *Store) UpdateArchitecture(ctx context.Context, a Architecture) error {
	m := architecture.MutateKey(a.ID)
	m.SetTitle(a.Title)
	m.SetDescription(a.Description)
	m.SetInput(a.Input)
	m.SetOutput(a.Output)
	m.SetItems(int32(a.Items))
	if err := m.Update(ctx, s.ex); err != nil {
		if errors.Is(err, runtime.ErrNoRow) {
			return ErrNotFound
		}
		return fmt.Errorf("store: update architecture: %w", err)
	}
	return nil
}

// DeleteArchitecture removes an architecture no model uses.
func (s *Store) DeleteArchitecture(ctx context.Context, a Architecture) error {
	used, err := model.New().Where(model.Architecture.Eq(a.Name)).Exists(ctx, s.ex)
	if err != nil {
		return err
	}
	if used {
		return ErrArchitectureInUse
	}
	if err := architecture.Delete(ctx, s.ex, a.ID); err != nil {
		if errors.Is(err, runtime.ErrNoRow) {
			return ErrNotFound
		}
		return fmt.Errorf("store: delete architecture: %w", err)
	}
	return nil
}
