package api

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"google.golang.org/protobuf/types/known/timestamppb"

	mimirv1 "github.com/gsoultan/mimir-server/gen/mimir/v1"
	"github.com/gsoultan/mimir-server/internal/arch"
	"github.com/gsoultan/mimir-server/internal/store"
)

// Answers are written to the store in batches, like usage counts. At most
// maxPendingAnswers wait for the next flush; past that, answers are still
// given but not kept, so a slow database never holds up predictions.
const maxPendingAnswers = 20_000

// A model has dropped when, over at least qualityMinFeedback answers with
// feedback in the last 7 days, it gets more than qualityDrop fewer right
// than its held-out score.
const (
	qualityMinFeedback = 20
	qualityDrop        = 0.10
	qualityWeek        = 7 * 24 * time.Hour
)

// answerLog holds answers until FlushAnswers writes them.
type answerLog struct {
	mu      sync.Mutex
	pending []store.Answer
	ids     map[string]bool
	dropped int64
}

// keepAnswer queues a for the store and returns its id, or "" when the
// queue is full.
func (s *Service) keepAnswer(a store.Answer) string {
	id, err := uuid.NewV7()
	if err != nil {
		return ""
	}
	a.ID = id.String()
	l := &s.answers
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.pending) >= maxPendingAnswers {
		l.dropped++
		return ""
	}
	if l.ids == nil {
		l.ids = map[string]bool{}
	}
	l.pending = append(l.pending, a)
	l.ids[a.ID] = true
	return a.ID
}

// FlushAnswers writes queued answers to the store. The server calls it
// every few seconds and on shutdown.
func (s *Service) FlushAnswers(ctx context.Context) error {
	l := &s.answers
	l.mu.Lock()
	batch, dropped := l.pending, l.dropped
	l.pending, l.ids, l.dropped = nil, nil, 0
	l.mu.Unlock()
	if dropped > 0 {
		s.log.WarnContext(ctx, "answers: queue full, some not kept for feedback", "dropped", dropped)
	}
	if len(batch) == 0 {
		return nil
	}
	if err := s.store.AddAnswers(ctx, batch); err != nil {
		l.mu.Lock() // keep them for next time, as room allows
		if l.ids == nil {
			l.ids = map[string]bool{}
		}
		for _, a := range batch {
			if len(l.pending) >= maxPendingAnswers {
				break
			}
			l.pending = append(l.pending, a)
			l.ids[a.ID] = true
		}
		l.mu.Unlock()
		return err
	}
	return nil
}

// queued reports whether an answer is still waiting to be written.
func (s *Service) queued(id string) bool {
	s.answers.mu.Lock()
	defer s.answers.mu.Unlock()
	return s.answers.ids[id]
}

func (s *Service) SendFeedback(ctx context.Context, req *connect.Request[mimirv1.SendFeedbackRequest]) (*connect.Response[mimirv1.SendFeedbackResponse], error) {
	id := req.Msg.GetPredictionId()
	if !store.ValidID(id) {
		return nil, predictionNotFound(id)
	}
	a, err := s.store.GetAnswer(ctx, id)
	if errors.Is(err, store.ErrAnswerNotFound) && s.queued(id) {
		// Feedback sent right after the prediction: write it now.
		if err := s.FlushAnswers(ctx); err != nil {
			return nil, s.internal(ctx, err)
		}
		a, err = s.store.GetAnswer(ctx, id)
	}
	if errors.Is(err, store.ErrAnswerNotFound) {
		return nil, predictionNotFound(id)
	}
	if err != nil {
		return nil, s.internal(ctx, err)
	}
	// The caller must still be allowed the model; another team's answer
	// reads as missing.
	m, err := s.get(ctx, a.ModelID)
	if err != nil {
		return nil, predictionNotFound(id)
	}
	labels := m.Labels
	if a, ok := arch.Lookup(m.Architecture); ok {
		labels = arch.Resolve(a, arch.Spec{InputShape: m.InputShape, Labels: m.Labels}).Labels
	}
	label := req.Msg.GetLabel()
	if a.Number && label != "" {
		return nil, invalid("%s answers with a number: send the right one as value.", m.Name)
	}
	if !a.Number {
		if label == "" {
			return nil, invalid("Send the right answer as label, one of: %s.", joinLabels(labels))
		}
		if len(labels) > 0 && !contains(labels, label) {
			return nil, invalid("%q isn't one of %s's answers: %s.", label, m.Name, joinLabels(labels))
		}
	}
	a, err = s.store.SetFeedback(ctx, id, label, req.Msg.GetValue())
	if errors.Is(err, store.ErrAnswerNotFound) {
		return nil, predictionNotFound(id)
	}
	if err != nil {
		return nil, s.internal(ctx, err)
	}
	return connect.NewResponse(&mimirv1.SendFeedbackResponse{Correct: a.Correct}), nil
}

// maxKeptFields is the most a question's fields may take, keys and values,
// to be kept with its answer; a larger one is answered but kept without
// them.
const maxKeptFields = 8 << 10

