package api

import (
	"bufio"
	"bytes"
	"context"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"

	mimirv1 "github.com/gsoultan/mimir-server/gen/mimir/v1"
	"github.com/gsoultan/mimir-server/gen/mimir/v1/mimirv1connect"
	"github.com/gsoultan/mimir-server/internal/arch"
	"github.com/gsoultan/mimir-server/internal/ask"
	"github.com/gsoultan/mimir-server/internal/auth"
	"github.com/gsoultan/mimir-server/internal/dbsource"
	"github.com/gsoultan/mimir-server/internal/s3source"
	"github.com/gsoultan/mimir-server/internal/store"
	"github.com/gsoultan/mimir-server/internal/training"
)

var _ mimirv1connect.ScoringServiceHandler = (*Service)(nil)

const (
	// ScoringsAtOnce is how many scorings run at a time, so a big table
	// doesn't crowd out predictions.
	ScoringsAtOnce = 2
	// scoringChunk is how many rows are answered together before they
	// are written out; scoringAsk how many of them are asked at once, so
	// the model answers them in batches.
	scoringChunk = 512
	scoringAsk   = 16
	// scoringCheck is how often a running scoring writes its progress
	// and looks for a cancel.
	scoringCheck = 2 * time.Second
	// minScoringRepeat and maxScoringRepeat bound how often a scoring
	// runs again.
	minScoringRepeat = 3600
	maxScoringRepeat = 30 * 24 * 3600
	// scoringSample is how many result rows GetScoring returns.
	scoringSample = 10
)

// Result columns a scoring adds after the rows' own.
const (
	colAnswer      = "predicted"
	colConfidence  = "confidence"
	colSuggestions = "suggestions"
	colProblem     = "problem"
)

// ScoringPath is where a scoring's results are downloaded, with {id} for
// the scoring's id.
const ScoringPath = "/api/scorings/{id}/results"

func (s *Service) ListScorings(ctx context.Context, req *connect.Request[mimirv1.ListScoringsRequest]) (*connect.Response[mimirv1.ListScoringsResponse], error) {
	p, _ := auth.From(ctx)
	scs, err := s.store.ListScorings(ctx, p.Team(), req.Msg.GetModelId())
	if err != nil {
		return nil, s.internal(ctx, err)
	}
	names, err := s.modelNames(ctx, p)
	if err != nil {
		return nil, s.internal(ctx, err)
	}
	datasets, err := s.datasetNames(ctx, p.Team())
	if err != nil {
		return nil, s.internal(ctx, err)
	}
	res := &mimirv1.ListScoringsResponse{}
	for _, sc := range scs {
		name, ok := names[sc.ModelID]
		if !ok {
			continue // a model outside the caller's access
		}
		res.Scorings = append(res.Scorings, scoringProto(sc, name, datasets[sc.DatasetID]))
	}
	return connect.NewResponse(res), nil
}

func (s *Service) GetScoring(ctx context.Context, req *connect.Request[mimirv1.GetScoringRequest]) (*connect.Response[mimirv1.GetScoringResponse], error) {
	sc, m, err := s.getScoring(ctx, req.Msg.GetId())
	if err != nil {
		return nil, err
	}
	res := &mimirv1.GetScoringResponse{Scoring: s.scoringProto(ctx, sc, m)}
	if f, err := os.Open(s.store.ScoringResultPath(sc.ID)); err == nil {
		defer f.Close()
		r := csv.NewReader(bufio.NewReader(f))
		r.FieldsPerRecord = -1
		r.Read() // the header is the scoring's columns
		for range scoringSample {
			row, err := r.Read()
			if err != nil {
				break
			}
			res.Sample = append(res.Sample, &mimirv1.Row{Values: row})
		}
	}
	return connect.NewResponse(res), nil
}

