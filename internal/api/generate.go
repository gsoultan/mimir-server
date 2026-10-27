package api

import (
	"context"
	"errors"
	"math"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"connectrpc.com/connect"

	mimirv1 "github.com/gsoultan/mimir-server/gen/mimir/v1"
	"github.com/gsoultan/mimir-server/internal/arch"
	"github.com/gsoultan/mimir-server/internal/auth"
	"github.com/gsoultan/mimir-server/internal/memguard"
	"github.com/gsoultan/mimir-server/internal/runtime"
	"github.com/gsoultan/mimir-server/internal/store"
)

// Limits on a generation.
const (
	defaultTokens   = 128
	maxTokens       = 1024
	maxTemperature  = 2
	maxPrompt       = 64 << 10 // bytes
	maxInstructions = 8000     // characters
	maxMessages     = 200
)

// Writing is one request for a language model to write: a prompt to
// continue, or a chat to answer.
type Writing struct {
	Prompt string
	// Messages is a chat, oldest first, ending with the user's. A first
	// system message is the instructions.
	Messages []runtime.Message
	// MaxTokens, Temperature and Seed are as in runtime.Sampling; 0 tokens
	// means the model's default.
	MaxTokens   int
	Temperature float64
	Seed        uint64
	// Stop ends the text before the first of these the model writes.
	Stop []string
	// OnText gets the text as it is written.
	OnText func(string) error
}

// Written is what a language model wrote, and how long it took.
type Written struct {
	runtime.Generation
	Duration time.Duration
}

// LanguageModel returns the language model with id or name for the caller,
// checking it may be asked: it exists for them, writes text, and runs here.
// ref may also be the model's API name.
func (s *Service) LanguageModel(ctx context.Context, ref string) (store.Model, error) {
	var m store.Model
	var err error
	if store.ValidID(ref) {
		m, err = s.get(ctx, ref)
	} else {
		m, err = s.byAPIName(ctx, ref)
	}
	if err != nil {
		return store.Model{}, err
	}
	if !isLanguage(m) {
		return store.Model{}, invalid("%s isn't a language model: ask it with Predict.", m.Name)
	}
	// Workers don't take language models, so they run here even when the
	// placement says otherwise.
	if !s.Local {
		return store.Model{}, connect.NewError(connect.CodeUnavailable, errors.New("Language models run on the console itself, and this server leaves running models to workers. Start it as standalone to use them."))
	}
	return m, nil
}

// LanguageModels lists the language models the caller may ask.
func (s *Service) LanguageModels(ctx context.Context) ([]store.Model, error) {
	p, _ := auth.From(ctx)
	all, err := s.store.List(ctx, p.Team())
	if err != nil {
		return nil, s.internal(ctx, err)
	}
	var out []store.Model
	for _, m := range all {
		if isLanguage(m) && p.Access.Allows(m.ID) {
			out = append(out, m)
		}
	}
	return out, nil
}

// byAPIName finds a model by the name apps send to the OpenAI-compatible
// API.
func (s *Service) byAPIName(ctx context.Context, name string) (store.Model, error) {
	all, err := s.LanguageModels(ctx)
	if err != nil {
		return store.Model{}, err
	}
	var found []store.Model
	for _, m := range all {
		if APIName(m.Name) == name {
			found = append(found, m)
		}
	}
	switch len(found) {
	case 0:
		return store.Model{}, connect.NewError(connect.CodeNotFound, errors.New("There's no language model called "+name+" that this key may use."))
	case 1:
		return found[0], nil
	}
	return store.Model{}, invalid("Several models are called %s; send the model's id instead.", name)
}

func isLanguage(m store.Model) bool {
	a, ok := arch.Lookup(m.Architecture)
	return ok && a.Answers() == arch.Written
}

var notSlug = regexp.MustCompile(`[^a-z0-9.]+`)

// APIName is the name apps send for a model to the OpenAI-compatible API:
// its name in lower case, with dashes for spaces and other signs.
func APIName(name string) string {
	return strings.Trim(notSlug.ReplaceAllString(strings.ToLower(name), "-"), "-.")
}

