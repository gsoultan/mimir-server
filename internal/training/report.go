package training

import (
	"context"
	"fmt"
	"math"
	"slices"
	"time"

	"github.com/gsoultan/mimir/data"
	"github.com/gsoultan/mimir/metrics"
	"github.com/gsoultan/mimir/nn"
	"github.com/gsoultan/mimir/tensor"
	"github.com/gsoultan/mimir/train"

	"github.com/gsoultan/mimir-server/internal/arch"
	"github.com/gsoultan/mimir-server/internal/store"
)

const (
	// maxConfusion is the most labels a report keeps a confusion table
	// for; maxClasses the most it scores one by one.
	maxConfusion = 30
	maxClasses   = 200
	// maxPoints is how many real answers and guesses a number model's
	// report keeps for its chart.
	maxPoints = 200
	// defaultK is how many suggestions a ranked model is scored on when
	// its architecture doesn't say.
	defaultK = 5
)

// Score scores a trained model on a set of examples in detail, for its
// kind of answer.
func Score(ctx context.Context, model nn.Module, set data.Dataset, a arch.Architecture, labels []string) (*store.Report, error) {
	sc := &scorer{kind: string(a.Answers()), labels: labels}
	switch a.Answers() {
	case arch.Number:
	case arch.Ranked:
		sc.k = a.Items
		if sc.k <= 0 {
			sc.k = defaultK
		}
		fallthrough
	case arch.Class:
		n := len(labels)
		sc.confusion = make([][]int64, n)
		for i := range sc.confusion {
			sc.confusion[i] = make([]int64, n)
		}
		sc.k = min(sc.k, n)
		sc.hits = make([]float64, sc.k)
	default:
		return nil, fmt.Errorf("training: no report for %s models", a.Answers())
	}
	start := time.Now()
	if _, err := train.Evaluate(ctx, model, set, nil, func() metrics.Metric { return sc }); err != nil {
		return nil, err
	}
	r := sc.report()
	if r.Examples > 0 {
		r.MillisPerExample = float64(time.Since(start).Microseconds()) / 1000 / float64(r.Examples)
	}
	return r, nil
}

// scorer collects every prediction as train.Evaluate passes the batches
// by, then sums them up into a report.
type scorer struct {
	kind   string
	labels []string
	n      int64

	// class and ranked
	confusion [][]int64
	logLoss   float64
	k         int
	hits      []float64 // hits[i]: right answer in the first i+1
	rr, ndcg  float64

	// number
	absSum, sqSum   float64
	realSum, realSq float64
	within          float64
	points          []store.Point
}

func (s *scorer) Name() string   { return "report" }
func (s *scorer) Value() float64 { return 0 }

func (s *scorer) Update(ctx context.Context, pred, target *tensor.Tensor) error {
	if s.kind == string(arch.Number) {
		return pairs(ctx, pred, target, func(p, t float64) {
			d := p - t
			s.absSum += math.Abs(d)
			s.sqSum += d * d
			s.realSum += t
			s.realSq += t * t
			if math.Abs(d) <= 0.1*math.Abs(t) {
				s.within++
			}
			if len(s.points) < maxPoints {
				s.points = append(s.points, store.Point{Real: t, Guess: p})
			}
			s.n++
		})
	}
	p, err := pred.Data[float32](ctx)
	if err != nil {
		return err
	}
	t, err := target.Data[int32](ctx)
	if err != nil {
		return err
	}
	c := len(s.labels)
	if c == 0 || len(p) != len(t)*c {
		return fmt.Errorf("training: %d scores for %d answers of %d labels", len(p), len(t), c)
	}
	order := make([]int, c)
	for i, real := range t {
		row := p[i*c : (i+1)*c]
		if int(real) < 0 || int(real) >= c {
			continue
		}
		guess := argmax(row)
		s.confusion[real][guess]++
		s.logLoss -= logSoftmax(row, int(real))
		if s.k > 0 {
			for j := range order {
				order[j] = j
			}
			slices.SortStableFunc(order, func(a, b int) int {
				switch {
				case row[a] > row[b]:
					return -1
				case row[a] < row[b]:
					return 1
				}
				return 0
			})
			if rank := slices.Index(order, int(real)); rank >= 0 {
				s.rr += 1 / float64(rank+1)
				if rank < s.k {
					s.ndcg += 1 / math.Log2(float64(rank+2))
					for j := rank; j < s.k; j++ {
						s.hits[j]++
					}
				}
			}
		}
		s.n++
	}
	return nil
}

func (s *scorer) report() *store.Report {
	r := &store.Report{Kind: s.kind, Examples: int(s.n)}
	if s.n == 0 {
		return r
	}
	n := float64(s.n)
	if s.kind == string(arch.Number) {
		r.MAE, r.RMSE, r.Within10, r.Points = s.absSum/n, math.Sqrt(s.sqSum/n), s.within/n, s.points
		// R² compares the errors with always answering the average.
		if spread := s.realSq - s.realSum*s.realSum/n; spread > 0 {
			r.R2 = 1 - s.sqSum/spread
		}
		return r
	}
	var right, f1Sum float64
	labelled := 0
	for i, label := range s.labels {
		var guessed, real int64
		for j := range s.labels {
			guessed += s.confusion[j][i]
			real += s.confusion[i][j]
		}
		tp := float64(s.confusion[i][i])
		right += tp
		cs := store.ClassScore{Label: label, Support: real}
		if guessed > 0 {
			cs.Precision = tp / float64(guessed)
		}
		if real > 0 {
			cs.Recall = tp / float64(real)
			labelled++
		}
		if cs.Precision+cs.Recall > 0 {
			cs.F1 = 2 * cs.Precision * cs.Recall / (cs.Precision + cs.Recall)
		}
		if real > 0 {
			f1Sum += cs.F1
		}
		if len(s.labels) <= maxClasses {
			r.Classes = append(r.Classes, cs)
		}
	}
	r.Accuracy, r.LogLoss = right/n, s.logLoss/n
	if labelled > 0 {
		r.MacroF1 = f1Sum / float64(labelled)
	}
	if len(s.labels) <= maxConfusion {
		r.Confusion = s.confusion
	}
	if s.kind == string(arch.Ranked) {
		r.K, r.MRR, r.NDCG = s.k, s.rr/n, s.ndcg/n
		for _, h := range s.hits {
			r.HitAt = append(r.HitAt, h/n)
		}
	}
	return r
}

func argmax(row []float32) int {
	best := 0
	for i, v := range row {
		if v > row[best] {
			best = i
		}
	}
	return best
}

// logSoftmax is the log of the probability the scores give label i.
func logSoftmax(row []float32, i int) float64 {
	top := float64(row[argmax(row)])
	var sum float64
	for _, v := range row {
		sum += math.Exp(float64(v) - top)
	}
	return float64(row[i]) - top - math.Log(sum)
}
