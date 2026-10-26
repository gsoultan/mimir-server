package training

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"math/rand/v2"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/gsoultan/mimir/io/mim"
	"github.com/gsoultan/mimir/llm"
	"github.com/gsoultan/mimir/optim"
	"github.com/gsoultan/mimir/tensor"

	"github.com/gsoultan/mimir-server/internal/runtime"
	"github.com/gsoultan/mimir-server/internal/store"
)

// Limits on the writing a language model is fine-tuned on.
const (
	// MaxTextBytes bounds one text dataset's file.
	MaxTextBytes = 64 << 20
	// MaxTextExamples bounds how many examples it holds.
	MaxTextExamples = 100_000
	// MaxExampleTokens is the most tokens of one example a fine-tune
	// learns from; the rest of a longer one is left out.
	MaxExampleTokens = 512
	// passageBytes is about how long a passage of plain text is: its
	// paragraphs are put together up to this length.
	passageBytes = 2000
)

// LoRA settings of a fresh fine-tune: rank 8 pairs on the attention
// projections, scaled by 16/8, PEFT's usual start.
const (
	loraRank  = 8
	loraAlpha = 16
)

// Example is one thing a fine-tune learns to write: the assistant's turns
// of a chat, the completion of a prompt, or, with no completion, the
// prompt itself, a passage of text.
type Example struct {
	Messages   []runtime.Message
	Prompt     string
	Completion string
}

// Writing is a parsed text dataset.
type Writing struct {
	Examples []Example
	// Chats, Prompts and Passages count the examples of each kind.
	Chats, Prompts, Passages int
}

// line is one line of a JSON Lines dataset, in the formats OpenAI's
// fine-tuning takes, and a plain {"text": ...}.
type line struct {
	Messages []struct {
		Role    string `json:"role"`
		Content string `json:"content"`
	} `json:"messages"`
	Prompt     *string `json:"prompt"`
	Completion *string `json:"completion"`
	Text       *string `json:"text"`
}

// ParseText reads the writing a language model is fine-tuned on. A file
// whose first line is a JSON object is JSON Lines, one example a line:
// {"messages": [{"role": "user", "content": ...}, {"role": "assistant",
// ...}]}, {"prompt": ..., "completion": ...} or {"text": ...}. Any other
// file is plain text, such as a document, learned whole, in passages.
func ParseText(b []byte) (*Writing, error) {
	if len(b) > MaxTextBytes {
		return nil, fmt.Errorf("%w: the file can be at most %d MB", ErrBadTable, MaxTextBytes>>20)
	}
	if !utf8.Valid(b) {
		return nil, fmt.Errorf("%w: the file isn't text: save it as UTF-8 text or JSON Lines", ErrBadTable)
	}
	b = bytes.TrimPrefix(b, []byte("\ufeff"))
	trimmed := bytes.TrimSpace(b)
	if len(trimmed) == 0 {
		return nil, fmt.Errorf("%w: the file is empty", ErrBadTable)
	}
	w := &Writing{}
	var err error
	if trimmed[0] == '{' {
		err = w.jsonLines(trimmed)
	} else {
		w.passages(string(trimmed))
	}
	if err != nil {
		return nil, err
	}
	if len(w.Examples) > MaxTextExamples {
		return nil, fmt.Errorf("%w: at most %d examples", ErrBadTable, MaxTextExamples)
	}
	return w, nil
}

