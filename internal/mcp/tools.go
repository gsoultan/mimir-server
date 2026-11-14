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

package mcp

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"connectrpc.com/connect"

	mimirv1 "github.com/gsoultan/mimir-server/gen/mimir/v1"
	"github.com/gsoultan/mimir-server/internal/api"
	"github.com/gsoultan/mimir-server/internal/arch"
	"github.com/gsoultan/mimir-server/internal/auth"
	"github.com/gsoultan/mimir-server/internal/runtime"
	"github.com/gsoultan/mimir-server/internal/store"
)

// maxRows bounds how many rows one score_table call asks about.
const maxRows = 1000

// errBadArgs marks a tool's arguments as wrong, so the assistant is told
// why.
var errBadArgs = errors.New("bad arguments")

func badArgs(format string, a ...any) error {
	return fmt.Errorf("%w: "+format, append([]any{errBadArgs}, a...)...)
}

const instructions = `This server holds machine learning models trained on the user's own data, and collections of documents.
Call list_models first to see the models and collections this key may use, and each model's columns.
Ask a model about one case with predict, or about many at once with score_table, giving values by column name.
Find passages in a collection with search_documents, or get an answer written from them, with numbered sources, with ask_documents.`

// tool describes one tool to clients.
type tool struct {
	Name        string         `json:"name"`
	Title       string         `json:"title"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"inputSchema"`
	Annotations map[string]any `json:"annotations"`
}

func object(required []string, props map[string]any) map[string]any {
	s := map[string]any{"type": "object", "properties": props, "additionalProperties": false}
	if len(required) > 0 {
		s["required"] = required
	}
	return s
}

var (
	modelArg  = map[string]any{"type": "string", "description": "The model's name or id, as list_models gives them."}
	fieldsArg = map[string]any{
		"type":                 "object",
		"description":          "The case's values by column name, as list_models lists the model's columns. Columns left out read as empty.",
		"additionalProperties": map[string]any{"type": []string{"string", "number", "boolean", "null"}},
	}
	topKArg       = map[string]any{"type": "integer", "minimum": 1, "maximum": 100, "description": "How many of the likeliest answers to return, for a model that picks a label."}
	collectionArg = map[string]any{"type": "string", "description": "The collection's name or id, as list_models gives them."}
	readOnly      = map[string]any{"readOnlyHint": true, "destructiveHint": false, "openWorldHint": false}
)

var tools = []tool{
	{
		Name: "list_models", Title: "List models",
		Description: "Lists the models and document collections this key may use: for each model what it answers (a label, a number, several numbers, things found in a photo, or text) and the columns to give it.",
		InputSchema: object(nil, map[string]any{}), Annotations: readOnly,
	},
	{
		Name: "predict", Title: "Ask a model",
		Description: "Asks a model about one case, given by column name, and returns its answer: the likeliest labels with their chances, or a number.",
		InputSchema: object([]string{"model", "fields"}, map[string]any{
			"model": modelArg, "fields": fieldsArg, "top_k": topKArg,
			"photo_base64": map[string]any{"type": "string", "description": "A JPG, PNG or GIF in base64, for a model that looks at photos."},
		}),
		Annotations: readOnly,
	},
	{
		Name: "score_table", Title: "Score a table",
		Description: fmt.Sprintf("Asks a model about every row of a table, up to %d rows, and returns an answer for each row in order. A row the model can't read gets an error instead of failing the rest.", maxRows),
		InputSchema: object([]string{"model", "rows"}, map[string]any{
			"model": modelArg, "top_k": topKArg,
			"rows": map[string]any{"type": "array", "maxItems": maxRows, "items": fieldsArg, "description": "The rows, each its values by column name."},
		}),
		Annotations: readOnly,
	},
	{
		Name: "search_documents", Title: "Search documents",
		Description: "Finds the passages of a document collection that best match a query, best first, with the document each comes from.",
		InputSchema: object([]string{"collection", "query"}, map[string]any{
			"collection": collectionArg,
			"query":      map[string]any{"type": "string", "description": "What to look for, in plain words."},
			"limit":      map[string]any{"type": "integer", "minimum": 1, "maximum": 20, "description": "How many passages to return; 5 when left out."},
		}),
		Annotations: readOnly,
	},
	{
		Name: "ask_documents", Title: "Ask documents",
		Description: "Answers a question from a document collection with its own language model, citing the passages it used as [1], [2] and so on, which are returned with the answer.",
		InputSchema: object([]string{"collection", "question"}, map[string]any{
			"collection": collectionArg,
			"question":   map[string]any{"type": "string"},
			"sources":    map[string]any{"type": "integer", "minimum": 1, "maximum": 10, "description": "How many passages to answer from; 5 when left out."},
		}),
		Annotations: readOnly,
	},
}

