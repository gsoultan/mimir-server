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
	// How a language model answers when a chat or app doesn't say: its
	// instructions (the system message), temperature and longest answer
	// in tokens. Other models keep the defaults.
	Instructions string
	Temperature  float64
	MaxTokens    int32
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
	t.Col(&m.InputShape).Default("'{}'").ConvertFrom(storm.JSONText)
	t.Col(&m.Labels).Default("'{}'").ConvertFrom(storm.JSONText)
	t.Col(&m.Features).Default("'{}'").ConvertFrom(storm.JSONText)
	t.Col(&m.Columns).Default("'[]'").ConvertFrom(storm.JSONText)
	t.Col(&m.JobID).Default("''")
	t.Col(&m.Accuracy).Default("0")
	t.Col(&m.WorkerID).Default("''")
	t.Col(&m.Instructions).Default("''")
	t.Col(&m.Temperature).Default("0.7")
	t.Col(&m.MaxTokens).Default("256")
	// A team's models, newest first; also the team key's index.
	t.Col(&m.Team).NoIndex()
	t.Index(&m.Team, storm.Desc(&m.CreateTime), storm.Desc(&m.ID))
	// The models placed on a worker, and unplacing them when it goes.
	t.Index(&m.WorkerID)
	t.Check(storm.RawSQL(`length(description) <= 500`))
	t.Check(storm.RawSQL(`architecture <> ''`))
	t.Check(storm.RawSQL(`size_bytes > 0`))
	t.Check(storm.RawSQL(`length(sha256) = 64`))
	t.Check(storm.RawSQL(`length(instructions) <= 8000`))
	t.Check(storm.RawSQL(`temperature >= 0 AND temperature <= 2`))
	t.Check(storm.RawSQL(`max_tokens >= 1 AND max_tokens <= 1024`))
}
