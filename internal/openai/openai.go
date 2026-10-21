// Package openai serves the server's language models over the HTTP API
// OpenAI's clients speak, under /v1: chat completions, completions and the
// model list, with answers streamed as server-sent events on request. Apps
// and libraries made for OpenAI point their base URL here and send a mimir
// API key; keys, per-model access and limits apply as for any other call.
package openai

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"connectrpc.com/connect"

	"github.com/gsoultan/mimir-server/internal/api"
	"github.com/gsoultan/mimir-server/internal/auth"
	"github.com/gsoultan/mimir-server/internal/runtime"
	"github.com/gsoultan/mimir-server/internal/store"
)

// maxBody is the largest request body read.
const maxBody = 1 << 20

// Handler serves /v1/models, /v1/chat/completions and /v1/completions.
func Handler(svc *api.Service, st *store.Store, log *slog.Logger) http.Handler {
	h := &handler{svc: svc, st: st, log: log}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/models", h.signedIn(h.models))
	mux.HandleFunc("POST /v1/chat/completions", h.signedIn(h.chat))
	mux.HandleFunc("POST /v1/completions", h.signedIn(h.completions))
	mux.HandleFunc("/v1/", func(w http.ResponseWriter, r *http.Request) {
		fail(w, http.StatusNotFound, "not_found", "This server answers /v1/models, /v1/chat/completions and /v1/completions.")
	})
	return mux
}

type handler struct {
	svc *api.Service
	st  *store.Store
	log *slog.Logger
}

// signedIn lets through requests with a valid mimir API key that may ask
// models, carrying who sent them.
func (h *handler) signedIn(next func(http.ResponseWriter, *http.Request)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") {
			fail(w, http.StatusUnauthorized, "invalid_api_key", "Send a mimir API key: Authorization: Bearer mimir_...")
			return
		}
		p, err := auth.Authenticate(r.Context(), h.st, r.Header, r.RemoteAddr)
		if err != nil || p == nil || p.Key == nil {
			msg := "That API key isn't valid. It may have been revoked."
			if ce := new(connect.Error); errors.As(err, &ce) && connect.CodeOf(err) == connect.CodeUnauthenticated {
				msg = ce.Message()
			}
			fail(w, http.StatusUnauthorized, "invalid_api_key", msg)
			return
		}
		if !p.Can(auth.ModelsPredict) {
			fail(w, http.StatusForbidden, "permission_denied", "This key may not ask models.")
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, maxBody)
		next(w, r.WithContext(auth.WithPrincipal(r.Context(), *p)))
	}
}

type model struct {
	ID      string `json:"id"`
	Object  string `json:"object"`
	Created int64  `json:"created"`
	OwnedBy string `json:"owned_by"`
}

func (h *handler) models(w http.ResponseWriter, r *http.Request) {
	ms, err := h.svc.LanguageModels(r.Context())
	if err != nil {
		h.fail(w, r, err)
		return
	}
	out := struct {
		Object string  `json:"object"`
		Data   []model `json:"data"`
	}{Object: "list", Data: []model{}}
	for _, m := range ms {
		out.Data = append(out.Data, model{ID: api.APIName(m.Name), Object: "model", Created: m.Created.Unix(), OwnedBy: "mimir"})
	}
	writeJSON(w, http.StatusOK, out)
}

// stops reads "stop", which is a string or a list of them.
type stops []string

func (s *stops) UnmarshalJSON(b []byte) error {
	var one string
	if json.Unmarshal(b, &one) == nil {
		*s = stops{one}
		return nil
	}
	var many []string
	if err := json.Unmarshal(b, &many); err != nil {
		return errors.New("stop must be a string or a list of strings")
	}
	*s = many
	return nil
}

// content reads a message's content: a string, or a list of parts of which
// only text parts are understood.
type content string

func (c *content) UnmarshalJSON(b []byte) error {
	var s string
	if json.Unmarshal(b, &s) == nil {
		*c = content(s)
		return nil
	}
	var parts []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if err := json.Unmarshal(b, &parts); err != nil {
		return errors.New("a message's content must be text or a list of text parts")
	}
	var sb strings.Builder
	for _, p := range parts {
		if p.Type != "text" {
			return fmt.Errorf("these models read text only, not %s parts", p.Type)
		}
		sb.WriteString(p.Text)
	}
	*c = content(sb.String())
	return nil
}

// settings are the request fields chat and completions share.
type settings struct {
	Model               string   `json:"model"`
	Stream              bool     `json:"stream"`
	MaxTokens           *int     `json:"max_tokens"`
	MaxCompletionTokens *int     `json:"max_completion_tokens"`
	Temperature         *float64 `json:"temperature"`
	Seed                *int64   `json:"seed"`
	Stop                stops    `json:"stop"`
	N                   *int     `json:"n"`
	StreamOptions       struct {
		IncludeUsage bool `json:"include_usage"`
	} `json:"stream_options"`
}

