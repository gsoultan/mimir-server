package mapfile

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestOpen(t *testing.T) {
	dir := t.TempDir()
	want := bytes.Repeat([]byte("mimir"), 10000)
	path := filepath.Join(dir, "f")
	if err := os.WriteFile(path, want, 0o600); err != nil {
		t.Fatal(err)
	}
	f, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(f.Bytes(), want) {
		t.Fatal("mapped bytes differ from the file")
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}

	empty := filepath.Join(dir, "empty")
	os.WriteFile(empty, nil, 0o600)
	e, err := Open(empty)
	if err != nil || len(e.Bytes()) != 0 {
		t.Fatalf("empty file: %v, %d bytes", err, len(e.Bytes()))
	}
	e.Close()
}
