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
