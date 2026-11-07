// Copyright (c) 2026 Gembit Soultan Shirazi
//
// This file is part of mimir-server, a server that trains, serves and manages mimir models,
// created and owned by Gembit Soultan Shirazi.

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
