package api

import (
	"context"
	"errors"
	"math"
	"strings"
	"time"
	"unicode/utf8"

	"connectrpc.com/connect"

	mimirv1 "github.com/gsoultan/mimir-server/gen/mimir/v1"
	"github.com/gsoultan/mimir-server/internal/arch"
	"github.com/gsoultan/mimir-server/internal/auth"
	"github.com/gsoultan/mimir-server/internal/runtime"
)

// Limits on a generation.
const (
	defaultTokens  = 128
	maxTokens      = 1024
	maxTemperature = 2
	maxPrompt      = 64 << 10 // bytes
)

func (s *Service) Generate(ctx context.Context, req *connect.Request[mimirv1.GenerateRequest]) (*connect.Response[mimirv1.GenerateResponse], error) {
	msg := req.Msg
	m, err := s.get(ctx, msg.GetModelId())
	if err != nil {
		return nil, err
	}
	a, ok := arch.Lookup(m.Architecture)
	if !ok || a.Answers() != arch.Written {
		return nil, invalid("%s isn't a language model: ask it with Predict.", m.Name)
	}
	prompt := msg.GetPrompt()
	switch {
	case strings.TrimSpace(prompt) == "":
		return nil, invalid("Write the start of a text for the model to continue.")
	case !utf8.ValidString(prompt):
		return nil, invalid("The prompt must be valid text.")
	case len(prompt) > maxPrompt:
		return nil, invalid("The prompt can be at most %d KB.", maxPrompt>>10)
	}
	tokens := int(msg.GetMaxTokens())
	switch {
	case tokens == 0:
		tokens = defaultTokens
	case tokens < 0 || tokens > maxTokens:
		return nil, invalid("max_tokens must be between 1 and %d.", maxTokens)
	}
	temp := float64(msg.GetTemperature())
	if math.IsNaN(temp) || temp < 0 || temp > maxTemperature {
		return nil, invalid("temperature must be between 0 and %d.", maxTemperature)
	}
	// Workers don't take language models, so they run here even when the
	// placement says otherwise.
	if !s.Local {
		return nil, connect.NewError(connect.CodeUnavailable, errors.New("Language models run on the console itself, and this server leaves running models to workers. Start it as standalone to use them."))
	}
	if p, ok := auth.From(ctx); ok {
		if err := s.admit(ctx, p); err != nil {
			return nil, err
		}
	}
	start := time.Now()
	load := func() ([]byte, error) { return s.store.Weights(m.ID) }
	g, err := s.cache.Generate(ctx, m.ID, a, load, prompt, runtime.Sampling{MaxTokens: tokens, Temperature: temp, Seed: msg.GetSeed()})
	switch {
	case errors.Is(err, runtime.ErrBadInput):
		return nil, invalid("%s.", strings.TrimPrefix(err.Error(), runtime.ErrBadInput.Error()+": "))
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return nil, connect.NewError(connect.CodeCanceled, errors.New("The request ended before the model finished writing."))
	case err != nil:
		return nil, s.internal(ctx, err)
	}
	return connect.NewResponse(&mimirv1.GenerateResponse{
		Text:            g.Text,
		PromptTokens:    int32(g.PromptTokens),
		GeneratedTokens: int32(g.GeneratedTokens),
		Finished:        g.Finished,
		DurationMicros:  time.Since(start).Microseconds(),
	}), nil
}
