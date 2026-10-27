package api

import (
	"bytes"
	"cmp"
	"context"
	"encoding/csv"
	"errors"
	"fmt"
	"hash/fnv"
	"io"
	"slices"
	"strings"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"

	mimirv1 "github.com/gsoultan/mimir-server/gen/mimir/v1"
	"github.com/gsoultan/mimir-server/internal/arch"
	"github.com/gsoultan/mimir-server/internal/ask"
	"github.com/gsoultan/mimir-server/internal/auth"
	"github.com/gsoultan/mimir-server/internal/runtime"
	"github.com/gsoultan/mimir-server/internal/schema"
	"github.com/gsoultan/mimir-server/internal/store"
	"github.com/gsoultan/mimir-server/internal/training"
)

const (
	// defaultShare is the part of a dataset's rows a benchmark keeps
	// aside when the request doesn't say.
	defaultShare = 0.2
	// scoreTimeout bounds scoring one model on one benchmark.
	scoreTimeout = 10 * time.Minute
)

// scoreSlots bounds how many models are scored at once, so scoring
// doesn't crowd out predictions.
var scoreSlots = make(chan struct{}, 2)

func (s *Service) ListBenchmarks(ctx context.Context, req *connect.Request[mimirv1.ListBenchmarksRequest]) (*connect.Response[mimirv1.ListBenchmarksResponse], error) {
	p, _ := auth.From(ctx)
	bs, err := s.store.ListBenchmarks(ctx, p.Team())
	if err != nil {
		return nil, s.internal(ctx, err)
	}
	names, err := s.datasetNames(ctx, p.Team())
	if err != nil {
		return nil, s.internal(ctx, err)
	}
	res := &mimirv1.ListBenchmarksResponse{}
	for _, b := range bs {
		if d := req.Msg.GetDatasetId(); d != "" && b.DatasetID != d {
			continue
		}
		scores, err := s.store.BenchmarkScores(ctx, b.ID)
		if err != nil {
			return nil, s.internal(ctx, err)
		}
		bp := benchmarkProto(b, names[b.DatasetID], scores)
		if top := leader(b, scores); top != nil {
			if m, err := s.store.Get(ctx, top.ModelID); err == nil {
				bp.LeaderName = m.Name
			}
		}
		res.Benchmarks = append(res.Benchmarks, bp)
	}
	return connect.NewResponse(res), nil
}

func (s *Service) GetBenchmark(ctx context.Context, req *connect.Request[mimirv1.GetBenchmarkRequest]) (*connect.Response[mimirv1.GetBenchmarkResponse], error) {
	b, err := s.getBenchmark(ctx, req.Msg.GetId())
	if err != nil {
		return nil, err
	}
	p, _ := auth.From(ctx)
	scores, err := s.store.BenchmarkScores(ctx, b.ID)
	if err != nil {
		return nil, s.internal(ctx, err)
	}
	ms, err := s.store.List(ctx, p.Team())
	if err != nil {
		return nil, s.internal(ctx, err)
	}
	models := map[string]store.Model{}
	for _, m := range ms {
		if p.Access.Allows(m.ID) {
			models[m.ID] = m
		}
	}
	names, err := s.datasetNames(ctx, p.Team())
	if err != nil {
		return nil, s.internal(ctx, err)
	}
	res := &mimirv1.GetBenchmarkResponse{Benchmark: benchmarkProto(b, names[b.DatasetID], scores)}
	scored := map[string]bool{}
	rank := 0
	for _, sc := range ranked(b, scores) {
		scored[sc.ModelID] = true
		m, ok := models[sc.ModelID]
		if !ok {
			continue
		}
		sp := scoreProto(sc, m)
		if sc.Status == store.JobSucceeded {
			rank++
			sp.Rank = int32(rank)
		}
		res.Scores = append(res.Scores, sp)
	}
	if len(res.Scores) > 0 && res.Scores[0].Rank == 1 {
		res.Benchmark.LeaderName = res.Scores[0].ModelName
	}
	header, err := s.benchmarkHeader(b)
	if err != nil {
		return nil, s.internal(ctx, err)
	}
	for _, m := range ms {
		if _, ok := models[m.ID]; ok && !scored[m.ID] && fits(b, header, m) == nil {
			res.Candidates = append(res.Candidates, &mimirv1.Candidate{ModelId: m.ID, ModelName: m.Name, Architecture: m.Architecture})
		}
	}
	return connect.NewResponse(res), nil
}

