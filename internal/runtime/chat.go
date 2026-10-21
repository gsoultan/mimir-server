package runtime

import (
	"context"
	"fmt"
	"slices"
	"strings"
)

// Message is one turn of a chat.
type Message struct {
	// Role is "system", "user" or "assistant".
	Role    string
	Content string
}

// Roles of a chat message.
const (
	RoleSystem    = "system"
	RoleUser      = "user"
	RoleAssistant = "assistant"
)

// chatFormat is how one family of models expects a chat written out: the
// markers around each turn, and where the model's answer starts. A model
// file's chat template is data that is never run (it was a Jinja template
// in GGUF, and running one was CVE-2024-34359); its markers only pick one
// of these fixed formats.
type chatFormat struct {
	name string
	// turn writes one message.
	turn func(role, content string) string
	// reply opens the assistant's turn the model writes.
	reply string
	// stop ends the answer early when the model writes it as text.
	stop []string
	// system says whether the format has a system turn; without one the
	// instructions go in front of the first user message.
	system bool
}

var (
	chatML = chatFormat{
		name:   "chatml",
		turn:   func(r, c string) string { return "<|im_start|>" + r + "\n" + c + "<|im_end|>\n" },
		reply:  "<|im_start|>assistant\n",
		stop:   []string{"<|im_end|>", "<|im_start|>"},
		system: true,
	}
	llama3Chat = chatFormat{
		name: "llama3",
		turn: func(r, c string) string {
			return "<|start_header_id|>" + r + "<|end_header_id|>\n\n" + c + "<|eot_id|>"
		},
		reply:  "<|start_header_id|>assistant<|end_header_id|>\n\n",
		stop:   []string{"<|eot_id|>", "<|start_header_id|>"},
		system: true,
	}
	zephyrChat = chatFormat{
		name:   "zephyr",
		turn:   func(r, c string) string { return "<|" + r + "|>\n" + c + "</s>\n" },
		reply:  "<|assistant|>\n",
		stop:   []string{"</s>", "<|user|>", "<|system|>"},
		system: true,
	}
	instChat = chatFormat{
		name: "inst",
		turn: func(r, c string) string {
			if r == RoleUser {
				return "[INST] " + c + " [/INST]"
			}
			return " " + c + "</s>"
		},
		stop: []string{"</s>", "[INST]"},
	}
	gemmaChat = chatFormat{
		name: "gemma",
		turn: func(r, c string) string {
			if r == RoleAssistant {
				r = "model"
			}
			return "<start_of_turn>" + r + "\n" + c + "<end_of_turn>\n"
		},
		reply: "<start_of_turn>model\n",
		stop:  []string{"<end_of_turn>", "<start_of_turn>"},
	}
	plainChat = chatFormat{
		name: "plain",
		turn: func(r, c string) string {
			switch r {
			case RoleSystem:
				return c + "\n\n"
			case RoleUser:
				return "User: " + c + "\n"
			}
			return "Assistant: " + c + "\n"
		},
		reply:  "Assistant:",
		stop:   []string{"\nUser:", "\nAssistant:"},
		system: true,
	}
)

// formatOf picks the chat format from a model's chat template, or, when it
// has none, from the marker tokens in its vocabulary.
func formatOf(template string, vocab []string) chatFormat {
	has := func(marker string) bool {
		return strings.Contains(template, marker) || (template == "" && slices.Contains(vocab, marker))
	}
	switch {
	case has("<|im_start|>"):
		return chatML
	case has("<|start_header_id|>"):
		return llama3Chat
	case has("<start_of_turn>"):
		return gemmaChat
	case strings.Contains(template, "<|user|>"):
		return zephyrChat
	case strings.Contains(template, "[INST]"):
		return instChat
	}
	return plainChat
}

// render writes a chat out in format f, ending where the model's answer
// starts.
func (f chatFormat) render(msgs []Message) string {
	var b strings.Builder
	var pending string // instructions for a format without a system turn
	for _, m := range msgs {
		c := m.Content
		switch {
		case m.Role == RoleSystem && !f.system:
			pending = c
			continue
		case m.Role == RoleUser && pending != "":
			c, pending = pending+"\n\n"+c, ""
		}
		b.WriteString(f.turn(m.Role, c))
	}
	b.WriteString(f.reply)
	return b.String()
}

// ChatFormat names the chat format the model is written to in: chatml,
// llama3, zephyr, inst, gemma or plain.
func (lm *LanguageModel) ChatFormat() string { return lm.chat.name }

// Chat writes the model's next turn in a chat. msgs must end with a user
// message; a system message, if any, comes first. It fails with
// ErrBadInput for a chat that doesn't fit the model's context.
func (lm *LanguageModel) Chat(ctx context.Context, msgs []Message, s Sampling) (Generation, error) {
	if len(msgs) == 0 || msgs[len(msgs)-1].Role != RoleUser {
		return Generation{}, fmt.Errorf("%w: a chat must end with a user message", ErrBadInput)
	}
	for i, m := range msgs {
		switch {
		case m.Role == RoleSystem && i != 0:
			return Generation{}, fmt.Errorf("%w: only the first message can be the instructions", ErrBadInput)
		case m.Role != RoleSystem && m.Role != RoleUser && m.Role != RoleAssistant:
			return Generation{}, fmt.Errorf("%w: a message's role is system, user or assistant, not %q", ErrBadInput, m.Role)
		}
	}
	s.Stop = append(slices.Clone(s.Stop), lm.chat.stop...)
	s.special = true
	return lm.Generate(ctx, lm.chat.render(msgs), s)
}
