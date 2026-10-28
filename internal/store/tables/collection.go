package tables

import (
	"github.com/gsoultan/storm"
)

// Collection is a team's set of documents to answer questions from. Its
// passages' embeddings are made by EmbeddingModel, so deleting that model
// deletes the collection; LanguageModelID, which writes the answers, is a
// plain-text reference that can be changed, and asking fails while it
// names a deleted model.
type Collection struct {
	Record
	Named
	TeamOwned
	EmbeddingModel  Model
	LanguageModelID string
	Instructions    string
	CreatedBy       string
}

func (c *Collection) Schema(t *storm.Table) {
	t.Col(&c.EmbeddingModel).OnDelete(storm.Cascade).Immutable() // indexed, for the cascade
	t.Col(&c.LanguageModelID).Default("''")
	t.Col(&c.Instructions).Default("''")
	t.Col(&c.CreatedBy).Default("''").Immutable()
	// A team's collections, by name; also the team key's index.
	t.Col(&c.Team).NoIndex()
	t.Index(&c.Team, &c.Name, &c.ID)
}
