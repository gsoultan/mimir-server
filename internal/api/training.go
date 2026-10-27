package api

import (
	"bytes"
	"context"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"net/url"
	"path/filepath"
	"strings"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"

	mimirv1 "github.com/gsoultan/mimir-server/gen/mimir/v1"
	"github.com/gsoultan/mimir-server/gen/mimir/v1/mimirv1connect"
	"github.com/gsoultan/mimir-server/internal/arch"
	"github.com/gsoultan/mimir-server/internal/auth"
	"github.com/gsoultan/mimir-server/internal/cluster"
	"github.com/gsoultan/mimir-server/internal/dbsource"
	"github.com/gsoultan/mimir-server/internal/s3source"
	"github.com/gsoultan/mimir-server/internal/store"
	"github.com/gsoultan/mimir-server/internal/templates"
	"github.com/gsoultan/mimir-server/internal/training"
)

var (
	_ mimirv1connect.DatasetServiceHandler  = (*Service)(nil)
	_ mimirv1connect.TrainingServiceHandler = (*Service)(nil)
)

// maxCSVBytes bounds an uploaded table.
const maxCSVBytes = training.MaxTableBytes

// Datasets

func (s *Service) ListDatasets(ctx context.Context, _ *connect.Request[mimirv1.ListDatasetsRequest]) (*connect.Response[mimirv1.ListDatasetsResponse], error) {
	p, _ := auth.From(ctx)
	ds, err := s.store.ListDatasets(ctx, p.Team())
	if err != nil {
		return nil, s.internal(ctx, err)
	}
	res := &mimirv1.ListDatasetsResponse{}
	for _, d := range ds {
		res.Datasets = append(res.Datasets, datasetProto(d))
	}
	return connect.NewResponse(res), nil
}

func (s *Service) GetDataset(ctx context.Context, req *connect.Request[mimirv1.GetDatasetRequest]) (*connect.Response[mimirv1.GetDatasetResponse], error) {
	d, err := s.getDataset(ctx, req.Msg.GetId())
	if err != nil {
		return nil, err
	}
	res := &mimirv1.GetDatasetResponse{Dataset: datasetProto(d)}
	if d.Kind == store.DatasetTable || d.Kind == store.DatasetPhotoTable {
		b, err := s.store.ReadDataset(d.ID)
		if err == nil && d.Kind == store.DatasetPhotoTable {
			var arc *training.Archive
			if arc, err = training.OpenArchive(b); err == nil {
				_, b, err = arc.Table()
			}
		}
		if err == nil {
			for _, row := range training.Sample(b, 5) {
				res.Sample = append(res.Sample, &mimirv1.Row{Values: row})
			}
		}
	}
	return connect.NewResponse(res), nil
}