func (s *Service) CreateScoring(ctx context.Context, req *connect.Request[mimirv1.CreateScoringRequest]) (*connect.Response[mimirv1.CreateScoringResponse], error) {
	p, _ := auth.From(ctx)
	msg := req.Msg
	m, err := s.get(ctx, msg.GetModelId())
	if err != nil {
		return nil, err
	}
	if _, err := scorable(m); err != nil {
		return nil, invalid("%s can't score a table: %v.", m.Name, err)
	}
	sc := store.Scoring{ModelID: m.ID, CreatedBy: p.Actor(), TeamID: p.Team(), RepeatSeconds: msg.GetRepeatSeconds()}
	conn, query, path := msg.GetConnection(), strings.TrimSpace(msg.GetQuery()), strings.TrimSpace(msg.GetPath())
	sources := 0
	for _, set := range []bool{len(msg.GetCsv()) > 0, conn != nil || query != "", msg.GetBucket() != nil, msg.GetDatasetId() != ""} {
		if set {
			sources++
		}
	}
	if sources != 1 {
		return nil, invalid("Give the rows one way: a CSV file, a database query, files in storage or a dataset.")
	}
	// rows are fetched now, to check them, and kept for the first run.
	var rows []byte
	var source string
	switch {
	case len(msg.GetCsv()) > 0:
		if len(msg.GetCsv()) > maxCSVBytes {
			return nil, invalid("That file is too big. Tables up to %d MB work here.", maxCSVBytes>>20)
		}
		rows, source = msg.GetCsv(), "the uploaded file"
	case conn != nil || query != "":
		if !p.Can(auth.DatabaseQuery) {
			return nil, connect.NewError(connect.CodePermissionDenied, errors.New("Only admins can connect a database."))
		}
		r, err := s.fetch(ctx, conn, query)
		if err != nil {
			return nil, err
		}
		rows, source = r.CSV(), conn.GetDriver()+" query"
		sc.Driver, sc.DSN, sc.Query = conn.GetDriver(), strings.TrimSpace(conn.GetDsn()), query
	case msg.GetBucket() != nil:
		if !p.Can(auth.DatabaseQuery) {
			return nil, connect.NewError(connect.CodePermissionDenied, errors.New("Only admins can connect storage."))
		}
		cl, err := openBucket(msg.GetBucket())
		if err != nil {
			return nil, err
		}
		if rows, err = s.readBucket(ctx, cl, path, store.DatasetTable); err != nil {
			return nil, err
		}
		source = cl.Conn().Bucket + "/" + path
		sc.Driver, sc.DSN, sc.Query = s3source.Driver, cl.Conn().JSON(), path
	default:
		d, err := s.getDataset(ctx, msg.GetDatasetId())
		if err != nil {
			return nil, err
		}
		if d.Kind != store.DatasetTable {
			return nil, invalid("%s isn't a table; pick a table dataset.", d.Name)
		}
		if rows, err = s.store.ReadDataset(d.ID); err != nil {
			return nil, s.internal(ctx, err)
		}
		source, sc.DatasetID = d.Name, d.ID
	}
	if sc.RepeatSeconds != 0 {
		if sc.Driver == "" {
			return nil, invalid("Only rows from a database or storage can be scored again on a schedule; an upload or a dataset stays the same.")
		}
		if sc.RepeatSeconds < minScoringRepeat || sc.RepeatSeconds > maxScoringRepeat {
			return nil, invalid("Score again every hour at most often, and at least every 30 days.")
		}
	}
	if err := checkScoringRows(rows, m); err != nil {
		return nil, invalid("%v", err)
	}
	name := strings.TrimSpace(msg.GetName())
	if name == "" {
		name = m.Name + " on " + source
		if r := []rune(name); len(r) > 100 {
			name = string(r[:99]) + "…"
		}
	}
	if sc.Name, err = cleanName("scoring", name); err != nil {
		return nil, err
	}
	// An upload is kept for good; fetched rows only until the first run
	// reads them.
	if sc, err = s.store.CreateScoring(ctx, sc, rows); err != nil {
		return nil, s.internal(ctx, err)
	}
	if sc.RepeatSeconds > 0 {
		if err := s.store.SetScoringRepeat(ctx, sc.ID, sc.RepeatSeconds); err != nil {
			return nil, s.internal(ctx, err)
		}
	}
	s.audit(ctx, p.Actor(), "scoring.created", sc.Name, fmt.Sprintf("%s with %s", source, m.Name))
	s.KickScorings()
	if sc, err = s.store.GetScoring(ctx, sc.ID); err != nil {
		return nil, s.internal(ctx, err)
	}
	return connect.NewResponse(&mimirv1.CreateScoringResponse{Scoring: s.scoringProto(ctx, sc, m)}), nil
}

