package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/carlosprados/keystone/internal/adapter"
	httpadapter "github.com/carlosprados/keystone/internal/adapter/http"
	mqttadapter "github.com/carlosprados/keystone/internal/adapter/mqtt"
	natsadapter "github.com/carlosprados/keystone/internal/adapter/nats"
	reconcileadapter "github.com/carlosprados/keystone/internal/adapter/reconcile"
	"github.com/carlosprados/keystone/internal/agent"
	"github.com/carlosprados/keystone/internal/clock"
	"github.com/carlosprados/keystone/internal/config"
	"github.com/carlosprados/keystone/internal/runner"
	"github.com/carlosprados/keystone/internal/security"
	"github.com/carlosprados/keystone/internal/selfupdate"
	"github.com/carlosprados/keystone/internal/version"
)

// resolveDeviceID settles on a name for this device: what the caller asked for,
// then KEYSTONE_DEVICE_ID, then the hostname, then a constant. Every adapter
// that labels its traffic per device resolves it the same way, so a device does
// not answer to one name over NATS and another over MQTT.
func resolveDeviceID(explicit string) string {
	if explicit != "" {
		return explicit
	}
	if v := os.Getenv("KEYSTONE_DEVICE_ID"); v != "" {
		return v
	}
	if hostname, _ := os.Hostname(); hostname != "" {
		return hostname
	}
	return "keystone-agent"
}

