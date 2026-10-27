package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/gsoultan/storm"
	"github.com/gsoultan/storm/runtime"

	"github.com/gsoultan/mimir-server/internal/store/db/answer"
	"github.com/gsoultan/mimir-server/internal/store/db/answerday"
)

// ErrAnswerNotFound reports that no kept answer has the requested id:
// it never existed, or is older than AnswerKeep.
var ErrAnswerNotFound = errors.New("store: answer not found")

// AnswerKeep is how long answers are kept for feedback. Their daily
// counts are kept for AnswerDayKeep.
const (
	AnswerKeep    = 30 * 24 * time.Hour
	AnswerDayKeep = 400 * 24 * time.Hour
)

// Answer is one prediction a model made.
type Answer struct {
	ID        string
	ModelID   string
	VersionID string
	Time      time.Time
	// Label is a picking model's answer; a number model's is Value.
	Label      string
	Value      float64
	Number     bool
	Confidence float64
	Fields     map[string]string
	// Feedback: what the answer should have been, and whether it was.
	Checked    bool
	TruthLabel string
	TruthValue float64
	Correct    bool
}

// Judge reports whether a's answer matches the truth: the same label, or
// a number within 10% of the real one (0.1 of it when it is 0), as a
// number model's training score counts it.
func (a Answer) Judge(label string, value float64) bool {
	if !a.Number {
		return a.Label == label
	}
	tolerance := 0.1 * math.Abs(value)
	if value == 0 {
		tolerance = 0.1
	}
	return math.Abs(a.Value-value) <= tolerance
}

func btoi(b bool) int {
	if b {
		return 1
	}
	return 0
}

// Day is the UTC day t falls on, as AnswerDay keys it.
func Day(t time.Time) time.Time {
	return t.UTC().Truncate(24 * time.Hour)
}

// AddAnswers stores answers and counts them on their days, in one
// transaction.
func (s *Store) AddAnswers(ctx context.Context, as []Answer) error {
	if len(as) == 0 {
		return nil
	}
	ops := make([]runtime.BatchOp, 0, len(as))
	for _, a := range as {
		fields, err := json.Marshal(a.Fields)
		if err != nil || a.Fields == nil {
			fields = []byte("{}")
		}
		n := answer.Create()
		n.SetID(a.ID)
		n.SetCreateTime(ms(a.Time))
		n.SetModelID(a.ModelID)
		n.SetVersionID(a.VersionID)
		n.SetLabel(a.Label)
		n.SetValue(a.Value)
		n.SetConfidence(a.Confidence)
		n.SetFields(runtime.JSON(fields))
		n.DoNothing() // a retried flush stores nothing twice
		op, err := n.Op()
		if err != nil {
			return fmt.Errorf("store: add answers: %w", err)
		}
		ops = append(ops, op)
	}
	err := storm.InTx(ctx, s.ex, func(ex storm.Executor) error {
		// Only answers stored now are counted, so a retry counts nothing twice.
		days := map[dayKey]int64{}
		err := ex.Batch(ctx, ops, func(i int, _ runtime.Rows, affected int64, err error) error {
			if err == nil && affected > 0 {
				days[dayKey{as[i].ModelID, Day(as[i].Time)}]++
			}
			return err
		})
		if err != nil {
			return err
		}
		counts := make([]runtime.BatchOp, 0, len(days))
		for k, n := range days {
			op, err := dayOp(k, n, 0, 0)
			if err != nil {
				return err
			}
			counts = append(counts, op)
		}
		return ex.Batch(ctx, counts, nil)
	})
	if err != nil {
		return fmt.Errorf("store: add answers: %w", err)
	}
	return nil
}

type dayKey struct {
	model string
	day   time.Time
}

func dayOp(k dayKey, answers, feedback, correct int64) (runtime.BatchOp, error) {
	d := answerday.Create()
	d.SetModelID(k.model)
	d.SetDay(k.day)
	d.AddAnswersOnConflict(answers)
	d.AddFeedbackOnConflict(feedback)
	d.AddCorrectOnConflict(correct)
	op, err := d.OnConflictModelIDDay().Op()
	if err != nil {
		return runtime.BatchOp{}, fmt.Errorf("store: count answers: %w", err)
	}
	return op, nil
}

func fromAnswer(r answer.Row) Answer {
	a := Answer{
		ID: r.ID, ModelID: r.ModelID, VersionID: r.VersionID, Time: r.CreateTime.UTC(),
		Label: r.Label, Value: r.Value, Number: r.Label == "", Confidence: r.Confidence,
		Checked: r.FeedbackTime.Valid, TruthLabel: r.TruthLabel, TruthValue: r.TruthValue, Correct: r.Correct,
	}
	json.Unmarshal(r.Fields, &a.Fields)
	return a
}

