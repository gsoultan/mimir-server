package api

import (
	"cmp"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"

	mimirv1 "github.com/gsoultan/mimir-server/gen/mimir/v1"
	"github.com/gsoultan/mimir-server/gen/mimir/v1/mimirv1connect"
	"github.com/gsoultan/mimir-server/internal/auth"
	"github.com/gsoultan/mimir-server/internal/docread"
	"github.com/gsoultan/mimir-server/internal/runtime"
	"github.com/gsoultan/mimir-server/internal/store"
)

var _ mimirv1connect.DocumentServiceHandler = (*Service)(nil)

const (
	// MaxDocumentBytes bounds one file added to a collection.
	MaxDocumentBytes = 64 << 20
	// embedBatch is how many passages are embedded at once.
	embedBatch = 16
	// Passages a search returns and an answer is written from, by
	// default and at most.
	defaultSearch  = 5
	maxSearch      = 20
	defaultSources = 5
	maxSources     = 10
	// candidates is how many passages each way of matching (meaning and
	// words) puts forward before the two are merged.
	candidates = 40
	// rrfK damps the merge of the two rankings (reciprocal rank fusion):
	// a passage near the top of both beats one at the top of one.
	rrfK = 60
	// askHistory is how many earlier messages of a chat go with a
	// question, so a long chat still leaves room for the sources.
	askHistory = 6
	// indexes is how many collections' embeddings stay in memory.
	indexes = 4
)

// groundRules are what the language model is told before the sources.
const groundRules = "Answer the question from the numbered sources below. Cite the sources you use, as [1], [2] and so on, right after what they support. " +
	"If the sources don't hold the answer, say you couldn't find it in the documents; don't make up an answer."

func noCollection() error {
	return connect.NewError(connect.CodeNotFound, errors.New("No such collection."))
}

func noDocument() error {
	return connect.NewError(connect.CodeNotFound, errors.New("No such document."))
}

func (s *Service) ListCollections(ctx context.Context, req *connect.Request[mimirv1.ListCollectionsRequest]) (*connect.Response[mimirv1.ListCollectionsResponse], error) {
	p, _ := auth.From(ctx)
	cs, err := s.store.ListCollections(ctx, p.Team())
	if err != nil {
		return nil, s.internal(ctx, err)
	}
	names, err := s.modelNames(ctx, p)
	if err != nil {
		return nil, s.internal(ctx, err)
	}
	docs, err := s.store.TeamDocuments(ctx, p.Team())
	if err != nil {
		return nil, s.internal(ctx, err)
	}
	totals := map[string]*mimirv1.Collection{}
	res := &mimirv1.ListCollectionsResponse{}
	for _, c := range cs {
		if _, ok := names[c.EmbeddingModelID]; !ok {
			continue // a model outside the caller's access
		}
		out := collectionProto(c, names)
		totals[c.ID] = out
		res.Collections = append(res.Collections, out)
	}
	for _, d := range docs {
		if out, ok := totals[d.CollectionID]; ok {
			out.Documents++
			out.Passages += d.Passages
			out.Bytes += d.Bytes
		}
	}
	return connect.NewResponse(res), nil
}

func (s *Service) GetCollection(ctx context.Context, req *connect.Request[mimirv1.GetCollectionRequest]) (*connect.Response[mimirv1.GetCollectionResponse], error) {
	c, err := s.getCollection(ctx, req.Msg.GetId())
	if err != nil {
		return nil, err
	}
	docs, err := s.store.ListDocuments(ctx, c.ID)
	if err != nil {
		return nil, s.internal(ctx, err)
	}
	names, err := s.modelNames(ctx, mustPrincipal(ctx))
	if err != nil {
		return nil, s.internal(ctx, err)
	}
	res := &mimirv1.GetCollectionResponse{Collection: collectionProto(c, names)}
	for _, d := range docs {
		res.Collection.Documents++
		res.Collection.Passages += d.Passages
		res.Collection.Bytes += d.Bytes
		res.Documents = append(res.Documents, documentProto(d))
	}
	return connect.NewResponse(res), nil
}