func (s *Service) CreateDataset(ctx context.Context, req *connect.Request[mimirv1.CreateDatasetRequest]) (*connect.Response[mimirv1.CreateDatasetResponse], error) {
	p, _ := auth.From(ctx)
	name, err := cleanName("dataset", req.Msg.GetName())
	if err != nil {
		return nil, err
	}
	tpl, err := template(req.Msg.GetTemplateId())
	if err != nil {
		return nil, err
	}
	kind := store.DatasetKind(req.Msg.GetKind())
	var file []byte
	conn, query := req.Msg.GetConnection(), strings.TrimSpace(req.Msg.GetQuery())
	var stored *s3source.Client
	path := strings.TrimSpace(req.Msg.GetPath())
	switch {
	case req.Msg.GetExample():
		if tpl == nil {
			return nil, invalid("Pick a template to use its example data.")
		}
		if _, _, file, err = templates.Example(*tpl); err != nil {
			return nil, s.internal(ctx, err)
		}
		kind = kindOf(*tpl)
	case conn != nil || query != "":
		if !p.Can(auth.DatabaseQuery) {
			return nil, connect.NewError(connect.CodePermissionDenied, errors.New("Only admins can connect a database."))
		}
		if kind != store.DatasetTable {
			return nil, invalid("A query gives a table; choose the table kind.")
		}
		r, err := s.fetch(ctx, conn, query)
		if err != nil {
			return nil, err
		}
		file = r.CSV()
	case req.Msg.GetBucket() != nil:
		if !p.Can(auth.DatabaseQuery) {
			return nil, connect.NewError(connect.CodePermissionDenied, errors.New("Only admins can connect storage."))
		}
		if tpl != nil && kind == store.DatasetKind(0) {
			kind = kindOf(*tpl)
		}
		if stored, err = openBucket(req.Msg.GetBucket()); err != nil {
			return nil, err
		}
		if file, err = s.readBucket(ctx, stored, path, kind); err != nil {
			return nil, err
		}
	case kind == store.DatasetTable:
		file = req.Msg.GetCsv()
	case kind.Archive():
		file = req.Msg.GetArchive()
	case kind == store.DatasetText:
		file = req.Msg.GetText()
	}

	var d store.Dataset
	var check *mimirv1.DataCheck
	switch kind {
	case store.DatasetTable:
		if len(file) == 0 {
			return nil, invalid("The file is empty. Upload a CSV with a header row.")
		}
		if len(file) > maxCSVBytes {
			return nil, invalid("That file is too big. Tables up to 64 MB work here.")
		}
		d, check, err = describeTable(file, strings.TrimSpace(req.Msg.GetTarget()), tpl, nil)
		if query != "" {
			d.Driver, d.DSN, d.Query = conn.GetDriver(), conn.GetDsn(), query
		}
		if stored != nil {
			// Kept so the tables can be fetched again. Photos are read once.
			d.Driver, d.DSN, d.Query = s3source.Driver, stored.Conn().JSON(), path
		}
	case store.DatasetPhotos:
		d, check, err = describePhotos(file, tpl)
	case store.DatasetPhotoTable:
		d, check, err = describePhotoTable(file, strings.TrimSpace(req.Msg.GetTarget()), tpl, nil)
	case store.DatasetText:
		d, err = describeText(file)
	case store.DatasetMNIST:
		file = nil
		d = store.Dataset{Kind: store.DatasetMNIST, Rows: 60000, InputShape: []int{1, 28, 28},
			Labels: []string{"0", "1", "2", "3", "4", "5", "6", "7", "8", "9"}}
	default:
		return nil, invalid("Choose a table (CSV), photos (ZIP), writing for a language model (text or JSON Lines) or the built-in handwritten digits.")
	}
	if err != nil {
		return nil, err
	}
	d.Name, d.TeamID = name, p.Team()
	d, err = s.store.CreateDataset(ctx, d, file)
	if err != nil {
		return nil, s.internal(ctx, err)
	}
	s.audit(ctx, p.Actor(), "dataset.create", d.Name, fmt.Sprintf("%d rows", d.Rows))
	return connect.NewResponse(&mimirv1.CreateDatasetResponse{Dataset: datasetProto(d), Check: check}), nil
}

// fetch runs a query for a dataset, refusing the server's own files.
func (s *Service) fetch(ctx context.Context, conn *mimirv1.Connection, query string) (dbsource.Result, error) {
	driver, dsn := conn.GetDriver(), strings.TrimSpace(conn.GetDsn())
	if driver == "sqlite" {
		// The server's own database and files are off limits: they hold
		// password hashes and sessions.
		path, ok := sqlitePath(dsn)
		if !ok {
			return dbsource.Result{}, invalid("That file path isn't valid.")
		}
		abs, err := filepath.Abs(path)
		if err != nil {
			return dbsource.Result{}, invalid("That file path isn't valid.")
		}
		if real, err := filepath.EvalSymlinks(abs); err == nil {
			abs = real
		}
		data, _ := filepath.Abs(s.store.Dir())
		if real, err := filepath.EvalSymlinks(data); err == nil {
			data = real
		}
		if abs == data || strings.HasPrefix(abs, data+string(filepath.Separator)) {
			return dbsource.Result{}, invalid("That file is part of this server's own data. Point at another database.")
		}
	}
	if driver == "postgres" && s.store.IsOwnDatabase(dsn) {
		return dbsource.Result{}, invalid("That is this server's own database. Point at another database.")
	}
	r, err := dbsource.Fetch(ctx, driver, dsn, query, training.MaxRows, s.store.RefuseOwnDatabase)
	if errors.Is(err, dbsource.ErrBadQuery) {
		return dbsource.Result{}, invalid("%s", strings.TrimPrefix(err.Error(), "dbsource: bad query: "))
	}
	if err != nil {
		return dbsource.Result{}, s.internal(ctx, err)
	}
	return r, nil
}

