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
}

const workerColumns = "id, name, created_by, create_time, last_seen"

func scanWorker(row scanner) (Worker, error) {
	var w Worker
	var created int64
	var seen sql.NullInt64
	err := row.Scan(&w.ID, &w.Name, &w.CreatedBy, &created, &seen)
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
func (s *Store) DeleteWorker(ctx context.Context, id string) error {
	res, err := s.db.ExecContext(ctx, "DELETE FROM workers WHERE id = ?", id)
	if err != nil {
		return fmt.Errorf("store: delete worker: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}
