// Package dbsource fetches a table of examples from a database query, so
// a model can learn from live data and keep learning as it changes.
package dbsource

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/csv"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	_ "github.com/go-sql-driver/mysql" // mysql
	_ "github.com/jackc/pgx/v5/stdlib" // pgx
	_ "modernc.org/sqlite"             // sqlite
)

// ErrBadQuery reports a query or connection the caller can fix.
var ErrBadQuery = errors.New("dbsource: bad query")

// Limits on one fetch.
const (
	MaxRows    = 1_000_000
	MaxColumns = 1001
	Timeout    = 5 * time.Minute
)

// Drivers lists what can be connected to, by the name used in the API.
var Drivers = map[string]string{"postgres": "pgx", "mysql": "mysql", "sqlite": "sqlite"}

// Result is a query's rows as text, ready to be written as CSV.
type Result struct {
	Columns []string
	Rows    [][]string
}

// CSV returns the result with a header row.
func (r Result) CSV() []byte {
	var buf bytes.Buffer
	w := csv.NewWriter(&buf)
	w.Write(r.Columns)
	w.WriteAll(r.Rows)
	return buf.Bytes()
}

// Fetch runs query read-only and returns up to limit rows. Nothing is
// written to the database: the query runs in a read-only transaction
// where the driver supports one, and sqlite files are opened read-only.
func Fetch(ctx context.Context, driver, dsn, query string, limit int) (Result, error) {
	name, ok := Drivers[driver]
	if !ok {
		return Result{}, fmt.Errorf("%w: choose postgres, mysql or sqlite", ErrBadQuery)
	}
	if strings.TrimSpace(dsn) == "" {
		return Result{}, fmt.Errorf("%w: the connection is empty", ErrBadQuery)
	}
	if strings.TrimSpace(query) == "" {
		return Result{}, fmt.Errorf("%w: the query is empty", ErrBadQuery)
	}
	if driver == "sqlite" {
		if !strings.HasPrefix(dsn, "file:") {
			dsn = "file:" + dsn
		}
		sep := "?"
		if strings.Contains(dsn, "?") {
			sep = "&"
		}
		dsn += sep + "mode=ro&_pragma=query_only(1)"
	}
	if limit <= 0 || limit > MaxRows {
		limit = MaxRows
	}
	ctx, cancel := context.WithTimeout(ctx, Timeout)
	defer cancel()

	db, err := sql.Open(name, dsn)
	if err != nil {
		return Result{}, fmt.Errorf("%w: %v", ErrBadQuery, err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: driver != "sqlite"})
	if err != nil {
		return Result{}, fmt.Errorf("%w: couldn't connect: %v", ErrBadQuery, err)
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, query)
	if err != nil {
		return Result{}, fmt.Errorf("%w: %v", ErrBadQuery, err)
	}
	defer rows.Close()
	cols, err := rows.Columns()
	if err != nil {
		return Result{}, err
	}
	if len(cols) < 2 {
		return Result{}, fmt.Errorf("%w: the query should return at least two columns: the answer and something to learn from", ErrBadQuery)
	}
	if len(cols) > MaxColumns {
		return Result{}, fmt.Errorf("%w: at most %d columns", ErrBadQuery, MaxColumns)
	}
	res := Result{Columns: cols}
	vals := make([]any, len(cols))
	ptrs := make([]any, len(cols))
	for i := range vals {
		ptrs[i] = &vals[i]
	}
	for rows.Next() {
		if len(res.Rows) >= limit {
			break
		}
		if err := rows.Scan(ptrs...); err != nil {
			return Result{}, fmt.Errorf("%w: %v", ErrBadQuery, err)
		}
		row := make([]string, len(cols))
		for i, v := range vals {
			row[i] = text(v)
		}
		res.Rows = append(res.Rows, row)
	}
	if err := rows.Err(); err != nil {
		return Result{}, fmt.Errorf("%w: %v", ErrBadQuery, err)
	}
	return res, nil
}

// text renders a database value the way a CSV cell would hold it.
func text(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case []byte:
		return string(x)
	case string:
		return x
	case float64:
		return strconv.FormatFloat(x, 'g', -1, 64)
	case float32:
		return strconv.FormatFloat(float64(x), 'g', -1, 32)
	case bool:
		if x {
			return "1"
		}
		return "0"
	case time.Time:
		return x.Format(time.RFC3339)
	default:
		return fmt.Sprint(x)
	}
}
