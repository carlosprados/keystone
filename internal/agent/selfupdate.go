package agent

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"time"

	"github.com/carlosprados/keystone/internal/artifact"
	"github.com/carlosprados/keystone/internal/security"
	"github.com/carlosprados/keystone/internal/selfupdate"
	"github.com/carlosprados/keystone/internal/version"
)

// SelfUpdateSpec is an instruction to replace the agent's own binary.
type SelfUpdateSpec struct {
	// Version names the new build. It becomes a directory name, so it is what
	// an operator will see in logs and telemetry — and what a rollback points
	// back at.
	Version string
	// URI is where the binary is fetched from.
	URI string
	// SHA256 is mandatory. An agent binary is the one artifact where "we could
	// not check it" must never mean "install it anyway".
	SHA256 string
	// SigURI and CertURI locate the detached signature. Empty means
	// "<URI>.sig" and the configured leaf certificate, matching how artifacts
	// are signed elsewhere.
	SigURI  string
	CertURI string
}

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
func (a *Agent) StageSelfUpdate(ctx context.Context, spec SelfUpdateSpec) error {
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
	if spec.Version == version.Version {
		return fmt.Errorf("self-update: %s is already running", spec.Version)
	}

	layout := selfupdate.Layout{Root: a.selfUpdateRoot}
	if err := layout.Prepare(); err != nil {
		return fmt.Errorf("self-update: prepare layout: %w", err)
	}

	if installed, err := layout.Installed(); err == nil {
		for _, v := range installed {
			if v == spec.Version {
				return fmt.Errorf("self-update: version %s is already installed", spec.Version)
			}
		}
	}

	// Download into a directory of its own, so a failed or partial attempt
	// never sits next to a good one with the same name.
	stagingDir := filepath.Join(layout.StagingDir(), spec.Version)
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

	if err := a.verifySelfUpdateSignature(ctx, stagingDir, res.Path, spec); err != nil {
		return err
	}

	if err := layout.Install(res.Path, spec.Version); err != nil {
		return fmt.Errorf("self-update: %w", err)
	}

	// Order matters here, and the obvious order is wrong. MarkPending records
	// what is running now as the version to fall back to, so it has to read the
	// symlink BEFORE the symlink moves. Activating first makes it record the
	// version being installed as its own fallback, which leaves a trial with
	// nowhere to go back to.
	//
	// It is also the safer failure: if marking fails, nothing has moved yet.
	if err := layout.MarkPending(spec.Version); err != nil {
		return fmt.Errorf("self-update: mark pending: %w", err)
	}
	if err := layout.Activate(spec.Version); err != nil {
		// Marked but not activated: clear the marker rather than leave the gate
		// counting restarts of a version that is not going to run.
		if st, lerr := layout.LoadState(); lerr == nil {
			st.Pending = ""
			st.Boots = 0
			_ = layout.SaveState(st)
		}
		return fmt.Errorf("self-update: %w", err)
	}

	log.Printf("[selfupdate] %s installed and activated; it takes effect on the next restart", spec.Version)
	return nil
}

// verifySelfUpdateSignature applies the same rule as every other artifact: a
// signature is required unless verification was explicitly disabled.
func (a *Agent) verifySelfUpdateSignature(ctx context.Context, stagingDir, binaryPath string, spec SelfUpdateSpec) error {
	if a.insecureSkipVerify {
		log.Printf("[selfupdate] WARNING installing %s WITHOUT signature verification (--insecure-skip-verify)", spec.Version)
		return nil
	}
	if a.trustPool == nil {
		return fmt.Errorf("self-update: signature required but no trust bundle configured; set KEYSTONE_TRUST_BUNDLE")
	}

	sigURI := spec.SigURI
	if sigURI == "" {
		sigURI = spec.URI + ".sig"
	}
	cfg := artifact.DefaultDownloadConfig()

	sig, err := artifact.DownloadWithConfig(ctx, filepath.Join(stagingDir, "sig"), sigURI, cfg)
	if err != nil {
		return fmt.Errorf("self-update: download signature %s: %w", sigURI, err)
	}

	certPath := os.Getenv("KEYSTONE_LEAF_CERT")
	if spec.CertURI != "" {
		cert, cerr := artifact.DownloadWithConfig(ctx, filepath.Join(stagingDir, "cert"), spec.CertURI, cfg)
		if cerr != nil {
			return fmt.Errorf("self-update: download certificate %s: %w", spec.CertURI, cerr)
		}
		certPath = cert.Path
	}

	now, err := a.verificationTime()
	if err != nil {
		return fmt.Errorf("self-update: %w", err)
	}
	if certPath == "" {
		return fmt.Errorf("self-update: no certificate for signature verification; set KEYSTONE_LEAF_CERT or send certUri")
	}
	if err := security.VerifyDetachedAt(binaryPath, sig.Path, certPath, a.trustPool, now); err != nil {
		return fmt.Errorf("self-update: signature verification failed for %s: %w", spec.Version, err)
	}
	log.Printf("[selfupdate] signature verified for %s", spec.Version)
	return nil
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
