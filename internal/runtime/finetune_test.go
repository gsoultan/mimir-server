package runtime_test

import (
	"context"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/gsoultan/mimir-server/internal/convert"
	"github.com/gsoultan/mimir-server/internal/runtime"
)

// tinyLlama returns the tiny GGUF language model as a Mimir Model file.
func tinyLlama(t *testing.T) []byte {
	t.Helper()
	b, err := os.ReadFile("../convert/testdata/tiny-llama.gguf")
	if err != nil {
		t.Fatal(err)
	}
	m, _, err := convert.ToMim(b)
	if err != nil {
		t.Fatal(err)
	}
	return m
}

// TestTrainingTokens checks which tokens a fine-tune learns: the
// assistant's answers of a chat, and a completion with the end of text.
func TestTrainingTokens(t *testing.T) {
	lm, err := runtime.OpenLanguageModel(tinyLlama(t))
	if err != nil {
		t.Fatal(err)
	}
	defer lm.Close()

	ids, learn := lm.ChatTokens([]runtime.Message{{Role: runtime.RoleUser, Content: "Hi"}, {Role: runtime.RoleAssistant, Content: "Hello there"}, {Role: runtime.RoleUser, Content: "Bye"}})
	if len(ids) != len(learn) || len(ids) == 0 {
		t.Fatalf("%d tokens, %d flags", len(ids), len(learn))
	}
	// One answer is learned: a run of tokens after the first and before
	// the last user turn.
	first, last := -1, -1
	for i, l := range learn {
		if l {
			if first < 0 {
				first = i
			}
			last = i
		}
	}
	if first <= 0 || last == len(learn)-1 || slices.Contains(learn[first:last+1], false) {
		t.Fatalf("learned %v", learn)
	}

	ids, learn = lm.CompletionTokens("Question?", "Answer.")
	n := 0
	for _, l := range learn {
		if l {
			n++
		}
	}
	if !learn[len(learn)-1] || learn[0] || n == len(ids) || n < 2 {
		t.Errorf("completion learned %v of %v", learn, ids)
	}
	ids, learn = lm.CompletionTokens("A passage.", "")
	if slices.Contains(learn, false) || len(ids) < 2 {
		t.Errorf("passage learned %v", learn)
	}
}

// TestEmbed checks the embeddings of a language model: one per text, of
// length 1, the same for the same text.
func TestEmbed(t *testing.T) {
	lm, err := runtime.OpenLanguageModel(tinyLlama(t))
	if err != nil {
		t.Fatal(err)
	}
	defer lm.Close()
	vecs, tokens, err := lm.Embed(context.Background(), []string{"Once upon a time", "Once upon a time", strings.Repeat("long ", 2000)})
	if err != nil {
		t.Fatal(err)
	}
	if len(vecs) != 3 || tokens <= runtime.MaxEmbedTokens || tokens > 2*4+runtime.MaxEmbedTokens+10 {
		t.Fatalf("%d embeddings of %d tokens", len(vecs), tokens)
	}
	var norm float64
	for i, x := range vecs[0] {
		norm += float64(x) * float64(x)
		if x != vecs[1][i] {
			t.Fatal("the same text embeds differently")
		}
	}
	if norm < 0.999 || norm > 1.001 {
		t.Errorf("squared length %v", norm)
	}
	if _, _, err := lm.Embed(context.Background(), []string{""}); err == nil {
		t.Error("embedded an empty text")
	}
}
