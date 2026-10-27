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
	"io"
	"io/fs"
	"net"
	"net/netip"
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
	"github.com/gsoultan/mimir-server/internal/secret"
	"github.com/gsoultan/mimir-server/internal/store/db/model"
	"github.com/gsoultan/mimir-server/internal/store/db/team"
	"github.com/gsoultan/mimir-server/internal/store/db/user"
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

// acceptReplaced lets automigrate replace indexes and CHECK constraints,
// which it otherwise refuses as destructive: neither holds data, and the
// model's reviewed migrations replace each one it drops. A dropped table
// or column is still refused.
func acceptReplaced(c migrate.Change) bool {
	return c.Object == migrate.ObjectIndex || c.Object == migrate.ObjectCheck
}

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
	// Language is how a language model answers by default.
	Language Language
	// OnDevice lets people download it and answer in their own browser.
	OnDevice bool
	// VersionID is the version answering now (see ListVersions).
	VersionID string
}

// Language is how a language model answers when a chat or an app doesn't
// say: its instructions (the system message), how freely it picks words
// and its longest answer in tokens.
type Language struct {
	Instructions string
	Temperature  float64
	MaxTokens    int
}

// DefaultLanguage is how a new model answers until someone changes it: no
// instructions, a little variety, and answers of up to a few paragraphs.
var DefaultLanguage = Language{Temperature: 0.7, MaxTokens: 256}

// WeightsVersion names the weights the model has now. Retraining a model
// in place gives it a new one, so a cache keyed by it loads the new
// weights on the next question.
func (m Model) WeightsVersion() string { return m.SHA256 }

// PinnedWorker reports the worker the model is set to run on. A model on
// none runs wherever there is room.
func (m Model) PinnedWorker() (id string, pinned bool) { return m.WorkerID, m.WorkerID != "" }

