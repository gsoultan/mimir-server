package server_test

import (
	"context"
	"testing"

	"connectrpc.com/connect"

	mimirv1 "github.com/gsoultan/mimir-server/gen/mimir/v1"
)

// TestFeedbackAndQuality sends feedback on predictions and reads back how
// the model does in use.
func TestFeedbackAndQuality(t *testing.T) {
	srv := newServer(t)
	ctx := context.Background()
	own := owner(t, srv)
	m := createModel(t, own, "digits", "mnist-mlp")
	input := &mimirv1.Tensor{Values: make([]float32, 784)}

	var ids []string
	var label string
	for range 3 {
		p, err := own.predict.Predict(ctx, connect.NewRequest(&mimirv1.PredictRequest{ModelId: m.GetId(), Input: input}))
		if err != nil || p.Msg.GetPredictionId() == "" {
			t.Fatalf("Predict = %v, %v", p, err)
		}
		ids = append(ids, p.Msg.GetPredictionId())
		label = p.Msg.GetGuesses()[0].GetLabel()
	}
	other := "0"
	if label == "0" {
		other = "1"
	}

	// Feedback straight after the prediction finds it before the flush.
	f, err := own.predict.SendFeedback(ctx, connect.NewRequest(&mimirv1.SendFeedbackRequest{PredictionId: ids[0], Label: label}))
	if err != nil || !f.Msg.GetCorrect() {
		t.Fatalf("SendFeedback right = %v, %v", f, err)
	}
	if err := svc.FlushAnswers(ctx); err != nil {
		t.Fatal(err)
	}
	if f, err := own.predict.SendFeedback(ctx, connect.NewRequest(&mimirv1.SendFeedbackRequest{PredictionId: ids[1], Label: other})); err != nil || f.Msg.GetCorrect() {
		t.Fatalf("SendFeedback wrong = %v, %v", f, err)
	}
	// Sent again, feedback replaces the earlier one rather than counting twice.
	if _, err := own.predict.SendFeedback(ctx, connect.NewRequest(&mimirv1.SendFeedbackRequest{PredictionId: ids[1], Label: label})); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []*mimirv1.SendFeedbackRequest{
		{PredictionId: ids[2]},                       // no answer
		{PredictionId: ids[2], Label: "seventeen"},   // not one of its labels
		{PredictionId: ids[2], Label: "1", Value: 2}, // fine: value is ignored for labels
	} {
		_, err := own.predict.SendFeedback(ctx, connect.NewRequest(bad))
		if want := connect.CodeInvalidArgument; bad.GetLabel() == "1" {
			if err != nil {
				t.Fatalf("SendFeedback %v: %v", bad, err)
			}
		} else if code(err) != want {
			t.Fatalf("SendFeedback %v: %v", bad, err)
		}
	}
	if _, err := own.predict.SendFeedback(ctx, connect.NewRequest(&mimirv1.SendFeedbackRequest{PredictionId: m.GetId(), Label: label})); code(err) != connect.CodeNotFound {
		t.Fatalf("SendFeedback of no prediction: %v", err)
	}

	q, err := own.models.GetModelQuality(ctx, connect.NewRequest(&mimirv1.GetModelQualityRequest{ModelId: m.GetId()}))
	if err != nil {
		t.Fatal(err)
	}
	right := int64(2)
	if label == "1" {
		right = 3
	}
	if len(q.Msg.GetDays()) != 1 || q.Msg.GetWeekAnswers() != 3 || q.Msg.GetWeekFeedback() != 3 || q.Msg.GetDays()[0].GetCorrect() != right || q.Msg.GetDropped() {
		t.Fatalf("GetModelQuality = %v", q.Msg)
	}

	if err := svc.CheckQuality(ctx); err != nil {
		t.Fatal(err)
	}
}
