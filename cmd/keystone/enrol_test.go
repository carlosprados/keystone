package main

import (
	"os"
	"path/filepath"
	"testing"
)

func enrolDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	gen := filepath.Join(dir, "gen-1")
	if err := os.Mkdir(gen, 0o700); err != nil {
		t.Fatal(err)
	}
	meta := `{"version":1,"enrolUrl":"https://e.example","caPin":"sha256:00","tenant":"acme","device":"pi-1"}`
	if err := os.WriteFile(filepath.Join(gen, "enrolment.json"), []byte(meta), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("gen-1", filepath.Join(dir, "current")); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestEnrolmentFillsTheMQTTIdentity(t *testing.T) {
	dir := enrolDir(t)
	var tenant, device, cert, key, ca string
	if _, err := applyEnrolment(dir, mqttIdentity{&tenant, &device, &cert, &key, &ca}); err != nil {
		t.Fatal(err)
	}
	if tenant != "acme" || device != "pi-1" {
		t.Errorf("identity %s/%s, want acme/pi-1", tenant, device)
	}
	if cert != filepath.Join(dir, "current", "device.crt") || key != filepath.Join(dir, "current", "device.key") || ca != filepath.Join(dir, "current", "broker-ca.pem") {
		t.Errorf("TLS paths not taken through current/: %s %s %s", cert, key, ca)
	}
}

// TestEnrolmentRefusesAConflictingSetting: a device ID that disagrees with the
// certificate would put the device under topics its certificate does not
// cover, and the broker would drop its publishes silently.
func TestEnrolmentRefusesAConflictingSetting(t *testing.T) {
	dir := enrolDir(t)
	for name, id := range map[string]func() mqttIdentity{
		"device ID": func() mqttIdentity {
			d := "pi-2"
			return mqttIdentity{new(string), &d, new(string), new(string), new(string)}
		},
		"tenant": func() mqttIdentity {
			tn := "other"
			return mqttIdentity{&tn, new(string), new(string), new(string), new(string)}
		},
		"TLS cert": func() mqttIdentity {
			c := "/etc/x.pem"
			return mqttIdentity{new(string), new(string), &c, new(string), new(string)}
		},
	} {
		if _, err := applyEnrolment(dir, id()); err == nil {
			t.Errorf("a conflicting %s was accepted", name)
		}
	}
	same := "pi-1"
	if _, err := applyEnrolment(dir, mqttIdentity{new(string), &same, new(string), new(string), new(string)}); err != nil {
		t.Errorf("a device ID that matches was refused: %v", err)
	}
	if _, err := applyEnrolment(t.TempDir(), mqttIdentity{new(string), new(string), new(string), new(string), new(string)}); err == nil {
		t.Error("an empty directory was accepted as an enrolment")
	}
}
