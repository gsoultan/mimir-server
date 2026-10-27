package store

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"github.com/gsoultan/storm/runtime"

	"github.com/gsoultan/mimir-server/internal/store/db/benchmark"
	"github.com/gsoultan/mimir-server/internal/store/db/benchmarkscore"
)

// Benchmark is a fixed set of test examples every model of its kind is
// scored on the same way. Its rows are a CSV file, BenchmarkPath.
type Benchmark struct {
	ID          string
	Name        string
	Description string
	DatasetID   string
	Target      string
	Kind        string // "class", "number" or "ranked"
	Rows        int64
	CreatedBy   string
	Created     time.Time
	TeamID      string
}

// BenchmarkScore is one model's latest result on one benchmark.
type BenchmarkScore struct {
	ID          string
	BenchmarkID string
	ModelID     string
	WeightsSHA  string // the weights the model had when it was scored
	Status      JobStatus
	Error       string
	Report      *Report
	Headline    float64
	Scored      time.Time // zero until it is scored
}

// BenchmarkDir is where benchmarks' frozen rows live.
func (s *Store) BenchmarkDir() string { return filepath.Join(s.Dir(), "benchmarks") }

// BenchmarkPath is where a benchmark's rows live.
func (s *Store) BenchmarkPath(id string) string { return filepath.Join(s.BenchmarkDir(), id+".csv") }

// ReadBenchmark returns a benchmark's rows, a CSV with a header.
func (s *Store) ReadBenchmark(id string) ([]byte, error) {
	b, err := os.ReadFile(s.BenchmarkPath(id))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, ErrNotFound
	}
	return b, err
}

func fromBenchmark(r benchmark.Row) Benchmark {
	return Benchmark{
		ID:          r.ID,
		Name:        r.Name,
		Description: r.Description,
		DatasetID:   r.DatasetID,
		Target:      r.Target,
		Kind:        r.Kind,
		Rows:        r.Rows,
		CreatedBy:   r.CreatedBy,
		Created:     r.CreateTime.UTC(),
		TeamID:      r.TeamID.V,
	}
}

// CreateBenchmark adds a benchmark with its rows, filling in ID and
// Created. The file is written first, so a benchmark is never without
// its rows.
func (s *Store) CreateBenchmark(ctx context.Context, b Benchmark, rows []byte) (Benchmark, error) {
	id, err := newID()
	if err != nil {
		return Benchmark{}, err
	}
	b.ID, b.Created = id, now()
	if b.TeamID == "" {
		b.TeamID = DefaultTeam
	}
	if err := os.MkdirAll(s.BenchmarkDir(), 0o700); err != nil {
		return Benchmark{}, fmt.Errorf("store: benchmark folder: %w", err)
	}
	if err := writeFileAtomic(s.BenchmarkPath(b.ID), rows); err != nil {
		return Benchmark{}, fmt.Errorf("store: write benchmark: %w", err)
	}
	n := benchmark.Create()
	n.SetID(b.ID)
	n.SetCreateTime(b.Created)
	n.SetName(b.Name)
	n.SetDescription(b.Description)
	n.SetTeamID(b.TeamID)
	n.SetDatasetID(b.DatasetID)
	n.SetTarget(b.Target)
	n.SetKind(b.Kind)
	n.SetRows(b.Rows)
	n.SetCreatedBy(b.CreatedBy)
	if _, err := n.Insert(ctx, s.ex); err != nil {
		os.Remove(s.BenchmarkPath(b.ID))
		return Benchmark{}, fmt.Errorf("store: insert benchmark: %w", err)
	}
	return b, nil
}

// GetBenchmark returns one benchmark.
func (s *Store) GetBenchmark(ctx context.Context, id string) (Benchmark, error) {
	r, ok, err := benchmark.New().Where(benchmark.ID.Eq(id)).One(ctx, s.ex)
	if err != nil {
		return Benchmark{}, err
	}
	if !ok {
		return Benchmark{}, ErrNotFound
	}
	return fromBenchmark(r), nil
}

// ListBenchmarks returns a team's benchmarks, newest first.
func (s *Store) ListBenchmarks(ctx context.Context, teamID string) ([]Benchmark, error) {
	rows, err := benchmark.New().
		Where(benchmark.TeamID.Eq(teamID)).
		Order(benchmark.CreateTime.Desc(), benchmark.ID.Desc()).
		Limit(1000).
		All(ctx, s.ex, nil)
	if err != nil {
		return nil, fmt.Errorf("store: list benchmarks: %w", err)
	}
	out := make([]Benchmark, 0, len(rows))
	for _, r := range rows {
		out = append(out, fromBenchmark(r))
	}
	return out, nil
}

// DatasetBenchmarks returns the benchmarks frozen from a dataset, whose
// rows its training leaves out.
func (s *Store) DatasetBenchmarks(ctx context.Context, datasetID string) ([]Benchmark, error) {
	rows, err := benchmark.New().
		Where(benchmark.DatasetID.Eq(datasetID)).
		Order(benchmark.CreateTime.Asc(), benchmark.ID.Asc()).
		Limit(1000).
		All(ctx, s.ex, nil)
	if err != nil {
		return nil, fmt.Errorf("store: dataset benchmarks: %w", err)
	}
	out := make([]Benchmark, 0, len(rows))
	for _, r := range rows {
		out = append(out, fromBenchmark(r))
	}
	return out, nil
}

