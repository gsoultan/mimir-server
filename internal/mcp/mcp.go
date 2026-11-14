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

// Package mcp lets AI assistants use the server's models through the
// Model Context Protocol, at /mcp over its Streamable HTTP transport.
// Claude, ChatGPT, Cursor and other MCP clients connect with a mimir API
// key and get tools to list the models and collections the key may use,
// ask a model about one row, score a whole table, and search or ask a
// collection of documents. Keys, per-model access and limits apply as for
// any other call.
//
// The server is stateless: every request stands alone, so it hands out no
// session id and opens no stream of its own (GET is refused, as the
// transport allows).
package mcp

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"runtime/debug"
	"slices"
	"strings"

	"connectrpc.com/connect"

	"github.com/gsoultan/mimir-server/internal/api"
	"github.com/gsoultan/mimir-server/internal/auth"
	"github.com/gsoultan/mimir-server/internal/store"
)

// Path is where the server answers MCP clients.
const Path = "/mcp"

// maxBody is the largest request read: room for a table of a few
// thousand short rows.
const maxBody = 4 << 20

// versions are the protocol versions spoken, newest first.
var versions = []string{"2025-06-18", "2025-03-26", "2024-11-05"}

// JSON-RPC error codes.
const (
	parseError     = -32700
	invalidRequest = -32600
	noMethod       = -32601
	invalidParams  = -32602
)

// Handler serves MCP at Path.
func Handler(svc *api.Service, st *store.Store, log *slog.Logger) http.Handler {
	h := &handler{svc: svc, st: st, log: log}
	return http.HandlerFunc(h.serve)
}

type handler struct {
	svc *api.Service
	st  *store.Store
	log *slog.Logger
}

type request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type response struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (h *handler) serve(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		http.Error(w, "This MCP server answers POST requests only; it opens no stream of its own.", http.StatusMethodNotAllowed)
		return
	}
	p, ok := h.signIn(w, r)
	if !ok {
		return
	}
	ctx := auth.WithPrincipal(r.Context(), p)
	b, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBody))
	if err != nil {
		writeJSON(w, http.StatusRequestEntityTooLarge, response{JSONRPC: "2.0", ID: json.RawMessage("null"), Error: &rpcError{invalidRequest, "A request can be at most 4 MB."}})
		return
	}
	b = bytes.TrimSpace(b)
	// A list of messages is a batch, as the 2025-03-26 version allows.
	if len(b) > 0 && b[0] == '[' {
		var batch []json.RawMessage
		if json.Unmarshal(b, &batch) != nil || len(batch) == 0 {
			writeJSON(w, http.StatusBadRequest, response{JSONRPC: "2.0", ID: json.RawMessage("null"), Error: &rpcError{parseError, "The request isn't valid JSON-RPC."}})
			return
		}
		var out []response
		for _, m := range batch {
			if res, ok := h.handle(r.WithContext(ctx), m); ok {
				out = append(out, res)
			}
		}
		if len(out) == 0 {
			w.WriteHeader(http.StatusAccepted)
			return
		}
		writeJSON(w, http.StatusOK, out)
		return
	}
	res, ok := h.handle(r.WithContext(ctx), b)
	if !ok {
		w.WriteHeader(http.StatusAccepted) // a notification or a client's response
		return
	}
	status := http.StatusOK
	if res.Error != nil && (res.Error.Code == parseError || res.Error.Code == invalidRequest) {
		status = http.StatusBadRequest
	}
	writeJSON(w, status, res)
}

// signIn checks the request's mimir API key, answering a missing or bad
// one itself.
func (h *handler) signIn(w http.ResponseWriter, r *http.Request) (auth.Principal, bool) {
	refuse := func(status int, msg string) {
		if status == http.StatusUnauthorized {
			w.Header().Set("WWW-Authenticate", `Bearer realm="mimir"`)
		}
		http.Error(w, msg, status)
	}
	if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") {
		refuse(http.StatusUnauthorized, "Send a mimir API key: Authorization: Bearer mimir_...")
		return auth.Principal{}, false
	}
	p, err := auth.Authenticate(r.Context(), h.st, r.Header, r.RemoteAddr)
	if err != nil || p == nil || p.Key == nil {
		msg := "That API key isn't valid. It may have been revoked."
		if ce := new(connect.Error); errors.As(err, &ce) && connect.CodeOf(err) == connect.CodeUnauthenticated {
			msg = ce.Message()
		}
		refuse(http.StatusUnauthorized, msg)
		return auth.Principal{}, false
	}
	if !p.Can(auth.ModelsPredict) {
		refuse(http.StatusForbidden, "This key may not ask models.")
		return auth.Principal{}, false
	}
	return *p, true
}

