// Package store keeps the server's records in PostgreSQL, through storm's
// generated packages under internal/store/db, and model weights, datasets
// and training checkpoints on disk under the data folder.
package store

import (
	"context"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/gsoultan/storm"
	"github.com/gsoultan/storm/migrate"
	"github.com/gsoultan/storm/runtime"
	"github.com/gsoultan/storm/runtime/pgxdrv"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gsoultan/mimir-server/internal/schema"
	"github.com/gsoultan/mimir-server/internal/store/db/model"
	"github.com/gsoultan/mimir-server/internal/store/db/team"
	"github.com/gsoultan/mimir-server/internal/store/tables"
)

// ErrNotFound reports that no model has the requested id.
var ErrNotFound = errors.New("store: model not found")

// migrations holds the reviewed migrations `storm diff` wrote from the
// model. The server applies the model itself with migrate.Auto; these are
// the record of what that does, and CI checks they carry the whole model.
//
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
	pool    *pgxpool.Pool
	ex      pgxdrv.Pool
	url     string
	weights string // directory holding <id>.mim files
	applied int    // migration steps Open applied
}

// Pool limits used unless the database URL sets its own pool_* options.
const (
	defaultMaxConns        = 16
	defaultMinConns        = 1
	defaultMaxConnLifetime = time.Hour
	defaultMaxConnIdleTime = 5 * time.Minute
	defaultHealthCheck     = 30 * time.Second
	defaultConnectTimeout  = 10 * time.Second
)

// Open connects to the PostgreSQL database at databaseURL, brings its
// schema up to the model with storm's automigrate, and keeps files in dir.
//
// Automigrate takes an advisory lock, so servers starting together migrate
// once; applies every step in one transaction; and refuses a plan that
// could lose data. The role needs CREATE on the database.
func Open(ctx context.Context, databaseURL, dir string) (*Store, error) {
	if strings.TrimSpace(databaseURL) == "" {
		return nil, errors.New("store: no database URL: pass -database-url or set MIMIR_DATABASE_URL, such as postgres://mimir:secret@localhost:5432/mimir")
	}
	weights := filepath.Join(dir, "models")
	if err := os.MkdirAll(weights, 0o750); err != nil {
		return nil, fmt.Errorf("store: create %s: %w", weights, err)
	}
	cfg, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		return nil, fmt.Errorf("store: database URL: %w", err)
	}
	poolDefaults(cfg, databaseURL)
	pool, err := pgxdrv.NewPoolConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("store: connect: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("store: connect to %s: %w", Redact(databaseURL), err)
	}
	s := &Store{pool: pool, ex: pgxdrv.Pool{P: pool}, url: databaseURL, weights: weights}
	if err := s.migrate(ctx); err != nil {
		pool.Close()
		return nil, err
	}
	return s, nil
}

// poolDefaults sets the pool's limits, leaving any the URL names alone.
func poolDefaults(cfg *pgxpool.Config, databaseURL string) {
	set := func(opt string) bool { return strings.Contains(databaseURL, opt) }
	if !set("pool_max_conns") {
		cfg.MaxConns = defaultMaxConns
	}
	if !set("pool_min_conns") {
		cfg.MinConns = defaultMinConns
	}
	if !set("pool_max_conn_lifetime") {
		cfg.MaxConnLifetime = defaultMaxConnLifetime
	}
	if !set("pool_max_conn_idle_time") {
		cfg.MaxConnIdleTime = defaultMaxConnIdleTime
	}
	if !set("pool_health_check_period") {
		cfg.HealthCheckPeriod = defaultHealthCheck
	}
	if !set("connect_timeout") {
		cfg.ConnConfig.ConnectTimeout = defaultConnectTimeout
	}
}

