package tables

import (
	"time"

	"github.com/gsoultan/storm"
)

// Answer is one prediction a model made, kept for a while so the app that
// asked can say what the right answer was. Answers with that feedback are
// how the server knows how well a model does in use.
type Answer struct {
	Record
	Model Model
	// VersionID is the ModelVersion that answered.
	VersionID string
	// Label is the answer a model that picks one gave; a number model
	// leaves it empty and answers with Value.
	Label      string
	Value      float64
	Confidence float64
	// Fields is the question by column name (map[string]string) as jsonb,
	// for a model asked that way; empty otherwise.
	Fields storm.JSON
	// What the answer should have been, from feedback, and whether the
	// model got it: the same label, or a number within 10% of it.
	FeedbackTime *time.Time
	TruthLabel   string
	TruthValue   float64
	Correct      bool
}

func (a *Answer) Schema(t *storm.Table) {
	// A model's answers, newest first; also the model key's index.
	t.Col(&a.Model).OnDelete(storm.Cascade).NoIndex()
	t.Index(&a.Model, storm.Desc(&a.CreateTime))
	t.Col(&a.VersionID).Default("''")
	t.Col(&a.Label).Default("''")
	t.Col(&a.Value).Default("0")
	t.Col(&a.Confidence).Default("0")
	t.Col(&a.Fields).Default("'{}'")
	t.Col(&a.TruthLabel).Default("''")
	t.Col(&a.TruthValue).Default("0")
	t.Col(&a.Correct).Default("false")
	// The retention trim; rows arrive in time order.
	t.Index(&a.CreateTime).Using(storm.BRIN)
	t.Check(storm.RawSQL(`length(label) <= 200`))
	t.Check(storm.RawSQL(`length(truth_label) <= 200`))
}

// AnswerDay counts a model's answers on one day (UTC), and how many of
// those got feedback and were right, for its charts and accuracy alerts.
// Counters outlive the answers themselves.
type AnswerDay struct {
	Model    Model
	Day      time.Time
	Answers  int64
	Feedback int64
	Correct  int64
}

func (d *AnswerDay) Schema(t *storm.Table) {
	// The key serves a model's chart (model, day range) and its cascade.
	t.PrimaryKey(&d.Model, &d.Day)
	t.Col(&d.Model).OnDelete(storm.Cascade).NoIndex()
	t.Col(&d.Answers).Default("0")
	t.Col(&d.Feedback).Default("0")
	t.Col(&d.Correct).Default("0")
	// Every model's last week, for the accuracy check.
	t.Index(&d.Day)
	t.Check(storm.RawSQL(`answers >= 0 AND feedback >= 0 AND correct >= 0`))
}