// handle answers one message; ok is false when it needs no answer.
func (h *handler) handle(r *http.Request, b []byte) (res response, ok bool) {
	var req request
	if err := json.Unmarshal(b, &req); err != nil {
		return response{JSONRPC: "2.0", ID: json.RawMessage("null"), Error: &rpcError{parseError, "The request isn't valid JSON-RPC: " + err.Error()}}, true
	}
	if req.ID == nil || string(req.ID) == "null" {
		return response{}, false // notifications, such as notifications/initialized
	}
	res = response{JSONRPC: "2.0", ID: req.ID}
	if req.JSONRPC != "2.0" || req.Method == "" {
		res.Error = &rpcError{invalidRequest, `A request needs "jsonrpc": "2.0" and a method.`}
		return res, true
	}
	switch req.Method {
	case "initialize":
		var params struct {
			ProtocolVersion string `json:"protocolVersion"`
		}
		json.Unmarshal(req.Params, &params)
		version := versions[0]
		if slices.Contains(versions, params.ProtocolVersion) {
			version = params.ProtocolVersion
		}
		res.Result = map[string]any{
			"protocolVersion": version,
			"capabilities":    map[string]any{"tools": map[string]any{"listChanged": false}},
			"serverInfo":      map[string]any{"name": "mimir-server", "title": "mimir", "version": serverVersion()},
			"instructions":    instructions,
		}
	case "ping":
		res.Result = map[string]any{}
	case "tools/list":
		res.Result = map[string]any{"tools": tools}
	case "tools/call":
		var params struct {
			Name      string          `json:"name"`
			Arguments json.RawMessage `json:"arguments"`
		}
		if json.Unmarshal(req.Params, &params) != nil {
			res.Error = &rpcError{invalidParams, "tools/call takes a name and arguments."}
			return res, true
		}
		run, known := calls[params.Name]
		if !known {
			res.Error = &rpcError{invalidParams, "There's no tool called " + params.Name + "."}
			return res, true
		}
		if len(params.Arguments) == 0 || string(params.Arguments) == "null" {
			params.Arguments = json.RawMessage("{}")
		}
		out, err := run(h, r, params.Arguments)
		res.Result = h.result(r, out, err)
	default:
		res.Error = &rpcError{noMethod, "This server doesn't answer " + req.Method + "."}
	}
	return res, true
}

// result is a tool's answer as MCP sends it: as JSON text for every client,
// and as structured content for those that read it. A failed call is a
// result too, so the assistant reads why and can try again.
func (h *handler) result(r *http.Request, out any, err error) map[string]any {
	if err != nil {
		var ce *connect.Error
		msg := "Something went wrong on the server."
		switch {
		case errors.As(err, &ce) && connect.CodeOf(err) != connect.CodeInternal && connect.CodeOf(err) != connect.CodeUnknown:
			msg = ce.Message()
		case errors.Is(err, errBadArgs):
			msg = err.Error()
		default:
			h.log.ErrorContext(r.Context(), "mcp", "error", err)
		}
		return map[string]any{"content": []any{map[string]any{"type": "text", "text": msg}}, "isError": true}
	}
	b, _ := json.MarshalIndent(out, "", "  ")
	return map[string]any{
		"content":           []any{map[string]any{"type": "text", "text": string(b)}},
		"structuredContent": out,
	}
}

func serverVersion() string {
	if bi, ok := debug.ReadBuildInfo(); ok && bi.Main.Version != "" && bi.Main.Version != "(devel)" {
		return strings.TrimPrefix(bi.Main.Version, "v")
	}
	return "dev"
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}
