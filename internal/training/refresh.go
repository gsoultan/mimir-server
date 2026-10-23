package training

import (
	"bytes"
	"context"
	"fmt"
	"runtime/debug"
	"strconv"
	"time"

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

// ClaimLease is how long a claimed keep-learning pass is left to this
// server while its rows are fetched; one that never gets queued, because
// the server stopped, is due again after it.
const ClaimLease = time.Hour

// MemoryRetry is how soon a pass that was short of memory looks again.
const MemoryRetry = 5 * time.Minute

// RefreshesAtOnce bounds how many datasets the scheduler fetches at a
// time: each fetch holds its rows in memory.
const RefreshesAtOnce = 2

// schedule claims the keep-learning passes that are due and prepares
// each in the background: it fetches the examples again and, when there
// are enough new rows, queues a pass that continues from the model. The
// runner loop doesn't wait, so a slow database never holds up the queue.
func (r *Runner) schedule(ctx context.Context) {
	due, err := r.st.ClaimDueJobs(ctx, r.Now(), ClaimLease, 100)
	if err != nil {
		r.log.ErrorContext(ctx, "training: due jobs", "error", err)
		return
	}
	for _, j := range due {
		r.refreshing.Go(func() {
			select {
			case r.refreshSlots <- struct{}{}:
			case <-ctx.Done():
				return
			}
			defer func() { <-r.refreshSlots }()
			defer func() {
				if v := recover(); v != nil {
					r.log.ErrorContext(ctx, "training: preparing the next pass crashed", "job_id", j.ID, "panic", v, "stack", string(debug.Stack()))
				}
			}()
			r.nextPass(ctx, j)
		})
	}
}

// nextPass fetches a keep-learning job's examples again and queues its
// next pass, or, when it waits for new rows that haven't come, records
// what it found and when it looks again.
func (r *Runner) nextPass(ctx context.Context, j store.Job) {
	log := r.log.With("job_id", j.ID)
	every := time.Duration(j.RepeatSeconds) * time.Second
	rows, stale := j.TrainedRows, error(nil)
	if d, err := r.st.GetDataset(ctx, j.DatasetID); err == nil {
		rows = d.Rows
		if d.FromQuery() {
			if !r.admitFetch(refreshNeed(d)) {
				log.WarnContext(ctx, "training: short of memory to fetch new rows; trying again soon")
				r.st.SetJobCheck(ctx, j.ID, "The server was short of memory to fetch new rows, so it will look again in a few minutes.", r.Now().Add(MemoryRetry))
				return
			}
			if nd, err := Refresh(ctx, r.st, d); err != nil {
				// Learn from the rows already here; the run page says why.
				log.WarnContext(ctx, "training: refresh before next pass", "error", err)
				stale = err
			} else {
				rows = nd.Rows
			}
		}
	}
	if ctx.Err() != nil {
		return // stopping; the claim runs out and the next start does it
	}
	if j.MinNewRows > 0 {
		if stale != nil {
			r.st.SetJobCheck(ctx, j.ID, "Couldn't fetch new rows ("+stale.Error()+"); it will look again.", r.Now().Add(every))
			return
		}
		if gained := rows - j.TrainedRows; gained < j.MinNewRows {
			r.st.SetJobCheck(ctx, j.ID, fmt.Sprintf("%s new rows since the last pass; it waits for %s.", count(max(gained, 0)), count(j.MinNewRows)), r.Now().Add(every))
			log.DebugContext(ctx, "training: not enough new rows yet", "new_rows", gained, "min_new_rows", j.MinNewRows)
			return
		}
	}
	next := store.Job{
		Name: j.Name, Description: j.Description, Architecture: j.Architecture, DatasetID: j.DatasetID,
		Settings: j.Settings, CreatedBy: j.CreatedBy, RepeatSeconds: j.RepeatSeconds,
		ParentID: j.ParentID, BaseModelID: j.BaseModelID, TeamID: j.TeamID, RunOn: j.RunOn,
		MinNewRows: j.MinNewRows, TrainedRows: rows,
	}
	if next.ParentID == "" {
		next.ParentID = j.ID
	}
	if j.ModelID != "" {
		next.BaseModelID = j.ModelID
	}
	n, err := r.st.HandOver(ctx, j, next)
	if err != nil {
		log.ErrorContext(ctx, "training: queue next pass", "error", err)
		return
	}
	if stale != nil {
		r.st.SetJobNote(ctx, n.ID, "Couldn't fetch new rows ("+stale.Error()+"), so it learned from the rows already here.")
	}
	log.InfoContext(ctx, "training: next pass queued", "next_job_id", n.ID, "rows", rows)
	r.Kick()
}

// admitFetch reports whether a keep-learning fetch of need bytes may run
// now: Admit decides when set, else the memory budget from WatchMemory.
func (r *Runner) admitFetch(need int64) bool {
	if r.Admit != nil {
		return r.Admit(need)
	}
	return r.memory.Admit(need)
}

// refreshNeed guesses the memory a dataset's fetch takes: its rows as
// text, once read and once parsed, from the size of the last fetch.
func refreshNeed(d store.Dataset) int64 {
	return 3 * max(d.SizeBytes, 1<<20)
}

// count writes n with thousands separators.
func count(n int64) string {
	s := strconv.FormatInt(n, 10)
	for i := len(s) - 3; i > 0; i -= 3 {
		s = s[:i] + "," + s[i:]
	}
	return s
}
