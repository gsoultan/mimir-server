package docread

import (
	"strings"
	"unicode/utf8"
)

// Passage sizes, in characters: a passage grows by paragraphs up to
// Target, and a paragraph longer than Max is cut at sentences, then at
// spaces. About 4 characters make a token, so a passage is 200 to 400
// tokens: enough to stand on its own, small enough that several fit in
// a question.
const (
	Target = 1000
	Max    = 1600
)

// Split cuts text into passages: paragraphs kept whole and together up
// to Target characters, each passage starting with the last heading seen
// when the text is Markdown, so it says what it is about.
func Split(text, kind string) []string {
	var out []string
	var cur strings.Builder
	heading := ""
	flush := func() {
		if t := strings.TrimSpace(cur.String()); t != "" {
			out = append(out, t)
		}
		cur.Reset()
	}
	add := func(p string) {
		if cur.Len() > 0 && utf8.RuneCountInString(cur.String())+utf8.RuneCountInString(p) > Target {
			flush()
		}
		if cur.Len() == 0 && heading != "" && p != heading {
			cur.WriteString(heading)
			cur.WriteString("\n\n")
		} else if cur.Len() > 0 {
			cur.WriteString("\n\n")
		}
		cur.WriteString(p)
	}
	for _, para := range strings.Split(text, "\n\n") {
		para = strings.TrimSpace(para)
		if para == "" {
			continue
		}
		if kind == Markdown && strings.HasPrefix(para, "#") && !strings.Contains(para, "\n") {
			flush()
			heading = para
			continue
		}
		for _, piece := range cut(para) {
			add(piece)
		}
	}
	flush()
	return out
}

// cut splits a paragraph longer than Max at sentence ends, and a
// sentence longer than Max at spaces.
func cut(p string) []string {
	if utf8.RuneCountInString(p) <= Max {
		return []string{p}
	}
	var out []string
	var cur strings.Builder
	for _, s := range sentences(p) {
		if cur.Len() > 0 && utf8.RuneCountInString(cur.String())+utf8.RuneCountInString(s) > Target {
			out = append(out, strings.TrimSpace(cur.String()))
			cur.Reset()
		}
		for utf8.RuneCountInString(s) > Max {
			head, rest := atSpace(s, Target)
			if cur.Len() > 0 {
				out = append(out, strings.TrimSpace(cur.String()))
				cur.Reset()
			}
			out = append(out, strings.TrimSpace(head))
			s = rest
		}
		cur.WriteString(s)
	}
	if t := strings.TrimSpace(cur.String()); t != "" {
		out = append(out, t)
	}
	return out
}

// sentences splits text after ". ", "! ", "? " and line ends, keeping
// the separators.
func sentences(p string) []string {
	var out []string
	start := 0
	for i := 0; i < len(p); i++ {
		c := p[i]
		end := c == '\n' || ((c == '.' || c == '!' || c == '?') && i+1 < len(p) && p[i+1] == ' ')
		if end {
			out = append(out, p[start:i+1])
			start = i + 1
		}
	}
	if start < len(p) {
		out = append(out, p[start:])
	}
	return out
}

// atSpace cuts s near n characters, at the last space before it when
// there is one.
func atSpace(s string, n int) (string, string) {
	i, count := 0, 0
	for i < len(s) && count < n {
		_, size := utf8.DecodeRuneInString(s[i:])
		i += size
		count++
	}
	if j := strings.LastIndexByte(s[:i], ' '); j > n/2 {
		i = j + 1
	}
	return s[:i], s[i:]
}
