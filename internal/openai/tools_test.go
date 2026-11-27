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
	"encoding/json"
	"strings"
	"testing"
)

func weatherTools(t *testing.T, choice string, parallel *bool) *toolUse {
	t.Helper()
	var tools []tool
	if err := json.Unmarshal([]byte(`[
		{"type":"function","function":{"name":"get_weather","description":"Weather in a city","parameters":{"type":"object","properties":{"city":{"type":"string"}}}}},
		{"type":"function","function":{"name":"get_time"}}
	]`), &tools); err != nil {
		t.Fatal(err)
	}
	u, err := readTools(tools, json.RawMessage(choice), parallel)
	if err != nil {
		t.Fatal(err)
	}
	return u
}

func TestReadTools(t *testing.T) {
	if u := weatherTools(t, `"none"`, nil); u != nil {
		t.Errorf("tool_choice none offers %v", u)
	}
	u := weatherTools(t, ``, nil)
	if u == nil || u.must || len(u.tools) != 2 {
		t.Fatalf("auto: %+v", u)
	}
	ins := u.instructions()
	if !strings.Contains(ins, "<tools>") || !strings.Contains(ins, `"name":"get_weather"`) || !strings.Contains(ins, `"name":"get_time"`) {
		t.Errorf("instructions %q", ins)
	}
	u = weatherTools(t, `{"type":"function","function":{"name":"get_time"}}`, nil)
	if !u.must || u.only != "get_time" || strings.Contains(u.instructions(), "get_weather") {
		t.Errorf("named choice %+v", u)
	}
	if u := weatherTools(t, `"required"`, nil); !u.must || !strings.Contains(u.instructions(), "at least one") {
		t.Errorf("required %+v", u)
	}

	for _, bad := range []struct{ tools, choice string }{
		{`[{"type":"retrieval"}]`, ``},
		{`[{"type":"function","function":{"name":"has space"}}]`, ``},
		{`[{"type":"function","function":{"name":"a"}},{"type":"function","function":{"name":"a"}}]`, ``},
		{`[{"type":"function","function":{"name":"a","parameters":[1]}}]`, ``},
		{`[{"type":"function","function":{"name":"a"}}]`, `"sometimes"`},
		{`[{"type":"function","function":{"name":"a"}}]`, `{"type":"function","function":{"name":"b"}}`},
		{`[]`, `"required"`},
	} {
		var tools []tool
		json.Unmarshal([]byte(bad.tools), &tools)
		if _, err := readTools(tools, json.RawMessage(bad.choice), nil); err == nil {
			t.Errorf("tools %s, choice %s: no error", bad.tools, bad.choice)
		}
	}
}

func TestReadCalls(t *testing.T) {
	u := weatherTools(t, ``, nil)
	for _, tc := range []struct {
		name, text, before string
		calls              []string // name and arguments of each
	}{
		{"text only", "It's sunny.", "It's sunny.", nil},
		{"tagged call", "<tool_call>\n{\"name\": \"get_weather\", \"arguments\": {\"city\": \"Oslo\"}}\n</tool_call>", "", []string{`get_weather {"city":"Oslo"}`}},
		{"text then calls", "Let me check.\n<tool_call>\n{\"name\":\"get_weather\",\"arguments\":{\"city\":\"Oslo\"}}\n</tool_call>\n<tool_call>\n{\"name\":\"get_time\",\"arguments\":{}}\n</tool_call>", "Let me check.\n", []string{`get_weather {"city":"Oslo"}`, `get_time {}`}},
		{"unclosed call", "<tool_call>\n{\"name\":\"get_time\",\"arguments\":{}}", "", []string{`get_time {}`}},
		{"arguments as a string", `<tool_call>{"name":"get_weather","arguments":"{\"city\":\"Rome\"}"}</tool_call>`, "", []string{`get_weather {"city":"Rome"}`}},
		{"bare llama call", `{"name": "get_weather", "parameters": {"city": "Lima"}}`, "", []string{`get_weather {"city":"Lima"}`}},
		{"bare calls with ;", `{"name": "get_time", "parameters": {}}; {"name": "get_weather", "parameters": {"city": "Lima"}}`, "", []string{`get_time {}`, `get_weather {"city":"Lima"}`}},
		{"unknown function", `<tool_call>{"name":"rm_rf","arguments":{}}</tool_call>`, `<tool_call>{"name":"rm_rf","arguments":{}}</tool_call>`, nil},
		{"JSON that isn't a call", `{"answer": 42}`, `{"answer": 42}`, nil},
		{"broken JSON", "<tool_call>{\"name\":", "<tool_call>{\"name\":", nil},
	} {
		before, calls := u.read(tc.text)
		var got []string
		for _, c := range calls {
			if !strings.HasPrefix(c.ID, "call_") || c.Type != "function" {
				t.Errorf("%s: call %+v", tc.name, c)
			}
			got = append(got, c.Function.Name+" "+c.Function.Arguments)
		}
		if before != tc.before || strings.Join(got, "|") != strings.Join(tc.calls, "|") {
			t.Errorf("%s: read %q, %v; want %q, %v", tc.name, before, got, tc.before, tc.calls)
		}
	}

	no := false
	one := weatherTools(t, ``, &no)
	if _, calls := one.read(`{"name":"get_time","parameters":{}}; {"name":"get_time","parameters":{}}`); len(calls) != 1 {
		t.Errorf("parallel_tool_calls false kept %d calls", len(calls))
	}
}

func TestWriteCalls(t *testing.T) {
	var c toolCall
	c.Function.Name, c.Function.Arguments = "get_weather", `{"city":"Oslo"}`
	got := writeCalls("Checking.", []toolCall{c})
	want := "Checking.\n<tool_call>\n{\"name\":\"get_weather\",\"arguments\":{\"city\":\"Oslo\"}}\n</tool_call>"
	if got != want {
		t.Errorf("writeCalls = %q", got)
	}
	// What the model is shown it wrote reads back as the same call.
	if _, calls := weatherTools(t, ``, nil).read(got); len(calls) != 1 || calls[0].Function.Arguments != c.Function.Arguments {
		t.Errorf("read back %v", calls)
	}
	c.Function.Arguments = "not json"
	if got := writeCalls("", []toolCall{c}); !strings.Contains(got, `"arguments":"not json"`) {
		t.Errorf("arguments that aren't JSON: %q", got)
	}
}

func TestStreamer(t *testing.T) {
	for _, tc := range []struct {
		name   string
		pieces []string
		sent   string
	}{
		{"plain text", []string{"Hel", "lo <", "b>"}, "Hello <b>"},
		{"text then call", []string{"Sure.", " <tool", "_call>", "{\"name\":"}, "Sure. "},
		{"bare call", []string{"  ", "{\"na", "me\":"}, ""},
		{"partial marker held", []string{"a <tool_c"}, "a "},
	} {
		var out strings.Builder
		s := &streamer{send: func(t string) error { out.WriteString(t); return nil }}
		for _, p := range tc.pieces {
			s.write(p)
		}
		if out.String() != tc.sent || s.sent != len(tc.sent) {
			t.Errorf("%s: sent %q (%d)", tc.name, out.String(), s.sent)
		}
	}
}
