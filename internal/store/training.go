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
)

// DatasetKind says where a dataset's examples come from.
type DatasetKind int

const (
	DatasetTable DatasetKind = 1 // a CSV someone uploaded
	DatasetMNIST DatasetKind = 2 // the built-in handwritten digits
)

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
}

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
	CreatedBy     string
	Created       time.Time
	Started       time.Time // zero until it runs
	Ended         time.Time // zero until it's done
}

// ErrDatasetInUse means a dataset still has training jobs.
var ErrDatasetInUse = errors.New("store: dataset has training jobs")

// DatasetDir is where dataset files live.
func (s *Store) DatasetDir() string { return filepath.Join(filepath.Dir(s.weights), "datasets") }

// DatasetPath is where a table dataset's CSV lives.
func (s *Store) DatasetPath(id string) string { return filepath.Join(s.DatasetDir(), id+".csv") }

// ReadDataset returns a table dataset's CSV.
func (s *Store) ReadDataset(id string) ([]byte, error) {
	b, err := os.ReadFile(s.DatasetPath(id))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, ErrNotFound
	}
	return b, err
}

// JobDir is where a job keeps its checkpoint.
func (s *Store) JobDir(id string) string {
	return filepath.Join(filepath.Dir(s.weights), "training", id)
}

const datasetColumns = "id, name, kind, rows, features, labels, input_shape, target, size_bytes, create_time"

func scanDataset(row scanner) (Dataset, error) {
	var d Dataset
	var created int64
	var features, labels, shape string
	err := row.Scan(&d.ID, &d.Name, &d.Kind, &d.Rows, &features, &labels, &shape, &d.Target, &d.SizeBytes, &created)
	d.Created = time.UnixMilli(created).UTC()
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
	if csv != nil {
		if err := os.MkdirAll(s.DatasetDir(), 0o750); err != nil {
			return Dataset{}, err
		}
		if err := writeFileAtomic(s.DatasetPath(id), csv); err != nil {
			return Dataset{}, fmt.Errorf("store: write dataset: %w", err)
		}
		d.SizeBytes = int64(len(csv))
	}
	_, err = s.db.ExecContext(ctx, "INSERT INTO datasets ("+datasetColumns+") VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)",
		d.ID, d.Name, d.Kind, d.Rows, toJSON(d.Features), toJSON(d.Labels), toJSON(d.InputShape), d.Target, d.SizeBytes, millis(d.Created))
	if err != nil {
		os.Remove(s.DatasetPath(id))
		return Dataset{}, fmt.Errorf("store: insert dataset: %w", err)
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

// ListDatasets returns every dataset, newest first.
func (s *Store) ListDatasets(ctx context.Context) ([]Dataset, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT "+datasetColumns+" FROM datasets ORDER BY create_time DESC, id DESC LIMIT 1000")
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
	if err := os.Remove(s.DatasetPath(id)); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}

const jobColumns = "id, name, description, architecture, dataset_id, epochs, batch_size, learning_rate, hold_out, status, error, epoch, step, steps_per_epoch, history, model_id, created_by, create_time, start_time, end_time"

func scanJob(row scanner) (Job, error) {
	var j Job
	var created int64
	var started, ended sql.NullInt64
	var history string
	err := row.Scan(&j.ID, &j.Name, &j.Description, &j.Architecture, &j.DatasetID,
		&j.Settings.Epochs, &j.Settings.BatchSize, &j.Settings.LearningRate, &j.Settings.HoldOut,
		&j.Status, &j.Error, &j.Epoch, &j.Step, &j.StepsPerEpoch, &history, &j.ModelID, &j.CreatedBy, &created, &started, &ended)
	j.Created = time.UnixMilli(created).UTC()
	j.Started, j.Ended = fromMillis(started), fromMillis(ended)
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
	_, err = s.db.ExecContext(ctx, "INSERT INTO training_jobs ("+jobColumns+") VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, '', 0, 0, 0, '[]', '', ?, ?, NULL, NULL)",
		j.ID, j.Name, j.Description, j.Architecture, j.DatasetID,
		j.Settings.Epochs, j.Settings.BatchSize, j.Settings.LearningRate, j.Settings.HoldOut, j.Status, j.CreatedBy, millis(j.Created))
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

// ListJobs returns every job, newest first.
func (s *Store) ListJobs(ctx context.Context) ([]Job, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT "+jobColumns+" FROM training_jobs ORDER BY create_time DESC, id DESC LIMIT 1000")
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

// ClaimJob takes the oldest queued job and marks it running. It returns
// ErrNotFound when the queue is empty.
func (s *Store) ClaimJob(ctx context.Context) (Job, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Job{}, err
	}
	defer tx.Rollback()
	j, err := scanJob(tx.QueryRowContext(ctx, "SELECT "+jobColumns+" FROM training_jobs WHERE status = ? ORDER BY create_time, id LIMIT 1", JobQueued))
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
