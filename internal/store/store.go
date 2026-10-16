// Package store keeps model metadata in SQLite and model weights on disk.
package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"embed"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	_ "modernc.org/sqlite" // registers the "sqlite" driver, pure Go
)

// ErrNotFound reports that no model has the requested id.
var ErrNotFound = errors.New("store: model not found")

//go:embed migrations/*.sql
var migrations embed.FS

// Model is a stored model's metadata.
type Model struct {
	ID           string
	Name         string
	Description  string
	Architecture string
	SizeBytes    int64
	SHA256       string
	Created      time.Time
	Updated      time.Time
}

// Store is safe for concurrent use.
type Store struct {
	db      *sql.DB
	weights string // directory holding <id>.safetensors files
}

// Open opens or creates the store in dir and brings its schema up to date.
func Open(ctx context.Context, dir string) (*Store, error) {
	weights := filepath.Join(dir, "models")
	if err := os.MkdirAll(weights, 0o750); err != nil {
		return nil, fmt.Errorf("store: create %s: %w", weights, err)
	}
	dsn := "file:" + filepath.Join(dir, "mimir.db") +
		"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(on)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("store: open: %w", err)
	}
	s := &Store{db: db, weights: weights}
	if err := s.migrate(ctx); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

// Close closes the database.
func (s *Store) Close() error { return s.db.Close() }

// migrate applies every migration newer than the schema's user_version, in
// order, each in its own transaction.
func (s *Store) migrate(ctx context.Context) error {
	var version int
	if err := s.db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil {
		return fmt.Errorf("store: read schema version: %w", err)
	}
	names, err := fs.Glob(migrations, "migrations/*.sql")
	if err != nil {
		return err
	}
	slices.Sort(names)
	for _, name := range names {
		n, err := strconv.Atoi(strings.SplitN(filepath.Base(name), "_", 2)[0])
		if err != nil {
			return fmt.Errorf("store: migration %s: bad number: %w", name, err)
		}
		if n <= version {
			continue
		}
		body, err := migrations.ReadFile(name)
		if err != nil {
			return err
		}
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, string(body)); err != nil {
			tx.Rollback()
			return fmt.Errorf("store: migration %s: %w", name, err)
		}
		// PRAGMA takes no parameters; n is an integer parsed above.
		if _, err := tx.ExecContext(ctx, fmt.Sprintf("PRAGMA user_version = %d", n)); err != nil {
			tx.Rollback()
			return fmt.Errorf("store: migration %s: %w", name, err)
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("store: migration %s: %w", name, err)
		}
		version = n
	}
	return nil
}

const columns = "id, name, description, architecture, size_bytes, sha256, create_time, update_time"

type scanner interface{ Scan(dest ...any) error }

func scan(row scanner) (Model, error) {
	var m Model
	var created, updated int64
	err := row.Scan(&m.ID, &m.Name, &m.Description, &m.Architecture, &m.SizeBytes, &m.SHA256, &created, &updated)
	m.Created = time.UnixMilli(created).UTC()
	m.Updated = time.UnixMilli(updated).UTC()
	return m, err
}

// List returns every model, newest first.
func (s *Store) List(ctx context.Context) ([]Model, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT "+columns+" FROM models ORDER BY create_time DESC, id DESC LIMIT 1000")
	if err != nil {
		return nil, fmt.Errorf("store: list: %w", err)
	}
	defer rows.Close()
	var out []Model
	for rows.Next() {
		m, err := scan(rows)
		if err != nil {
			return nil, fmt.Errorf("store: list: %w", err)
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// Get returns the model with id, or ErrNotFound.
func (s *Store) Get(ctx context.Context, id string) (Model, error) {
	m, err := scan(s.db.QueryRowContext(ctx, "SELECT "+columns+" FROM models WHERE id = ?", id))
	if errors.Is(err, sql.ErrNoRows) {
		return Model{}, ErrNotFound
	}
	if err != nil {
		return Model{}, fmt.Errorf("store: get %s: %w", id, err)
	}
	return m, nil
}

// Create stores weights and a new model row. It fills in the model's ID,
// size, checksum and times and returns it.
func (s *Store) Create(ctx context.Context, m Model, weights []byte) (Model, error) {
	id, err := uuid.NewV7()
	if err != nil {
		return Model{}, fmt.Errorf("store: new id: %w", err)
	}
	sum := sha256.Sum256(weights)
	now := time.Now().UTC().Truncate(time.Millisecond)
	m.ID, m.SizeBytes, m.SHA256 = id.String(), int64(len(weights)), hex.EncodeToString(sum[:])
	m.Created, m.Updated = now, now

	// The file goes first and the row second, so a row never points at a
	// missing file. A crash between the two leaves an orphan file, which is
	// harmless.
	path := s.path(m.ID)
	if err := writeFileAtomic(path, weights); err != nil {
		return Model{}, fmt.Errorf("store: write weights: %w", err)
	}
	_, err = s.db.ExecContext(ctx, "INSERT INTO models ("+columns+") VALUES (?, ?, ?, ?, ?, ?, ?, ?)",
		m.ID, m.Name, m.Description, m.Architecture, m.SizeBytes, m.SHA256, now.UnixMilli(), now.UnixMilli())
	if err != nil {
		os.Remove(path)
		return Model{}, fmt.Errorf("store: insert model: %w", err)
	}
	return m, nil
}

// Update changes a model's name and description and returns the result.
func (s *Store) Update(ctx context.Context, id, name, description string) (Model, error) {
	res, err := s.db.ExecContext(ctx, "UPDATE models SET name = ?, description = ?, update_time = ? WHERE id = ?",
		name, description, time.Now().UTC().UnixMilli(), id)
	if err != nil {
		return Model{}, fmt.Errorf("store: update %s: %w", id, err)
	}
	if n, err := res.RowsAffected(); err == nil && n == 0 {
		return Model{}, ErrNotFound
	}
	return s.Get(ctx, id)
}

// Delete removes a model's row and then its weights.
func (s *Store) Delete(ctx context.Context, id string) error {
	res, err := s.db.ExecContext(ctx, "DELETE FROM models WHERE id = ?", id)
	if err != nil {
		return fmt.Errorf("store: delete %s: %w", id, err)
	}
	if n, err := res.RowsAffected(); err == nil && n == 0 {
		return ErrNotFound
	}
	if err := os.Remove(s.path(id)); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("store: delete weights %s: %w", id, err)
	}
	return nil
}

// Weights returns a model's weights file.
func (s *Store) Weights(id string) ([]byte, error) {
	b, err := os.ReadFile(s.path(id))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, ErrNotFound
	}
	return b, err
}

// path returns where a model's weights live. IDs are UUIDs made by Create,
// and callers' ids are checked by uuid.Parse first, so they never hold a
// path separator.
func (s *Store) path(id string) string {
	return filepath.Join(s.weights, id+".safetensors")
}

// ValidID reports whether id has the shape of a model id.
func ValidID(id string) bool {
	_, err := uuid.Parse(id)
	return err == nil && !strings.ContainsAny(id, `/\`)
}

func writeFileAtomic(path string, b []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".upload-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name()) // no-op after the rename
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}
