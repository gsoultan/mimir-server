package store

import (
	"errors"
	"strings"
	"testing"
)

func TestParseName(t *testing.T) {
	for _, tc := range []struct {
		in, want string
		ok       bool
	}{
		{"  houses ", "houses", true},
		{strings.Repeat("é", MaxName), strings.Repeat("é", MaxName), true},
		{strings.Repeat("a", MaxName+1), "", false},
		{"   ", "", false},
		{"\xff", "", false},
	} {
		n, err := ParseName("model", tc.in)
		if tc.ok != (err == nil) || n.String() != tc.want {
			t.Errorf("ParseName(%q) = %q, %v", tc.in, n, err)
		}
		var inv *InvalidError
		if err != nil && (!errors.As(err, &inv) || !strings.Contains(err.Error(), "model")) {
			t.Errorf("ParseName(%q) refused with %v, want an InvalidError naming the model", tc.in, err)
		}
	}
}

func TestParseDescription(t *testing.T) {
	if d, err := ParseDescription(" "); err != nil || d.String() != "" {
		t.Errorf("an empty description: %q, %v", d, err)
	}
	if _, err := ParseDescription(strings.Repeat("a", MaxDescription+1)); err == nil {
		t.Error("an over-long description was taken")
	}
}
