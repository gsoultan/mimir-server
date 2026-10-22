package backup

import (
	"archive/tar"
	"compress/gzip"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

// The data folder comes back as it was, less what was skipped, and a
// second restore over it needs replace.
func TestFilesRoundTrip(t *testing.T) {
	data := t.TempDir()
	write(t, filepath.Join(data, "models", "a.mim"), "weights")
	write(t, filepath.Join(data, "datasets", "d1", "rows.csv"), "x,y\n1,2\n")
	write(t, filepath.Join(data, "secret.key"), "the key")
	write(t, filepath.Join(data, "mimir.json"), "{}")
	write(t, filepath.Join(data, "backups", "old", "main.dump"), "old")
	if err := os.Symlink("/etc/passwd", filepath.Join(data, "link")); err != nil {
		t.Fatal(err)
	}

	archive := filepath.Join(t.TempDir(), FilesArchive)
	skip := []string{filepath.Join(data, "secret.key"), filepath.Join(data, "mimir.json"), filepath.Join(data, "backups"), ""}
	n, err := ArchiveFiles(data, archive, skip)
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Errorf("archived %d files, want 2", n)
	}

	to := t.TempDir()
	if n, err := ExtractFiles(archive, to, false); err != nil || n != 2 {
		t.Fatalf("ExtractFiles = %d, %v; want 2", n, err)
	}
	for name, want := range map[string]string{"models/a.mim": "weights", "datasets/d1/rows.csv": "x,y\n1,2\n"} {
		b, err := os.ReadFile(filepath.Join(to, name))
		if err != nil || string(b) != want {
			t.Errorf("%s = %q, %v; want %q", name, b, err, want)
		}
	}
	for _, name := range []string{"secret.key", "mimir.json", "backups", "link"} {
		if _, err := os.Lstat(filepath.Join(to, name)); err == nil {
			t.Errorf("%s was restored", name)
		}
	}

	write(t, filepath.Join(to, "models", "a.mim"), "changed")
	if _, err := ExtractFiles(archive, to, false); err == nil || !strings.Contains(err.Error(), "models/a.mim") {
		t.Errorf("restore over a file = %v, want it named", err)
	}
	if b, _ := os.ReadFile(filepath.Join(to, "models", "a.mim")); string(b) != "changed" {
		t.Error("a refused restore wrote anyway")
	}
	if _, err := ExtractFiles(archive, to, true); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(to, "models", "a.mim")); string(b) != "weights" {
		t.Error("replace left the file as it was")
	}
}

// An archive can't write outside the data folder or make links.
func TestExtractRefusesEscapes(t *testing.T) {
	for _, hdr := range []tar.Header{
		{Name: "../evil", Typeflag: tar.TypeReg, Mode: 0o600},
		{Name: "/etc/evil", Typeflag: tar.TypeReg, Mode: 0o600},
		{Name: `models\..\..\evil`, Typeflag: tar.TypeReg, Mode: 0o600},
		{Name: "models/link", Typeflag: tar.TypeSymlink, Linkname: "/etc"},
	} {
		archive := filepath.Join(t.TempDir(), FilesArchive)
		f, err := os.Create(archive)
		if err != nil {
			t.Fatal(err)
		}
		gz := gzip.NewWriter(f)
		tw := tar.NewWriter(gz)
		if err := tw.WriteHeader(&hdr); err != nil {
			t.Fatal(err)
		}
		tw.Close()
		gz.Close()
		f.Close()
		to := filepath.Join(t.TempDir(), "data")
		if _, err := ExtractFiles(archive, to, true); err == nil {
			t.Errorf("%q: extracted", hdr.Name)
		}
	}
}

func TestPruneKeepsNewestWhole(t *testing.T) {
	parent := t.TempDir()
	start := time.Date(2026, 10, 1, 3, 0, 0, 0, time.UTC)
	var dirs []string
	for i := range 4 {
		dir, err := NewDir(parent, start.Add(time.Duration(i)*24*time.Hour))
		if err != nil {
			t.Fatal(err)
		}
		if err := WriteManifest(dir, Manifest{Format: Format}); err != nil {
			t.Fatal(err)
		}
		dirs = append(dirs, dir)
	}
	// Not whole, and not ours: both stay.
	if _, err := NewDir(parent, start.Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(parent, "notes", ManifestName), "{}")

	removed, err := Prune(parent, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(removed) != 2 || removed[0] != dirs[0] || removed[1] != dirs[1] {
		t.Errorf("removed %v, want the two oldest", removed)
	}
	left, _ := os.ReadDir(parent)
	if len(left) != 4 {
		t.Errorf("%d entries left, want 4", len(left))
	}
}

func TestManifest(t *testing.T) {
	dir := t.TempDir()
	if _, err := ReadManifest(dir); err == nil {
		t.Error("read a manifest that isn't there")
	}
	m := Manifest{Format: Format, Created: time.Now().UTC().Round(0), Version: "v0.1.0", Schema: 6, KeyID: "abc", LogDatabase: true, Files: true}
	if err := WriteManifest(dir, m); err != nil {
		t.Fatal(err)
	}
	got, err := ReadManifest(dir)
	if err != nil || got != m {
		t.Errorf("ReadManifest = %+v, %v; want %+v", got, err, m)
	}
	newer := t.TempDir()
	write(t, filepath.Join(newer, ManifestName), `{"format": 2}`)
	if _, err := ReadManifest(newer); err == nil || !strings.Contains(err.Error(), "newer") {
		t.Errorf("newer format = %v", err)
	}
}