func (w *Writing) jsonLines(b []byte) error {
	sc := bufio.NewScanner(bytes.NewReader(b))
	sc.Buffer(nil, MaxTextBytes)
	for n := 1; sc.Scan(); n++ {
		text := bytes.TrimSpace(sc.Bytes())
		if len(text) == 0 {
			continue
		}
		bad := func(format string, a ...any) error {
			return fmt.Errorf("%w: line %d: %s", ErrBadTable, n, fmt.Sprintf(format, a...))
		}
		var l line
		if err := json.Unmarshal(text, &l); err != nil {
			return bad("not a JSON object: %v", err)
		}
		switch {
		case l.Messages != nil:
			e := Example{}
			answers := 0
			for i, m := range l.Messages {
				switch m.Role {
				case runtime.RoleSystem:
					if i != 0 {
						return bad("only the first message can be the instructions (role system)")
					}
				case runtime.RoleUser:
				case runtime.RoleAssistant:
					if strings.TrimSpace(m.Content) != "" {
						answers++
					}
				default:
					return bad("a message's role is system, user or assistant, not %q", m.Role)
				}
				e.Messages = append(e.Messages, runtime.Message{Role: m.Role, Content: m.Content})
			}
			if answers == 0 {
				return bad("the chat has no answer from the assistant to learn")
			}
			w.Examples = append(w.Examples, e)
			w.Chats++
		case l.Prompt != nil:
			if l.Completion == nil || strings.TrimSpace(*l.Completion) == "" {
				return bad("a prompt needs a completion to learn")
			}
			w.Examples = append(w.Examples, Example{Prompt: *l.Prompt, Completion: *l.Completion})
			w.Prompts++
		case l.Text != nil:
			if strings.TrimSpace(*l.Text) == "" {
				continue
			}
			w.Examples = append(w.Examples, Example{Prompt: *l.Text})
			w.Passages++
		default:
			return bad(`want "messages", "prompt" and "completion", or "text"`)
		}
		if len(w.Examples) > MaxTextExamples {
			return fmt.Errorf("%w: at most %d examples", ErrBadTable, MaxTextExamples)
		}
	}
	if err := sc.Err(); err != nil {
		return fmt.Errorf("%w: %v", ErrBadTable, err)
	}
	if len(w.Examples) == 0 {
		return fmt.Errorf("%w: the file has no examples", ErrBadTable)
	}
	return nil
}

// passages splits plain text into passages of about passageBytes, at
// paragraphs where it can and at spaces where a paragraph is longer.
func (w *Writing) passages(text string) {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	var cur strings.Builder
	flush := func() {
		if s := strings.TrimSpace(cur.String()); s != "" {
			w.Examples = append(w.Examples, Example{Prompt: s})
			w.Passages++
		}
		cur.Reset()
	}
	for p := range strings.SplitSeq(text, "\n\n") {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		if cur.Len() > 0 && cur.Len()+len(p) > passageBytes {
			flush()
		}
		for len(p) > passageBytes {
			cut := strings.LastIndexAny(p[:passageBytes], " \n\t")
			if cut <= 0 {
				cut = passageBytes
				for !utf8.RuneStart(p[cut]) {
					cut--
				}
			}
			cur.WriteString(p[:cut])
			flush()
			p = strings.TrimSpace(p[cut:])
		}
		if cur.Len() > 0 {
			cur.WriteString("\n\n")
		}
		cur.WriteString(p)
	}
	flush()
}

// tokens returns the tokens of e as lm reads them, and which it learns.
func (e Example) tokens(lm *runtime.LanguageModel) ([]int32, []bool) {
	if e.Messages != nil {
		return lm.ChatTokens(e.Messages)
	}
	return lm.CompletionTokens(e.Prompt, e.Completion)
}

// Describe fills in what a text dataset holds: its examples, and the
// kinds of them.
func (w *Writing) Describe(d *store.Dataset) {
	d.Rows = int64(len(w.Examples))
	d.Labels = nil
	for _, k := range []struct {
		name string
		n    int
	}{{"chats", w.Chats}, {"prompts", w.Prompts}, {"passages", w.Passages}} {
		if k.n > 0 {
			d.Labels = append(d.Labels, k.name)
		}
	}
}

