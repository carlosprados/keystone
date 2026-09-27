//go:build mqttlive

package mqtt

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// TestReloadIdentityLive runs against a real broker that requires client
// certificates and logs the identity each client connects with:
//
//	KEYSTONE_LIVE_BROKER=ssl://127.0.0.1:18883 KEYSTONE_LIVE_ID=<dir> \
//	  go test -tags mqttlive -run TestReloadIdentityLive ./internal/adapter/mqtt/
//
// <dir> holds gen-a/ and gen-b/ (device.crt, device.key, broker-ca.pem) and a
// "current" link to gen-a, laid out as an enrolment store. The broker log is
// the evidence: it must show the second identity connecting without the
// adapter being restarted.
func TestReloadIdentityLive(t *testing.T) {
	broker, dir := os.Getenv("KEYSTONE_LIVE_BROKER"), os.Getenv("KEYSTONE_LIVE_ID")
	if broker == "" || dir == "" {
		t.Skip("KEYSTONE_LIVE_BROKER and KEYSTONE_LIVE_ID not set")
	}
	cur := filepath.Join(dir, "current")
	cfg := DefaultConfig()
	cfg.Broker = broker
	cfg.DeviceID = "pi-live"
	cfg.Tenant = "acme"
	cfg.TLSCert = filepath.Join(cur, "device.crt")
	cfg.TLSKey = filepath.Join(cur, "device.key")
	cfg.TLSCA = filepath.Join(cur, "broker-ca.pem")
	cfg.PublishStateInterval = 0
	cfg.PublishHealthInterval = 0
	cfg.LWTEnabled = false

	a := New(cfg, plainHandler{})
	if err := a.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = a.Stop(context.Background()) })
	waitConnected(t, a, "with identity A")

	if err := os.Symlink("gen-b", cur+".tmp"); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(cur+".tmp", cur); err != nil {
		t.Fatal(err)
	}
	t.Log("current -> gen-b; reloading")
	a.ReloadIdentity()
	time.Sleep(500 * time.Millisecond)
	waitConnected(t, a, "after ReloadIdentity")
	t.Log("reconnected; the broker log must show identity B now")
}

func waitConnected(t *testing.T, a *Adapter, what string) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		// Not Connected(): paho's IsConnected is also true while a connection
		// is still being attempted, which would let this pass on nothing.
		a.mu.RLock()
		open := a.client != nil && a.client.IsConnectionOpen()
		a.mu.RUnlock()
		if open {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("not connected %s", what)
}

// TestAutomaticReconnectReadsTheNewIdentityLive: with no ReloadIdentity at all,
// a reconnect paho makes by itself after the broker goes away must also use
// the files as they are now. KEYSTONE_LIVE_CONTAINER names the broker's
// container, which the test restarts.
func TestAutomaticReconnectReadsTheNewIdentityLive(t *testing.T) {
	broker, dir, ctr := os.Getenv("KEYSTONE_LIVE_BROKER"), os.Getenv("KEYSTONE_LIVE_ID"), os.Getenv("KEYSTONE_LIVE_CONTAINER")
	if broker == "" || dir == "" || ctr == "" {
		t.Skip("KEYSTONE_LIVE_BROKER, KEYSTONE_LIVE_ID and KEYSTONE_LIVE_CONTAINER not set")
	}
	cur := filepath.Join(dir, "current")
	cfg := DefaultConfig()
	cfg.Broker = broker
	cfg.DeviceID = "pi-live"
	cfg.Tenant = "acme"
	cfg.TLSCert = filepath.Join(cur, "device.crt")
	cfg.TLSKey = filepath.Join(cur, "device.key")
	cfg.TLSCA = filepath.Join(cur, "broker-ca.pem")
	cfg.PublishStateInterval = 0
	cfg.PublishHealthInterval = 0
	cfg.LWTEnabled = false
	cfg.MaxReconnectWait = 2 * time.Second

	a := New(cfg, plainHandler{})
	if err := a.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = a.Stop(context.Background()) })
	waitConnected(t, a, "with identity A")

	if err := os.Symlink("gen-b", cur+".tmp"); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(cur+".tmp", cur); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("docker", "restart", ctr).CombinedOutput(); err != nil {
		t.Fatalf("restart broker: %v: %s", err, out)
	}
	time.Sleep(time.Second)
	waitConnected(t, a, "after the broker came back")
	t.Log("reconnected by itself; the broker log must show identity B")
}
