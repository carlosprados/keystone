//go:build interop

// This file is the drift alarm between Keystone and the delta servers it talks
// to. Keystone deliberately does NOT depend on ota-updater to apply patches —
// an edge agent should not carry a server's module graph — so the two hold
// independent implementations of the same wire format. Independent
// implementations drift, and the failure would land on a device in the field.
//
// So: this test, and only this test, imports ota-updater's own pkg/delta,
// generates a patch exactly as its server would, and asserts that Keystone's
// decoder reconstructs the target. It is behind a build tag because it is the
// one place that dependency exists.
//
//	go test -tags interop ./internal/artifact/
//
// If it fails, the format changed. That is the alarm working: check what
// ota-updater now emits before shipping an agent that assumes otherwise.
package artifact

import (
	"math/rand"
	"testing"

	otadelta "github.com/carlosprados/ota-updater/pkg/delta"
)

func TestInteropWithOTAUpdater(t *testing.T) {
	rng := rand.New(rand.NewSource(11))
	base := make([]byte, 512<<10)
	if _, err := rng.Read(base); err != nil {
		t.Fatalf("rng: %v", err)
	}
	target := mutate(base)

	// Produced by the server's own code path, not by a local reimplementation.
	patch, err := otadelta.Generate(base, target)
	if err != nil {
		t.Fatalf("ota-updater delta.Generate: %v", err)
	}

	got, err := ApplyDelta(DeltaFormatBsdiffZstd, base, patch, SHA256Bytes(target))
	if err != nil {
		t.Fatalf("Keystone could not apply a patch produced by ota-updater: %v", err)
	}
	if string(got) != string(target) {
		t.Fatal("reconstructed bytes differ from the target")
	}

	// The reverse direction matters too: a patch Keystone accepts must be one
	// ota-updater would also accept, or the two disagree about the format
	// while both appearing to work.
	round, err := otadelta.Apply(base, patch)
	if err != nil {
		t.Fatalf("ota-updater delta.Apply: %v", err)
	}
	if SHA256Bytes(round) != SHA256Bytes(target) {
		t.Fatal("ota-updater's own round trip does not match the target")
	}
}
