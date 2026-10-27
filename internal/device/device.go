// Package device answers with models in a browser. cmd/mimir-wasm wraps it
// for JavaScript; it is plain Go so it is tested like the rest.
package device

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/gsoultan/mimir-server/internal/arch"
	"github.com/gsoultan/mimir-server/internal/ask"
	"github.com/gsoultan/mimir-server/internal/runtime"
	"github.com/gsoultan/mimir-server/internal/schema"
)

// Model is what the page knows about a model: the parts of the server's
// Model message that say how to read a question and name the answers.
type Model struct {
	ID           string          `json:"id"`
	Version      string          `json:"version"`
	Architecture string          `json:"architecture"`
	InputShape   []int           `json:"inputShape"`
	Labels       []string        `json:"labels"`
	Features     []string        `json:"features"`
	Columns      []schema.Column `json:"columns"`
}

// Question is one thing to ask: fields by column name, a photo, a sound,
// or the numbers themselves.
type Question struct {
	Fields map[string]string `json:"fields"`
	Photo  []byte            `json:"-"`
	Sound  []byte            `json:"-"`
	Input  []float32         `json:"input"`
	TopK   int               `json:"topK"`
}

// Guess is one answer and how sure the model is of it.
type Guess struct {
	Label       string  `json:"label"`
	Probability float32 `json:"probability"`
}

// Answer is what the model said.
type Answer struct {
	Number  bool    `json:"number,omitempty"`
	Value   float64 `json:"value,omitempty"`
	Guesses []Guess `json:"guesses,omitempty"`
	// Values is a decision model's expected reward of each action, in the
	// order of its labels.
	Values []float32 `json:"values,omitempty"`
}

// ErrNotLoaded is what Predict returns for a model not loaded yet.
var ErrNotLoaded = errors.New("Load the model first.")

// Runner holds the models loaded on this device.
type Runner struct {
	cache  *runtime.Cache
	models map[string]loaded
}

type loaded struct {
	m       Model
	a       arch.Architecture
	spec    arch.Spec
	weights []byte // kept until the first answer builds the model
}

// New returns a Runner with no models loaded.
func New() *Runner { return &Runner{cache: runtime.NewCache(0), models: map[string]loaded{}} }

// Load readies model m with its weights file, so the next answer builds it.
func (r *Runner) Load(m Model, weights []byte) error {
	a, ok := arch.Lookup(m.Architecture)
	switch {
	case !ok:
		return fmt.Errorf("This browser doesn't know the model type %s.", m.Architecture)
	case a.Custom || a.Answers() == arch.Written:
		return fmt.Errorf("%s models run on the server only.", a.Title)
	}
	r.models[m.ID] = loaded{m: m, a: a, spec: arch.Resolve(a, arch.Spec{InputShape: m.InputShape, Labels: m.Labels}), weights: weights}
	// Build it now, so a bad file is found at load and not at the first
	// question.
	_, err := r.Predict(context.Background(), m.ID, Question{Input: make([]float32, size(r.models[m.ID].spec.InputShape)), TopK: 1})
	if err != nil {
		delete(r.models, m.ID)
		return err
	}
	return nil
}

// Unload forgets a model.
func (r *Runner) Unload(id string) { delete(r.models, id) }

// Predict asks model id question q.
func (r *Runner) Predict(ctx context.Context, id string, q Question) (Answer, error) {
	l, ok := r.models[id]
	if !ok {
		return Answer{}, ErrNotLoaded
	}
	in := q.Input
	if len(in) == 0 {
		var err error
		if in, err = ask.Encode(l.a, l.m.Columns, l.m.Features, q.Fields, q.Photo, q.Sound); err != nil {
			return Answer{}, errors.New(ask.Reason(err))
		}
	}
	if want := size(l.spec.InputShape); len(in) != want {
		return Answer{}, fmt.Errorf("Send %d numbers; got %d.", want, len(in))
	}
	load := func() ([]byte, error) {
		w := l.weights
		l.weights = nil
		r.models[id] = l
		return w, nil
	}
	res, err := r.cache.Predict(ctx, id, l.m.Version, l.a, l.spec, load, in)
	if err != nil {
		return Answer{}, err
	}
	if l.a.Answers() == arch.Number {
		a := Answer{Number: true}
		if len(res.Output) > 0 {
			a.Value = float64(res.Output[0])
		}
		return a, nil
	}
	k := q.TopK
	if k <= 0 {
		k = 3
		if l.a.Answers() == arch.Ranked && l.a.Items > 0 {
			k = l.a.Items
		}
	}
	var out Answer
	for _, g := range res.Top(l.spec.Labels, k) {
		out.Guesses = append(out.Guesses, Guess{Label: g.Label, Probability: g.Probability})
	}
	if l.a.Target == arch.ActionTarget {
		out.Values = res.Output
	}
	return out, nil
}

// ParseModel reads a Model the page sent as JSON.
func ParseModel(s string) (Model, error) {
	var m Model
	if err := json.Unmarshal([]byte(s), &m); err != nil {
		return Model{}, fmt.Errorf("device: bad model: %w", err)
	}
	return m, nil
}

func size(shape []int) int {
	n := 1
	for _, d := range shape {
		n *= d
	}
	return n
}