func (s *Service) CreateCollection(ctx context.Context, req *connect.Request[mimirv1.CreateCollectionRequest]) (*connect.Response[mimirv1.CreateCollectionResponse], error) {
	p, _ := auth.From(ctx)
	msg := req.Msg
	name, err := store.ParseName("collection", msg.GetName())
	if err != nil {
		return nil, refused(err)
	}
	instructions, err := collectionInstructions(msg.GetInstructions())
	if err != nil {
		return nil, err
	}
	emb, err := s.LanguageModel(ctx, msg.GetEmbeddingModelId())
	if err != nil {
		return nil, err
	}
	lm, err := s.LanguageModel(ctx, cmp.Or(msg.GetLanguageModelId(), emb.ID))
	if err != nil {
		return nil, err
	}
	c, err := s.store.CreateCollection(ctx, store.Collection{
		Name: name.String(), EmbeddingModelID: emb.ID, LanguageModelID: lm.ID, Instructions: instructions, CreatedBy: p.Actor(), TeamID: p.Team(),
	})
	if err != nil {
		return nil, s.internal(ctx, err)
	}
	s.audit(ctx, p.Actor(), "collection.create", c.Name, "")
	names := map[string]string{emb.ID: emb.Name, lm.ID: lm.Name}
	return connect.NewResponse(&mimirv1.CreateCollectionResponse{Collection: collectionProto(c, names)}), nil
}

func (s *Service) UpdateCollection(ctx context.Context, req *connect.Request[mimirv1.UpdateCollectionRequest]) (*connect.Response[mimirv1.UpdateCollectionResponse], error) {
	p, _ := auth.From(ctx)
	msg := req.Msg
	c, err := s.getCollection(ctx, msg.GetId())
	if err != nil {
		return nil, err
	}
	name, err := store.ParseName("collection", msg.GetName())
	if err != nil {
		return nil, refused(err)
	}
	instructions, err := collectionInstructions(msg.GetInstructions())
	if err != nil {
		return nil, err
	}
	lm, err := s.LanguageModel(ctx, cmp.Or(msg.GetLanguageModelId(), c.LanguageModelID))
	if err != nil {
		return nil, err
	}
	c.Name, c.LanguageModelID, c.Instructions = name.String(), lm.ID, instructions
	if err := s.store.UpdateCollection(ctx, c); errors.Is(err, store.ErrNotFound) {
		return nil, noCollection()
	} else if err != nil {
		return nil, s.internal(ctx, err)
	}
	s.audit(ctx, p.Actor(), "collection.update", c.Name, "")
	names, err := s.modelNames(ctx, p)
	if err != nil {
		return nil, s.internal(ctx, err)
	}
	return connect.NewResponse(&mimirv1.UpdateCollectionResponse{Collection: collectionProto(c, names)}), nil
}

func (s *Service) DeleteCollection(ctx context.Context, req *connect.Request[mimirv1.DeleteCollectionRequest]) (*connect.Response[mimirv1.DeleteCollectionResponse], error) {
	p, _ := auth.From(ctx)
	c, err := s.getCollection(ctx, req.Msg.GetId())
	if err != nil {
		return nil, err
	}
	if err := s.store.DeleteCollection(ctx, c.ID); errors.Is(err, store.ErrNotFound) {
		return nil, noCollection()
	} else if err != nil {
		return nil, s.internal(ctx, err)
	}
	s.indexes.forget(c.ID)
	s.audit(ctx, p.Actor(), "collection.delete", c.Name, "")
	return connect.NewResponse(&mimirv1.DeleteCollectionResponse{}), nil
}

