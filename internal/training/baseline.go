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


package training

import (
	"bytes"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"

	"github.com/gsoultan/mimir-server/internal/drift"
	"github.com/gsoultan/mimir-server/internal/schema"
	"github.com/gsoultan/mimir-server/internal/store"
)

// Baseline describes what the examples in src looked like in the columns
// cols a model reads, for drift to compare its questions with. Only a
// table, alone or with photos, has columns; other sources return nil.
func Baseline(src Source, cols []schema.Column) ([]drift.Column, error) {
	b := src.CSV
	switch src.Kind {
	case store.DatasetTable:
	case store.DatasetPhotoTable:
		arc, err := OpenArchive(src.CSV)
		if err != nil {
			return nil, err
		}
		if _, b, err = arc.Table(); err != nil {
			return nil, err
		}
	default:
		return nil, nil
	}
	if len(cols) == 0 {
		return nil, nil
	}
	cr := csv.NewReader(bytes.NewReader(b))
	cr.TrimLeadingSpace = true
	cr.ReuseRecord = true
	cr.FieldsPerRecord = -1
	header, err := cr.Read()
	if err != nil {
		return nil, fmt.Errorf("%w: the first row must name the columns", ErrBadTable)
	}
	header = slices.Clone(header)
	for i := range header {
		header[i] = strings.TrimSpace(strings.TrimPrefix(header[i], "\ufeff"))
	}
	ti := len(header) - 1
	if src.Target != "" {
		ti = slices.Index(header, src.Target)
	}
	// Where each column the model reads is in the table; -1 for one the
	// table no longer has, which then reads as empty.
	at := make([]int, len(cols))
	for j, c := range cols {
		at[j] = slices.Index(header, c.Name)
	}
	bl := drift.NewBuilder(cols)
	cells := make([]string, len(cols))
	for rows := 0; rows < MaxRows; rows++ {
		rec, err := cr.Read()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("%w: %v", ErrBadTable, err)
		}
		// Rows without an answer weren't learned from.
		if ti >= 0 && (ti >= len(rec) || strings.TrimSpace(rec[ti]) == "") {
			continue
		}
		for j, i := range at {
			cells[j] = ""
			if i >= 0 && i < len(rec) {
				cells[j] = rec[i]
			}
		}
		bl.Add(cells)
	}
	return bl.Columns(), nil
}
