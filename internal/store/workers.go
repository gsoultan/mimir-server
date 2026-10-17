package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// Worker is a registered worker: a process that connects with its token
// and runs models for this server.
type Worker struct {
	ID        string
	Name      string
	CreatedBy string
	Created   time.Time
	LastSeen  time.Time // zero until it first connects
	// PublicKey is the Ed25519 key it joined with; nil until it joins.
	PublicKey []byte
	// LastAddress is where it last connected from.
	LastAddress string
}

const workerColumns = "id, name, created_by, create_time, last_seen, public_key, last_address"

func scanWorker(row scanner) (Worker, error) {
	var w Worker
	var created int64
	var seen sql.NullInt64
	err := row.Scan(&w.ID, &w.Name, &w.CreatedBy, &created, &seen, &w.PublicKey, &w.LastAddress)
	w.Created = time.UnixMilli(created).UTC()
	w.LastSeen = fromMillis(seen)
	return w, err
}

// CreateWorker registers a worker whose token hashes to tokenHash.
func (s *Store) CreateWorker(ctx context.Context, w Worker, tokenHash string) (Worker, error) {
	id, err := newID()
	if err != nil {
		return Worker{}, err
	}
	w.ID, w.Created = id, time.Now().UTC().Truncate(time.Millisecond)
	_, err = s.db.ExecContext(ctx, "INSERT INTO workers (id, name, token_hash, created_by, create_time) VALUES (?, ?, ?, ?, ?)",
		w.ID, w.Name, tokenHash, w.CreatedBy, millis(w.Created))
	if err != nil {
		return Worker{}, fmt.Errorf("store: insert worker: %w", err)
	}
	return w, nil
}

// ListWorkers returns every registered worker, oldest first.
func (s *Store) ListWorkers(ctx context.Context) ([]Worker, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT "+workerColumns+" FROM workers ORDER BY create_time, id LIMIT 1000")
	if err != nil {
		return nil, fmt.Errorf("store: list workers: %w", err)
	}
	defer rows.Close()
	var out []Worker
	for rows.Next() {
		w, err := scanWorker(rows)
		if err != nil {
			return nil, fmt.Errorf("store: list workers: %w", err)
		}
		out = append(out, w)
	}
	return out, rows.Err()
}

// WorkerByToken returns the worker whose token hashes to tokenHash and
// notes that it was seen.
func (s *Store) WorkerByToken(ctx context.Context, tokenHash string) (Worker, error) {
	w, err := scanWorker(s.db.QueryRowContext(ctx, "SELECT "+workerColumns+" FROM workers WHERE token_hash = ?", tokenHash))
	if errors.Is(err, sql.ErrNoRows) {
		return Worker{}, ErrNotFound
	}
	if err != nil {
		return Worker{}, fmt.Errorf("store: worker: %w", err)
	}
	s.TouchWorker(ctx, w.ID)
	return w, nil
}

// TouchWorker records that a worker is connected now. Best effort.
func (s *Store) TouchWorker(ctx context.Context, id string) {
	s.db.ExecContext(ctx, "UPDATE workers SET last_seen = ? WHERE id = ?", millis(time.Now()), id)
}

