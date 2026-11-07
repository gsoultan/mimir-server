// Copyright (c) 2026 Gembit Soultan Shirazi
// SPDX-License-Identifier: AGPL-3.0-only OR LicenseRef-Commercial
//
// This file is part of mimir-server, a server that trains, serves and manages mimir models,
// created and owned by Gembit Soultan Shirazi.
// It is dual-licensed: you may use it under the GNU Affero General Public
// License, version 3, as published by the Free Software Foundation, or
// under a commercial license from Gembit Soultan Shirazi. See the LICENSE and
// LICENSING.md files at the root of this repository.
//
// This program is distributed in the hope that it will be useful, but
// WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the GNU Affero
// General Public License for more details.

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
