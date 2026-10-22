package store

import (
	"context"
	"testing"
	"time"

	"github.com/gsoultan/mimir-server/internal/store/db/auditevent"
	"github.com/gsoultan/mimir-server/internal/testdb"
)

// With a log database the audit log is written, read, counted and trimmed
// there, and team names still come from the main database.
func TestLogDatabase(t *testing.T) {
	ctx := context.Background()
	mainURL, logURL := testdb.URL(t), testdb.URL(t)
	s, err := Open(ctx, mainURL, t.TempDir(), WithLogDatabase(logURL))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.Ping(ctx); err != nil {
		t.Fatal(err)
	}
	team, err := s.CreateTeam(ctx, "Research")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Audit(ctx, AuditEvent{Actor: "a@example.com", Action: "model.create", Target: "m1", TeamID: team.ID}); err != nil {
		t.Fatal(err)
	}
	if err := s.Audit(ctx, AuditEvent{Actor: "a@example.com", Action: "worker.join", Target: "w1", Time: time.Now().Add(-48 * time.Hour)}); err != nil {
		t.Fatal(err)
	}

	inMain, err := auditevent.New().Count(ctx, s.ex)
	if err != nil {
		t.Fatal(err)
	}
	inLog, err := auditevent.New().Count(ctx, s.logEx)
	if err != nil {
		t.Fatal(err)
	}
	if inMain != 0 || inLog != 2 {
		t.Fatalf("events: %d in the main database and %d in the log one, want 0 and 2", inMain, inLog)
	}

	events, err := s.ListAuditEvents(ctx, team.ID, "", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].TeamName != "Research" {
		t.Fatalf("team events: %+v", events)
	}
	if ev, err := s.WorkerEvents(ctx, "w1", 10); err != nil || len(ev) != 1 {
		t.Fatalf("worker events: %v, %v", ev, err)
	}
	// A deleted team's events stay, without its name.
	if err := s.DeleteTeam(ctx, team.ID); err != nil {
		t.Fatal(err)
	}
	if events, err = s.ListAuditEvents(ctx, "", "", 10); err != nil || len(events) != 2 {
		t.Fatalf("all events: %v, %v", events, err)
	}

	st, err := s.Stats(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if st.AuditEvents != 2 || st.LogPath == "" {
		t.Fatalf("stats: %+v", st)
	}
	if n, err := s.Trim(ctx, 24*time.Hour, 24*time.Hour); err != nil || n < 1 {
		t.Fatalf("trim: %d, %v", n, err)
	}
	if inLog, _ = auditevent.New().Count(ctx, s.logEx); inLog != 1 {
		t.Fatalf("after the trim the log holds %d events, want 1", inLog)
	}
	s.Close()

	// Reopening finds the log schema already in place.
	s2, err := Open(ctx, mainURL, t.TempDir(), WithLogDatabase(logURL))
	if err != nil {
		t.Fatal(err)
	}
	s2.Close()
}

// CheckDatabase connects without changing the database.
func TestCheckDatabase(t *testing.T) {
	ctx := context.Background()
	url := testdb.URL(t)
	version, has, err := CheckDatabase(ctx, url)
	if err != nil || version == "" || has {
		t.Fatalf("empty database: %q, %v, %v", version, has, err)
	}
	s, err := Open(ctx, url, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateUser(ctx, User{Email: "o@example.com", Name: "O", PasswordHash: "x", Access: Access{Role: Owner, AllModels: true}}); err != nil {
		t.Fatal(err)
	}
	s.Close()
	if _, has, err = CheckDatabase(ctx, url); err != nil || !has {
		t.Fatalf("database with an owner: %v, %v", has, err)
	}
	if _, _, err := CheckDatabase(ctx, "postgres://nobody:pw@127.0.0.1:1/none?connect_timeout=2"); err == nil {
		t.Fatal("no error for a database that isn't there")
	}
}
