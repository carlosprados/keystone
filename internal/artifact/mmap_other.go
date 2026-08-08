//go:build !linux

package artifact

import "os"

// mapFile falls back to reading the file onto the heap where mmap is not
// available. Keystone ships Linux binaries; this keeps the package building
// and testable elsewhere, at the memory cost the Linux path exists to avoid.
func mapFile(path string) (data []byte, release func(), err error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, err
	}
	return b, func() {}, nil
}
