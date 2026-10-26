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

	"github.com/gsoultan/mimir-model/tokenizer"
	"github.com/gsoultan/mimir/io/mim"
	"github.com/gsoultan/mimir/llm"
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
	chat    chatFormat
	// special maps the text of each control and user-defined token, such
	// as <|im_start|>, to its id, so a chat's markers become those tokens
	// rather than being spelled out. control holds the ids that end an
	// answer when the model writes them.
	special map[string]int32
	control map[int32]bool
	turns   chan struct{} // a token per generation running; see maxGenerations

	// adapter is a fine-tune's LoRA adapter, which the graph already
	// applies and embed applies too; nil for a model of its own.
	adapter *mim.Model
	// embed computes embeddings, built on first use (see Embed); embedMu
	// guards it and runs one embedding at a time.
	embedMu  sync.Mutex
	embed    *llm.Model
	embedErr error
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

// OpenFineTune loads a fine-tune: the language model of base with the
// LoRA adapter of adapter, a Mimir Model adapter file, applied to it.
// baseSHA256 is the lowercase hex SHA-256 of base, checked against the
// base the adapter names. It fails with ErrBadWeights for files that
// aren't these.
func OpenFineTune(base, adapter []byte, baseSHA256 string) (*LanguageModel, error) {
	m, err := mim.Parse(base)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrBadWeights, err)
	}
	a, err := mim.Parse(adapter)
	if err == nil {
		err = m.ApplyLoRA(a, baseSHA256)
	}
	if err != nil {
		if a != nil {
			a.Close()
		}
		m.Close()
		return nil, fmt.Errorf("%w: the fine-tune: %v", ErrBadWeights, err)
	}
	lm, err := newLanguageModel(m)
	if err != nil {
		a.Close()
		m.Close()
		return nil, err
	}
	lm.adapter = a
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
	lm := &LanguageModel{m: m, tok: tok, context: context, special: map[string]int32{}, control: map[int32]bool{}, turns: make(chan struct{}, maxGenerations)}
	vocab, _ := md["tokenizer.ggml.tokens"].([]string)
	types := ints(md["tokenizer.ggml.token_type"])
	for i, t := range types {
		if i >= len(vocab) || (t != tokenControl && t != tokenUserDefined) || vocab[i] == "" {
			continue
		}
		if _, dup := lm.special[vocab[i]]; !dup {
			lm.special[vocab[i]] = int32(i)
		}
		if t == tokenControl {
			lm.control[int32(i)] = true
		}
	}
	template, _ := md["tokenizer.chat_template"].(string)
	lm.chat = formatOf(template, vocab)
	return lm, nil
}

// maxGenerations is how many answers one language model writes at once.
// Each has a session of its own, with its key-value cache, so they
// interleave: a question asked while others are answered starts at once
// rather than after them. More would share the processors too thinly, so
// the rest wait their turn.
const maxGenerations = 4

// Token types, as GGUF's tokenizer.ggml.token_type stores them.
const (
	tokenControl     = 3
	tokenUserDefined = 4
)

// ints reads a list of integers from metadata, whichever integer type the
// file stored it as.
func ints(v any) []int64 {
	switch v := v.(type) {
	case []int64:
		return v
	case []int32:
		out := make([]int64, len(v))
		for i, x := range v {
			out[i] = int64(x)
		}
		return out
	case []any:
		out := make([]int64, 0, len(v))
		for _, x := range v {
			switch x := x.(type) {
			case int64:
				out = append(out, x)
			case int32:
				out = append(out, int64(x))
			case int:
				out = append(out, int64(x))
			default:
				return nil
			}
		}
		return out
	}
	return nil
}

// encode tokenizes text, after the beginning-of-text token if the
// vocabulary has one. With special set, the text of control and
// user-defined tokens becomes those tokens, as a chat's markers must.
func (lm *LanguageModel) encode(text string, special bool) []int32 {
	return lm.encodeFrom(text, special, true)
}

