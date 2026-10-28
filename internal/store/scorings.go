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

	"github.com/gsoultan/mimir-server/internal/store/db/scoring"
)

// Scoring answers every row of a table with one model, once or on a
// schedule. Its rows come from an upload (InputPath), a database query
// or storage (Driver, DSN and Query, as on a Dataset) or a table dataset
// (DatasetID). DSN is a secret and never leaves the server.
type Scoring struct {
	ID        string
	Name      string
	ModelID   string
	DatasetID string
	Driver    string
	DSN       string
	Query     string
	Status    JobStatus
	Error     string
	// Rows is how many rows the last run read; Done how many it has
	// answered so far, Failed how many of those it couldn't.
	Rows   int64
	Done   int64
	Failed int64
	// Columns is the result file's header.
	Columns     []string
	ResultBytes int64
	VersionID   string // the model version the last run answered with
	CreatedBy   string
	Created     time.Time
	Started     time.Time // zero until it runs
	Ended       time.Time // zero until it's done
	TeamID      string
	// RepeatSeconds > 0 runs it again that often; NextRun is when.
	RepeatSeconds int64
	NextRun       time.Time
	Runs          int64
}

// Upload reports whether the scoring's rows were uploaded, so they can't
// be fetched again.
func (s Scoring) Upload() bool { return s.Driver == "" && s.DatasetID == "" }

func scoringContext(id string) string { return "scorings.dsn:" + id }

// ScoringDir is where scorings' uploads and results live.
func (s *Store) ScoringDir() string { return filepath.Join(s.Dir(), "scorings") }

// ScoringInputPath is where an uploaded scoring's rows live.
func (s *Store) ScoringInputPath(id string) string {
	return filepath.Join(s.ScoringDir(), id+".in.csv")
}

// ScoringResultPath is where a scoring's latest results live.
func (s *Store) ScoringResultPath(id string) string {
	return filepath.Join(s.ScoringDir(), id+".csv")
}

// ScoringTempPath is where a running scoring writes its results until
// it finishes; they replace the last results only then.
func (s *Store) ScoringTempPath(id string) string {
	return filepath.Join(s.ScoringDir(), id+".part")
}

func (s *Store) fromScoring(r scoring.Row) (Scoring, error) {
	dsn, err := s.openSecret(r.Dsn, scoringContext(r.ID))
	if err != nil {
		return Scoring{}, err
	}
	return Scoring{
		ID:            r.ID,
		Name:          r.Name,
		ModelID:       r.ModelID,
		DatasetID:     r.DatasetID,
		Driver:        r.Driver,
		DSN:           dsn,
		Query:         r.SourceQuery,
		Status:        JobStatus(r.Status),
		Error:         r.Error,
		Rows:          r.Rows,
		Done:          r.Done,
		Failed:        r.Failed,
		Columns:       fromStrs(r.Columns),
		ResultBytes:   r.ResultBytes,
		VersionID:     r.VersionID,
		CreatedBy:     r.CreatedBy,
		Created:       r.CreateTime.UTC(),
		Started:       timeOf(r.StartTime),
		Ended:         timeOf(r.EndTime),
		TeamID:        r.TeamID.V,
		RepeatSeconds: r.RepeatSeconds,
		NextRun:       timeOf(r.NextRun),
		Runs:          r.Runs,
	}, nil
}

func (s *Store) fromScorings(rows []scoring.Row) ([]Scoring, error) {
	out := make([]Scoring, 0, len(rows))
	for _, r := range rows {
		sc, err := s.fromScoring(r)
		if err != nil {
			return nil, err
		}
		out = append(out, sc)
	}
	return out, nil
}

