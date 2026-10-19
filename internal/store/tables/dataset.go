package tables

import (
	"time"

	"github.com/gsoultan/storm"
)

// Dataset is a set of examples. DSN is a secret and never sent to clients.
type Dataset struct {
	Record
	Named
	TeamOwned
	Kind        DatasetKind
	Rows        int64
	Features    []string
	Labels      []string
	InputShape  []int64
	Target      string
	SizeBytes   int64
	Driver      string
	DSN         string
	SourceQuery string // column source_query since 0001; renaming it would drop and re-add the column
	RefreshTime *time.Time
	// Columns is the []schema.Column the rows were read with, as jsonb.
	Columns      storm.JSON
	NumberTarget bool
	Skipped      int64
}

// Aggregates: how many datasets each team has.
func (d *Dataset) Aggregates(a *storm.Aggregates) {
	perTeam := a.Named("PerTeam")
	perTeam.By(&d.Team)
	perTeam.Count("N")
}

func (d *Dataset) Schema(t *storm.Table) {
	t.Col(&d.Kind).Immutable()
	t.Col(&d.Features).Default("'{}'")
	t.Col(&d.Labels).Default("'{}'")
	t.Col(&d.InputShape).Default("'{}'")
	t.Col(&d.Target).Default("''")
	t.Col(&d.SizeBytes).Default("0")
	t.Col(&d.Driver).Default("''").Immutable()
	t.Col(&d.DSN).Named("dsn").Default("''").Immutable()
	t.Col(&d.SourceQuery).Default("''").Immutable()
	t.Col(&d.Columns).Default("'[]'")
	t.Col(&d.NumberTarget).Default("false")
	t.Col(&d.Skipped).Default("0")
	// A team's datasets, newest first; also the team key's index.
	t.Col(&d.Team).NoIndex()
	t.Index(&d.Team, storm.Desc(&d.CreateTime), storm.Desc(&d.ID))
	t.Check(storm.RawSQL(between("kind", DatasetTable, DatasetPhotoTable)))
}