// migrate applies the model to the database and makes sure the first team
// exists.
func (s *Store) migrate(ctx context.Context) error {
	want, err := storm.Build(tables.All()...)
	if err != nil {
		return fmt.Errorf("store: model: %w", err)
	}
	res, err := migrate.AutoPool(ctx, s.pool, want, migrate.AutoOptions{})
	if err != nil {
		return fmt.Errorf("store: migrate: %w", err)
	}
	s.applied = len(res.Applied)
	// A new database starts with the team everything belongs to by
	// default. With no team at all nothing could be made, so an empty
	// table gets it back too.
	n, err := team.New().Count(ctx, s.ex)
	if err != nil {
		return fmt.Errorf("store: count teams: %w", err)
	}
	if n == 0 {
		t := team.Create()
		t.SetID(DefaultTeam)
		t.SetName("My team")
		t.SetCreateTime(now())
		if _, err := t.DoNothing().Insert(ctx, s.ex); err != nil && !errors.Is(err, runtime.ErrConflict) && !errors.Is(err, runtime.ErrNoRow) {
			return fmt.Errorf("store: first team: %w", err)
		}
	}
	return nil
}

// Applied reports how many schema steps Open applied; 0 when the database
// already matched the model.
func (s *Store) Applied() int { return s.applied }

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

// Close closes the database pool.
func (s *Store) Close() error {
	s.pool.Close()
	return nil
}

// Redact returns a database URL without its password, for logs and screens.
func Redact(databaseURL string) string {
	u, err := url.Parse(databaseURL)
	if err != nil || u.Scheme == "" {
		return "(database)"
	}
	if u.User != nil {
		if _, ok := u.User.Password(); ok {
			u.User = url.UserPassword(u.User.Username(), "xxxxx")
		}
	}
	u.RawQuery = ""
	return u.String()
}

func fromModel(r model.Row) Model {
	return Model{
		ID:           r.ID,
		Name:         r.Name,
		Description:  r.Description,
		Architecture: r.Architecture,
		SizeBytes:    r.SizeBytes,
		SHA256:       r.Sha256,
		Created:      r.CreateTime.UTC(),
		Updated:      r.UpdateTime.UTC(),
		InputShape:   fromJSON[[]int](r.InputShape),
		Labels:       fromJSON[[]string](r.Labels),
		Features:     fromJSON[[]string](r.Features),
		Accuracy:     r.Accuracy,
		JobID:        r.JobID,
		TeamID:       r.TeamID.V,
		WorkerID:     r.WorkerID,
		Columns:      fromJSON[[]schema.Column](r.Columns),
	}
}

// List returns a team's models, newest first.
func (s *Store) List(ctx context.Context, teamID string) ([]Model, error) {
	rows, err := model.New().
		Where(model.TeamID.Eq(teamID)).
		Order(model.CreateTime.Desc(), model.ID.Desc()).
		Limit(1000).
		All(ctx, s.ex, nil)
	if err != nil {
		return nil, fmt.Errorf("store: list: %w", err)
	}
	out := make([]Model, 0, len(rows))
	for _, r := range rows {
		out = append(out, fromModel(r))
	}
	return out, nil
}

// Get returns the model with id, or ErrNotFound.
func (s *Store) Get(ctx context.Context, id string) (Model, error) {
	r, ok, err := model.New().Where(model.ID.Eq(id)).One(ctx, s.ex)
	if err != nil {
		return Model{}, fmt.Errorf("store: get %s: %w", id, err)
	}
	if !ok {
		return Model{}, ErrNotFound
	}
	return fromModel(r), nil
}

