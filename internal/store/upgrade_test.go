package store

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/gsoultan/mimir-server/internal/schema"
	"github.com/gsoultan/mimir-server/internal/testdb"
)

// TestUpgradeFrom0001 opens a database a 0001 server wrote, rows and all,
// and checks the upgrade keeps every value: the JSON-in-text lists become
// arrays and jsonb, and read back through the store as they were written.
func TestUpgradeFrom0001(t *testing.T) {
	ctx := context.Background()
	url := testdb.URL(t)

	conn, err := pgx.Connect(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	baseline, err := migrations.ReadFile("migrations/0001_baseline.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(ctx, string(baseline)); err != nil {
		t.Fatalf("apply 0001: %v", err)
	}
	// Rows as the 0001 store wrote them: an empty list as "" on models and
	// "[]" on datasets, webhooks and job history.
	const (
		modelID   = "01900000-0000-7000-8000-0000000000a1"
		emptyID   = "01900000-0000-7000-8000-0000000000a2"
		dataID    = "01900000-0000-7000-8000-0000000000b1"
		jobID     = "01900000-0000-7000-8000-0000000000c1"
		hookID    = "01900000-0000-7000-8000-0000000000d1"
		allHookID = "01900000-0000-7000-8000-0000000000d2"
		userID    = "01900000-0000-7000-8000-0000000000e1"
	)
	at := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	seed := []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO teams (id, name, create_time) VALUES ($1, 'My team', $2)`, []any{DefaultTeam, at}},
		{`INSERT INTO users (id, email, name, password_hash, role, all_models, create_time, team_id)
		  VALUES ($1, 'ada@example.com', 'Ada', 'hash', 4, true, $2, $3)`, []any{userID, at, DefaultTeam}},
		{`INSERT INTO models (id, name, architecture, size_bytes, sha256, create_time, update_time,
		    input_shape, labels, features, team_id, columns)
		  VALUES ($1, 'houses', 'mlp', 10, repeat('a', 64), $2, $2,
		    '[3]', '["cheap","dear \"ish\"","a,b"]', '["rooms","area","note"]', $3,
		    '[{"name":"rooms","kind":"number","fill":2.5},{"name":"note","kind":"category","values":["x","y"]}]')`,
			[]any{modelID, at, DefaultTeam}},
		{`INSERT INTO models (id, name, architecture, size_bytes, sha256, create_time, update_time, team_id)
		  VALUES ($1, 'uploaded', 'mlp', 10, repeat('b', 64), $2, $2, $3)`, []any{emptyID, at, DefaultTeam}},
		{`INSERT INTO datasets (id, name, kind, rows, features, labels, input_shape, target, create_time, team_id, columns)
		  VALUES ($1, 'homes', 4, 120, '["rooms"]', '["cheap","dear"]', '[28,28,3]', 'price', $2, $3,
		    '[{"name":"rooms","kind":"number"}]')`, []any{dataID, at, DefaultTeam}},
		{`INSERT INTO training_jobs (id, name, architecture, dataset_id, epochs, batch_size, learning_rate, hold_out,
		    status, history, create_time, team_id)
		  VALUES ($1, 'run', 'mlp', $2, 2, 32, 0.01, 0.2, 3,
		    '[{"epoch":1,"loss":0.5,"accuracy":0.75,"val_loss":0.6,"millis":120},{"epoch":2,"loss":0.25,"accuracy":0.9,"val_loss":0.3,"millis":110,"mae":1.5}]',
		    $3, $4)`, []any{jobID, dataID, at, DefaultTeam}},
		{`INSERT INTO webhooks (id, name, url, secret, events, create_time, team_id)
		  VALUES ($1, 'chat', 'https://example.com/hook', 's3cret', '["model.created","job.finished"]', $2, $3)`,
			[]any{hookID, at, DefaultTeam}},
		{`INSERT INTO webhooks (id, name, url, secret, events, create_time, team_id)
		  VALUES ($1, 'all', 'https://example.com/all', 's3cret', '[]', $2, $3)`, []any{allHookID, at, DefaultTeam}},
	}
	for _, r := range seed {
		if _, err := conn.Exec(ctx, r.sql, r.args...); err != nil {
			t.Fatalf("seed: %v\n%s", err, r.sql)
		}
	}
	conn.Close(ctx)

	s, err := Open(ctx, url, t.TempDir())
	if err != nil {
		t.Fatalf("Open on a 0001 database: %v", err)
	}
	if s.Applied() == 0 {
		t.Error("Open applied nothing to a 0001 database")
	}

	m, err := s.Get(ctx, modelID)
	if err != nil {
		t.Fatal(err)
	}
	wantCols := []schema.Column{{Name: "rooms", Kind: schema.Number, Fill: 2.5}, {Name: "note", Kind: schema.Category, Values: []string{"x", "y"}}}
	if !reflect.DeepEqual(m.InputShape, []int{3}) ||
		!reflect.DeepEqual(m.Labels, []string{"cheap", `dear "ish"`, "a,b"}) ||
		!reflect.DeepEqual(m.Features, []string{"rooms", "area", "note"}) ||
		!reflect.DeepEqual(m.Columns, wantCols) {
		t.Errorf("model after upgrade = %+v", m)
	}
	if e, err := s.Get(ctx, emptyID); err != nil || e.InputShape != nil || e.Labels != nil || e.Features != nil || e.Columns != nil {
		t.Errorf("uploaded model after upgrade = %+v, %v; want nil lists", e, err)
	}

	d, err := s.GetDataset(ctx, dataID)
	if err != nil {
		t.Fatal(err)
	}
	if d.Kind != DatasetPhotoTable || d.Rows != 120 || d.Target != "price" ||
		!reflect.DeepEqual(d.Features, []string{"rooms"}) ||
		!reflect.DeepEqual(d.Labels, []string{"cheap", "dear"}) ||
		!reflect.DeepEqual(d.InputShape, []int{28, 28, 3}) ||
		!reflect.DeepEqual(d.Columns, []schema.Column{{Name: "rooms", Kind: schema.Number}}) {
		t.Errorf("dataset after upgrade = %+v", d)
	}

	j, err := s.GetJob(ctx, jobID)
	if err != nil {
		t.Fatal(err)
	}
	wantHistory := []Epoch{
		{Epoch: 1, Loss: 0.5, Accuracy: 0.75, ValLoss: 0.6, Millis: 120},
		{Epoch: 2, Loss: 0.25, Accuracy: 0.9, ValLoss: 0.3, Millis: 110, MAE: 1.5},
	}
	if j.Status != JobSucceeded || !reflect.DeepEqual(j.History, wantHistory) {
		t.Errorf("job after upgrade = %+v", j)
	}

	w, err := s.GetWebhook(ctx, hookID)
	if err != nil || !reflect.DeepEqual(w.Events, []string{"model.created", "job.finished"}) || !w.Wants("job.finished") || w.Wants("model.deleted") {
		t.Errorf("webhook after upgrade = %+v, %v", w, err)
	}
	if all, err := s.GetWebhook(ctx, allHookID); err != nil || all.Events != nil || !all.Wants("anything") {
		t.Errorf("every-event webhook after upgrade = %+v, %v", all, err)
	}

	u, err := s.GetUser(ctx, userID)
	if err != nil || u.Access.Role != Owner || u.Access.Role.String() != "owner" {
		t.Errorf("user after upgrade = %+v, %v", u, err)
	}

	// The upgraded database takes new rows, and a second start finds
	// nothing left to do.
	if err := s.JobProgress(ctx, jobID, 3, 0, 4, append(wantHistory, Epoch{Epoch: 3})); err != nil {
		t.Fatal(err)
	}
	s.Close()
	again, err := Open(ctx, url, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer again.Close()
	if n := again.Applied(); n != 0 {
		t.Errorf("second Open applied %d steps, want 0", n)
	}
	if j, err := again.GetJob(ctx, jobID); err != nil || len(j.History) != 3 {
		t.Errorf("job after progress = %+v, %v", j, err)
	}
}
