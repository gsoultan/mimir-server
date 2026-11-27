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
	"errors"
	"strings"
	"testing"

	"github.com/gsoultan/mimir-server/internal/testdb"
)

// TestFetchPostgres renders each kind of column the way the CSV a model
// learns from has always held it.
func TestFetchPostgres(t *testing.T) {
	url := testdb.URL(t)
	ctx := context.Background()
	query := `SELECT 1::int2 AS a, 2::int4 AS b, 3::int8 AS c, 1.5::float4 AS d, 2.25::float8 AS e,
		12.340::numeric AS f, true AS g, 'hi'::text AS h, 'x'::varchar AS i,
		'2026-11-27 08:30:15.5+00'::timestamptz AS j, '2026-11-27'::date AS k,
		'2026-11-27 08:30:15'::timestamp AS l, '1 day 02:00:00'::interval AS m,
		'0b9e1a4c-8f2d-4e6a-9b1c-2d3e4f5a6b7c'::uuid AS n, '{"a": 1}'::jsonb AS o,
		'{"b":2}'::json AS p, '\x01ff'::bytea AS q, NULL::text AS r, '10.0.0.1'::inet AS s,
		'08:30:00'::time AS t, 'red'::text AS u`
	r, err := Fetch(ctx, "postgres", url, query, 10, nil)
	if err != nil {
		t.Fatal(err)
	}
	got := string(r.CSV())
	want := "a,b,c,d,e,f,g,h,i,j,k,l,m,n,o,p,q,r,s,t,u\n" +
		`1,2,3,1.5,2.25,12.340,1,hi,x,2026-11-27T08:30:15Z,2026-11-27T00:00:00Z,2026-11-27T08:30:15Z,1 day 02:00:00,0b9e1a4c-8f2d-4e6a-9b1c-2d3e4f5a6b7c,"{""a"": 1}","{""b"":2}",` + "\x01\xff" + `,,10.0.0.1,08:30:00,red` + "\n"
	if got != want {
		t.Fatalf("csv =\n%q\nwant\n%q", got, want)
	}

	if r, err := Fetch(ctx, "postgres", url, "SELECT g, g FROM generate_series(1, 50) g", 7, nil); err != nil || len(r.Rows) != 7 {
		t.Fatalf("limit: %d rows, %v", len(r.Rows), err)
	}
	for _, q := range []string{"SELECT 1", "SELECT * FROM nothing", "CREATE TABLE t (a int, b int)"} {
		if _, err := Fetch(ctx, "postgres", url, q, 10, nil); !errors.Is(err, ErrBadQuery) {
			t.Errorf("%q: %v, want ErrBadQuery", q, err)
		}
	}
	refuse := errors.New("not there")
	_, err = Fetch(ctx, "postgres", url, "SELECT 1, 2", 10, func(network, address, database string) error { return refuse })
	if err == nil || !strings.Contains(err.Error(), "not there") {
		t.Fatalf("guard: %v, want the guard's refusal", err)
	}
}
