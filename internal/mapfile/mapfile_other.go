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