func (s *Service) RunScoring(ctx context.Context, req *connect.Request[mimirv1.RunScoringRequest]) (*connect.Response[mimirv1.RunScoringResponse], error) {
	sc, m, err := s.getScoring(ctx, req.Msg.GetId())
	if err != nil {
		return nil, err
	}
	ok, err := s.store.RequeueScoring(ctx, sc.ID)
	if err != nil {
		return nil, s.internal(ctx, err)
	}
	if !ok {
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("This scoring is already waiting or running."))
	}
	p, _ := auth.From(ctx)
	s.audit(ctx, p.Actor(), "scoring.run", sc.Name, "")
	s.KickScorings()
	if sc, err = s.store.GetScoring(ctx, sc.ID); err != nil {
		return nil, s.internal(ctx, err)
	}
	return connect.NewResponse(&mimirv1.RunScoringResponse{Scoring: s.scoringProto(ctx, sc, m)}), nil
}

func (s *Service) CancelScoring(ctx context.Context, req *connect.Request[mimirv1.CancelScoringRequest]) (*connect.Response[mimirv1.CancelScoringResponse], error) {
	sc, m, err := s.getScoring(ctx, req.Msg.GetId())
	if err != nil {
		return nil, err
	}
	ok, err := s.store.CancelScoring(ctx, sc.ID)
	if err != nil {
		return nil, s.internal(ctx, err)
	}
	if !ok {
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("This scoring isn't waiting or running."))
	}
	s.scorer.cancel(sc.ID)
	p, _ := auth.From(ctx)
	s.audit(ctx, p.Actor(), "scoring.cancelled", sc.Name, "")
	if sc, err = s.store.GetScoring(ctx, sc.ID); err != nil {
		return nil, s.internal(ctx, err)
	}
	return connect.NewResponse(&mimirv1.CancelScoringResponse{Scoring: s.scoringProto(ctx, sc, m)}), nil
}

func (s *Service) SetScoringRepeat(ctx context.Context, req *connect.Request[mimirv1.SetScoringRepeatRequest]) (*connect.Response[mimirv1.SetScoringRepeatResponse], error) {
	sc, m, err := s.getScoring(ctx, req.Msg.GetId())
	if err != nil {
		return nil, err
	}
	seconds := req.Msg.GetRepeatSeconds()
	if seconds != 0 {
		if sc.Driver == "" {
			return nil, invalid("Only rows from a database or storage can be scored again on a schedule.")
		}
		if seconds < minScoringRepeat || seconds > maxScoringRepeat {
			return nil, invalid("Score again every hour at most often, and at least every 30 days.")
		}
	}
	if err := s.store.SetScoringRepeat(ctx, sc.ID, seconds); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil, noScoring()
		}
		return nil, s.internal(ctx, err)
	}
	detail := "stopped"
	if seconds > 0 {
		detail = fmt.Sprintf("every %s", time.Duration(seconds)*time.Second)
	}
	p, _ := auth.From(ctx)
	s.audit(ctx, p.Actor(), "scoring.repeat", sc.Name, detail)
	if sc, err = s.store.GetScoring(ctx, sc.ID); err != nil {
		return nil, s.internal(ctx, err)
	}
	return connect.NewResponse(&mimirv1.SetScoringRepeatResponse{Scoring: s.scoringProto(ctx, sc, m)}), nil
}

func (s *Service) DeleteScoring(ctx context.Context, req *connect.Request[mimirv1.DeleteScoringRequest]) (*connect.Response[mimirv1.DeleteScoringResponse], error) {
	sc, _, err := s.getScoring(ctx, req.Msg.GetId())
	if err != nil {
		return nil, err
	}
	if err := s.store.DeleteScoring(ctx, sc.ID); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("Cancel this scoring before deleting it."))
		}
		return nil, s.internal(ctx, err)
	}
	p, _ := auth.From(ctx)
	s.audit(ctx, p.Actor(), "scoring.deleted", sc.Name, "")
	return connect.NewResponse(&mimirv1.DeleteScoringResponse{}), nil
}