// CreateScoring queues a scoring, filling in ID, Created and Status.
// upload, when set, is its rows, kept as a file first so the scoring is
// never without them.
func (s *Store) CreateScoring(ctx context.Context, sc Scoring, upload []byte) (Scoring, error) {
	id, err := newID()
	if err != nil {
		return Scoring{}, err
	}
	sc.ID, sc.Created, sc.Status = id, now(), JobQueued
	if sc.TeamID == "" {
		sc.TeamID = DefaultTeam
	}
	if err := os.MkdirAll(s.ScoringDir(), 0o700); err != nil {
		return Scoring{}, fmt.Errorf("store: scoring folder: %w", err)
	}
	if upload != nil {
		if err := writeFileAtomic(s.ScoringInputPath(sc.ID), upload); err != nil {
			return Scoring{}, fmt.Errorf("store: write scoring rows: %w", err)
		}
	}
	n := scoring.Create()
	n.SetID(sc.ID)
	n.SetCreateTime(sc.Created)
	n.SetName(sc.Name)
	n.SetTeamID(sc.TeamID)
	n.SetModelID(sc.ModelID)
	n.SetDatasetID(sc.DatasetID)
	n.SetDriver(sc.Driver)
	dsn, err := s.box.Seal(sc.DSN, scoringContext(sc.ID))
	if err != nil {
		return Scoring{}, err
	}
	n.SetDsn(dsn)
	n.SetSourceQuery(sc.Query)
	n.SetStatus(int32(sc.Status))
	n.SetCreatedBy(sc.CreatedBy)
	n.SetRepeatSeconds(sc.RepeatSeconds)
	if _, err := n.Insert(ctx, s.ex); err != nil {
		os.Remove(s.ScoringInputPath(sc.ID))
		return Scoring{}, fmt.Errorf("store: insert scoring: %w", err)
	}
	return sc, nil
}

// GetScoring returns one scoring.
func (s *Store) GetScoring(ctx context.Context, id string) (Scoring, error) {
	r, ok, err := scoring.New().Where(scoring.ID.Eq(id)).One(ctx, s.ex)
	if err != nil {
		return Scoring{}, err
	}
	if !ok {
		return Scoring{}, ErrNotFound
	}
	return s.fromScoring(r)
}

// ListScorings returns a team's scorings, newest first; modelID, when
// set, keeps only that model's.
func (s *Store) ListScorings(ctx context.Context, teamID, modelID string) ([]Scoring, error) {
	rows, err := scoring.New().
		Where(scoring.TeamID.Eq(teamID)).
		WhereIf(modelID != "", scoring.ModelID.Eq(modelID)).
		Order(scoring.CreateTime.Desc(), scoring.ID.Desc()).
		Limit(1000).
		All(ctx, s.ex, nil)
	if err != nil {
		return nil, fmt.Errorf("store: list scorings: %w", err)
	}
	return s.fromScorings(rows)
}

// ClaimScoring takes the oldest queued scoring not in skip and marks it
// running, from no rows done. It returns ErrNotFound when there is none.
func (s *Store) ClaimScoring(ctx context.Context, skip ...string) (Scoring, error) {
	var sc Scoring
	err := storm.InTx(ctx, s.ex, func(ex storm.Executor) error {
		// SKIP LOCKED: two claimers take two different scorings, and
		// neither waits for the other.
		r, ok, err := scoring.New().
			Where(scoring.Status.Eq(int32(JobQueued))).
			WhereIf(len(skip) > 0, scoring.ID.NotIn(skip...)).
			Order(scoring.CreateTime.Asc(), scoring.ID.Asc()).
			ForUpdateSkipLocked().
			One(ctx, ex)
		if err != nil {
			return err
		}
		if !ok {
			return ErrNotFound
		}
		t := now()
		m := scoring.Mutate(r)
		m.SetStatus(int32(JobRunning))
		m.SetStartTime(t)
		m.SetEndTimeNull()
		m.SetError("")
		m.SetRows(0)
		m.SetDone(0)
		m.SetFailed(0)
		if err := m.Update(ctx, ex); err != nil {
			return err
		}
		if sc, err = s.fromScoring(r); err != nil {
			return err
		}
		sc.Status, sc.Started, sc.Ended, sc.Error = JobRunning, t, time.Time{}, ""
		sc.Rows, sc.Done, sc.Failed = 0, 0, 0
		return nil
	})
	if err != nil {
		return Scoring{}, err
	}
	return sc, nil
}

