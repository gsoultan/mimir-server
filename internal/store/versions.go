package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/gsoultan/storm"
	"github.com/gsoultan/storm/runtime"

	"github.com/gsoultan/mimir-server/internal/schema"
	"github.com/gsoultan/mimir-server/internal/store/db/model"
	"github.com/gsoultan/mimir-server/internal/store/db/modelversion"
	"github.com/gsoultan/mimir-server/internal/store/tables"
)

// ErrVersionNotFound reports that a model has no version with the
// requested id.
var ErrVersionNotFound = errors.New("store: model version not found")

// VersionSource says where a version's weights came from. It is the
// column's own type, so the values and their CHECK are declared once.
type VersionSource = tables.VersionSource

const (
	VersionUploaded     = tables.VersionUploaded
	VersionTrained      = tables.VersionTrained
	VersionKeepLearning = tables.VersionKeepLearning
)

// KeptVersions is how many versions a model keeps. When a new one makes
// more, the oldest go, but never the one answering now.
const KeptVersions = 20

// Version is one set of weights a model has had.
type Version struct {
	ID        string
	ModelID   string
	Number    int
	Source    VersionSource
	SizeBytes int64
	SHA256    string
	Created   time.Time
	// What the model reads and answers with these weights, and how well.
	InputShape []int
	Labels     []string
	Features   []string
	Columns    []schema.Column
	JobID      string
	Accuracy   float64
	// Note says why the version exists or wasn't used.
	Note string
	// Live is set by ListVersions on the version answering now.
	Live bool
}

func fromVersion(r modelversion.Row) Version {
	return Version{
		ID:         r.ID,
		ModelID:    r.ModelID,
		Number:     int(r.Number),
		Source:     VersionSource(r.Source),
		SizeBytes:  r.SizeBytes,
		SHA256:     r.Sha256,
		Created:    r.CreateTime.UTC(),
		InputShape: fromInt64s(r.InputShape),
		Labels:     fromStrs(r.Labels),
		Features:   fromStrs(r.Features),
		Columns:    fromDoc[schema.Column](r.Columns),
		JobID:      r.JobID,
		Accuracy:   r.Accuracy,
		Note:       r.Note,
	}
}

// versionOf describes the weights m has now as a version.
func versionOf(m Model) Version {
	src := VersionUploaded
	if m.JobID != "" {
		src = VersionTrained
	}
	return Version{
		ModelID: m.ID, Source: src, SizeBytes: m.SizeBytes, SHA256: m.SHA256,
		InputShape: m.InputShape, Labels: m.Labels, Features: m.Features, Columns: m.Columns,
		JobID: m.JobID, Accuracy: m.Accuracy,
	}
}

// versionPath returns where a version's weights live.
func (s *Store) versionPath(id string) string {
	return filepath.Join(s.weights, "versions", id+".mim")
}

// ListVersions returns a model's versions, newest first, with the one
// answering now marked Live. A model made before versions were kept gets
// its first one recorded here.
func (s *Store) ListVersions(ctx context.Context, modelID string) ([]Version, error) {
	m, err := s.Get(ctx, modelID)
	if err != nil {
		return nil, err
	}
	if m, err = s.ensureVersion(ctx, m); err != nil {
		return nil, err
	}
	rows, err := modelversion.New().
		Where(modelversion.ModelID.Eq(modelID)).
		Order(modelversion.Number.Desc()).
		Limit(KeptVersions*2).
		All(ctx, s.ex, nil)
	if err != nil {
		return nil, fmt.Errorf("store: list versions: %w", err)
	}
	out := make([]Version, 0, len(rows))
	for _, r := range rows {
		v := fromVersion(r)
		v.Live = v.ID == m.VersionID
		out = append(out, v)
	}
	return out, nil
}

// GetVersion returns one of a model's versions, or ErrVersionNotFound.
func (s *Store) GetVersion(ctx context.Context, modelID, id string) (Version, error) {
	r, ok, err := modelversion.New().Where(modelversion.ID.Eq(id)).One(ctx, s.ex)
	if err != nil {
		return Version{}, fmt.Errorf("store: get version %s: %w", id, err)
	}
	if !ok || r.ModelID != modelID {
		return Version{}, ErrVersionNotFound
	}
	return fromVersion(r), nil
}

