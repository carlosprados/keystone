//go:build linux

package artifact

import (
	"fmt"
	"os"
	"syscall"
)

// mapFile maps path read-only and returns the bytes plus a release function.
//
// This exists for one reason: bsdiff needs the whole base artifact addressable
// as a byte slice, and holding it on the Go heap doubles the memory an update
// costs on a device that has little. Mapped pages are the kernel's page cache,
// so they are backed by the file, shared with any other reader, and evictable
// under pressure instead of being charged to the process's heap.
func mapFile(path string) (data []byte, release func(), err error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, nil, err
	}
	defer f.Close()

	fi, err := f.Stat()
	if err != nil {
		return nil, nil, err
	}
	size := fi.Size()
	if size <= 0 {
		return nil, nil, fmt.Errorf("cannot map %s: empty file", path)
	}
	if size != int64(int(size)) {
		return nil, nil, fmt.Errorf("cannot map %s: %d bytes exceeds this platform's address space", path, size)
	}

	b, err := syscall.Mmap(int(f.Fd()), 0, int(size), syscall.PROT_READ, syscall.MAP_SHARED)
	if err != nil {
		return nil, nil, fmt.Errorf("mmap %s: %w", path, err)
	}
	return b, func() { _ = syscall.Munmap(b) }, nil
}
