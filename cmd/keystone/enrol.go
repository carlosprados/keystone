package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/carlosprados/keystone/internal/clock"
	"github.com/carlosprados/keystone/internal/enrol"
	"github.com/carlosprados/keystone/internal/security"
)

// enrolCommand is the mode that turns a bootstrap file into a device identity.
const enrolCommand = "enrol"

// runEnrol implements `keystone enrol`. It runs once, on the device, before the
// agent is configured to use the identity with --enrol-dir.
func runEnrol(args []string) int {
	fs := flag.NewFlagSet("keystone enrol", flag.ContinueOnError)
	fs.Usage = func() {
		fmt.Fprintf(fs.Output(), `Usage: keystone enrol --bootstrap <file> [--dir <dir>] [--replace]

Enrol this device: generate a P-256 key that never leaves it, send a
certificate request to the enrolment server named in the bootstrap file,
trusting only the CA pinned there, and store the certificate, key and broker
CA in --dir. The token is single-use; the bootstrap file is deleted on success.

Then run the agent with --enrol-dir (KEYSTONE_ENROL_DIR) set to the same
directory: it takes its MQTT identity, tenant and device ID from there, and
renews the certificate by itself.

`)
		fs.PrintDefaults()
	}
	bootstrap := fs.String("bootstrap", "", "Bootstrap file from the operator (required)")
	dir := fs.String("dir", os.Getenv("KEYSTONE_ENROL_DIR"), "Directory for the identity (or KEYSTONE_ENROL_DIR)")
	replace := fs.Bool("replace", false, "Enrol over an existing identity, with a new token: after a refused renewal or an expired certificate")
	timeout := fs.Duration("timeout", 5*time.Minute, "Give up after this long, including waits the server asks for")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *bootstrap == "" || *dir == "" {
		fs.Usage()
		return 2
	}

	policy := clock.PolicyHighWater
	if p := os.Getenv("KEYSTONE_CLOCK_POLICY"); p != "" {
		var err error
		if policy, err = clock.ParsePolicy(p); err != nil {
			fmt.Fprintf(os.Stderr, "enrol: %v\n", err)
			return 1
		}
	}
	clk := clock.New(policy, filepath.Join("runtime", "state"))

	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()
	e, err := enrol.Enrol(ctx, enrol.Options{
		BootstrapPath: *bootstrap,
		Dir:           *dir,
		Replace:       *replace,
		Now:           clk.Now,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "enrol: %v\n", err)
		return 1
	}

	store := enrol.Store{Dir: *dir}
	fmt.Printf("enrolled %s/%s; certificate valid until %s, renewed after %s\n",
		e.Tenant, e.Device, e.NotAfter.Format(time.RFC3339), e.RenewAfter.Format(time.RFC3339))
	fmt.Printf("identity in %s\n", filepath.Join(*dir, "current"))
	fmt.Printf("run the agent with: KEYSTONE_ENROL_DIR=%s (and --mqtt-broker)\n", *dir)

	// Said now rather than at the agent's next start, which would refuse it.
	if err := security.CheckTransportSeparation(os.Getenv("KEYSTONE_TRUST_BUNDLE"), store.BrokerCAPath(), store.CertPath()); err != nil {
		fmt.Fprintf(os.Stderr, "enrol: WARNING the agent will refuse to start with this identity: %v\n", err)
		return 1
	}
	return 0
}

// mqttIdentity is the part of the MQTT configuration an enrolment decides.
type mqttIdentity struct {
	tenant, deviceID, cert, key, ca *string
}

// applyEnrolment fills the MQTT identity from the enrolment in dir.
//
// A value set explicitly that disagrees with the enrolment is refused rather
// than preferred: a device ID that differs from the certificate's name would
// put the device under topics its certificate does not cover, and the broker's
// ACL would drop its publishes without a word.
func applyEnrolment(dir string, id mqttIdentity) (*enrol.Store, error) {
	store := enrol.Store{Dir: dir}
	e, err := store.Current()
	if err != nil {
		return nil, fmt.Errorf("--enrol-dir %s: %w", dir, err)
	}
	for _, c := range []struct {
		name, want string
		got        *string
	}{
		{"--mqtt-tenant", e.Tenant, id.tenant},
		{"--mqtt-device-id", e.Device, id.deviceID},
	} {
		if *c.got != "" && *c.got != c.want {
			return nil, fmt.Errorf("%s is %q but the enrolment in %s is for %q; remove it, the enrolment provides it", c.name, *c.got, dir, c.want)
		}
		*c.got = c.want
	}
	for _, c := range []struct {
		name string
		got  *string
		path string
	}{
		{"--mqtt-tls-cert", id.cert, store.CertPath()},
		{"--mqtt-tls-key", id.key, store.KeyPath()},
		{"--mqtt-tls-ca", id.ca, store.BrokerCAPath()},
	} {
		if *c.got != "" && *c.got != c.path {
			return nil, fmt.Errorf("%s is set to %s, but --enrol-dir provides the MQTT identity; remove it", c.name, *c.got)
		}
		*c.got = c.path
	}
	return &store, nil
}
