// Copyright (c) 2026 Gembit Soultan Shirazi
// SPDX-License-Identifier: PolyForm-Noncommercial-1.0.0 OR PolyForm-Small-Business-1.0.0 OR LicenseRef-Commercial
//
// This file is part of mimir-server, a server that trains, serves and manages mimir models,
// created and owned by Gembit Soultan Shirazi.
// It is free to use for personal and other noncommercial purposes, for
// nonprofits, and for companies with fewer than 100 people and under
// 1,000,000 USD in yearly revenue, under the PolyForm Noncommercial and
// PolyForm Small Business licenses. Any other use needs a commercial
// license from Gembit Soultan Shirazi. See the LICENSE and LICENSING.md files
// at the root of this repository.

// Package update keeps mimir-server up to date: it asks GitHub for the
// newest release, downloads the file for this machine, checks it against
// the release's SHA256SUMS, makes sure it runs here, and restarts the
// server on it.
//
// The new binary is kept in the data folder, under updates/, and never
// written over the installed one. The data folder is the one place the
// server can always write, whether it was installed from an archive, a
// Linux package or a container image, and the installed binary stays as
// it was. Each start, Launch looks in updates/ and switches to a newer
// version kept there. A newer installed binary, from a package upgrade or
// a new image, wins over an older kept one, which is then deleted.
//
// A kept version that fails to start three times in a row is set aside,
// and the installed binary runs again.
package update

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

const (
	dirName      = "updates"
	manifestName = "current.json"
	// maxTries is how many starts a kept version gets before it is set
	// aside, when it never says it started well.
	maxTries = 3
	// testEnv, set when the updater runs a new binary to see that it
	// works, keeps that run from switching to a kept version.
	testEnv = "MIMIR_UPDATE_TEST"
)

// manifest names the version kept in the updates folder.
type manifest struct {
	Version string `json:"version"`
	// File is the binary's name in the updates folder.
	File   string `json:"file"`
	SHA256 string `json:"sha256"`
	// From is the version it replaced.
	From string `json:"from,omitempty"`
	// Tries counts starts since it was kept that didn't reach Confirm.
	Tries int `json:"tries"`
	// OK says it started and served once, so it is kept for good.
	OK bool `json:"ok"`
}

// Dir is where updates are kept in the data folder.
func Dir(data string) string { return filepath.Join(data, dirName) }

func readManifest(dir string) (manifest, error) {
	var m manifest
	b, err := os.ReadFile(filepath.Join(dir, manifestName))
	if err != nil {
		return m, err
	}
	if err := json.Unmarshal(b, &m); err != nil {
		return m, err
	}
	// The file name comes from the folder, never a path elsewhere.
	if m.File == "" || filepath.Base(m.File) != m.File || m.File == manifestName {
		return m, errors.New("updates: the kept version names no file")
	}
	return m, nil
}

func writeManifest(dir string, m manifest) error {
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	tmp := filepath.Join(dir, manifestName+".tmp")
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(dir, manifestName))
}

// forget deletes the kept version and its manifest.
func forget(dir string, m manifest) {
	_ = os.Remove(filepath.Join(dir, manifestName))
	if m.File != "" {
		_ = os.Remove(filepath.Join(dir, m.File))
	}
}

func fileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// Launch returns the binary to run in place of this one, version
// current, with data as the data folder: a newer version kept there, or
// "" to carry on. It writes what it decides to w.
func Launch(data, current string, w io.Writer) string {
	if os.Getenv(testEnv) != "" || data == "" {
		return ""
	}
	dir := Dir(data)
	m, err := readManifest(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return ""
	}
	if err != nil {
		fmt.Fprintf(w, "mimir-server: setting aside the kept update, which can't be read: %v\n", err)
		forget(dir, m)
		return ""
	}
	if !Newer(m.Version, current) {
		if Newer(current, m.Version) {
			// The installed binary was upgraded past it.
			forget(dir, m)
		}
		return ""
	}
	path := filepath.Join(dir, m.File)
	if !m.OK && m.Tries >= maxTries {
		fmt.Fprintf(w, "mimir-server: version %s didn't start after %d tries; running version %s instead\n", m.Version, m.Tries, current)
		forget(dir, m)
		return ""
	}
	if sum, err := fileSHA256(path); err != nil || !strings.EqualFold(sum, m.SHA256) {
		fmt.Fprintf(w, "mimir-server: setting aside version %s, whose file is missing or changed\n", m.Version)
		forget(dir, m)
		return ""
	}
	if !m.OK {
		m.Tries++
		if err := writeManifest(dir, m); err != nil {
			fmt.Fprintf(w, "mimir-server: not switching to version %s: %v\n", m.Version, err)
			return ""
		}
	}
	return path
}

// Confirm records that version current started and serves, so a kept
// version is kept for good. It returns the version it replaced when this
// is the first start after an update, else "".
func Confirm(data, current string) (from string, err error) {
	dir := Dir(data)
	m, err := readManifest(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return "", nil
	}
	if err != nil || m.Version != strings.TrimPrefix(current, "v") || m.OK {
		return "", err
	}
	m.OK, m.Tries = true, 0
	if err := writeManifest(dir, m); err != nil {
		return "", err
	}
	// Older kept versions are no longer needed.
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if n := e.Name(); n != m.File && n != manifestName && strings.HasPrefix(n, binaryPrefix) {
			_ = os.Remove(filepath.Join(dir, n))
		}
	}
	return m.From, nil
}

// DataDir finds the -data flag in a command line, as the server and its
// commands take it, or the default, ./data.
func DataDir(args []string) string {
	for i, a := range args {
		if a == "--" {
			break
		}
		name, value, hasValue := strings.Cut(strings.TrimLeft(a, "-"), "=")
		if !strings.HasPrefix(a, "-") || name != "data" {
			continue
		}
		if hasValue {
			return value
		}
		if i+1 < len(args) {
			return args[i+1]
		}
	}
	return "./data"
}
