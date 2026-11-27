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
	"bytes"
	"context"
	"encoding/csv"
	"errors"
	"fmt"
	"hash/fnv"
	"io"
	"time"

	"connectrpc.com/connect"

	mimirv1 "github.com/gsoultan/mimir-server/gen/mimir/v1"
	"github.com/gsoultan/mimir-server/internal/auth"
	"github.com/gsoultan/mimir-server/internal/schema"
	"github.com/gsoultan/mimir-server/internal/store"
	"github.com/gsoultan/mimir-server/internal/synth"
	"github.com/gsoultan/mimir-server/internal/training"
)

// makeRowsWork bounds rows × columns² of one MakeRows call: each made-up
// row costs about columns² steps.
const makeRowsWork = 4_000_000_000

// MakeRows makes up a new dataset of rows like a table dataset's own.
func (s *Service) MakeRows(ctx context.Context, req *connect.Request[mimirv1.MakeRowsRequest]) (*connect.Response[mimirv1.MakeRowsResponse], error) {
	p, _ := auth.From(ctx)
	d, err := s.getDataset(ctx, req.Msg.GetDatasetId())
	if err != nil {
		return nil, err
	}
	if d.Kind != store.DatasetTable {
		return nil, invalid("Rows can only be made up like a table's. Photos, sounds and writing aren't supported yet.")
	}
	n := req.Msg.GetRows()
	if n < synth.MinRows || n > training.MaxRows {
		return nil, invalid("Make between %d and %d rows.", synth.MinRows, training.MaxRows)
	}
	name := req.Msg.GetName()
	if name == "" {
		name = d.Name + " (made up)"
	}
	if name, err = cleanName("dataset", name); err != nil {
		return nil, err
	}
	b, err := s.store.ReadDataset(d.ID)
	if err != nil {
		return nil, s.internal(ctx, err)
	}
	// Rows a benchmark keeps for testing aren't learned from here either,
	// or models taught on the made-up rows would have seen the test.
	if b, err = training.WithoutBenchmarks(ctx, s.store, d.ID, b); err != nil {
		return nil, s.internal(ctx, err)
	}
	header, rows, err := readCSV(b)
	if err != nil {
		return nil, invalid("The dataset's table can't be read: %v", err)
	}
	if cols := int64(len(header)); n*cols*cols > makeRowsWork {
		return nil, invalid("That is a lot of rows for %d columns. Make at most %d at a time.", cols, makeRowsWork/(cols*cols))
	}
	kinds := map[string]schema.Kind{}
	for _, c := range d.Columns {
		kinds[c.Name] = c.Kind
	}
	if d.Target != "" {
		kinds[d.Target] = schema.Category
		if d.NumberTarget && len(d.Labels) == 0 {
			kinds[d.Target] = schema.Number
		}
	}
	h := fnv.New64a()
	fmt.Fprintf(h, "%s/%d/%d", d.ID, n, time.Now().UnixNano())
	seed := h.Sum64()
	m, err := synth.Fit(header, rows, kinds, seed)
	if errors.Is(err, synth.ErrTooFew) {
		return nil, invalid("There are too few rows to learn from. At least %d are needed.", synth.MinRows)
	}
	if err != nil {
		return nil, s.internal(ctx, err)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	w := csv.NewWriter(&buf)
	w.Write(header)
	w.WriteAll(m.Rows(int(n), seed))
	if err := w.Error(); err != nil {
		return nil, s.internal(ctx, err)
	}
	if buf.Len() > maxCSVBytes {
		return nil, invalid("That many rows would be over 64 MB. Make fewer.")
	}
	file := buf.Bytes()
	made, check, err := describeTable(file, d.Target, nil, nil)
	if err != nil {
		return nil, err
	}
	made.Name, made.TeamID, made.MadeFrom = name, p.Team(), d.ID
	made, err = s.store.CreateDataset(ctx, made, file)
	if err != nil {
		return nil, s.internal(ctx, err)
	}
	s.audit(ctx, p.Actor(), "dataset.make_rows", made.Name, fmt.Sprintf("%d rows made up like %s", made.Rows, d.Name))
	return connect.NewResponse(&mimirv1.MakeRowsResponse{Dataset: datasetProto(made), Check: check}), nil
}

// readCSV reads a table with a header row.
func readCSV(b []byte) (header []string, rows [][]string, err error) {
	r := csv.NewReader(bytes.NewReader(b))
	r.TrimLeadingSpace = true
	r.FieldsPerRecord = -1
	if header, err = r.Read(); err != nil {
		return nil, nil, err
	}
	for {
		rec, err := r.Read()
		if errors.Is(err, io.EOF) {
			return header, rows, nil
		}
		if err != nil {
			return nil, nil, err
		}
		rows = append(rows, rec)
	}
}