// ScoringResultsHandler serves GET ScoringPath: the latest results of a
// scoring the caller may see, as a CSV file.
func (s *Service) ScoringResultsHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx, err := auth.Check(r.Context(), s.store, s.MustEnroll, mimirv1connect.ScoringServiceGetScoringProcedure, r.Header, r.RemoteAddr)
		if err != nil {
			uploadError(w, err)
			return
		}
		sc, _, err := s.getScoring(ctx, r.PathValue("id"))
		if err != nil {
			uploadError(w, err)
			return
		}
		f, err := os.Open(s.store.ScoringResultPath(sc.ID))
		if err != nil {
			uploadError(w, connect.NewError(connect.CodeNotFound, errors.New("This scoring has no results yet.")))
			return
		}
		defer f.Close()
		st, err := f.Stat()
		if err != nil {
			uploadError(w, s.internal(ctx, err))
			return
		}
		w.Header().Set("Content-Type", "text/csv; charset=utf-8")
		w.Header().Set("Content-Disposition", `attachment; filename="`+downloadName(sc.Name)+`.csv"`)
		w.Header().Set("Cache-Control", "no-store")
		http.ServeContent(w, r, "", st.ModTime(), f)
	})
}

// downloadName turns a scoring's name into a safe file name.
func downloadName(name string) string {
	var b strings.Builder
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			b.WriteRune(r)
		case r == ' ' || r == '.':
			b.WriteByte('-')
		}
	}
	if b.Len() == 0 {
		return "scoring"
	}
	return b.String()
}

// scorable says why model m can't answer a table's rows; it returns the
// architecture when it can.
func scorable(m store.Model) (arch.Architecture, error) {
	a, ok := arch.Lookup(m.Architecture)
	if !ok {
		return a, errors.New("its type isn't available on this server anymore")
	}
	if a.Input != arch.Table || a.Fixed() {
		return a, errors.New("it doesn't read table rows")
	}
	switch a.Answers() {
	case arch.Class, arch.Number, arch.Ranked:
		return a, nil
	}
	return a, errors.New("it doesn't answer with a label or a number")
}

// checkScoringRows checks that rows is a table with a column model m
// reads.
func checkScoringRows(rows []byte, m store.Model) error {
	r := csv.NewReader(bytes.NewReader(rows))
	r.FieldsPerRecord, r.LazyQuotes = -1, true
	header, err := r.Read()
	if err != nil {
		return errors.New("The rows can't be read: send a CSV with a header row.")
	}
	for i := range header {
		header[i] = cleanHeader(header[i])
	}
	if !slices.ContainsFunc(header, func(h string) bool { return reads(m, h) }) {
		return fmt.Errorf("None of these columns is one %s reads. It reads %s.", m.Name, strings.Join(modelColumns(m, 8), ", "))
	}
	return nil
}

// modelColumns names up to n of the columns model m reads.
func modelColumns(m store.Model, n int) []string {
	var names []string
	for _, c := range m.Columns {
		names = append(names, c.Name)
	}
	if len(names) == 0 {
		names = m.Features
	}
	if len(names) > n {
		names = append(names[:n:n], "…")
	}
	return names
}

func cleanHeader(h string) string { return strings.TrimSpace(strings.TrimPrefix(h, "\ufeff")) }

func noScoring() error { return connect.NewError(connect.CodeNotFound, errors.New("No such scoring.")) }

// getScoring returns a scoring and its model, when the caller may see
// both; others read as missing.
func (s *Service) getScoring(ctx context.Context, id string) (store.Scoring, store.Model, error) {
	if !store.ValidID(id) {
		return store.Scoring{}, store.Model{}, noScoring()
	}
	sc, err := s.store.GetScoring(ctx, id)
	if errors.Is(err, store.ErrNotFound) {
		return store.Scoring{}, store.Model{}, noScoring()
	}
	if err != nil {
		return store.Scoring{}, store.Model{}, s.internal(ctx, err)
	}
	if p, _ := auth.From(ctx); sc.TeamID != p.Team() {
		return store.Scoring{}, store.Model{}, noScoring()
	}
	m, err := s.get(ctx, sc.ModelID)
	if err != nil {
		return store.Scoring{}, store.Model{}, noScoring()
	}
	return sc, m, nil
}

// modelNames maps the ids of the models the caller may see to their names.
func (s *Service) modelNames(ctx context.Context, p auth.Principal) (map[string]string, error) {
	ms, err := s.store.List(ctx, p.Team())
	if err != nil {
		return nil, err
	}
	names := map[string]string{}
	for _, m := range ms {
		if p.Access.Allows(m.ID) {
			names[m.ID] = m.Name
		}
	}
	return names, nil
}

