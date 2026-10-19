package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/gsoultan/storm"
	"github.com/gsoultan/storm/runtime"

	"github.com/gsoultan/mimir-server/internal/store/db/auditevent"
	"github.com/gsoultan/mimir-server/internal/store/db/model"
	"github.com/gsoultan/mimir-server/internal/store/db/trainingjob"
	"github.com/gsoultan/mimir-server/internal/store/db/worker"
	"github.com/gsoultan/mimir-server/internal/store/db/workersample"
	"github.com/gsoultan/mimir-server/internal/store/tables"
)

// Worker is a registered worker: a process that connects with its token
// and runs models for this server.
type Worker struct {
	ID        string
	Name      string
	CreatedBy string
	Created   time.Time
	LastSeen  time.Time // zero until it first connects
	// PublicKey is the Ed25519 key it joined with; nil until it joins.
	PublicKey []byte
	// LastAddress is where it last connected from.
	LastAddress string
}

func fromWorker(r worker.Row) Worker {
	return Worker{
		ID:          r.ID,
		Name:        r.Name,
		CreatedBy:   r.CreatedBy,
		Created:     r.CreateTime.UTC(),
		LastSeen:    timeOf(r.LastSeen),
		PublicKey:   r.PublicKey,
		LastAddress: r.LastAddress,
	}
}

// CreateWorker registers a worker whose token hashes to tokenHash.
func (s *Store) CreateWorker(ctx context.Context, w Worker, tokenHash string) (Worker, error) {
	id, err := newID()
	if err != nil {
		return Worker{}, err
	}
	w.ID, w.Created = id, now()
	n := worker.Create()
	n.SetID(w.ID)
	n.SetName(w.Name)
	n.SetTokenHash(tokenHash)
	n.SetCreatedBy(w.CreatedBy)
	n.SetCreateTime(w.Created)
	if _, err := n.Insert(ctx, s.ex); err != nil {
		return Worker{}, fmt.Errorf("store: insert worker: %w", err)
	}
	return w, nil
}

// ListWorkers returns every registered worker, oldest first.
func (s *Store) ListWorkers(ctx context.Context) ([]Worker, error) {
	rows, err := worker.New().Order(worker.CreateTime.Asc(), worker.ID.Asc()).Limit(1000).All(ctx, s.ex, nil)
	if err != nil {
		return nil, fmt.Errorf("store: list workers: %w", err)
	}
	out := make([]Worker, 0, len(rows))
	for _, r := range rows {
		out = append(out, fromWorker(r))
	}
	return out, nil
}

// WorkerByToken returns the worker whose token hashes to tokenHash and
// notes that it was seen.
func (s *Store) WorkerByToken(ctx context.Context, tokenHash string) (Worker, error) {
	r, ok, err := worker.New().Where(worker.TokenHash.Eq(tokenHash)).One(ctx, s.ex)
	if err != nil {
		return Worker{}, fmt.Errorf("store: worker: %w", err)
	}
	if !ok {
		return Worker{}, ErrNotFound
	}
	s.TouchWorker(ctx, r.ID)
	return fromWorker(r), nil
}

// TouchWorker records that a worker is connected now. Best effort.
func (s *Store) TouchWorker(ctx context.Context, id string) {
	m := worker.MutateKey(id)
	m.SetLastSeen(now())
	m.Update(ctx, s.ex)
}

// DeleteWorker removes a worker; its token stops working.
// Models and waiting runs placed on it go back to automatic.
func (s *Store) DeleteWorker(ctx context.Context, id string) error {
	return storm.InTx(ctx, s.ex, func(ex storm.Executor) error {
		if err := worker.Delete(ctx, ex, id); err != nil {
			if errors.Is(err, runtime.ErrNoRow) {
				return ErrNotFound
			}
			return fmt.Errorf("store: delete worker: %w", err)
		}
		var um model.Mut
		um.SetWorkerID("")
		if _, err := model.New().Where(model.WorkerID.Eq(id)).UpdateAll(ctx, ex, um); err != nil {
			return fmt.Errorf("store: delete worker: %w", err)
		}
		var ur trainingjob.Mut
		ur.SetRunOn("")
		if _, err := trainingjob.New().Where(trainingjob.RunOn.Eq(id)).UpdateAll(ctx, ex, ur); err != nil {
			return fmt.Errorf("store: delete worker: %w", err)
		}
		return nil
	})
}

