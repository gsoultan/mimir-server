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

package dbsource

import (
	"context"
	"fmt"
	"net"
	"syscall"
	"time"

	"github.com/gsoultan/caduceus"
	"github.com/gsoultan/caduceus/codec"
	"github.com/gsoultan/caduceus/wire"
)

// fetchPostgres is Fetch for PostgreSQL, through caduceus. The query runs
// in a read-only transaction, and every address dialled, after names are
// resolved and for each fallback host, is put to guard first.
func fetchPostgres(ctx context.Context, dsn, query string, limit int, guard Guard) (Result, error) {
	cfg, err := wire.ParseConfig(dsn)
	if err != nil {
		return Result{}, fmt.Errorf("%w: %v", ErrBadQuery, err)
	}
	d := &net.Dialer{Timeout: 30 * time.Second, KeepAlive: 5 * time.Minute}
	if guard != nil {
		database := cfg.Database
		d.Control = func(network, address string, _ syscall.RawConn) error {
			return guard(network, address, database)
		}
	}
	cfg.DialFunc = d.DialContext
	// Intervals come as PostgreSQL writes them, "1 day 02:00:00", rather
	// than as a Go duration, which would lose the months and days.
	conn, err := caduceus.ConnectConfig(ctx, cfg, caduceus.Options{ResultFormat: textIntervals})
	if err != nil {
		return Result{}, fmt.Errorf("%w: couldn't connect: %v", ErrBadQuery, err)
	}
	defer conn.Close(context.Background())
	tx, err := conn.BeginTx(ctx, caduceus.TxOptions{ReadOnly: true})
	if err != nil {
		return Result{}, fmt.Errorf("%w: couldn't connect: %v", ErrBadQuery, err)
	}
	defer tx.Rollback(context.Background())
	rows, err := tx.Query(ctx, query)
	if err != nil {
		return Result{}, fmt.Errorf("%w: %v", ErrBadQuery, err)
	}
	defer rows.Close()
	fields := rows.Fields()
	if len(fields) < 2 {
		return Result{}, fmt.Errorf("%w: the query should return at least two columns: the answer and something to learn from", ErrBadQuery)
	}
	if len(fields) > MaxColumns {
		return Result{}, fmt.Errorf("%w: at most %d columns", ErrBadQuery, MaxColumns)
	}
	res := Result{Columns: make([]string, len(fields))}
	for i, f := range fields {
		res.Columns[i] = f.Name
	}
	for rows.Next() {
		if len(res.Rows) >= limit {
			break
		}
		row := make([]string, len(fields))
		for i, raw := range rows.RawValues() {
			v, err := cell(fields[i], raw)
			if err != nil {
				return Result{}, fmt.Errorf("%w: column %q: %v", ErrBadQuery, fields[i].Name, err)
			}
			row[i] = v
		}
		res.Rows = append(res.Rows, row)
	}
	if err := rows.Err(); err != nil {
		return Result{}, fmt.Errorf("%w: %v", ErrBadQuery, err)
	}
	return res, nil
}

// textIntervals is caduceus's choice of format for every column but an
// interval, which is asked for as text.
func textIntervals(oid uint32) int16 {
	if oid == codec.Interval {
		return 0
	}
	return codec.ResultFormat(oid)
}

// cell renders one value as text. json and jsonb keep the text the server
// sent, as does an interval, a uuid is written with its dashes, and the rest go through text.
func cell(f wire.Field, raw []byte) (string, error) {
	if raw == nil {
		return "", nil
	}
	switch f.DataTypeOID {
	case codec.JSON, codec.JSONB, codec.Interval:
		return string(raw), nil
	}
	v, err := codec.Natural(f.DataTypeOID, f.Format, raw)
	if err != nil {
		return "", err
	}
	if u, ok := v.([16]byte); ok {
		return fmt.Sprintf("%x-%x-%x-%x-%x", u[0:4], u[4:6], u[6:8], u[8:10], u[10:16]), nil
	}
	return text(v), nil
}