// calls runs each tool on its arguments.
var calls = map[string]func(*handler, *http.Request, json.RawMessage) (any, error){
	"list_models":      (*handler).listModels,
	"predict":          (*handler).predict,
	"score_table":      (*handler).scoreTable,
	"search_documents": (*handler).searchDocuments,
	"ask_documents":    (*handler).askDocuments,
}

// decode reads a tool's arguments into v, refusing unknown ones.
func decode(args json.RawMessage, v any) error {
	dec := json.NewDecoder(bytes.NewReader(args))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return badArgs("%v", strings.TrimPrefix(err.Error(), "json: "))
	}
	return nil
}

type columnInfo struct {
	Name   string   `json:"name"`
	Kind   string   `json:"kind"`
	Values []string `json:"values,omitempty"`
}

type modelInfo struct {
	ID          string       `json:"id"`
	Name        string       `json:"name"`
	Description string       `json:"description,omitempty"`
	Answers     string       `json:"answers"`
	Columns     []columnInfo `json:"columns,omitempty"`
	Labels      []string     `json:"labels,omitempty"`
	Note        string       `json:"note,omitempty"`
}

type collectionInfo struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// answers says in a word or two what a model answers with.
func answers(architecture string) string {
	a, ok := arch.Lookup(architecture)
	if !ok {
		return "unavailable"
	}
	switch a.Answers() {
	case arch.Number:
		return "number"
	case arch.Several:
		return "numbers"
	case arch.Boxes:
		return "things found"
	case arch.Written:
		return "text"
	}
	return "label"
}

func (h *handler) listModels(r *http.Request, args json.RawMessage) (any, error) {
	ctx := r.Context()
	if err := decode(args, &struct{}{}); err != nil {
		return nil, err
	}
	if p, _ := auth.From(ctx); !p.Can(auth.ModelsRead) {
		return nil, connect.NewError(connect.CodePermissionDenied, errors.New("This key may not list models."))
	}
	res, err := h.svc.ListModels(ctx, connect.NewRequest(&mimirv1.ListModelsRequest{}))
	if err != nil {
		return nil, err
	}
	out := struct {
		Models      []modelInfo      `json:"models"`
		Collections []collectionInfo `json:"collections"`
	}{Models: []modelInfo{}, Collections: []collectionInfo{}}
	for _, m := range res.Msg.GetModels() {
		if m.GetStatus() != mimirv1.ModelStatus_MODEL_STATUS_READY {
			continue
		}
		mi := modelInfo{ID: m.GetId(), Name: m.GetName(), Description: m.GetDescription(), Answers: answers(m.GetArchitecture())}
		for _, c := range m.GetColumns() {
			ci := columnInfo{Name: c.GetName(), Kind: c.GetKind(), Values: c.GetValues()}
			if len(ci.Values) > 30 {
				ci.Values = ci.Values[:30] // the commonest; others are read too
			}
			mi.Columns = append(mi.Columns, ci)
		}
		if mi.Answers != "text" && len(m.GetLabels()) <= 100 {
			mi.Labels = m.GetLabels()
		}
		if mi.Answers == "text" {
			mi.Note = "A language model: use it through the OpenAI-compatible API at /v1, not predict."
		}
		out.Models = append(out.Models, mi)
	}
	cs, err := h.svc.Collections(ctx)
	if err != nil {
		return nil, err
	}
	for _, c := range cs {
		out.Collections = append(out.Collections, collectionInfo{ID: c.ID, Name: c.Name})
	}
	return out, nil
}

// modelID finds the model a name or id stands for, among those the caller
// may use.
func (h *handler) modelID(ctx context.Context, ref string) (string, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return "", badArgs("name the model")
	}
	if store.ValidID(ref) {
		return ref, nil
	}
	res, err := h.svc.ListModels(ctx, connect.NewRequest(&mimirv1.ListModelsRequest{}))
	if err != nil {
		return "", err
	}
	var found []string
	for _, m := range res.Msg.GetModels() {
		if strings.EqualFold(m.GetName(), ref) || api.APIName(m.GetName()) == api.APIName(ref) {
			found = append(found, m.GetId())
		}
	}
	switch len(found) {
	case 0:
		return "", connect.NewError(connect.CodeNotFound, fmt.Errorf("There's no model called %s that this key may use; list_models lists them.", ref))
	case 1:
		return found[0], nil
	}
	return "", badArgs("several models are called %s; send the model's id instead", ref)
}

