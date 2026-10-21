package runtime

import "testing"

func TestValidUTF8(t *testing.T) {
	for in, want := range map[string]string{
		"hello":              "hello",
		"line\nnext\ttab":    "line\nnext\ttab",
		"bell\a and \x00nul": "bell and nul",
		"caf\xc3":            "caf", // é cut off after its first byte
		"caf\xc3\xa9":        "café",
		"a\xffb":             "a\uFFFDb",
		"":                   "",
	} {
		if got := validUTF8([]byte(in)); got != want {
			t.Errorf("validUTF8(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestStops(t *testing.T) {
	stop := []string{"<|im_end|>", "\nUser:"}
	if at, ok := stopAt([]byte("Hello<|im_end|>more"), stop); !ok || at != 5 {
		t.Errorf("stopAt = %d, %v", at, ok)
	}
	if _, ok := stopAt([]byte("Hello <|im"), stop); ok {
		t.Error("stopAt found a stop string that isn't finished")
	}
	for in, want := range map[string]int{
		"Hello":           5,
		"Hello <|im":      6, // could become <|im_end|>
		"Hello\nUs":       5,
		"caf\xc3":         3, // é not finished yet
		"Hello <|imagine": 15,
	} {
		if got := settled([]byte(in), stop); got != want {
			t.Errorf("settled(%q) = %d, want %d", in, got, want)
		}
	}
}

func TestChatFormats(t *testing.T) {
	msgs := []Message{{RoleSystem, "Be brief."}, {RoleUser, "Hi"}, {RoleAssistant, "Hello!"}, {RoleUser, "Bye"}}
	for _, tc := range []struct {
		template string
		vocab    []string
		want     string
		render   string
	}{
		{"{% for m in messages %}<|im_start|>{{ m.role }}", nil, "chatml",
			"<|im_start|>system\nBe brief.<|im_end|>\n<|im_start|>user\nHi<|im_end|>\n<|im_start|>assistant\nHello!<|im_end|>\n<|im_start|>user\nBye<|im_end|>\n<|im_start|>assistant\n"},
		{"", []string{"a", "<|start_header_id|>"}, "llama3", ""},
		{"{{ '<|user|>\\n' + message['content'] + eos_token }}", nil, "zephyr",
			"<|system|>\nBe brief.</s>\n<|user|>\nHi</s>\n<|assistant|>\nHello!</s>\n<|user|>\nBye</s>\n<|assistant|>\n"},
		{"{{ '[INST] ' + message['content'] + ' [/INST]' }}", nil, "inst",
			"[INST] Be brief.\n\nHi [/INST] Hello!</s>[INST] Bye [/INST]"},
		{"<start_of_turn>", nil, "gemma", ""},
		{"", []string{"a", "b"}, "plain", "Be brief.\n\nUser: Hi\nAssistant: Hello!\nUser: Bye\nAssistant:"},
	} {
		f := formatOf(tc.template, tc.vocab)
		if f.name != tc.want {
			t.Errorf("formatOf(%q) = %s, want %s", tc.template, f.name, tc.want)
			continue
		}
		if tc.render != "" {
			if got := f.render(msgs); got != tc.render {
				t.Errorf("%s render:\n%q\nwant\n%q", f.name, got, tc.render)
			}
		}
	}
}