// ensureVersion records the weights a model made before versions were
// kept have now as its version 1, and returns the model with it.
func (s *Store) ensureVersion(ctx context.Context, m Model) (Model, error) {
	if m.VersionID != "" {
		return m, nil
	}
	id, err := newID()
	if err != nil {
		return Model{}, err
	}
	if err := linkOrCopy(s.path(m.ID), s.versionPath(id)); err != nil {
		return Model{}, fmt.Errorf("store: keep version: %w", err)
	}
	kept := false
	err = storm.InTx(ctx, s.ex, func(ex storm.Executor) error {
		r, ok, err := model.New().Where(model.ID.Eq(m.ID)).ForUpdate().One(ctx, ex)
		if err != nil {
			return fmt.Errorf("store: lock model: %w", err)
		}
		if !ok {
			return ErrNotFound
		}
		if r.VersionID != "" { // another call recorded it first
			m = fromModel(r)
			return nil
		}
		v := versionOf(fromModel(r))
		v.ID, v.Number, v.Created = id, 1, ms(r.UpdateTime)
		if err := insertVersion(ctx, ex, v); err != nil {
			return err
		}
		u := model.MutateKey(m.ID)
		u.SetVersionID(id)
		if err := u.Update(ctx, ex); err != nil {
			return fmt.Errorf("store: set version: %w", err)
		}
		m.VersionID, kept = id, true
		return nil
	})
	if !kept {
		os.Remove(s.versionPath(id))
	}
	if err != nil {
		return Model{}, err
	}
	return m, nil
}

// AddVersion stores weights from a newer pass as a new version of a
// model. When live, the model answers with it from now on, keeping its id
// so apps keep working; otherwise it is only kept, for someone to choose.
// note says why, in plain words. The oldest versions past KeptVersions go.
func (s *Store) AddVersion(ctx context.Context, id string, m Model, weights []byte, live bool, note string) (Version, error) {
	cur, err := s.Get(ctx, id)
	if err != nil {
		return Version{}, err
	}
	if cur, err = s.ensureVersion(ctx, cur); err != nil {
		return Version{}, err
	}
	vid, err := newID()
	if err != nil {
		return Version{}, err
	}
	sum := sha256.Sum256(weights)
	v := Version{
		ID: vid, ModelID: id, Source: VersionKeepLearning, SizeBytes: int64(len(weights)),
		SHA256: hex.EncodeToString(sum[:]), Created: now(),
		InputShape: m.InputShape, Labels: m.Labels, Features: m.Features, Columns: m.Columns,
		JobID: m.JobID, Accuracy: m.Accuracy, Note: note,
	}
	if err := writeFileAtomic(s.versionPath(vid), weights); err != nil {
		return Version{}, fmt.Errorf("store: write weights: %w", err)
	}
	stored := false
	err = storm.InTx(ctx, s.ex, func(ex storm.Executor) error {
		if _, err := s.lockModel(ctx, ex, id); err != nil {
			return err
		}
		last, _, err := modelversion.New().Where(modelversion.ModelID.Eq(id)).Order(modelversion.Number.Desc()).One(ctx, ex)
		if err != nil {
			return fmt.Errorf("store: last version: %w", err)
		}
		v.Number = int(last.Number) + 1
		if err := insertVersion(ctx, ex, v); err != nil {
			return err
		}
		stored = true
		if live {
			return s.putLive(ctx, ex, v)
		}
		return nil
	})
	if err != nil {
		if !stored {
			os.Remove(s.versionPath(vid))
		}
		return Version{}, err
	}
	s.prune(ctx, id)
	v.Live = live
	return v, nil
}

// Replace swaps a model's weights, shape, labels, columns and score for
// those of a newer pass, keeping its id so apps keep working. The weights
// it had stay as an earlier version.
func (s *Store) Replace(ctx context.Context, id string, m Model, weights []byte) error {
	_, err := s.AddVersion(ctx, id, m, weights, true, "")
	return err
}

// UseVersion makes one of a model's versions the one that answers, and
// returns the model as it is then.
func (s *Store) UseVersion(ctx context.Context, modelID, versionID string) (Model, error) {
	v, err := s.GetVersion(ctx, modelID, versionID)
	if err != nil {
		return Model{}, err
	}
	if _, err := os.Stat(s.versionPath(v.ID)); err != nil {
		return Model{}, fmt.Errorf("store: version %d's weights: %w", v.Number, err)
	}
	err = storm.InTx(ctx, s.ex, func(ex storm.Executor) error {
		if _, err := s.lockModel(ctx, ex, modelID); err != nil {
			return err
		}
		return s.putLive(ctx, ex, v)
	})
	if err != nil {
		return Model{}, err
	}
	return s.Get(ctx, modelID)
}

