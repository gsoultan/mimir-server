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

package openai

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// Tool calling follows the format most open chat models learned it in
// (Qwen 2.5 and 3, Hermes, and others that copied them): the tools are
// listed as JSON inside <tools> in the instructions, the model writes each
// call as {"name": ..., "arguments": ...} inside <tool_call>, and the
// tools' answers come back inside <tool_response> in a user turn. Models
// that write a bare call object instead, as Llama 3.1 does, are read too.
const (
	callOpen      = "<tool_call>"
	callClose     = "</tool_call>"
	responseOpen  = "<tool_response>"
	responseClose = "</tool_response>"
	maxTools      = 128
)

// tool is one function the caller offers the model.
type tool struct {
	Type     string `json:"type"`
	Function struct {
		Name        string          `json:"name"`
		Description string          `json:"description,omitempty"`
		Parameters  json.RawMessage `json:"parameters,omitempty"`
	} `json:"function"`
}

// toolCall is one call the model asked for, as OpenAI writes it.
type toolCall struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

var toolName = regexp.MustCompile(`^[a-zA-Z0-9_.-]{1,64}$`)

// toolUse is what a chat request says about tools: which the model may
// call, and how.
type toolUse struct {
	tools []tool
	names map[string]bool
	// must says the model has to call a tool; only, when set, is the one
	// it has to call.
	must bool
	only string
	// one keeps the first call only, for parallel_tool_calls false.
	one bool
}

// readTools checks a request's tools and tool_choice. It returns nil when
// the model is offered no tools, including for tool_choice "none".
func readTools(tools []tool, choice json.RawMessage, parallel *bool) (*toolUse, error) {
	if len(tools) > maxTools {
		return nil, fmt.Errorf("tools can have at most %d functions", maxTools)
	}
	u := &toolUse{names: map[string]bool{}, one: parallel != nil && !*parallel}
	for _, t := range tools {
		if t.Type != "function" {
			return nil, fmt.Errorf("tools can be functions only, not %q", t.Type)
		}
		n := t.Function.Name
		if !toolName.MatchString(n) {
			return nil, fmt.Errorf("a function's name is 1 to 64 letters, digits, _, . or -, not %q", n)
		}
		if u.names[n] {
			return nil, fmt.Errorf("two functions are called %s", n)
		}
		if p := bytes.TrimSpace(t.Function.Parameters); len(p) > 0 && p[0] != '{' {
			return nil, fmt.Errorf("the parameters of %s must be a JSON schema object", n)
		}
		u.names[n] = true
		u.tools = append(u.tools, t)
	}
	var mode string
	switch c := bytes.TrimSpace(choice); {
	case len(c) == 0 || string(c) == "null":
		mode = "auto"
	case json.Unmarshal(c, &mode) == nil:
		switch mode {
		case "auto", "none":
		case "required":
			u.must = true
		default:
			return nil, fmt.Errorf("tool_choice is auto, none, required or a function, not %q", mode)
		}
	default:
		var named struct {
			Type     string `json:"type"`
			Function struct {
				Name string `json:"name"`
			} `json:"function"`
		}
		if json.Unmarshal(c, &named) != nil || named.Type != "function" {
			return nil, errors.New(`tool_choice is auto, none, required or {"type": "function", "function": {"name": ...}}`)
		}
		if !u.names[named.Function.Name] {
			return nil, fmt.Errorf("tool_choice names %q, which isn't one of the tools", named.Function.Name)
		}
		u.must, u.only = true, named.Function.Name
	}
	if u.must && len(u.tools) == 0 {
		return nil, errors.New("tool_choice asks for a tool call, but the request has no tools")
	}
	if mode == "none" || len(u.tools) == 0 {
		return nil, nil
	}
	return u, nil
}

// instructions are what the model reads about its tools, after the rest of
// its instructions.
func (u *toolUse) instructions() string {
	var b strings.Builder
	b.WriteString("# Tools\n\nYou may call one or more functions to assist with the user query.\n\n")
	b.WriteString("You are provided with function signatures within <tools></tools> XML tags:\n<tools>")
	for _, t := range u.tools {
		if u.only != "" && t.Function.Name != u.only {
			continue
		}
		sig, _ := json.Marshal(t)
		b.WriteString("\n")
		b.Write(sig)
	}
	b.WriteString("\n</tools>\n\nFor each function call, return a json object with function name and arguments within <tool_call></tool_call> XML tags:\n")
	b.WriteString("<tool_call>\n{\"name\": <function-name>, \"arguments\": <args-json-object>}\n</tool_call>")
	switch {
	case u.only != "":
		fmt.Fprintf(&b, "\n\nCall the function %s now.", u.only)
	case u.must:
		b.WriteString("\n\nCall at least one function now.")
	}
	return b.String()
}

