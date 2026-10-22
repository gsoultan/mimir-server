package config

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/gsoultan/mimir-server/internal/secret"
)

func box(t *testing.T) *secret.Box {
	t.Helper()
	k, err := secret.Generate()
	if err != nil {
		t.Fatal(err)
	}
	b, err := secret.New(k)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestWriteIsPrivateAndNeverReplaces(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", DefaultName)
	want := File{DatabaseURL: "postgres://mimir:hunter2@db/mimir", LogDatabaseURL: "postgres://log@db/log"}
	if err := Write(path, want); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Errorf("mode %04o, want 0600", info.Mode().Perm())
	}
	if dir, _ := os.Stat(filepath.Dir(path)); runtime.GOOS != "windows" && dir.Mode().Perm() != 0o700 {
		t.Errorf("folder mode %04o, want 0700", dir.Mode().Perm())
	}
	got, err := Read(path)
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Errorf("read %+v, want %+v", got, want)
	}
	if err := Write(path, File{DatabaseURL: "postgres://other@db/x"}); !errors.Is(err, ErrExists) {
		t.Fatalf("second write: %v, want ErrExists", err)
	}
	if got, _ := Read(path); got != want {
		t.Error("second write changed the file")
	}
	// No temporary files are left behind.
	entries, _ := os.ReadDir(filepath.Dir(path))
	if len(entries) != 1 {
		t.Errorf("folder holds %d entries, want just the file", len(entries))
	}
}

func TestReadRefusesFilesOthersCanOpen(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("no Unix modes")
	}
	path := filepath.Join(t.TempDir(), DefaultName)
	if err := os.WriteFile(path, []byte(`{"database_url":"postgres://a@b/c"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Read(path); err == nil || !strings.Contains(err.Error(), "chmod 600") {
		t.Fatalf("Read of a 0644 file: %v", err)
	}
	if _, err := Read(filepath.Join(t.TempDir(), "missing.json")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("missing file: %v", err)
	}
	os.Chmod(path, 0o600)
	os.WriteFile(path, []byte(`{"database_url":"x","password":"y"}`), 0o600)
	if _, err := Read(path); err == nil {
		t.Fatal("an unknown field was accepted")
	}
}

func TestSealKeepsPasswordsOutOfTheFile(t *testing.T) {
	b := box(t)
	path := filepath.Join(t.TempDir(), DefaultName)
	plain := File{DatabaseURL: "postgres://mimir:hunter2@db/mimir", LogDatabaseURL: "env:LOG_URL"}
	sealed, err := plain.Seal(b)
	if err != nil {
		t.Fatal(err)
	}
	if err := Write(path, sealed); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(path)
	if strings.Contains(string(raw), "hunter2") {
		t.Fatalf("the password is in the file: %s", raw)
	}
	if !strings.Contains(string(raw), `"env:LOG_URL"`) {
		t.Fatalf("a reference was sealed: %s", raw)
	}
	t.Setenv("LOG_URL", "postgres://log@db/log")
	f, _ := Read(path)
	opened, err := f.Open(b)
	if err != nil {
		t.Fatal(err)
	}
	if opened.DatabaseURL != plain.DatabaseURL || opened.LogDatabaseURL != "postgres://log@db/log" {
		t.Errorf("opened %+v", opened)
	}
	if _, err := f.Open(box(t)); !errors.Is(err, secret.ErrWrongKey) {
		t.Errorf("opened with another key: %v", err)
	}
	// A sealed value moved to the other field does not open there.
	swapped := File{DatabaseURL: "postgres://x@y/z", LogDatabaseURL: f.DatabaseURL}
	if _, err := swapped.Open(b); err == nil {
		t.Error("a sealed value opened in another field")
	}
}

func TestResolveReferences(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "db-url")
	os.WriteFile(file, []byte("postgres://f@db/f\n"), 0o600)
	t.Setenv("MIMIR_TEST_URL", "postgres://e@db/e")
	for in, want := range map[string]string{
		"env:MIMIR_TEST_URL": "postgres://e@db/e",
		"file:" + file:       "postgres://f@db/f",
		"postgres://p@db/p":  "postgres://p@db/p",
		"":                   "",
	} {
		got, err := Resolve(nil, in, "database_url")
		if err != nil || got != want {
			t.Errorf("Resolve(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	if _, err := Resolve(nil, "env:MIMIR_TEST_UNSET_VAR", "database_url"); err == nil {
		t.Error("an unset variable resolved")
	}
}
