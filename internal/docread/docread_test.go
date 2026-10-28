package docread

import (
	"archive/zip"
	"bytes"
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestKindOf(t *testing.T) {
	for name, want := range map[string]string{
		"a.TXT": Text, "notes.md": Markdown, "page.htm": HTML, "Policy.docx": Docx, "x.pdf": PDF, "photo.png": "", "noext": "",
	} {
		if got := KindOf(name); got != want {
			t.Errorf("KindOf(%q) = %q, want %q", name, got, want)
		}
	}
}

func TestReadHTML(t *testing.T) {
	got, err := Read(HTML, []byte(`<html><head><title>Returns</title><style>p{color:red}</style></head>
<body><h1>Returns</h1><p>Items can be returned within <b>30 days</b>.</p><script>alert(1)</script><ul><li>Keep the receipt</li></ul></body></html>`))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Returns", "Items can be returned within 30 days.", "Keep the receipt"} {
		if !strings.Contains(got, want) {
			t.Errorf("text %q lacks %q", got, want)
		}
	}
	for _, bad := range []string{"alert", "color:red"} {
		if strings.Contains(got, bad) {
			t.Errorf("text %q has %q", got, bad)
		}
	}
}

func TestReadDocx(t *testing.T) {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, _ := zw.Create("word/document.xml")
	fmt.Fprint(w, `<?xml version="1.0"?><w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"><w:body>
<w:p><w:r><w:t>Refund policy</w:t></w:r></w:p>
<w:p><w:r><w:t xml:space="preserve">Damaged items are </w:t></w:r><w:r><w:t>refunded in full.</w:t></w:r></w:p>
</w:body></w:document>`)
	zw.Close()
	got, err := Read(Docx, buf.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	if want := "Refund policy\n\nDamaged items are refunded in full.\n"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
	if _, err := Read(Docx, []byte("not a zip")); err == nil {
		t.Error("read a docx that isn't one")
	}
}

// minimalPDF writes a one-page PDF that shows lines of text.
func minimalPDF(lines ...string) []byte {
	var content strings.Builder
	content.WriteString("BT /F1 12 Tf 72 720 Td 14 TL\n")
	for _, l := range lines {
		fmt.Fprintf(&content, "(%s) Tj T*\n", l)
	}
	content.WriteString("ET")
	objs := []string{
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
		"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Contents 4 0 R /Resources << /Font << /F1 5 0 R >> >> >>",
		fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", content.Len(), content.String()),
		"<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>",
	}
	var b bytes.Buffer
	b.WriteString("%PDF-1.4\n")
	offsets := make([]int, len(objs))
	for i, o := range objs {
		offsets[i] = b.Len()
		fmt.Fprintf(&b, "%d 0 obj\n%s\nendobj\n", i+1, o)
	}
	xref := b.Len()
	fmt.Fprintf(&b, "xref\n0 %d\n0000000000 65535 f \n", len(objs)+1)
	for _, off := range offsets {
		fmt.Fprintf(&b, "%010d 00000 n \n", off)
	}
	fmt.Fprintf(&b, "trailer\n<< /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", len(objs)+1, xref)
	return b.Bytes()
}

func TestReadPDF(t *testing.T) {
	got, err := Read(PDF, minimalPDF("Opening hours", "We open at nine."))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "Opening hours") || !strings.Contains(got, "We open at nine.") {
		t.Errorf("text %q", got)
	}
	for _, bad := range [][]byte{[]byte("%PDF-1.4\ngarbage"), minimalPDF()[:200]} {
		if _, err := Read(PDF, bad); err == nil {
			t.Errorf("read a broken PDF %q", bad)
		}
	}
}

func TestReadText(t *testing.T) {
	got, err := Read(Text, []byte("\ufeffline one\r\nline two\r\n\r\n\r\n\r\nnext\x00 para  \n"))
	if err != nil {
		t.Fatal(err)
	}
	if want := "line one\nline two\n\nnext para\n"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
	for _, bad := range [][]byte{{0xff, 0xfe, 0x00}, []byte("  \n\n ")} {
		if _, err := Read(Text, bad); err == nil {
			t.Errorf("read %q", bad)
		}
	}
	if _, err := Read("image", []byte("x")); err != ErrUnknownKind {
		t.Errorf("unknown kind: %v", err)
	}
}

func TestSplit(t *testing.T) {
	para := strings.Repeat("Word ", 120) // 600 characters
	text := "# Shipping\n\n" + para + "\n\n" + para + "\n\n## Returns\n\nShort one.\n\n" + strings.Repeat("A long sentence that goes on. ", 200)
	ps := Split(text, Markdown)
	if len(ps) < 4 {
		t.Fatalf("%d passages: %q", len(ps), ps)
	}
	if !strings.HasPrefix(ps[0], "# Shipping\n\n") || !strings.HasPrefix(ps[1], "# Shipping\n\n") {
		t.Errorf("the shipping passages don't say so: %q, %q", ps[0][:20], ps[1][:20])
	}
	if !strings.HasPrefix(ps[2], "## Returns\n\nShort one.") {
		t.Errorf("third passage %q", ps[2][:30])
	}
	for i, p := range ps {
		if n := utf8.RuneCountInString(p); n > Max+30 || n == 0 {
			t.Errorf("passage %d is %d characters", i, n)
		}
	}
	// Text with no breaks at all is still cut.
	ps = Split(strings.Repeat("x", 5000), Text)
	if len(ps) < 4 {
		t.Errorf("%d passages from 5000 characters without breaks", len(ps))
	}
	if got := Split("  \n\n ", Text); len(got) != 0 {
		t.Errorf("passages of nothing: %q", got)
	}
}
