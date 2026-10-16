package store

import (
	"context"
	"fmt"
)

// UsageKey names one counter: a month, a person and the key they used.
type UsageKey struct {
	Month   string // "2026-10"
	UserID  string
	KeyID   string // "" for the web app
	KeyName string
}

// Usage is one counter with its owner's name.
type Usage struct {
	UsageKey
	UserName    string
	Email       string
	Predictions int64
}

// AddUsage adds counts in one transaction.
func (s *Store) AddUsage(ctx context.Context, counts map[UsageKey]int64) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for k, n := range counts {
		_, err := tx.ExecContext(ctx, `INSERT INTO usage (month, user_id, key_id, key_name, predictions) VALUES (?, ?, ?, ?, ?)
			ON CONFLICT (month, user_id, key_id) DO UPDATE SET predictions = predictions + excluded.predictions, key_name = excluded.key_name`,
			k.Month, k.UserID, k.KeyID, k.KeyName, n)
		if err != nil {
			return fmt.Errorf("store: add usage: %w", err)
		}
	}
	return tx.Commit()
}

// UserUsage returns a person's predictions in a month, keys included.
func (s *Store) UserUsage(ctx context.Context, month, userID string) (int64, error) {
	var n int64
	err := s.db.QueryRowContext(ctx, "SELECT COALESCE(SUM(predictions), 0) FROM usage WHERE month = ? AND user_id = ?", month, userID).Scan(&n)
	return n, err
}

// ListUsage returns a month's counters, busiest first; userID, when set,
// keeps only that person's, and team only that team's people.
func (s *Store) ListUsage(ctx context.Context, month, userID, team string) ([]Usage, error) {
	q := `SELECT u.month, u.user_id, u.key_id, u.key_name, u.predictions, p.name, p.email
		FROM usage u JOIN users p ON p.id = u.user_id WHERE u.month = ?`
	args := []any{month}
	if team != "" {
		q += " AND p.team_id = ?"
		args = append(args, team)
	}
	if userID != "" {
		q += " AND u.user_id = ?"
		args = append(args, userID)
	}
	rows, err := s.db.QueryContext(ctx, q+" ORDER BY u.predictions DESC, p.email LIMIT 1000", args...)
	if err != nil {
		return nil, fmt.Errorf("store: list usage: %w", err)
	}
	defer rows.Close()
	var out []Usage
	for rows.Next() {
		var u Usage
		if err := rows.Scan(&u.Month, &u.UserID, &u.KeyID, &u.KeyName, &u.Predictions, &u.UserName, &u.Email); err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

// ActiveJobs counts a person's training runs waiting or learning, whether
// they started them on the web or with one of their keys.
func (s *Store) ActiveJobs(ctx context.Context, email string) (int, error) {
	viaKey := " (" + email + ")" // how a key's runs name their owner
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM training_jobs
		WHERE (created_by = ? OR (length(created_by) > length(?) AND substr(created_by, -length(?)) = ?)) AND status IN (?, ?)`,
		email, viaKey, viaKey, viaKey, JobQueued, JobRunning).Scan(&n)
	return n, err
}