// DeleteBenchmark removes a benchmark, its scores and its rows.
func (s *Store) DeleteBenchmark(ctx context.Context, id string) error {
	if err := benchmark.Delete(ctx, s.ex, id); err != nil {
		if errors.Is(err, runtime.ErrNoRow) {
			return ErrNotFound
		}
		return fmt.Errorf("store: delete benchmark: %w", err)
	}
	if err := os.Remove(s.BenchmarkPath(id)); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("store: delete benchmark rows: %w", err)
	}
	return nil
}

func fromScore(r benchmarkscore.Row) BenchmarkScore {
	return BenchmarkScore{
		ID:          r.ID,
		BenchmarkID: r.BenchmarkID,
		ModelID:     r.ModelID,
		WeightsSHA:  r.WeightsSha,
		Status:      JobStatus(r.Status),
		Error:       r.Error,
		Report:      reportOf(r.Report),
		Headline:    r.Headline,
		Scored:      timeOf(r.ScoredAt),
	}
}

// QueueScore marks a model as waiting to be scored on a benchmark, with
// the weights it has now, and returns the score's id. A model scored
// before keeps one row, started over.
func (s *Store) QueueScore(ctx context.Context, benchmarkID, modelID, weightsSHA string) (string, error) {
	id, err := newID()
	if err != nil {
		return "", err
	}
	n := benchmarkscore.Create()
	n.OnConflictBenchmarkIDModelID()
	n.SetID(id)
	n.SetCreateTime(now())
	n.SetBenchmarkID(benchmarkID)
	n.SetModelID(modelID)
	n.SetWeightsSha(weightsSHA)
	n.SetStatus(int32(JobQueued))
	n.SetError("")
	n.SetReport(reportDoc(nil))
	n.SetHeadline(0)
	n.SetScoredAtNull()
	r, err := n.Insert(ctx, s.ex)
	if err != nil {
		return "", fmt.Errorf("store: queue score: %w", err)
	}
	return r.ID, nil
}

// SetScoreRunning marks a score as being worked out.
func (s *Store) SetScoreRunning(ctx context.Context, id string) error {
	m := benchmarkscore.MutateKey(id)
	m.SetStatus(int32(JobRunning))
	return s.updateScore(ctx, m)
}

// FinishScore stores a score's result: its report and headline, or why
// it failed.
func (s *Store) FinishScore(ctx context.Context, id string, r *Report, headline float64, failure string) error {
	m := benchmarkscore.MutateKey(id)
	if failure != "" {
		m.SetStatus(int32(JobFailed))
		m.SetError(failure)
	} else {
		m.SetStatus(int32(JobSucceeded))
		m.SetReport(reportDoc(r))
		m.SetHeadline(headline)
		m.SetError("")
	}
	m.SetScoredAt(now())
	return s.updateScore(ctx, m)
}

func (s *Store) updateScore(ctx context.Context, m benchmarkscore.Mut) error {
	if err := m.Update(ctx, s.ex); err != nil {
		if errors.Is(err, runtime.ErrNoRow) {
			return ErrNotFound
		}
		return fmt.Errorf("store: update score: %w", err)
	}
	return nil
}

// BenchmarkScores returns every score on a benchmark, in no set order.
func (s *Store) BenchmarkScores(ctx context.Context, benchmarkID string) ([]BenchmarkScore, error) {
	rows, err := benchmarkscore.New().
		Where(benchmarkscore.BenchmarkID.Eq(benchmarkID)).
		Limit(1000).
		All(ctx, s.ex, nil)
	if err != nil {
		return nil, fmt.Errorf("store: list scores: %w", err)
	}
	out := make([]BenchmarkScore, 0, len(rows))
	for _, r := range rows {
		out = append(out, fromScore(r))
	}
	return out, nil
}

// ModelScores returns a model's scores on every benchmark.
func (s *Store) ModelScores(ctx context.Context, modelID string) ([]BenchmarkScore, error) {
	rows, err := benchmarkscore.New().
		Where(benchmarkscore.ModelID.Eq(modelID)).
		Limit(1000).
		All(ctx, s.ex, nil)
	if err != nil {
		return nil, fmt.Errorf("store: model scores: %w", err)
	}
	out := make([]BenchmarkScore, 0, len(rows))
	for _, r := range rows {
		out = append(out, fromScore(r))
	}
	return out, nil
}

// FailUnfinishedScores marks scores left queued or running by a stopped
// server as failed, so none waits forever.
func (s *Store) FailUnfinishedScores(ctx context.Context) error {
	rows, err := benchmarkscore.New().
		Where(benchmarkscore.Status.In(int32(JobQueued), int32(JobRunning))).
		Limit(10000).
		All(ctx, s.ex, nil)
	if err != nil {
		return fmt.Errorf("store: unfinished scores: %w", err)
	}
	for _, r := range rows {
		if err := s.FinishScore(ctx, r.ID, nil, 0, "The server stopped before this score was finished. Score it again."); err != nil {
			return err
		}
	}
	return nil
}