func (s *Service) CreateBenchmark(ctx context.Context, req *connect.Request[mimirv1.CreateBenchmarkRequest]) (*connect.Response[mimirv1.CreateBenchmarkResponse], error) {
	msg := req.Msg
	name, desc, err := cleanText("benchmark", msg.GetName(), msg.GetDescription())
	if err != nil {
		return nil, err
	}
	d, err := s.getDataset(ctx, msg.GetDatasetId())
	if err != nil {
		return nil, err
	}
	if d.Kind != store.DatasetTable {
		return nil, invalid("Benchmarks are made from a table dataset for now; %s isn't one.", d.Name)
	}
	table, err := s.store.ReadDataset(d.ID)
	if err != nil {
		return nil, s.internal(ctx, err)
	}
	var rows []byte
	var n int
	if up := msg.GetCsv(); len(up) > 0 {
		if rows, n, err = testRows(up, table, d.Target); err != nil {
			return nil, invalid("%v", err)
		}
	} else {
		share := msg.GetShare()
		switch {
		case share == 0:
			share = defaultShare
		case share < 0.05 || share > 0.5:
			return nil, invalid("Keep between 5%% and 50%% of the rows aside.")
		}
		h := fnv.New64a()
		io.WriteString(h, d.ID+name)
		if rows, n, err = training.Freeze(table, share, h.Sum64(), training.MaxBenchmarkRows); err != nil {
			return nil, invalid("%s", strings.TrimPrefix(err.Error(), training.ErrBadTable.Error()+": "))
		}
	}
	kind := "class"
	if d.NumberTarget {
		kind = "number"
	}
	target := d.Target
	if target == "" {
		// A table without a named answer column answers with its last.
		if h, err := csv.NewReader(bytes.NewReader(rows)).Read(); err == nil && len(h) > 0 {
			target = strings.TrimSpace(strings.TrimPrefix(h[len(h)-1], "\ufeff"))
		}
	}
	p, _ := auth.From(ctx)
	b, err := s.store.CreateBenchmark(ctx, store.Benchmark{Name: name, Description: desc, DatasetID: d.ID, Target: target, Kind: kind, Rows: int64(n), CreatedBy: p.Actor(), TeamID: p.Team()}, rows)
	if err != nil {
		return nil, s.internal(ctx, err)
	}
	s.audit(ctx, p.Actor(), "benchmark.created", b.Name, fmt.Sprintf("%d rows from %s", n, d.Name))
	return connect.NewResponse(&mimirv1.CreateBenchmarkResponse{Benchmark: benchmarkProto(b, d.Name, nil)}), nil
}

func (s *Service) DeleteBenchmark(ctx context.Context, req *connect.Request[mimirv1.DeleteBenchmarkRequest]) (*connect.Response[mimirv1.DeleteBenchmarkResponse], error) {
	b, err := s.getBenchmark(ctx, req.Msg.GetId())
	if err != nil {
		return nil, err
	}
	if err := s.store.DeleteBenchmark(ctx, b.ID); err != nil && !errors.Is(err, store.ErrNotFound) {
		return nil, s.internal(ctx, err)
	}
	p, _ := auth.From(ctx)
	s.audit(ctx, p.Actor(), "benchmark.deleted", b.Name, "")
	return connect.NewResponse(&mimirv1.DeleteBenchmarkResponse{}), nil
}

func (s *Service) ScoreModels(ctx context.Context, req *connect.Request[mimirv1.ScoreModelsRequest]) (*connect.Response[mimirv1.ScoreModelsResponse], error) {
	b, err := s.getBenchmark(ctx, req.Msg.GetBenchmarkId())
	if err != nil {
		return nil, err
	}
	if len(req.Msg.GetModelIds()) == 0 {
		return nil, invalid("Pick at least one model to score.")
	}
	header, err := s.benchmarkHeader(b)
	if err != nil {
		return nil, s.internal(ctx, err)
	}
	var ms []store.Model
	for _, id := range req.Msg.GetModelIds() {
		m, err := s.get(ctx, id)
		if err != nil {
			return nil, err
		}
		if err := fits(b, header, m); err != nil {
			return nil, invalid("%s can't take this test: %v", m.Name, err)
		}
		if !slices.ContainsFunc(ms, func(o store.Model) bool { return o.ID == m.ID }) {
			ms = append(ms, m)
		}
	}
	res := &mimirv1.ScoreModelsResponse{}
	for _, m := range ms {
		id, err := s.store.QueueScore(ctx, b.ID, m.ID, m.SHA256)
		if err != nil {
			return nil, s.internal(ctx, err)
		}
		s.scoreLater(id, b, m)
		res.Scores = append(res.Scores, scoreProto(store.BenchmarkScore{ModelID: m.ID, Status: store.JobQueued, WeightsSHA: m.SHA256}, m))
	}
	return connect.NewResponse(res), nil
}

