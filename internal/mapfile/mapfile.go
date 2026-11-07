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

// Package mapfile maps a file read-only into memory, so a large model can
// be read in place: the pages come from the file as they are touched and
// stay out of the Go heap, and the system can drop them again whenever it
// needs the memory.
package mapfile

import (
	"errors"
	"os"
)

// File is a read-only view of a file's bytes.
type File struct {
	data  []byte
	unmap func() error
}

// Bytes returns the file's contents. They must not be written to or read
// after Close.
func (f *File) Bytes() []byte { return f.data }

// Close releases the mapping.
func (f *File) Close() error {
	if f.unmap == nil {
		return nil
	}
	err := f.unmap()
	f.data, f.unmap = nil, nil
	return err
}

// Open maps the file at path.
func Open(path string) (*File, error) {
	fd, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer fd.Close()
	st, err := fd.Stat()
	if err != nil {
		return nil, err
	}
	size := st.Size()
	if size == 0 {
		return &File{}, nil
	}
	if int64(int(size)) != size {
		return nil, errors.New("mapfile: file too large to map")
	}
	return mmap(fd, int(size))
}
