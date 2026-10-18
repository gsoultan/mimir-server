package tables

import (
	"time"

	"github.com/gsoultan/storm"
)

// Model is a stored model's metadata; its weights live on disk.
type Model struct {
	Record
	Named
	TeamOwned
	Description  string
	Architecture string
	SizeBytes    int64
	SHA256       string
	UpdateTime   time.Time
	InputShape   []int64
	Labels       []string
	Features     []string
	// Columns is the []schema.Column a table model reads, as jsonb.
	Columns  storm.JSON
	JobID    string
	Accuracy float64
	WorkerID string // "" runs it wherever there is room
}

// Aggregates: how many models each team has, and how many are placed on
// each worker.
func (m *Model) Aggregates(a *storm.Aggregates) {
	perTeam := a.Named("PerTeam")
	perTeam.By(&m.Team)
	perTeam.Count("N")

	perWorker := a.Named("PerWorker")
	perWorker.By(&m.WorkerID)
	perWorker.Count("N")
}

func (m *Model) Schema(t *storm.Table) {
	t.Col(&m.Description).Default("''")
	t.Col(&m.InputShape).Default("'{}'")
	t.Col(&m.Labels).Default("'{}'")
	t.Col(&m.Features).Default("'{}'")
	t.Col(&m.Columns).Default("'[]'")
	t.Col(&m.JobID).Default("''")
	t.Col(&m.Accuracy).Default("0")
	t.Col(&m.WorkerID).Default("''")
	// A team's models, newest first; also the team key's index.
	t.Col(&m.Team).NoIndex()
	t.Index(&m.Team, storm.Desc(&m.CreateTime), storm.Desc(&m.ID))
	// The models placed on a worker, and unplacing them when it goes.
	t.Index(&m.WorkerID)
	t.Check(storm.RawSQL(`length(description) <= 500`))
	t.Check(storm.RawSQL(`architecture <> ''`))
	t.Check(storm.RawSQL(`size_bytes > 0`))
	t.Check(storm.RawSQL(`length(sha256) = 64`))
}
