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

//go:build windows

package mapfile

import (
	"os"
	"syscall"
	"unsafe"
)

// mmap maps the file as mimir-model's mim.Open does on Windows. The file
// stays in use, and cannot be replaced or deleted, until Close.
func mmap(fd *os.File, size int) (*File, error) {
	m, err := syscall.CreateFileMapping(syscall.Handle(fd.Fd()), nil, syscall.PAGE_READONLY, uint32(uint64(size)>>32), uint32(size), nil)
	if err != nil {
		return nil, os.NewSyscallError("CreateFileMapping", err)
	}
	addr, err := syscall.MapViewOfFile(m, syscall.FILE_MAP_READ, 0, 0, uintptr(size))
	syscall.CloseHandle(m)
	if err != nil {
		return nil, os.NewSyscallError("MapViewOfFile", err)
	}
	data := unsafe.Slice(*(**byte)(unsafe.Pointer(&addr)), size)
	return &File{data: data, unmap: func() error { return os.NewSyscallError("UnmapViewOfFile", syscall.UnmapViewOfFile(addr)) }}, nil
}
