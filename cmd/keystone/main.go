package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/carlosprados/keystone/internal/adapter"
	httpadapter "github.com/carlosprados/keystone/internal/adapter/http"
	mqttadapter "github.com/carlosprados/keystone/internal/adapter/mqtt"
	natsadapter "github.com/carlosprados/keystone/internal/adapter/nats"
	"github.com/carlosprados/keystone/internal/agent"
	"github.com/carlosprados/keystone/internal/config"
	"github.com/carlosprados/keystone/internal/version"
)

func main() {
	// Load .env as early as possible so adapter configuration (flags/env) can use it.
	config.LoadDotEnvDefault()

	// HTTP adapter flags
	httpAddr := flag.String("http", ":8080", "HTTP listen address (empty to disable)")

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
	applyStringEnv("mqtt-user", mqttUser, "KEYSTONE_MQTT_USER")
	applyStringEnv("mqtt-pass", mqttPass, "KEYSTONE_MQTT_PASS")
	applyIntEnv("mqtt-qos", mqttQoS, "KEYSTONE_MQTT_QOS")
	applyDurationEnv("mqtt-state-interval", mqttStateInterval, "KEYSTONE_MQTT_STATE_INTERVAL")
	applyDurationEnv("mqtt-health-interval", mqttHealthInterval, "KEYSTONE_MQTT_HEALTH_INTERVAL")

	if *showVersion {
		fmt.Printf("keystone %s (%s)\n", version.Version, version.Commit)
		return
	}

	// Root context with graceful shutdown
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// Create agent
	a := agent.New(agent.Options{HTTPAddr: *httpAddr})

	// Create adapter registry
	registry := adapter.NewRegistry()

	// Register HTTP adapter (enabled by default)
	if *httpAddr != "" {
		httpCfg := httpadapter.Config{Addr: *httpAddr}
		httpAdapter := httpadapter.New(httpCfg, a)
		registry.Register(httpAdapter)
		log.Printf("[main] HTTP adapter configured on %s", *httpAddr)
	}

	// Register NATS adapter (if configured)
	if *natsURL != "" {
		if *natsDeviceID == "" {
			// Try to get device ID from environment or generate one
			*natsDeviceID = os.Getenv("KEYSTONE_DEVICE_ID")
			if *natsDeviceID == "" {
				hostname, _ := os.Hostname()
				if hostname != "" {
					*natsDeviceID = hostname
				} else {
					*natsDeviceID = "keystone-agent"
				}
			}
		}

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
		if *mqttDeviceID == "" {
			// Try to get device ID from environment or generate one
			*mqttDeviceID = os.Getenv("KEYSTONE_DEVICE_ID")
			if *mqttDeviceID == "" {
				hostname, _ := os.Hostname()
				if hostname != "" {
					*mqttDeviceID = hostname
				} else {
					*mqttDeviceID = "keystone-agent"
				}
			}
		}

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
		if *mqttQoS >= 0 && *mqttQoS <= 2 {
			mqttCfg.CommandQoS = byte(*mqttQoS)
			mqttCfg.ResponseQoS = byte(*mqttQoS)
		}

		mqtt := mqttadapter.New(mqttCfg, a)
		registry.Register(mqtt)
		log.Printf("[main] MQTT adapter configured for %s (device: %s)", *mqttBroker, *mqttDeviceID)
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

	// Block until shutdown signal
	<-ctx.Done()
	log.Println("[main] shutdown signal received, draining...")

	// Graceful shutdown with timeout
	shutCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Stop all adapters
	if err := registry.StopAll(shutCtx); err != nil {
		log.Printf("[main] adapter shutdown error: %v", err)
	}

	// Close agent
	if err := a.Close(); err != nil {
		log.Printf("[main] agent close error: %v", err)
	}

	log.Println("[main] bye")
	_ = os.Stdout.Sync()
}
