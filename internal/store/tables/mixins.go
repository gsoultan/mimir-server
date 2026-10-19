package tables

import (
	"time"

	"github.com/gsoultan/storm"
)

// Record is what every top-level row starts with: its id, a UUIDv7 the
// server makes (so ids sort by creation and need no round trip), and when
// it was made. storm keys a table on its id column, and neither column has
// an update setter: CreateTime is Immutable, and a primary key never has one.
//
// Ids stay text rather than storm's uuid type: every caller of package
// store, the API and the URLs speak them as strings, and the plain-text
// soft references (Model.JobID, TrainingJob.ParentID, ...) use "" for none.
type Record struct {
	ID         string
	CreateTime time.Time
}

func (r *Record) Schema(t *storm.Table) {
	t.Col(&r.CreateTime).Immutable()
}

// TeamOwned is the team a resource belongs to: set when the resource is
// made and never changed, so the column has no update setter. A team with
// resources left cannot be deleted (the default NO ACTION key), unless the
// resource says otherwise in its own Schema, which runs after this one.
//
// The column stays nullable, as shipped in 0001: package store always sets
// it, but tightening it to NOT NULL is a step migrate.Auto refuses as
// data-losing on a database that already has rows.
//
// A person's team is not this mixin: people move between teams
// (User.Team), resources do not.
type TeamOwned struct {
	Team *Team
}

func (o *TeamOwned) Schema(t *storm.Table) {
	t.Col(&o.Team).Immutable()
}

// Named is a display name: what people call a team, a model, a key. It is
// never empty and at most 100 characters; uniqueness, where a table wants
// it, is that table's own declaration.
type Named struct {
	Name string
}

func (n *Named) Schema(t *storm.Table) {
	t.Check(storm.RawSQL(`length(name) BETWEEN 1 AND 100`))
}