func (s *Service) scoringProto(ctx context.Context, sc store.Scoring, m store.Model) *mimirv1.Scoring {
	dataset := ""
	if sc.DatasetID != "" {
		if d, err := s.store.GetDataset(ctx, sc.DatasetID); err == nil {
			dataset = d.Name
		}
	}
	return scoringProto(sc, m.Name, dataset)
}

func scoringProto(sc store.Scoring, modelName, datasetName string) *mimirv1.Scoring {
	out := &mimirv1.Scoring{
		Id: sc.ID, Name: sc.Name, ModelId: sc.ModelID, ModelName: modelName, DatasetId: sc.DatasetID,
		Status: mimirv1.JobStatus(sc.Status), Error: sc.Error, Rows: sc.Rows, Done: sc.Done, Failed: sc.Failed,
		Columns: sc.Columns, ResultBytes: sc.ResultBytes, CreatedBy: sc.CreatedBy, CreateTime: timestamppb.New(sc.Created),
		RepeatSeconds: sc.RepeatSeconds, Runs: int32(sc.Runs), ModelVersionId: sc.VersionID,
	}
	switch {
	case sc.Driver == s3source.Driver:
		out.Source, out.SourceDetail = "storage", sc.Query
		if c, err := s3source.Parse(sc.DSN); err == nil {
			out.SourceDetail = c.Bucket + "/" + sc.Query
		}
	case sc.Driver != "":
		out.Source, out.SourceDetail = "database", sc.Driver+": "+sc.Query
	case sc.DatasetID != "":
		out.Source, out.SourceDetail = "dataset", datasetName
		if datasetName == "" {
			out.SourceDetail = "a deleted dataset"
		}
	default:
		out.Source, out.SourceDetail = "upload", "an uploaded file"
	}
	if !sc.Started.IsZero() {
		out.StartTime = timestamppb.New(sc.Started)
	}
	if !sc.Ended.IsZero() {
		out.EndTime = timestamppb.New(sc.Ended)
	}
	if sc.RepeatSeconds > 0 && !sc.NextRun.IsZero() {
		out.NextRunTime = timestamppb.New(sc.NextRun)
	}
	return out
}

func scoringEvent(sc store.Scoring, modelName string) map[string]any {
	return map[string]any{
		"id": sc.ID, "name": sc.Name, "model_id": sc.ModelID, "model": modelName, "status": sc.Status.String(),
		"rows": sc.Rows, "done": sc.Done, "failed": sc.Failed, "error": sc.Error,
	}
}

// scorer runs queued scorings in the background, a few at a time.
type scorer struct {
	kick    chan struct{}
	mu      sync.Mutex
	running map[string]context.CancelFunc
}

func (sr *scorer) init() {
	sr.mu.Lock()
	defer sr.mu.Unlock()
	if sr.kick == nil {
		sr.kick = make(chan struct{}, 1)
		sr.running = map[string]context.CancelFunc{}
	}
}

// cancel stops a scoring this process is running, if it is.
func (sr *scorer) cancel(id string) {
	sr.mu.Lock()
	defer sr.mu.Unlock()
	if c, ok := sr.running[id]; ok {
		c()
	}
}

// KickScorings asks the scoring runner to look at the queue now.
func (s *Service) KickScorings() {
	s.scorer.init()
	select {
	case s.scorer.kick <- struct{}{}:
	default:
	}
}

