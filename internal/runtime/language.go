package runtime

import (
	"context"
	"errors"
	"fmt"
	"math"
	"math/rand/v2"
	"slices"
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"

	"github.com/gsoultan/mim/tokenizer"
	"github.com/gsoultan/mimir/io/mim"
	"github.com/gsoultan/mimir/tensor"
)

// IsLanguageModel reports whether b is a Mimir Model file of a language
// model: a graph that reads tokens at positions and answers logits, as mim
// builds for a GGUF model. It doesn't check that the model runs; see
// OpenLanguageModel.
func IsLanguageModel(b []byte) bool {
	m, err := mim.Parse(b)
	if err != nil {
		return false
	}
	defer m.Close()
	_, err = languageInputs(m)
	return err == nil
}

// languageInputs checks the inputs and output of a language model's graph
// and returns the longest block of tokens it reads in one run, its context
// length.
func languageInputs(m *mim.Model) (int, error) {
	ins, outs := m.Inputs(), m.Outputs()
	if len(ins) != 2 || ins[0].Name != "tokens" || ins[1].Name != "positions" || len(outs) != 1 || outs[0].Name != "logits" {
		return 0, fmt.Errorf("%w: not a language model: want tokens and positions in, logits out", ErrBadWeights)
	}
	t := ins[0].Shape
	if len(t) != 2 || t[0].Size != 1 || t[1].Size >= 0 || t[1].Bound <= 1 {
		return 0, fmt.Errorf("%w: tokens input %s isn't one block of up to a context of tokens", ErrBadWeights, ins[0])
	}
	return t[1].Bound, nil
}

// LanguageModel is a loaded Mimir Model file of a language model, which
// continues a prompt one token at a time.
type LanguageModel struct {
	m       *mim.Model
	tok     tokenizer.Tokenizer
	context int
	mu      sync.Mutex // one generation at a time: each holds a key-value cache
}

// OpenLanguageModel loads a Mimir Model file of a language model and its
// tokenizer. It fails with ErrBadWeights for a file that is not one or
// that mimir cannot run.
func OpenLanguageModel(b []byte) (*LanguageModel, error) {
	m, err := mim.Parse(b)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrBadWeights, err)
	}
	lm, err := newLanguageModel(m)
	if err != nil {
		m.Close()
		return nil, err
	}
	return lm, nil
}

func newLanguageModel(m *mim.Model) (*LanguageModel, error) {
	context, err := languageInputs(m)
	if err != nil {
		return nil, err
	}
	md, err := m.Metadata()
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrBadWeights, err)
	}
	tok, err := tokenizer.New(md)
	if err != nil {
		return nil, fmt.Errorf("%w: its tokenizer: %v", ErrBadWeights, err)
	}
	return &LanguageModel{m: m, tok: tok, context: context}, nil
}

// Context is the most tokens the model reads and writes in one
// generation, prompt included.
func (lm *LanguageModel) Context() int { return lm.context }

// Close releases the model.
func (lm *LanguageModel) Close() error { return lm.m.Close() }

// Sampling says how a generation picks each next token.
type Sampling struct {
	// MaxTokens is the most tokens to write.
	MaxTokens int
	// Temperature 0 always picks the likeliest token; higher values pick
	// less likely ones more often.
	Temperature float64
	// TopK limits sampling to the k likeliest tokens; 0 means 40.
	TopK int
	// Seed makes sampling repeatable.
	Seed uint64
}

// Generation is the text a language model wrote.
type Generation struct {
	Text            string
	PromptTokens    int
	GeneratedTokens int
	// Finished says the model ended the text itself, rather than running
	// into MaxTokens or the end of its context.
	Finished bool
}