// writing turns the settings into a Writing, with the model's defaults for
// what they leave out.
func (s settings) writing(m store.Model) (api.Writing, error) {
	if s.N != nil && *s.N != 1 {
		return api.Writing{}, errors.New("n must be 1: these models write one answer at a time")
	}
	if len(s.Stop) > 4 {
		return api.Writing{}, errors.New("stop can have at most 4 strings")
	}
	w := api.Writing{MaxTokens: m.Language.MaxTokens, Temperature: m.Language.Temperature, Stop: s.Stop}
	switch {
	case s.MaxCompletionTokens != nil:
		w.MaxTokens = *s.MaxCompletionTokens
	case s.MaxTokens != nil:
		w.MaxTokens = *s.MaxTokens
	}
	if w.MaxTokens < 1 {
		return api.Writing{}, errors.New("max_tokens must be at least 1")
	}
	if s.Temperature != nil {
		w.Temperature = *s.Temperature
	}
	if s.Seed != nil {
		w.Seed = uint64(*s.Seed)
	}
	return w, nil
}

type chatRequest struct {
	settings
	Messages []struct {
		Role    string  `json:"role"`
		Content content `json:"content"`
	} `json:"messages"`
}

func (h *handler) chat(w http.ResponseWriter, r *http.Request) {
	var req chatRequest
	if !h.read(w, r, &req) {
		return
	}
	m, err := h.svc.LanguageModel(r.Context(), req.Model)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	wr, err := req.writing(m)
	if err != nil {
		fail(w, http.StatusBadRequest, "invalid_request_error", err.Error())
		return
	}
	// System and developer messages, wherever they are, become the
	// instructions; without any, the model's own apply.
	var instructions []string
	for _, msg := range req.Messages {
		switch msg.Role {
		case "system", "developer":
			instructions = append(instructions, string(msg.Content))
		case runtime.RoleUser, runtime.RoleAssistant:
			wr.Messages = append(wr.Messages, runtime.Message{Role: msg.Role, Content: string(msg.Content)})
		default:
			fail(w, http.StatusBadRequest, "invalid_request_error", fmt.Sprintf("Messages can be from system, developer, user or assistant, not %q: these models don't call tools.", msg.Role))
			return
		}
	}
	system := m.Language.Instructions
	if instructions != nil {
		system = strings.Join(instructions, "\n\n")
	}
	if strings.TrimSpace(system) != "" {
		wr.Messages = append([]runtime.Message{{Role: runtime.RoleSystem, Content: system}}, wr.Messages...)
	}
	if wr.Messages == nil {
		wr.Messages = []runtime.Message{} // a chat, though an empty one
	}
	h.answer(w, r, m, req.settings, wr, "chat.completion")
}

type completionRequest struct {
	settings
	Prompt json.RawMessage `json:"prompt"`
}

func (h *handler) completions(w http.ResponseWriter, r *http.Request) {
	var req completionRequest
	if !h.read(w, r, &req) {
		return
	}
	var prompt string
	if json.Unmarshal(req.Prompt, &prompt) != nil {
		var list []string
		if json.Unmarshal(req.Prompt, &list) != nil || len(list) != 1 {
			fail(w, http.StatusBadRequest, "invalid_request_error", "prompt must be a string, or a list of one string.")
			return
		}
		prompt = list[0]
	}
	m, err := h.svc.LanguageModel(r.Context(), req.Model)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	wr, err := req.writing(m)
	if err != nil {
		fail(w, http.StatusBadRequest, "invalid_request_error", err.Error())
		return
	}
	wr.Prompt = prompt
	h.answer(w, r, m, req.settings, wr, "text_completion")
}