// RunScorings runs queued scorings, ScoringsAtOnce at a time, and queues
// repeating ones when they are due, until ctx ends. It waits for the
// scorings it started; one cut short goes back in the queue at the next
// start.
func (s *Service) RunScorings(ctx context.Context, every time.Duration) {
	s.scorer.init()
	var wg sync.WaitGroup
	defer wg.Wait()
	t := time.NewTicker(every)
	defer t.Stop()
	slots := make(chan struct{}, ScoringsAtOnce)
	for {
		if _, err := s.store.QueueDueScorings(ctx, time.Now()); err != nil && ctx.Err() == nil {
			s.log.ErrorContext(ctx, "scoring: queue due", "error", err)
		}
	claim:
		for {
			select {
			case slots <- struct{}{}:
			default:
				break claim
			}
			s.scorer.mu.Lock()
			skip := make([]string, 0, len(s.scorer.running))
			for id := range s.scorer.running {
				skip = append(skip, id)
			}
			s.scorer.mu.Unlock()
			sc, err := s.store.ClaimScoring(ctx, skip...)
			if err != nil {
				<-slots
				if !errors.Is(err, store.ErrNotFound) && ctx.Err() == nil {
					s.log.ErrorContext(ctx, "scoring: claim", "error", err)
				}
				break
			}
			rctx, cancel := context.WithCancel(ctx)
			s.scorer.mu.Lock()
			s.scorer.running[sc.ID] = cancel
			s.scorer.mu.Unlock()
			wg.Go(func() {
				defer func() {
					s.scorer.mu.Lock()
					delete(s.scorer.running, sc.ID)
					s.scorer.mu.Unlock()
					cancel()
					<-slots
					s.KickScorings()
				}()
				s.runScoring(rctx, sc)
			})
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		case <-s.scorer.kick:
		}
	}
}

// runScoring runs one claimed scoring to its end and records how it went.
func (s *Service) runScoring(ctx context.Context, sc store.Scoring) {
	start := time.Now()
	res, failure := s.score1(ctx, sc)
	if ctx.Err() != nil && failure != "" {
		// Cancelled, or the server is stopping: a stopping server's
		// scoring is queued again when it starts.
		os.Remove(s.store.ScoringTempPath(sc.ID))
		return
	}
	done := context.WithoutCancel(ctx)
	finished, err := s.store.FinishScoring(done, sc.ID, res, failure)
	if err != nil {
		s.log.ErrorContext(done, "scoring: finish", "scoring_id", sc.ID, "error", err)
		return
	}
	if !finished {
		os.Remove(s.store.ScoringTempPath(sc.ID)) // cancelled meanwhile
		return
	}
	if failure == "" {
		if err := os.Rename(s.store.ScoringTempPath(sc.ID), s.store.ScoringResultPath(sc.ID)); err != nil {
			s.log.ErrorContext(done, "scoring: keep results", "scoring_id", sc.ID, "error", err)
		}
	} else {
		os.Remove(s.store.ScoringTempPath(sc.ID))
	}
	sc, err = s.store.GetScoring(done, sc.ID)
	if err != nil {
		return
	}
	modelName := ""
	if m, err := s.store.Get(done, sc.ModelID); err == nil {
		modelName = m.Name
	}
	switch {
	case failure == "":
		s.log.InfoContext(done, "scoring finished", "scoring_id", sc.ID, "rows", sc.Rows, "failed", sc.Failed, "took", time.Since(start).Round(time.Millisecond))
		s.auditIn(done, sc.TeamID, "server", "scoring.finished", sc.Name, fmt.Sprintf("%d rows, %d not answered", sc.Done, sc.Failed))
		s.emitIn(done, sc.TeamID, "scoring.finished", scoringEvent(sc, modelName))
	default:
		s.log.WarnContext(done, "scoring failed", "scoring_id", sc.ID, "error", failure)
		s.auditIn(done, sc.TeamID, "server", "scoring.failed", sc.Name, failure)
		s.emitIn(done, sc.TeamID, "scoring.failed", scoringEvent(sc, modelName))
		s.Notifier.Notify(done, "Scoring failed: "+sc.Name, failure)
	}
}

// scoringRows reads a scoring's rows: kept from an upload or from when it
// was made, fetched again from its database or storage, or a dataset's.
func (s *Service) scoringRows(ctx context.Context, sc store.Scoring) ([]byte, string) {
	kept := s.store.ScoringInputPath(sc.ID)
	if b, err := os.ReadFile(kept); err == nil {
		if !sc.Upload() {
			os.Remove(kept) // fetched once, for the first run
		}
		return b, ""
	}
	switch {
	case sc.Upload():
		return nil, "The uploaded rows are gone. Make a new scoring with the file."
	case sc.DatasetID != "":
		b, err := s.store.ReadDataset(sc.DatasetID)
		if err != nil {
			return nil, "The dataset these rows came from was deleted."
		}
		return b, ""
	case sc.Driver == s3source.Driver:
		c, err := s3source.Parse(sc.DSN)
		if err != nil {
			return nil, "The storage connection can't be read."
		}
		cl, err := s3source.New(c)
		if err != nil {
			return nil, fmt.Sprintf("Storage can't be reached: %v", strings.Replace(err.Error(), "s3source: bad request: ", "", 1))
		}
		b, err := cl.Table(ctx, sc.Query, maxCSVBytes)
		if err != nil {
			return nil, fmt.Sprintf("The files in storage couldn't be read: %v", strings.Replace(err.Error(), "s3source: bad request: ", "", 1))
		}
		return b, ""
	}
	r, err := dbsource.Fetch(ctx, sc.Driver, sc.DSN, sc.Query, training.MaxRows, s.store.RefuseOwnDatabase)
	if err != nil {
		return nil, fmt.Sprintf("The query didn't give rows: %v", strings.TrimPrefix(err.Error(), "dbsource: bad query: "))
	}
	return r.CSV(), ""
}