// Generate continues prompt. It fails with ErrBadInput for an empty
// prompt or one longer than the model's context.
func (lm *LanguageModel) Generate(ctx context.Context, prompt string, s Sampling) (Generation, error) {
	ids := lm.tok.Encode(prompt, true)
	if len(ids) == 0 || prompt == "" {
		return Generation{}, fmt.Errorf("%w: the prompt is empty", ErrBadInput)
	}
	if len(ids) >= lm.context {
		return Generation{}, fmt.Errorf("%w: the prompt is %d tokens; this model reads at most %d", ErrBadInput, len(ids), lm.context-1)
	}
	if s.TopK <= 0 {
		s.TopK = 40
	}
	rng := rand.New(rand.NewPCG(s.Seed, 0x6d696d6972))

	lm.mu.Lock()
	defer lm.mu.Unlock()
	sess := lm.m.NewSession()
	g := Generation{PromptTokens: len(ids)}
	var text []byte
	logits, err := lm.step(ctx, sess, ids, 0)
	pos := len(ids)
	for err == nil && g.GeneratedTokens < s.MaxTokens {
		next := pick(logits, s, rng)
		if next == lm.tok.EOS() {
			g.Finished = true
			break
		}
		text = append(text, lm.tok.Piece(next)...)
		g.GeneratedTokens++
		if pos >= lm.context || g.GeneratedTokens == s.MaxTokens {
			break
		}
		logits, err = lm.step(ctx, sess, []int32{next}, pos)
		pos++
	}
	if err != nil {
		return Generation{}, err
	}
	g.Text = validUTF8(text)
	return g, nil
}

// step runs ids at positions pos, pos+1, ... and returns the logits of the
// last one.
func (lm *LanguageModel) step(ctx context.Context, sess *mim.Session, ids []int32, pos int) ([]float32, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	p := make([]int32, len(ids))
	for i := range p {
		p[i] = int32(pos + i)
	}
	out, err := sess.Run(ctx, map[string]*tensor.Tensor{
		"tokens":    tensor.FromSlice(slices.Clone(ids), 1, len(ids)),
		"positions": tensor.FromSlice(p, 1, len(ids)),
	})
	if err != nil {
		return nil, fmt.Errorf("runtime: run language model: %w", err)
	}
	all, err := out["logits"].Data[float32](ctx)
	if err != nil {
		return nil, fmt.Errorf("runtime: run language model: %w", err)
	}
	if len(all)%len(ids) != 0 {
		return nil, errors.New("runtime: run language model: logits don't fit the tokens")
	}
	v := len(all) / len(ids)
	return all[len(all)-v:], nil
}

// pick chooses the next token from logits.
func pick(logits []float32, s Sampling, rng *rand.Rand) int32 {
	if s.Temperature <= 0 {
		best := 0
		for i, x := range logits {
			if x > logits[best] {
				best = i
			}
		}
		return int32(best)
	}
	idx := make([]int, len(logits))
	for i := range idx {
		idx[i] = i
	}
	slices.SortFunc(idx, func(a, b int) int {
		switch {
		case logits[a] > logits[b]:
			return -1
		case logits[a] < logits[b]:
			return 1
		}
		return a - b
	})
	idx = idx[:min(s.TopK, len(idx))]
	w := make([]float64, len(idx))
	var sum float64
	for i, j := range idx {
		w[i] = math.Exp((float64(logits[j]) - float64(logits[idx[0]])) / s.Temperature)
		sum += w[i]
	}
	r := rng.Float64() * sum
	for i, x := range w {
		if r -= x; r <= 0 {
			return int32(idx[i])
		}
	}
	return int32(idx[len(idx)-1])
}

// validUTF8 drops a last character the model hadn't finished writing and
// control characters other than line breaks and tabs, and replaces any
// other invalid bytes.
func validUTF8(b []byte) string {
	for n := 1; n <= min(len(b), utf8.UTFMax-1); n++ {
		if utf8.RuneStart(b[len(b)-n]) {
			if !utf8.FullRune(b[len(b)-n:]) {
				b = b[:len(b)-n]
			}
			break
		}
	}
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) && r != '\n' && r != '\t' {
			return -1
		}
		return r
	}, strings.ToValidUTF8(string(b), "\uFFFD"))
}
