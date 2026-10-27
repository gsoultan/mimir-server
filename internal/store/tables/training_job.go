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
	// Layers is the design of a table model someone shaped themselves, as
	// arch.FormatLayers writes it; "" is the usual shape.
	Layers string
	// MinNewRows > 0 makes a keep-learning pass wait until its source has
	// that many more rows than TrainedRows, the rows the last pass had.
	MinNewRows  int64
	TrainedRows int64
	// CheckTime and CheckNote say when the source was last looked at for
	// new rows without starting a pass, and what was found.
	CheckTime *time.Time
	CheckNote string
	// Report is the store.Report a finished run's model got on the
	// examples kept aside, as jsonb; {} until it has one.
	Report storm.JSON
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
	t.Col(&j.Layers).Default("''").Immutable()
	t.Col(&j.MinNewRows).Default("0")
	t.Col(&j.TrainedRows).Default("0")
	t.Col(&j.CheckNote).Default("''")
	t.Col(&j.Report).Default("'{}'")
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
	t.Check(storm.RawSQL(`length(layers) <= 200`))
	t.Check(storm.RawSQL(`min_new_rows >= 0`))
}
