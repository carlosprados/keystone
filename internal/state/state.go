package state

import (
    "encoding/json"
    "os"
    "path/filepath"
    "time"

    "github.com/carlosprados/keystone/internal/store"
)

type PlanStatus struct {
    Path    string    `json:"path"`
    Status  string    `json:"status"`
    Error   string    `json:"error"`
    Updated time.Time `json:"updated"`
}

type Snapshot struct {
    Plan       PlanStatus            `json:"plan"`
    Components []store.ComponentInfo `json:"components"`
    PlanComponents []PlanComponent    `json:"plan_components"`
}

// PlanComponent persists mapping from component name to recipe and deps.
type PlanComponent struct {
    Name          string   `json:"name"`
    RecipePath    string   `json:"recipe_path"`
    RecipeMeta    string   `json:"recipe_meta"`
    Deps          []string `json:"deps"`
}

func Save(dir string, snap Snapshot) error {
    if err := os.MkdirAll(dir, 0o755); err != nil { return err }
    path := filepath.Join(dir, "snapshot.json")
    tmp := path + ".tmp"
    b, err := json.MarshalIndent(snap, "", "  ")
    if err != nil { return err }
    if err := os.WriteFile(tmp, b, 0o644); err != nil { return err }
    return os.Rename(tmp, path)
}

func Load(dir string) (Snapshot, error) {
    var snap Snapshot
    path := filepath.Join(dir, "snapshot.json")
    b, err := os.ReadFile(path)
    if err != nil { return snap, err }
    if err := json.Unmarshal(b, &snap); err != nil { return snap, err }
    return snap, nil
}