// GetAnswer returns a kept answer, or ErrAnswerNotFound.
func (s *Store) GetAnswer(ctx context.Context, id string) (Answer, error) {
	r, ok, err := answer.New().Where(answer.ID.Eq(id)).One(ctx, s.ex)
	if err != nil {
		return Answer{}, fmt.Errorf("store: get answer: %w", err)
	}
	if !ok {
		return Answer{}, ErrAnswerNotFound
	}
	return fromAnswer(r), nil
}

// SetFeedback records what an answer should have been and returns the
// answer judged by it. Feedback sent again replaces the earlier one, and
// the day's counts follow.
func (s *Store) SetFeedback(ctx context.Context, id, label string, value float64) (Answer, error) {
	var a Answer
	err := storm.InTx(ctx, s.ex, func(ex storm.Executor) error {
		r, ok, err := answer.New().Where(answer.ID.Eq(id)).ForUpdate().One(ctx, ex)
		if err != nil {
			return fmt.Errorf("store: get answer: %w", err)
		}
		if !ok {
			return ErrAnswerNotFound
		}
		a = fromAnswer(r)
		was, wasRight := a.Checked, a.Correct
		a.Checked, a.TruthLabel, a.TruthValue = true, label, value
		a.Correct = a.Judge(label, value)
		u := answer.MutateKey(id)
		u.SetFeedbackTime(now())
		u.SetTruthLabel(label)
		u.SetTruthValue(value)
		u.SetCorrect(a.Correct)
		if err := u.Update(ctx, ex); err != nil {
			return fmt.Errorf("store: set feedback: %w", err)
		}
		feedback, correct := int64(1), int64(btoi(a.Correct))
		if was {
			feedback, correct = 0, correct-int64(btoi(wasRight))
		}
		op, err := dayOp(dayKey{a.ModelID, Day(a.Time)}, 0, feedback, correct)
		if err != nil {
			return err
		}
		return ex.Batch(ctx, []runtime.BatchOp{op}, nil)
	})
	if err != nil {
		return Answer{}, err
	}
	return a, nil
}

// ListAnswers returns a model's newest answers, up to limit.
func (s *Store) ListAnswers(ctx context.Context, modelID string, limit int) ([]Answer, error) {
	rows, err := answer.New().
		Where(answer.ModelID.Eq(modelID)).
		Order(answer.CreateTime.Desc()).
		Limit(int64(limit)).
		All(ctx, s.ex, nil)
	if err != nil {
		return nil, fmt.Errorf("store: list answers: %w", err)
	}
	out := make([]Answer, len(rows))
	for i, r := range rows {
		out[i] = fromAnswer(r)
	}
	return out, nil
}

// AnswerDay is a model's answers on one day and the feedback on them.
type AnswerDay struct {
	ModelID  string
	Day      time.Time
	Answers  int64
	Feedback int64
	Correct  int64
}

// AnswerDays returns a model's daily counts from since on, oldest first;
// with modelID "", every model's.
func (s *Store) AnswerDays(ctx context.Context, modelID string, since time.Time) ([]AnswerDay, error) {
	rows, err := answerday.New().
		WhereIf(modelID != "", answerday.ModelID.Eq(modelID)).
		Where(answerday.Day.Gte(Day(since))).
		Order(answerday.Day.Asc(), answerday.ModelID.Asc()).
		Limit(100_000).
		All(ctx, s.ex, nil)
	if err != nil {
		return nil, fmt.Errorf("store: answer days: %w", err)
	}
	out := make([]AnswerDay, len(rows))
	for i, r := range rows {
		out[i] = AnswerDay{ModelID: r.ModelID, Day: r.Day.UTC(), Answers: r.Answers, Feedback: r.Feedback, Correct: r.Correct}
	}
	return out, nil
}

// trimAnswers removes answers past AnswerKeep and counts past
// AnswerDayKeep.
func (s *Store) trimAnswers(ctx context.Context, t time.Time) (int64, error) {
	a, err := answer.New().Where(answer.CreateTime.Lt(t.Add(-AnswerKeep))).DeleteAll(ctx, s.ex)
	if err != nil {
		return 0, err
	}
	d, err := answerday.New().Where(answerday.Day.Lt(t.Add(-AnswerDayKeep))).DeleteAll(ctx, s.ex)
	return a + d, err
}
