package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"github.com/gsoultan/mimir-server/internal/schema"
)

// DatasetKind says where a dataset's examples come from.
type DatasetKind int

const (
	DatasetTable DatasetKind = 1 // a CSV someone uploaded
	DatasetMNIST DatasetKind = 2 // the built-in handwritten digits
	// DatasetPhotos is a ZIP of photos, one folder per answer.
	DatasetPhotos DatasetKind = 3
	// DatasetPhotoTable is a ZIP of a data.csv and the photos it names.
	DatasetPhotoTable DatasetKind = 4
)

// Archive reports whether the dataset's file is a ZIP.
func (k DatasetKind) Archive() bool { return k == DatasetPhotos || k == DatasetPhotoTable }

// Dataset is a set of examples models learn from.
type Dataset struct {
	ID         string
	Name       string
	Kind       DatasetKind
	Rows       int64
	Features   []string
	Labels     []string
	InputShape []int
	Target     string
	SizeBytes  int64
	Created    time.Time
	// Driver, DSN and Query are set when the rows come from a database.
	// DSN is a secret and never leaves the server.
	Driver    string
	DSN       string
	Query     string
	Refreshed time.Time // when the query last ran; zero for uploads
	TeamID    string
	// Columns are the input columns and their kinds, as first found;
	// empty when every column is a number.
	Columns []schema.Column
	// NumberTarget says every answer is a number.
	NumberTarget bool
	// Skipped counts rows left out: no answer, or a missing photo.
	Skipped int64
}

// FromQuery reports whether the dataset's rows come from a database.
func (d Dataset) FromQuery() bool { return d.Query != "" }

// JobStatus is where a training job is in its life.
type JobStatus int

const (
	JobQueued    JobStatus = 1
	JobRunning   JobStatus = 2
	JobSucceeded JobStatus = 3
	JobFailed    JobStatus = 4
	JobCancelled JobStatus = 5
)

// Done reports whether the job is over, whichever way.
func (s JobStatus) Done() bool { return s >= JobSucceeded }

// Settings are a job's training knobs.
type Settings struct {
	Epochs       int
	BatchSize    int
	LearningRate float64
	HoldOut      float64
}

// Epoch is how one pass over the examples went.
type Epoch struct {
	Epoch    int     `json:"epoch"`
	Loss     float64 `json:"loss"`
	Accuracy float64 `json:"accuracy"`
	ValLoss  float64 `json:"val_loss"`
	Millis   int64   `json:"millis"`
	// MAE is how far a number model's answers are off on average.
	MAE float64 `json:"mae,omitempty"`
}

// Job is one training run.
type Job struct {
	ID            string
	Name          string
	Description   string
	Architecture  string
	DatasetID     string
	Settings      Settings
	Status        JobStatus
	Error         string
	Epoch         int
	Step          int
	StepsPerEpoch int
	History       []Epoch
	ModelID       string
	Worker        string // the worker that ran it, or "" for this server
	// RepeatSeconds > 0 means the job keeps learning: when NextRun comes,
	// a new pass fetches the examples again and continues from the model.
	RepeatSeconds int64
	NextRun       time.Time
	ParentID      string // the first job of the chain, for later passes
	BaseModelID   string // the model a pass continued from
	Note          string // what a pass did with its result
	CreatedBy     string
	Created       time.Time
	Started       time.Time // zero until it runs
	Ended         time.Time // zero until it's done
	TeamID        string
	RunOn         string // the worker asked to train it; "" for any
}

// ErrDatasetInUse means a dataset still has training jobs.
var ErrDatasetInUse = errors.New("store: dataset has training jobs")

// Dir is the server's data folder.
func (s *Store) Dir() string { return filepath.Dir(s.weights) }

// DatasetDir is where dataset files live.
func (s *Store) DatasetDir() string { return filepath.Join(filepath.Dir(s.weights), "datasets") }

// DatasetPath is where a table dataset's CSV lives.
func (s *Store) DatasetPath(id string) string { return filepath.Join(s.DatasetDir(), id+".csv") }