// GetWorker returns one worker.
func (s *Store) GetWorker(ctx context.Context, id string) (Worker, error) {
	r, ok, err := worker.New().Where(worker.ID.Eq(id)).One(ctx, s.ex)
	if err != nil {
		return Worker{}, fmt.Errorf("store: worker: %w", err)
	}
	if !ok {
		return Worker{}, ErrNotFound
	}
	return fromWorker(r), nil
}

// SetWorkerKey records the key a worker joined with. It only sets a key
// when none is recorded, so two links racing to join can't both win; it
// reports whether this call set it.
func (s *Store) SetWorkerKey(ctx context.Context, id string, key []byte) (bool, error) {
	set := false
	err := storm.InTx(ctx, s.ex, func(ex storm.Executor) error {
		// The row lock makes the check and the write one step.
		r, ok, err := worker.New().Where(worker.ID.Eq(id), worker.PublicKey.IsNull()).ForUpdate().One(ctx, ex)
		if err != nil || !ok {
			return err
		}
		m := worker.Mutate(r)
		m.SetPublicKey(key)
		if err := m.Update(ctx, ex); err != nil {
			return err
		}
		set = true
		return nil
	})
	if err != nil {
		return false, fmt.Errorf("store: set worker key: %w", err)
	}
	return set, nil
}

// SetWorkerAddress records where a worker connected from.
func (s *Store) SetWorkerAddress(ctx context.Context, id, addr string) error {
	m := worker.MutateKey(id)
	m.SetLastAddress(addr)
	if err := m.Update(ctx, s.ex); err != nil && !errors.Is(err, runtime.ErrNoRow) {
		return fmt.Errorf("store: set worker address: %w", err)
	}
	return nil
}

// ReplaceWorkerToken gives a worker a new token hash and forgets its key.
func (s *Store) ReplaceWorkerToken(ctx context.Context, id, tokenHash string) error {
	m := worker.MutateKey(id)
	m.SetTokenHash(tokenHash)
	m.SetPublicKey(nil) // NULL: the next link joins afresh
	if err := m.Update(ctx, s.ex); err != nil {
		if errors.Is(err, runtime.ErrNoRow) {
			return ErrNotFound
		}
		return fmt.Errorf("store: replace worker token: %w", err)
	}
	return nil
}

// WorkerSample is one minute of a worker's stats.
type WorkerSample struct {
	Time                    time.Time
	CPUPercent              float64
	MemoryUsed, MemoryTotal int64
	DiskUsed, DiskTotal     int64
	Busy, Capacity          int
}

// AddWorkerSample stores a sample. A second one in the same millisecond
// replaces the first.
func (s *Store) AddWorkerSample(ctx context.Context, workerID string, x WorkerSample) error {
	n := workersample.Create()
	n.SetWorkerID(workerID)
	n.SetTime(ms(x.Time))
	n.SetCpuPercent(x.CPUPercent)
	n.SetMemoryUsed(x.MemoryUsed)
	n.SetMemoryTotal(x.MemoryTotal)
	n.SetDiskUsed(x.DiskUsed)
	n.SetDiskTotal(x.DiskTotal)
	n.SetBusy(int64(x.Busy))
	n.SetCapacity(int64(x.Capacity))
	if _, err := n.OnConflictWorkerIDTime().Insert(ctx, s.ex); err != nil {
		return fmt.Errorf("store: add worker sample: %w", err)
	}
	return nil
}

// WorkerSamples returns a worker's samples since a time, oldest first,
// averaged into at most points buckets so a week still charts quickly.
func (s *Store) WorkerSamples(ctx context.Context, workerID string, since time.Time, points int) ([]WorkerSample, error) {
	since = ms(since)
	width := max(time.Since(since).Milliseconds()/int64(max(points, 1)), 1)
	rows, err := tables.WorkerSampleBuckets.Query(ctx, s.ex, workerID, since, width)
	if err != nil {
		return nil, fmt.Errorf("store: worker samples: %w", err)
	}
	out := make([]WorkerSample, 0, len(rows))
	for _, r := range rows {
		out = append(out, WorkerSample{
			Time:        r.Time.UTC(),
			CPUPercent:  r.CPUPercent,
			MemoryUsed:  r.MemoryUsed,
			MemoryTotal: r.MemoryTotal,
			DiskUsed:    r.DiskUsed,
			DiskTotal:   r.DiskTotal,
			Busy:        int(r.Busy),
			Capacity:    int(r.Capacity),
		})
	}
	return out, nil
}

