// Package docread reads the text out of the files people keep their
// knowledge in (plain text, Markdown, HTML, Word and PDF) and cuts it
// into passages a few paragraphs long, for embedding and search.
package docread

import (
	"archive/zip"
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/ledongthuc/pdf"
	"golang.org/x/net/html"
)

// Kinds of document, by how they are read.
const (
	Text     = "text"
	Markdown = "markdown"
	HTML     = "html"
	Docx     = "docx"
	PDF      = "pdf"
)

// MaxText bounds the text read from one file, so a small file that
// unpacks into a huge one can't take the server's memory.
const MaxText = 32 << 20

// ErrUnknownKind is returned for a file whose name doesn't say how to
// read it.
var ErrUnknownKind = errors.New("docread: unknown kind")

// KindOf returns how a file is read, from its name's extension; "" for a
// kind it can't read.
func KindOf(name string) string {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".txt", ".text", ".csv", ".tsv", ".log", ".json", ".yaml", ".yml":
		return Text
	case ".md", ".markdown":
		return Markdown
	case ".html", ".htm":
		return HTML
	case ".docx":
		return Docx
	case ".pdf":
		return PDF
	}
	return ""
}

// Read returns the text of a file of the given kind, with paragraphs
// apart by blank lines. The error says in plain words what was wrong.
func Read(kind string, data []byte) (text string, err error) {
	switch kind {
	case Text, Markdown:
		text, err = plain(data)
	case HTML:
		text, err = readHTML(data)
	case Docx:
		text, err = readDocx(data)
	case PDF:
		text, err = readPDF(data)
	default:
		return "", ErrUnknownKind
	}
	if err != nil {
		return "", err
	}
	text = tidy(text)
	if strings.TrimSpace(text) == "" {
		return "", errors.New("there is no text in it")
	}
	return text, nil
}

func plain(data []byte) (string, error) {
	data = bytes.TrimPrefix(data, []byte("\ufeff"))
	if !utf8.Valid(data) {
		return "", errors.New("it isn't UTF-8 text")
	}
	if len(data) > MaxText {
		return "", fmt.Errorf("it holds more than %d MB of text", MaxText>>20)
	}
	return string(data), nil
}

// blocks are the HTML elements that start a new paragraph; skipped are
// those whose text isn't the page's.
var (
	blocks = map[string]bool{
		"p": true, "div": true, "br": true, "li": true, "tr": true, "h1": true, "h2": true, "h3": true,
		"h4": true, "h5": true, "h6": true, "section": true, "article": true, "blockquote": true,
		"pre": true, "table": true, "ul": true, "ol": true, "dt": true, "dd": true, "hr": true, "header": true,
		"footer": true, "main": true, "aside": true, "figcaption": true, "title": true,
	}
	skipped = map[string]bool{"script": true, "style": true, "noscript": true, "template": true, "svg": true, "head": false}
)

func readHTML(data []byte) (string, error) {
	if len(data) > MaxText {
		return "", fmt.Errorf("it is more than %d MB", MaxText>>20)
	}
	z := html.NewTokenizer(bytes.NewReader(data))
	var b strings.Builder
	skip := 0
	for {
		switch z.Next() {
		case html.ErrorToken:
			if z.Err() == io.EOF {
				return b.String(), nil
			}
			return "", errors.New("it isn't readable HTML")
		case html.StartTagToken, html.SelfClosingTagToken:
			name, _ := z.TagName()
			tag := string(name)
			if skipped[tag] {
				skip++
			} else if blocks[tag] {
				b.WriteString("\n\n")
			} else if tag == "td" || tag == "th" {
				b.WriteString(" ")
			}
		case html.EndTagToken:
			name, _ := z.TagName()
			tag := string(name)
			if skipped[tag] && skip > 0 {
				skip--
			} else if blocks[tag] {
				b.WriteString("\n\n")
			}
		case html.TextToken:
			if skip == 0 {
				b.Write(z.Text())
			}
		}
	}
}

func readDocx(data []byte) (string, error) {
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return "", errors.New("it isn't a Word document")
	}
	var body *zip.File
	for _, f := range zr.File {
		if f.Name == "word/document.xml" {
			body = f
		}
	}
	if body == nil {
		return "", errors.New("it isn't a Word document")
	}
	rc, err := body.Open()
	if err != nil {
		return "", errors.New("it isn't a readable Word document")
	}
	defer rc.Close()
	d := xml.NewDecoder(io.LimitReader(rc, MaxText))
	var b strings.Builder
	inText := false
	for {
		tok, err := d.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", errors.New("it isn't a readable Word document")
		}
		switch t := tok.(type) {
		case xml.StartElement:
			switch t.Name.Local {
			case "t":
				inText = true
			case "tab":
				b.WriteString("\t")
			case "br", "cr":
				b.WriteString("\n")
			case "tc":
				b.WriteString(" ")
			}
		case xml.EndElement:
			switch t.Name.Local {
			case "t":
				inText = false
			case "p":
				b.WriteString("\n\n")
			}
		case xml.CharData:
			if inText {
				b.Write(t)
			}
		}
	}
	return b.String(), nil
}

func readPDF(data []byte) (text string, err error) {
	// The PDF reader panics on some broken files; one of those is a file
	// it can't read, not a crash.
	defer func() {
		if recover() != nil {
			text, err = "", errors.New("it isn't a readable PDF")
		}
	}()
	r, err := pdf.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		if strings.Contains(err.Error(), "encrypt") {
			return "", errors.New("it is a PDF with a password")
		}
		return "", errors.New("it isn't a readable PDF")
	}
	var b strings.Builder
	for i := 1; i <= r.NumPage(); i++ {
		p := r.Page(i)
		if p.V.IsNull() {
			continue
		}
		rows, err := p.GetTextByRow()
		if err != nil {
			continue
		}
		for _, row := range rows {
			for k, w := range row.Content {
				if k > 0 {
					b.WriteString(" ")
				}
				b.WriteString(w.S)
			}
			b.WriteString("\n")
			if b.Len() > MaxText {
				return "", fmt.Errorf("it holds more than %d MB of text", MaxText>>20)
			}
		}
		b.WriteString("\n\n")
	}
	if strings.TrimSpace(b.String()) == "" {
		return "", errors.New("its pages have no text to read, as in a scan; only PDFs with text can be read")
	}
	return b.String(), nil
}

// tidy makes line ends plain, drops control characters and trailing
// spaces, and keeps at most one blank line between paragraphs.
func tidy(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
	s = strings.ToValidUTF8(s, "")
	s = strings.Map(func(r rune) rune {
		if r == '\n' || r == '\t' || !unicode.IsControl(r) {
			if r == '\u00a0' {
				return ' '
			}
			return r
		}
		return -1
	}, s)
	var b strings.Builder
	gap := false
	for _, l := range strings.Split(s, "\n") {
		l = strings.TrimRightFunc(l, unicode.IsSpace)
		if strings.TrimSpace(l) == "" {
			gap = b.Len() > 0
			continue
		}
		if gap {
			b.WriteString("\n")
			gap = false
		}
		b.WriteString(l)
		b.WriteString("\n")
	}
	return b.String()
}