func main() {
	// The agent re-executes this binary as a privilege-dropping shim when a
	// process component declares [lifecycle.run.security]. This has to be the
	// very first thing main does: the shim must reduce its own privileges and
	// exec the component, never start an agent.
	if len(os.Args) > 1 && os.Args[1] == runner.PrivdropFlag {
		if err := runner.RunPrivdropShim(os.Args[2:]); err != nil {
			// Fail closed: never fall back to running the component unconfined.
			fmt.Fprintf(os.Stderr, "keystone: %v\n", err)
			os.Exit(1)
		}
		return // unreachable: RunPrivdropShim execs on success
	}

	// The pre-start gate runs this, as root, to verify a staged proposal before
	// installing it. It is a separate mode so the gate uses the verifier of the
	// version already installed, never code the proposal brought with it.
	//
	// Spelled as a flag on purpose. A version from before this mode existed
	// rejects an unknown flag and exits 2 at once, so the gate refuses the
	// proposal. A bare word would have been ignored by flag parsing, and that
	// old binary would have started a whole agent, as root, inside the gate.
	if len(os.Args) > 1 && os.Args[1] == verifyUpdateFlag {
		os.Exit(runVerifyUpdate(os.Args[2:]))
	}

	// Load .env as early as possible so adapter configuration (flags/env) can use it.
	config.LoadDotEnvDefault()

	// HTTP adapter flags
	httpAddr := flag.String("http", "127.0.0.1:8080", "HTTP listen address (empty to disable)")
	apiToken := flag.String("api-token", "", "Bearer token required for the HTTP API (or KEYSTONE_API_TOKEN); required to bind a non-loopback address")
	allowNoEKUSigners := flag.Bool("allow-no-eku-signers", false, "Transition only: accept signing certificates that carry no extended key usage. Signers must be issued for codeSigning; certificates made before that was required have no EKU and are refused without this. Logged loudly every time it admits one. Will be removed")
	insecureSkipVerify := flag.Bool("insecure-skip-verify", false, "Disable mandatory artifact integrity checks (sha256 + signature). Dev/demo only (or KEYSTONE_INSECURE_SKIP_VERIFY=true)")

	// NATS adapter flags
	natsURL := flag.String("nats-url", "", "NATS server URL (empty to disable NATS adapter)")
	natsDeviceID := flag.String("nats-device-id", "", "Device ID for NATS subjects (required if NATS enabled)")
	natsTLSCert := flag.String("nats-tls-cert", "", "Path to NATS client TLS certificate")
	natsTLSKey := flag.String("nats-tls-key", "", "Path to NATS client TLS key")
	natsTLSCA := flag.String("nats-tls-ca", "", "Path to NATS CA certificate")
	natsTLSVerify := flag.Bool("nats-tls-verify", true, "Verify NATS server TLS certificate")
	natsStateInterval := flag.Duration("nats-state-interval", 10*time.Second, "Interval for publishing state events (0 to disable)")
	natsHealthInterval := flag.Duration("nats-health-interval", 30*time.Second, "Interval for publishing health events (0 to disable)")

	// NATS authentication flags (mutually exclusive, priority: nkey > creds > token > user)
	natsCreds := flag.String("nats-creds", "", "Path to NATS credentials file (.creds)")
	natsNKey := flag.String("nats-nkey", "", "Path to NATS NKey seed file")
	natsToken := flag.String("nats-token", "", "NATS authentication token")
	natsUser := flag.String("nats-user", "", "NATS username")
	natsPass := flag.String("nats-pass", "", "NATS password")

	// JetStream flags (persistent job queue)
	jsEnabled := flag.Bool("nats-jetstream", false, "Enable JetStream for persistent job queue")
	jsStreamName := flag.String("nats-js-stream", "KEYSTONE_JOBS", "JetStream stream name for jobs")
	jsWorkers := flag.Int("nats-js-workers", 1, "Number of concurrent job processor workers")

	// MQTT adapter flags
	mqttBroker := flag.String("mqtt-broker", "", "MQTT broker URL (empty to disable MQTT adapter)")
	mqttDeviceID := flag.String("mqtt-device-id", "", "Device ID for MQTT topics (required if MQTT enabled)")
	mqttClientID := flag.String("mqtt-client-id", "", "MQTT client ID (defaults to keystone-{device-id})")
	mqttTLSCert := flag.String("mqtt-tls-cert", "", "Path to MQTT client TLS certificate")
	mqttTLSKey := flag.String("mqtt-tls-key", "", "Path to MQTT client TLS key")
	mqttTLSCA := flag.String("mqtt-tls-ca", "", "Path to MQTT CA certificate")
	mqttTLSVerify := flag.Bool("mqtt-tls-verify", true, "Verify MQTT server TLS certificate")
	mqttUser := flag.String("mqtt-user", "", "MQTT username")
	mqttPass := flag.String("mqtt-pass", "", "MQTT password")
	mqttStateInterval := flag.Duration("mqtt-state-interval", 10*time.Second, "Interval for publishing state events (0 to disable)")
	mqttHealthInterval := flag.Duration("mqtt-health-interval", 30*time.Second, "Interval for publishing health events (0 to disable)")
	mqttQoS := flag.Int("mqtt-qos", 1, "Default QoS level for commands and responses (0, 1, or 2)")
	mqttDedupeTTL := flag.Duration("mqtt-command-dedupe-ttl", 10*time.Minute, "How long a commandId is remembered, so a redelivery of the same command is executed once. Raise it on links where a device can be offline longer than this")

	selfUpdateRoot := flag.String("self-update-root", "", "Directory holding the A/B install (versions/, current, state/). Setting it enables the confirmation half of self-update: this run marks itself confirmed once the plan is healthy and, where a remote control plane is configured, once that control plane has heard from the device. Empty disables it")

	clockPolicy := flag.String("clock-policy", "high-water", "What to do when the system clock is behind known-good time: high-water (verify against the later of the two) or strict (refuse to verify)")

	// Periodic reconcile flags
	selfUpdateConfirmTimeout := flag.Duration("self-update-confirm-timeout", 5*time.Minute, "How long a newly installed version has to confirm itself (plan healthy and, with a remote control plane, heard from) before it restarts so the pre-start gate counts the attempt; the gate rolls back after its limit of starts. Only applies while a version is on trial. 0 disables it, and then a version that starts fine but cannot reach its control plane is never rolled back")
	reconcileInterval := flag.Duration("reconcile-interval", 0, "Re-apply the plan in effect on this interval so dead components are restarted (0 disables it)")
	reconcileJitter := flag.Duration("reconcile-jitter", 0, "Spread reconcile passes across a fleet by this much (defaults to 10% of the interval)")

	// General flags
	demo := flag.Bool("demo", false, "Run a built-in demo: start a mock 3-component stack")
	showVersion := flag.Bool("version", false, "Print version and exit")
	flag.Parse()

	// Track explicitly-set flags so env vars only fill missing values.
	setFlags := map[string]bool{}
	flag.Visit(func(f *flag.Flag) {
		setFlags[f.Name] = true
	})

	applyStringEnv := func(flagName string, dst *string, envKey string) {
		if setFlags[flagName] {
			return
		}
		if v, ok := os.LookupEnv(envKey); ok && v != "" {
			*dst = v
		}
	}
	applyBoolEnv := func(flagName string, dst *bool, envKey string) {
		if setFlags[flagName] {
			return
		}
		v, ok := os.LookupEnv(envKey)
		if !ok || v == "" {
			return
		}
		parsed, err := strconv.ParseBool(v)
		if err != nil {
			log.Printf("[main] warning: invalid boolean env %s=%q (ignored)", envKey, v)
			return
		}
		*dst = parsed
	}
	applyIntEnv := func(flagName string, dst *int, envKey string) {
		if setFlags[flagName] {
			return
		}
		v, ok := os.LookupEnv(envKey)
		if !ok || v == "" {
			return
		}
		parsed, err := strconv.Atoi(v)
		if err != nil {
			log.Printf("[main] warning: invalid integer env %s=%q (ignored)", envKey, v)
			return
		}
		*dst = parsed
	}
	applyDurationEnv := func(flagName string, dst *time.Duration, envKey string) {
		if setFlags[flagName] {
			return
		}
		v, ok := os.LookupEnv(envKey)
		if !ok || v == "" {
			return
		}
		parsed, err := time.ParseDuration(v)
		if err != nil {
			log.Printf("[main] warning: invalid duration env %s=%q (ignored)", envKey, v)
			return
		}
		*dst = parsed
	}

	// MQTT env support (flags always win over env vars).
	applyStringEnv("mqtt-broker", mqttBroker, "KEYSTONE_MQTT_BROKER")
	applyStringEnv("mqtt-device-id", mqttDeviceID, "KEYSTONE_MQTT_DEVICE_ID")
	applyStringEnv("mqtt-client-id", mqttClientID, "KEYSTONE_MQTT_CLIENT_ID")
	applyStringEnv("mqtt-tls-cert", mqttTLSCert, "KEYSTONE_MQTT_TLS_CERT")
	applyStringEnv("mqtt-tls-key", mqttTLSKey, "KEYSTONE_MQTT_TLS_KEY")
	applyStringEnv("mqtt-tls-ca", mqttTLSCA, "KEYSTONE_MQTT_TLS_CA")
	applyBoolEnv("mqtt-tls-verify", mqttTLSVerify, "KEYSTONE_MQTT_TLS_VERIFY")
	applyBoolEnv("allow-no-eku-signers", allowNoEKUSigners, "KEYSTONE_ALLOW_NO_EKU_SIGNERS")
	security.AllowNoEKUSigners(*allowNoEKUSigners)
	// Refused rather than warned: a CA that issues transport identities and is
	// also trusted for code lets anyone who can get a connection certificate get
	// code accepted. This is configuration, fixed once, not a condition that
	// comes and goes like a broker being away.
	if err := security.CheckTransportSeparation(os.Getenv("KEYSTONE_TRUST_BUNDLE"),
		*mqttTLSCA, *mqttTLSCert, *natsTLSCA, *natsTLSCert); err != nil {
		log.Fatalf("[main] refusing to start: %v", err)
	}
	if *allowNoEKUSigners {
		log.Printf("[main] WARNING --allow-no-eku-signers: signing certificates with no extended key usage are accepted. Reissue them for codeSigning and turn this off")
	}
	applyStringEnv("mqtt-user", mqttUser, "KEYSTONE_MQTT_USER")
	applyStringEnv("mqtt-pass", mqttPass, "KEYSTONE_MQTT_PASS")
	applyIntEnv("mqtt-qos", mqttQoS, "KEYSTONE_MQTT_QOS")
	applyDurationEnv("mqtt-command-dedupe-ttl", mqttDedupeTTL, "KEYSTONE_MQTT_COMMAND_DEDUPE_TTL")
	applyStringEnv("self-update-root", selfUpdateRoot, "KEYSTONE_SELF_UPDATE_ROOT")
	applyDurationEnv("mqtt-state-interval", mqttStateInterval, "KEYSTONE_MQTT_STATE_INTERVAL")
	applyDurationEnv("mqtt-health-interval", mqttHealthInterval, "KEYSTONE_MQTT_HEALTH_INTERVAL")

	applyStringEnv("clock-policy", clockPolicy, "KEYSTONE_CLOCK_POLICY")

	// Periodic reconcile env support.
	applyDurationEnv("self-update-confirm-timeout", selfUpdateConfirmTimeout, "KEYSTONE_SELF_UPDATE_CONFIRM_TIMEOUT")
	applyDurationEnv("reconcile-interval", reconcileInterval, "KEYSTONE_RECONCILE_INTERVAL")
	applyDurationEnv("reconcile-jitter", reconcileJitter, "KEYSTONE_RECONCILE_JITTER")
	// Jitter follows the interval unless someone asked for a specific value —
	// including 0, which is a legitimate ask for a single device.
	if !setFlags["reconcile-jitter"] && os.Getenv("KEYSTONE_RECONCILE_JITTER") == "" {
		*reconcileJitter = *reconcileInterval / 10
	}

	if *showVersion {
		fmt.Printf("keystone %s (%s)\n", version.Version, version.Commit)
		return
	}

	// Root context with graceful shutdown
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// Create agent
	skipVerify := *insecureSkipVerify || strings.EqualFold(os.Getenv("KEYSTONE_INSECURE_SKIP_VERIFY"), "true")
	policy, err := clock.ParsePolicy(*clockPolicy)
	if err != nil {
		log.Fatalf("[main] %v", err)
	}
	a := agent.New(agent.Options{HTTPAddr: *httpAddr, InsecureSkipVerify: skipVerify, ClockPolicy: policy, SelfUpdateRoot: *selfUpdateRoot})

	// Create adapter registry
	registry := adapter.NewRegistry()

	// Register HTTP adapter (enabled by default)
	if *httpAddr != "" {
		token := *apiToken
		if token == "" {
			token = os.Getenv("KEYSTONE_API_TOKEN")
		}
		httpCfg := httpadapter.Config{Addr: *httpAddr, Token: token}
		httpAdapter := httpadapter.New(httpCfg, a)
		registry.Register(httpAdapter)
		log.Printf("[main] HTTP adapter configured on %s", *httpAddr)
	}

	// Register NATS adapter (if configured)
	if *natsURL != "" {
		*natsDeviceID = resolveDeviceID(*natsDeviceID)

		natsCfg := natsadapter.DefaultConfig()
		natsCfg.URL = *natsURL
		natsCfg.DeviceID = *natsDeviceID
		natsCfg.TLSCert = *natsTLSCert
		natsCfg.TLSKey = *natsTLSKey
		natsCfg.TLSCA = *natsTLSCA
		natsCfg.TLSVerify = *natsTLSVerify
		natsCfg.PublishStateInterval = *natsStateInterval
		natsCfg.PublishHealthInterval = *natsHealthInterval

		// Authentication configuration
		natsCfg.CredentialsFile = *natsCreds
		natsCfg.NKeyFile = *natsNKey
		natsCfg.Token = *natsToken
		natsCfg.Username = *natsUser
		natsCfg.Password = *natsPass

		// JetStream configuration
		natsCfg.JetStream.Enabled = *jsEnabled
		if *jsStreamName != "" {
			natsCfg.JetStream.StreamName = *jsStreamName
		}
		if *jsWorkers > 0 {
			natsCfg.JetStream.WorkerCount = *jsWorkers
		}

		nats := natsadapter.New(natsCfg, a)
		registry.Register(nats)
		jsStatus := "disabled"
		if *jsEnabled {
			jsStatus = fmt.Sprintf("enabled (stream=%s, workers=%d)", natsCfg.JetStream.StreamName, natsCfg.JetStream.WorkerCount)
		}
		log.Printf("[main] NATS adapter configured for %s (device: %s, jetstream: %s)", *natsURL, *natsDeviceID, jsStatus)
	}

	// Register MQTT adapter (if configured)
	if *mqttBroker != "" {
		*mqttDeviceID = resolveDeviceID(*mqttDeviceID)

		mqttCfg := mqttadapter.DefaultConfig()
		mqttCfg.Broker = *mqttBroker
		mqttCfg.DeviceID = *mqttDeviceID
		mqttCfg.ClientID = *mqttClientID
		mqttCfg.TLSCert = *mqttTLSCert
		mqttCfg.TLSKey = *mqttTLSKey
		mqttCfg.TLSCA = *mqttTLSCA
		mqttCfg.TLSVerify = *mqttTLSVerify
		mqttCfg.Username = *mqttUser
		mqttCfg.Password = *mqttPass
		mqttCfg.PublishStateInterval = *mqttStateInterval
		mqttCfg.PublishHealthInterval = *mqttHealthInterval
		mqttCfg.CommandDedupeTTL = *mqttDedupeTTL
		if *mqttQoS >= 0 && *mqttQoS <= 2 {
			mqttCfg.CommandQoS = byte(*mqttQoS)
			mqttCfg.ResponseQoS = byte(*mqttQoS)
		}

		mqtt := mqttadapter.New(mqttCfg, a)
		registry.Register(mqtt)
		log.Printf("[main] MQTT adapter configured for %s (device: %s)", *mqttBroker, *mqttDeviceID)
	}

	// Self-update confirmation. Built here rather than inside the agent because
	// whether a report is required depends on which adapters were configured,
	// which only this function knows.
	if *selfUpdateRoot != "" {
		layout := selfupdate.Layout{Root: *selfUpdateRoot}
		// A remote control plane is what makes being mute a failure. With only
		// a loopback HTTP adapter there is nobody to be mute to, and requiring
		// a report would mean no update could ever confirm — every one of them
		// reverted by a guardrail meant to catch the broken ones.
		requireReport := *mqttBroker != "" || *natsURL != ""
		// The version this run confirms is the install directory it was started
		// from, the same name the pending marker and the gate use. The compiled-in
		// version is only a fallback for a binary started outside the layout.
		running := version.Version
		if exe, err := os.Executable(); err == nil {
			if v, ok := layout.RunningVersion(exe); ok {
				running = v
			}
		}
		conf := selfupdate.NewConfirmation(layout, running, requireReport)
		a.SetUpdateConfirmation(conf)
		// Convergence is marked when a plan applies. With no plan to resume
		// there is nothing to converge: without this, an agent with no plan
		// could never confirm an update, and the deadline would revert it.
		if !a.ResumesPlan() {
			conf.MarkConverged()
		}
		conf.WatchDeadline(ctx, *selfUpdateConfirmTimeout, a.RequestRestart)
		log.Printf("[main] self-update confirmation enabled at %s (version %s, report required: %v)",
			*selfUpdateRoot, running, requireReport)
	}

	// Register the periodic reconcile adapter (if configured). Off unless asked
	// for: switching it on by default would change how every device in a fleet
	// behaves the moment the binary is updated.
	if *reconcileInterval > 0 {
		reconcileCfg := reconcileadapter.Config{
			Interval: *reconcileInterval,
			Jitter:   *reconcileJitter,
			DeviceID: resolveDeviceID(""),
		}
		registry.Register(reconcileadapter.New(reconcileCfg, a))
		log.Printf("[main] periodic reconcile configured every %s (jitter %s)", *reconcileInterval, *reconcileJitter)
	}

	// Start all adapters
	log.Printf("[main] keystone starting version=%s adapters=%v", version.Version, registry.List())
	if err := registry.StartAll(ctx); err != nil {
		log.Fatalf("[main] failed to start adapters: %v", err)
	}

	// If requested, run the internal demo stack
	if *demo {
		go func() {
			if err := a.StartDemo(); err != nil {
				log.Printf("[main] demo start error: %v", err)
			}
		}()
	}

	// Block until a shutdown signal or a restart request.
	//
	// A staged self-update ends here: the agent does not re-execute itself, it
	// exits and lets the supervisor start it again from the symlink, resolved
	// afresh. A process that replaces its own image keeps everything it got
	// wrong — its open descriptors, its memory, and its belief that the binary
	// on disk is the one it is running.
	restarting := false
	select {
	case <-ctx.Done():
		log.Println("[main] shutdown signal received, draining...")
	case reason := <-a.RestartRequests():
		restarting = true
		log.Printf("[main] restart requested: %s", reason)
		// Let an in-flight response reach whoever asked. Without this pause,
		// the command that ordered the update is the one whose answer never
		// arrives.
		time.Sleep(agent.RestartGrace())
		log.Println("[main] draining for restart...")
	}

	// Graceful shutdown with timeout
	shutCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Stop all adapters
	if err := registry.StopAll(shutCtx); err != nil {
		log.Printf("[main] adapter shutdown error: %v", err)
	}

	// Close the agent, stopping its components — unless it is coming straight
	// back, in which case they are left running for the next start to adopt.
	// That is what keeps a self-update from restarting everything the agent
	// supervises.
	//
	// A signal goes through the stopping path on purpose: SIGTERM is what
	// `systemctl stop` sends as well as `systemctl restart`, and the agent
	// cannot tell them apart. Leaving components alive on a genuine stop would
	// strand processes that nothing is watching and nothing will report.
	closeErr := error(nil)
	if restarting {
		closeErr = a.CloseForRestart()
	} else {
		closeErr = a.Close()
	}
	if closeErr != nil {
		log.Printf("[main] agent close error: %v", closeErr)
	}

	log.Println("[main] bye")
	_ = os.Stdout.Sync()
}
