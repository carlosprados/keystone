package artifact

import (
	"net/http"
	"net/url"
	"testing"
)

// TestTransportHonoursProxyEnvironment guards a defect that produced a symptom
// with no relation to its cause: a hand-built http.Transport inherits nothing
// from http.DefaultTransport, so leaving Proxy unset silently ignores
// HTTPS_PROXY. On a network where egress must go through a proxy, downloads did
// not take a different route — they timed out, and the error never mentioned a
// proxy.
//
// The check is on the field rather than on a live proxied request because
// http.ProxyFromEnvironment resolves the environment once per process and
// caches it: a test that set the variables would pass or fail depending on
// whether another test had already triggered that resolution. What can regress
// here is the field going missing again, and that is what this pins.
func TestTransportHonoursProxyEnvironment(t *testing.T) {
	tr := newTransport(DefaultDownloadConfig())

	if tr.Proxy == nil {
		t.Fatal("transport has no Proxy func: HTTPS_PROXY/HTTP_PROXY/NO_PROXY would be ignored")
	}
}

// TestTransportProxyResolvesExplicitConfig exercises the resolution itself
// without touching the process environment, by calling the same function the
// transport is wired to with a request it must route.
func TestTransportProxyResolvesExplicitConfig(t *testing.T) {
	tr := newTransport(DefaultDownloadConfig())

	req, err := http.NewRequest("GET", "http://artifacts.example.net/keystone.tar.gz", nil)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}

	// Whatever the ambient environment says, the call must not error: a Proxy
	// func that panics or fails on a well-formed request would break every
	// download rather than just the proxied ones.
	proxyURL, err := tr.Proxy(req)
	if err != nil {
		t.Fatalf("proxy resolution failed for a well-formed request: %v", err)
	}
	if proxyURL != nil {
		if _, err := url.Parse(proxyURL.String()); err != nil {
			t.Fatalf("proxy resolution returned an unusable URL %q: %v", proxyURL, err)
		}
	}
}

// TestTransportTimeoutsComeFromConfig: the transport is now built in its own
// function, so the settings it used to carry inline must still arrive.
func TestTransportTimeoutsComeFromConfig(t *testing.T) {
	cfg := DefaultDownloadConfig()
	tr := newTransport(cfg)

	if tr.TLSHandshakeTimeout != cfg.TLSTimeout {
		t.Errorf("TLSHandshakeTimeout = %v, want %v", tr.TLSHandshakeTimeout, cfg.TLSTimeout)
	}
	if tr.ResponseHeaderTimeout != cfg.ReadTimeout {
		t.Errorf("ResponseHeaderTimeout = %v, want %v", tr.ResponseHeaderTimeout, cfg.ReadTimeout)
	}
	if tr.IdleConnTimeout != cfg.IdleTimeout {
		t.Errorf("IdleConnTimeout = %v, want %v", tr.IdleConnTimeout, cfg.IdleTimeout)
	}
	if !tr.ForceAttemptHTTP2 {
		t.Error("ForceAttemptHTTP2 was dropped")
	}
}
