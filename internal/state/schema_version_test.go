package state

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// TestSaveStampsSchemaVersion: the version is written by Save rather than
// trusted from the caller, so a snapshot cannot claim a format it was not
// written by.
func TestSaveStampsSchemaVersion(t *testing.T) {
	dir := t.TempDir()

	// Deliberately lying about the version on the way in.
	if err := Save(dir, Snapshot{SchemaVersion: 99}); err != nil {
		t.Fatalf("save: %v", err)
	}

	b, err := os.ReadFile(filepath.Join(dir, "snapshot.json"))
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	var raw struct {
		SchemaVersion int `json:"schema_version"`
	}
	if err := json.Unmarshal(b, &raw); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if raw.SchemaVersion != CurrentSchemaVersion {
		t.Errorf("schema_version = %d, want %d", raw.SchemaVersion, CurrentSchemaVersion)
	}
}

// TestLoadRefusesNewerSnapshot is the case this exists for: an update ran,
// wrote state in a shape this build does not know, and was rolled back. Reading
// it anyway would mean guessing at which plan is in force, which components to
// adopt, and what each dataset last published — the value the anti-replay rule
// compares against.
func TestLoadRefusesNewerSnapshot(t *testing.T) {
	dir := t.TempDir()

	newer := map[string]any{"schema_version": CurrentSchemaVersion + 1}
	b, _ := json.Marshal(newer)
	if err := os.WriteFile(filepath.Join(dir, "snapshot.json"), b, 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	snap, err := Load(dir)
	if !errors.Is(err, ErrSnapshotFromNewerAgent) {
		t.Fatalf("error = %v, want ErrSnapshotFromNewerAgent", err)
	}
	if snap.Plan.Path != "" || len(snap.Components) != 0 {
		t.Error("a refused snapshot must come back empty, not half-read")
	}
}

// TestLoadAcceptsUnversionedSnapshot: snapshots written before versioning
// existed carry no field at all. The format did not change, it only became
// explicit, so refusing them would wipe the state of every device on upgrade.
func TestLoadAcceptsUnversionedSnapshot(t *testing.T) {
	dir := t.TempDir()

	old := `{"plan":{"path":"runtime/plans/applied.toml","status":"running"},"components":[]}`
	if err := os.WriteFile(filepath.Join(dir, "snapshot.json"), []byte(old), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	snap, err := Load(dir)
	if err != nil {
		t.Fatalf("an unversioned snapshot was refused: %v", err)
	}
	if snap.Plan.Path != "runtime/plans/applied.toml" {
		t.Errorf("plan path = %q, want it preserved", snap.Plan.Path)
	}
}

// TestRoundTrip: the ordinary path must keep working.
func TestSchemaVersionRoundTrip(t *testing.T) {
	dir := t.TempDir()

	if err := Save(dir, Snapshot{Plan: PlanStatus{Path: "p.toml", Status: "running"}}); err != nil {
		t.Fatalf("save: %v", err)
	}
	snap, err := Load(dir)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if snap.SchemaVersion != CurrentSchemaVersion || snap.Plan.Path != "p.toml" {
		t.Errorf("round trip lost something: %+v", snap)
	}
}
