// Package store keeps model metadata in SQLite and model weights on disk.
package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"embed"
	"encoding/hex"
	"encoding/json"
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

	"github.com/gsoultan/mimir-server/internal/schema"
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
	// InputShape, Labels and Features are set for models taught from a
	// dataset; uploaded models of a fixed architecture leave them empty.
	InputShape []int
	Labels     []string
	Features   []string
	// Accuracy is the share of held-out examples a taught model got right,
	// 0 when unknown.
	Accuracy float64
	JobID    string
	// TeamID is the team the model belongs to.
	TeamID string
	// WorkerID is the worker that answers its questions; "" for automatic.
	WorkerID string
	// Columns are the table columns it reads and how each becomes
	// numbers; empty when it reads numbers as they are.
	Columns []schema.Column
}

// Store is safe for concurrent use.
type Store struct {
	db      *sql.DB
	dbPath  string
	weights string // directory holding <id>.mim files
}

// Open opens or creates the store in dir and brings its schema up to date.
func Open(ctx context.Context, dir string) (*Store, error) {
	weights := filepath.Join(dir, "models")
	if err := os.MkdirAll(weights, 0o750); err != nil {
		return nil, fmt.Errorf("store: create %s: %w", weights, err)
	}
	dbPath := filepath.Join(dir, "mimir.db")
	dsn := "file:" + dbPath +
		"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(on)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("store: open: %w", err)
	}
	s := &Store{db: db, dbPath: dbPath, weights: weights}
	if err := s.migrate(ctx); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

// LatestSchemaVersion is the newest migration shipped with this build.
func LatestSchemaVersion() int {
	names, _ := fs.Glob(migrations, "migrations/*.sql")
	latest := 0
	for _, name := range names {
		if n, err := strconv.Atoi(strings.SplitN(filepath.Base(name), "_", 2)[0]); err == nil && n > latest {
			latest = n
		}
	}
	return latest
}

// Close closes the database.
func (s *Store) Close() error { return s.db.Close() }

// rebuildMarker starts a migration that rebuilds a table other tables
// point at. It runs with foreign keys off, as SQLite's guide to changing a
// table asks, and the keys are checked before it commits.
const rebuildMarker = "-- rebuild: foreign keys off"

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
		if err := s.apply(ctx, n, string(body)); err != nil {
			return fmt.Errorf("store: migration %s: %w", name, err)
		}
		version = n
	}
	return nil
}

// apply runs migration n in a transaction on one connection.
func (s *Store) apply(ctx context.Context, n int, body string) error {
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	rebuild := strings.HasPrefix(body, rebuildMarker)
	if rebuild {
		if _, err := conn.ExecContext(ctx, "PRAGMA foreign_keys = OFF"); err != nil {
			return err
		}
		defer conn.ExecContext(context.WithoutCancel(ctx), "PRAGMA foreign_keys = ON")
	}
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, body); err != nil {
		return err
	}
	if rebuild {
		rows, err := tx.QueryContext(ctx, "PRAGMA foreign_key_check")
		if err != nil {
			return err
		}
		broken := rows.Next()
		rows.Close()
		if broken {
			return errors.New("a foreign key no longer holds")
		}
	}
	// PRAGMA takes no parameters; n is an integer parsed from the name.
	if _, err := tx.ExecContext(ctx, fmt.Sprintf("PRAGMA user_version = %d", n)); err != nil {
		return err
	}
	return tx.Commit()
}

const columns = "id, name, description, architecture, size_bytes, sha256, create_time, update_time, input_shape, labels, features, job_id, accuracy, team_id, worker_id, columns"

type scanner interface{ Scan(dest ...any) error }

func scan(row scanner) (Model, error) {
	var m Model
	var created, updated int64
	var shape, labels, features, cols string
	var team sql.NullString
	err := row.Scan(&m.ID, &m.Name, &m.Description, &m.Architecture, &m.SizeBytes, &m.SHA256, &created, &updated, &shape, &labels, &features, &m.JobID, &m.Accuracy, &team, &m.WorkerID, &cols)
	m.Columns = fromJSON[[]schema.Column](cols)
	m.TeamID = team.String
	m.Created = time.UnixMilli(created).UTC()
	m.Updated = time.UnixMilli(updated).UTC()
	m.InputShape = fromJSON[[]int](shape)
	m.Labels = fromJSON[[]string](labels)
	m.Features = fromJSON[[]string](features)
	return m, err
}

// List returns a team's models, newest first.
func (s *Store) List(ctx context.Context, team string) ([]Model, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT "+columns+" FROM models WHERE team_id = ? ORDER BY create_time DESC, id DESC LIMIT 1000", team)
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
	if m.TeamID == "" {
		m.TeamID = DefaultTeam
	}

	// The file goes first and the row second, so a row never points at a
	// missing file. A crash between the two leaves an orphan file, which is
	// harmless.
	path := s.path(m.ID)
	if err := writeFileAtomic(path, weights); err != nil {
		return Model{}, fmt.Errorf("store: write weights: %w", err)
	}
	_, err = s.db.ExecContext(ctx, "INSERT INTO models ("+columns+") VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)",
		m.ID, m.Name, m.Description, m.Architecture, m.SizeBytes, m.SHA256, now.UnixMilli(), now.UnixMilli(),
		toJSON(m.InputShape), toJSON(m.Labels), toJSON(m.Features), m.JobID, m.Accuracy, m.TeamID, m.WorkerID, toJSON(m.Columns))
	if err != nil {
		os.Remove(path)
		return Model{}, fmt.Errorf("store: insert model: %w", err)
	}
	return m, nil
}

// Replace swaps a model's weights, shape, labels, columns and score for
// those of a newer pass, keeping its id so apps keep working.
func (s *Store) Replace(ctx context.Context, id string, m Model, weights []byte) error {
	sum := sha256.Sum256(weights)
	path := s.path(id)
	if err := writeFileAtomic(path, weights); err != nil {
		return fmt.Errorf("store: write weights: %w", err)
	}
	res, err := s.db.ExecContext(ctx, "UPDATE models SET size_bytes = ?, sha256 = ?, update_time = ?, input_shape = ?, labels = ?, features = ?, job_id = ?, accuracy = ?, columns = ? WHERE id = ?",
		len(weights), hex.EncodeToString(sum[:]), time.Now().UTC().UnixMilli(), toJSON(m.InputShape), toJSON(m.Labels), toJSON(m.Features), m.JobID, m.Accuracy, toJSON(m.Columns), id)
	if err != nil {
		return fmt.Errorf("store: replace model: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
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

// SetModelWorker places a model on a worker, or "" for automatic.
func (s *Store) SetModelWorker(ctx context.Context, id, workerID string) error {
	res, err := s.db.ExecContext(ctx, "UPDATE models SET worker_id = ? WHERE id = ?", workerID, id)
	if err != nil {
		return fmt.Errorf("store: set model worker: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
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
	return filepath.Join(s.weights, id+".mim")
}

// ValidID reports whether id has the shape of a model id.
func ValidID(id string) bool {
	_, err := uuid.Parse(id)
	return err == nil && !strings.ContainsAny(id, `/\`)
}

// toJSON and fromJSON keep small lists in text columns. An empty list is
// stored as "" so uploaded models read back with nil slices.
func toJSON[T any](v T) string {
	b, err := json.Marshal(v)
	if err != nil || string(b) == "null" || string(b) == "[]" {
		return ""
	}
	return string(b)
}

func fromJSON[T any](s string) T {
	var v T
	if s != "" {
		json.Unmarshal([]byte(s), &v)
	}
	return v
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