// sqlitePath returns the file a SQLite connection opens. dbsource always
// opens it as a URI, and SQLite decodes a URI's path, so "dat%61/x.db" is
// "data/x.db": the path is checked as SQLite will read it. A URI naming a
// host other than localhost is refused.
func sqlitePath(dsn string) (string, bool) {
	path := strings.TrimPrefix(dsn, "file:")
	path, _, _ = strings.Cut(path, "#")
	path, _, _ = strings.Cut(path, "?")
	if rest, ok := strings.CutPrefix(path, "//"); ok {
		host, p, found := strings.Cut(rest, "/")
		if !found || (host != "" && host != "localhost") {
			return "", false
		}
		path = "/" + p
	}
	path, err := url.PathUnescape(path)
	if err != nil || path == "" || strings.ContainsRune(path, 0) {
		return "", false
	}
	return path, true
}

// openBucket checks a storage connection.
func openBucket(b *mimirv1.Bucket) (*s3source.Client, error) {
	cl, err := s3source.New(s3source.Conn{
		Provider: b.GetProvider(), Endpoint: b.GetEndpoint(), Region: b.GetRegion(), AccountID: b.GetAccountId(),
		Bucket: b.GetBucket(), AccessKeyID: b.GetAccessKeyId(), SecretAccessKey: b.GetSecretAccessKey(),
	})
	if err != nil {
		return nil, badBucket(err)
	}
	return cl, nil
}

// readBucket downloads what a path names, as the file an upload of kind
// would be.
func (s *Service) readBucket(ctx context.Context, cl *s3source.Client, path string, kind store.DatasetKind) ([]byte, error) {
	var b []byte
	var err error
	switch {
	case kind == store.DatasetTable:
		b, err = cl.Table(ctx, path, maxCSVBytes)
	case kind.Archive():
		b, err = cl.Archive(ctx, path, training.MaxArchiveBytes)
	default:
		return nil, invalid("Choose a table (CSV) or photos to read from storage.")
	}
	if err != nil {
		return nil, s.mapBucket(ctx, err)
	}
	return b, nil
}

// countRows counts a CSV's rows after the header.
func countRows(b []byte) int {
	r := csv.NewReader(bytes.NewReader(b))
	r.FieldsPerRecord, r.LazyQuotes, r.ReuseRecord = -1, true, true
	n := -1
	for {
		if _, err := r.Read(); err != nil {
			if err == io.EOF {
				return max(n, 0)
			}
			continue
		}
		n++
	}
}

// badBucket turns a s3source.ErrBad into a message for people.
func badBucket(err error) error {
	if !errors.Is(err, s3source.ErrBad) {
		return err
	}
	msg := strings.Replace(err.Error(), "s3source: bad request: ", "", 1)
	return invalid("%s.", strings.ToUpper(msg[:1])+msg[1:])
}

func (s *Service) TestBucket(ctx context.Context, req *connect.Request[mimirv1.TestBucketRequest]) (*connect.Response[mimirv1.TestBucketResponse], error) {
	cl, err := openBucket(req.Msg.GetBucket())
	if err != nil {
		return nil, err
	}
	kind := store.DatasetKind(req.Msg.GetKind())
	if kind == store.DatasetKind(0) {
		kind = store.DatasetTable
	}
	f, err := cl.Find(ctx, req.Msg.GetPath())
	if err != nil {
		return nil, s.mapBucket(ctx, err)
	}
	res := &mimirv1.TestBucketResponse{Files: int64(len(f.Objects)), SizeBytes: f.Size()}
	for _, o := range f.Objects[:min(10, len(f.Objects))] {
		res.Names = append(res.Names, f.Name(o))
	}
	if kind == store.DatasetTable {
		b, err := s.readBucket(ctx, cl, req.Msg.GetPath(), kind)
		if err != nil {
			return nil, err
		}
		rows := training.Sample(b, 5)
		if len(rows) == 0 || len(rows[0]) < 2 {
			return nil, invalid("The first row should name at least two columns, separated by commas.")
		}
		res.Columns = rows[0]
		for _, r := range rows[1:] {
			res.Sample = append(res.Sample, &mimirv1.Row{Values: r})
		}
		res.Rows = int64(countRows(b))
	}
	return connect.NewResponse(res), nil
}

func (s *Service) mapBucket(ctx context.Context, err error) error {
	if errors.Is(err, s3source.ErrBad) {
		return badBucket(err)
	}
	return s.internal(ctx, err)
}

