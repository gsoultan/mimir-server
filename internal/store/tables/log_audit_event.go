package tables

import (
	"time"

	"github.com/gsoultan/storm"
)

// LogAuditEvent is the audit_events table as a separate log database keeps
// it: the same columns and indexes as AuditEvent, read and written through
// the same generated package, but with the team as a plain id, since the
// teams it names live in the main database. It is not part of All and has
// no generated package of its own.
//
//storm:ignore
type LogAuditEvent struct {
	ID     string
	Time   time.Time
	Actor  string
	Action string
	Target string
	Detail string
	TeamID *string
}

func (e *LogAuditEvent) Schema(t *storm.Table) {
	t.Name("audit_events")
	t.Col(&e.Time).Immutable()
	t.Col(&e.Actor).Immutable()
	t.Col(&e.Action).Immutable()
	t.Col(&e.Target).Default("''").Immutable()
	t.Col(&e.Detail).Default("''").Immutable()
	t.Index(&e.TeamID, storm.Desc(&e.ID))
	t.Index(&e.Target, storm.Desc(&e.Time), storm.Desc(&e.ID))
	t.Index(&e.Time).Using(storm.BRIN)
}

// LogAll is every model of a separate log database.
func LogAll() []any { return []any{&LogAuditEvent{}} }