// Write has language model m write: continue w.Prompt, or answer
// w.Messages. Unset settings come from the model's defaults.
func (s *Service) Write(ctx context.Context, m store.Model, w Writing) (Written, error) {
	if w.Messages == nil {
		switch p := w.Prompt; {
		case strings.TrimSpace(p) == "":
			return Written{}, invalid("Write the start of a text for the model to continue.")
		case !utf8.ValidString(p):
			return Written{}, invalid("The prompt must be valid text.")
		case len(p) > maxPrompt:
			return Written{}, invalid("The prompt can be at most %d KB.", maxPrompt>>10)
		}
	} else if err := checkChat(w.Messages); err != nil {
		return Written{}, err
	}
	switch {
	case w.MaxTokens < 0 || w.MaxTokens > maxTokens:
		return Written{}, invalid("max_tokens must be between 1 and %d.", maxTokens)
	case math.IsNaN(w.Temperature) || w.Temperature < 0 || w.Temperature > maxTemperature:
		return Written{}, invalid("temperature must be between 0 and %d.", maxTemperature)
	}
	if p, ok := auth.From(ctx); ok {
		if err := s.admit(ctx, p); err != nil {
			return Written{}, err
		}
	}
	sampling := runtime.Sampling{MaxTokens: w.MaxTokens, Temperature: w.Temperature, Seed: w.Seed, Stop: w.Stop, OnText: w.OnText}
	start := time.Now()
	files, ver, err := s.languageFiles(ctx, m)
	if err != nil {
		return Written{}, err
	}
	var g runtime.Generation
	if w.Messages == nil {
		g, err = s.cache.Generate(ctx, m.ID, ver, files, w.Prompt, sampling)
	} else {
		g, err = s.cache.Chat(ctx, m.ID, ver, files, w.Messages, sampling)
	}
	var sent *sendError
	var long *runtime.TooLongError
	switch {
	case errors.As(err, &sent):
		return Written{}, sent.err
	case errors.As(err, &long) && w.Messages != nil:
		return Written{}, invalid("This chat is %d word pieces, more than %s can remember (%d). Start a new chat, or shorten the instructions.", long.Tokens, m.Name, long.Max)
	case errors.Is(err, runtime.ErrBadInput):
		return Written{}, invalid("%s.", strings.TrimPrefix(err.Error(), runtime.ErrBadInput.Error()+": "))
	case tooBig(err):
		return Written{}, tooBigError(err)
	case errors.Is(err, memguard.ErrLowMemory):
		return Written{}, lowMemory()
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return Written{}, connect.NewError(connect.CodeCanceled, errors.New("The request ended before the model finished writing."))
	case err != nil:
		return Written{}, s.internal(ctx, err)
	}
	return Written{Generation: g, Duration: time.Since(start)}, nil
}

// MaxEmbedInputs bounds how many texts one embeddings request sends.
const MaxEmbedInputs = 256

// Embedded is what a language model made of texts: an embedding of each,
// and how many tokens it read.
type Embedded struct {
	Vectors [][]float32
	Tokens  int
}

// Embed has language model m make an embedding of each text: a list of
// numbers whose dot product with another text's says how alike the two
// are, for search and grouping. Each text counts up to
// runtime.MaxEmbedTokens tokens; the rest is left out.
func (s *Service) Embed(ctx context.Context, m store.Model, texts []string) (Embedded, error) {
	switch {
	case len(texts) == 0:
		return Embedded{}, invalid("Send at least one text to embed.")
	case len(texts) > MaxEmbedInputs:
		return Embedded{}, invalid("Send at most %d texts at once.", MaxEmbedInputs)
	}
	n := 0
	for _, t := range texts {
		switch {
		case strings.TrimSpace(t) == "":
			return Embedded{}, invalid("A text to embed can't be empty.")
		case !utf8.ValidString(t):
			return Embedded{}, invalid("Texts must be valid text.")
		}
		n += len(t)
	}
	if n > maxPrompt*4 {
		return Embedded{}, invalid("The texts can be at most %d KB together.", maxPrompt*4>>10)
	}
	if p, ok := auth.From(ctx); ok {
		if err := s.admit(ctx, p); err != nil {
			return Embedded{}, err
		}
	}
	files, ver, err := s.languageFiles(ctx, m)
	if err != nil {
		return Embedded{}, err
	}
	vecs, tokens, err := s.cache.Embed(ctx, m.ID, ver, files, texts)
	switch {
	case errors.Is(err, runtime.ErrBadInput):
		return Embedded{}, invalid("%s.", strings.TrimPrefix(err.Error(), runtime.ErrBadInput.Error()+": "))
	case tooBig(err):
		return Embedded{}, tooBigError(err)
	case errors.Is(err, memguard.ErrLowMemory):
		return Embedded{}, lowMemory()
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return Embedded{}, connect.NewError(connect.CodeCanceled, errors.New("The request ended before the model finished."))
	case err != nil:
		return Embedded{}, s.internal(ctx, err)
	}
	return Embedded{Vectors: vecs, Tokens: tokens}, nil
}