func (s *Service) TestQuery(ctx context.Context, req *connect.Request[mimirv1.TestQueryRequest]) (*connect.Response[mimirv1.TestQueryResponse], error) {
	r, err := s.fetch(ctx, req.Msg.GetConnection(), strings.TrimSpace(req.Msg.GetQuery()))
	if err != nil {
		return nil, err
	}
	res := &mimirv1.TestQueryResponse{Columns: r.Columns, Rows: int64(len(r.Rows))}
	for _, row := range r.Rows[:min(5, len(r.Rows))] {
		res.Sample = append(res.Sample, &mimirv1.Row{Values: row})
	}
	return connect.NewResponse(res), nil
}

func (s *Service) RefreshDataset(ctx context.Context, req *connect.Request[mimirv1.RefreshDatasetRequest]) (*connect.Response[mimirv1.RefreshDatasetResponse], error) {
	p, _ := auth.From(ctx)
	d, err := s.getDataset(ctx, req.Msg.GetId())
	if err != nil {
		return nil, err
	}
	if !d.FromQuery() {
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("These examples were uploaded, so there's nothing to fetch again."))
	}
	d, err = training.Refresh(ctx, s.store, d)
	if errors.Is(err, dbsource.ErrBadQuery) || errors.Is(err, training.ErrBadTable) {
		return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("The query didn't give usable rows: %v", err))
	}
	if errors.Is(err, s3source.ErrBad) {
		return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("The files in storage didn't give usable rows: %v", strings.Replace(err.Error(), "s3source: bad request: ", "", 1)))
	}
	if err != nil {
		return nil, s.internal(ctx, err)
	}
	s.audit(ctx, p.Actor(), "dataset.refresh", d.Name, fmt.Sprintf("%d rows", d.Rows))
	return connect.NewResponse(&mimirv1.RefreshDatasetResponse{Dataset: datasetProto(d)}), nil
}

func (s *Service) DeleteDataset(ctx context.Context, req *connect.Request[mimirv1.DeleteDatasetRequest]) (*connect.Response[mimirv1.DeleteDatasetResponse], error) {
	p, _ := auth.From(ctx)
	d, err := s.getDataset(ctx, req.Msg.GetId())
	if err != nil {
		return nil, err
	}
	err = s.store.DeleteDataset(ctx, d.ID)
	if errors.Is(err, store.ErrDatasetInUse) {
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("Training runs still use these examples. Delete those runs first."))
	}
	if err != nil {
		return nil, s.internal(ctx, err)
	}
	s.audit(ctx, p.Actor(), "dataset.delete", d.Name, "")
	return connect.NewResponse(&mimirv1.DeleteDatasetResponse{}), nil
}

// Jobs

func (s *Service) ListJobs(ctx context.Context, _ *connect.Request[mimirv1.ListJobsRequest]) (*connect.Response[mimirv1.ListJobsResponse], error) {
	p, _ := auth.From(ctx)
	js, err := s.store.ListJobs(ctx, p.Team())
	if err != nil {
		return nil, s.internal(ctx, err)
	}
	names, err := s.datasetNames(ctx, p.Team())
	if err != nil {
		return nil, s.internal(ctx, err)
	}
	res := &mimirv1.ListJobsResponse{}
	for _, j := range js {
		res.Jobs = append(res.Jobs, jobProto(j, names[j.DatasetID]))
	}
	return connect.NewResponse(res), nil
}

func (s *Service) GetJob(ctx context.Context, req *connect.Request[mimirv1.GetJobRequest]) (*connect.Response[mimirv1.GetJobResponse], error) {
	j, err := s.getJob(ctx, req.Msg.GetId())
	if err != nil {
		return nil, err
	}
	jp := s.jobWithDataset(ctx, j)
	jp.Report = reportProto(j.Report)
	return connect.NewResponse(&mimirv1.GetJobResponse{Job: jp}), nil
}