// fitLoRA fine-tunes the language model of t.BaseWeights on the writing
// of t.Source with LoRA: small trainable pairs next to its attention
// projections learn, and its own weights stay as they are, quantized or
// not. The result's weights are the pairs, as an adapter file of the
// base. A run continues from t.BaseAdapter, when set, and resumes from
// the checkpoint in t.CheckpointDir.
func fitLoRA(ctx context.Context, t Task, progress Progress) (Result, error) {
	s := t.Settings
	if len(t.BaseWeights) == 0 {
		return Result{}, errors.New("training: a fine-tune needs the language model it starts from")
	}
	w, err := ParseText(t.Source.CSV)
	if err != nil {
		return Result{}, err
	}
	seqs, learn, err := tokenize(t.BaseWeights, w)
	if err != nil {
		return Result{}, err
	}

	// Hold some examples out to score on, when there are enough.
	r := rand.New(rand.NewPCG(42, 0))
	order := r.Perm(len(seqs))
	held := 0
	if len(seqs) >= MinRows && s.HoldOut > 0 {
		held = min(max(int(math.Round(float64(len(seqs))*s.HoldOut)), 1), len(seqs)-1)
	}
	test, trainSet := order[:held], slices.Clone(order[held:])

	f, err := mim.Parse(t.BaseWeights)
	if err != nil {
		return Result{}, fmt.Errorf("%w: %v", runtime.ErrBadWeights, err)
	}
	defer f.Close()
	md, err := f.Metadata()
	if err != nil {
		return Result{}, fmt.Errorf("%w: %v", runtime.ErrBadWeights, err)
	}
	model, err := llm.Load(f, md, tensor.DefaultDevice())
	if err != nil {
		return Result{}, fmt.Errorf("%w: this language model can't be fine-tuned: %v", ErrBadTable, err)
	}

	history := t.History
	start := 0
	adapter := t.BaseAdapter
	if b, epoch := lastCheckpoint(t.CheckpointDir); b != nil && epoch <= s.Epochs {
		adapter, start = b, epoch
	}
	if len(history) > start {
		history = history[:start]
	}
	if adapter != nil {
		a, err := mim.Parse(adapter)
		if err != nil {
			return Result{}, fmt.Errorf("%w: the fine-tune to continue: %v", runtime.ErrBadWeights, err)
		}
		defer a.Close()
		if err := model.ReadAdapter(a); err != nil {
			return Result{}, fmt.Errorf("%w: the fine-tune to continue: %v", runtime.ErrBadWeights, err)
		}
	} else if err := model.AddLoRA(llm.LoRAConfig{Rank: loraRank, Alpha: loraAlpha, Seed: 42}); err != nil {
		return Result{}, err
	}

	opt := optim.AdamW(model.Adapters(), optim.LR(s.LearningRate), optim.WeightDecay(0))
	schedule := optim.Cosine(s.LearningRate, s.LearningRate/20, s.Epochs)
	batch := max(s.BatchSize, 1)
	steps := (len(trainSet) + batch - 1) / batch
	var lastWrite time.Time
	progress(len(history), 0, steps, history)
	for epoch := start + 1; epoch <= s.Epochs; epoch++ {
		began := time.Now()
		opt.SetLR(schedule(epoch))
		rand.New(rand.NewPCG(42, uint64(epoch))).Shuffle(len(trainSet), func(i, j int) {
			trainSet[i], trainSet[j] = trainSet[j], trainSet[i]
		})
		var sum float64
		for step := range steps {
			l, err := trainStep(ctx, model, opt, seqs, learn, trainSet[step*batch:min((step+1)*batch, len(trainSet))])
			if err != nil {
				return Result{}, err
			}
			sum += l
			if time.Since(lastWrite) > time.Second {
				lastWrite = time.Now()
				progress(epoch-1, step+1, steps, history)
			}
		}
		ep := store.Epoch{Epoch: epoch, Loss: sum / float64(max(steps, 1))}
		// Score on the held-out examples, or, with none, on what it
		// learned from; the UI says which.
		scoreOn := test
		if len(scoreOn) == 0 {
			scoreOn = trainSet
		}
		loss, right, total, err := score(ctx, model, seqs, learn, scoreOn)
		if err != nil {
			return Result{}, err
		}
		ep.Accuracy = float64(right) / float64(max(total, 1))
		if len(test) > 0 {
			ep.ValLoss = loss
		}
		ep.Millis = time.Since(began).Milliseconds()
		history = append(history, ep)
		if t.CheckpointDir != "" {
			if err := checkpoint(ctx, model, t.CheckpointDir, epoch); err != nil {
				return Result{}, err
			}
		}
		progress(epoch, 0, steps, history)
	}

	var buf bytes.Buffer
	sum := sha256.Sum256(t.BaseWeights)
	if err := model.WriteAdapter(ctx, &buf, hex.EncodeToString(sum[:]), ""); err != nil {
		return Result{}, fmt.Errorf("couldn't save the fine-tune: %w", err)
	}
	return Result{Weights: buf.Bytes(), History: history}, nil
}

