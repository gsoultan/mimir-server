package store

import (
	"context"
	"encoding/json"

	"github.com/gsoultan/mimir-server/internal/store/db/trainingjob"
)

// Report is how a finished run's model did on the examples kept aside,
// in more detail than each pass's accuracy: what someone needs to judge
// it. Which fields are set depends on Kind.
type Report struct {
	// Kind is "class" (picks a label), "number" or "ranked" (lists the
	// labels that fit best, as a recommender does).
	Kind string `json:"kind"`
	// Examples is how many were scored. OnTraining says nothing was kept
	// aside, so these are the examples it learned from.
	Examples   int  `json:"examples"`
	OnTraining bool `json:"on_training,omitempty"`
	// MillisPerExample is how long an answer took while scoring, in
	// batches, on the machine that trained it. SizeBytes is the model file.
	MillisPerExample float64 `json:"ms_per_example"`
	SizeBytes        int64   `json:"size_bytes"`

	// Class and ranked models.
	Accuracy float64 `json:"accuracy,omitempty"`
	// MacroF1 averages F1 over the labels, so a rare label counts as much
	// as a common one. LogLoss is how sure it was of the right answers.
	MacroF1 float64      `json:"macro_f1,omitempty"`
	LogLoss float64      `json:"log_loss,omitempty"`
	Classes []ClassScore `json:"classes,omitempty"`
	// Confusion counts each real label (row) against each guess (column),
	// in Classes' order; left out when there are too many labels to show.
	Confusion [][]int64 `json:"confusion,omitempty"`

	// Number models, in the answer's units.
	MAE      float64 `json:"mae,omitempty"`
	RMSE     float64 `json:"rmse,omitempty"`
	R2       float64 `json:"r2,omitempty"`
	Within10 float64 `json:"within10,omitempty"`
	// Points are some real answers and the model's guesses, for a chart.
	Points []Point `json:"points,omitempty"`

	// Ranked models: HitAt[i] is the share whose right answer was in the
	// first i+1 suggestions; MRR and NDCG reward it nearer the top.
	K     int       `json:"k,omitempty"`
	HitAt []float64 `json:"hit_at,omitempty"`
	MRR   float64   `json:"mrr,omitempty"`
	NDCG  float64   `json:"ndcg,omitempty"`
}

// ClassScore is how a model did on one label.
type ClassScore struct {
	Label     string  `json:"label"`
	Precision float64 `json:"precision"`
	Recall    float64 `json:"recall"`
	F1        float64 `json:"f1"`
	// Support is how many examples really had this label.
	Support int64 `json:"support"`
}

// Point is one real answer and the model's guess.
type Point struct {
	Real  float64 `json:"real"`
	Guess float64 `json:"guess"`
}

// SetJobReport records a finished run's report.
func (s *Store) SetJobReport(ctx context.Context, id string, r *Report) error {
	m := trainingjob.MutateKey(id)
	m.SetReport(reportDoc(r))
	return s.updateJob(ctx, m)
}

func reportDoc(r *Report) []byte {
	if r == nil {
		return []byte("{}")
	}
	b, err := json.Marshal(r)
	if err != nil { // only unsupported values fail, and these have none
		return []byte("{}")
	}
	return b
}

// reportOf reads a stored report; nil when there is none.
func reportOf(b []byte) *Report {
	var r Report
	if err := json.Unmarshal(b, &r); err != nil || r.Kind == "" {
		return nil
	}
	return &r
}
