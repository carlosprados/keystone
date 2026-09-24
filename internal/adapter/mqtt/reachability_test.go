package mqtt

import "testing"

// TestBrokerIsLoopback decides what counts as proof that a control plane can
// still reach this device. Publishing to a broker on the same box proves
// nothing: the device would confirm a self-update with its cable pulled out.
func TestBrokerIsLoopback(t *testing.T) {
	local := []string{
		"tcp://localhost:1883",
		"tcp://127.0.0.1:1883",
		"ssl://127.0.0.1:8883",
		"tcp://[::1]:1883",
		"localhost:1883",
		"127.0.0.1:1883",
		"tcp://127.10.20.30:1883",
	}
	for _, b := range local {
		if !brokerIsLoopback(b) {
			t.Errorf("%q was not recognised as local; publishing to it would be treated as proof of reachability", b)
		}
	}

	remote := []string{
		"tcp://broker.example.net:1883",
		"ssl://api.opengate.es:8883",
		"tcp://192.168.1.40:1883",
		"tcp://10.0.0.5:1883",
		"",
	}
	for _, b := range remote {
		if brokerIsLoopback(b) {
			t.Errorf("%q was treated as local; publishing to it would stop counting as proof", b)
		}
	}
}

// TestBrokerIsLoopbackOnGarbage: an unparseable broker string must not be
// treated as local, because that would silently weaken the evidence an update
// needs while looking like a configuration problem somewhere else.
func TestBrokerIsLoopbackOnGarbage(t *testing.T) {
	for _, b := range []string{"://", "not a url at all", "tcp://"} {
		if brokerIsLoopback(b) {
			t.Errorf("%q was treated as local", b)
		}
	}
}