// tokenize turns the examples of w into the tokens of the language model
// in base, cut to what it learns from, with which tokens it learns.
func tokenize(base []byte, w *Writing) ([][]int32, [][]bool, error) {
	lm, err := runtime.OpenLanguageModel(base)
	if err != nil {
		return nil, nil, err
	}
	defer lm.Close()
	limit := min(lm.Context(), MaxExampleTokens)
	var seqs [][]int32
	var learn [][]bool
	for _, e := range w.Examples {
		ids, l := e.tokens(lm)
		if len(ids) > limit {
			ids, l = ids[:limit], l[:limit]
		}
		if len(ids) < 2 || !slices.Contains(l[1:], true) {
			continue // nothing to learn within the limit
		}
		seqs, learn = append(seqs, ids), append(learn, l)
	}
	if len(seqs) == 0 {
		return nil, nil, fmt.Errorf("%w: no example has anything to learn within its first %d word pieces", ErrBadTable, limit)
	}
	return seqs, learn, nil
}

// trainStep takes one step on the examples at idx. Each example's loss is
// worked out alone and their gradients add up, so a step needs the memory
// of one example, not of the batch: a language model's logits are large.
func trainStep(ctx context.Context, model *llm.Model, opt *optim.Optimizer, seqs [][]int32, learn [][]bool, idx []int) (float64, error) {
	var sum float64
	scale := tensor.Full(tensor.Float32, 1/float64(len(idx)))
	for _, i := range idx {
		if err := ctx.Err(); err != nil {
			return 0, context.Cause(ctx)
		}
		loss := model.Loss([][]int32{seqs[i]}, [][]bool{learn[i]})
		loss = loss.Mul(scale.To(loss.Device()))
		if err := loss.Backward(ctx); err != nil {
			return 0, err
		}
		v, err := loss.Item[float32](ctx)
		if err != nil {
			return 0, err
		}
		sum += float64(v)
	}
	if err := opt.Step(ctx); err != nil {
		return 0, err
	}
	opt.ZeroGrad()
	return sum, nil
}

// score is how well the model writes the learned tokens of the examples
// at idx: the mean loss per token, and how many tokens it gets right.
func score(ctx context.Context, model *llm.Model, seqs [][]int32, learn [][]bool, idx []int) (loss float64, right, total int, err error) {
	var sum float64
	for _, i := range idx {
		if err := ctx.Err(); err != nil {
			return 0, 0, 0, context.Cause(ctx)
		}
		l, r, n, err := model.Score(ctx, [][]int32{seqs[i]}, [][]bool{learn[i]})
		if err != nil {
			return 0, 0, 0, err
		}
		sum += l * float64(n)
		right += r
		total += n
	}
	return sum / float64(max(total, 1)), right, total, nil
}

// checkpointPrefix starts the name of a fine-tune's checkpoint, which
// ends with the epoch it finished.
const checkpointPrefix = "lora-epoch-"

// checkpoint writes the model's pairs after epoch to dir, replacing the
// checkpoint of the epoch before.
func checkpoint(ctx context.Context, model *llm.Model, dir string, epoch int) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	var buf bytes.Buffer
	if err := model.WriteAdapter(ctx, &buf, "", ""); err != nil {
		return err
	}
	path := filepath.Join(dir, checkpointPrefix+strconv.Itoa(epoch)+".mim")
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, buf.Bytes(), 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		return err
	}
	if epoch > 1 {
		os.Remove(filepath.Join(dir, checkpointPrefix+strconv.Itoa(epoch-1)+".mim"))
	}
	return nil
}

// lastCheckpoint returns the latest checkpoint in dir and the epoch it
// finished, or nil when there is none.
func lastCheckpoint(dir string) ([]byte, int) {
	if dir == "" {
		return nil, 0
	}
	names, _ := filepath.Glob(filepath.Join(dir, checkpointPrefix+"*.mim"))
	best, epoch := "", 0
	for _, n := range names {
		e, err := strconv.Atoi(strings.TrimSuffix(strings.TrimPrefix(filepath.Base(n), checkpointPrefix), ".mim"))
		if err == nil && e > epoch {
			best, epoch = n, e
		}
	}
	if best == "" {
		return nil, 0
	}
	b, err := os.ReadFile(best)
	if err != nil {
		return nil, 0
	}
	return b, epoch
}
