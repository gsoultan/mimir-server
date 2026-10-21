package tables

import "github.com/gsoultan/storm"

// Architecture is a model type someone wrote in their own code and runs on
// workers, described here so the server knows what its models read and
// answer. The built-in types live in package arch, not in the database.
type Architecture struct {
	Record
	// Name is what workers run it under; it never changes.
	Name        string
	Title       string
	Description string
	// Input is the arch.Input it reads ("table", "image" or "tensor"), and
	// Output the arch.Output it answers ("class", "number" or "ranked").
	Input  string
	Output string
	// Items is how many a ranked answer lists.
	Items     int32
	CreatedBy string
}

func (a *Architecture) Schema(t *storm.Table) {
	t.Col(&a.Name).Unique().Immutable()
	t.Col(&a.Description).Default("''")
	t.Col(&a.Items).Default("0")
	t.Col(&a.CreatedBy).Default("''")
	t.Check(storm.RawSQL(`name ~ '^[a-z0-9][a-z0-9-]{0,39}$'`))
	t.Check(storm.RawSQL(`length(title) BETWEEN 1 AND 100`))
	t.Check(storm.RawSQL(`length(description) <= 500`))
	t.Check(storm.RawSQL(`input IN ('table', 'image', 'tensor')`))
	t.Check(storm.RawSQL(`output IN ('class', 'number', 'ranked')`))
	t.Check(storm.RawSQL(`items BETWEEN 0 AND 100`))
}