// Placement counts the models set to run on each worker and the runs
// waiting for each.
func (s *Store) Placement(ctx context.Context) (models, waiting map[string]int, err error) {
	models, waiting = map[string]int{}, map[string]int{}
	placed, err := model.New().Where(model.WorkerID.NotEq("")).AllPerWorker(ctx, s.ex)
	if err != nil {
		return nil, nil, fmt.Errorf("store: placement: %w", err)
	}
	for _, r := range placed {
		models[r.WorkerID] = int(r.N)
	}
	queued, err := trainingjob.New().
		Where(trainingjob.RunOn.NotEq(""), trainingjob.Status.Eq(int32(JobQueued))).
		AllPerRunOn(ctx, s.ex)
	if err != nil {
		return nil, nil, fmt.Errorf("store: placement: %w", err)
	}
	for _, r := range queued {
		waiting[r.RunOn] = int(r.N)
	}
	return models, waiting, nil
}

// Item is a name for a model or run, for a worker's dashboard.
type Item struct {
	ID, Name string
	Status   int
	Time     time.Time
}

// ModelsOnWorker returns the models, in any team, set to run on a worker.
func (s *Store) ModelsOnWorker(ctx context.Context, workerID string) ([]Item, error) {
	rows, err := model.New().
		Where(model.WorkerID.Eq(workerID)).
		Order(model.Name.Asc(), model.ID.Asc()).
		Limit(200).
		All(ctx, s.ex, nil)
	if err != nil {
		return nil, fmt.Errorf("store: items: %w", err)
	}
	out := make([]Item, 0, len(rows))
	for _, r := range rows {
		out = append(out, Item{ID: r.ID, Name: r.Name, Time: r.CreateTime.UTC()})
	}
	return out, nil
}

// RunsOnWorker returns runs waiting for a worker, or run on it, newest
// first.
func (s *Store) RunsOnWorker(ctx context.Context, workerID, workerName string) ([]Item, error) {
	rows, err := trainingjob.New().
		AnyOf(
			trainingjob.And(trainingjob.RunOn.Eq(workerID)),
			trainingjob.And(trainingjob.Worker.Eq(workerName), trainingjob.Worker.NotEq("")),
		).
		Order(trainingjob.CreateTime.Desc(), trainingjob.ID.Desc()).
		Limit(20).
		All(ctx, s.ex, nil)
	if err != nil {
		return nil, fmt.Errorf("store: items: %w", err)
	}
	out := make([]Item, 0, len(rows))
	for _, r := range rows {
		// When it ended, else when it started, else when it was made.
		t := r.CreateTime
		if v, ok := r.StartTime.Get(); ok {
			t = v
		}
		if v, ok := r.EndTime.Get(); ok {
			t = v
		}
		out = append(out, Item{ID: r.ID, Name: r.Name, Status: int(r.Status), Time: t.UTC()})
	}
	return out, nil
}

// WorkerEvents returns a worker's recent audit events, newest first.
func (s *Store) WorkerEvents(ctx context.Context, name string, limit int) ([]AuditEvent, error) {
	rows, err := auditevent.New().
		Where(auditevent.Target.Eq(name), auditevent.Action.Like(`worker.%`)).
		Order(auditevent.Time.Desc(), auditevent.ID.Desc()).
		Limit(int64(limit)).
		All(ctx, s.ex, nil)
	if err != nil {
		return nil, fmt.Errorf("store: worker events: %w", err)
	}
	out := make([]AuditEvent, 0, len(rows))
	for _, r := range rows {
		out = append(out, AuditEvent{ID: r.ID, Time: r.Time.UTC(), Actor: r.Actor, Action: r.Action, Detail: r.Detail, Target: name})
	}
	return out, nil
}
