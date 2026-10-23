package schema

import (
	"hash/fnv"
	"math"
	"slices"
	"strings"
	"testing"
)

// referenceText is the text encoding as first written, word by word.
func referenceText(s string) []float32 {
	out := make([]float32, TextDims)
	words := Words(s)
	for _, w := range words[:min(len(words), MaxWords)] {
		h := fnv.New32a()
		h.Write([]byte(w))
		out[h.Sum32()%TextDims]++
	}
	var norm float64
	for i, n := range out {
		if n > 0 {
			out[i] = float32(math.Log1p(float64(n)))
			norm += float64(out[i] * out[i])
		}
	}
	if norm > 0 {
		scale := float32(1 / math.Sqrt(norm))
		for i := range out {
			out[i] *= scale
		}
	}
	return out
}

var texts = []string{
	"",
	"   ",
	"hello",
	"Hello, World!",
	"the THE The tHe",
	"it's a dog-eat-dog world; 42 dogs, 7 cats.",
	"Ünïcödé wörds, ÀÉÎÕÜ and straße and İstanbul",
	"日本語のテキスト、そして English mixed",
	"emoji 😀 split words 😀here",
	"bad utf8 \xff\xfe bytes",
	strings.Repeat("word ", MaxWords+50),
	strings.Repeat("a b ", MaxWords),
	"trailing word",
	"leading!!word",
}

func TestEncodeTextMatchesReference(t *testing.T) {
	for _, s := range texts {
		want := referenceText(s)
		got := encodeText(nil, s)
		if !slices.Equal(got, want) {
			t.Errorf("encodeText(%q) differs from the reference encoding", s)
		}
		// Appended after other numbers, and the same.
		if got := encodeText([]float32{1, 2}, s); !slices.Equal(got[2:], want) || got[0] != 1 || got[1] != 2 {
			t.Errorf("encodeText(%q) appended wrongly", s)
		}
		if got, want := CountWords(s), len(Words(s)); got != want {
			t.Errorf("CountWords(%q) = %d, want %d", s, got, want)
		}
	}
}

func TestEncodeRowReportsUnreadableNumbers(t *testing.T) {
	cols := []Column{{Name: "n", Kind: Number, Fill: 2.5}, {Name: "c", Kind: Category, Values: []string{"a", "b"}}, {Name: "t", Kind: Text}}
	var missed []int
	got := EncodeRow(nil, cols, []string{"x", "b", "hi"}, func(j int) { missed = append(missed, j) })
	if !slices.Equal(missed, []int{0}) {
		t.Fatalf("missed %v, want [0]", missed)
	}
	if len(got) != Width(cols) || got[0] != 2.5 || got[1] != 0 || got[2] != 1 {
		t.Fatalf("got %v", got[:3])
	}
	missed = nil
	EncodeRow(nil, cols, []string{"", "a", ""}, func(j int) { missed = append(missed, j) })
	if len(missed) != 0 {
		t.Fatalf("an empty cell counted as unreadable: %v", missed)
	}
	// Encode reads the same row the same way.
	if want := Encode(nil, cols, func(i int) string { return []string{"x", "b", "hi"}[i] }); !slices.Equal(want, got) {
		t.Fatal("Encode and EncodeRow differ")
	}
}
