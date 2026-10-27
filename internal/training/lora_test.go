package training

import (
	"errors"
	"strings"
	"testing"
)

func TestParseText(t *testing.T) {
	w, err := ParseText([]byte(`{"messages":[{"role":"user","content":"Hi"},{"role":"assistant","content":"Hello"}]}

{"prompt":"2+2=","completion":"4"}
{"text":"A passage."}
{"text":"  "}
`))
	if err != nil {
		t.Fatal(err)
	}
	if len(w.Examples) != 3 || w.Chats != 1 || w.Prompts != 1 || w.Passages != 1 {
		t.Fatalf("parsed %+v", w)
	}
	if e := w.Examples[1]; e.Prompt != "2+2=" || e.Completion != "4" {
		t.Errorf("prompt %+v", e)
	}

	long := strings.Repeat("word ", 1000) // one paragraph past a passage
	w, err = ParseText([]byte("\ufeffFirst paragraph.\r\n\r\nSecond paragraph.\n\n\n" + long))
	if err != nil {
		t.Fatal(err)
	}
	if len(w.Examples) < 3 || w.Examples[0].Prompt != "First paragraph.\n\nSecond paragraph." {
		t.Fatalf("passages %q", w.Examples)
	}
	for _, e := range w.Examples {
		if len(e.Prompt) > passageBytes || e.Completion != "" {
			t.Errorf("passage of %d bytes, completion %q", len(e.Prompt), e.Completion)
		}
	}

	for _, bad := range []string{
		"",
		"  \n ",
		`{"messages":[{"role":"user","content":"Hi"}]}`,
		`{"messages":[{"role":"user","content":"Hi"},{"role":"system","content":"x"},{"role":"assistant","content":"y"}]}`,
		`{"prompt":"x","completion":""}`,
		`{"other":1}`,
		"{\"text\":\"x\"}\n[1]",
		"caf\xe9",
	} {
		if _, err := ParseText([]byte(bad)); !errors.Is(err, ErrBadTable) {
			t.Errorf("ParseText(%q) = %v", bad, err)
		}
	}
}
