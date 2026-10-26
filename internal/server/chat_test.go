package server_test

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"connectrpc.com/connect"

	mimirv1 "github.com/gsoultan/mimir-server/gen/mimir/v1"
)

// languageModel uploads the tiny GGUF language model as name.
func languageModel(t *testing.T, c clients, name string) *mimirv1.Model {
	t.Helper()
	res, err := c.models.CreateModel(context.Background(), connect.NewRequest(&mimirv1.CreateModelRequest{Name: name, Architecture: "mim", Weights: fixture(t, "tiny-llama.gguf")}))
	if err != nil {
		t.Fatal(err)
	}
	return res.Msg.GetModel()
}

// TestChat streams a chat answer and the continuation of a text, and keeps
// a language model's default settings.
func TestChat(t *testing.T) {
	ctx := context.Background()
	srv := newServer(t)
	c := owner(t, srv)
	m := languageModel(t, c, "Tiny Llama")
	if l := m.GetLanguage(); l == nil || l.GetApiName() != "tiny-llama" || l.GetMaxTokens() != 256 || l.GetTemperature() < 0.69 || l.GetTemperature() > 0.71 {
		t.Fatalf("language settings %v", l)
	}

	stream, err := c.predict.Chat(ctx, connect.NewRequest(&mimirv1.ChatRequest{
		ModelId:   m.GetId(),
		Messages:  []*mimirv1.ChatMessage{{Role: "user", Content: "Hello"}, {Role: "assistant", Content: "Hi"}, {Role: "user", Content: "Tell me a story"}},
		MaxTokens: 6,
	}))
	if err != nil {
		t.Fatal(err)
	}
	var text strings.Builder
	var last *mimirv1.ChatResponse
	for stream.Receive() {
		last = stream.Msg()
		text.WriteString(last.GetText())
	}
	if err := stream.Err(); err != nil {
		t.Fatal(err)
	}
	if last == nil || !last.GetDone() || last.GetGeneratedTokens() == 0 || last.GetGeneratedTokens() > 6 || last.GetContextTokens() == 0 || last.GetPromptTokens() == 0 {
		t.Fatalf("last event %v", last)
	}

	gs, err := c.predict.GenerateStream(ctx, connect.NewRequest(&mimirv1.GenerateStreamRequest{ModelId: m.GetId(), Prompt: "Once upon a time", MaxTokens: 5}))
	if err != nil {
		t.Fatal(err)
	}
	var pieces strings.Builder
	var done *mimirv1.GenerateStreamResponse
	for gs.Receive() {
		done = gs.Msg()
		pieces.WriteString(done.GetText())
	}
	if gs.Err() != nil || done == nil || !done.GetDone() {
		t.Fatalf("GenerateStream: %v, %v", done, gs.Err())
	}
	whole, err := c.predict.Generate(ctx, connect.NewRequest(&mimirv1.GenerateRequest{ModelId: m.GetId(), Prompt: "Once upon a time", MaxTokens: 5}))
	if err != nil || whole.Msg.GetText() != pieces.String() {
		t.Fatalf("streamed %q, whole %q (%v)", pieces.String(), whole.Msg.GetText(), err)
	}

	for _, bad := range []*mimirv1.ChatRequest{
		{ModelId: m.GetId()},
		{ModelId: m.GetId(), Messages: []*mimirv1.ChatMessage{{Role: "assistant", Content: "hi"}}},
		{ModelId: m.GetId(), Messages: []*mimirv1.ChatMessage{{Role: "system", Content: "x"}, {Role: "user", Content: "hi"}}},
		{ModelId: m.GetId(), Messages: []*mimirv1.ChatMessage{{Role: "tool", Content: "hi"}}},
	} {
		s, err := c.predict.Chat(ctx, connect.NewRequest(bad))
		if err == nil {
			for s.Receive() {
			}
			err = s.Err()
		}
		if code(err) != connect.CodeInvalidArgument {
			t.Errorf("Chat(%v): %v", bad, err)
		}
	}

	upd, err := c.models.UpdateModel(ctx, connect.NewRequest(&mimirv1.UpdateModelRequest{Id: m.GetId(), Name: m.GetName(), Language: &mimirv1.LanguageSettings{Instructions: "Be brief.", Temperature: 0, MaxTokens: 64}}))
	if err != nil {
		t.Fatal(err)
	}
	if l := upd.Msg.GetModel().GetLanguage(); l.GetInstructions() != "Be brief." || l.GetTemperature() != 0 || l.GetMaxTokens() != 64 {
		t.Fatalf("saved %v", l)
	}
	if _, err := c.models.UpdateModel(ctx, connect.NewRequest(&mimirv1.UpdateModelRequest{Id: m.GetId(), Name: m.GetName(), Language: &mimirv1.LanguageSettings{MaxTokens: 0}})); code(err) != connect.CodeInvalidArgument {
		t.Errorf("max_tokens 0: %v", err)
	}
	cnn := createModel(t, c, "Digits", "mnist-mlp")
	if cnn.GetLanguage() != nil {
		t.Errorf("a digits model has language settings %v", cnn.GetLanguage())
	}
	if _, err := c.models.UpdateModel(ctx, connect.NewRequest(&mimirv1.UpdateModelRequest{Id: cnn.GetId(), Name: "Digits", Language: &mimirv1.LanguageSettings{MaxTokens: 10}})); code(err) != connect.CodeInvalidArgument {
		t.Errorf("language settings on a digits model: %v", err)
	}

	// Streams need a sign-in, like calls.
	anon := newClients(srv)
	s, err := anon.predict.Chat(ctx, connect.NewRequest(&mimirv1.ChatRequest{ModelId: m.GetId(), Messages: []*mimirv1.ChatMessage{{Role: "user", Content: "hi"}}}))
	if err == nil {
		for s.Receive() {
		}
		err = s.Err()
	}
	if code(err) != connect.CodeUnauthenticated {
		t.Errorf("signed-out Chat: %v", err)
	}
}

