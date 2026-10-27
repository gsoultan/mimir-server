package tables

import "github.com/gsoultan/storm"

// ModelVersion is one set of weights a model has had: its first upload or
// training run, and each keep-learning pass after. The model's own row
// describes the version that answers now (Model.VersionID); the others
// are kept so people can compare them and go back. Weights live on disk
// beside the model's, under versions/.
type ModelVersion struct {
	Record
	Model Model
	// Number counts a model's versions from 1, in the order they were made.
	Number    int32
	Source    VersionSource
	SizeBytes int64
	SHA256    string
	// What the model reads and answers with these weights, and how well,
	// copied back onto the model when this version is put live.
	InputShape []int64
	Labels     []string
	Features   []string
	// Columns is the []schema.Column these weights read, as jsonb.
	Columns  storm.JSON
	JobID    string
	Accuracy float64
	// Note says why the version exists or wasn't used, in plain words.
	Note string
}

func (v *ModelVersion) Schema(t *storm.Table) {
	// A model's versions in order, and their numbers unique; also the
	// model key's index, for the cascade.
	t.Col(&v.Model).OnDelete(storm.Cascade).NoIndex()
	t.Index(&v.Model, &v.Number).Unique()
	t.Col(&v.InputShape).Default("'{}'")
	t.Col(&v.Labels).Default("'{}'")
	t.Col(&v.Features).Default("'{}'")
	t.Col(&v.Columns).Default("'[]'")
	t.Col(&v.JobID).Default("''")
	t.Col(&v.Accuracy).Default("0")
	t.Col(&v.Note).Default("''")
	t.Check(storm.RawSQL(`number >= 1`))
	t.Check(storm.RawSQL(between("source", VersionUploaded, VersionKeepLearning)))
	t.Check(storm.RawSQL(`size_bytes > 0`))
	t.Check(storm.RawSQL(`length(sha256) = 64`))
	t.Check(storm.RawSQL(`length(note) <= 500`))
}
