package store

import (
	"context"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/gsoultan/mimir-server/internal/testdb"
)

// A dump restores into an empty database with its rows, a second restore
// needs replace, and Plan finds nothing left to change.
func TestDumpRestore(t *testing.T) {
	if _, err := exec.LookPath("pg_dump"); err != nil {
		t.Skip("pg_dump is not installed")
	}
	ctx := context.Background()
	from, to := testdb.URL(t), testdb.URL(t)
	s, err := Open(ctx, from, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	team, err := s.CreateTeam(ctx, "Research")
	s.Close()
	if err != nil {
		t.Fatal(err)
	}

	dump := filepath.Join(t.TempDir(), "main.dump")
	if err := Dump(ctx, from, dump); err != nil {
		t.Fatal(err)
	}
	if empty, err := Empty(ctx, to); err != nil || !empty {
		t.Fatalf("Empty(new database) = %v, %v; want true", empty, err)
	}
	if err := Restore(ctx, to, dump, false); err != nil {
		t.Fatal(err)
	}
	if empty, err := Empty(ctx, to); err != nil || empty {
		t.Fatalf("Empty(restored) = %v, %v; want false", empty, err)
	}
	if err := Restore(ctx, to, dump, false); err == nil {
		t.Error("restoring over the tables without replace worked")
	}
	if err := Restore(ctx, to, dump, true); err != nil {
		t.Fatalf("replace: %v", err)
	}
	steps, err := Plan(ctx, to, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(steps) != 0 {
		t.Errorf("Plan(restored) = %d steps, want none: %+v", len(steps), steps)
	}
	r, err := Open(ctx, to, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	got, err := r.GetTeam(ctx, team.ID)
	if err != nil || got.Name != "Research" {
		t.Errorf("restored team = %+v, %v; want Research", got, err)
	}
	r.Close()

	// A table only a newer version has outlives a restore over it, and
	// Plan says this version would refuse it.
	if err := execOn(ctx, to, `CREATE TABLE from_a_newer_version (id text PRIMARY KEY)`); err != nil {
		t.Fatal(err)
	}
	if err := Restore(ctx, to, dump, true); err != nil {
		t.Fatalf("replace: %v", err)
	}
	if steps, err := Plan(ctx, to, false); err != nil || len(steps) != 1 || !steps[0].Refused {
		t.Errorf("Plan(restored over a newer table) = %+v, %v; want one refused step", steps, err)
	}
}

func execOn(ctx context.Context, url, sql string) error {
	conn, err := pgx.Connect(ctx, url)
	if err != nil {
		return err
	}
	defer conn.Close(ctx)
	_, err = conn.Exec(ctx, sql)
	return err
}

// Plan reports a column the model doesn't have as a step the server
// refuses, which is what an older version sees on a newer database.
func TestPlanRefusesNewerDatabase(t *testing.T) {
	ctx := context.Background()
	url := testdb.URL(t)
	s, err := Open(ctx, url, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err := s.pool.Exec(ctx, `ALTER TABLE teams ADD COLUMN from_a_newer_version text`); err != nil {
		t.Fatal(err)
	}
	steps, err := Plan(ctx, url, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(steps) != 1 || !steps[0].Refused {
		t.Fatalf("Plan = %+v, want one refused step", steps)
	}
}
