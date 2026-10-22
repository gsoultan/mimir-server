// Package backup lays out a backup of a mimir server on disk and reads it
// back: a folder holding a pg_dump of the main database, one of the log
// database when the audit log has its own, an archive of the data folder
// and a manifest saying what made it. `mimir-server backup` writes one and
// `mimir-server restore` puts it back; the dumps themselves are made and
// loaded by package store.
//
// The secret key is never in a backup. The manifest records only an id of
// it (secret.KeyID), so a restore can tell the right key from the wrong one
// before it writes anything.
package backup

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

// The files in a backup folder.
const (
	ManifestName = "manifest.json"
	MainDump     = "main.dump"
	LogDump      = "log.dump"
	FilesArchive = "files.tar.gz"
)

// Format is the layout this build writes. A restore refuses a later one.
const Format = 1

// prefix starts the name of every backup folder NewDir makes.
const prefix = "mimir-"

// Manifest says what a backup holds and what made it.
type Manifest struct {
	Format  int       `json:"format"`
	Created time.Time `json:"created"`
	// Version is the server version that made it, Schema the newest
	// migration that version knows.
	Version string `json:"server_version"`
	Schema  int    `json:"schema_version"`
	// KeyID names the secret key the database's secrets are sealed with.
	KeyID string `json:"key_id"`
	// LogDatabase is true when the backup holds a separate log database.
	LogDatabase bool `json:"log_database"`
	// Files is true when the backup holds the data folder.
	Files bool `json:"files"`
}

// WriteManifest writes m into dir. It is written last, so a folder with a
// manifest holds a whole backup.
func WriteManifest(dir string, m Manifest) error {
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	return writeFile(filepath.Join(dir, ManifestName), append(b, '\n'))
}

// ReadManifest reads the manifest in dir.
func ReadManifest(dir string) (Manifest, error) {
	b, err := os.ReadFile(filepath.Join(dir, ManifestName))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Manifest{}, fmt.Errorf("backup: %s has no %s; it isn't a whole backup", dir, ManifestName)
		}
		return Manifest{}, fmt.Errorf("backup: %w", err)
	}
	var m Manifest
	if err := json.Unmarshal(b, &m); err != nil {
		return Manifest{}, fmt.Errorf("backup: %s: %w", ManifestName, err)
	}
	switch {
	case m.Format < 1:
		return Manifest{}, fmt.Errorf("backup: %s: no format", ManifestName)
	case m.Format > Format:
		return Manifest{}, fmt.Errorf("backup: made by a newer mimir-server (format %d); restore it with that version or later", m.Format)
	}
	return m, nil
}

// NewDir makes a new folder for a backup in parent, named for the time,
// such as mimir-20261022-101953, readable only by its owner.
func NewDir(parent string, now time.Time) (string, error) {
	if err := os.MkdirAll(parent, 0o700); err != nil {
		return "", fmt.Errorf("backup: %w", err)
	}
	dir := filepath.Join(parent, prefix+now.UTC().Format("20060102-150405"))
	if err := os.Mkdir(dir, 0o700); err != nil {
		return "", fmt.Errorf("backup: %w", err)
	}
	return dir, nil
}

// Prune removes all but the newest keep whole backups in parent, and
// returns the folders it removed. Only folders NewDir named that hold a
// manifest count, and nothing else in parent is touched. keep below 1
// removes nothing.
func Prune(parent string, keep int) ([]string, error) {
	if keep < 1 {
		return nil, nil
	}
	entries, err := os.ReadDir(parent)
	if err != nil {
		return nil, fmt.Errorf("backup: %w", err)
	}
	var whole []string
	for _, e := range entries {
		if !e.IsDir() || !strings.HasPrefix(e.Name(), prefix) {
			continue
		}
		if _, err := os.Stat(filepath.Join(parent, e.Name(), ManifestName)); err == nil {
			whole = append(whole, e.Name())
		}
	}
	// The names sort by time.
	slices.Sort(whole)
	var removed []string
	for len(whole) > keep {
		dir := filepath.Join(parent, whole[0])
		if err := os.RemoveAll(dir); err != nil {
			return removed, fmt.Errorf("backup: %w", err)
		}
		removed = append(removed, dir)
		whole = whole[1:]
	}
	return removed, nil
}

func writeFile(path string, b []byte) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("backup: %w", err)
	}
	_, werr := f.Write(b)
	serr := f.Sync()
	if err := errors.Join(werr, serr, f.Close()); err != nil {
		return fmt.Errorf("backup: write %s: %w", path, err)
	}
	return nil
}
