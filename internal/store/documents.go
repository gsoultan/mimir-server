package store

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/gsoultan/storm"
	"github.com/gsoultan/storm/runtime"

	"github.com/gsoultan/mimir-server/internal/store/db/collection"
	"github.com/gsoultan/mimir-server/internal/store/db/document"
	"github.com/gsoultan/mimir-server/internal/store/db/passage"
)

// Collection is a team's set of documents to answer questions from.
// EmbeddingModelID makes its passages' embeddings and is fixed;
// LanguageModelID writes the answers.
type Collection struct {
	ID               string
	Name             string
	EmbeddingModelID string
	LanguageModelID  string
	Instructions     string
	CreatedBy        string
	Created          time.Time
	TeamID           string
}

// Document is a file in a collection, read into passages in the
// background.
type Document struct {
	ID           string
	CollectionID string
	Name         string
	Kind         string
	Bytes        int64
	Status       JobStatus
	Error        string
	Passages     int64
	CreatedBy    string
	Created      time.Time
	Ended        time.Time // zero until it's read
	TeamID       string
}

// Passage is a piece of a document with its embedding.
type Passage struct {
	ID           string
	DocumentID   string
	CollectionID string
	Number       int32
	Text         string
	Vector       []byte
	// DocumentName and Score are filled in by a search.
	DocumentName string
	Score        float64
}

// MaxPassages bounds how many passages one collection holds, so its
// embeddings fit in memory for a search.
const MaxPassages = 50000

// DocumentDir is where a collection's files live.
func (s *Store) DocumentDir(collectionID string) string {
	return filepath.Join(s.Dir(), "documents", collectionID)
}

// DocumentPath is where a document's file lives.
func (s *Store) DocumentPath(collectionID, id string) string {
	return filepath.Join(s.DocumentDir(collectionID), id)
}

func fromCollection(r collection.Row) Collection {
	return Collection{
		ID: r.ID, Name: r.Name, EmbeddingModelID: r.EmbeddingModelID, LanguageModelID: r.LanguageModelID,
		Instructions: r.Instructions, CreatedBy: r.CreatedBy, Created: r.CreateTime.UTC(), TeamID: r.TeamID.V,
	}
}

func fromDocument(r document.Row) Document {
	return Document{
		ID: r.ID, CollectionID: r.CollectionID, Name: r.Name, Kind: r.Kind, Bytes: r.Bytes,
		Status: JobStatus(r.Status), Error: r.Error, Passages: r.Passages, CreatedBy: r.CreatedBy,
		Created: r.CreateTime.UTC(), Ended: timeOf(r.EndTime), TeamID: r.TeamID.V,
	}
}

// CreateCollection adds a collection, filling in ID and Created.
func (s *Store) CreateCollection(ctx context.Context, c Collection) (Collection, error) {
	id, err := newID()
	if err != nil {
		return Collection{}, err
	}
	c.ID, c.Created = id, now()
	if c.TeamID == "" {
		c.TeamID = DefaultTeam
	}
	n := collection.Create()
	n.SetID(c.ID)
	n.SetCreateTime(c.Created)
	n.SetName(c.Name)
	n.SetTeamID(c.TeamID)
	n.SetEmbeddingModelID(c.EmbeddingModelID)
	n.SetLanguageModelID(c.LanguageModelID)
	n.SetInstructions(c.Instructions)
	n.SetCreatedBy(c.CreatedBy)
	if _, err := n.Insert(ctx, s.ex); err != nil {
		return Collection{}, fmt.Errorf("store: insert collection: %w", err)
	}
	return c, nil
}

// GetCollection returns one collection.
func (s *Store) GetCollection(ctx context.Context, id string) (Collection, error) {
	r, ok, err := collection.New().Where(collection.ID.Eq(id)).One(ctx, s.ex)
	if err != nil {
		return Collection{}, fmt.Errorf("store: get collection: %w", err)
	}
	if !ok {
		return Collection{}, ErrNotFound
	}
	return fromCollection(r), nil
}

// ListCollections returns a team's collections by name.
func (s *Store) ListCollections(ctx context.Context, teamID string) ([]Collection, error) {
	rows, err := collection.New().
		Where(collection.TeamID.Eq(teamID)).
		Order(collection.Name.Asc(), collection.ID.Asc()).
		Limit(1000).
		All(ctx, s.ex, nil)
	if err != nil {
		return nil, fmt.Errorf("store: list collections: %w", err)
	}
	out := make([]Collection, len(rows))
	for i, r := range rows {
		out[i] = fromCollection(r)
	}
	return out, nil
}

