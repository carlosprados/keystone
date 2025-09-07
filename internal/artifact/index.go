package artifact

import (
    "encoding/json"
    "os"
    "path/filepath"
    "sort"
    "sync"
    "time"
)

// IndexEntry tracks a downloaded artifact.
type IndexEntry struct {
    URI      string    `json:"uri"`
    SHA256   string    `json:"sha256"`
    Path     string    `json:"path"`
    Size     int64     `json:"size"`
    Updated  time.Time `json:"updated"`
}

// Index is a simple on-disk artifact index stored as JSON.
type Index struct {
    mu   sync.Mutex
    Root string
    File string
    M    map[string]IndexEntry // key: uri
}

func LoadIndex(root string) (*Index, error) {
    idx := &Index{Root: root, File: filepath.Join(root, "index.json"), M: map[string]IndexEntry{}}
    b, err := os.ReadFile(idx.File)
    if err != nil {
        return idx, nil // treat as empty
    }
    _ = json.Unmarshal(b, &idx.M)
    return idx, nil
}

func (i *Index) Save() error {
    i.mu.Lock()
    defer i.mu.Unlock()
    if err := os.MkdirAll(i.Root, 0o755); err != nil { return err }
    b, err := json.MarshalIndent(i.M, "", "  ")
    if err != nil { return err }
    tmp := i.File + ".tmp"
    if err := os.WriteFile(tmp, b, 0o644); err != nil { return err }
    return os.Rename(tmp, i.File)
}

func (i *Index) Get(uri string) (IndexEntry, bool) {
    i.mu.Lock(); defer i.mu.Unlock()
    e, ok := i.M[uri]
    return e, ok
}

func (i *Index) Put(e IndexEntry) {
    i.mu.Lock()
    e.Updated = time.Now()
    i.M[e.URI] = e
    i.mu.Unlock()
}

// EnforceCacheLimit trims artifacts by LRU (oldest Updated first) to keep total size <= maxBytes.
// It deletes files on disk and removes their index entries.
func EnforceCacheLimit(root string, maxBytes int64) error {
    if maxBytes <= 0 { return nil }
    idx, _ := LoadIndex(root)
    // Gather existing entries and total size
    type pair struct{ key string; e IndexEntry }
    var entries []pair
    var total int64
    for k, e := range idx.M {
        if st, err := os.Stat(e.Path); err == nil {
            // refresh size if changed
            if e.Size != st.Size() {
                e.Size = st.Size()
                idx.M[k] = e
            }
            total += e.Size
            entries = append(entries, pair{key: k, e: e})
        } else {
            // remove stale index entry
            delete(idx.M, k)
        }
    }
    if total <= maxBytes {
        _ = idx.Save()
        return nil
    }
    sort.Slice(entries, func(i, j int) bool { return entries[i].e.Updated.Before(entries[j].e.Updated) })
    for _, p := range entries {
        if total <= maxBytes { break }
        _ = os.Remove(p.e.Path)
        delete(idx.M, p.key)
        total -= p.e.Size
    }
    return idx.Save()
}
