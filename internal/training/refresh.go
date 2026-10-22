package training

import (
	"bytes"
	"context"

	"github.com/gsoultan/mimir-server/internal/dbsource"
	"github.com/gsoultan/mimir-server/internal/s3source"
	"github.com/gsoultan/mimir-server/internal/store"
)

// Refresh runs a query dataset's query again, or reads its files from
// storage again, and stores the new rows. It checks them the same way an
// upload is checked, so a broken query leaves the old rows in place.
func Refresh(ctx context.Context, st *store.Store, d store.Dataset) (store.Dataset, error) {
	if !d.FromQuery() {
		return d, nil
	}
	b, err := fetch(ctx, st, d)
	if err != nil {
		return d, err
	}
	t, err := ParseTable(bytes.NewReader(b), d.Target, Source{Columns: d.Columns}.Hints())
	if err != nil {
		return d, err
	}
	t.Describe(&d)
	return st.RefreshDataset(ctx, d, b)
}

func fetch(ctx context.Context, st *store.Store, d store.Dataset) ([]byte, error) {
	if d.Driver == s3source.Driver {
		c, err := s3source.Parse(d.DSN)
		if err != nil {
			return nil, err
		}
		cl, err := s3source.New(c)
		if err != nil {
			return nil, err
		}
		return cl.Table(ctx, d.Query, MaxTableBytes)
	}
	r, err := dbsource.Fetch(ctx, d.Driver, d.DSN, d.Query, MaxRows, st.RefuseOwnDatabase)
	if err != nil {
		return nil, err
	}
	return r.CSV(), nil
}

// schedule starts the keep-learning passes that are due: it fetches each
// one's examples again and queues a pass that continues from the model.
func (r *Runner) schedule(ctx context.Context) {
	due, err := r.st.DueJobs(ctx, r.Now())
	if err != nil {
		r.log.ErrorContext(ctx, "training: due jobs", "error", err)
		return
	}
	for _, j := range due {
		log := r.log.With("job_id", j.ID)
		next := store.Job{
			Name: j.Name, Description: j.Description, Architecture: j.Architecture, DatasetID: j.DatasetID,
			Settings: j.Settings, CreatedBy: j.CreatedBy, RepeatSeconds: j.RepeatSeconds,
			ParentID: j.ParentID, BaseModelID: j.BaseModelID, TeamID: j.TeamID, RunOn: j.RunOn,
		}
		if next.ParentID == "" {
			next.ParentID = j.ID
		}
		if j.ModelID != "" {
			next.BaseModelID = j.ModelID
		}
		var stale error
		if d, err := r.st.GetDataset(ctx, j.DatasetID); err == nil && d.FromQuery() {
			if _, err := Refresh(ctx, r.st, d); err != nil {
				// Learn from the rows already here; the run page says why.
				log.WarnContext(ctx, "training: refresh before next pass", "error", err)
				stale = err
			}
		}
		n, err := r.st.HandOver(ctx, j, next)
		if err != nil {
			log.ErrorContext(ctx, "training: queue next pass", "error", err)
			continue
		}
		if stale != nil {
			r.st.SetJobNote(ctx, n.ID, "Couldn't fetch new rows ("+stale.Error()+"), so it learned from the rows already here.")
		}
		log.InfoContext(ctx, "training: next pass queued", "next_job_id", n.ID)
	}
}
