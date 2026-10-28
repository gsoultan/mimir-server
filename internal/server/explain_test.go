package server_test

import (
	"context"
	"fmt"
	"math"
	"math/rand/v2"
	"strings"
	"testing"

	"connectrpc.com/connect"

	mimirv1 "github.com/gsoultan/mimir-server/gen/mimir/v1"
)

// sumOf adds up the effects of an explanation.
func sumOf(cs []*mimirv1.Contribution) float64 {
	var s float64
	for _, c := range cs {
		s += c.GetEffect()
	}
	return s
}

// TestExplainAnAnswer explains a table model's answers, exactly for a
// few columns and by sampling for many, and which columns matter overall.
func TestExplainAnAnswer(t *testing.T) {
	ctx := context.Background()
	srv := newServer(t)
	c := owner(t, srv)
	fruit, _ := trainFruit(t, c)

	res, err := c.predict.Explain(ctx, connect.NewRequest(&mimirv1.ExplainRequest{ModelId: fruit, Fields: map[string]string{"width": "1.2", "height": "0.9"}}))
	if err != nil {
		t.Fatal(err)
	}
	x := res.Msg
	if x.GetLabel() != "apple" || !x.GetExact() || x.GetNumber() || len(x.GetContributions()) != 2 || x.GetValue() < 0.5 {
		t.Fatalf("explanation %v", x)
	}
	if d := sumOf(x.GetContributions()) - (x.GetValue() - x.GetTypical()); math.Abs(d) > 1e-4 {
		t.Errorf("effects add up to %v off the answer less the typical row's", d)
	}
	for _, ct := range x.GetContributions() {
		if ct.GetEffect() <= 0 || ct.GetTypical() == "" || ct.GetValue() == "" {
			t.Errorf("contribution %v: a wide, tall fruit should look more like an apple", ct)
		}
	}
	pear, err := c.predict.Explain(ctx, connect.NewRequest(&mimirv1.ExplainRequest{ModelId: fruit, Fields: map[string]string{"width": "1.2", "height": "0.9"}, Label: "pear"}))
	if err != nil || pear.Msg.GetLabel() != "pear" || math.Abs(pear.Msg.GetValue()+x.GetValue()-1) > 1e-4 {
		t.Fatalf("explaining pear: %v, %v", pear, err)
	}
	if _, err := c.predict.Explain(ctx, connect.NewRequest(&mimirv1.ExplainRequest{ModelId: fruit, Label: "plum"})); code(err) != connect.CodeInvalidArgument {
		t.Errorf("explaining a label it doesn't answer: %v", err)
	}
	digits := createModel(t, c, "Digits", "mnist-mlp")
	if _, err := c.predict.Explain(ctx, connect.NewRequest(&mimirv1.ExplainRequest{ModelId: digits.GetId()})); code(err) != connect.CodeInvalidArgument {
		t.Errorf("explaining a photo model: %v", err)
	}

	all, err := c.predict.ExplainModel(ctx, connect.NewRequest(&mimirv1.ExplainModelRequest{ModelId: fruit}))
	if err != nil {
		t.Fatal(err)
	}
	if all.Msg.GetSource() != "examples" || all.Msg.GetRows() != 24 || len(all.Msg.GetColumns()) != 2 {
		t.Fatalf("overview %v", all.Msg)
	}
	if s := all.Msg.GetColumns()[0].GetShare() + all.Msg.GetColumns()[1].GetShare(); math.Abs(s-1) > 1e-6 {
		t.Errorf("shares add up to %v", s)
	}

	// Many columns: x0 decides, the rest are noise.
	r := rand.New(rand.NewPCG(3, 4))
	var b strings.Builder
	for i := range 12 {
		fmt.Fprintf(&b, "x%d,", i)
	}
	b.WriteString("plan,churn\n")
	for i := range 300 {
		x0 := r.NormFloat64()
		fmt.Fprintf(&b, "%.3f,", x0)
		for range 11 {
			fmt.Fprintf(&b, "%.3f,", r.NormFloat64())
		}
		plans := []string{"basic", "plus", "pro"}
		answer := "no"
		if x0 > 0 {
			answer = "yes"
		}
		fmt.Fprintf(&b, "%s,%s\n", plans[i%3], answer)
	}
	ds, err := c.datasets.CreateDataset(ctx, connect.NewRequest(&mimirv1.CreateDatasetRequest{Name: "Wide", Kind: mimirv1.DatasetKind_DATASET_KIND_TABLE, Csv: []byte(b.String())}))
	if err != nil {
		t.Fatal(err)
	}
	j, err := c.training.CreateJob(ctx, connect.NewRequest(&mimirv1.CreateJobRequest{Name: "Wide", Architecture: "table-mlp", DatasetId: ds.Msg.GetDataset().GetId(), Settings: &mimirv1.TrainingSettings{Epochs: 20, LearningRate: 0.01}}))
	if err != nil {
		t.Fatal(err)
	}
	runner.Tick(ctx)
	job, err := c.training.GetJob(ctx, connect.NewRequest(&mimirv1.GetJobRequest{Id: j.Msg.GetJob().GetId()}))
	if err != nil || job.Msg.GetJob().GetModelId() == "" {
		t.Fatalf("job %v, %v", job, err)
	}
	fields := map[string]string{"plan": "pro"}
	for i := range 12 {
		fields[fmt.Sprintf("x%d", i)] = fmt.Sprintf("%.2f", 0.3+float64(i)/10)
	}
	fields["x0"] = "2.5"
	wide, err := c.predict.Explain(ctx, connect.NewRequest(&mimirv1.ExplainRequest{ModelId: job.Msg.GetJob().GetModelId(), Fields: fields, Label: "yes"}))
	if err != nil {
		t.Fatal(err)
	}
	w := wide.Msg
	if w.GetExact() || len(w.GetContributions()) != 13 || w.GetContributions()[0].GetColumn() != "x0" {
		t.Fatalf("wide explanation: exact %v, %d columns, first %v", w.GetExact(), len(w.GetContributions()), w.GetContributions()[0])
	}
	if d := sumOf(w.GetContributions()) - (w.GetValue() - w.GetTypical()); math.Abs(d) > 1e-4 {
		t.Errorf("sampled effects add up to %v off", d)
	}
	again, _ := c.predict.Explain(ctx, connect.NewRequest(&mimirv1.ExplainRequest{ModelId: job.Msg.GetJob().GetModelId(), Fields: fields, Label: "yes"}))
	if math.Abs(again.Msg.GetContributions()[0].GetEffect()-w.GetContributions()[0].GetEffect()) > 1e-4 {
		t.Errorf("the same row got another explanation")
	}
}
