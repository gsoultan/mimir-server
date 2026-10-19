package store

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"github.com/gsoultan/storm"
	"github.com/gsoultan/storm/runtime"

	"github.com/gsoultan/mimir-server/internal/schema"
	"github.com/gsoultan/mimir-server/internal/store/db/dataset"
	"github.com/gsoultan/mimir-server/internal/store/db/trainingjob"
	"github.com/gsoultan/mimir-server/internal/store/tables"
)

// DatasetKind says where a dataset's examples come from. It is the
// column's own type, declared with the schema.
type DatasetKind = tables.DatasetKind

const (
	DatasetTable      = tables.DatasetTable      // a CSV someone uploaded
	DatasetMNIST      = tables.DatasetMNIST      // the built-in handwritten digits
	DatasetPhotos     = tables.DatasetPhotos     // a ZIP of photos, one folder per answer
	DatasetPhotoTable = tables.DatasetPhotoTable // a ZIP of a data.csv and the photos it names
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
	// Driver, DSN and Query are set when the rows come from a database.
	// For S3-compatible storage, Driver is "s3", DSN the bucket connection
	// and Query the path in the bucket. DSN is a secret and never leaves
	// the server.
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

// FromQuery reports whether the dataset's rows come from a database or
// from storage, so they can be fetched again.
func (d Dataset) FromQuery() bool { return d.Driver != "" }

// JobStatus is where a training job is in its life. It is the column's
// own type, declared with the schema.
type JobStatus = tables.JobStatus

const (
	JobQueued    = tables.JobQueued
	JobRunning   = tables.JobRunning
	JobSucceeded = tables.JobSucceeded
	JobFailed    = tables.JobFailed
	JobCancelled = tables.JobCancelled
)

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

func (s *Store) fromDataset(r dataset.Row) (Dataset, error) {
	dsn, err := s.openSecret(r.Dsn, datasetContext(r.ID))
	if err != nil {
		return Dataset{}, err
	}
	return Dataset{
		ID:           r.ID,
		Name:         r.Name,
		Kind:         DatasetKind(r.Kind),
		Rows:         r.Rows,
		Features:     fromStrs(r.Features),
		Labels:       fromStrs(r.Labels),
		InputShape:   fromInt64s(r.InputShape),
		Target:       r.Target,
		SizeBytes:    r.SizeBytes,
		Created:      r.CreateTime.UTC(),
		Driver:       r.Driver,
		DSN:          dsn,
		Query:        r.SourceQuery,
		Refreshed:    timeOf(r.RefreshTime),
		TeamID:       r.TeamID.V,
		Columns:      fromDoc[schema.Column](r.Columns),
		NumberTarget: r.NumberTarget,
		Skipped:      r.Skipped,
	}, nil
}

// CreateDataset stores a dataset row and, for a table, its CSV.
func (s *Store) CreateDataset(ctx context.Context, d Dataset, csv []byte) (Dataset, error) {
	id, err := newID()
	if err != nil {
		return Dataset{}, err
	}
	d.ID, d.Created = id, now()
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
	n := dataset.Create()
	if d.FromQuery() {
		d.Refreshed = d.Created
		n.SetRefreshTime(d.Refreshed)
	}
	n.SetID(d.ID)
	n.SetName(d.Name)
	n.SetKind(int32(d.Kind))
	n.SetRows(d.Rows)
	n.SetFeatures(strs(d.Features))
	n.SetLabels(strs(d.Labels))
	n.SetInputShape(int64s(d.InputShape))
	n.SetTarget(d.Target)
	n.SetSizeBytes(d.SizeBytes)
	n.SetCreateTime(d.Created)
	n.SetDriver(d.Driver)
	dsn, err := s.box.Seal(d.DSN, datasetContext(d.ID))
	if err != nil {
		return Dataset{}, err
	}
	n.SetDsn(dsn)
	n.SetSourceQuery(d.Query)
	n.SetTeamID(d.TeamID)
	n.SetColumns(doc(d.Columns))
	n.SetNumberTarget(d.NumberTarget)
	n.SetSkipped(d.Skipped)
	if _, err := n.Insert(ctx, s.ex); err != nil {
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
	d.SizeBytes, d.Refreshed = int64(len(csv)), now()
	m := dataset.MutateKey(d.ID)
	m.SetRows(d.Rows)
	m.SetFeatures(strs(d.Features))
	m.SetLabels(strs(d.Labels))
	m.SetInputShape(int64s(d.InputShape))
	m.SetSizeBytes(d.SizeBytes)
	m.SetRefreshTime(d.Refreshed)
	m.SetColumns(doc(d.Columns))
	m.SetNumberTarget(d.NumberTarget)
	m.SetSkipped(d.Skipped)
	if err := m.Update(ctx, s.ex); err != nil && !errors.Is(err, runtime.ErrNoRow) {
		return Dataset{}, fmt.Errorf("store: refresh dataset: %w", err)
	}
	return d, nil
}

// UpdateDatasetColumns changes how a dataset's columns are read: its
// answer column, column kinds and what follows from them.
func (s *Store) UpdateDatasetColumns(ctx context.Context, d Dataset) (Dataset, error) {
	m := dataset.MutateKey(d.ID)
	m.SetTarget(d.Target)
	m.SetRows(d.Rows)
	m.SetFeatures(strs(d.Features))
	m.SetLabels(strs(d.Labels))
	m.SetInputShape(int64s(d.InputShape))
	m.SetColumns(doc(d.Columns))
	m.SetNumberTarget(d.NumberTarget)
	m.SetSkipped(d.Skipped)
	if err := m.Update(ctx, s.ex); err != nil && !errors.Is(err, runtime.ErrNoRow) {
		return Dataset{}, fmt.Errorf("store: update dataset columns: %w", err)
	}
	return d, nil
}

// GetDataset returns one dataset.
func (s *Store) GetDataset(ctx context.Context, id string) (Dataset, error) {
	r, ok, err := dataset.New().Where(dataset.ID.Eq(id)).One(ctx, s.ex)
	if err != nil {
		return Dataset{}, err
	}
	if !ok {
		return Dataset{}, ErrNotFound
	}
	return s.fromDataset(r)
}

// ListDatasets returns a team's datasets, newest first.
func (s *Store) ListDatasets(ctx context.Context, teamID string) ([]Dataset, error) {
	rows, err := dataset.New().
		Where(dataset.TeamID.Eq(teamID)).
		Order(dataset.CreateTime.Desc(), dataset.ID.Desc()).
		Limit(1000).
		All(ctx, s.ex, nil)
	if err != nil {
		return nil, fmt.Errorf("store: list datasets: %w", err)
	}
	out := make([]Dataset, 0, len(rows))
	for _, r := range rows {
		d, err := s.fromDataset(r)
		if err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, nil
}

// DeleteDataset removes a dataset and its file. It refuses while jobs
// still point at it.
func (s *Store) DeleteDataset(ctx context.Context, id string) error {
	used, err := trainingjob.New().Where(trainingjob.DatasetID.Eq(id)).Exists(ctx, s.ex)
	if err != nil {
		return err
	}
	if used {
		return ErrDatasetInUse
	}
	if err := dataset.Delete(ctx, s.ex, id); err != nil {
		switch {
		case errors.Is(err, runtime.ErrNoRow):
			return ErrNotFound
		case errors.Is(err, runtime.ErrForeignKeyViolation):
			return ErrDatasetInUse // a job arrived since the check
		}
		return fmt.Errorf("store: delete dataset: %w", err)
	}
	for _, p := range []string{s.DatasetPath(id), s.archivePath(id)} {
		if err := os.Remove(p); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
	}
	return nil
}

func fromJob(r trainingjob.Row) Job {
	return Job{
		ID:           r.ID,
		Name:         r.Name,
		Description:  r.Description,
		Architecture: r.Architecture,
		DatasetID:    r.DatasetID,
		Settings: Settings{
			Epochs:       int(r.Epochs),
			BatchSize:    int(r.BatchSize),
			LearningRate: r.LearningRate,
			HoldOut:      r.HoldOut,
		},
		Status:        JobStatus(r.Status),
		Error:         r.Error,
		Epoch:         int(r.Epoch),
		Step:          int(r.Step),
		StepsPerEpoch: int(r.StepsPerEpoch),
		History:       fromDoc[Epoch](r.History),
		ModelID:       r.ModelID,
		Worker:        r.Worker,
		RepeatSeconds: r.RepeatSeconds,
		NextRun:       timeOf(r.NextRun),
		ParentID:      r.ParentID,
		BaseModelID:   r.BaseModelID,
		Note:          r.Note,
		CreatedBy:     r.CreatedBy,
		Created:       r.CreateTime.UTC(),
		Started:       timeOf(r.StartTime),
		Ended:         timeOf(r.EndTime),
		TeamID:        r.TeamID.V,
		RunOn:         r.RunOn,
	}
}

// CreateJob queues a job.
func (s *Store) CreateJob(ctx context.Context, j Job) (Job, error) {
	id, err := newID()
	if err != nil {
		return Job{}, err
	}
	j.ID, j.Status, j.Created = id, JobQueued, now()
	if j.TeamID == "" {
		j.TeamID = DefaultTeam
	}
	n := trainingjob.Create()
	if j.RepeatSeconds > 0 {
		j.NextRun = j.Created.Add(time.Duration(j.RepeatSeconds) * time.Second)
		n.SetNextRun(j.NextRun)
	}
	n.SetID(j.ID)
	n.SetName(j.Name)
	n.SetDescription(j.Description)
	n.SetArchitecture(j.Architecture)
	n.SetDatasetID(j.DatasetID)
	n.SetEpochs(int64(j.Settings.Epochs))
	n.SetBatchSize(int64(j.Settings.BatchSize))
	n.SetLearningRate(j.Settings.LearningRate)
	n.SetHoldOut(j.Settings.HoldOut)
	n.SetStatus(int32(j.Status))
	n.SetCreatedBy(j.CreatedBy)
	n.SetCreateTime(j.Created)
	n.SetRepeatSeconds(j.RepeatSeconds)
	n.SetParentID(j.ParentID)
	n.SetBaseModelID(j.BaseModelID)
	n.SetTeamID(j.TeamID)
	n.SetRunOn(j.RunOn)
	if _, err := n.Insert(ctx, s.ex); err != nil {
		return Job{}, fmt.Errorf("store: insert job: %w", err)
	}
	return j, nil
}

// GetJob returns one job.
func (s *Store) GetJob(ctx context.Context, id string) (Job, error) {
	r, ok, err := trainingjob.New().Where(trainingjob.ID.Eq(id)).One(ctx, s.ex)
	if err != nil {
		return Job{}, err
	}
	if !ok {
		return Job{}, ErrNotFound
	}
	return fromJob(r), nil
}

// ListJobs returns a team's jobs, newest first.
func (s *Store) ListJobs(ctx context.Context, teamID string) ([]Job, error) {
	rows, err := trainingjob.New().
		Where(trainingjob.TeamID.Eq(teamID)).
		Order(trainingjob.CreateTime.Desc(), trainingjob.ID.Desc()).
		Limit(1000).
		All(ctx, s.ex, nil)
	if err != nil {
		return nil, fmt.Errorf("store: list jobs: %w", err)
	}
	return jobs(rows), nil
}

func jobs(rows []trainingjob.Row) []Job {
	out := make([]Job, 0, len(rows))
	for _, r := range rows {
		out = append(out, fromJob(r))
	}
	return out
}

// ClaimJob takes the oldest queued job not in skip and marks it running.
// It returns ErrNotFound when there is none.
func (s *Store) ClaimJob(ctx context.Context, skip ...string) (Job, error) {
	var j Job
	err := storm.InTx(ctx, s.ex, func(ex storm.Executor) error {
		// SKIP LOCKED: two claimers take two different jobs, and neither
		// waits for the other.
		r, ok, err := trainingjob.New().
			Where(trainingjob.Status.Eq(int32(JobQueued))).
			WhereIf(len(skip) > 0, trainingjob.ID.NotIn(skip...)).
			Order(trainingjob.CreateTime.Asc(), trainingjob.ID.Asc()).
			ForUpdateSkipLocked().
			One(ctx, ex)
		if err != nil {
			return err
		}
		if !ok {
			return ErrNotFound
		}
		t := now()
		m := trainingjob.Mutate(r)
		m.SetStatus(int32(JobRunning))
		m.SetStartTime(t)
		m.SetEndTimeNull()
		m.SetError("")
		if err := m.Update(ctx, ex); err != nil {
			return err
		}
		j = fromJob(r)
		j.Status, j.Started, j.Ended, j.Error = JobRunning, t, time.Time{}, ""
		return nil
	})
	if err != nil {
		return Job{}, err
	}
	return j, nil
}

// updateJob applies m to a job. A job that is gone is not an error: the
// runner writes progress for jobs a person may have just deleted.
func (s *Store) updateJob(ctx context.Context, m trainingjob.Mut) error {
	if err := m.Update(ctx, s.ex); err != nil && !errors.Is(err, runtime.ErrNoRow) {
		return err
	}
	return nil
}

// SetJobStatus moves a job to a final state, or back to the queue.
func (s *Store) SetJobStatus(ctx context.Context, id string, status JobStatus, errText, modelID string) error {
	m := trainingjob.MutateKey(id)
	m.SetStatus(int32(status))
	m.SetError(errText)
	if modelID != "" {
		m.SetModelID(modelID)
	}
	if status.Done() {
		m.SetEndTime(now())
	} else {
		m.SetEndTimeNull()
	}
	return s.updateJob(ctx, m)
}

// SetJobNote records what a pass did with its result.
func (s *Store) SetJobNote(ctx context.Context, id, note string) error {
	m := trainingjob.MutateKey(id)
	m.SetNote(note)
	return s.updateJob(ctx, m)
}

// SetRepeat changes how often a job keeps learning; 0 stops it. The next
// pass is one interval from now.
func (s *Store) SetRepeat(ctx context.Context, id string, seconds int64) error {
	m := trainingjob.MutateKey(id)
	m.SetRepeatSeconds(seconds)
	if seconds > 0 {
		m.SetNextRun(now().Add(time.Duration(seconds) * time.Second))
	} else {
		m.SetNextRunNull()
	}
	return s.updateJob(ctx, m)
}

// DueJobs returns finished jobs whose next keep-learning pass is due.
func (s *Store) DueJobs(ctx context.Context, at time.Time) ([]Job, error) {
	rows, err := trainingjob.New().
		Where(
			trainingjob.RepeatSeconds.Gt(0),
			trainingjob.NextRun.IsNotNull(),
			trainingjob.NextRun.Lte(at),
			trainingjob.Status.Gte(int32(JobSucceeded)),
		).
		Order(trainingjob.NextRun.Asc(), trainingjob.ID.Asc()).
		Limit(1000).
		All(ctx, s.ex, nil)
	if err != nil {
		return nil, fmt.Errorf("store: due jobs: %w", err)
	}
	return jobs(rows), nil
}

// HandOver moves a job's schedule to its next pass: the old job stops
// repeating and the new one is queued with the interval. It returns the
// new job.
func (s *Store) HandOver(ctx context.Context, old Job, next Job) (Job, error) {
	j, err := s.CreateJob(ctx, next)
	if err != nil {
		return Job{}, err
	}
	m := trainingjob.MutateKey(old.ID)
	m.SetRepeatSeconds(0)
	m.SetNextRunNull()
	return j, s.updateJob(ctx, m)
}

// SetJobWorker records who is running a job.
func (s *Store) SetJobWorker(ctx context.Context, id, workerName string) error {
	m := trainingjob.MutateKey(id)
	m.SetWorker(workerName)
	return s.updateJob(ctx, m)
}

// RequeueJob puts a cancelled or failed job back in the queue.
func (s *Store) RequeueJob(ctx context.Context, id string) (bool, error) {
	requeued := false
	err := storm.InTx(ctx, s.ex, func(ex storm.Executor) error {
		r, ok, err := trainingjob.New().
			Where(trainingjob.ID.Eq(id), trainingjob.Status.In(int32(JobFailed), int32(JobCancelled))).
			ForUpdate().
			One(ctx, ex)
		if err != nil || !ok {
			return err
		}
		m := trainingjob.Mutate(r)
		m.SetStatus(int32(JobQueued))
		m.SetError("")
		m.SetEndTimeNull()
		if err := m.Update(ctx, ex); err != nil {
			return err
		}
		requeued = true
		return nil
	})
	return requeued && err == nil, err
}

// JobProgress records where a running job is.
func (s *Store) JobProgress(ctx context.Context, id string, epoch, step, stepsPerEpoch int, history []Epoch) error {
	m := trainingjob.MutateKey(id)
	m.SetEpoch(int64(epoch))
	m.SetStep(int64(step))
	m.SetStepsPerEpoch(int64(stepsPerEpoch))
	m.SetHistory(doc(history))
	return s.updateJob(ctx, m)
}

// JobStatusOf returns just a job's status, for a runner polling for cancel.
func (s *Store) JobStatusOf(ctx context.Context, id string) (JobStatus, error) {
	r, ok, err := trainingjob.New().Where(trainingjob.ID.Eq(id)).One(ctx, s.ex)
	if err != nil {
		return 0, err
	}
	if !ok {
		return 0, ErrNotFound
	}
	return JobStatus(r.Status), nil
}

// DeleteJob removes a job and its checkpoint. A running job can't go.
func (s *Store) DeleteJob(ctx context.Context, id string) error {
	err := storm.InTx(ctx, s.ex, func(ex storm.Executor) error {
		// Locked, so the runner can't claim it between the check and the
		// delete.
		_, ok, err := trainingjob.New().
			Where(trainingjob.ID.Eq(id), trainingjob.Status.NotEq(int32(JobRunning))).
			ForUpdate().
			One(ctx, ex)
		if err != nil {
			return fmt.Errorf("store: delete job: %w", err)
		}
		if !ok {
			return ErrNotFound
		}
		if err := trainingjob.Delete(ctx, ex, id); err != nil {
			if errors.Is(err, runtime.ErrNoRow) {
				return ErrNotFound
			}
			return fmt.Errorf("store: delete job: %w", err)
		}
		return nil
	})
	if err != nil {
		return err
	}
	return os.RemoveAll(s.JobDir(id))
}

// ResetRunningJobs puts jobs a previous process left running back in the
// queue, for a server that restarted mid-training.
func (s *Store) ResetRunningJobs(ctx context.Context) error {
	var m trainingjob.Mut
	m.SetStatus(int32(JobQueued))
	_, err := trainingjob.New().Where(trainingjob.Status.Eq(int32(JobRunning))).UpdateAll(ctx, s.ex, m)
	return err
}