// writeCalls writes an assistant's earlier tool calls as the model writes
// them, after any text it wrote first.
func writeCalls(text string, calls []toolCall) string {
	var b strings.Builder
	b.WriteString(text)
	for _, c := range calls {
		args := json.RawMessage(c.Function.Arguments)
		if !json.Valid(args) {
			args, _ = json.Marshal(c.Function.Arguments)
		}
		call, _ := json.Marshal(struct {
			Name      string          `json:"name"`
			Arguments json.RawMessage `json:"arguments"`
		}{c.Function.Name, args})
		if b.Len() > 0 {
			b.WriteString("\n")
		}
		b.WriteString(callOpen + "\n")
		b.Write(call)
		b.WriteString("\n" + callClose)
	}
	return b.String()
}

// writeResponse writes one tool's answer as the model reads it.
func writeResponse(content string) string {
	return responseOpen + "\n" + content + "\n" + responseClose
}

// read splits what the model wrote into its text and the tool calls in it.
// before is the text written before the first call, as written; a model
// that wrote no call it was offered returns the whole text and no calls.
func (u *toolUse) read(text string) (before string, calls []toolCall) {
	if i := strings.Index(text, callOpen); i >= 0 {
		before = text[:i]
		rest := text[i:]
		for {
			body, ok := strings.CutPrefix(strings.TrimLeft(rest, " \t\r\n"), callOpen)
			if !ok {
				break
			}
			end := strings.Index(body, callClose)
			if end < 0 {
				end = len(body) // the answer ran out before the call was closed
			}
			c, ok := u.call(body[:end])
			if !ok {
				return text, nil
			}
			calls = append(calls, c)
			rest = body[min(end+len(callClose), len(body)):]
		}
	} else if t := strings.TrimSpace(text); strings.HasPrefix(t, "{") {
		// A bare call, or several one per line or separated by ";".
		for _, part := range splitBare(t) {
			c, ok := u.call(part)
			if !ok {
				return text, nil
			}
			calls = append(calls, c)
		}
	}
	if len(calls) == 0 {
		return text, nil
	}
	if u.one {
		calls = calls[:1]
	}
	return before, calls
}

// splitBare splits a text of JSON objects one after another, on lines of
// their own or separated by ";" as Llama 3.1 writes them. It returns nil for
// a text that isn't one.
func splitBare(t string) []string {
	var parts []string
	for t = strings.TrimSpace(t); t != ""; {
		dec := json.NewDecoder(strings.NewReader(t))
		var raw json.RawMessage
		if dec.Decode(&raw) != nil {
			return nil
		}
		parts = append(parts, string(raw))
		t = strings.TrimLeft(t[dec.InputOffset():], " \t\r\n;")
	}
	return parts
}

// call reads one call: {"name": ..., "arguments": {...}}, with
// "parameters" for "arguments" as Llama writes it. A call of a function
// that wasn't offered isn't one.
func (u *toolUse) call(body string) (toolCall, bool) {
	var raw struct {
		Name       string          `json:"name"`
		Arguments  json.RawMessage `json:"arguments"`
		Parameters json.RawMessage `json:"parameters"`
	}
	if json.Unmarshal([]byte(strings.TrimSpace(body)), &raw) != nil || !u.names[raw.Name] {
		return toolCall{}, false
	}
	args := raw.Arguments
	if len(args) == 0 {
		args = raw.Parameters
	}
	var c toolCall
	c.ID, c.Type, c.Function.Name = newCallID(), "function", raw.Name
	var s string
	switch {
	case len(args) == 0 || string(args) == "null":
		c.Function.Arguments = "{}"
	case json.Unmarshal(args, &s) == nil:
		c.Function.Arguments = s // arguments already written as a string of JSON
	default:
		var buf bytes.Buffer
		if json.Compact(&buf, args) != nil {
			return toolCall{}, false
		}
		c.Function.Arguments = buf.String()
	}
	return c, true
}

func newCallID() string {
	var b [12]byte
	rand.Read(b[:])
	return "call_" + hex.EncodeToString(b[:])
}

// streamer passes on a streamed answer's text while holding back what may
// be a tool call, which is sent whole once the answer is done.
type streamer struct {
	send   func(string) error
	held   string
	sent   int  // bytes of the answer passed on
	start  bool // nothing but space written yet
	bare   bool // the answer opened with "{": maybe a bare call
	inCall bool
}

func (s *streamer) write(t string) error {
	if s.inCall || s.bare {
		return nil
	}
	s.held += t
	if !s.start {
		trimmed := strings.TrimLeft(s.held, " \t\r\n")
		if trimmed == "" {
			return nil
		}
		s.start = true
		if strings.HasPrefix(trimmed, "{") {
			s.bare = true
			return nil
		}
	}
	if i := strings.Index(s.held, callOpen); i >= 0 {
		s.inCall = true
		out := s.held[:i]
		s.held = ""
		return s.pass(out)
	}
	// Keep back an ending that may be the start of a call.
	keep := 0
	for k := min(len(callOpen)-1, len(s.held)); k > 0; k-- {
		if strings.HasSuffix(s.held, callOpen[:k]) {
			keep = k
			break
		}
	}
	out := s.held[:len(s.held)-keep]
	s.held = s.held[len(s.held)-keep:]
	return s.pass(out)
}

func (s *streamer) pass(t string) error {
	if t == "" {
		return nil
	}
	s.sent += len(t)
	return s.send(t)
}