func (s *Service) AddDocument(ctx context.Context, req *connect.Request[mimirv1.AddDocumentRequest]) (*connect.Response[mimirv1.AddDocumentResponse], error) {
	p, _ := auth.From(ctx)
	msg := req.Msg
	c, err := s.getCollection(ctx, msg.GetCollectionId())
	if err != nil {
		return nil, err
	}
	name := filepath.Base(strings.ReplaceAll(strings.TrimSpace(msg.GetName()), `\`, "/"))
	kind := docread.KindOf(name)
	switch {
	case name == "." || name == "/" || !utf8.ValidString(name):
		return nil, invalid("Give the file's name, with its extension.")
	case kind == "":
		return nil, invalid("%s can't be read: add text (.txt, .md), web pages (.html), Word documents (.docx) or PDFs.", name)
	case len(msg.GetData()) == 0:
		return nil, invalid("%s is empty.", name)
	case len(msg.GetData()) > MaxDocumentBytes:
		return nil, invalid("%s is more than %d MB.", name, MaxDocumentBytes>>20)
	}
	d, err := s.store.AddDocument(ctx, store.Document{
		CollectionID: c.ID, Name: shortName(name), Kind: kind, CreatedBy: p.Actor(), TeamID: p.Team(),
	}, msg.GetData())
	if err != nil {
		return nil, s.internal(ctx, err)
	}
	s.audit(ctx, p.Actor(), "document.add", d.Name, "to "+c.Name)
	s.KickDocuments()
	return connect.NewResponse(&mimirv1.AddDocumentResponse{Document: documentProto(d)}), nil
}

func (s *Service) GetDocument(ctx context.Context, req *connect.Request[mimirv1.GetDocumentRequest]) (*connect.Response[mimirv1.GetDocumentResponse], error) {
	d, _, err := s.getDocument(ctx, req.Msg.GetId())
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&mimirv1.GetDocumentResponse{Document: documentProto(d)}), nil
}

func (s *Service) DeleteDocument(ctx context.Context, req *connect.Request[mimirv1.DeleteDocumentRequest]) (*connect.Response[mimirv1.DeleteDocumentResponse], error) {
	p, _ := auth.From(ctx)
	d, c, err := s.getDocument(ctx, req.Msg.GetId())
	if err != nil {
		return nil, err
	}
	if d.Status == store.JobRunning {
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("The document is being read; delete it when that's done."))
	}
	if err := s.store.DeleteDocument(ctx, d); errors.Is(err, store.ErrNotFound) {
		return nil, noDocument()
	} else if err != nil {
		return nil, s.internal(ctx, err)
	}
	s.indexes.forget(c.ID)
	s.audit(ctx, p.Actor(), "document.delete", d.Name, "from "+c.Name)
	return connect.NewResponse(&mimirv1.DeleteDocumentResponse{}), nil
}

func (s *Service) Search(ctx context.Context, req *connect.Request[mimirv1.SearchRequest]) (*connect.Response[mimirv1.SearchResponse], error) {
	msg := req.Msg
	c, err := s.getCollection(ctx, msg.GetCollectionId())
	if err != nil {
		return nil, err
	}
	limit := int(msg.GetLimit())
	if limit < 0 || limit > maxSearch {
		return nil, invalid("limit must be between 1 and %d.", maxSearch)
	}
	q := strings.TrimSpace(msg.GetQuery())
	if q == "" || !utf8.ValidString(q) || len(q) > maxPrompt {
		return nil, invalid("Write what to search for, up to %d KB.", maxPrompt>>10)
	}
	emb, err := s.LanguageModel(ctx, c.EmbeddingModelID)
	if err != nil {
		return nil, err
	}
	hits, err := s.search(ctx, c, emb, q, cmp.Or(limit, defaultSearch))
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&mimirv1.SearchResponse{Passages: s.passageProtos(ctx, hits)}), nil
}

func (s *Service) Ask(ctx context.Context, req *connect.Request[mimirv1.AskRequest], out *connect.ServerStream[mimirv1.AskResponse]) error {
	msg := req.Msg
	c, err := s.getCollection(ctx, msg.GetCollectionId())
	if err != nil {
		return err
	}
	n := int(msg.GetSources())
	if n < 0 || n > maxSources {
		return invalid("sources must be between 1 and %d.", maxSources)
	}
	var chat []runtime.Message
	for _, m := range msg.GetMessages() {
		if m.GetRole() == runtime.RoleSystem {
			return invalid("Give a collection its instructions in its settings, not as a message.")
		}
		chat = append(chat, runtime.Message{Role: m.GetRole(), Content: m.GetContent()})
	}
	g, err := s.Ground(ctx, c, chat, "", cmp.Or(n, defaultSources))
	if err != nil {
		return err
	}
	tokens, temp := withDefaults(g.Model, int(msg.GetMaxTokens()), msg.Temperature)
	// The sources go out with the first text, once the model has taken
	// the question: a question too long with all of them is asked again
	// with fewer.
	sent := false
	send := func(text string, sources []store.Passage) error {
		res := &mimirv1.AskResponse{Text: text}
		if !sent {
			res.Sources = s.passageProtos(ctx, sources)
			sent = true
		}
		return out.Send(res)
	}
	w, used, err := s.WriteGrounded(ctx, g, Writing{MaxTokens: tokens, Temperature: temp}, func(sources []store.Passage) func(string) error {
		return sending(func(t string) error { return send(t, sources) })
	})
	if err != nil {
		return err
	}
	if !sent {
		if err := send("", used); err != nil {
			return err
		}
	}
	return out.Send(&mimirv1.AskResponse{
		Done:            true,
		PromptTokens:    int32(w.PromptTokens),
		GeneratedTokens: int32(w.GeneratedTokens),
		Finished:        w.Finished,
		DurationMicros:  w.Duration.Microseconds(),
	})
}

// Grounding is a question ready to be answered from a collection: the
// language model, the chat, and the passages found for it, best first.
type Grounding struct {
	Collection   store.Collection
	Model        store.Model
	Instructions string
	Chat         []runtime.Message
	Sources      []store.Passage
}

// Ground finds the passages of collection c that answer the last
// question of chat, for its language model to answer from. extra are
// instructions to add to the collection's, such as an app's.
func (s *Service) Ground(ctx context.Context, c store.Collection, chat []runtime.Message, extra string, n int) (Grounding, error) {
	if err := checkChat(chat); err != nil {
		return Grounding{}, err
	}
	for _, m := range chat {
		if m.Role == runtime.RoleSystem {
			return Grounding{}, invalid("Only the user and the assistant speak in a chat with documents.")
		}
	}
	emb, err := s.LanguageModel(ctx, c.EmbeddingModelID)
	if err != nil {
		return Grounding{}, err
	}
	lm, err := s.LanguageModel(ctx, c.LanguageModelID)
	if connect.CodeOf(err) == connect.CodeNotFound {
		return Grounding{}, connect.NewError(connect.CodeFailedPrecondition, errors.New("The language model that answers for "+c.Name+" is gone; choose another in its settings."))
	} else if err != nil {
		return Grounding{}, err
	}
	if len(chat) > askHistory+1 {
		chat = chat[len(chat)-askHistory-1:]
	}
	if chat[0].Role != runtime.RoleUser {
		chat = chat[1:]
	}
	// A short follow-up ("and for children?") is searched with the
	// question before it, which says what it is about.
	query := chat[len(chat)-1].Content
	if utf8.RuneCountInString(query) < 60 {
		for i := len(chat) - 2; i >= 0; i-- {
			if chat[i].Role == runtime.RoleUser {
				query = chat[i].Content + "\n" + query
				break
			}
		}
	}
	hits, err := s.search(ctx, c, emb, query, n)
	if err != nil {
		return Grounding{}, err
	}
	instructions := cmp.Or(c.Instructions, lm.Language.Instructions)
	if strings.TrimSpace(extra) != "" {
		instructions = strings.TrimSpace(instructions + "\n\n" + extra)
	}
	return Grounding{Collection: c, Model: lm, Instructions: instructions, Chat: chat, Sources: hits}, nil
}

// Messages are the chat to send the language model with these sources:
// the instructions, the ground rules and the sources first.
func (g Grounding) Messages(sources []store.Passage) []runtime.Message {
	var b strings.Builder
	if g.Instructions != "" {
		b.WriteString(g.Instructions)
		b.WriteString("\n\n")
	}
	b.WriteString(groundRules)
	b.WriteString("\n\nSources:")
	if len(sources) == 0 {
		b.WriteString("\n(none were found)")
	}
	for i, p := range sources {
		fmt.Fprintf(&b, "\n\n[%d] From %s:\n%s", i+1, p.DocumentName, p.Text)
	}
	return append([]runtime.Message{{Role: runtime.RoleSystem, Content: b.String()}}, g.Chat...)
}

// WriteGrounded has the language model answer from the sources, with
// fewer of them when the question is too long for it with all, and
// returns the answer and the sources it was given. onText makes the
// function that gets the text as it is written, for the sources used.
func (s *Service) WriteGrounded(ctx context.Context, g Grounding, w Writing, onText func([]store.Passage) func(string) error) (Written, []store.Passage, error) {
	sources := g.Sources
	for {
		w.Messages = g.Messages(sources)
		if onText != nil {
			w.OnText = onText(sources)
		}
		res, err := s.Write(ctx, g.Model, w)
		if err != nil && connect.CodeOf(err) == connect.CodeInvalidArgument && strings.Contains(err.Error(), "word pieces") && len(sources) > 1 {
			sources = sources[:len(sources)/2]
			continue
		}
		return res, sources, err
	}
}

// CollectionByAPIName finds the collection apps call name in the
// OpenAI-compatible API.
func (s *Service) CollectionByAPIName(ctx context.Context, name string) (store.Collection, error) {
	cs, err := s.Collections(ctx)
	if err != nil {
		return store.Collection{}, err
	}
	var found []store.Collection
	for _, c := range cs {
		if APIName(c.Name) == name {
			found = append(found, c)
		}
	}
	switch len(found) {
	case 0:
		return store.Collection{}, connect.NewError(connect.CodeNotFound, errors.New("There's no language model or collection called "+name+" that this key may use."))
	case 1:
		return found[0], nil
	}
	return store.Collection{}, invalid("Several collections are called %s; rename one.", name)
}

// Collections lists the collections the caller may ask: those whose
// models they may use.
func (s *Service) Collections(ctx context.Context) ([]store.Collection, error) {
	p, _ := auth.From(ctx)
	cs, err := s.store.ListCollections(ctx, p.Team())
	if err != nil {
		return nil, s.internal(ctx, err)
	}
	var out []store.Collection
	for _, c := range cs {
		if p.Access.Allows(c.EmbeddingModelID) && p.Access.Allows(c.LanguageModelID) {
			out = append(out, c)
		}
	}
	return out, nil
}

// getCollection returns a collection the caller may see: one of their
// team's, whose embedding model they may use. Others read as missing.
func (s *Service) getCollection(ctx context.Context, id string) (store.Collection, error) {
	if !store.ValidID(id) {
		return store.Collection{}, noCollection()
	}
	c, err := s.store.GetCollection(ctx, id)
	if errors.Is(err, store.ErrNotFound) {
		return store.Collection{}, noCollection()
	} else if err != nil {
		return store.Collection{}, s.internal(ctx, err)
	}
	if p, _ := auth.From(ctx); c.TeamID != p.Team() || !p.Access.Allows(c.EmbeddingModelID) {
		return store.Collection{}, noCollection()
	}
	return c, nil
}

func (s *Service) getDocument(ctx context.Context, id string) (store.Document, store.Collection, error) {
	if !store.ValidID(id) {
		return store.Document{}, store.Collection{}, noDocument()
	}
	d, err := s.store.GetDocument(ctx, id)
	if errors.Is(err, store.ErrNotFound) {
		return store.Document{}, store.Collection{}, noDocument()
	} else if err != nil {
		return store.Document{}, store.Collection{}, s.internal(ctx, err)
	}
	c, err := s.getCollection(ctx, d.CollectionID)
	if err != nil {
		return store.Document{}, store.Collection{}, noDocument()
	}
	return d, c, nil
}

func mustPrincipal(ctx context.Context) auth.Principal {
	p, _ := auth.From(ctx)
	return p
}

func collectionInstructions(s string) (string, error) {
	s = strings.TrimSpace(s)
	switch {
	case !utf8.ValidString(s):
		return "", invalid("Instructions must be valid text.")
	case utf8.RuneCountInString(s) > maxInstructions:
		return "", invalid("Instructions can be at most %d characters.", maxInstructions)
	}
	return s, nil
}

// shortName keeps a file name within a name's length, cutting before
// the extension.
func shortName(name string) string {
	if utf8.RuneCountInString(name) <= store.MaxName {
		return name
	}
	ext := filepath.Ext(name)
	if utf8.RuneCountInString(ext) > 10 {
		ext = ""
	}
	r := []rune(strings.TrimSuffix(name, ext))
	return string(r[:store.MaxName-utf8.RuneCountInString(ext)-1]) + "…" + ext
}

func collectionProto(c store.Collection, names map[string]string) *mimirv1.Collection {
	return &mimirv1.Collection{
		Id: c.ID, Name: c.Name, ApiName: APIName(c.Name),
		EmbeddingModelId: c.EmbeddingModelID, EmbeddingModelName: names[c.EmbeddingModelID],
		LanguageModelId: c.LanguageModelID, LanguageModelName: names[c.LanguageModelID],
		Instructions: c.Instructions, CreatedBy: c.CreatedBy, CreateTime: timestamppb.New(c.Created),
	}
}

func documentProto(d store.Document) *mimirv1.Document {
	out := &mimirv1.Document{
		Id: d.ID, CollectionId: d.CollectionID, Name: d.Name, Kind: d.Kind, Bytes: d.Bytes,
		Status: mimirv1.JobStatus(d.Status), Error: d.Error, Passages: d.Passages,
		CreatedBy: d.CreatedBy, CreateTime: timestamppb.New(d.Created),
	}
	if !d.Ended.IsZero() {
		out.EndTime = timestamppb.New(d.Ended)
	}
	return out
}

func (s *Service) passageProtos(ctx context.Context, ps []store.Passage) []*mimirv1.Passage {
	out := make([]*mimirv1.Passage, len(ps))
	for i, p := range ps {
		out[i] = &mimirv1.Passage{Id: p.ID, DocumentId: p.DocumentID, DocumentName: p.DocumentName, Number: p.Number, Text: p.Text, Score: p.Score}
	}
	return out
}

// Searching. A collection's passages are matched two ways: by meaning,
// comparing the question's embedding with every passage's, and by words,
// through PostgreSQL's full-text index. Each gives a ranking; the two
// are merged by reciprocal rank fusion.

// quantize keeps an embedding in a byte per number: a float32 scale,
// then each number over the scale as a signed byte. Embeddings are
// compared by angle, which this keeps to within a percent or so, at a
// quarter of the memory.
func quantize(v []float32) []byte {
	var top float64
	for _, x := range v {
		top = max(top, math.Abs(float64(x)))
	}
	out := make([]byte, 4+len(v))
	scale := float32(top / 127)
	binary.LittleEndian.PutUint32(out, math.Float32bits(scale))
	if scale == 0 {
		return out
	}
	for i, x := range v {
		out[4+i] = byte(int8(math.Round(float64(x / scale))))
	}
	return out
}

// indexed is a passage held in memory for search.
type indexed struct {
	store.Passage
	vec  []int8
	norm float32
}

// index is a collection's passages in memory.
type index struct {
	passages []indexed
	docs     map[string]string // document names by id
	used     time.Time
}

// indexCache keeps the embeddings of the collections searched last.
type indexCache struct {
	mu sync.Mutex
	m  map[string]*index
}

func (c *indexCache) forget(id string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.m, id)
}

func (c *indexCache) get(id string) *index {
	c.mu.Lock()
	defer c.mu.Unlock()
	ix := c.m[id]
	if ix != nil {
		ix.used = time.Now()
	}
	return ix
}

func (c *indexCache) put(id string, ix *index) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.m == nil {
		c.m = map[string]*index{}
	}
	ix.used = time.Now()
	c.m[id] = ix
	for len(c.m) > indexes {
		oldest := ""
		for k, v := range c.m {
			if oldest == "" || v.used.Before(c.m[oldest].used) {
				oldest = k
			}
		}
		delete(c.m, oldest)
	}
}

// loadIndex returns collection c's passages in memory, reading them when
// they aren't.
func (s *Service) loadIndex(ctx context.Context, c store.Collection) (*index, error) {
	if ix := s.indexes.get(c.ID); ix != nil {
		return ix, nil
	}
	ps, err := s.store.AllPassages(ctx, c.ID)
	if err != nil {
		return nil, err
	}
	docs, err := s.store.ListDocuments(ctx, c.ID)
	if err != nil {
		return nil, err
	}
	ix := &index{passages: make([]indexed, 0, len(ps)), docs: map[string]string{}}
	for _, d := range docs {
		ix.docs[d.ID] = d.Name
	}
	for _, p := range ps {
		if len(p.Vector) < 4 {
			continue
		}
		vec := make([]int8, len(p.Vector)-4)
		var sum float64
		for i, b := range p.Vector[4:] {
			vec[i] = int8(b)
			sum += float64(vec[i]) * float64(vec[i])
		}
		p.Vector = nil
		ix.passages = append(ix.passages, indexed{Passage: p, vec: vec, norm: float32(math.Sqrt(sum))})
	}
	s.indexes.put(c.ID, ix)
	return ix, nil
}

// search returns up to n of collection c's passages that best answer
// query, best first, with their documents' names.
func (s *Service) search(ctx context.Context, c store.Collection, emb store.Model, query string, n int) ([]store.Passage, error) {
	ix, err := s.loadIndex(ctx, c)
	if err != nil {
		return nil, s.internal(ctx, err)
	}
	if len(ix.passages) == 0 {
		return nil, nil
	}
	e, err := s.Embed(ctx, emb, []string{query})
	if err != nil {
		return nil, err
	}
	q := e.Vectors[0]
	byMeaning := make([]scored, 0, len(ix.passages))
	for i := range ix.passages {
		p := &ix.passages[i]
		if len(p.vec) != len(q) || p.norm == 0 {
			continue // made by another model's embedding, or empty
		}
		var dot float32
		for k, x := range p.vec {
			dot += float32(x) * q[k]
		}
		byMeaning = append(byMeaning, scored{p.ID, float64(dot / p.norm)})
	}
	byMeaning = top(byMeaning, candidates)

	var byWords []scored
	if words := searchWords(query); len(words) > 0 {
		matched, err := s.store.MatchPassages(ctx, c.ID, strings.Join(words, " or "), 400)
		if err != nil {
			return nil, s.internal(ctx, err)
		}
		byWords = top(rankByWords(matched, words), candidates)
	}

	merged := map[string]float64{}
	for rank, h := range byMeaning {
		merged[h.id] += 1 / float64(rrfK+rank+1)
	}
	for rank, h := range byWords {
		merged[h.id] += 1 / float64(rrfK+rank+1)
	}
	all := make([]scored, 0, len(merged))
	for id, sc := range merged {
		all = append(all, scored{id, sc})
	}
	all = top(all, n)
	byID := make(map[string]*indexed, len(ix.passages))
	for i := range ix.passages {
		byID[ix.passages[i].ID] = &ix.passages[i]
	}
	out := make([]store.Passage, 0, len(all))
	for _, h := range all {
		p, ok := byID[h.id]
		if !ok {
			continue // added since the index was read
		}
		hit := p.Passage
		hit.DocumentName, hit.Score = ix.docs[p.DocumentID], h.score
		out = append(out, hit)
	}
	return out, nil
}

type scored struct {
	id    string
	score float64
}

// top sorts by score, best first, and keeps n.
func top(s []scored, n int) []scored {
	slices.SortFunc(s, func(a, b scored) int {
		if c := cmp.Compare(b.score, a.score); c != 0 {
			return c
		}
		return cmp.Compare(a.id, b.id)
	})
	return s[:min(n, len(s))]
}

// stopWords are words too common to search by, in English and
// Indonesian.
var stopWords = map[string]bool{}

func init() {
	for _, w := range strings.Fields(`a an and are as at be but by can do does for from has have how i if in is it its me my
		of on or our so that the their them there these this to was we were what when where which who why will with you your
		ada adalah agar akan apa atau bagaimana bisa dan dari dengan di ini itu jika juga ke kami kita mana pada saya siapa
		untuk yang`) {
		stopWords[w] = true
	}
}

// words splits text into lower-case words.
func words(text string) []string {
	return strings.FieldsFunc(strings.ToLower(text), func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsNumber(r) })
}

// stem cuts a few common English endings off a word, so "returned"
// and "returns" count as "return" when passages are ranked, as they do
// in the full-text search.
func stem(w string) string {
	for _, end := range []string{"ing", "ed", "es", "ly", "s"} {
		if len(w) > len(end)+3 && strings.HasSuffix(w, end) {
			return strings.TrimSuffix(w, end)
		}
	}
	return w
}

// searchWords are the words of a query worth searching by, lower case,
// each once.
func searchWords(q string) []string {
	seen := map[string]bool{}
	var out []string
	for _, w := range words(q) {
		if utf8.RuneCountInString(w) < 2 || stopWords[w] || seen[w] {
			continue
		}
		seen[w] = true
		out = append(out, w)
		if len(out) == 32 {
			break
		}
	}
	return out
}

// rankByWords scores passages by the query words they hold, rarer words
// counting more (as in BM25, with the matched passages standing in for
// the collection).
func rankByWords(ps []store.Passage, query []string) []scored {
	query = slices.Clone(query)
	for i, w := range query {
		query[i] = stem(w)
	}
	df := map[string]int{}
	tokens := make([]map[string]int, len(ps))
	for i, p := range ps {
		tokens[i] = map[string]int{}
		for _, w := range words(p.Text) {
			tokens[i][stem(w)]++
		}
		for _, w := range query {
			if tokens[i][w] > 0 {
				df[w]++
			}
		}
	}
	n := float64(len(ps)) + 1
	out := make([]scored, len(ps))
	for i, p := range ps {
		var sc float64
		for _, w := range query {
			if tf := float64(tokens[i][w]); tf > 0 {
				idf := math.Log(1 + n/float64(df[w]))
				sc += idf * tf * 2.2 / (tf + 1.2)
			}
		}
		out[i] = scored{p.ID, sc}
	}
	return out
}

// Reading documents. A runner in the background reads queued documents
// one at a time: their text, cut into passages, each with an embedding.

// KickDocuments asks the document reader to look at the queue now.
func (s *Service) KickDocuments() {
	s.docKick.Do(func() { s.docKicks = make(chan struct{}, 1) })
	select {
	case s.docKicks <- struct{}{}:
	default:
	}
}

// RunDocuments reads queued documents until ctx ends.
func (s *Service) RunDocuments(ctx context.Context, every time.Duration) {
	s.docKick.Do(func() { s.docKicks = make(chan struct{}, 1) })
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		for ctx.Err() == nil {
			d, err := s.store.ClaimDocument(ctx)
			if err != nil {
				if !errors.Is(err, store.ErrNotFound) && ctx.Err() == nil {
					s.log.ErrorContext(ctx, "documents: claim", "error", err)
				}
				break
			}
			s.readDocument(ctx, d)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		case <-s.docKicks:
		}
	}
}

// readDocument reads one claimed document and records how it went.
func (s *Service) readDocument(ctx context.Context, d store.Document) {
	start := time.Now()
	passages, failure := s.read1(ctx, d)
	if ctx.Err() != nil {
		return // the server is stopping: it's read again at the next start
	}
	if err := s.store.FinishDocument(ctx, d.ID, passages, failure); err != nil {
		s.log.ErrorContext(ctx, "documents: finish", "document_id", d.ID, "error", err)
		return
	}
	s.indexes.forget(d.CollectionID)
	if failure != "" {
		s.log.WarnContext(ctx, "document not read", "document_id", d.ID, "error", failure)
		s.auditIn(ctx, d.TeamID, "server", "document.failed", d.Name, failure)
		return
	}
	s.log.InfoContext(ctx, "document read", "document_id", d.ID, "passages", len(passages), "took", time.Since(start).Round(time.Millisecond))
}

// read1 reads a document's passages and their embeddings, or says in
// plain words why it couldn't.
func (s *Service) read1(ctx context.Context, d store.Document) ([]store.Passage, string) {
	c, err := s.store.GetCollection(ctx, d.CollectionID)
	if err != nil {
		return nil, "Its collection is gone."
	}
	emb, err := s.store.Get(ctx, c.EmbeddingModelID)
	if err != nil {
		return nil, "The collection's embedding model is gone."
	}
	data, err := os.ReadFile(s.store.DocumentPath(d.CollectionID, d.ID))
	if err != nil {
		return nil, "Its file is gone."
	}
	text, err := docread.Read(d.Kind, data)
	if err != nil {
		return nil, fmt.Sprintf("It couldn't be read: %v.", err)
	}
	pieces := docread.Split(text, d.Kind)
	have, err := s.store.CollectionPassages(ctx, c.ID)
	if err != nil {
		return nil, "Something went wrong on the server; add it again."
	}
	if have+int64(len(pieces)) > store.MaxPassages {
		return nil, fmt.Sprintf("Its %d passages would take the collection past %d; make another collection for more.", len(pieces), store.MaxPassages)
	}
	out := make([]store.Passage, 0, len(pieces))
	for i := 0; i < len(pieces); i += embedBatch {
		batch := pieces[i:min(i+embedBatch, len(pieces))]
		e, err := s.Embed(ctx, emb, batch)
		if err != nil {
			if ctx.Err() != nil {
				return nil, "stopped"
			}
			return nil, fmt.Sprintf("Its passages couldn't be embedded: %v", connectMessage(err))
		}
		for k, v := range e.Vectors {
			out = append(out, store.Passage{Number: int32(i + k + 1), Text: batch[k], Vector: quantize(v)})
		}
	}
	return out, ""
}

// connectMessage is an error's message without its code.
func connectMessage(err error) string {
	var ce *connect.Error
	if errors.As(err, &ce) {
		return ce.Message()
	}
	return err.Error()
}