// fields turns JSON values into the text a model's columns read.
func fields(in map[string]any) (map[string]string, error) {
	out := make(map[string]string, len(in))
	for k, v := range in {
		switch v := v.(type) {
		case nil:
			out[k] = ""
		case string:
			out[k] = v
		case bool:
			out[k] = strconv.FormatBool(v)
		case json.Number:
			out[k] = v.String()
		case float64:
			out[k] = strconv.FormatFloat(v, 'g', -1, 64)
		default:
			return nil, badArgs("the value of %s must be text, a number or true/false", k)
		}
	}
	return out, nil
}

type guess struct {
	Label  string  `json:"label"`
	Chance float32 `json:"chance"`
}

type value struct {
	Label string  `json:"label"`
	Value float64 `json:"value"`
}

type found struct {
	Label  string  `json:"label"`
	Score  float32 `json:"score"`
	Left   float32 `json:"left"`
	Top    float32 `json:"top"`
	Width  float32 `json:"width"`
	Height float32 `json:"height"`
}

// answer is one prediction, as tools return it.
type answer struct {
	Label        string   `json:"label,omitempty"`
	Chance       *float32 `json:"chance,omitempty"`
	Guesses      []guess  `json:"guesses,omitempty"`
	Value        *float64 `json:"value,omitempty"`
	Values       []value  `json:"values,omitempty"`
	Found        []found  `json:"found,omitempty"`
	PredictionID string   `json:"prediction_id,omitempty"`
}

func answerOf(res *mimirv1.PredictResponse) answer {
	a := answer{PredictionID: res.GetPredictionId()}
	switch {
	case res.GetNumber():
		v := res.GetValue()
		a.Value = &v
		for _, nv := range res.GetValues() {
			a.Values = append(a.Values, value{nv.GetLabel(), nv.GetValue()})
		}
	case len(res.GetBoxes()) > 0:
		a.Found = []found{}
		for _, b := range res.GetBoxes() {
			a.Found = append(a.Found, found{b.GetLabel(), b.GetScore(), b.GetLeft(), b.GetTop(), b.GetWidth(), b.GetHeight()})
		}
	case len(res.GetGuesses()) > 0:
		g := res.GetGuesses()
		a.Label = g[0].GetLabel()
		c := g[0].GetProbability()
		a.Chance = &c
		if len(g) > 1 {
			for _, x := range g {
				a.Guesses = append(a.Guesses, guess{x.GetLabel(), x.GetProbability()})
			}
		}
	}
	return a
}

func checkTopK(k int) error {
	if k < 0 || k > 100 {
		return badArgs("top_k must be between 1 and 100")
	}
	return nil
}

func (h *handler) predict(r *http.Request, args json.RawMessage) (any, error) {
	ctx := r.Context()
	var in struct {
		Model       string         `json:"model"`
		Fields      map[string]any `json:"fields"`
		TopK        int            `json:"top_k"`
		PhotoBase64 string         `json:"photo_base64"`
	}
	if err := decode(args, &in); err != nil {
		return nil, err
	}
	if err := checkTopK(in.TopK); err != nil {
		return nil, err
	}
	id, err := h.modelID(ctx, in.Model)
	if err != nil {
		return nil, err
	}
	f, err := fields(in.Fields)
	if err != nil {
		return nil, err
	}
	req := &mimirv1.PredictRequest{ModelId: id, Fields: f, TopK: int32(in.TopK)}
	if in.PhotoBase64 != "" {
		if req.Photo, err = base64.StdEncoding.DecodeString(in.PhotoBase64); err != nil {
			return nil, badArgs("photo_base64 isn't valid base64")
		}
	}
	res, err := h.svc.Predict(ctx, connect.NewRequest(req))
	if err != nil {
		return nil, err
	}
	return answerOf(res.Msg), nil
}

type rowAnswer struct {
	Row int `json:"row"`
	answer
	Error string `json:"error,omitempty"`
}