func (s *Service) CreateJob(ctx context.Context, req *connect.Request[mimirv1.CreateJobRequest]) (*connect.Response[mimirv1.CreateJobResponse], error) {
	p, _ := auth.From(ctx)
	name, desc, err := cleanText("run", req.Msg.GetName(), req.Msg.GetDescription())
	if err != nil {
		return nil, err
	}
	d, err := s.getDataset(ctx, req.Msg.GetDatasetId())
	if err != nil {
		return nil, err
	}
	a, ok := arch.Lookup(req.Msg.GetArchitecture())
	if !ok || !a.Trainable() {
		return nil, invalid("Unknown model type %q. Pick one from the list.", req.Msg.GetArchitecture())
	}
	if !contains(training.Architectures(d), a.Name) {
		return nil, invalid("%s can't learn from %s. Pick a model type that fits these examples.", a.Title, d.Name)
	}
	from, err := s.fineTuneFrom(ctx, a, req.Msg.GetBaseModelId())
	if err != nil {
		return nil, err
	}
	st := req.Msg.GetSettings()
	set := store.Settings{Epochs: int(st.GetEpochs()), BatchSize: int(st.GetBatchSize()), LearningRate: st.GetLearningRate(), HoldOut: st.GetHoldOut()}
	if ls := st.GetLayers(); len(ls) > 0 {
		if !a.Designable {
			return nil, invalid("%s models can't be designed step by step. Pick a table model, or leave the shape as it is.", a.Title)
		}
		design := make([]arch.Layer, len(ls))
		for i, l := range ls {
			design[i] = arch.Layer{Kind: l.GetKind(), Units: int(l.GetUnits()), Rate: l.GetRate()}
		}
		if err := arch.CheckLayers(design); err != nil {
			return nil, invalid("That design can't be built: %s.", strings.TrimPrefix(err.Error(), arch.ErrBadLayers.Error()+": "))
		}
		set.Layers = arch.FormatLayers(design)
	}
	def := training.SettingsFor(d)
	if set.Epochs == 0 {
		set.Epochs = def.Epochs
	}
	if set.BatchSize == 0 {
		set.BatchSize = def.BatchSize
	}
	if set.LearningRate == 0 {
		set.LearningRate = def.LearningRate
	}
	if st == nil || st.HoldOut == 0 {
		set.HoldOut = def.HoldOut
	}
	if a.Learner != nil {
		set.Epochs = 1 // it learns in one go
	}
	switch {
	case set.Epochs < 1 || set.Epochs > 1000:
		return nil, invalid("Passes over the examples must be between 1 and 1000.")
	case set.BatchSize < 1 || set.BatchSize > 4096:
		return nil, invalid("Batch size must be between 1 and 4096.")
	case set.LearningRate <= 0 || set.LearningRate > 1:
		return nil, invalid("Learning rate must be above 0 and at most 1.")
	case set.HoldOut < 0 || set.HoldOut > 0.5:
		return nil, invalid("Keep between 0%% and 50%% of the examples aside for scoring.")
	}
	if err := s.checkTrainingLimit(ctx, p); err != nil {
		if connect.CodeOf(err) == connect.CodeResourceExhausted {
			return nil, err
		}
		return nil, s.internal(ctx, err)
	}
	repeat, minRows := req.Msg.GetRepeatSeconds(), req.Msg.GetMinNewRows()
	if err := checkRepeat(repeat, minRows); err != nil {
		return nil, err
	}
	if repeat == 0 {
		minRows = 0
	}
	if from != "" && repeat > 0 {
		return nil, invalid("A fine-tune runs once; it doesn't keep learning on a schedule.")
	}
	runOn := req.Msg.GetWorkerId()
	if runOn != "" {
		if _, err := s.worker(ctx, runOn); err != nil {
			return nil, err
		}
	}
	j, err := s.store.CreateJob(ctx, store.Job{Name: name, Description: desc, Architecture: a.Name, DatasetID: d.ID, Settings: set, CreatedBy: p.Actor(), RepeatSeconds: repeat, TeamID: p.Team(), RunOn: runOn, MinNewRows: minRows, TrainedRows: d.Rows, BaseModelID: from})
	if err != nil {
		return nil, s.internal(ctx, err)
	}
	s.audit(ctx, p.Actor(), "training.create", j.Name, fmt.Sprintf("%s on %s, %d epochs", a.Title, d.Name, set.Epochs))
	s.emit(ctx, "training.queued", jobEvent(j, d.Name))
	if s.KickTraining != nil {
		s.KickTraining()
	}
	return connect.NewResponse(&mimirv1.CreateJobResponse{Job: jobProto(j, d.Name)}), nil
}

// fineTuneFrom returns the id of the language model a run of architecture
// a fine-tunes, checking the caller may use it: base, which a language
// model's run needs and any other's must leave empty.
func (s *Service) fineTuneFrom(ctx context.Context, a arch.Architecture, base string) (string, error) {
	if a.Answers() != arch.Written {
		if base != "" {
			return "", invalid("Only a language model is fine-tuned from another model.")
		}
		return "", nil
	}
	if base == "" {
		return "", invalid("Pick the language model to fine-tune.")
	}
	m, err := s.get(ctx, base)
	if err != nil {
		return "", err
	}
	if !isLanguage(m) {
		return "", invalid("%s isn't a language model, so it can't be fine-tuned on writing.", m.Name)
	}
	return m.ID, nil
}