// updateScoring applies m to a scoring. One that is gone is not an
// error: the runner writes progress for scorings a person may have just
// deleted.
func (s *Store) updateScoring(ctx context.Context, m scoring.Mut) error {
	if err := m.Update(ctx, s.ex); err != nil && !errors.Is(err, runtime.ErrNoRow) {
		return fmt.Errorf("store: update scoring: %w", err)
	}
	return nil
}

// ScoringProgress records how far a running scoring is.
func (s *Store) ScoringProgress(ctx context.Context, id string, rows, done, failed int64) error {
	m := scoring.MutateKey(id)
	m.SetRows(rows)
	m.SetDone(done)
	m.SetFailed(failed)
	return s.updateScoring(ctx, m)
}

// ScoringResult is what a finished run of a scoring made.
type ScoringResult struct {
	Rows, Done, Failed int64
	Columns            []string
	Bytes              int64
	VersionID          string
}

// FinishScoring ends a running scoring: with its results, or failed with
// why, and schedules its next run when it repeats. A scoring cancelled
// meanwhile stays cancelled; FinishScoring then reports false.
func (s *Store) FinishScoring(ctx context.Context, id string, res ScoringResult, failure string) (bool, error) {
	finished := false
	err := storm.InTx(ctx, s.ex, func(ex storm.Executor) error {
		r, ok, err := scoring.New().
			Where(scoring.ID.Eq(id), scoring.Status.Eq(int32(JobRunning))).
			ForUpdate().
			One(ctx, ex)
		if err != nil || !ok {
			return err
		}
		t := now()
		m := scoring.Mutate(r)
		m.SetEndTime(t)
		if failure != "" {
			m.SetStatus(int32(JobFailed))
			m.SetError(failure)
		} else {
			m.SetStatus(int32(JobSucceeded))
			m.SetError("")
			m.SetRows(res.Rows)
			m.SetDone(res.Done)
			m.SetFailed(res.Failed)
			m.SetColumns(strs(res.Columns))
			m.SetResultBytes(res.Bytes)
			m.SetVersionID(res.VersionID)
			m.SetRuns(r.Runs + 1)
		}
		if r.RepeatSeconds > 0 {
			m.SetNextRun(t.Add(time.Duration(r.RepeatSeconds) * time.Second))
		}
		finished = true
		return m.Update(ctx, ex)
	})
	if err != nil {
		return false, fmt.Errorf("store: finish scoring: %w", err)
	}
	return finished, nil
}

// ScoringStatus returns just a scoring's status, for a runner polling
// for cancel.
func (s *Store) ScoringStatus(ctx context.Context, id string) (JobStatus, error) {
	r, ok, err := scoring.New().Where(scoring.ID.Eq(id)).One(ctx, s.ex)
	if err != nil {
		return 0, err
	}
	if !ok {
		return 0, ErrNotFound
	}
	return JobStatus(r.Status), nil
}

// CancelScoring stops a queued or running scoring and reports whether it
// did.
func (s *Store) CancelScoring(ctx context.Context, id string) (bool, error) {
	var m scoring.Mut
	m.SetStatus(int32(JobCancelled))
	m.SetEndTime(now())
	n, err := scoring.New().
		Where(scoring.ID.Eq(id), scoring.Status.In(int32(JobQueued), int32(JobRunning))).
		UpdateAll(ctx, s.ex, m)
	if err != nil {
		return false, fmt.Errorf("store: cancel scoring: %w", err)
	}
	return n > 0, nil
}

