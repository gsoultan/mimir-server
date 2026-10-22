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
