package tables

import (
	"time"

	"github.com/gsoultan/storm"
)

// Worker is a process that runs models for this server. Only its token's
// hash is kept; PublicKey is the Ed25519 key it joined with.
type Worker struct {
	Record
	Named
	TokenHash   string
	CreatedBy   string
	LastSeen    *time.Time
	PublicKey   []byte
	LastAddress string
	// Tasks is the work the console gives the worker, set from its page:
	// "all", "train" or "serve"; "" leaves it to the worker's own -tasks.
	Tasks string
}

func (w *Worker) Schema(t *storm.Table) {
	t.Col(&w.TokenHash).Unique() // every connection's lookup; replaced when the worker is re-registered
	t.Col(&w.CreatedBy).Immutable()
	t.Col(&w.LastAddress).Default("''")
	t.Col(&w.Tasks).Default("''")
}