// describeText checks the writing a language model is fine-tuned on and
// describes it.
func describeText(b []byte) (store.Dataset, error) {
	if len(b) == 0 {
		return store.Dataset{}, invalid("The file is empty. Upload plain text, or JSON Lines of chats or prompts and completions.")
	}
	w, err := training.ParseText(b)
	if err != nil {
		return store.Dataset{}, badTable(err)
	}
	d := store.Dataset{Kind: store.DatasetText}
	w.Describe(&d)
	return d, nil
}

// MaxMinNewRows bounds how many new rows a keep-learning pass may wait for.
const MaxMinNewRows = 1_000_000_000

// checkRepeat bounds a keep-learning interval: off, or between an hour
// and 90 days. A job that waits for new rows may look every 15 minutes,
// since most looks only count rows and start nothing.
func checkRepeat(seconds, minNewRows int64) error {
	if minNewRows < 0 || minNewRows > MaxMinNewRows {
		return invalid("New rows to wait for must be between 0 and %d.", MaxMinNewRows)
	}
	least, word := int64(3600), "every hour"
	if minNewRows > 0 {
		least, word = 900, "every 15 minutes"
	}
	if seconds != 0 && (seconds < least || seconds > 90*24*3600) {
		return invalid("Keep learning %s at most often, and at least every 90 days.", word)
	}
	return nil
}

func (s *Service) SetRepeat(ctx context.Context, req *connect.Request[mimirv1.SetRepeatRequest]) (*connect.Response[mimirv1.SetRepeatResponse], error) {
	p, _ := auth.From(ctx)
	j, err := s.getJob(ctx, req.Msg.GetId())
	if err != nil {
		return nil, err
	}
	seconds, minRows := req.Msg.GetRepeatSeconds(), req.Msg.GetMinNewRows()
	if err := checkRepeat(seconds, minRows); err != nil {
		return nil, err
	}
	if seconds == 0 {
		minRows = 0
	}
	if minRows > 0 && j.TrainedRows == 0 {
		// A run from before rows were counted: count from what its
		// examples hold now, rather than starting a pass at once.
		if d, err := s.store.GetDataset(ctx, j.DatasetID); err == nil {
			if err := s.store.SetTrainedRows(ctx, j.ID, d.Rows); err != nil {
				return nil, s.internal(ctx, err)
			}
		}
	}
	if err := s.store.SetRepeat(ctx, j.ID, seconds, minRows); err != nil {
		return nil, s.internal(ctx, err)
	}
	detail := "stopped"
	if seconds > 0 {
		detail = fmt.Sprintf("every %s", time.Duration(seconds)*time.Second)
		if minRows > 0 {
			detail += fmt.Sprintf(" once %d new rows are in", minRows)
		}
	}
	s.audit(ctx, p.Actor(), "training.repeat", j.Name, detail)
	if j, err = s.store.GetJob(ctx, j.ID); err != nil {
		return nil, s.internal(ctx, err)
	}
	names, _ := s.datasetNames(ctx, p.Team())
	return connect.NewResponse(&mimirv1.SetRepeatResponse{Job: jobProto(j, names[j.DatasetID])}), nil
}

func (s *Service) CancelJob(ctx context.Context, req *connect.Request[mimirv1.CancelJobRequest]) (*connect.Response[mimirv1.CancelJobResponse], error) {
	p, _ := auth.From(ctx)
	j, err := s.getJob(ctx, req.Msg.GetId())
	if err != nil {
		return nil, err
	}
	switch j.Status {
	case store.JobQueued:
		if err := s.store.SetJobStatus(ctx, j.ID, store.JobCancelled, "", ""); err != nil {
			return nil, s.internal(ctx, err)
		}
	case store.JobRunning:
		if s.CancelTraining == nil || !s.CancelTraining(j.ID) {
			// Not running in this process (it was interrupted); mark it.
			if err := s.store.SetJobStatus(ctx, j.ID, store.JobCancelled, "", ""); err != nil {
				return nil, s.internal(ctx, err)
			}
		}
	default:
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("This run is already over."))
	}
	s.audit(ctx, p.Actor(), "training.cancel", j.Name, "")
	j, _ = s.store.GetJob(ctx, j.ID)
	return connect.NewResponse(&mimirv1.CancelJobResponse{Job: s.jobWithDataset(ctx, j)}), nil
}