// UpdateCollection changes a collection's name, language model and
// instructions.
func (s *Store) UpdateCollection(ctx context.Context, c Collection) error {
	m := collection.MutateKey(c.ID)
	m.SetName(c.Name)
	m.SetLanguageModelID(c.LanguageModelID)
	m.SetInstructions(c.Instructions)
	if err := m.Update(ctx, s.ex); err != nil {
		if errors.Is(err, runtime.ErrNoRow) {
			return ErrNotFound
		}
		return fmt.Errorf("store: update collection: %w", err)
	}
	return nil
}

// DeleteCollection removes a collection with its documents, passages and
// files.
func (s *Store) DeleteCollection(ctx context.Context, id string) error {
	if err := collection.Delete(ctx, s.ex, id); err != nil {
		if errors.Is(err, runtime.ErrNoRow) {
			return ErrNotFound
		}
		return fmt.Errorf("store: delete collection: %w", err)
	}
	return os.RemoveAll(s.DocumentDir(id))
}

// modelCollectionIDs returns the ids of the collections whose embeddings
// a model makes, so their files can go with it.
func (s *Store) modelCollectionIDs(ctx context.Context, modelID string) ([]string, error) {
	rows, err := collection.New().Where(collection.EmbeddingModelID.Eq(modelID)).Limit(100000).All(ctx, s.ex, nil)
	if err != nil {
		return nil, fmt.Errorf("store: model collections: %w", err)
	}
	ids := make([]string, len(rows))
	for i, r := range rows {
		ids[i] = r.ID
	}
	return ids, nil
}

// AddDocument queues a document, keeping its file first so it is never
// without one, and fills in ID, Created, Bytes and Status.
func (s *Store) AddDocument(ctx context.Context, d Document, data []byte) (Document, error) {
	id, err := newID()
	if err != nil {
		return Document{}, err
	}
	d.ID, d.Created, d.Status, d.Bytes = id, now(), JobQueued, int64(len(data))
	if d.TeamID == "" {
		d.TeamID = DefaultTeam
	}
	if err := os.MkdirAll(s.DocumentDir(d.CollectionID), 0o700); err != nil {
		return Document{}, fmt.Errorf("store: document folder: %w", err)
	}
	path := s.DocumentPath(d.CollectionID, d.ID)
	if err := writeFileAtomic(path, data); err != nil {
		return Document{}, fmt.Errorf("store: write document: %w", err)
	}
	n := document.Create()
	n.SetID(d.ID)
	n.SetCreateTime(d.Created)
	n.SetName(d.Name)
	n.SetTeamID(d.TeamID)
	n.SetCollectionID(d.CollectionID)
	n.SetKind(d.Kind)
	n.SetBytes(d.Bytes)
	n.SetStatus(int32(d.Status))
	n.SetCreatedBy(d.CreatedBy)
	if _, err := n.Insert(ctx, s.ex); err != nil {
		os.Remove(path)
		return Document{}, fmt.Errorf("store: insert document: %w", err)
	}
	return d, nil
}

// GetDocument returns one document.
func (s *Store) GetDocument(ctx context.Context, id string) (Document, error) {
	r, ok, err := document.New().Where(document.ID.Eq(id)).One(ctx, s.ex)
	if err != nil {
		return Document{}, fmt.Errorf("store: get document: %w", err)
	}
	if !ok {
		return Document{}, ErrNotFound
	}
	return fromDocument(r), nil
}

// ListDocuments returns a collection's documents, newest first.
func (s *Store) ListDocuments(ctx context.Context, collectionID string) ([]Document, error) {
	rows, err := document.New().
		Where(document.CollectionID.Eq(collectionID)).
		Order(document.CreateTime.Desc(), document.ID.Desc()).
		Limit(10000).
		All(ctx, s.ex, nil)
	if err != nil {
		return nil, fmt.Errorf("store: list documents: %w", err)
	}
	out := make([]Document, len(rows))
	for i, r := range rows {
		out[i] = fromDocument(r)
	}
	return out, nil
}

// TeamDocuments returns every document of a team's, for totals per
// collection.
func (s *Store) TeamDocuments(ctx context.Context, teamID string) ([]Document, error) {
	rows, err := document.New().Where(document.TeamID.Eq(teamID)).Limit(100000).Unordered().All(ctx, s.ex, nil)
	if err != nil {
		return nil, fmt.Errorf("store: team documents: %w", err)
	}
	out := make([]Document, len(rows))
	for i, r := range rows {
		out[i] = fromDocument(r)
	}
	return out, nil
}

