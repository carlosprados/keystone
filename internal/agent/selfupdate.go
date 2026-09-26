package agent

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/carlosprados/keystone/internal/adapter"
	"github.com/carlosprados/keystone/internal/artifact"
	"github.com/carlosprados/keystone/internal/security"
	"github.com/carlosprados/keystone/internal/selfupdate"
	"github.com/carlosprados/keystone/internal/version"
)

// StageSelfUpdate downloads, verifies and installs a new agent binary beside
// the running one, and marks it pending so the pre-start gate will count its
// restarts.
//
// It does not restart anything. The caller decides when the device can afford
// to, and systemd is what starts the new version — see
// docs/self-update-design.md.
//
// Nothing is stopped at any point: the new binary is installed into its own
// directory while the current one keeps running, and only the final restart
// interrupts service. That ordering is deliberate. The reconcile path for
// ordinary components stops a component before downloading its replacement,
// which turned a mistyped URL into three minutes of downtime in the field; an
// update that did the same with the agent's own binary would make the outage
// the full download time, over whatever link the device has.
func (a *Agent) StageSelfUpdate(ctx context.Context, spec adapter.SelfUpdateSpec) error {
	if a.selfUpdateRoot == "" {
		return fmt.Errorf("self-update is not enabled on this agent (--self-update-root is unset)")
	}
	if spec.Version == "" {
		return fmt.Errorf("self-update: version is required")
	}
	if spec.URI == "" {
		return fmt.Errorf("self-update: uri is required")
	}
	if spec.SHA256 == "" && !a.insecureSkipVerify {
		return fmt.Errorf("self-update: sha256 is required")
	}
	// A release binary reports "0.12.4" and commands name it "v0.12.4".
	if strings.TrimPrefix(spec.Version, "v") == strings.TrimPrefix(version.Version, "v") {
		return fmt.Errorf("self-update: %s is already running", spec.Version)
	}

	layout := selfupdate.Layout{Root: a.selfUpdateRoot}
	if err := layout.Prepare(); err != nil {
		return fmt.Errorf("self-update: prepare layout: %w", err)
	}

	// A version already on disk is refused only while it is in use: the one
	// running, or the one a failure rolls back to. Any other is a version that
	// failed its trial, and retrying it after fixing what failed — the broker,
	// the network — has to be possible without inventing a new version number.
	// Install then insists the bytes are the same, so the name keeps meaning
	// one binary.
	if installed, err := layout.Installed(); err == nil {
		current, _ := layout.Current()
		st, _ := layout.LoadState()
		for _, v := range installed {
			if v != spec.Version {
				continue
			}
			if v == current || v == st.Confirmed {
				return fmt.Errorf("self-update: version %s is already installed and in use", spec.Version)
			}
			log.Printf("[selfupdate] %s is installed from an earlier attempt that did not confirm; retrying it", spec.Version)
		}
	}

	// Download into a directory of its own, so a failed or partial attempt
	// never sits next to a good one with the same name.
	// Not staging/<version>: that is where the finished proposal goes, and this
	// directory is removed on return.
	stagingDir := filepath.Join(layout.StagingDir(), ".download-"+spec.Version)
	if err := os.MkdirAll(stagingDir, 0o755); err != nil {
		return fmt.Errorf("self-update: create staging dir: %w", err)
	}
	defer os.RemoveAll(stagingDir)

	log.Printf("[selfupdate] downloading %s from %s", spec.Version, spec.URI)
	cfg := artifact.DefaultDownloadConfig()
	res, err := artifact.DownloadWithConfig(ctx, stagingDir, spec.URI, cfg)
	if err != nil {
		return fmt.Errorf("self-update: download %s: %w", spec.URI, err)
	}

	if spec.SHA256 != "" {
		if err := artifact.VerifySHA256(res.Path, spec.SHA256); err != nil {
			return fmt.Errorf("self-update: %w", err)
		}
	}

	sigPath, certPath, err := a.verifySelfUpdateSignature(ctx, stagingDir, res.Path, spec)
	if err != nil {
		return err
	}

	// Propose, do not install. Under the A/B unit this process cannot write
	// versions/ or move `current`, and that is the design: the pre-start gate
	// runs as root, verifies the staged binary against the trust bundle itself,
	// and only then installs and activates it. A compromised agent can propose
	// a binary but not get one run that the trust bundle does not vouch for.
	if err := layout.StageProposal(res.Path, sigPath, certPath, spec.Version); err != nil {
		return fmt.Errorf("self-update: %w", err)
	}
	if err := layout.Propose(spec.Version); err != nil {
		return fmt.Errorf("self-update: propose: %w", err)
	}

	log.Printf("[selfupdate] %s staged and proposed; the pre-start gate verifies and installs it on the next start", spec.Version)
	return nil
}