// encodeFrom is encode, with the beginning-of-text token only if bos is
// set.
func (lm *LanguageModel) encodeFrom(text string, special, bos bool) []int32 {
	if !special || len(lm.special) == 0 {
		return lm.tok.Encode(text, bos)
	}
	out := lm.tok.Encode("", bos) // just the beginning-of-text token, if any
	for text != "" {
		at, id, n := len(text), int32(0), 0
		for s, i := range lm.special {
			if k := strings.Index(text, s); k >= 0 && (k < at || (k == at && len(s) > n)) {
				at, id, n = k, i, len(s)
			}
		}
		if at > 0 {
			out = append(out, lm.tok.Encode(text[:at], false)...)
		}
		if n == 0 {
			break
		}
		out = append(out, id)
		text = text[at+n:]
	}
	return out
}

// Count is how many tokens text is to the model.
func (lm *LanguageModel) Count(text string) int { return len(lm.tok.Encode(text, false)) }

// Context is the most tokens the model reads and writes in one
// generation, prompt included.
func (lm *LanguageModel) Context() int { return lm.context }

// Close releases the model.
func (lm *LanguageModel) Close() error {
	lm.embedMu.Lock()
	defer lm.embedMu.Unlock()
	lm.embed, lm.embedErr = nil, errors.New("runtime: the model is closed")
	if lm.adapter != nil {
		lm.adapter.Close()
	}
	return lm.m.Close()
}

// MaxEmbedTokens is the most tokens of one text Embed reads; the rest is
// left out, as OpenAI's API does past its own limit.
const MaxEmbedTokens = 512

// Embed returns an embedding of each text: the mean of the model's last
// hidden states over the text's tokens, scaled to length 1, so the dot
// product of two says how alike the texts are. A fine-tune embeds with
// its adapter. tokens is how many tokens it read in all.
func (lm *LanguageModel) Embed(ctx context.Context, texts []string) (vecs [][]float32, tokens int, err error) {
	lm.embedMu.Lock()
	defer lm.embedMu.Unlock()
	if lm.embed == nil && lm.embedErr == nil {
		lm.embed, lm.embedErr = lm.embedder()
	}
	if lm.embedErr != nil {
		return nil, 0, lm.embedErr
	}
	for _, text := range texts {
		if err := ctx.Err(); err != nil {
			return nil, 0, err
		}
		if strings.TrimSpace(text) == "" {
			return nil, 0, fmt.Errorf("%w: an empty text has no embedding", ErrBadInput)
		}
		ids := lm.encode(text, false)
		if len(ids) > MaxEmbedTokens {
			ids = ids[:MaxEmbedTokens]
		}
		v, err := lm.embed.Embedding(ctx, ids)
		if err != nil {
			return nil, 0, err
		}
		vecs = append(vecs, v)
		tokens += len(ids)
	}
	return vecs, tokens, nil
}

// embedder builds the model Embed runs: the layers of the file, read
// eagerly rather than through its graph, with the adapter's pairs.
func (lm *LanguageModel) embedder() (*llm.Model, error) {
	md, err := lm.m.Metadata()
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrBadWeights, err)
	}
	e, err := llm.Load(lm.m, md, tensor.DefaultDevice())
	if err != nil {
		return nil, fmt.Errorf("%w: this model can't make embeddings: %v", ErrBadInput, err)
	}
	if lm.adapter != nil {
		if err := e.ReadAdapter(lm.adapter); err != nil {
			return nil, fmt.Errorf("%w: the fine-tune: %v", ErrBadWeights, err)
		}
	}
	return e, nil
}

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
	// Stop ends the text before the first of these the model writes.
	Stop []string
	// OnText, when set, gets the text as it is written, piece by piece;
	// the pieces add up to Generation.Text. An error from it ends the
	// generation with that error.
	OnText func(string) error
	// special reads control tokens written in the prompt as those tokens.
	special bool
}

// Generation is the text a language model wrote.
type Generation struct {
	Text            string
	PromptTokens    int
	GeneratedTokens int
	// Finished says the model ended the text itself, rather than running
	// into MaxTokens or the end of its context.
	Finished bool
	// Context is the most tokens the model reads and writes at once.
	Context int
}

// TooLongError is a prompt longer than the model reads. It is ErrBadInput.
type TooLongError struct{ Tokens, Max int }

