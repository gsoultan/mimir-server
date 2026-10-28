package tables

import (
	"time"

	"github.com/gsoultan/storm"
)

// Scoring answers every row of a table with one model. Its rows come
// from an upload (kept as a file in the data folder), a database query or
// storage (Driver, DSN and SourceQuery, as on Dataset; DSN is a secret
// and never sent to clients) or a table dataset (DatasetID, a plain-text
// reference, so deleting the dataset only makes the next run fail). Each
// run writes its results as a CSV file next to the upload.
type Scoring struct {
	Record
	Named
	TeamOwned
	Model       Model
	DatasetID   string
	Driver      string
	DSN         string
	SourceQuery string
	Status      JobStatus
	Error       string
	Rows        int64
	Done        int64
	Failed      int64
	// Columns are the result file's header.
	Columns     []string
	ResultBytes int64
	// VersionID is the model version the last run answered with.
	VersionID     string
	CreatedBy     string
	StartTime     *time.Time
	EndTime       *time.Time
	RepeatSeconds int64
	NextRun       *time.Time
	Runs          int64
}

func (s *Scoring) Schema(t *storm.Table) {
	// Deleting a model deletes its scorings; the store removes their files.
	t.Col(&s.Model).OnDelete(storm.Cascade).Immutable() // indexed, for the cascade and a model's list
	t.Col(&s.DatasetID).Default("''").Immutable()
	t.Col(&s.Driver).Default("''").Immutable()
	// Not Immutable, as Dataset.DSN is not: the store seals a plaintext
	// one in place.
	t.Col(&s.DSN).Named("dsn").Default("''")
	t.Col(&s.SourceQuery).Default("''").Immutable()
	t.Col(&s.Error).Default("''")
	t.Col(&s.Rows).Default("0")
	t.Col(&s.Done).Default("0")
	t.Col(&s.Failed).Default("0")
	t.Col(&s.Columns).Default("'{}'")
	t.Col(&s.ResultBytes).Default("0")
	t.Col(&s.VersionID).Default("''")
	t.Col(&s.CreatedBy).Default("''").Immutable()
	t.Col(&s.RepeatSeconds).Default("0")
	t.Col(&s.Runs).Default("0")
	// A team's scorings, newest first; also the team key's index.
	t.Col(&s.Team).NoIndex()
	t.Index(&s.Team, storm.Desc(&s.CreateTime), storm.Desc(&s.ID))
	// The queue: the oldest queued scoring is claimed first; also finds
	// what a restart left running.
	t.Index(&s.Status, &s.CreateTime, &s.ID)
	// Scorings that are due to run again. Partial: most never repeat.
	t.Index(&s.NextRun, &s.ID).Where(storm.RawSQL(`repeat_seconds > 0`))
	t.Check(storm.RawSQL(between("status", JobQueued, JobCancelled)))
	t.Check(storm.RawSQL(`rows >= 0 AND done >= 0 AND failed >= 0 AND repeat_seconds >= 0`))
}
