package dbsource

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
)

func TestFetchSQLite(t *testing.T) {
	path := filepath.Join(t.TempDir(), "shop.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("CREATE TABLE fruit (weight REAL, height REAL, kind TEXT); INSERT INTO fruit VALUES (150, 7, 'apple'), (170.5, 10, 'pear'), (NULL, 4, 'plum')"); err != nil {
		t.Fatal(err)
	}
	db.Close()
	ctx := context.Background()

	r, err := Fetch(ctx, "sqlite", path, "SELECT weight, height, kind FROM fruit ORDER BY height", 10)
	if err != nil {
		t.Fatal(err)
	}
	got := string(r.CSV())
	want := "weight,height,kind\n,4,plum\n150,7,apple\n170.5,10,pear\n"
	if got != want {
		t.Fatalf("csv = %q, want %q", got, want)
	}
	// Limits, errors and read-only.
	if r, _ := Fetch(ctx, "sqlite", path, "SELECT * FROM fruit", 2); len(r.Rows) != 2 {
		t.Fatalf("limit: %d rows", len(r.Rows))
	}
	for _, c := range []struct{ driver, query string }{
		{"sqlite", "SELECT kind FROM fruit"},
		{"sqlite", "SELECT * FROM nothing"},
		{"sqlite", "DELETE FROM fruit"},
		{"oracle", "SELECT 1, 2"},
		{"sqlite", "  "},
	} {
		if _, err := Fetch(ctx, c.driver, path, c.query, 10); err == nil || !strings.Contains(err.Error(), "dbsource: bad query") {
			t.Errorf("%s %q: %v", c.driver, c.query, err)
		}
	}
	if r, err := Fetch(ctx, "sqlite", path, "SELECT * FROM fruit", 10); err != nil || len(r.Rows) != 3 {
		t.Fatalf("after DELETE attempt: %v rows, %v", len(r.Rows), err)
	}
}
