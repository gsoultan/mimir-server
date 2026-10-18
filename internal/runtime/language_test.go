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
