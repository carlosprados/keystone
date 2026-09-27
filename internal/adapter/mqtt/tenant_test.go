package mqtt

import (
	"context"
	"reflect"
	"strings"
	"testing"
)

func TestEveryTopicIsUnderTheTenant(t *testing.T) {
	tp := NewTopics("acme", "pi-1")
	v := reflect.ValueOf(*tp)
	for i := 0; i < v.NumField(); i++ {
		f := v.Type().Field(i)
		if f.Type.Kind() != reflect.String || !f.IsExported() {
			continue
		}
		if topic := v.Field(i).String(); !strings.HasPrefix(topic, "keystone/acme/pi-1/") {
			t.Errorf("%s = %q, want it under keystone/acme/pi-1/", f.Name, topic)
		}
	}
	if tp.CmdWildcard != "keystone/acme/pi-1/cmd/+" || tp.Status != "keystone/acme/pi-1/status" {
		t.Errorf("command subscription %q, status %q", tp.CmdWildcard, tp.Status)
	}
}

// The cases are the control plane's own (its internal/ident tests), copied
// verbatim: the two sides must accept and refuse exactly the same names, or
// a device the agent starts with is one whose messages the control plane
// drops without a word.
func TestIdentityRulesMatchTheControlPlane(t *testing.T) {
	a63, a64 := strings.Repeat("a", 63), strings.Repeat("a", 64)
	x128, x129 := strings.Repeat("x", 128), strings.Repeat("x", 129)

	for _, ok := range []string{"lab", "acme", "a", "0", "a-b", "a1-2b", a63} {
		if err := ValidateTenant(ok); err != nil {
			t.Errorf("tenant %q refused: %v", ok, err)
		}
	}
	for _, bad := range []string{"", "Lab", "ACME", "a.b", "a_b", "-a", "a-", "a/b", "a+b", "a#b", "a b", "a\x00b", a64} {
		if ValidateTenant(bad) == nil {
			t.Errorf("tenant %q accepted", bad)
		}
	}
	for _, ok := range []string{"pi", "lab-pi-edge", "lab.pi_1", "A.b-C_9", "events", "status", "...", x128} {
		if err := ValidateDeviceID(ok); err != nil {
			t.Errorf("device ID %q refused: %v", ok, err)
		}
	}
	for _, bad := range []string{"", ".", "..", "cmd", "resp", "a/b", "a+b", "a#b", "a b", "a\x00b", "é", x129} {
		if ValidateDeviceID(bad) == nil {
			t.Errorf("device ID %q accepted", bad)
		}
	}
}

// TestTheAdapterRefusesToStartWithoutATenant: main checks this too, but the
// adapter must not be usable around it, publishing under keystone//<device>/.
func TestTheAdapterRefusesToStartWithoutATenant(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Broker = "tcp://127.0.0.1:1"
	cfg.DeviceID = "pi-1"
	if err := New(cfg, plainHandler{}).Start(context.Background()); err == nil {
		t.Fatal("started with no tenant")
	}
}
