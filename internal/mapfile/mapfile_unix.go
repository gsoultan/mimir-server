// Copyright (c) 2026 Gembit Soultan Shirazi
//
// This file is part of mimir-server, a server that trains, serves and manages mimir models,
// created and owned by Gembit Soultan Shirazi.

//go:build unix

package mapfile

import (
	"os"
	"syscall"
)

func mmap(fd *os.File, size int) (*File, error) {
	data, err := syscall.Mmap(int(fd.Fd()), 0, size, syscall.PROT_READ, syscall.MAP_SHARED)
	if err != nil {
		return nil, os.NewSyscallError("mmap", err)
	}
	return &File{data: data, unmap: func() error { return syscall.Munmap(data) }}, nil
}
