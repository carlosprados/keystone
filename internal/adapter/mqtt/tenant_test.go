package mqtt

import (
	"reflect"
	"strings"
	"testing"
)

// TestNoTenantKeepsEveryTopic is the compatibility contract: an agent with no
// tenant must use exactly the topics it always did. A changed default would
// break the broker's ACL and the command channel at once, and a denied publish
// under MQTT 3.1.1 is dropped silently — the device would just go quiet.
func TestNoTenantKeepsEveryTopic(t *testing.T) {
	tp := NewTopics("", "pi-1")
	v := reflect.ValueOf(*tp)
	for i := 0; i < v.NumField(); i++ {
		f := v.Type().Field(i)
		if f.Type.Kind() != reflect.String || !f.IsExported() {
			continue
		}
		topic := v.Field(i).String()
		if !strings.HasPrefix(topic, "keystone/pi-1/") {
			t.Errorf("%s = %q, want it under keystone/pi-1/", f.Name, topic)
		}
	}
	if tp.Status != "keystone/pi-1/status" || tp.CmdSelfUpdate != "keystone/pi-1/cmd/self-update" {
		t.Errorf("tenant-less topics moved: status %q, self-update %q", tp.Status, tp.CmdSelfUpdate)
	}
}

func TestATenantAddsOneLevelToEveryTopic(t *testing.T) {
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
	if tp.CmdWildcard != "keystone/acme/pi-1/cmd/+" {
		t.Errorf("command subscription = %q", tp.CmdWildcard)
	}
}

// TestTenantAndDeviceCannotBecomeWildcards: "+" or "#" would make the device's
// own subscription cover other devices' commands.
func TestTenantAndDeviceCannotBecomeWildcards(t *testing.T) {
	for _, bad := range []string{"a/b", "+", "#", "acme#", " acme", ""} {
		if ValidateTenant(bad) == nil {
			t.Errorf("tenant %q accepted", bad)
		}
	}
	for _, bad := range []string{"+", "pi#1", "#", ""} {
		if ValidateDeviceID(bad) == nil {
			t.Errorf("device ID %q accepted", bad)
		}
	}
	if err := ValidateTenant("acme"); err != nil {
		t.Errorf("a plain tenant was refused: %v", err)
	}
	if err := ValidateDeviceID("site-3/pi-1"); err != nil {
		t.Errorf("a device ID with '/', which installs already use, was refused: %v", err)
	}
}
