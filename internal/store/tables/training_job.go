package tables

import (
	"time"

	"github.com/gsoultan/storm"
)

// TrainingJob is one training run. History is the []store.Epoch so far, as
// jsonb.
type TrainingJob struct {
	Record
	Named
	TeamOwned
	Description   string
	Architecture  string
	Dataset       Dataset
	Epochs        int64
	BatchSize     int64
	LearningRate  float64
	HoldOut       float64
	Status        JobStatus
	Error         string
	Epoch         int64
	Step          int64
	StepsPerEpoch int64
	History       storm.JSON
	ModelID       string
	Worker        string // the worker that ran it, by name; "" for this server
	CreatedBy     string
	StartTime     *time.Time
	EndTime       *time.Time
	RepeatSeconds int64
	NextRun       *time.Time
	ParentID      string
	BaseModelID   string
	Note          string
	RunOn         string // the worker asked to run it, by id; "" for any
}

// Aggregates: how many runs wait for each worker.
func (j *TrainingJob) Aggregates(a *storm.Aggregates) {
	perRunOn := a.Named("PerRunOn")
	perRunOn.By(&j.RunOn)
	perRunOn.Count("N")
}

func (j *TrainingJob) Schema(t *storm.Table) {
	t.Col(&j.Dataset).OnDelete(storm.Restrict).Immutable() // indexed, for the restrict and the in-use check
	t.Col(&j.Description).Default("''")
	t.Col(&j.Architecture).Immutable()
	t.Col(&j.Error).Default("''")
	t.Col(&j.Epoch).Default("0")
	t.Col(&j.Step).Default("0")
	t.Col(&j.StepsPerEpoch).Default("0")
	t.Col(&j.History).Default("'[]'").ConvertFrom(storm.JSONText)
	t.Col(&j.ModelID).Default("''")
	t.Col(&j.Worker).Default("''")
	t.Col(&j.CreatedBy).Default("''").Immutable()
	t.Col(&j.RepeatSeconds).Default("0")
	t.Col(&j.ParentID).Default("''").Immutable()
	t.Col(&j.BaseModelID).Default("''").Immutable()
	t.Col(&j.Note).Default("''")
	t.Col(&j.RunOn).Default("''")
	// A team's runs, newest first; also the team key's index.
	t.Col(&j.Team).NoIndex()
	t.Index(&j.Team, storm.Desc(&j.CreateTime), storm.Desc(&j.ID))
	// The queue: the oldest queued run is claimed first. It also counts
	// queued and running runs and finds what a restart left running.
	t.Index(&j.Status, &j.CreateTime, &j.ID)
	// Keep-learning runs that are due. Partial: most runs never repeat.
	t.Index(&j.NextRun, &j.ID).Where(storm.RawSQL(`repeat_seconds > 0`))
	// A worker's runs: waiting for it (and unplacing them when it goes), or
	// run by it.
	t.Index(&j.RunOn)
	t.Index(&j.Worker)
	t.Check(storm.RawSQL(between("status", JobQueued, JobCancelled)))
}