// Store is safe for concurrent use.
type Store struct {
	pool *pgxpool.Pool
	ex   pgxdrv.Pool
	url  string
	// logPool and logURL are the separate log database, nil and "" when
	// the audit log is in the main one; logEx runs audit queries either way.
	logPool *pgxpool.Pool
	logEx   pgxdrv.Pool
	logURL  string
	weights string // directory holding <id>.mim files
	applied int    // migration steps Open applied
	box     *secret.Box
	sealed  int // plaintext secrets Open sealed in place
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
//
// Stored secrets are sealed with the key WithSecretKey gives, or else the
// one in dir's key file (made on first start); Open seals any still in
// plaintext and refuses a key that does not open those already sealed.
func Open(ctx context.Context, databaseURL, dir string, opts ...Option) (*Store, error) {
	if strings.TrimSpace(databaseURL) == "" {
		return nil, errors.New("store: no database URL: pass -database-url or set MIMIR_DATABASE_URL, such as postgres://mimir:secret@localhost:5432/mimir")
	}
	var o options
	for _, opt := range opts {
		opt(&o)
	}
	if o.key == nil {
		k, _, err := secret.Load("", "", dir)
		if err != nil {
			return nil, fmt.Errorf("store: %w", err)
		}
		o.key = k
	}
	box, err := secret.New(o.key)
	if err != nil {
		return nil, fmt.Errorf("store: %w", err)
	}
	weights := filepath.Join(dir, "models")
	if err := os.MkdirAll(weights, 0o750); err != nil {
		return nil, fmt.Errorf("store: create %s: %w", weights, err)
	}
	if err := os.MkdirAll(filepath.Join(weights, "versions"), 0o750); err != nil {
		return nil, fmt.Errorf("store: create %s: %w", weights, err)
	}
	removeStale(weights, staleTemp)
	removeStale(filepath.Join(weights, "versions"), staleTemp)
	pool, err := connect(ctx, databaseURL)
	if err != nil {
		return nil, err
	}
	s := &Store{pool: pool, ex: pgxdrv.Pool{P: pool}, url: databaseURL, weights: weights, box: box}
	s.logEx = s.ex
	if err := s.migrate(ctx); err != nil {
		pool.Close()
		return nil, err
	}
	if s.sealed, err = s.sealLegacy(ctx); err != nil {
		pool.Close()
		return nil, err
	}
	if o.logURL != "" {
		if err := s.openLog(ctx, o.logURL); err != nil {
			pool.Close()
			return nil, err
		}
	}
	return s, nil
}

// connect opens a pool on databaseURL and checks the database answers.
func connect(ctx context.Context, databaseURL string) (*pgxpool.Pool, error) {
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
	return pool, nil
}

// openLog connects the separate log database and brings its schema up to
// the log model.
func (s *Store) openLog(ctx context.Context, databaseURL string) error {
	pool, err := connect(ctx, databaseURL)
	if err != nil {
		return fmt.Errorf("log database: %w", err)
	}
	want, err := storm.Build(tables.LogAll()...)
	if err != nil {
		pool.Close()
		return fmt.Errorf("store: log model: %w", err)
	}
	res, err := migrate.AutoPool(ctx, pool, want, migrate.AutoOptions{Accept: acceptReplaced})
	if err != nil {
		pool.Close()
		return fmt.Errorf("store: migrate log database: %w", err)
	}
	s.applied += len(res.Applied)
	s.logPool, s.logEx, s.logURL = pool, pgxdrv.Pool{P: pool}, databaseURL
	return nil
}

// CheckDatabase connects to the PostgreSQL database at databaseURL without
// changing it, for first-run setup. It returns the server's version and
// whether the database already holds mimir accounts.
func CheckDatabase(ctx context.Context, databaseURL string) (version string, hasAccounts bool, err error) {
	pool, err := connect(ctx, databaseURL)
	if err != nil {
		return "", false, err
	}
	defer pool.Close()
	conn, err := pool.Acquire(ctx)
	if err != nil {
		return "", false, fmt.Errorf("store: connect: %w", err)
	}
	// The server reports its version when a connection starts.
	version = "PostgreSQL " + conn.Conn().PgConn().ParameterStatus("server_version")
	conn.Release()
	n, err := user.New().Count(ctx, pgxdrv.Pool{P: pool})
	if err != nil {
		// A new database has no users table yet.
		return version, false, nil
	}
	return version, n > 0, nil
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
	res, err := migrate.AutoPool(ctx, s.pool, want, migrate.AutoOptions{Accept: acceptReplaced})
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

// Ping checks the databases answer, for readiness checks.
func (s *Store) Ping(ctx context.Context) error {
	if err := s.pool.Ping(ctx); err != nil {
		return err
	}
	if s.logPool != nil {
		if err := s.logPool.Ping(ctx); err != nil {
			return fmt.Errorf("log database: %w", err)
		}
	}
	return nil
}

// Close closes the database pools.
func (s *Store) Close() error {
	s.pool.Close()
	if s.logPool != nil {
		s.logPool.Close()
	}
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
		InputShape:   fromInt64s(r.InputShape),
		Labels:       fromStrs(r.Labels),
		Features:     fromStrs(r.Features),
		Accuracy:     r.Accuracy,
		JobID:        r.JobID,
		TeamID:       r.TeamID.V,
		WorkerID:     r.WorkerID,
		Columns:      fromDoc[schema.Column](r.Columns),
		Language:     Language{Instructions: r.Instructions, Temperature: r.Temperature, MaxTokens: int(r.MaxTokens)},
		OnDevice:     r.OnDevice,
		VersionID:    r.VersionID,
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
	sum := sha256.Sum256(weights)
	return s.create(ctx, m, int64(len(weights)), sum[:], func(path string) error { return writeFileAtomic(path, weights) })
}

// CreateFromFile stores a new model whose weights are the file at path,
// one of TempFile's, which it moves into place rather than copying. sum is
// the file's SHA-256 when the caller worked it out while writing the file;
// nil has it read once to work it out. The file is never held in memory.
func (s *Store) CreateFromFile(ctx context.Context, m Model, path string, sum []byte) (Model, error) {
	f, err := os.Open(path)
	if err != nil {
		return Model{}, fmt.Errorf("store: open weights: %w", err)
	}
	var n int64
	if sum == nil {
		h := sha256.New()
		n, err = io.Copy(h, f)
		sum = h.Sum(nil)
	} else {
		var info os.FileInfo
		if info, err = f.Stat(); err == nil {
			n = info.Size()
		}
	}
	if err == nil {
		// The bytes reach the disk before the row says they are there.
		err = f.Sync()
	}
	f.Close()
	if err != nil {
		return Model{}, fmt.Errorf("store: read weights: %w", err)
	}
	return s.create(ctx, m, n, sum, func(dst string) error { return os.Rename(path, dst) })
}

// TempFile creates an empty file beside the stored weights, where an
// upload or a conversion is written before CreateFromFile moves it into
// place. The caller removes it if it isn't used; ones left by a crash are
// removed when the store next opens.
func (s *Store) TempFile() (*os.File, error) {
	return os.CreateTemp(s.weights, tempPrefix+"*")
}

// staleTemp is how old a file being written beside the weights must be
// for Open to take it for one a stopped server left. Uploads wait at most
// an hour to be used.
const staleTemp = 6 * time.Hour

// tempPrefix starts the names of files being written beside the weights.
const tempPrefix = ".upload-"

// removeStale removes files TempFile and writeFileAtomic left behind by a
// server that stopped while writing them. Only old ones go, so a file
// another server sharing the directory is writing stays.
func removeStale(dir string, age time.Duration) {
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if !strings.HasPrefix(e.Name(), tempPrefix) {
			continue
		}
		if info, err := e.Info(); err == nil && time.Since(info.ModTime()) > age {
			os.Remove(filepath.Join(dir, e.Name()))
		}
	}
}

// create adds the row of a new model of size bytes with checksum sum,
// after place has put its weights at the path given.
func (s *Store) create(ctx context.Context, m Model, size int64, sum []byte, place func(path string) error) (Model, error) {
	id, err := uuid.NewV7()
	if err != nil {
		return Model{}, fmt.Errorf("store: new id: %w", err)
	}
	t := now()
	m.ID, m.SizeBytes, m.SHA256 = id.String(), size, hex.EncodeToString(sum)
	m.Created, m.Updated = t, t
	if m.TeamID == "" {
		m.TeamID = DefaultTeam
	}
	if m.Language == (Language{}) {
		m.Language = DefaultLanguage
	}

	// The file goes first and the row second, so a row never points at a
	// missing file. A crash between the two leaves an orphan file, which is
	// harmless.
	path := s.path(m.ID)
	if err := place(path); err != nil {
		return Model{}, fmt.Errorf("store: write weights: %w", err)
	}
	// Its first version shares the file's bytes on disk.
	if m.VersionID, err = newID(); err != nil {
		os.Remove(path)
		return Model{}, err
	}
	if err := linkOrCopy(path, s.versionPath(m.VersionID)); err != nil {
		os.Remove(path)
		return Model{}, fmt.Errorf("store: keep version: %w", err)
	}
	v := versionOf(m)
	v.ID, v.Number, v.Created = m.VersionID, 1, t
	n := model.Create()
	n.SetID(m.ID)
	n.SetName(m.Name)
	n.SetDescription(m.Description)
	n.SetArchitecture(m.Architecture)
	n.SetSizeBytes(m.SizeBytes)
	n.SetSha256(m.SHA256)
	n.SetCreateTime(t)
	n.SetUpdateTime(t)
	n.SetInputShape(int64s(m.InputShape))
	n.SetLabels(strs(m.Labels))
	n.SetFeatures(strs(m.Features))
	n.SetJobID(m.JobID)
	n.SetAccuracy(m.Accuracy)
	n.SetTeamID(m.TeamID)
	n.SetWorkerID(m.WorkerID)
	n.SetColumns(doc(m.Columns))
	n.SetInstructions(m.Language.Instructions)
	n.SetTemperature(m.Language.Temperature)
	n.SetMaxTokens(int32(m.Language.MaxTokens))
	n.SetVersionID(m.VersionID)
	err = storm.InTx(ctx, s.ex, func(ex storm.Executor) error {
		if _, err := n.Insert(ctx, ex); err != nil {
			return fmt.Errorf("store: insert model: %w", err)
		}
		return insertVersion(ctx, ex, v)
	})
	if err != nil {
		os.Remove(path)
		os.Remove(s.versionPath(m.VersionID))
		return Model{}, err
	}
	return m, nil
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

// SetLanguage changes how a language model answers by default.
func (s *Store) SetLanguage(ctx context.Context, id string, l Language) error {
	u := model.MutateKey(id)
	u.SetInstructions(l.Instructions)
	u.SetTemperature(l.Temperature)
	u.SetMaxTokens(int32(l.MaxTokens))
	if err := u.Update(ctx, s.ex); err != nil {
		if errors.Is(err, runtime.ErrNoRow) {
			return ErrNotFound
		}
		return fmt.Errorf("store: set language %s: %w", id, err)
	}
	return nil
}

// SetOnDevice lets people run a model in their browser, or stops them.
func (s *Store) SetOnDevice(ctx context.Context, id string, on bool) error {
	u := model.MutateKey(id)
	u.SetOnDevice(on)
	if err := u.Update(ctx, s.ex); err != nil {
		if errors.Is(err, runtime.ErrNoRow) {
			return ErrNotFound
		}
		return fmt.Errorf("store: set on device %s: %w", id, err)
	}
	return nil
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

// Delete removes a model's row and then its weights, every version's.
func (s *Store) Delete(ctx context.Context, id string) error {
	versions, err := s.versionIDs(ctx, id)
	if err != nil {
		return err
	}
	if err := model.Delete(ctx, s.ex, id); err != nil {
		if errors.Is(err, runtime.ErrNoRow) {
			return ErrNotFound
		}
		return fmt.Errorf("store: delete %s: %w", id, err)
	}
	s.removeVersions(versions)
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

// The array columns are NOT NULL, so a nil list is written as an empty
// one; an empty one reads back as nil, as uploaded models always have.

func strs(v []string) []string {
	if v == nil {
		return []string{}
	}
	return v
}

func fromStrs(v []string) []string {
	if len(v) == 0 {
		return nil
	}
	return v
}

func int64s(v []int) []int64 {
	out := make([]int64, len(v))
	for i, n := range v {
		out[i] = int64(n)
	}
	return out
}

func fromInt64s(v []int64) []int {
	if len(v) == 0 {
		return nil
	}
	out := make([]int, len(v))
	for i, n := range v {
		out[i] = int(n)
	}
	return out
}

// doc and fromDoc keep a list of structs (a dataset's columns, a run's
// history) in a jsonb column. Empty is [], and reads back as nil.
func doc[T any](v []T) runtime.JSON {
	if len(v) == 0 {
		return runtime.JSON("[]")
	}
	b, err := json.Marshal(v)
	if err != nil { // only unsupported values fail, and these have none
		panic(fmt.Sprintf("store: encode %T: %v", v, err))
	}
	return b
}

func fromDoc[T any](j runtime.JSON) []T {
	var v []T
	if err := j.Unmarshal(&v); err != nil || len(v) == 0 {
		return nil
	}
	return v
}

func writeFileAtomic(path string, b []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), tempPrefix+"*")
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

// ErrOwnDatabase refuses a connection to the server's own database.
var ErrOwnDatabase = errors.New("that is this server's own database; point at another database")

// RefuseOwnDatabase is a dbsource.Guard: it refuses a connection that
// would open the database this server keeps its own records in. It judges
// the address actually dialled, so a fallback host, a resolved name or
// another spelling of this machine (127.0.0.2, a socket, the machine's own
// address) reaches the same answer as the name in our own URL. Another
// database on the same server is fine.
func (s *Store) RefuseOwnDatabase(network, address, database string) error {
	ours := s.pool.Config().ConnConfig
	if database != ours.Database {
		return nil
	}
	ends := []*pgconn.FallbackConfig{{Host: ours.Host, Port: ours.Port}}
	ends = append(ends, ours.Fallbacks...)
	ip, port, socket := dialled(network, address)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for _, end := range ends {
		if end.Port != port {
			continue
		}
		local := strings.HasPrefix(end.Host, "/")
		var addrs []netip.Addr
		if !local {
			if a, err := netip.ParseAddr(end.Host); err == nil {
				addrs = []netip.Addr{a}
			} else {
				addrs, _ = net.DefaultResolver.LookupNetIP(ctx, "ip", end.Host)
			}
			for _, a := range addrs {
				a = a.Unmap()
				if a == ip {
					return ErrOwnDatabase
				}
				local = local || onThisMachine(a)
			}
		}
		// Our database is on this machine: so is anything reached through
		// a socket or at one of this machine's own addresses.
		if local && (socket || (ip.IsValid() && onThisMachine(ip))) {
			return ErrOwnDatabase
		}
	}
	return nil
}

// dialled reads the address a PostgreSQL connection dials: an IP and port,
// or a socket file, whose name ends in the port.
func dialled(network, address string) (ip netip.Addr, port uint16, socket bool) {
	if strings.HasPrefix(network, "unix") {
		_, p, _ := strings.Cut(filepath.Base(address), ".s.PGSQL.")
		n, _ := strconv.ParseUint(p, 10, 16)
		return netip.Addr{}, uint16(n), true
	}
	ap, err := netip.ParseAddrPort(address)
	if err != nil {
		return netip.Addr{}, 0, false
	}
	return ap.Addr().Unmap().WithZone(""), ap.Port(), false
}

// onThisMachine reports whether ip reaches this machine: loopback, the
// unspecified address, or one of its interfaces' addresses.
func onThisMachine(ip netip.Addr) bool {
	if ip.IsLoopback() || ip.IsUnspecified() {
		return true
	}
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return false
	}
	for _, a := range addrs {
		if n, ok := a.(*net.IPNet); ok {
			if b, ok := netip.AddrFromSlice(n.IP); ok && b.Unmap() == ip {
				return true
			}
		}
	}
	return false
}