// lockModel locks a model's row for the rest of transaction ex, so
// versions are numbered and put live one at a time.
func (s *Store) lockModel(ctx context.Context, ex storm.Executor, id string) (model.Row, error) {
	r, ok, err := model.New().Where(model.ID.Eq(id)).ForUpdate().One(ctx, ex)
	if err != nil {
		return model.Row{}, fmt.Errorf("store: lock model: %w", err)
	}
	if !ok {
		return model.Row{}, ErrNotFound
	}
	return r, nil
}

// putLive makes v's weights the model's, file first, and its row say so.
func (s *Store) putLive(ctx context.Context, ex storm.Executor, v Version) error {
	if err := linkOrCopy(s.versionPath(v.ID), s.path(v.ModelID)); err != nil {
		return fmt.Errorf("store: write weights: %w", err)
	}
	u := model.MutateKey(v.ModelID)
	u.SetSizeBytes(v.SizeBytes)
	u.SetSha256(v.SHA256)
	u.SetUpdateTime(now())
	u.SetInputShape(int64s(v.InputShape))
	u.SetLabels(strs(v.Labels))
	u.SetFeatures(strs(v.Features))
	u.SetJobID(v.JobID)
	u.SetAccuracy(v.Accuracy)
	u.SetColumns(doc(v.Columns))
	u.SetVersionID(v.ID)
	if err := u.Update(ctx, ex); err != nil {
		if errors.Is(err, runtime.ErrNoRow) {
			return ErrNotFound
		}
		return fmt.Errorf("store: put version live: %w", err)
	}
	return nil
}

func insertVersion(ctx context.Context, ex storm.Executor, v Version) error {
	n := modelversion.Create()
	n.SetID(v.ID)
	n.SetCreateTime(v.Created)
	n.SetModelID(v.ModelID)
	n.SetNumber(int32(v.Number))
	n.SetSource(int32(v.Source))
	n.SetSizeBytes(v.SizeBytes)
	n.SetSha256(v.SHA256)
	n.SetInputShape(int64s(v.InputShape))
	n.SetLabels(strs(v.Labels))
	n.SetFeatures(strs(v.Features))
	n.SetColumns(doc(v.Columns))
	n.SetJobID(v.JobID)
	n.SetAccuracy(v.Accuracy)
	n.SetNote(v.Note)
	if _, err := n.Insert(ctx, ex); err != nil {
		return fmt.Errorf("store: insert version: %w", err)
	}
	return nil
}

// prune removes a model's oldest versions past KeptVersions, never the
// live one. A failure only leaves extra versions, so it is not reported.
func (s *Store) prune(ctx context.Context, modelID string) {
	m, err := s.Get(ctx, modelID)
	if err != nil {
		return
	}
	rows, err := modelversion.New().
		Where(modelversion.ModelID.Eq(modelID)).
		Order(modelversion.Number.Desc()).
		Offset(KeptVersions).
		All(ctx, s.ex, nil)
	if err != nil {
		return
	}
	for _, r := range rows {
		if r.ID == m.VersionID {
			continue
		}
		if err := modelversion.Delete(ctx, s.ex, r.ID); err == nil {
			os.Remove(s.versionPath(r.ID))
		}
	}
}

// versionIDs returns the ids of a model's versions, for removing their
// files when the model goes.
func (s *Store) versionIDs(ctx context.Context, modelID string) ([]string, error) {
	rows, err := modelversion.New().Where(modelversion.ModelID.Eq(modelID)).All(ctx, s.ex, nil)
	if err != nil {
		return nil, fmt.Errorf("store: list versions: %w", err)
	}
	ids := make([]string, len(rows))
	for i, r := range rows {
		ids[i] = r.ID
	}
	return ids, nil
}

// linkOrCopy puts the file at src at dst, replacing what is there in one
// step. It hard-links where it can, so a version and the live weights
// share their bytes on disk, and copies where it can't.
func linkOrCopy(src, dst string) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o750); err != nil {
		return err
	}
	// A name of its own, so two calls putting the same file don't meet.
	f, err := os.CreateTemp(filepath.Dir(dst), tempPrefix+"*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	f.Close()
	os.Remove(tmp)
	if err := os.Link(src, tmp); err != nil {
		if err := copyFile(src, tmp); err != nil {
			os.Remove(tmp)
			return err
		}
	}
	if err := os.Rename(tmp, dst); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o640)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	if err := out.Sync(); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

// removeVersions removes the files of versions whose rows are gone.
func (s *Store) removeVersions(ids []string) {
	for _, id := range ids {
		os.Remove(s.versionPath(id))
	}
}