// asked is one row's answer, as cells of the result.
type asked struct {
	cells []string
	bad   bool
}

// score1 answers every row of a scoring into its temporary results file.
// failure says why it couldn't, for people.
func (s *Service) score1(ctx context.Context, sc store.Scoring) (store.ScoringResult, string) {
	m, err := s.store.Get(ctx, sc.ModelID)
	if err != nil {
		return store.ScoringResult{}, "The model was deleted."
	}
	a, err := scorable(m)
	if err != nil {
		return store.ScoringResult{}, fmt.Sprintf("%s can't score a table: %v.", m.Name, err)
	}
	rows, failure := s.scoringRows(ctx, sc)
	if failure != "" {
		return store.ScoringResult{}, failure
	}
	total := int64(countRows(rows))
	cr := csv.NewReader(bytes.NewReader(rows))
	cr.FieldsPerRecord, cr.LazyQuotes, cr.ReuseRecord = -1, true, false
	header, err := cr.Read()
	if err != nil {
		return store.ScoringResult{}, "The rows can't be read: they should be a CSV with a header row."
	}
	for i := range header {
		header[i] = cleanHeader(header[i])
	}
	var use []int // the columns the model reads, by index
	for i, h := range header {
		if reads(m, h) && !slices.Contains(header[:i], h) {
			use = append(use, i)
		}
	}
	if len(use) == 0 {
		return store.ScoringResult{}, fmt.Sprintf("None of the columns is one %s reads. It reads %s.", m.Name, strings.Join(modelColumns(m, 8), ", "))
	}
	spec := arch.Resolve(a, arch.Spec{InputShape: m.InputShape, Labels: m.Labels})
	added := []string{colAnswer}
	switch a.Answers() {
	case arch.Class:
		added = append(added, colConfidence)
	case arch.Ranked:
		added = append(added, colSuggestions)
	}
	added = append(added, colProblem)
	columns := slices.Clone(header)
	for _, c := range added {
		columns = append(columns, unique(columns, c))
	}

	if err := os.MkdirAll(s.store.ScoringDir(), 0o700); err != nil {
		return store.ScoringResult{}, "The results couldn't be written."
	}
	f, err := os.Create(s.store.ScoringTempPath(sc.ID))
	if err != nil {
		s.log.ErrorContext(ctx, "scoring: results file", "error", err)
		return store.ScoringResult{}, "The results couldn't be written."
	}
	defer f.Close()
	bw := bufio.NewWriterSize(f, 1<<16)
	cw := csv.NewWriter(bw)
	cw.Write(columns)

	load := func() ([]byte, error) { return s.store.Weights(m.ID) }
	timeout := s.PredictTimeout
	if timeout <= 0 {
		timeout = DefaultPredictTimeout
	}
	ask1 := func(row []string) asked {
		fields := make(map[string]string, len(use))
		for _, i := range use {
			if i < len(row) {
				fields[header[i]] = row[i]
			}
		}
		input, err := ask.Encode(a, m.Columns, m.Features, fields, nil, nil)
		if err != nil {
			return problem(len(added), ask.Reason(err))
		}
		actx, cancel := context.WithTimeout(ctx, timeout)
		defer cancel()
		out, err := s.answer(actx, m, a, spec, load, input)
		switch {
		case err != nil && ctx.Err() != nil:
			return problem(len(added), "stopped")
		case err != nil && actx.Err() != nil:
			return problem(len(added), fmt.Sprintf("took longer than %v", timeout))
		case err != nil:
			return problem(len(added), fmt.Sprintf("the model couldn't answer: %v", err))
		}
		switch a.Answers() {
		case arch.Number:
			if len(out.Output) == 0 {
				return problem(len(added), "the model gave no answer")
			}
			return asked{cells: []string{strconv.FormatFloat(float64(out.Output[0]), 'g', 6, 64), ""}}
		case arch.Ranked:
			k := min(len(spec.Labels), 10)
			if a.Items > 0 {
				k = min(a.Items, maxTopK)
			}
			top := out.Top(spec.Labels, k)
			if len(top) == 0 {
				return problem(len(added), "the model gave no answer")
			}
			labels := make([]string, len(top))
			for i, g := range top {
				labels[i] = g.Label
			}
			return asked{cells: []string{top[0].Label, strings.Join(labels, "; "), ""}}
		}
		top := out.Top(spec.Labels, 1)
		if len(top) == 0 {
			return problem(len(added), "the model gave no answer")
		}
		return asked{cells: []string{top[0].Label, strconv.FormatFloat(float64(top[0].Probability), 'f', 4, 32), ""}}
	}

	var read, done, failed int64
	last := time.Now()
	chunk := make([][]string, 0, scoringChunk)
	answers := make([]asked, scoringChunk)
	flush := func() string {
		next := make(chan int)
		var wg sync.WaitGroup
		for range min(scoringAsk, len(chunk)) {
			wg.Go(func() {
				for i := range next {
					answers[i] = ask1(chunk[i])
				}
			})
		}
		for i := range chunk {
			next <- i
		}
		close(next)
		wg.Wait()
		if ctx.Err() != nil {
			return "stopped"
		}
		for i, row := range chunk {
			out := make([]string, 0, len(columns))
			out = append(out, row...)
			for len(out) < len(header) {
				out = append(out, "")
			}
			out = append(out[:len(header)], answers[i].cells...)
			cw.Write(out)
			done++
			if answers[i].bad {
				failed++
			}
		}
		chunk = chunk[:0]
		if time.Since(last) >= scoringCheck {
			last = time.Now()
			if st, err := s.store.ScoringStatus(ctx, sc.ID); err == nil && st != store.JobRunning {
				return "stopped" // cancelled on another server
			}
			if err := s.store.ScoringProgress(ctx, sc.ID, total, done, failed); err != nil && ctx.Err() == nil {
				s.log.WarnContext(ctx, "scoring: progress", "scoring_id", sc.ID, "error", err)
			}
		}
		return ""
	}
	for {
		row, err := cr.Read()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			var pe *csv.ParseError
			if errors.As(err, &pe) {
				continue // a broken line; the rest of the file still reads
			}
			return store.ScoringResult{}, "The rows can't be read."
		}
		read++
		chunk = append(chunk, row)
		if len(chunk) == scoringChunk {
			if f := flush(); f != "" {
				return store.ScoringResult{}, f
			}
		}
	}
	if len(chunk) > 0 {
		if f := flush(); f != "" {
			return store.ScoringResult{}, f
		}
	}
	cw.Flush()
	if err := cw.Error(); err != nil {
		return store.ScoringResult{}, "The results couldn't be written."
	}
	if err := bw.Flush(); err != nil {
		return store.ScoringResult{}, "The results couldn't be written."
	}
	size, _ := f.Seek(0, io.SeekCurrent)
	if err := f.Sync(); err != nil {
		return store.ScoringResult{}, "The results couldn't be written."
	}
	if read == 0 {
		return store.ScoringResult{}, "There were no rows to score."
	}
	return store.ScoringResult{Rows: read, Done: done, Failed: failed, Columns: columns, Bytes: size, VersionID: m.VersionID}, ""
}

// problem is a row that couldn't be answered, with why in the last of
// its n cells.
func problem(n int, why string) asked {
	cells := make([]string, n)
	cells[n-1] = why
	return asked{cells: cells, bad: true}
}

// unique returns name, or name with a number after it when columns
// already has it.
func unique(columns []string, name string) string {
	if !slices.Contains(columns, name) {
		return name
	}
	for i := 2; ; i++ {
		if n := fmt.Sprintf("%s_%d", name, i); !slices.Contains(columns, n) {
			return n
		}
	}
}