// answer has the model write and sends the answer whole, or as events
// when the request asked to stream.
func (h *handler) answer(w http.ResponseWriter, r *http.Request, m store.Model, s settings, wr api.Writing, object string) {
	id := newID(object)
	created := time.Now().Unix()
	name := api.APIName(m.Name)
	chat := object == "chat.completion"
	if !s.Stream {
		res, err := h.svc.Write(r.Context(), m, wr)
		if err != nil {
			h.fail(w, r, err)
			return
		}
		choice := map[string]any{"index": 0, "finish_reason": finish(res)}
		if chat {
			choice["message"] = map[string]any{"role": "assistant", "content": res.Text}
		} else {
			choice["text"] = res.Text
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"id": id, "object": object, "created": created, "model": name,
			"choices": []any{choice}, "usage": usage(res),
		})
		return
	}

	flusher, _ := w.(http.Flusher)
	started := false
	start := func() {
		if started {
			return
		}
		started = true
		hd := w.Header()
		hd.Set("Content-Type", "text/event-stream")
		hd.Set("Cache-Control", "no-store")
		hd.Set("X-Accel-Buffering", "no")
		w.WriteHeader(http.StatusOK)
	}
	event := func(v any) error {
		start()
		b, _ := json.Marshal(v)
		if _, err := fmt.Fprintf(w, "data: %s\n\n", b); err != nil {
			return err
		}
		if flusher != nil {
			flusher.Flush()
		}
		return nil
	}
	chunkObject := object
	if chat {
		chunkObject = "chat.completion.chunk"
	}
	chunk := func(text string, first bool, reason any) map[string]any {
		c := map[string]any{"index": 0, "finish_reason": reason}
		if chat {
			delta := map[string]any{}
			if first {
				delta["role"] = "assistant"
			}
			if text != "" {
				delta["content"] = text
			}
			c["delta"] = delta
		} else {
			c["text"] = text
		}
		return map[string]any{"id": id, "object": chunkObject, "created": created, "model": name, "choices": []any{c}}
	}
	first := true
	wr.OnText = func(t string) error {
		err := event(chunk(t, first, nil))
		first = false
		return err
	}
	res, err := h.svc.Write(r.Context(), m, wr)
	if err != nil {
		if !started {
			h.fail(w, r, err)
			return
		}
		_, typ, msg := describe(err)
		_ = event(map[string]any{"error": map[string]any{"message": msg, "type": typ}})
		return
	}
	last := chunk("", first, finish(res))
	if s.StreamOptions.IncludeUsage {
		last["usage"] = usage(res)
	}
	if event(last) == nil {
		fmt.Fprint(w, "data: [DONE]\n\n")
		if flusher != nil {
			flusher.Flush()
		}
	}
}

func finish(res api.Written) string {
	if res.Finished {
		return "stop"
	}
	return "length"
}

func usage(res api.Written) map[string]int {
	return map[string]int{"prompt_tokens": res.PromptTokens, "completion_tokens": res.GeneratedTokens, "total_tokens": res.PromptTokens + res.GeneratedTokens}
}

func newID(object string) string {
	var b [12]byte
	rand.Read(b[:])
	prefix := "cmpl-"
	if object == "chat.completion" {
		prefix = "chatcmpl-"
	}
	return prefix + hex.EncodeToString(b[:])
}

// read decodes a JSON request body into v, answering a bad one itself.
func (h *handler) read(w http.ResponseWriter, r *http.Request, v any) bool {
	b, err := io.ReadAll(r.Body)
	if err != nil {
		fail(w, http.StatusRequestEntityTooLarge, "invalid_request_error", "The request can be at most 1 MB.")
		return false
	}
	if err := json.Unmarshal(b, v); err != nil {
		fail(w, http.StatusBadRequest, "invalid_request_error", "The request isn't valid JSON for this endpoint: "+err.Error())
		return false
	}
	return true
}

// fail answers with an API error from the server's own errors.
func (h *handler) fail(w http.ResponseWriter, r *http.Request, err error) {
	status, typ, msg := describe(err)
	if status == http.StatusInternalServerError {
		h.log.ErrorContext(r.Context(), "openai", "error", err)
	}
	fail(w, status, typ, msg)
}

// describe maps an error from the API service to an HTTP status, an
// OpenAI error type and a message.
func describe(err error) (int, string, string) {
	msg := err.Error()
	var ce *connect.Error
	if errors.As(err, &ce) {
		msg = ce.Message()
	}
	switch connect.CodeOf(err) {
	case connect.CodeInvalidArgument:
		return http.StatusBadRequest, "invalid_request_error", msg
	case connect.CodeNotFound:
		return http.StatusNotFound, "model_not_found", msg
	case connect.CodePermissionDenied:
		return http.StatusForbidden, "permission_denied", msg
	case connect.CodeResourceExhausted:
		return http.StatusTooManyRequests, "rate_limit_exceeded", msg
	case connect.CodeUnavailable:
		return http.StatusServiceUnavailable, "server_error", msg
	case connect.CodeCanceled:
		return 499, "canceled", msg
	}
	if errors.Is(err, context.Canceled) {
		return 499, "canceled", "The request ended before the model finished writing."
	}
	return http.StatusInternalServerError, "server_error", "Something went wrong on the server."
}

func fail(w http.ResponseWriter, status int, typ, msg string) {
	writeJSON(w, status, map[string]any{"error": map[string]any{"message": msg, "type": typ, "code": typ}})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}