// TestOpenAI talks to a language model the way OpenAI's clients do.
func TestOpenAI(t *testing.T) {
	ctx := context.Background()
	srv := newServer(t)
	c := owner(t, srv)
	m := languageModel(t, c, "Tiny Llama")
	other := languageModel(t, c, "Hidden")
	created, err := c.keys.CreateApiKey(ctx, connect.NewRequest(&mimirv1.CreateApiKeyRequest{
		Name: "app", Access: &mimirv1.Access{Role: mimirv1.Role_ROLE_VIEWER, ModelIds: []string{m.GetId()}},
	}))
	if err != nil {
		t.Fatal(err)
	}
	key := created.Msg.GetSecret()

	call := func(method, path, key, body string) *http.Response {
		t.Helper()
		req, _ := http.NewRequest(method, srv.URL+path, strings.NewReader(body))
		if key != "" {
			req.Header.Set("Authorization", "Bearer "+key)
		}
		req.Header.Set("Content-Type", "application/json")
		res, err := srv.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { res.Body.Close() })
		return res
	}
	decode := func(res *http.Response) map[string]any {
		t.Helper()
		var v map[string]any
		if err := json.NewDecoder(res.Body).Decode(&v); err != nil {
			t.Fatal(err)
		}
		return v
	}

	res := call("GET", "/v1/models", key, "")
	list := decode(res)
	data, _ := list["data"].([]any)
	if res.StatusCode != 200 || len(data) != 1 || data[0].(map[string]any)["id"] != "tiny-llama" {
		t.Fatalf("models: %d %v", res.StatusCode, list)
	}
	_ = other

	res = call("POST", "/v1/chat/completions", key, `{"model":"tiny-llama","max_tokens":5,"messages":[{"role":"system","content":"Be kind."},{"role":"user","content":[{"type":"text","text":"Hello"}]}]}`)
	body := decode(res)
	choices, _ := body["choices"].([]any)
	if res.StatusCode != 200 || body["object"] != "chat.completion" || len(choices) != 1 {
		t.Fatalf("chat: %d %v", res.StatusCode, body)
	}
	if u, _ := body["usage"].(map[string]any); u["completion_tokens"].(float64) < 1 || u["completion_tokens"].(float64) > 5 {
		t.Fatalf("usage %v", body["usage"])
	}

	res = call("POST", "/v1/chat/completions", key, `{"model":"tiny-llama","max_tokens":5,"stream":true,"stream_options":{"include_usage":true},"messages":[{"role":"user","content":"Hello"}]}`)
	if res.StatusCode != 200 || !strings.HasPrefix(res.Header.Get("Content-Type"), "text/event-stream") {
		t.Fatalf("stream: %d %s", res.StatusCode, res.Header.Get("Content-Type"))
	}
	var events []string
	sc := bufio.NewScanner(res.Body)
	for sc.Scan() {
		if line, ok := strings.CutPrefix(sc.Text(), "data: "); ok {
			events = append(events, line)
		}
	}
	if len(events) < 2 || events[len(events)-1] != "[DONE]" {
		t.Fatalf("events %v", events)
	}
	var first, last map[string]any
	json.Unmarshal([]byte(events[0]), &first)
	json.Unmarshal([]byte(events[len(events)-2]), &last)
	if first["object"] != "chat.completion.chunk" || first["choices"].([]any)[0].(map[string]any)["delta"].(map[string]any)["role"] != "assistant" {
		t.Fatalf("first event %v", first)
	}
	if last["choices"].([]any)[0].(map[string]any)["finish_reason"] == nil || last["usage"] == nil {
		t.Fatalf("last event %v", last)
	}

	res = call("POST", "/v1/completions", key, `{"model":"`+m.GetId()+`","prompt":"Once upon a time","max_tokens":4,"temperature":0}`)
	body = decode(res)
	if res.StatusCode != 200 || body["object"] != "text_completion" {
		t.Fatalf("completions: %d %v", res.StatusCode, body)
	}

	res = call("POST", "/v1/embeddings", key, `{"model":"tiny-llama","input":["Once upon a time","Once upon a time","A dragon"]}`)
	body = decode(res)
	data, _ = body["data"].([]any)
	if res.StatusCode != 200 || body["object"] != "list" || len(data) != 3 {
		t.Fatalf("embeddings: %d %v", res.StatusCode, body)
	}
	var vecs [][]float64
	for i, d := range data {
		e := d.(map[string]any)
		if e["object"] != "embedding" || e["index"].(float64) != float64(i) {
			t.Fatalf("embedding %d: %v", i, e)
		}
		var v []float64
		for _, x := range e["embedding"].([]any) {
			v = append(v, x.(float64))
		}
		vecs = append(vecs, v)
	}
	dot := func(a, b []float64) float64 {
		var s float64
		for i := range a {
			s += a[i] * b[i]
		}
		return s
	}
	if len(vecs[0]) == 0 || dot(vecs[0], vecs[0]) < 0.999 || dot(vecs[0], vecs[0]) > 1.001 || dot(vecs[0], vecs[1]) < 0.999 || dot(vecs[0], vecs[2]) > 0.9999 {
		t.Errorf("embeddings of %d numbers: |a|² %v, a·a' %v, a·b %v", len(vecs[0]), dot(vecs[0], vecs[0]), dot(vecs[0], vecs[1]), dot(vecs[0], vecs[2]))
	}
	if u, _ := body["usage"].(map[string]any); u["prompt_tokens"].(float64) < 3 {
		t.Errorf("usage %v", body["usage"])
	}
	res = call("POST", "/v1/embeddings", key, `{"model":"tiny-llama","input":"A dragon","encoding_format":"base64"}`)
	body = decode(res)
	if data, _ := body["data"].([]any); res.StatusCode != 200 || len(data) != 1 {
		t.Fatalf("base64 embeddings: %d %v", res.StatusCode, body)
	} else if b64, _ := data[0].(map[string]any)["embedding"].(string); len(b64) != base64.StdEncoding.EncodedLen(4*len(vecs[2])) {
		t.Errorf("base64 embedding of %d characters for %d numbers", len(b64), len(vecs[2]))
	}

	for _, tc := range []struct {
		name, path, key, body string
		status                int
	}{
		{"no key", "/v1/models", "", "", 401},
		{"bad key", "/v1/models", "mimir_nope", "", 401},
		{"model the key can't use", "/v1/chat/completions", key, `{"model":"hidden","messages":[{"role":"user","content":"hi"}]}`, 404},
		{"unknown model", "/v1/chat/completions", key, `{"model":"nope","messages":[{"role":"user","content":"hi"}]}`, 404},
		{"tool message", "/v1/chat/completions", key, `{"model":"tiny-llama","messages":[{"role":"tool","content":"hi"}]}`, 400},
		{"no messages", "/v1/chat/completions", key, `{"model":"tiny-llama","messages":[]}`, 400},
		{"two answers", "/v1/chat/completions", key, `{"model":"tiny-llama","n":2,"messages":[{"role":"user","content":"hi"}]}`, 400},
		{"bad JSON", "/v1/completions", key, `{`, 400},
		{"tokens to embed", "/v1/embeddings", key, `{"model":"tiny-llama","input":[[1,2,3]]}`, 400},
		{"nothing to embed", "/v1/embeddings", key, `{"model":"tiny-llama","input":[]}`, 400},
		{"empty text to embed", "/v1/embeddings", key, `{"model":"tiny-llama","input":" "}`, 400},
		{"other dimensions", "/v1/embeddings", key, `{"model":"tiny-llama","input":"hi","dimensions":3}`, 400},
		{"unknown path", "/v1/images/generations", key, `{}`, 404},
	} {
		method := "POST"
		if tc.body == "" {
			method = "GET"
		}
		res := call(method, tc.path, tc.key, tc.body)
		b, _ := io.ReadAll(res.Body)
		if res.StatusCode != tc.status || !strings.Contains(string(b), `"error"`) {
			t.Errorf("%s: %d %s", tc.name, res.StatusCode, b)
		}
	}
}