// RequeueScoring queues a scoring that isn't queued or running, and
// reports whether it did.
func (s *Store) RequeueScoring(ctx context.Context, id string) (bool, error) {
	var m scoring.Mut
	m.SetStatus(int32(JobQueued))
	m.SetError("")
	m.SetEndTimeNull()
	n, err := scoring.New().
		Where(scoring.ID.Eq(id), scoring.Status.Gte(int32(JobSucceeded))).
		UpdateAll(ctx, s.ex, m)
	if err != nil {
		return false, fmt.Errorf("store: requeue scoring: %w", err)
	}
	return n > 0, nil
}

// SetScoringRepeat changes how often a scoring runs again; 0 stops it.
// The next run is due one interval from now.
func (s *Store) SetScoringRepeat(ctx context.Context, id string, seconds int64) error {
	m := scoring.MutateKey(id)
	m.SetRepeatSeconds(seconds)
	if seconds > 0 {
		m.SetNextRun(now().Add(time.Duration(seconds) * time.Second))
	} else {
		m.SetNextRunNull()
	}
	if err := m.Update(ctx, s.ex); err != nil {
		if errors.Is(err, runtime.ErrNoRow) {
			return ErrNotFound
		}
		return fmt.Errorf("store: set scoring repeat: %w", err)
	}
	return nil
}

// QueueDueScorings queues the repeating scorings whose next run is due
// at at and returns how many it queued. One still queued or running is
// left to finish; its next run is set when it does.
func (s *Store) QueueDueScorings(ctx context.Context, at time.Time) (int64, error) {
	var m scoring.Mut
	m.SetStatus(int32(JobQueued))
	m.SetError("")
	m.SetEndTimeNull()
	m.SetNextRunNull()
	n, err := scoring.New().
		Where(
			scoring.RepeatSeconds.Gt(0),
			scoring.NextRun.IsNotNull(),
			scoring.NextRun.Lte(at),
			scoring.Status.Gte(int32(JobSucceeded)),
		).
		UpdateAll(ctx, s.ex, m)
	if err != nil {
		return 0, fmt.Errorf("store: queue due scorings: %w", err)
	}
	return n, nil
}

// DeleteScoring removes a scoring and its files. A running one can't go.
func (s *Store) DeleteScoring(ctx context.Context, id string) error {
	n, err := scoring.New().
		Where(scoring.ID.Eq(id), scoring.Status.NotEq(int32(JobRunning))).
		DeleteAll(ctx, s.ex)
	if err != nil {
		return fmt.Errorf("store: delete scoring: %w", err)
	}
	if n == 0 {
		return ErrNotFound
	}
	return s.removeScoringFiles(id)
}

func (s *Store) removeScoringFiles(id string) error {
	for _, p := range []string{s.ScoringInputPath(id), s.ScoringResultPath(id), s.ScoringTempPath(id)} {
		if err := os.Remove(p); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
	}
	return nil
}

// modelScoringIDs returns the ids of a model's scorings, so their files
// can go with it.
func (s *Store) modelScoringIDs(ctx context.Context, modelID string) ([]string, error) {
	rows, err := scoring.New().Where(scoring.ModelID.Eq(modelID)).Limit(100000).All(ctx, s.ex, nil)
	if err != nil {
		return nil, fmt.Errorf("store: model scorings: %w", err)
	}
	ids := make([]string, len(rows))
	for i, r := range rows {
		ids[i] = r.ID
	}
	return ids, nil
}

// ResetRunningScorings queues again the scorings a previous process left
// running, for a server that restarted mid-run. They start over.
func (s *Store) ResetRunningScorings(ctx context.Context) error {
	var m scoring.Mut
	m.SetStatus(int32(JobQueued))
	if _, err := scoring.New().Where(scoring.Status.Eq(int32(JobRunning))).UpdateAll(ctx, s.ex, m); err != nil {
		return fmt.Errorf("store: reset running scorings: %w", err)
	}
	return nil
}