// languageFiles returns how the cache reads language model m's files,
// and the version of them it loads: for a fine-tune, its adapter on its
// base model, both of which its version follows.
func (s *Service) languageFiles(ctx context.Context, m store.Model) (runtime.LanguageFiles, string, error) {
	if !m.FineTune() {
		return runtime.LanguageFiles{Model: func() ([]byte, error) { return s.store.Weights(m.ID) }}, m.WeightsVersion(), nil
	}
	base, err := s.store.Get(ctx, m.BaseModelID)
	if errors.Is(err, store.ErrNotFound) {
		return runtime.LanguageFiles{}, "", connect.NewError(connect.CodeFailedPrecondition, errors.New("The model "+m.Name+" was fine-tuned from is gone."))
	} else if err != nil {
		return runtime.LanguageFiles{}, "", s.internal(ctx, err)
	}
	return runtime.LanguageFiles{
		Model:      func() ([]byte, error) { return s.store.Weights(base.ID) },
		Adapter:    func() ([]byte, error) { return s.store.Weights(m.ID) },
		BaseSHA256: base.SHA256,
	}, m.WeightsVersion() + "+" + base.WeightsVersion(), nil
}

// sendError is an error from passing on the text, such as a caller who
// went away, kept apart from the model's own errors.
type sendError struct{ err error }

func (e *sendError) Error() string { return e.err.Error() }

// sending wraps a function that passes text on so its errors come back as
// they are.
func sending(f func(string) error) func(string) error {
	return func(t string) error {
		if err := f(t); err != nil {
			return &sendError{err}
		}
		return nil
	}
}

func checkChat(msgs []runtime.Message) error {
	if len(msgs) == 0 || msgs[len(msgs)-1].Role != runtime.RoleUser {
		return invalid("A chat must end with a message from the user.")
	}
	if len(msgs) > maxMessages {
		return invalid("A chat can have at most %d messages; start a new one.", maxMessages)
	}
	n := 0
	for i, m := range msgs {
		switch {
		case m.Role == runtime.RoleSystem && i != 0:
			return invalid("Only the first message can be the instructions.")
		case m.Role != runtime.RoleSystem && m.Role != runtime.RoleUser && m.Role != runtime.RoleAssistant:
			return invalid("A message's role is system, user or assistant, not %q.", m.Role)
		case !utf8.ValidString(m.Content):
			return invalid("Messages must be valid text.")
		}
		n += len(m.Content)
	}
	if n > maxPrompt {
		return invalid("The chat can be at most %d KB; start a new one.", maxPrompt>>10)
	}
	return nil
}

// withDefaults fills a request's unset settings from the model's defaults.
func withDefaults(m store.Model, maxTokens int, temperature *float32) (int, float64) {
	t := m.Language.Temperature
	if temperature != nil {
		t = float64(*temperature)
	}
	if maxTokens == 0 {
		maxTokens = m.Language.MaxTokens
	}
	return maxTokens, t
}

func (s *Service) Generate(ctx context.Context, req *connect.Request[mimirv1.GenerateRequest]) (*connect.Response[mimirv1.GenerateResponse], error) {
	msg := req.Msg
	m, err := s.LanguageModel(ctx, msg.GetModelId())
	if err != nil {
		return nil, err
	}
	tokens := int(msg.GetMaxTokens())
	if tokens == 0 {
		tokens = defaultTokens
	}
	w, err := s.Write(ctx, m, Writing{Prompt: msg.GetPrompt(), MaxTokens: tokens, Temperature: float64(msg.GetTemperature()), Seed: msg.GetSeed()})
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&mimirv1.GenerateResponse{
		Text:            w.Text,
		PromptTokens:    int32(w.PromptTokens),
		GeneratedTokens: int32(w.GeneratedTokens),
		Finished:        w.Finished,
		DurationMicros:  w.Duration.Microseconds(),
	}), nil
}

