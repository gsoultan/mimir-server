package api

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"

	mimirv1 "github.com/gsoultan/mimir-server/gen/mimir/v1"
	"github.com/gsoultan/mimir-server/gen/mimir/v1/mimirv1connect"
	"github.com/gsoultan/mimir-server/internal/arch"
	"github.com/gsoultan/mimir-server/internal/auth"
	"github.com/gsoultan/mimir-server/internal/dbsource"
	"github.com/gsoultan/mimir-server/internal/store"
	"github.com/gsoultan/mimir-server/internal/training"
)

var (
	_ mimirv1connect.DatasetServiceHandler  = (*Service)(nil)
	_ mimirv1connect.TrainingServiceHandler = (*Service)(nil)
)

// maxCSVBytes bounds an uploaded table.
const maxCSVBytes = 64 << 20

// Datasets

func (s *Service) ListDatasets(ctx context.Context, _ *connect.Request[mimirv1.ListDatasetsRequest]) (*connect.Response[mimirv1.ListDatasetsResponse], error) {
	ds, err := s.store.ListDatasets(ctx)
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
	if d.Kind == store.DatasetTable {
		if b, err := s.store.ReadDataset(d.ID); err == nil {
			for _, row := range training.Sample(b, 5) {
				res.Sample = append(res.Sample, &mimirv1.Row{Values: row})
			}
		}
	}
	return connect.NewResponse(res), nil
}

func (s *Service) CreateDataset(ctx context.Context, req *connect.Request[mimirv1.CreateDatasetRequest]) (*connect.Response[mimirv1.CreateDatasetResponse], error) {
	p, _ := auth.From(ctx)
	name, _, err := cleanText(req.Msg.GetName(), "")
	if err != nil {
		return nil, err
	}
	var d store.Dataset
	var csvBytes []byte
	conn, query := req.Msg.GetConnection(), strings.TrimSpace(req.Msg.GetQuery())
	if conn != nil || query != "" {
		if !p.Can(auth.SettingsManage) {
			return nil, connect.NewError(connect.CodePermissionDenied, errors.New("Only admins can connect a database."))
		}
		if req.Msg.GetKind() != mimirv1.DatasetKind_DATASET_KIND_TABLE {
			return nil, invalid("A query gives a table; choose the table kind.")
		}
		r, err := s.fetch(ctx, conn, query)
		if err != nil {
			return nil, err
		}
		csvBytes = r.CSV()
	}
	switch req.Msg.GetKind() {
	case mimirv1.DatasetKind_DATASET_KIND_TABLE:
		if csvBytes == nil {
			csvBytes = req.Msg.GetCsv()
		}
		if len(csvBytes) == 0 {
			return nil, invalid("The file is empty. Upload a CSV with a header row.")
		}
		if len(csvBytes) > maxCSVBytes {
			return nil, invalid("That file is too big. Tables up to 64 MB work here.")
		}
		t, err := training.ParseTable(bytes.NewReader(csvBytes), strings.TrimSpace(req.Msg.GetTarget()))
		if errors.Is(err, training.ErrBadTable) {
			return nil, invalid("%s", strings.TrimPrefix(err.Error(), "training: bad table: "))
		}
		if err != nil {
			return nil, s.internal(ctx, err)
		}
		target := strings.TrimSpace(req.Msg.GetTarget())
		if target == "" {
			target = lastColumn(csvBytes)
		}
		d = store.Dataset{Name: name, Kind: store.DatasetTable, Rows: int64(t.Rows), Features: t.Features, Labels: t.Classes, InputShape: []int{len(t.Features)}, Target: target}
		if query != "" {
			d.Driver, d.DSN, d.Query = conn.GetDriver(), conn.GetDsn(), query
		}
	case mimirv1.DatasetKind_DATASET_KIND_MNIST:
		d = store.Dataset{Name: name, Kind: store.DatasetMNIST, Rows: 60000, InputShape: []int{1, 28, 28},
			Labels: []string{"0", "1", "2", "3", "4", "5", "6", "7", "8", "9"}}
	default:
		return nil, invalid("Choose a table (CSV) or the built-in handwritten digits.")
	}
	d, err = s.store.CreateDataset(ctx, d, csvBytes)
	if err != nil {
		return nil, s.internal(ctx, err)
	}
	s.audit(ctx, p.Actor(), "dataset.create", d.Name, fmt.Sprintf("%d rows", d.Rows))
	return connect.NewResponse(&mimirv1.CreateDatasetResponse{Dataset: datasetProto(d)}), nil
}