func (s *Service) ResumeJob(ctx context.Context, req *connect.Request[mimirv1.ResumeJobRequest]) (*connect.Response[mimirv1.ResumeJobResponse], error) {
	p, _ := auth.From(ctx)
	j, err := s.getJob(ctx, req.Msg.GetId())
	if err != nil {
		return nil, err
	}
	ok, err := s.store.RequeueJob(ctx, j.ID)
	if err != nil {
		return nil, s.internal(ctx, err)
	}
	if !ok {
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("Only a stopped or failed run can be resumed."))
	}
	s.audit(ctx, p.Actor(), "training.resume", j.Name, "")
	if s.KickTraining != nil {
		s.KickTraining()
	}
	j, _ = s.store.GetJob(ctx, j.ID)
	return connect.NewResponse(&mimirv1.ResumeJobResponse{Job: s.jobWithDataset(ctx, j)}), nil
}

func (s *Service) DeleteJob(ctx context.Context, req *connect.Request[mimirv1.DeleteJobRequest]) (*connect.Response[mimirv1.DeleteJobResponse], error) {
	p, _ := auth.From(ctx)
	j, err := s.getJob(ctx, req.Msg.GetId())
	if err != nil {
		return nil, err
	}
	if j.Status == store.JobRunning {
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("Stop the run before deleting it."))
	}
	if err := s.store.DeleteJob(ctx, j.ID); err != nil {
		return nil, s.internal(ctx, err)
	}
	s.audit(ctx, p.Actor(), "training.delete", j.Name, "")
	return connect.NewResponse(&mimirv1.DeleteJobResponse{}), nil
}

// TrainingDone is what the runner calls when a job ends: webhooks, alerts
// and the audit log.
func (s *Service) TrainingDone(ctx context.Context, j store.Job) {
	name := ""
	if d, err := s.store.GetDataset(ctx, j.DatasetID); err == nil {
		name = d.Name
	}
	switch j.Status {
	case store.JobSucceeded:
		s.auditIn(ctx, j.TeamID, "server", "training.succeeded", j.Name, fmt.Sprintf("%.1f%% right", accuracy(j)*100))
		s.emitIn(ctx, j.TeamID, "training.finished", jobEvent(j, name))
		if m, err := s.store.Get(ctx, j.ModelID); err == nil {
			s.emitIn(ctx, j.TeamID, "model.created", modelEvent(m, auth.Principal{}))
		}
		s.WarmModel(j.ModelID) // so the first prediction is as quick as the rest
		s.ScoreOnBenchmarks(ctx, j)
		s.Notifier.Notify(ctx, "Training finished: "+j.Name, fmt.Sprintf("%s learned from %s and gets %.1f%% of held-out examples right. It's ready to try.", j.Name, name, accuracy(j)*100))
	case store.JobFailed:
		s.auditIn(ctx, j.TeamID, "server", "training.failed", j.Name, j.Error)
		s.emitIn(ctx, j.TeamID, "training.failed", jobEvent(j, name))
		s.Notifier.Notify(ctx, "Training failed: "+j.Name, j.Error)
	}
}

// helpers

func (s *Service) getDataset(ctx context.Context, id string) (store.Dataset, error) {
	if !store.ValidID(id) {
		return store.Dataset{}, connect.NewError(connect.CodeNotFound, errors.New("No such dataset."))
	}
	d, err := s.store.GetDataset(ctx, id)
	if errors.Is(err, store.ErrNotFound) {
		return store.Dataset{}, connect.NewError(connect.CodeNotFound, errors.New("No such dataset."))
	}
	if err != nil {
		return store.Dataset{}, s.internal(ctx, err)
	}
	if p, _ := auth.From(ctx); d.TeamID != p.Team() {
		return store.Dataset{}, connect.NewError(connect.CodeNotFound, errors.New("No such dataset."))
	}
	return d, nil
}

func (s *Service) getJob(ctx context.Context, id string) (store.Job, error) {
	if !store.ValidID(id) {
		return store.Job{}, connect.NewError(connect.CodeNotFound, errors.New("No such training run."))
	}
	j, err := s.store.GetJob(ctx, id)
	if errors.Is(err, store.ErrNotFound) {
		return store.Job{}, connect.NewError(connect.CodeNotFound, errors.New("No such training run."))
	}
	if err != nil {
		return store.Job{}, s.internal(ctx, err)
	}
	if p, _ := auth.From(ctx); j.TeamID != p.Team() {
		return store.Job{}, connect.NewError(connect.CodeNotFound, errors.New("No such training run."))
	}
	return j, nil
}