// archivePath is where a photo dataset's ZIP lives.
func (s *Store) archivePath(id string) string { return filepath.Join(s.DatasetDir(), id+".zip") }

func (s *Store) datasetFile(d Dataset) string {
	if d.Kind.Archive() {
		return s.archivePath(d.ID)
	}
	return s.DatasetPath(d.ID)
}

// ReadDataset returns a dataset's file: a table's CSV or a photo ZIP.
func (s *Store) ReadDataset(id string) ([]byte, error) {
	b, err := os.ReadFile(s.DatasetPath(id))
	if errors.Is(err, fs.ErrNotExist) {
		b, err = os.ReadFile(s.archivePath(id))
	}
	if errors.Is(err, fs.ErrNotExist) {
		return nil, ErrNotFound
	}
	return b, err
}

// JobDir is where a job keeps its checkpoint.
func (s *Store) JobDir(id string) string {
	return filepath.Join(filepath.Dir(s.weights), "training", id)
}

const datasetColumns = "id, name, kind, rows, features, labels, input_shape, target, size_bytes, create_time, driver, dsn, query, refresh_time, team_id, columns, number_target, skipped"

func scanDataset(row scanner) (Dataset, error) {
	var d Dataset
	var created int64
	var features, labels, shape, cols string
	var refreshed sql.NullInt64
	var team sql.NullString
	err := row.Scan(&d.ID, &d.Name, &d.Kind, &d.Rows, &features, &labels, &shape, &d.Target, &d.SizeBytes, &created, &d.Driver, &d.DSN, &d.Query, &refreshed, &team, &cols, &d.NumberTarget, &d.Skipped)
	d.Columns = fromJSON[[]schema.Column](cols)
	d.TeamID = team.String
	d.Created = time.UnixMilli(created).UTC()
	d.Refreshed = fromMillis(refreshed)
	d.Features = fromJSON[[]string](features)
	d.Labels = fromJSON[[]string](labels)
	d.InputShape = fromJSON[[]int](shape)
	return d, err
}

// CreateDataset stores a dataset row and, for a table, its CSV.
func (s *Store) CreateDataset(ctx context.Context, d Dataset, csv []byte) (Dataset, error) {
	id, err := newID()
	if err != nil {
		return Dataset{}, err
	}
	d.ID, d.Created = id, time.Now().UTC().Truncate(time.Millisecond)
	if d.TeamID == "" {
		d.TeamID = DefaultTeam
	}
	if csv != nil {
		if err := os.MkdirAll(s.DatasetDir(), 0o750); err != nil {
			return Dataset{}, err
		}
		if err := writeFileAtomic(s.datasetFile(d), csv); err != nil {
			return Dataset{}, fmt.Errorf("store: write dataset: %w", err)
		}
		d.SizeBytes = int64(len(csv))
	}
	var refreshed any
	if d.FromQuery() {
		d.Refreshed = d.Created
		refreshed = millis(d.Created)
	}
	_, err = s.db.ExecContext(ctx, "INSERT INTO datasets ("+datasetColumns+") VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)",
		d.ID, d.Name, d.Kind, d.Rows, toJSON(d.Features), toJSON(d.Labels), toJSON(d.InputShape), d.Target, d.SizeBytes, millis(d.Created), d.Driver, d.DSN, d.Query, refreshed, d.TeamID,
		toJSON(d.Columns), d.NumberTarget, d.Skipped)
	if err != nil {
		os.Remove(s.datasetFile(d))
		return Dataset{}, fmt.Errorf("store: insert dataset: %w", err)
	}
	return d, nil
}

// RefreshDataset replaces a query dataset's rows with a fresh fetch.
func (s *Store) RefreshDataset(ctx context.Context, d Dataset, csv []byte) (Dataset, error) {
	if err := writeFileAtomic(s.DatasetPath(d.ID), csv); err != nil {
		return Dataset{}, fmt.Errorf("store: write dataset: %w", err)
	}
	d.SizeBytes, d.Refreshed = int64(len(csv)), time.Now().UTC().Truncate(time.Millisecond)
	_, err := s.db.ExecContext(ctx, "UPDATE datasets SET rows = ?, features = ?, labels = ?, input_shape = ?, size_bytes = ?, refresh_time = ?, columns = ?, number_target = ?, skipped = ? WHERE id = ?",
		d.Rows, toJSON(d.Features), toJSON(d.Labels), toJSON(d.InputShape), d.SizeBytes, millis(d.Refreshed), toJSON(d.Columns), d.NumberTarget, d.Skipped, d.ID)
	if err != nil {
		return Dataset{}, fmt.Errorf("store: refresh dataset: %w", err)
	}
	return d, nil
}