func (h *handler) scoreTable(r *http.Request, args json.RawMessage) (any, error) {
	ctx := r.Context()
	var in struct {
		Model string           `json:"model"`
		Rows  []map[string]any `json:"rows"`
		TopK  int              `json:"top_k"`
	}
	if err := decode(args, &in); err != nil {
		return nil, err
	}
	switch {
	case len(in.Rows) == 0:
		return nil, badArgs("send at least one row")
	case len(in.Rows) > maxRows:
		return nil, badArgs("send at most %d rows at once", maxRows)
	}
	if err := checkTopK(in.TopK); err != nil {
		return nil, err
	}
	topK := max(in.TopK, 1)
	id, err := h.modelID(ctx, in.Model)
	if err != nil {
		return nil, err
	}
	out := struct {
		Rows   []rowAnswer `json:"rows"`
		Failed int         `json:"failed"`
	}{Rows: make([]rowAnswer, 0, len(in.Rows))}
	for i, row := range in.Rows {
		ra := rowAnswer{Row: i + 1}
		f, err := fields(row)
		var res *connect.Response[mimirv1.PredictResponse]
		if err == nil {
			res, err = h.svc.Predict(ctx, connect.NewRequest(&mimirv1.PredictRequest{ModelId: id, Fields: f, TopK: int32(topK)}))
		}
		switch code := connect.CodeOf(err); {
		case err == nil:
			ra.answer = answerOf(res.Msg)
		case ctx.Err() != nil:
			return nil, ctx.Err()
		case errors.Is(err, errBadArgs), code == connect.CodeInvalidArgument:
			// This row's problem; the others are still asked.
			ra.Error = strings.TrimPrefix(err.Error(), errBadArgs.Error()+": ")
			if ce := new(connect.Error); errors.As(err, &ce) {
				ra.Error = ce.Message()
			}
			out.Failed++
		default:
			return nil, err // the model or the server can't answer at all
		}
		out.Rows = append(out.Rows, ra)
	}
	return out, nil
}

// collection finds the collection a name or id stands for, among those
// the caller may use.
func (h *handler) collection(ctx context.Context, ref string) (store.Collection, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return store.Collection{}, badArgs("name the collection")
	}
	cs, err := h.svc.Collections(ctx)
	if err != nil {
		return store.Collection{}, err
	}
	var found []store.Collection
	for _, c := range cs {
		if c.ID == ref || strings.EqualFold(c.Name, ref) || api.APIName(c.Name) == api.APIName(ref) {
			found = append(found, c)
		}
	}
	switch len(found) {
	case 0:
		return store.Collection{}, connect.NewError(connect.CodeNotFound, fmt.Errorf("There's no collection called %s that this key may use; list_models lists them.", ref))
	case 1:
		return found[0], nil
	}
	return store.Collection{}, badArgs("several collections are called %s; send its id instead", ref)
}

type passage struct {
	Source   int     `json:"source,omitempty"`
	Document string  `json:"document"`
	Number   int32   `json:"passage"`
	Text     string  `json:"text"`
	Score    float64 `json:"score,omitempty"`
}

func (h *handler) searchDocuments(r *http.Request, args json.RawMessage) (any, error) {
	ctx := r.Context()
	var in struct {
		Collection string `json:"collection"`
		Query      string `json:"query"`
		Limit      int    `json:"limit"`
	}
	if err := decode(args, &in); err != nil {
		return nil, err
	}
	c, err := h.collection(ctx, in.Collection)
	if err != nil {
		return nil, err
	}
	res, err := h.svc.Search(ctx, connect.NewRequest(&mimirv1.SearchRequest{CollectionId: c.ID, Query: in.Query, Limit: int32(in.Limit)}))
	if err != nil {
		return nil, err
	}
	out := struct {
		Passages []passage `json:"passages"`
	}{Passages: []passage{}}
	for _, p := range res.Msg.GetPassages() {
		out.Passages = append(out.Passages, passage{Document: p.GetDocumentName(), Number: p.GetNumber(), Text: p.GetText(), Score: p.GetScore()})
	}
	return out, nil
}

func (h *handler) askDocuments(r *http.Request, args json.RawMessage) (any, error) {
	ctx := r.Context()
	var in struct {
		Collection string `json:"collection"`
		Question   string `json:"question"`
		Sources    int    `json:"sources"`
	}
	if err := decode(args, &in); err != nil {
		return nil, err
	}
	switch {
	case strings.TrimSpace(in.Question) == "":
		return nil, badArgs("write the question")
	case in.Sources < 0 || in.Sources > 10:
		return nil, badArgs("sources must be between 1 and 10")
	}
	c, err := h.collection(ctx, in.Collection)
	if err != nil {
		return nil, err
	}
	n := in.Sources
	if n == 0 {
		n = 5
	}
	g, err := h.svc.Ground(ctx, c, []runtime.Message{{Role: runtime.RoleUser, Content: in.Question}}, "", n)
	if err != nil {
		return nil, err
	}
	w := api.Writing{MaxTokens: g.Model.Language.MaxTokens, Temperature: g.Model.Language.Temperature}
	res, used, err := h.svc.WriteGrounded(ctx, g, w, nil)
	if err != nil {
		return nil, err
	}
	out := struct {
		Answer   string    `json:"answer"`
		Finished bool      `json:"finished"`
		Sources  []passage `json:"sources"`
	}{Answer: strings.TrimSpace(res.Text), Finished: res.Finished, Sources: []passage{}}
	for i, p := range used {
		out.Sources = append(out.Sources, passage{Source: i + 1, Document: p.DocumentName, Number: p.Number, Text: p.Text})
	}
	return out, nil
}