func (s *Service) datasetNames(ctx context.Context, team string) (map[string]string, error) {
	ds, err := s.store.ListDatasets(ctx, team)
	if err != nil {
		return nil, err
	}
	names := map[string]string{}
	for _, d := range ds {
		names[d.ID] = d.Name
	}
	return names, nil
}

func (s *Service) jobWithDataset(ctx context.Context, j store.Job) *mimirv1.Job {
	name := ""
	if d, err := s.store.GetDataset(ctx, j.DatasetID); err == nil {
		name = d.Name
	}
	return jobProto(j, name)
}

func contains(xs []string, x string) bool {
	for _, y := range xs {
		if y == x {
			return true
		}
	}
	return false
}

func accuracy(j store.Job) float64 {
	if n := len(j.History); n > 0 {
		return j.History[n-1].Accuracy
	}
	return 0
}

func datasetProto(d store.Dataset) *mimirv1.Dataset {
	out := &mimirv1.Dataset{
		Id: d.ID, Name: d.Name, Kind: mimirv1.DatasetKind(d.Kind), Rows: d.Rows, Features: d.Features, Labels: d.Labels,
		InputShape: int64s(d.InputShape), Target: d.Target, SizeBytes: d.SizeBytes, CreateTime: timestamppb.New(d.Created),
		Architectures: training.Architectures(d),
		Driver:        d.Driver, Query: d.Query, // never the connection string
		Columns: columnsProto(d.Columns), NumberTarget: d.NumberTarget, Skipped: d.Skipped,
	}
	if d.Driver == s3source.Driver {
		// The path is in storage, not a query; the keys stay here.
		out.Query = ""
		c, _ := s3source.Parse(d.DSN)
		out.Storage = &mimirv1.StorageSource{Provider: c.Provider, Endpoint: c.Endpoint, Bucket: c.Bucket, Path: d.Query}
	}
	if !d.Refreshed.IsZero() {
		out.RefreshTime = timestamppb.New(d.Refreshed)
	}
	return out
}

func jobProto(j store.Job, datasetName string) *mimirv1.Job {
	out := &mimirv1.Job{
		Id: j.ID, Name: j.Name, Description: j.Description, Architecture: j.Architecture, DatasetId: j.DatasetID, DatasetName: datasetName,
		Settings: &mimirv1.TrainingSettings{Epochs: int32(j.Settings.Epochs), BatchSize: int32(j.Settings.BatchSize), LearningRate: j.Settings.LearningRate, HoldOut: j.Settings.HoldOut, Layers: cluster.LayersToProto(j.Settings.Layers)},
		Status:   mimirv1.JobStatus(j.Status), Error: j.Error, Epoch: int32(j.Epoch), ModelId: j.ModelID, Worker: j.Worker, CreatedBy: j.CreatedBy,
		RepeatSeconds: j.RepeatSeconds, ParentId: j.ParentID, BaseModelId: j.BaseModelID, Note: j.Note, RunOn: j.RunOn,
		MinNewRows: j.MinNewRows, TrainedRows: j.TrainedRows, CheckNote: j.CheckNote,
		CreateTime: timestamppb.New(j.Created), Accuracy: accuracy(j), StepsPerEpoch: int32(j.StepsPerEpoch), Step: int32(j.Step),
	}
	if !j.Started.IsZero() {
		out.StartTime = timestamppb.New(j.Started)
	}
	if !j.Ended.IsZero() {
		out.EndTime = timestamppb.New(j.Ended)
	}
	if !j.NextRun.IsZero() && j.RepeatSeconds > 0 {
		out.NextRunTime = timestamppb.New(j.NextRun)
	}
	if !j.CheckTime.IsZero() {
		out.CheckTime = timestamppb.New(j.CheckTime)
	}
	for _, e := range j.History {
		out.History = append(out.History, &mimirv1.EpochResult{Epoch: int32(e.Epoch), Loss: e.Loss, Accuracy: e.Accuracy, ValLoss: e.ValLoss, DurationMillis: e.Millis, TypicalError: e.MAE})
	}
	return out
}

func jobEvent(j store.Job, datasetName string) map[string]any {
	return map[string]any{
		"id": j.ID, "name": j.Name, "architecture": j.Architecture, "dataset": datasetName, "status": j.Status.String(),
		"epoch": j.Epoch, "epochs": j.Settings.Epochs, "accuracy": accuracy(j), "model_id": j.ModelID, "error": j.Error,
	}
}