// UpdateDatasetColumns changes how a dataset's columns are read: its
// answer column, column kinds and what follows from them.
func (s *Store) UpdateDatasetColumns(ctx context.Context, d Dataset) (Dataset, error) {
	_, err := s.db.ExecContext(ctx, "UPDATE datasets SET target = ?, rows = ?, features = ?, labels = ?, input_shape = ?, columns = ?, number_target = ?, skipped = ? WHERE id = ?",
		d.Target, d.Rows, toJSON(d.Features), toJSON(d.Labels), toJSON(d.InputShape), toJSON(d.Columns), d.NumberTarget, d.Skipped, d.ID)
	if err != nil {
		return Dataset{}, fmt.Errorf("store: update dataset columns: %w", err)
	}
	return d, nil
}

// GetDataset returns one dataset.
func (s *Store) GetDataset(ctx context.Context, id string) (Dataset, error) {
	d, err := scanDataset(s.db.QueryRowContext(ctx, "SELECT "+datasetColumns+" FROM datasets WHERE id = ?", id))
	if errors.Is(err, sql.ErrNoRows) {
		return Dataset{}, ErrNotFound
	}
	return d, err
}

// ListDatasets returns a team's datasets, newest first.
func (s *Store) ListDatasets(ctx context.Context, team string) ([]Dataset, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT "+datasetColumns+" FROM datasets WHERE team_id = ? ORDER BY create_time DESC, id DESC LIMIT 1000", team)
	if err != nil {
		return nil, fmt.Errorf("store: list datasets: %w", err)
	}
	defer rows.Close()
	var out []Dataset
	for rows.Next() {
		d, err := scanDataset(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// DeleteDataset removes a dataset and its file. It refuses while jobs
// still point at it.
func (s *Store) DeleteDataset(ctx context.Context, id string) error {
	var n int
	if err := s.db.QueryRowContext(ctx, "SELECT count(*) FROM training_jobs WHERE dataset_id = ?", id).Scan(&n); err != nil {
		return err
	}
	if n > 0 {
		return ErrDatasetInUse
	}
	res, err := s.db.ExecContext(ctx, "DELETE FROM datasets WHERE id = ?", id)
	if err != nil {
		return fmt.Errorf("store: delete dataset: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	for _, p := range []string{s.DatasetPath(id), s.archivePath(id)} {
		if err := os.Remove(p); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
	}
	return nil
}

const jobColumns = "id, name, description, architecture, dataset_id, epochs, batch_size, learning_rate, hold_out, status, error, epoch, step, steps_per_epoch, history, model_id, worker, created_by, create_time, start_time, end_time, repeat_seconds, next_run, parent_id, base_model_id, note, team_id, run_on"

func scanJob(row scanner) (Job, error) {
	var j Job
	var created int64
	var started, ended, next sql.NullInt64
	var history string
	var team sql.NullString
	err := row.Scan(&j.ID, &j.Name, &j.Description, &j.Architecture, &j.DatasetID,
		&j.Settings.Epochs, &j.Settings.BatchSize, &j.Settings.LearningRate, &j.Settings.HoldOut,
		&j.Status, &j.Error, &j.Epoch, &j.Step, &j.StepsPerEpoch, &history, &j.ModelID, &j.Worker, &j.CreatedBy, &created, &started, &ended,
		&j.RepeatSeconds, &next, &j.ParentID, &j.BaseModelID, &j.Note, &team, &j.RunOn)
	j.TeamID = team.String
	j.Created = time.UnixMilli(created).UTC()
	j.Started, j.Ended, j.NextRun = fromMillis(started), fromMillis(ended), fromMillis(next)
	j.History = fromJSON[[]Epoch](history)
	return j, err
}

// CreateJob queues a job.
func (s *Store) CreateJob(ctx context.Context, j Job) (Job, error) {
	id, err := newID()
	if err != nil {
		return Job{}, err
	}
	j.ID, j.Status, j.Created = id, JobQueued, time.Now().UTC().Truncate(time.Millisecond)
	if j.TeamID == "" {
		j.TeamID = DefaultTeam
	}
	var next any
	if j.RepeatSeconds > 0 {
		j.NextRun = j.Created.Add(time.Duration(j.RepeatSeconds) * time.Second)
		next = millis(j.NextRun)
	}
	_, err = s.db.ExecContext(ctx, "INSERT INTO training_jobs ("+jobColumns+") VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, '', 0, 0, 0, '[]', '', '', ?, ?, NULL, NULL, ?, ?, ?, ?, '', ?, ?)",
		j.ID, j.Name, j.Description, j.Architecture, j.DatasetID,
		j.Settings.Epochs, j.Settings.BatchSize, j.Settings.LearningRate, j.Settings.HoldOut, j.Status, j.CreatedBy, millis(j.Created),
		j.RepeatSeconds, next, j.ParentID, j.BaseModelID, j.TeamID, j.RunOn)
	if err != nil {
		return Job{}, fmt.Errorf("store: insert job: %w", err)
	}
	return j, nil
}

// GetJob returns one job.
func (s *Store) GetJob(ctx context.Context, id string) (Job, error) {
	j, err := scanJob(s.db.QueryRowContext(ctx, "SELECT "+jobColumns+" FROM training_jobs WHERE id = ?", id))
	if errors.Is(err, sql.ErrNoRows) {
		return Job{}, ErrNotFound
	}
	return j, err
}

// ListJobs returns a team's jobs, newest first.
func (s *Store) ListJobs(ctx context.Context, team string) ([]Job, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT "+jobColumns+" FROM training_jobs WHERE team_id = ? ORDER BY create_time DESC, id DESC LIMIT 1000", team)
	if err != nil {
		return nil, fmt.Errorf("store: list jobs: %w", err)
	}
	defer rows.Close()
	var out []Job
	for rows.Next() {
		j, err := scanJob(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, j)
	}
	return out, rows.Err()
}

// ClaimJob takes the oldest queued job not in skip and marks it running.
// It returns ErrNotFound when there is none.
func (s *Store) ClaimJob(ctx context.Context, skip ...string) (Job, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Job{}, err
	}
	defer tx.Rollback()
	// skip holds ids the runner set aside this round, as one JSON array.
	j, err := scanJob(tx.QueryRowContext(ctx, "SELECT "+jobColumns+" FROM training_jobs WHERE status = ? AND id NOT IN (SELECT value FROM json_each(?)) ORDER BY create_time, id LIMIT 1", JobQueued, skipList(skip)))
	if errors.Is(err, sql.ErrNoRows) {
		return Job{}, ErrNotFound
	}
	if err != nil {
		return Job{}, err
	}
	now := time.Now()
	if _, err := tx.ExecContext(ctx, "UPDATE training_jobs SET status = ?, start_time = ?, end_time = NULL, error = '' WHERE id = ?", JobRunning, millis(now), j.ID); err != nil {
		return Job{}, err
	}
	j.Status, j.Started, j.Ended, j.Error = JobRunning, now.UTC(), time.Time{}, ""
	return j, tx.Commit()
}

// SetJobStatus moves a job to a final state, or back to the queue.
func (s *Store) SetJobStatus(ctx context.Context, id string, status JobStatus, errText, modelID string) error {
	var end any
	if status.Done() {
		end = millis(time.Now())
	}
	_, err := s.db.ExecContext(ctx, "UPDATE training_jobs SET status = ?, error = ?, model_id = CASE WHEN ? <> '' THEN ? ELSE model_id END, end_time = ? WHERE id = ?",
		status, errText, modelID, modelID, end, id)
	return err
}

// SetJobNote records what a pass did with its result.
func (s *Store) SetJobNote(ctx context.Context, id, note string) error {
	_, err := s.db.ExecContext(ctx, "UPDATE training_jobs SET note = ? WHERE id = ?", note, id)
	return err
}

// SetRepeat changes how often a job keeps learning; 0 stops it. The next
// pass is one interval from now.
func (s *Store) SetRepeat(ctx context.Context, id string, seconds int64) error {
	var next any
	if seconds > 0 {
		next = millis(time.Now().Add(time.Duration(seconds) * time.Second))
	}
	_, err := s.db.ExecContext(ctx, "UPDATE training_jobs SET repeat_seconds = ?, next_run = ? WHERE id = ?", seconds, next, id)
	return err
}

// DueJobs returns finished jobs whose next keep-learning pass is due.
func (s *Store) DueJobs(ctx context.Context, now time.Time) ([]Job, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT "+jobColumns+" FROM training_jobs WHERE repeat_seconds > 0 AND next_run IS NOT NULL AND next_run <= ? AND status >= ? ORDER BY next_run", millis(now), JobSucceeded)
	if err != nil {
		return nil, fmt.Errorf("store: due jobs: %w", err)
	}
	defer rows.Close()
	var out []Job
	for rows.Next() {
		j, err := scanJob(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, j)
	}
	return out, rows.Err()
}

// HandOver moves a job's schedule to its next pass: the old job stops
// repeating and the new one is queued with the interval. It returns the
// new job.
func (s *Store) HandOver(ctx context.Context, old Job, next Job) (Job, error) {
	j, err := s.CreateJob(ctx, next)
	if err != nil {
		return Job{}, err
	}
	_, err = s.db.ExecContext(ctx, "UPDATE training_jobs SET repeat_seconds = 0, next_run = NULL WHERE id = ?", old.ID)
	return j, err
}

// SetJobWorker records who is running a job.
func (s *Store) SetJobWorker(ctx context.Context, id, worker string) error {
	_, err := s.db.ExecContext(ctx, "UPDATE training_jobs SET worker = ? WHERE id = ?", worker, id)
	return err
}

// RequeueJob puts a cancelled or failed job back in the queue.
func (s *Store) RequeueJob(ctx context.Context, id string) (bool, error) {
	res, err := s.db.ExecContext(ctx, "UPDATE training_jobs SET status = ?, error = '', end_time = NULL WHERE id = ? AND status IN (?, ?)",
		JobQueued, id, JobFailed, JobCancelled)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// JobProgress records where a running job is.
func (s *Store) JobProgress(ctx context.Context, id string, epoch, step, stepsPerEpoch int, history []Epoch) error {
	h := toJSON(history)
	if h == "" {
		h = "[]"
	}
	_, err := s.db.ExecContext(ctx, "UPDATE training_jobs SET epoch = ?, step = ?, steps_per_epoch = ?, history = ? WHERE id = ?",
		epoch, step, stepsPerEpoch, h, id)
	return err
}

// JobStatusOf returns just a job's status, for a runner polling for cancel.
func (s *Store) JobStatusOf(ctx context.Context, id string) (JobStatus, error) {
	var st JobStatus
	err := s.db.QueryRowContext(ctx, "SELECT status FROM training_jobs WHERE id = ?", id).Scan(&st)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, ErrNotFound
	}
	return st, err
}

// DeleteJob removes a job and its checkpoint. A running job can't go.
func (s *Store) DeleteJob(ctx context.Context, id string) error {
	res, err := s.db.ExecContext(ctx, "DELETE FROM training_jobs WHERE id = ? AND status <> ?", id, JobRunning)
	if err != nil {
		return fmt.Errorf("store: delete job: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return os.RemoveAll(s.JobDir(id))
}

// ResetRunningJobs puts jobs a previous process left running back in the
// queue, for a server that restarted mid-training.
func (s *Store) ResetRunningJobs(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, "UPDATE training_jobs SET status = ? WHERE status = ?", JobQueued, JobRunning)
	return err
}

// skipList is skip as a JSON array, "[]" when empty.
func skipList(skip []string) string {
	if len(skip) == 0 {
		return "[]"
	}
	return toJSON(skip)
}