// DeleteWorker removes a worker; its token stops working.
// Models and waiting runs placed on it go back to automatic.
func (s *Store) DeleteWorker(ctx context.Context, id string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	res, err := tx.ExecContext(ctx, "DELETE FROM workers WHERE id = ?", id)
	if err != nil {
		return fmt.Errorf("store: delete worker: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	if _, err := tx.ExecContext(ctx, "UPDATE models SET worker_id = '' WHERE worker_id = ?", id); err != nil {
		return fmt.Errorf("store: delete worker: %w", err)
	}
	if _, err := tx.ExecContext(ctx, "UPDATE training_jobs SET run_on = '' WHERE run_on = ?", id); err != nil {
		return fmt.Errorf("store: delete worker: %w", err)
	}
	return tx.Commit()
}

// GetWorker returns one worker.
func (s *Store) GetWorker(ctx context.Context, id string) (Worker, error) {
	w, err := scanWorker(s.db.QueryRowContext(ctx, "SELECT "+workerColumns+" FROM workers WHERE id = ?", id))
	if errors.Is(err, sql.ErrNoRows) {
		return Worker{}, ErrNotFound
	}
	if err != nil {
		return Worker{}, fmt.Errorf("store: worker: %w", err)
	}
	return w, nil
}

// SetWorkerKey records the key a worker joined with. It only sets a key
// when none is recorded, so two links racing to join can't both win; it
// reports whether this call set it.
func (s *Store) SetWorkerKey(ctx context.Context, id string, key []byte) (bool, error) {
	res, err := s.db.ExecContext(ctx, "UPDATE workers SET public_key = ? WHERE id = ? AND public_key IS NULL", key, id)
	if err != nil {
		return false, fmt.Errorf("store: set worker key: %w", err)
	}
	n, _ := res.RowsAffected()
	return n == 1, nil
}

// SetWorkerAddress records where a worker connected from.
func (s *Store) SetWorkerAddress(ctx context.Context, id, addr string) error {
	_, err := s.db.ExecContext(ctx, "UPDATE workers SET last_address = ? WHERE id = ?", addr, id)
	if err != nil {
		return fmt.Errorf("store: set worker address: %w", err)
	}
	return nil
}

// ReplaceWorkerToken gives a worker a new token hash and forgets its key.
func (s *Store) ReplaceWorkerToken(ctx context.Context, id, tokenHash string) error {
	res, err := s.db.ExecContext(ctx, "UPDATE workers SET token_hash = ?, public_key = NULL WHERE id = ?", tokenHash, id)
	if err != nil {
		return fmt.Errorf("store: replace worker token: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// WorkerSample is one minute of a worker's stats.
type WorkerSample struct {
	Time                    time.Time
	CPUPercent              float64
	MemoryUsed, MemoryTotal int64
	DiskUsed, DiskTotal     int64
	Busy, Capacity          int
}

// AddWorkerSample stores a sample. A second one in the same millisecond
// replaces the first.
func (s *Store) AddWorkerSample(ctx context.Context, workerID string, x WorkerSample) error {
	_, err := s.db.ExecContext(ctx, "INSERT OR REPLACE INTO worker_samples (worker_id, time, cpu_percent, memory_used, memory_total, disk_used, disk_total, busy, capacity) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)",
		workerID, millis(x.Time), x.CPUPercent, x.MemoryUsed, x.MemoryTotal, x.DiskUsed, x.DiskTotal, x.Busy, x.Capacity)
	if err != nil {
		return fmt.Errorf("store: add worker sample: %w", err)
	}
	return nil
}

// WorkerSamples returns a worker's samples since a time, oldest first,
// averaged into at most points buckets so a week still charts quickly.
func (s *Store) WorkerSamples(ctx context.Context, workerID string, since time.Time, points int) ([]WorkerSample, error) {
	from := millis(since)
	width := max((time.Since(since).Milliseconds())/int64(max(points, 1)), 1)
	rows, err := s.db.QueryContext(ctx, `SELECT MAX(time), AVG(cpu_percent), CAST(AVG(memory_used) AS INTEGER), MAX(memory_total),
		CAST(AVG(disk_used) AS INTEGER), MAX(disk_total), CAST(ROUND(AVG(busy)) AS INTEGER), MAX(capacity)
		FROM worker_samples WHERE worker_id = ? AND time >= ? GROUP BY (time - ?) / ? ORDER BY 1`, workerID, from, from, width)
	if err != nil {
		return nil, fmt.Errorf("store: worker samples: %w", err)
	}
	defer rows.Close()
	var out []WorkerSample
	for rows.Next() {
		var x WorkerSample
		var t int64
		if err := rows.Scan(&t, &x.CPUPercent, &x.MemoryUsed, &x.MemoryTotal, &x.DiskUsed, &x.DiskTotal, &x.Busy, &x.Capacity); err != nil {
			return nil, fmt.Errorf("store: worker samples: %w", err)
		}
		x.Time = time.UnixMilli(t).UTC()
		out = append(out, x)
	}
	return out, rows.Err()
}

// Placement counts the models set to run on each worker and the runs
// waiting for each.
func (s *Store) Placement(ctx context.Context) (models, waiting map[string]int, err error) {
	models, waiting = map[string]int{}, map[string]int{}
	for _, q := range []struct {
		sql string
		out map[string]int
	}{
		{"SELECT worker_id, COUNT(*) FROM models WHERE worker_id <> '' GROUP BY worker_id", models},
		{fmt.Sprintf("SELECT run_on, COUNT(*) FROM training_jobs WHERE run_on <> '' AND status = %d GROUP BY run_on", JobQueued), waiting},
	} {
		rows, err := s.db.QueryContext(ctx, q.sql)
		if err != nil {
			return nil, nil, fmt.Errorf("store: placement: %w", err)
		}
		for rows.Next() {
			var id string
			var n int
			if err := rows.Scan(&id, &n); err != nil {
				rows.Close()
				return nil, nil, fmt.Errorf("store: placement: %w", err)
			}
			q.out[id] = n
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return nil, nil, fmt.Errorf("store: placement: %w", err)
		}
	}
	return models, waiting, nil
}

// Item is a name for a model or run, for a worker's dashboard.
type Item struct {
	ID, Name string
	Status   int
	Time     time.Time
}

// ModelsOnWorker returns the models, in any team, set to run on a worker.
func (s *Store) ModelsOnWorker(ctx context.Context, workerID string) ([]Item, error) {
	return s.items(ctx, "SELECT id, name, 0, create_time FROM models WHERE worker_id = ? ORDER BY name, id LIMIT 200", workerID)
}

// RunsOnWorker returns runs waiting for a worker, or run on it, newest
// first.
func (s *Store) RunsOnWorker(ctx context.Context, workerID, workerName string) ([]Item, error) {
	return s.items(ctx, `SELECT id, name, status, COALESCE(end_time, start_time, create_time) FROM training_jobs
		WHERE run_on = ? OR (worker = ? AND worker <> '') ORDER BY create_time DESC, id DESC LIMIT 20`, workerID, workerName)
}

func (s *Store) items(ctx context.Context, q string, args ...any) ([]Item, error) {
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("store: items: %w", err)
	}
	defer rows.Close()
	var out []Item
	for rows.Next() {
		var it Item
		var t int64
		if err := rows.Scan(&it.ID, &it.Name, &it.Status, &t); err != nil {
			return nil, fmt.Errorf("store: items: %w", err)
		}
		it.Time = time.UnixMilli(t).UTC()
		out = append(out, it)
	}
	return out, rows.Err()
}

// WorkerEvents returns a worker's recent audit events, newest first.
func (s *Store) WorkerEvents(ctx context.Context, name string, limit int) ([]AuditEvent, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT id, time, actor, action, detail FROM audit_events WHERE target = ? AND action LIKE 'worker.%' ORDER BY time DESC, id DESC LIMIT ?", name, limit)
	if err != nil {
		return nil, fmt.Errorf("store: worker events: %w", err)
	}
	defer rows.Close()
	var out []AuditEvent
	for rows.Next() {
		var e AuditEvent
		var t int64
		if err := rows.Scan(&e.ID, &t, &e.Actor, &e.Action, &e.Detail); err != nil {
			return nil, fmt.Errorf("store: worker events: %w", err)
		}
		e.Time, e.Target = time.UnixMilli(t).UTC(), name
		out = append(out, e)
	}
	return out, rows.Err()
}
