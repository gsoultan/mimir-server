package tables

import "github.com/gsoultan/storm"

// Webhook is an endpoint that gets told about events. Events lists the
// event types it wants; empty means every event.
type Webhook struct {
	Record
	Named
	TeamOwned
	URL     string
	Secret  string
	Events  []string
	Enabled bool
}

func (w *Webhook) Schema(t *storm.Table) {
	t.Col(&w.Secret).Immutable()
	t.Col(&w.Events).Default("'{}'")
	t.Col(&w.Enabled).Default("true")
	// A team's webhooks go with it. The list, oldest first, is the team
	// key's index.
	t.Col(&w.Team).OnDelete(storm.Cascade).NoIndex()
	t.Index(&w.Team, &w.CreateTime, &w.ID)
	t.Check(storm.RawSQL(`length(url) BETWEEN 1 AND 2048`))
	t.Check(storm.RawSQL(`secret <> ''`))
}
