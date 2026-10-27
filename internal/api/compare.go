package api

import (
	"context"
	"errors"

	"connectrpc.com/connect"

	mimirv1 "github.com/gsoultan/mimir-server/gen/mimir/v1"
	"github.com/gsoultan/mimir-server/internal/auth"
	"github.com/gsoultan/mimir-server/internal/store"
)

// maxCompare is how many runs fit side by side.
const maxCompare = 6

func (s *Service) CompareJobs(ctx context.Context, req *connect.Request[mimirv1.CompareJobsRequest]) (*connect.Response[mimirv1.CompareJobsResponse], error) {
	var ids []string
	for _, id := range req.Msg.GetIds() {
		if !contains(ids, id) {
			ids = append(ids, id)
		}
	}
	if len(ids) < 2 {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("Pick at least two runs to compare."))
	}
	if len(ids) > maxCompare {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("Compare at most 6 runs at a time."))
	}
	js := make([]store.Job, 0, len(ids))
	for _, id := range ids {
		j, err := s.getJob(ctx, id)
		if err != nil {
			return nil, err
		}
		js = append(js, j)
	}
	p, _ := auth.From(ctx)
	names, err := s.datasetNames(ctx, p.Team())
	if err != nil {
		return nil, s.internal(ctx, err)
	}
	res := &mimirv1.CompareJobsResponse{Differs: differs(js), BestId: best(js)}
	for _, j := range js {
		jp := jobProto(j, names[j.DatasetID])
		jp.Report = reportProto(j.Report)
		res.Jobs = append(res.Jobs, jp)
	}
	return connect.NewResponse(res), nil
}

// differs names what is not the same in every run.
func differs(js []store.Job) []string {
	fields := []struct {
		name string
		of   func(store.Job) any
	}{
		{"architecture", func(j store.Job) any { return j.Architecture }},
		{"dataset", func(j store.Job) any { return j.DatasetID }},
		{"epochs", func(j store.Job) any { return j.Settings.Epochs }},
		{"batch_size", func(j store.Job) any { return j.Settings.BatchSize }},
		{"learning_rate", func(j store.Job) any { return j.Settings.LearningRate }},
		{"hold_out", func(j store.Job) any { return j.Settings.HoldOut }},
		{"layers", func(j store.Job) any { return j.Settings.Layers }},
		{"worker", func(j store.Job) any { return j.Worker }},
	}
	var out []string
	for _, f := range fields {
		for _, j := range js[1:] {
			if f.of(j) != f.of(js[0]) {
				out = append(out, f.name)
				break
			}
		}
	}
	return out
}

// best is the finished run with the highest accuracy; the earlier one wins
// a tie.
func best(js []store.Job) string {
	id, top := "", -1.0
	for _, j := range js {
		if j.Status == store.JobSucceeded && accuracy(j) > top {
			id, top = j.ID, accuracy(j)
		}
	}
	return id
}

// reportProto is a run's report for the API; nil when it has none.
func reportProto(r *store.Report) *mimirv1.RunReport {
	if r == nil {
		return nil
	}
	out := &mimirv1.RunReport{
		Kind: r.Kind, Examples: int64(r.Examples), OnTraining: r.OnTraining, MillisPerExample: r.MillisPerExample, SizeBytes: r.SizeBytes,
		Accuracy: r.Accuracy, MacroF1: r.MacroF1, LogLoss: r.LogLoss,
		Mae: r.MAE, Rmse: r.RMSE, R2: r.R2, Within10: r.Within10,
		K: int32(r.K), HitAt: r.HitAt, Mrr: r.MRR, Ndcg: r.NDCG,
	}
	for _, c := range r.Classes {
		out.Classes = append(out.Classes, &mimirv1.ClassScore{Label: c.Label, Precision: c.Precision, Recall: c.Recall, F1: c.F1, Support: c.Support})
	}
	for _, row := range r.Confusion {
		out.Confusion = append(out.Confusion, &mimirv1.ConfusionRow{Counts: row})
	}
	for _, p := range r.Points {
		out.Points = append(out.Points, &mimirv1.GuessPoint{Real: p.Real, Guess: p.Guess})
	}
	return out
}