// ScoreOnBenchmarks scores a model a run just made on the benchmarks
// frozen from the dataset it learned from, so the leaderboard keeps up.
func (s *Service) ScoreOnBenchmarks(ctx context.Context, j store.Job) {
	if j.ModelID == "" {
		return
	}
	bs, err := s.store.DatasetBenchmarks(ctx, j.DatasetID)
	if err != nil || len(bs) == 0 {
		return
	}
	m, err := s.store.Get(ctx, j.ModelID)
	if err != nil {
		return
	}
	for _, b := range bs {
		if b.TeamID != m.TeamID {
			continue
		}
		header, err := s.benchmarkHeader(b)
		if err != nil || fits(b, header, m) != nil {
			continue
		}
		id, err := s.store.QueueScore(ctx, b.ID, m.ID, m.SHA256)
		if err != nil {
			s.log.WarnContext(ctx, "benchmark: couldn't queue a score", "benchmark_id", b.ID, "model_id", m.ID, "error", err)
			continue
		}
		s.scoreLater(id, b, m)
	}
}

// scoreLater scores a model on a benchmark in the background, when a
// slot is free.
func (s *Service) scoreLater(id string, b store.Benchmark, m store.Model) {
	go func() {
		scoreSlots <- struct{}{}
		defer func() { <-scoreSlots }()
		ctx, cancel := context.WithTimeout(context.Background(), scoreTimeout)
		defer cancel()
		if err := s.store.SetScoreRunning(ctx, id); err != nil {
			return // deleted meanwhile
		}
		r, failure := s.score(ctx, b, m)
		headline := 0.0
		if r != nil {
			headline = headlineOf(b, r)
		}
		if err := s.store.FinishScore(context.WithoutCancel(ctx), id, r, headline, failure); err != nil && !errors.Is(err, store.ErrNotFound) {
			s.log.Warn("benchmark: couldn't store a score", "benchmark_id", b.ID, "model_id", m.ID, "error", err)
		}
	}()
}

// score asks a model each of a benchmark's examples and sums up how it
// did; failure says why it couldn't, for people.
func (s *Service) score(ctx context.Context, b store.Benchmark, m store.Model) (*store.Report, string) {
	a, ok := arch.Lookup(m.Architecture)
	if !ok {
		return nil, fmt.Sprintf("This model's type (%s) isn't available on this server anymore.", m.Architecture)
	}
	table, err := s.store.ReadBenchmark(b.ID)
	if err != nil {
		return nil, "The benchmark's rows can't be read."
	}
	cr := csv.NewReader(bytes.NewReader(table))
	cr.FieldsPerRecord = -1
	cr.TrimLeadingSpace = true
	header, err := cr.Read()
	if err != nil {
		return nil, "The benchmark's rows can't be read."
	}
	ti := slices.Index(header, b.Target)
	if ti < 0 {
		return nil, fmt.Sprintf("The benchmark has no %q column.", b.Target)
	}
	spec := arch.Resolve(a, arch.Spec{InputShape: m.InputShape, Labels: m.Labels})
	tally, err := training.NewTally(a, spec.Labels)
	if err != nil {
		return nil, err.Error()
	}
	load := func() ([]byte, error) { return s.store.Weights(m.ID) }
	fields := map[string]string{}
	for {
		row, err := cr.Read()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil || ti >= len(row) {
			continue
		}
		real := strings.TrimSpace(row[ti])
		if real == "" {
			continue
		}
		clear(fields)
		for i, h := range header {
			if i != ti && i < len(row) && reads(m, h) {
				fields[h] = row[i]
			}
		}
		input, err := ask.Encode(a, m.Columns, m.Features, fields, nil, nil)
		if err != nil {
			return nil, ask.Reason(err)
		}
		out, err := s.answer(ctx, m, a, spec, load, input)
		if err != nil {
			if ctx.Err() != nil {
				return nil, fmt.Sprintf("Scoring took longer than %v and was stopped.", scoreTimeout)
			}
			return nil, fmt.Sprintf("The model couldn't answer: %v", err)
		}
		if b.Kind == "number" {
			v, ok := schema.ParseNumber(real)
			if !ok || len(out.Output) == 0 {
				continue
			}
			err = tally.Number(ctx, float64(out.Output[0]), v)
		} else {
			err = tally.Label(ctx, out.Output, real)
		}
		if err != nil {
			return nil, err.Error()
		}
	}
	r := tally.Report()
	if r.Examples == 0 {
		return nil, "None of the benchmark's rows has an answer to score."
	}
	r.SizeBytes = m.SizeBytes
	return r, ""
}