// keptFields returns the fields a question was asked with, to keep with
// its answer, or nil when they are too large.
func keptFields(f map[string]string) map[string]string {
	n := 0
	for k, v := range f {
		if n += len(k) + len(v); n > maxKeptFields {
			return nil
		}
	}
	return f
}

func predictionNotFound(id string) error {
	return connect.NewError(connect.CodeNotFound, fmt.Errorf("No prediction with id %q is kept: predictions are kept for feedback for 30 days.", id))
}

func joinLabels(labels []string) string {
	const most = 10
	if len(labels) > most {
		return fmt.Sprintf("%s and %d more", joinLabels(labels[:most]), len(labels)-most)
	}
	out := ""
	for i, l := range labels {
		if i > 0 {
			out += ", "
		}
		out += l
	}
	return out
}

func (s *Service) GetModelQuality(ctx context.Context, req *connect.Request[mimirv1.GetModelQualityRequest]) (*connect.Response[mimirv1.GetModelQualityResponse], error) {
	m, err := s.get(ctx, req.Msg.GetModelId())
	if err != nil {
		return nil, err
	}
	days := int(req.Msg.GetDays())
	switch {
	case days == 0:
		days = 30
	case days < 0 || days > 365:
		return nil, invalid("days must be between 1 and 365.")
	}
	now := time.Now()
	rows, err := s.store.AnswerDays(ctx, m.ID, now.Add(-time.Duration(days-1)*24*time.Hour))
	if err != nil {
		return nil, s.internal(ctx, err)
	}
	res := &mimirv1.GetModelQualityResponse{}
	for _, d := range rows {
		res.Days = append(res.Days, &mimirv1.QualityDay{Day: timestamppb.New(d.Day), Answers: d.Answers, Feedback: d.Feedback, Correct: d.Correct})
	}
	q := week(rows, now)
	res.WeekAnswers, res.WeekFeedback, res.WeekAccuracy = q.answers, q.feedback, q.accuracy()
	res.Dropped = q.dropped(m.Accuracy)
	return connect.NewResponse(res), nil
}

// tally is a model's answers and feedback over some days.
type tally struct{ answers, feedback, correct int64 }

func (t tally) accuracy() float64 {
	if t.feedback == 0 {
		return 0
	}
	return float64(t.correct) / float64(t.feedback)
}

// dropped reports whether t is enough feedback to say the model does
// clearly worse in use than its held-out score.
func (t tally) dropped(trained float64) bool {
	return trained > 0 && t.feedback >= qualityMinFeedback && t.accuracy()+1e-9 < trained-qualityDrop
}

// week sums the days of the last 7, today included.
func week(days []store.AnswerDay, now time.Time) tally {
	from := store.Day(now.Add(-qualityWeek + 24*time.Hour))
	var t tally
	for _, d := range days {
		if !d.Day.Before(from) {
			t.answers += d.Answers
			t.feedback += d.Feedback
			t.correct += d.Correct
		}
	}
	return t
}

// CheckQuality alerts owners, and sends a model.accuracy_dropped webhook,
// for each model doing clearly worse in use than its held-out score; at
// most once a day for each. The server calls it every hour.
func (s *Service) CheckQuality(ctx context.Context) error {
	now := time.Now()
	rows, err := s.store.AnswerDays(ctx, "", now.Add(-qualityWeek))
	if err != nil {
		return err
	}
	per := map[string][]store.AnswerDay{}
	for _, d := range rows {
		per[d.ModelID] = append(per[d.ModelID], d)
	}
	today := store.Day(now).Format(time.DateOnly)
	for id, days := range per {
		q := week(days, now)
		if q.feedback < qualityMinFeedback {
			continue
		}
		m, err := s.store.Get(ctx, id)
		if err != nil || !q.dropped(m.Accuracy) {
			continue
		}
		key := "quality_alert:" + id
		if last, _ := s.store.GetSetting(ctx, key); last == today {
			continue
		}
		if err := s.store.SetSetting(ctx, key, today); err != nil {
			return err
		}
		live, trained := q.accuracy()*100, m.Accuracy*100
		if s.Notifier != nil {
			s.Notifier.Notify(ctx, "Accuracy dropped: "+m.Name, fmt.Sprintf(
				"%s got %.0f%% of the last week's answers right, by the feedback apps sent on %d of them. It got %.0f%% right when it was trained.\n\nLook at its versions on the model's page, or teach it again with newer examples.",
				m.Name, live, q.feedback, trained))
		}
		s.emitIn(ctx, m.TeamID, "model.accuracy_dropped", map[string]any{
			"model":    map[string]any{"id": m.ID, "name": m.Name, "architecture": m.Architecture, "version_id": m.VersionID},
			"week":     map[string]any{"answers": q.answers, "feedback": q.feedback, "accuracy": q.accuracy()},
			"accuracy": m.Accuracy,
		})
		s.log.WarnContext(ctx, "model accuracy dropped", "model_id", m.ID, "week_accuracy", q.accuracy(), "trained_accuracy", m.Accuracy, "feedback", q.feedback)
	}
	return nil
}
