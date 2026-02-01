package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/carlosprados/keystone/internal/adapter"
	httpadapter "github.com/carlosprados/keystone/internal/adapter/http"
	natsadapter "github.com/carlosprados/keystone/internal/adapter/nats"
	"github.com/carlosprados/keystone/internal/agent"
	"github.com/carlosprados/keystone/internal/version"
)

func main() {
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

	// General flags
	demo := flag.Bool("demo", false, "Run a built-in demo: start a mock 3-component stack")
	showVersion := flag.Bool("version", false, "Print version and exit")
	flag.Parse()

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
