package tables

import (
	"time"

	"github.com/gsoultan/storm"
)

// Document is a file in a collection, kept in the data folder and cut
// into passages in the background: queued, running, then succeeded or
// failed. Deleting its collection deletes it.
type Document struct {
	Record
	Named
	TeamOwned
	Collection Collection
	// Kind is how the file is read: text, markdown, html, docx or pdf.
	Kind      string
	Bytes     int64
	Status    JobStatus
	Error     string
	Passages  int64
	CreatedBy string
	EndTime   *time.Time
}

func (d *Document) Schema(t *storm.Table) {
	t.Col(&d.Collection).OnDelete(storm.Cascade).Immutable().NoIndex()
	t.Col(&d.Kind).Immutable()
	t.Col(&d.Bytes).Default("0").Immutable()
	t.Col(&d.Error).Default("''")
	t.Col(&d.Passages).Default("0")
	t.Col(&d.CreatedBy).Default("''").Immutable()
	t.Col(&d.Team).NoIndex()
	// A collection's documents, newest first; also the collection key's.
	t.Index(&d.Collection, storm.Desc(&d.CreateTime), storm.Desc(&d.ID))
	// A team's documents, for counting them per collection.
	t.Index(&d.Team, &d.Collection)
	// The queue: the oldest queued document is read first.
	t.Index(&d.Status, &d.CreateTime, &d.ID)
	t.Check(storm.RawSQL(between("status", JobQueued, JobCancelled)))
	t.Check(storm.RawSQL(`bytes >= 0 AND passages >= 0`))
}
