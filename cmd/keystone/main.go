package main

import (
    "context"
    "flag"
    "fmt"
    "net/http"
    "os"
    "os/signal"
    "syscall"
    "time"

    "github.com/carlosprados/keystone/internal/agent"
    "github.com/carlosprados/keystone/internal/version"
    "github.com/rs/zerolog"
    "github.com/rs/zerolog/log"
)

func main() {
    // Flags kept minimal for the MVP to avoid extra deps
    httpAddr := flag.String("http", ":8080", "HTTP listen address for local API and health endpoints")
    demo := flag.Bool("demo", false, "Run a built-in demo: start a mock 3-component stack")
    applyPlan := flag.String("apply", "", "Apply a deployment plan file (TOML) and run components")
    dryRun := flag.Bool("dry-run", false, "When used with --apply, compute order and do not start components")
    showVersion := flag.Bool("version", false, "Print version and exit")
    flag.Parse()

    if *showVersion {
        fmt.Printf("keystone %s (%s)\n", version.Version, version.Commit)
        return
    }

    // Logger setup (zerolog)
    if isatty() {
        // Human-friendly console output
        log.Logger = log.Output(zerolog.ConsoleWriter{Out: os.Stdout, TimeFormat: time.RFC3339})
    }
    zerolog.TimeFieldFormat = time.RFC3339

    // Root context with graceful shutdown
    ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
    defer stop()

    // Start agent with HTTP health endpoint
    a := agent.New(agent.Options{HTTPAddr: *httpAddr, DryRun: *dryRun})

    // HTTP server lifecycle
    srv := &http.Server{Addr: *httpAddr, Handler: a.Router()}

    go func() {
        log.Info().Str("addr", *httpAddr).Str("version", version.Version).Msg("keystone starting")
        if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
            log.Fatal().Err(err).Msg("http server error")
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

    if *applyPlan != "" {
        go func() {
            if err := a.ApplyPlan(*applyPlan); err != nil {
                log.Error().Err(err).Str("plan", *applyPlan).Msg("apply failed")
            } else {
                log.Info().Str("plan", *applyPlan).Msg("apply completed")
            }
        }()
    }

    // Block until shutdown signal
    <-ctx.Done()
    log.Info().Msg("shutdown signal received, draining...")

    // Graceful HTTP shutdown with timeout
    shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
    defer cancel()
    if err := srv.Shutdown(shutCtx); err != nil {
        log.Error().Err(err).Msg("http shutdown error")
    }

    if err := a.Close(); err != nil {
        log.Error().Err(err).Msg("agent close error")
    }

    log.Info().Msg("bye")
    _ = os.Stdout.Sync()
}

// isatty returns true if stdout is a TTY; best-effort without extra deps.
func isatty() bool {
    fi, err := os.Stdout.Stat()
    if err != nil { return false }
    return (fi.Mode() & os.ModeCharDevice) != 0
}