// fetch runs a query for a dataset, refusing the server's own files.
func (s *Service) fetch(ctx context.Context, conn *mimirv1.Connection, query string) (dbsource.Result, error) {
	driver, dsn := conn.GetDriver(), strings.TrimSpace(conn.GetDsn())
	if driver == "sqlite" {
		// The server's own database and files are off limits: they hold
		// password hashes and sessions.
		path := strings.TrimPrefix(dsn, "file:")
		path, _, _ = strings.Cut(path, "?")
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
	r, err := dbsource.Fetch(ctx, driver, dsn, query, training.MaxRows)
	if errors.Is(err, dbsource.ErrBadQuery) {
		return dbsource.Result{}, invalid("%s", strings.TrimPrefix(err.Error(), "dbsource: bad query: "))
	}
	if err != nil {
		return dbsource.Result{}, s.internal(ctx, err)
	}
	return r, nil
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
	js, err := s.store.ListJobs(ctx)
	if err != nil {
		return nil, s.internal(ctx, err)
	}
	names, err := s.datasetNames(ctx)
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
	return connect.NewResponse(&mimirv1.GetJobResponse{Job: s.jobWithDataset(ctx, j)}), nil
}

func (s *Service) CreateJob(ctx context.Context, req *connect.Request[mimirv1.CreateJobRequest]) (*connect.Response[mimirv1.CreateJobResponse], error) {
	p, _ := auth.From(ctx)
	name, desc, err := cleanText(req.Msg.GetName(), req.Msg.GetDescription())
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
	st := req.Msg.GetSettings()
	set := store.Settings{Epochs: int(st.GetEpochs()), BatchSize: int(st.GetBatchSize()), LearningRate: st.GetLearningRate(), HoldOut: st.GetHoldOut()}
	def := training.DefaultsFor(d.Kind)
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
	repeat := req.Msg.GetRepeatSeconds()
	if err := checkRepeat(repeat); err != nil {
		return nil, err
	}
	j, err := s.store.CreateJob(ctx, store.Job{Name: name, Description: desc, Architecture: a.Name, DatasetID: d.ID, Settings: set, CreatedBy: p.Actor(), RepeatSeconds: repeat})
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

// checkRepeat bounds a keep-learning interval: off, or between an hour
// and 90 days.
func checkRepeat(seconds int64) error {
	if seconds != 0 && (seconds < 3600 || seconds > 90*24*3600) {
		return invalid("Keep learning every hour at most often, and at least every 90 days.")
	}
	return nil
}

func (s *Service) SetRepeat(ctx context.Context, req *connect.Request[mimirv1.SetRepeatRequest]) (*connect.Response[mimirv1.SetRepeatResponse], error) {
	p, _ := auth.From(ctx)
	j, err := s.getJob(ctx, req.Msg.GetId())
	if err != nil {
		return nil, err
	}
	seconds := req.Msg.GetRepeatSeconds()
	if err := checkRepeat(seconds); err != nil {
		return nil, err
	}
	if err := s.store.SetRepeat(ctx, j.ID, seconds); err != nil {
		return nil, s.internal(ctx, err)
	}
	detail := "stopped"
	if seconds > 0 {
		detail = fmt.Sprintf("every %s", time.Duration(seconds)*time.Second)
	}
	s.audit(ctx, p.Actor(), "training.repeat", j.Name, detail)
	if j, err = s.store.GetJob(ctx, j.ID); err != nil {
		return nil, s.internal(ctx, err)
	}
	names, _ := s.datasetNames(ctx)
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
		s.audit(ctx, "server", "training.succeeded", j.Name, fmt.Sprintf("%.1f%% right", accuracy(j)*100))
		s.emit(ctx, "training.finished", jobEvent(j, name))
		if m, err := s.store.Get(ctx, j.ModelID); err == nil {
			s.emit(ctx, "model.created", modelEvent(m, auth.Principal{}))
		}
		s.Notifier.Notify(ctx, "Training finished: "+j.Name, fmt.Sprintf("%s learned from %s and gets %.1f%% of held-out examples right. It's ready to try.", j.Name, name, accuracy(j)*100))
	case store.JobFailed:
		s.audit(ctx, "server", "training.failed", j.Name, j.Error)
		s.emit(ctx, "training.failed", jobEvent(j, name))
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
	return j, nil
}

func (s *Service) datasetNames(ctx context.Context) (map[string]string, error) {
	ds, err := s.store.ListDatasets(ctx)
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

func lastColumn(csvBytes []byte) string {
	line, _, _ := bytes.Cut(csvBytes, []byte("\n"))
	parts := strings.Split(strings.TrimSpace(string(line)), ",")
	return strings.TrimSpace(parts[len(parts)-1])
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
	}
	if !d.Refreshed.IsZero() {
		out.RefreshTime = timestamppb.New(d.Refreshed)
	}
	return out
}

func jobProto(j store.Job, datasetName string) *mimirv1.Job {
	out := &mimirv1.Job{
		Id: j.ID, Name: j.Name, Description: j.Description, Architecture: j.Architecture, DatasetId: j.DatasetID, DatasetName: datasetName,
		Settings: &mimirv1.TrainingSettings{Epochs: int32(j.Settings.Epochs), BatchSize: int32(j.Settings.BatchSize), LearningRate: j.Settings.LearningRate, HoldOut: j.Settings.HoldOut},
		Status:   mimirv1.JobStatus(j.Status), Error: j.Error, Epoch: int32(j.Epoch), ModelId: j.ModelID, Worker: j.Worker, CreatedBy: j.CreatedBy,
		RepeatSeconds: j.RepeatSeconds, ParentId: j.ParentID, BaseModelId: j.BaseModelID, Note: j.Note,
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
	for _, e := range j.History {
		out.History = append(out.History, &mimirv1.EpochResult{Epoch: int32(e.Epoch), Loss: e.Loss, Accuracy: e.Accuracy, ValLoss: e.ValLoss, DurationMillis: e.Millis})
	}
	return out
}

func jobEvent(j store.Job, datasetName string) map[string]any {
	return map[string]any{
		"id": j.ID, "name": j.Name, "architecture": j.Architecture, "dataset": datasetName, "status": statusWord(j.Status),
		"epoch": j.Epoch, "epochs": j.Settings.Epochs, "accuracy": accuracy(j), "model_id": j.ModelID, "error": j.Error,
	}
}

func statusWord(s store.JobStatus) string {
	return [...]string{"", "queued", "running", "succeeded", "failed", "cancelled"}[s]
}