// answer runs a model on one input, on a worker or here, as Predict does.
func (s *Service) answer(ctx context.Context, m store.Model, a arch.Architecture, spec arch.Spec, load func() ([]byte, error), input []float32) (runtime.Result, error) {
	pin, pinned := m.PinnedWorker()
	if s.Pool != nil && (!s.Local || pinned || a.Custom) {
		r, _, ran, err := s.Pool.Predict(ctx, m.ID, m.WeightsVersion(), pin, a, spec, load, input)
		if ran {
			return r, err
		}
	}
	if pinned || a.Custom || !s.Local {
		return runtime.Result{}, errors.New("no worker that can run it is connected")
	}
	return s.cache.Predict(ctx, m.ID, m.WeightsVersion(), a, spec, load, input)
}

// reads reports whether model m reads column name.
func reads(m store.Model, name string) bool {
	if len(m.Columns) > 0 {
		return slices.ContainsFunc(m.Columns, func(c schema.Column) bool { return c.Name == name })
	}
	return slices.Contains(m.Features, name)
}

// fits says why model m can't take benchmark b, whose columns are
// header; nil when it can.
func fits(b store.Benchmark, header []string, m store.Model) error {
	a, ok := arch.Lookup(m.Architecture)
	if !ok {
		return errors.New("its type isn't available on this server")
	}
	if a.Input != arch.Table || a.Fixed() {
		return errors.New("it doesn't read table rows")
	}
	switch out := a.Answers(); {
	case b.Kind == "number" && out != arch.Number:
		return errors.New("it picks a label, and this test wants a number")
	case b.Kind != "number" && out != arch.Class && out != arch.Ranked:
		return errors.New("it answers with a number, and this test wants a label")
	}
	cols := m.Features
	if len(m.Columns) > 0 {
		cols = nil
		for _, c := range m.Columns {
			cols = append(cols, c.Name)
		}
	}
	if len(cols) == 0 {
		return errors.New("it wasn't taught from a table")
	}
	for _, c := range cols {
		if !slices.Contains(header, c) || c == b.Target {
			return fmt.Errorf("the test has no %q column", c)
		}
	}
	return nil
}

// benchmarkHeader is a benchmark's column names.
func (s *Service) benchmarkHeader(b store.Benchmark) ([]string, error) {
	table, err := s.store.ReadBenchmark(b.ID)
	if err != nil {
		return nil, err
	}
	cr := csv.NewReader(bytes.NewReader(table))
	cr.TrimLeadingSpace = true
	header, err := cr.Read()
	if err != nil {
		return nil, err
	}
	for i := range header {
		header[i] = strings.TrimSpace(strings.TrimPrefix(header[i], "\ufeff"))
	}
	return header, nil
}

// testRows checks uploaded test rows against the dataset they test: the
// same columns, with the answer column, and at most the row limit.
func testRows(up, table []byte, target string) ([]byte, int, error) {
	read := func(b []byte) ([]string, *csv.Reader, error) {
		cr := csv.NewReader(bytes.NewReader(b))
		cr.FieldsPerRecord = -1
		cr.TrimLeadingSpace = true
		h, err := cr.Read()
		for i := range h {
			h[i] = strings.TrimSpace(strings.TrimPrefix(h[i], "\ufeff"))
		}
		return h, cr, err
	}
	want, _, err := read(table)
	if err != nil {
		return nil, 0, errors.New("the dataset can't be read")
	}
	got, cr, err := read(up)
	if err != nil {
		return nil, 0, errors.New("the test rows aren't a CSV with a header")
	}
	if target == "" && len(want) > 0 {
		target = want[len(want)-1]
	}
	if !slices.Contains(got, target) {
		return nil, 0, fmt.Errorf("the test rows need the answer column %q", target)
	}
	for _, c := range want {
		if !slices.Contains(got, c) {
			return nil, 0, fmt.Errorf("the test rows need the dataset's %q column", c)
		}
	}
	n := 0
	for {
		_, err := cr.Read()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, 0, fmt.Errorf("the test rows can't be read: %v", err)
		}
		n++
	}
	switch {
	case n == 0:
		return nil, 0, errors.New("the test has no rows")
	case n > training.MaxBenchmarkRows:
		return nil, 0, fmt.Errorf("a test holds at most %d rows; this has %d", training.MaxBenchmarkRows, n)
	}
	return up, n, nil
}