// Create stores weights and a new model row. It fills in the model's ID,
// size, checksum and times and returns it.
func (s *Store) Create(ctx context.Context, m Model, weights []byte) (Model, error) {
	id, err := uuid.NewV7()
	if err != nil {
		return Model{}, fmt.Errorf("store: new id: %w", err)
	}
	sum := sha256.Sum256(weights)
	t := now()
	m.ID, m.SizeBytes, m.SHA256 = id.String(), int64(len(weights)), hex.EncodeToString(sum[:])
	m.Created, m.Updated = t, t
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
	n := model.Create()
	n.SetID(m.ID)
	n.SetName(m.Name)
	n.SetDescription(m.Description)
	n.SetArchitecture(m.Architecture)
	n.SetSizeBytes(m.SizeBytes)
	n.SetSha256(m.SHA256)
	n.SetCreateTime(t)
	n.SetUpdateTime(t)
	n.SetInputShape(toJSON(m.InputShape))
	n.SetLabels(toJSON(m.Labels))
	n.SetFeatures(toJSON(m.Features))
	n.SetJobID(m.JobID)
	n.SetAccuracy(m.Accuracy)
	n.SetTeamID(m.TeamID)
	n.SetWorkerID(m.WorkerID)
	n.SetColumns(toJSON(m.Columns))
	if _, err := n.Insert(ctx, s.ex); err != nil {
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
	u := model.MutateKey(id)
	u.SetSizeBytes(int64(len(weights)))
	u.SetSha256(hex.EncodeToString(sum[:]))
	u.SetUpdateTime(now())
	u.SetInputShape(toJSON(m.InputShape))
	u.SetLabels(toJSON(m.Labels))
	u.SetFeatures(toJSON(m.Features))
	u.SetJobID(m.JobID)
	u.SetAccuracy(m.Accuracy)
	u.SetColumns(toJSON(m.Columns))
	if err := u.Update(ctx, s.ex); err != nil {
		if errors.Is(err, runtime.ErrNoRow) {
			return ErrNotFound
		}
		return fmt.Errorf("store: replace model: %w", err)
	}
	return nil
}

// Update changes a model's name and description and returns the result.
func (s *Store) Update(ctx context.Context, id, name, description string) (Model, error) {
	u := model.MutateKey(id)
	u.SetName(name)
	u.SetDescription(description)
	u.SetUpdateTime(now())
	if err := u.Update(ctx, s.ex); err != nil {
		if errors.Is(err, runtime.ErrNoRow) {
			return Model{}, ErrNotFound
		}
		return Model{}, fmt.Errorf("store: update %s: %w", id, err)
	}
	return s.Get(ctx, id)
}

// SetModelWorker places a model on a worker, or "" for automatic.
func (s *Store) SetModelWorker(ctx context.Context, id, workerID string) error {
	u := model.MutateKey(id)
	u.SetWorkerID(workerID)
	if err := u.Update(ctx, s.ex); err != nil {
		if errors.Is(err, runtime.ErrNoRow) {
			return ErrNotFound
		}
		return fmt.Errorf("store: set model worker: %w", err)
	}
	return nil
}

// Delete removes a model's row and then its weights.
func (s *Store) Delete(ctx context.Context, id string) error {
	if err := model.Delete(ctx, s.ex, id); err != nil {
		if errors.Is(err, runtime.ErrNoRow) {
			return ErrNotFound
		}
		return fmt.Errorf("store: delete %s: %w", id, err)
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

// now is the current time as the database keeps it: UTC, to the
// millisecond, as every time was before the move to PostgreSQL.
func now() time.Time { return time.Now().UTC().Truncate(time.Millisecond) }

// ms rounds a time down to the millisecond, in UTC.
func ms(t time.Time) time.Time { return t.UTC().Truncate(time.Millisecond) }

// timeOf reads a nullable time; NULL is the zero time.
func timeOf(n runtime.Null[time.Time]) time.Time {
	if !n.Valid {
		return time.Time{}
	}
	return n.V.UTC()
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

// inTx runs fn in one transaction and commits it, or rolls back and
// returns fn's error. A panic in fn rolls back too.
func (s *Store) inTx(ctx context.Context, fn func(ex runtime.Executor) error) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("store: begin: %w", err)
	}
	defer tx.Rollback(context.WithoutCancel(ctx))
	if err := fn(pgxdrv.Tx{T: tx}); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// IsOwnDatabase reports whether a PostgreSQL connection string reaches the
// database this server keeps its own records in, which holds password
// hashes and sessions and so is never a source of examples. It compares
// host, port and database name; an unparsable string is not this one.
func (s *Store) IsOwnDatabase(dsn string) bool {
	theirs, err := pgconn.ParseConfig(dsn)
	if err != nil {
		return false
	}
	ours := s.pool.Config().ConnConfig
	return theirs.Database == ours.Database && theirs.Port == ours.Port && sameHost(theirs.Host, ours.Host)
}

// sameHost compares two hosts, treating the loopback names and a local
// socket directory as one machine.
func sameHost(a, b string) bool {
	local := func(h string) bool {
		return h == "localhost" || h == "127.0.0.1" || h == "::1" || strings.HasPrefix(h, "/")
	}
	return strings.EqualFold(a, b) || (local(a) && local(b))
}
