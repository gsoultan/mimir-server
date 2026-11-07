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

//go:build !unix && !windows

package mapfile

import (
	"io"
	"os"
)

// mmap reads the file into memory on systems without a memory mapping.
func mmap(fd *os.File, size int) (*File, error) {
	data := make([]byte, size)
	if _, err := io.ReadFull(fd, data); err != nil {
		return nil, err
	}
	return &File{data: data, unmap: func() error { return nil }}, nil
}