// metric names a benchmark's headline score: a label test is scored on
// the balanced score (macro F1) when one answer is rare, since a model
// that never says it still gets most right.
func metric(b store.Benchmark, r *store.Report) (string, bool) {
	switch {
	case b.Kind == "number":
		return "average miss (MAE)", true
	case r != nil && rare(r):
		return "balanced score (macro F1)", false
	}
	return "right answers", false
}

func rare(r *store.Report) bool {
	var total int64
	for _, c := range r.Classes {
		total += c.Support
	}
	if total == 0 || len(r.Classes) < 2 {
		return false
	}
	return slices.ContainsFunc(r.Classes, func(c store.ClassScore) bool { return float64(c.Support)/float64(total) < 0.2 })
}

func headlineOf(b store.Benchmark, r *store.Report) float64 {
	switch name, _ := metric(b, r); {
	case b.Kind == "number":
		return r.MAE
	case strings.HasPrefix(name, "balanced"):
		return r.MacroF1
	}
	return r.Accuracy
}

// ranked is a benchmark's scores, best first, then the unfinished in
// the order they were asked for.
func ranked(b store.Benchmark, scores []store.BenchmarkScore) []store.BenchmarkScore {
	out := slices.Clone(scores)
	lower := b.Kind == "number"
	slices.SortStableFunc(out, func(x, y store.BenchmarkScore) int {
		dx, dy := x.Status == store.JobSucceeded, y.Status == store.JobSucceeded
		switch {
		case dx && !dy:
			return -1
		case dy && !dx:
			return 1
		case dx && dy && x.Headline != y.Headline:
			if lower {
				return cmp.Compare(x.Headline, y.Headline)
			}
			return cmp.Compare(y.Headline, x.Headline)
		}
		return cmp.Compare(x.ID, y.ID)
	})
	return out
}

// leader is a benchmark's best finished score, nil when none is.
func leader(b store.Benchmark, scores []store.BenchmarkScore) *store.BenchmarkScore {
	r := ranked(b, scores)
	if len(r) == 0 || r[0].Status != store.JobSucceeded {
		return nil
	}
	return &r[0]
}

func (s *Service) getBenchmark(ctx context.Context, id string) (store.Benchmark, error) {
	missing := connect.NewError(connect.CodeNotFound, errors.New("No such benchmark."))
	if !store.ValidID(id) {
		return store.Benchmark{}, missing
	}
	b, err := s.store.GetBenchmark(ctx, id)
	if errors.Is(err, store.ErrNotFound) {
		return store.Benchmark{}, missing
	}
	if err != nil {
		return store.Benchmark{}, s.internal(ctx, err)
	}
	if p, _ := auth.From(ctx); b.TeamID != p.Team() {
		return store.Benchmark{}, missing
	}
	return b, nil
}

func benchmarkProto(b store.Benchmark, datasetName string, scores []store.BenchmarkScore) *mimirv1.Benchmark {
	var sample *store.Report
	scored := 0
	for _, sc := range scores {
		if sc.Status == store.JobSucceeded {
			scored++
			if sample == nil {
				sample = sc.Report
			}
		}
	}
	name, lower := metric(b, sample)
	bp := &mimirv1.Benchmark{
		Id: b.ID, Name: b.Name, Description: b.Description, DatasetId: b.DatasetID, DatasetName: datasetName,
		Target: b.Target, Kind: b.Kind, Rows: b.Rows, CreatedBy: b.CreatedBy, CreateTime: timestamppb.New(b.Created),
		Metric: name, LowerIsBetter: lower, Scored: int32(scored),
	}
	if top := leader(b, scores); top != nil {
		bp.LeaderHeadline = top.Headline
	}
	return bp
}

func scoreProto(sc store.BenchmarkScore, m store.Model) *mimirv1.BenchmarkScore {
	out := &mimirv1.BenchmarkScore{
		ModelId: m.ID, ModelName: m.Name, Architecture: m.Architecture,
		Status: mimirv1.JobStatus(sc.Status), Error: sc.Error, Report: reportProto(sc.Report),
		Headline: sc.Headline, Stale: sc.Status == store.JobSucceeded && sc.WeightsSHA != m.SHA256,
	}
	if !sc.Scored.IsZero() {
		out.ScoredTime = timestamppb.New(sc.Scored)
	}
	return out
}
