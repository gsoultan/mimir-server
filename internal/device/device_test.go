package device_test

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/gsoultan/mimir/io/mim"

	"github.com/gsoultan/mimir-server/internal/arch"
	"github.com/gsoultan/mimir-server/internal/device"
)

// TestRunner loads a table model the way the browser does and asks it by
// column name.
func TestRunner(t *testing.T) {
	a, _ := arch.Lookup("table-mlp")
	spec := arch.Spec{InputShape: []int{3}, Labels: []string{"stays", "leaves"}}
	var buf bytes.Buffer
	if err := mim.Save(context.Background(), &buf, a.New(spec), nil); err != nil {
		t.Fatal(err)
	}
	m, err := device.ParseModel(`{"id":"m1","version":"v1","architecture":"table-mlp","inputShape":[3],"labels":["stays","leaves"],
		"columns":[{"name":"age","kind":"number","fill":40},{"name":"plan","kind":"category","values":["basic","pro"]}]}`)
	if err != nil {
		t.Fatal(err)
	}
	r := device.New()
	ctx := context.Background()
	if _, err := r.Predict(ctx, "m1", device.Question{}); !errors.Is(err, device.ErrNotLoaded) {
		t.Fatalf("before load: %v", err)
	}
	if err := r.Load(m, []byte("not a model")); err == nil {
		t.Fatal("a bad file loaded")
	}
	if err := r.Load(m, buf.Bytes()); err != nil {
		t.Fatal(err)
	}
	ans, err := r.Predict(ctx, "m1", device.Question{Fields: map[string]string{"age": "31", "plan": "pro"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(ans.Guesses) != 2 || ans.Number {
		t.Fatalf("answer = %+v", ans)
	}
	if sum := ans.Guesses[0].Probability + ans.Guesses[1].Probability; sum < 0.99 || sum > 1.01 {
		t.Fatalf("probabilities add to %v", sum)
	}
	if _, err := r.Predict(ctx, "m1", device.Question{Fields: map[string]string{"age": "old"}}); err == nil || !strings.Contains(err.Error(), "needs a number") {
		t.Fatalf("bad number: %v", err)
	}
	if _, err := r.Predict(ctx, "m1", device.Question{Fields: map[string]string{"colour": "red"}}); err == nil || !strings.Contains(err.Error(), "no column") {
		t.Fatalf("unknown column: %v", err)
	}
	r.Unload("m1")
	if _, err := r.Predict(ctx, "m1", device.Question{Input: []float32{1, 0, 1}}); !errors.Is(err, device.ErrNotLoaded) {
		t.Fatalf("after unload: %v", err)
	}
}
