package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/carlosprados/keystone/internal/agent"
	"github.com/carlosprados/keystone/internal/version"
)

func main() {
	// Flags kept minimal for the MVP to avoid extra deps
	httpAddr := flag.String("http", ":8080", "HTTP listen address for local API and health endpoints")
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

	// Start agent with HTTP health endpoint
	a := agent.New(agent.Options{HTTPAddr: *httpAddr})

	// HTTP server lifecycle
	srv := &http.Server{Addr: *httpAddr, Handler: a.Router()}

	go func() {
		log.Printf("[main] keystone starting addr=%s version=%s", *httpAddr, version.Version)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("[main] http server error: %v", err)
		}
	}()

	// If requested, run the internal demo stack
	if *demo {
		go func() {
			if err := a.StartDemo(); err != nil {
				log.Printf("demo start error: %v", err)
			}
		}()
	}

	// Block until shutdown signal
	<-ctx.Done()
	log.Println("[main] shutdown signal received, draining...")

	// Graceful HTTP shutdown with timeout
	shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutCtx); err != nil {
		log.Printf("[main] http shutdown error: %v", err)
	}

	if err := a.Close(); err != nil {
		log.Printf("[main] agent close error: %v", err)
	}

	log.Println("[main] bye")
	_ = os.Stdout.Sync()
}