// DeleteDocument removes a document with its passages and file. One
// being read can't go.
func (s *Store) DeleteDocument(ctx context.Context, d Document) error {
	n, err := document.New().
		Where(document.ID.Eq(d.ID), document.Status.NotEq(int32(JobRunning))).
		DeleteAll(ctx, s.ex)
	if err != nil {
		return fmt.Errorf("store: delete document: %w", err)
	}
	if n == 0 {
		return ErrNotFound
	}
	if err := os.Remove(s.DocumentPath(d.CollectionID, d.ID)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// ClaimDocument takes the oldest queued document and marks it running.
// It returns ErrNotFound when there is none.
func (s *Store) ClaimDocument(ctx context.Context) (Document, error) {
	var d Document
	err := storm.InTx(ctx, s.ex, func(ex storm.Executor) error {
		r, ok, err := document.New().
			Where(document.Status.Eq(int32(JobQueued))).
			Order(document.CreateTime.Asc(), document.ID.Asc()).
			ForUpdateSkipLocked().
			One(ctx, ex)
		if err != nil {
			return err
		}
		if !ok {
			return ErrNotFound
		}
		m := document.Mutate(r)
		m.SetStatus(int32(JobRunning))
		if err := m.Update(ctx, ex); err != nil {
			return err
		}
		d = fromDocument(r)
		d.Status = JobRunning
		return nil
	})
	if err != nil {
		return Document{}, err
	}
	return d, nil
}

// FinishDocument ends a document being read: with its passages, which
// replace any it had, or failed with why. A document deleted meanwhile
// is not an error.
func (s *Store) FinishDocument(ctx context.Context, id string, passages []Passage, failure string) error {
	err := storm.InTx(ctx, s.ex, func(ex storm.Executor) error {
		r, ok, err := document.New().Where(document.ID.Eq(id)).ForUpdate().One(ctx, ex)
		if err != nil || !ok {
			return err
		}
		m := document.Mutate(r)
		m.SetEndTime(now())
		if failure != "" {
			m.SetStatus(int32(JobFailed))
			m.SetError(failure)
			return m.Update(ctx, ex)
		}
		if len(passages) > 0 {
			t := now()
			rows := make([]passage.Row, len(passages))
			for i, p := range passages {
				pid, err := newID()
				if err != nil {
					return err
				}
				rows[i] = passage.Row{ID: pid, CreateTime: t, DocumentID: id, CollectionID: r.CollectionID, Number: p.Number, Text: p.Text, Vector: p.Vector}
			}
			if _, err := passage.InsertAll(ctx, ex, rows); err != nil {
				return err
			}
		}
		m.SetStatus(int32(JobSucceeded))
		m.SetError("")
		m.SetPassages(int64(len(passages)))
		return m.Update(ctx, ex)
	})
	if err != nil {
		return fmt.Errorf("store: finish document: %w", err)
	}
	return nil
}

// CollectionPassages returns how many passages a collection's documents
// have, read or being read.
func (s *Store) CollectionPassages(ctx context.Context, collectionID string) (int64, error) {
	n, err := passage.New().Where(passage.CollectionID.Eq(collectionID)).Count(ctx, s.ex)
	if err != nil {
		return 0, fmt.Errorf("store: count passages: %w", err)
	}
	return n, nil
}

// AllPassages returns every passage of a collection, at most
// MaxPassages, for the in-memory search.
func (s *Store) AllPassages(ctx context.Context, collectionID string) ([]Passage, error) {
	rows, err := passage.New().
		Where(passage.CollectionID.Eq(collectionID)).
		Unordered().
		Limit(MaxPassages).
		All(ctx, s.ex, nil)
	if err != nil {
		return nil, fmt.Errorf("store: collection passages: %w", err)
	}
	out := make([]Passage, len(rows))
	for i, r := range rows {
		out[i] = Passage{ID: r.ID, DocumentID: r.DocumentID, CollectionID: r.CollectionID, Number: r.Number, Text: r.Text, Vector: r.Vector}
	}
	return out, nil
}

// MatchPassages returns up to limit of a collection's passages that hold
// the words of query, as a web search engine reads it: quoted phrases,
// OR, and -word.
func (s *Store) MatchPassages(ctx context.Context, collectionID, query string, limit int64) ([]Passage, error) {
	rows, err := passage.New().
		Where(passage.CollectionID.Eq(collectionID), passage.Search.WebSearch(query)).
		Unordered().
		Limit(limit).
		All(ctx, s.ex, nil)
	if err != nil {
		return nil, fmt.Errorf("store: match passages: %w", err)
	}
	out := make([]Passage, len(rows))
	for i, r := range rows {
		out[i] = Passage{ID: r.ID, DocumentID: r.DocumentID, CollectionID: r.CollectionID, Number: r.Number, Text: r.Text}
	}
	return out, nil
}

// ResetRunningDocuments queues again the documents a previous process
// left half read.
func (s *Store) ResetRunningDocuments(ctx context.Context) error {
	var m document.Mut
	m.SetStatus(int32(JobQueued))
	if _, err := document.New().Where(document.Status.Eq(int32(JobRunning))).UpdateAll(ctx, s.ex, m); err != nil {
		return fmt.Errorf("store: reset running documents: %w", err)
	}
	return nil
}
