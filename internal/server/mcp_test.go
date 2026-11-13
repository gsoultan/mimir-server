// Copyright (c) 2026 Gembit Soultan Shirazi
// SPDX-License-Identifier: PolyForm-Noncommercial-1.0.0 OR PolyForm-Small-Business-1.0.0 OR LicenseRef-Commercial
//
// This file is part of mimir-server, a server that trains, serves and manages mimir models,
// created and owned by Gembit Soultan Shirazi.
// It is free to use for personal and other noncommercial purposes, for
// nonprofits, and for companies with fewer than 100 people and under
// 1,000,000 USD in yearly revenue, under the PolyForm Noncommercial and
// PolyForm Small Business licenses. Any other use needs a commercial
// license from Gembit Soultan Shirazi. See the LICENSE and LICENSING.md files
// at the root of this repository.

package server_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"connectrpc.com/connect"

	mimirv1 "github.com/gsoultan/mimir-server/gen/mimir/v1"
	"github.com/gsoultan/mimir-server/gen/mimir/v1/mimirv1connect"
)

// TestMCP is an AI assistant using the server's models as an MCP client
// does: it starts a session, lists the tools, and calls them with an API
// key that reaches some models only.
func TestMCP(t *testing.T) {
	ctx := context.Background()
	srv := newServer(t)
	c := owner(t, srv)
	docs := mimirv1connect.NewDocumentServiceClient(srv.Client(), srv.URL, connect.WithInterceptors(c.jar))
	runDocuments(t)
	fruit, _ := trainFruit(t, c)
	hidden := createModel(t, c, "Digits", "mnist-mlp")
	lm := longLanguageModel(t, c, "Tiny Llama")
	col, err := docs.CreateCollection(ctx, connect.NewRequest(&mimirv1.CreateCollectionRequest{Name: "Shop help", EmbeddingModelId: lm.GetId()}))
	if err != nil {
		t.Fatal(err)
	}
	d, err := docs.AddDocument(ctx, connect.NewRequest(&mimirv1.AddDocumentRequest{CollectionId: col.Msg.GetCollection().GetId(), Name: "guide.md", Data: []byte(shopGuide)}))
	if err != nil {
		t.Fatal(err)
	}
	waitDocument(t, docs, d.Msg.GetDocument().GetId())
	created, err := c.keys.CreateApiKey(ctx, connect.NewRequest(&mimirv1.CreateApiKeyRequest{
		Name: "assistant", Access: &mimirv1.Access{Role: mimirv1.Role_ROLE_VIEWER, ModelIds: []string{fruit, lm.GetId()}},
	}))
	if err != nil {
		t.Fatal(err)
	}
	key := created.Msg.GetSecret()

	post := func(key, body string) *http.Response {
		t.Helper()
		req, _ := http.NewRequest("POST", srv.URL+"/mcp", strings.NewReader(body))
		if key != "" {
			req.Header.Set("Authorization", "Bearer "+key)
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json, text/event-stream")
		res, err := srv.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { res.Body.Close() })
		return res
	}
	type rpcResult struct {
		Result struct {
			ProtocolVersion   string            `json:"protocolVersion"`
			Tools             []json.RawMessage `json:"tools"`
			IsError           bool              `json:"isError"`
			Content           []struct{ Text string }
			StructuredContent map[string]any `json:"structuredContent"`
		} `json:"result"`
		Error *struct {
			Code    int
			Message string
		} `json:"error"`
	}
	rpc := func(method, params string) rpcResult {
		t.Helper()
		res := post(key, `{"jsonrpc":"2.0","id":1,"method":"`+method+`","params":`+params+`}`)
		var out rpcResult
		if res.StatusCode != 200 || json.NewDecoder(res.Body).Decode(&out) != nil {
			t.Fatalf("%s: %d", method, res.StatusCode)
		}
		return out
	}
	call := func(tool, args string) rpcResult {
		t.Helper()
		return rpc("tools/call", `{"name":"`+tool+`","arguments":`+args+`}`)
	}

	if r := rpc("initialize", `{"protocolVersion":"2025-03-26","capabilities":{},"clientInfo":{"name":"test","version":"1"}}`); r.Result.ProtocolVersion != "2025-03-26" {
		t.Errorf("initialize: %+v", r)
	}
	if r := rpc("initialize", `{"protocolVersion":"1999-01-01"}`); r.Result.ProtocolVersion != "2025-06-18" {
		t.Errorf("initialize with an unknown version: %+v", r)
	}
	if res := post(key, `{"jsonrpc":"2.0","method":"notifications/initialized"}`); res.StatusCode != http.StatusAccepted {
		t.Errorf("notification: %d", res.StatusCode)
	}
	if r := rpc("tools/list", `{}`); len(r.Result.Tools) != 5 {
		t.Errorf("tools: %d", len(r.Result.Tools))
	}

	r := call("list_models", `{}`)
	b, _ := json.Marshal(r.Result.StructuredContent)
	if r.Result.IsError || !strings.Contains(string(b), `"name":"width"`) || !strings.Contains(string(b), `"Shop help"`) || strings.Contains(string(b), hidden.GetId()) {
		t.Fatalf("list_models: %s", b)
	}

	r = call("predict", `{"model":"`+fruit+`","fields":{"width":1.2,"height":"0.9"}}`)
	if r.Result.IsError || r.Result.StructuredContent["label"] != "apple" || r.Result.StructuredContent["prediction_id"] == "" {
		t.Fatalf("predict: %+v", r.Result)
	}
	r = call("predict", `{"model":"fruit","fields":{"width":1.2,"height":0.9}}`)
	if r.Result.IsError || r.Result.StructuredContent["label"] != "apple" {
		t.Errorf("predict by name: %+v", r.Result)
	}

	r = call("score_table", `{"model":"Fruit","rows":[{"width":1.2,"height":0.9},{"width":"lots"},{"width":1.2,"height":0.9}]}`)
	rows, _ := r.Result.StructuredContent["rows"].([]any)
	if r.Result.IsError || len(rows) != 3 || r.Result.StructuredContent["failed"] != 1.0 {
		t.Fatalf("score_table: %+v", r.Result)
	}
	if row := rows[1].(map[string]any); row["error"] == "" || row["row"] != 2.0 {
		t.Errorf("bad row %v", row)
	}
	if row := rows[2].(map[string]any); row["label"] != "apple" {
		t.Errorf("third row %v", row)
	}

	r = call("search_documents", `{"collection":"shop-help","query":"zebra mascot","limit":1}`)
	ps, _ := r.Result.StructuredContent["passages"].([]any)
	if r.Result.IsError || len(ps) != 1 || !strings.Contains(ps[0].(map[string]any)["text"].(string), "zebra") {
		t.Fatalf("search_documents: %+v", r.Result)
	}
	r = call("ask_documents", `{"collection":"Shop help","question":"Who greets visitors?","sources":2}`)
	if src, _ := r.Result.StructuredContent["sources"].([]any); r.Result.IsError || len(src) == 0 {
		t.Fatalf("ask_documents: %+v", r.Result)
	}

	// Mistakes are tool results the assistant reads, except for asking a
	// tool that doesn't exist.
	for _, tc := range []struct{ tool, args, says string }{
		{"predict", `{"model":"` + hidden.GetId() + `","fields":{}}`, "No model"},
		{"predict", `{"model":"Digits","fields":{}}`, "no model called"},
		{"predict", `{"model":"Fruit","fields":{},"colour":"red"}`, "unknown field"},
		{"score_table", `{"model":"Fruit","rows":[]}`, "at least one row"},
		{"search_documents", `{"collection":"nope","query":"x"}`, "no collection"},
		{"ask_documents", `{"collection":"Shop help","question":" "}`, "question"},
	} {
		r := call(tc.tool, tc.args)
		if !r.Result.IsError || len(r.Result.Content) != 1 || !strings.Contains(r.Result.Content[0].Text, tc.says) {
			t.Errorf("%s %s: %+v", tc.tool, tc.args, r.Result)
		}
	}
	if r := call("delete_everything", `{}`); r.Error == nil || r.Error.Code != -32602 {
		t.Errorf("unknown tool: %+v", r)
	}
	if r := rpc("resources/list", `{}`); r.Error == nil || r.Error.Code != -32601 {
		t.Errorf("unknown method: %+v", r)
	}

	if res := post("", `{"jsonrpc":"2.0","id":1,"method":"ping"}`); res.StatusCode != 401 || !strings.HasPrefix(res.Header.Get("WWW-Authenticate"), "Bearer") {
		t.Errorf("no key: %d %v", res.StatusCode, res.Header)
	}
	if res := post("mimir_nope", `{"jsonrpc":"2.0","id":1,"method":"ping"}`); res.StatusCode != 401 {
		t.Errorf("bad key: %d", res.StatusCode)
	}
	if res := post(key, `{"jsonrpc":`); res.StatusCode != 400 {
		t.Errorf("bad JSON: %d", res.StatusCode)
	}
	res := post(key, `[{"jsonrpc":"2.0","id":1,"method":"ping"},{"jsonrpc":"2.0","method":"notifications/initialized"},{"jsonrpc":"2.0","id":2,"method":"ping"}]`)
	var batch []json.RawMessage
	if b, _ := io.ReadAll(res.Body); res.StatusCode != 200 || json.Unmarshal(b, &batch) != nil || len(batch) != 2 {
		t.Errorf("batch: %d %s", res.StatusCode, b)
	}
	req, _ := http.NewRequest("GET", srv.URL+"/mcp", nil)
	req.Header.Set("Authorization", "Bearer "+key)
	if res, err := srv.Client().Do(req); err != nil || res.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("GET: %v %v", res, err)
	} else {
		res.Body.Close()
	}
}
