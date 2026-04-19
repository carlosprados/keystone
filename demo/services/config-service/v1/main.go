package main

import (
	"encoding/json"
	"log"
	"net/http"
	"os"
	"time"
)

const version = "1.0.0"

type config struct {
	Version   string `json:"version"`
	RateMs    int    `json:"rate_ms"`
	Greeting  string `json:"greeting"`
	BatchSize int    `json:"batch_size"`
	Enriched  bool   `json:"enriched"`
}

func main() {
	addr := env("CONFIG_ADDR", ":7001")

	cfg := config{
		Version:   version,
		RateMs:    1000,
		Greeting:  "Hola equipo — config v1",
		BatchSize: 5,
		Enriched:  false,
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	mux.HandleFunc("/config", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(cfg)
	})

	log.Printf("[config-service v%s] listening on %s (rate_ms=%d, batch=%d)", version, addr, cfg.RateMs, cfg.BatchSize)
	srv := &http.Server{Addr: addr, Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	if err := srv.ListenAndServe(); err != nil {
		log.Fatalf("config-service: %v", err)
	}
}

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