// verifySelfUpdateSignature applies the same rule as every other artifact: a
// signature is required unless verification was explicitly disabled.
//
// It returns the signature and certificate it verified with, so they can travel
// with the proposal: the gate verifies the same pair again, as root.
func (a *Agent) verifySelfUpdateSignature(ctx context.Context, stagingDir, binaryPath string, spec adapter.SelfUpdateSpec) (sigPath, certPath string, err error) {
	if a.insecureSkipVerify {
		log.Printf("[selfupdate] WARNING installing %s WITHOUT signature verification (--insecure-skip-verify)", spec.Version)
		return "", "", nil
	}
	if a.trustPool == nil {
		return "", "", fmt.Errorf("self-update: signature required but no trust bundle configured; set KEYSTONE_TRUST_BUNDLE")
	}

	sigURI := spec.SigURI
	if sigURI == "" {
		sigURI = spec.URI + ".sig"
	}
	cfg := artifact.DefaultDownloadConfig()

	sig, err := artifact.DownloadWithConfig(ctx, filepath.Join(stagingDir, "sig"), sigURI, cfg)
	if err != nil {
		return "", "", fmt.Errorf("self-update: download signature %s: %w", sigURI, err)
	}

	certPath = os.Getenv("KEYSTONE_LEAF_CERT")
	if spec.CertURI != "" {
		cert, cerr := artifact.DownloadWithConfig(ctx, filepath.Join(stagingDir, "cert"), spec.CertURI, cfg)
		if cerr != nil {
			return "", "", fmt.Errorf("self-update: download certificate %s: %w", spec.CertURI, cerr)
		}
		certPath = cert.Path
	}

	now, verr := a.verificationTime()
	if verr != nil {
		return "", "", fmt.Errorf("self-update: %w", verr)
	}
	if certPath == "" {
		return "", "", fmt.Errorf("self-update: no certificate for signature verification; set KEYSTONE_LEAF_CERT or send certUri")
	}
	if err := security.VerifyDetachedAt(binaryPath, sig.Path, certPath, a.trustPool, now); err != nil {
		return "", "", fmt.Errorf("self-update: signature verification failed for %s: %w", spec.Version, err)
	}
	log.Printf("[selfupdate] signature verified for %s", spec.Version)
	return sig.Path, certPath, nil
}

// RequestRestart asks the process to exit so the supervisor starts it again.
//
// The agent does not restart itself: it exits, and systemd starts it from the
// symlink, resolved afresh. A process that re-executes itself keeps its own
// mistakes — the open file descriptors, the memory, the assumption that the
// binary on disk is the one it is running.
func (a *Agent) RequestRestart(reason string) {
	select {
	case a.restartRequests <- reason:
	default:
		// A restart is already queued. Asking twice changes nothing.
	}
}

// RestartRequests is consumed by main, which owns process lifetime.
func (a *Agent) RestartRequests() <-chan string { return a.restartRequests }

// waitBeforeRestart gives an in-flight response time to reach whoever asked for
// the update. Without it, the caller's own command is what kills the connection
// it expects the answer on.
const restartGrace = 2 * time.Second

// RestartGrace is exported so main and tests agree on the wait.
func RestartGrace() time.Duration { return restartGrace }