func (e *TooLongError) Error() string {
	return fmt.Sprintf("%v: the prompt is %d tokens; this model reads at most %d", ErrBadInput, e.Tokens, e.Max)
}

func (e *TooLongError) Unwrap() error { return ErrBadInput }

// Generate continues prompt. It fails with ErrBadInput for an empty
// prompt or one longer than the model's context.
func (lm *LanguageModel) Generate(ctx context.Context, prompt string, s Sampling) (Generation, error) {
	ids := lm.encode(prompt, s.special)
	if len(ids) == 0 || prompt == "" {
		return Generation{}, fmt.Errorf("%w: the prompt is empty", ErrBadInput)
	}
	if len(ids) >= lm.context {
		return Generation{}, &TooLongError{Tokens: len(ids), Max: lm.context - 1}
	}
	if s.TopK <= 0 {
		s.TopK = 40
	}
	rng := rand.New(rand.NewPCG(s.Seed, 0x6d696d6972))

	select {
	case lm.turns <- struct{}{}:
	case <-ctx.Done():
		return Generation{}, ctx.Err()
	}
	defer func() { <-lm.turns }()
	sess := lm.m.NewSession()
	g := Generation{PromptTokens: len(ids), Context: lm.context}
	var text []byte
	var out strings.Builder
	sent := 0 // bytes of text already passed on
	// flush passes on the text up to end that can no longer change.
	flush := func(end int) error {
		if end <= sent {
			return nil
		}
		piece := clean(text[sent:end])
		sent = end
		out.WriteString(piece)
		if s.OnText != nil && piece != "" {
			return s.OnText(piece)
		}
		return nil
	}
	logits, err := lm.step(ctx, sess, ids, 0)
	pos := len(ids)
	for err == nil && g.GeneratedTokens < s.MaxTokens {
		next := pick(logits, s, rng)
		if next == lm.tok.EOS() || lm.control[next] {
			g.Finished = true
			break
		}
		text = append(text, lm.tok.Piece(next)...)
		g.GeneratedTokens++
		if cut, ok := stopAt(text, s.Stop); ok {
			text = text[:cut]
			g.Finished = true
			break
		}
		if err = flush(settled(text, s.Stop)); err != nil {
			break
		}
		if pos >= lm.context || g.GeneratedTokens == s.MaxTokens {
			break
		}
		logits, err = lm.step(ctx, sess, []int32{next}, pos)
		pos++
	}
	if err == nil {
		err = flush(len(trimPartial(text)))
	}
	if err != nil {
		return Generation{}, err
	}
	g.Text = out.String()
	return g, nil
}

// stopAt reports where the first stop string in text starts.
func stopAt(text []byte, stop []string) (int, bool) {
	at := -1
	for _, s := range stop {
		if k := strings.Index(string(text), s); k >= 0 && (at < 0 || k < at) {
			at = k
		}
	}
	return at, at >= 0
}

// settled is how much of text can't change any more: all of it but a last
// character still being written and an end that could be the start of a
// stop string.
func settled(text []byte, stop []string) int {
	end := len(trimPartial(text))
	hold := 0
	for _, s := range stop {
		for n := min(len(s)-1, end); n > hold; n-- {
			if strings.HasPrefix(s, string(text[end-n:end])) {
				hold = n
				break
			}
		}
	}
	return end - hold
}

// trimPartial drops a last character the model hasn't finished writing.
func trimPartial(b []byte) []byte {
	for n := 1; n <= min(len(b), utf8.UTFMax-1); n++ {
		if utf8.RuneStart(b[len(b)-n]) {
			if !utf8.FullRune(b[len(b)-n:]) {
				return b[:len(b)-n]
			}
			break
		}
	}
	return b
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
func validUTF8(b []byte) string { return clean(trimPartial(b)) }

// clean drops control characters other than line breaks and tabs, and
// replaces invalid bytes.
func clean(b []byte) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) && r != '\n' && r != '\t' {
			return -1
		}
		return r
	}, strings.ToValidUTF8(string(b), "\uFFFD"))
}
