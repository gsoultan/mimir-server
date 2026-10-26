package store

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestAnswers(t *testing.T) {
	ctx := context.Background()
	s := open(t)
	m, err := s.Create(ctx, Model{Name: "price", Architecture: "table-mlp"}, []byte("w"))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	as := []Answer{
		{ID: "01900000-0000-7000-8000-000000000001", ModelID: m.ID, VersionID: m.VersionID, Time: now, Number: true, Value: 100, Fields: map[string]string{"rooms": "3"}},
		{ID: "01900000-0000-7000-8000-000000000002", ModelID: m.ID, Time: now, Number: true, Value: 50},
		{ID: "01900000-0000-7000-8000-000000000003", ModelID: m.ID, Time: now.Add(-48 * time.Hour), Number: true, Value: 7},
	}
	if err := s.AddAnswers(ctx, as); err != nil {
		t.Fatal(err)
	}
	// A retried flush neither fails nor counts twice.
	if err := s.AddAnswers(ctx, as[:1]); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetAnswer(ctx, as[0].ID)
	if err != nil || got.Value != 100 || !got.Number || got.Fields["rooms"] != "3" || got.Checked {
		t.Fatalf("GetAnswer = %+v, %v", got, err)
	}
	// Within 10% is right; further is wrong; sent again, it is replaced.
	if a, err := s.SetFeedback(ctx, as[0].ID, "", 105); err != nil || !a.Correct {
		t.Fatalf("SetFeedback close = %+v, %v", a, err)
	}
	if a, err := s.SetFeedback(ctx, as[1].ID, "", 80); err != nil || a.Correct {
		t.Fatalf("SetFeedback far = %+v, %v", a, err)
	}
	if a, err := s.SetFeedback(ctx, as[1].ID, "", 52); err != nil || !a.Correct {
		t.Fatalf("SetFeedback again = %+v, %v", a, err)
	}
	if _, err := s.SetFeedback(ctx, "01900000-0000-7000-8000-0000000000ff", "", 1); !errors.Is(err, ErrAnswerNotFound) {
		t.Fatalf("SetFeedback missing: %v", err)
	}
	days, err := s.AnswerDays(ctx, m.ID, now.Add(-7*24*time.Hour))
	if err != nil || len(days) != 2 {
		t.Fatalf("AnswerDays = %+v, %v", days, err)
	}
	if d := days[1]; d.Answers != 2 || d.Feedback != 2 || d.Correct != 2 || days[0].Answers != 1 {
		t.Fatalf("days = %+v", days)
	}
	if list, err := s.ListAnswers(ctx, m.ID, 10); err != nil || len(list) != 3 || list[0].Time.Before(list[2].Time) {
		t.Fatalf("ListAnswers = %+v, %v", list, err)
	}
}