func (s *Service) GenerateStream(ctx context.Context, req *connect.Request[mimirv1.GenerateStreamRequest], out *connect.ServerStream[mimirv1.GenerateStreamResponse]) error {
	msg := req.Msg
	m, err := s.LanguageModel(ctx, msg.GetModelId())
	if err != nil {
		return err
	}
	tokens := int(msg.GetMaxTokens())
	if tokens == 0 {
		tokens = defaultTokens
	}
	send := sending(func(t string) error { return out.Send(&mimirv1.GenerateStreamResponse{Text: t}) })
	w, err := s.Write(ctx, m, Writing{Prompt: msg.GetPrompt(), MaxTokens: tokens, Temperature: float64(msg.GetTemperature()), Seed: msg.GetSeed(), OnText: send})
	if err != nil {
		return err
	}
	return out.Send(&mimirv1.GenerateStreamResponse{
		Done:            true,
		PromptTokens:    int32(w.PromptTokens),
		GeneratedTokens: int32(w.GeneratedTokens),
		Finished:        w.Finished,
		DurationMicros:  w.Duration.Microseconds(),
		ContextTokens:   int32(w.Context),
	})
}

func (s *Service) Chat(ctx context.Context, req *connect.Request[mimirv1.ChatRequest], out *connect.ServerStream[mimirv1.ChatResponse]) error {
	msg := req.Msg
	m, err := s.LanguageModel(ctx, msg.GetModelId())
	if err != nil {
		return err
	}
	instructions := m.Language.Instructions
	if msg.Instructions != nil {
		instructions = msg.GetInstructions()
	}
	if utf8.RuneCountInString(instructions) > maxInstructions {
		return invalid("Instructions can be at most %d characters.", maxInstructions)
	}
	var msgs []runtime.Message
	if strings.TrimSpace(instructions) != "" {
		msgs = append(msgs, runtime.Message{Role: runtime.RoleSystem, Content: instructions})
	}
	for _, c := range msg.GetMessages() {
		if c.GetRole() == runtime.RoleSystem {
			return invalid("Send the instructions in instructions, not as a message.")
		}
		msgs = append(msgs, runtime.Message{Role: c.GetRole(), Content: c.GetContent()})
	}
	tokens, temp := withDefaults(m, int(msg.GetMaxTokens()), msg.Temperature)
	send := sending(func(t string) error { return out.Send(&mimirv1.ChatResponse{Text: t}) })
	w, err := s.Write(ctx, m, Writing{Messages: msgs, MaxTokens: tokens, Temperature: temp, Seed: msg.GetSeed(), OnText: send})
	if err != nil {
		return err
	}
	return out.Send(&mimirv1.ChatResponse{
		Done:            true,
		PromptTokens:    int32(w.PromptTokens),
		GeneratedTokens: int32(w.GeneratedTokens),
		Finished:        w.Finished,
		DurationMicros:  w.Duration.Microseconds(),
		ContextTokens:   int32(w.Context),
	})
}

// languageProto is a language model's default settings; nil for other
// models.
func languageProto(m store.Model) *mimirv1.LanguageSettings {
	if !isLanguage(m) {
		return nil
	}
	return &mimirv1.LanguageSettings{
		Instructions: m.Language.Instructions,
		Temperature:  float32(m.Language.Temperature),
		MaxTokens:    int32(m.Language.MaxTokens),
		ApiName:      APIName(m.Name),
	}
}

// checkLanguage validates new default settings.
func checkLanguage(l *mimirv1.LanguageSettings) (store.Language, error) {
	out := store.Language{Instructions: strings.TrimSpace(l.GetInstructions()), Temperature: float64(l.GetTemperature()), MaxTokens: int(l.GetMaxTokens())}
	switch {
	case !utf8.ValidString(out.Instructions):
		return out, invalid("Instructions must be valid text.")
	case utf8.RuneCountInString(out.Instructions) > maxInstructions:
		return out, invalid("Instructions can be at most %d characters.", maxInstructions)
	case math.IsNaN(out.Temperature) || out.Temperature < 0 || out.Temperature > maxTemperature:
		return out, invalid("Temperature must be between 0 and %d.", maxTemperature)
	case out.MaxTokens < 1 || out.MaxTokens > maxTokens:
		return out, invalid("The longest answer must be between 1 and %d tokens.", maxTokens)
	}
	return out, nil
}
