package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"

	"github.com/carlosprados/keystone/internal/clock"
	"github.com/carlosprados/keystone/internal/security"
	"github.com/carlosprados/keystone/internal/selfupdate"
)

// verifyUpdateFlag selects the verify-only mode the pre-start gate uses.
const verifyUpdateFlag = "--verify-update"

// runVerifyUpdate checks a proposal in dir — the binary, its detached signature
// and certificate, as the agent staged them — and returns the exit status the
// gate acts on: 0 to install it, anything else to refuse.
//
// The gate runs it from the version already installed, as root, so what decides
// is code the device already trusts, reading the trust bundle the agent cannot
// write. The agent verified the same files before proposing them; this repeats
// the check because the agent is exactly the party this boundary does not trust.
//
// It uses the agent's clock evidence (runtime/state, relative to the unit's
// WorkingDirectory) so a device with no RTC booting at 1970 does not reject a
// valid certificate as not yet valid.
func runVerifyUpdate(args []string) int {
	if len(args) != 1 {
		fmt.Fprintln(os.Stderr, "usage: keystone --verify-update <staged-dir>")
		return 2
	}
	dir := args[0]
	binary := filepath.Join(dir, selfupdate.BinaryName)

	if err := selfupdate.CheckArchitecture(binary); err != nil {
		fmt.Fprintf(os.Stderr, "verify-update: %v\n", err)
		return 1
	}
	if skip, _ := strconv.ParseBool(os.Getenv("KEYSTONE_INSECURE_SKIP_VERIFY")); skip {
		fmt.Fprintln(os.Stderr, "verify-update: WARNING accepting an unverified proposal (KEYSTONE_INSECURE_SKIP_VERIFY)")
		return 0
	}

	bundle := os.Getenv("KEYSTONE_TRUST_BUNDLE")
	if bundle == "" {
		fmt.Fprintln(os.Stderr, "verify-update: no trust bundle configured (KEYSTONE_TRUST_BUNDLE); refusing")
		return 1
	}
	roots, err := security.LoadTrustBundle(bundle)
	if err != nil {
		fmt.Fprintf(os.Stderr, "verify-update: %v\n", err)
		return 1
	}

	policy := clock.PolicyHighWater
	if p := os.Getenv("KEYSTONE_CLOCK_POLICY"); p != "" {
		if policy, err = clock.ParsePolicy(p); err != nil {
			fmt.Fprintf(os.Stderr, "verify-update: %v\n", err)
			return 1
		}
	}
	now, err := clock.New(policy, filepath.Join("runtime", "state")).VerificationTime()
	if err != nil {
		fmt.Fprintf(os.Stderr, "verify-update: %v\n", err)
		return 1
	}

	sig := filepath.Join(dir, selfupdate.StagedSig)
	cert := filepath.Join(dir, selfupdate.StagedCert)
	if err := security.VerifyDetachedAt(binary, sig, cert, roots, now); err != nil {
		fmt.Fprintf(os.Stderr, "verify-update: %v\n", err)
		return 1
	}
	fmt.Println("verify-update: OK")
	return 0
}
