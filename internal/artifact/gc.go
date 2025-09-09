package artifact

import (
	"os"
	"path/filepath"
)

// GC removes artifact subdirectories under root that are not present in `keep`.
// The `keep` set should contain relative paths like "<name>/<version>".
func GC(root string, keep map[string]struct{}) error {
	// If root doesn't exist, nothing to do
	if _, err := os.Stat(root); os.IsNotExist(err) {
		return nil
	}
	return filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if !info.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return nil
		}
		// Expect structure root/<name>/<version>/...
		// filepath.SplitList isn't right for POSIX; simpler: use separator
		// fallback: treat rel with path separators
		// Keep only top-level name/version directories
		// We'll approximate: if rel has two elements when split by filepath.Separator
		segs := splitPath(rel)
		if len(segs) == 2 {
			key := filepath.ToSlash(rel)
			if _, ok := keep[key]; !ok {
				// remove directory tree
				_ = os.RemoveAll(path)
				return filepath.SkipDir
			}
		}
		return nil
	})
}

func splitPath(p string) []string {
	var segs []string
	for p != "." && p != string(filepath.Separator) && p != "" {
		dir, file := filepath.Split(p)
		if file != "" {
			segs = append([]string{file}, segs...)
		}
		p = filepath.Clean(dir)
		if p == "." || p == string(filepath.Separator) || p == "" {
			break
		}
		// Avoid infinite loop
		if len(dir) == 0 {
			break
		}
		if dir == p {
			break
		}
	}
	return segs
}
