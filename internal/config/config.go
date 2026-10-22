// Package config reads and writes the console's configuration file: the
// databases it keeps its records in and where its secret key lives.
// First-run setup writes it; flags and environment variables still come
// first when they are set.
//
// The file holds credentials, so it is written atomically and readable only
// by its owner, and Read refuses one that other users can read. The
// database URLs in it may also be kept out of it altogether: a value of
// "env:NAME" is read from the environment variable NAME and "file:PATH"
// from the file at PATH. Setup seals the URLs it writes with the secret
// key (see package secret), so a copy of the file alone gives no password
// away.
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/gsoultan/mimir-server/internal/secret"
)

// Env is the variable that may name the file.
const Env = "MIMIR_CONFIG"

// DefaultName is the file's name in the data folder.
const DefaultName = "mimir.json"

// ErrExists means Write found a file already there and left it alone.
var ErrExists = errors.New("config: a configuration file is already there")

// File is the configuration file's content.
type File struct {
	// DatabaseURL is the PostgreSQL database for the server's records.
	DatabaseURL string `json:"database_url"`
	// LogDatabaseURL is a second PostgreSQL database for the audit log;
	// empty keeps the log in the main one.
	LogDatabaseURL string `json:"log_database_url,omitempty"`
	// SecretKeyFile is the file holding the secret key; empty means the
	// data folder's secret.key.
	SecretKeyFile string `json:"secret_key_file,omitempty"`
}

// Path returns the file's path: explicit when set, else <dataDir>/mimir.json.
func Path(explicit, dataDir string) string {
	if explicit != "" {
		return explicit
	}
	return filepath.Join(dataDir, DefaultName)
}

// Read reads the file at path. A missing file gives an error matching
// fs.ErrNotExist. On Unix a file that its group or others may read or write
// is refused, the way ssh refuses a private key anyone can read.
func Read(path string) (File, error) {
	info, err := os.Stat(path)
	if err != nil {
		return File{}, err
	}
	if !info.Mode().IsRegular() {
		return File{}, fmt.Errorf("config: %s is not a regular file", path)
	}
	if perm := info.Mode().Perm(); runtime.GOOS != "windows" && perm&0o077 != 0 {
		return File{}, fmt.Errorf("config: %s holds database passwords but other users can open it (mode %04o); run chmod 600 %s", path, perm, path)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return File{}, err
	}
	var f File
	dec := json.NewDecoder(strings.NewReader(string(b)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&f); err != nil {
		return File{}, fmt.Errorf("config: %s: %w", path, err)
	}
	return f, nil
}

// Write saves f to path atomically, readable only by its owner: it is
// written to a temporary file in the same folder, synced, then linked into
// place, so a crash leaves either no file or the whole one. It never
// replaces a file already there, and returns ErrExists instead.
func Write(path string, f File) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("config: %w", err)
	}
	b, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".*")
	if err != nil {
		return fmt.Errorf("config: %w", err)
	}
	name := tmp.Name()
	defer os.Remove(name)
	// CreateTemp makes the file 0600; say so anyway, in case of an odd umask
	// or filesystem.
	cerr := tmp.Chmod(0o600)
	_, werr := tmp.Write(append(b, '\n'))
	serr := tmp.Sync()
	if err := errors.Join(cerr, werr, serr, tmp.Close()); err != nil {
		return fmt.Errorf("config: write %s: %w", name, err)
	}
	// A hard link fails when the target exists, which rename would not.
	if err := os.Link(name, path); err != nil {
		if errors.Is(err, fs.ErrExist) {
			return ErrExists
		}
		// No hard links here (some network and FUSE filesystems): check, then
		// rename, which is still atomic but could race another writer.
		if _, serr := os.Lstat(path); serr == nil {
			return ErrExists
		}
		if err := os.Rename(name, path); err != nil {
			return fmt.Errorf("config: %w", err)
		}
	}
	syncDir(dir)
	return nil
}

// syncDir makes a new directory entry durable where the platform allows.
func syncDir(dir string) {
	if d, err := os.Open(dir); err == nil {
		d.Sync()
		d.Close()
	}
}

// Seal returns f with its database URLs sealed by box, leaving empty values
// and env:/file: references as they are.
func (f File) Seal(box *secret.Box) (File, error) {
	var err error
	if f.DatabaseURL, err = seal(box, f.DatabaseURL, "database_url"); err != nil {
		return File{}, err
	}
	if f.LogDatabaseURL, err = seal(box, f.LogDatabaseURL, "log_database_url"); err != nil {
		return File{}, err
	}
	return f, nil
}

// Open returns f with its database URLs resolved: env: and file:
// references read, sealed values opened with box.
func (f File) Open(box *secret.Box) (File, error) {
	var err error
	if f.DatabaseURL, err = Resolve(box, f.DatabaseURL, "database_url"); err != nil {
		return File{}, err
	}
	if f.LogDatabaseURL, err = Resolve(box, f.LogDatabaseURL, "log_database_url"); err != nil {
		return File{}, err
	}
	return f, nil
}

func seal(box *secret.Box, v, field string) (string, error) {
	if v == "" || secret.Sealed(v) || isRef(v) {
		return v, nil
	}
	return box.Seal(v, sealContext(field))
}

func isRef(v string) bool { return strings.HasPrefix(v, "env:") || strings.HasPrefix(v, "file:") }

// sealContext binds a sealed value to its field, so one can't be moved to
// another.
func sealContext(field string) string { return "config:" + field }

// Resolve returns the value v stands for: the variable an env:NAME names,
// the trimmed content of the file a file:PATH names, v opened with box when
// it is sealed, or v itself.
func Resolve(box *secret.Box, v, field string) (string, error) {
	switch {
	case strings.HasPrefix(v, "env:"):
		name := strings.TrimPrefix(v, "env:")
		out, ok := os.LookupEnv(name)
		if !ok || strings.TrimSpace(out) == "" {
			return "", fmt.Errorf("config: %s names $%s, which is not set", field, name)
		}
		return strings.TrimSpace(out), nil
	case strings.HasPrefix(v, "file:"):
		b, err := os.ReadFile(strings.TrimPrefix(v, "file:"))
		if err != nil {
			return "", fmt.Errorf("config: %s: %w", field, err)
		}
		return strings.TrimSpace(string(b)), nil
	case secret.Sealed(v):
		if box == nil {
			return "", fmt.Errorf("config: %s is sealed, and there is no secret key to open it", field)
		}
		out, err := box.Open(v, sealContext(field))
		if err != nil {
			return "", fmt.Errorf("config: %s: %w", field, err)
		}
		return out, nil
	}
	return v, nil
}
