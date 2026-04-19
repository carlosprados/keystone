package main

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"os"
	"sync"
	"sync/atomic"
	"time"
)

const version = "2.0.0"

type remoteConfig struct {
	Version   string `json:"version"`
	RateMs    int    `json:"rate_ms"`
	Greeting  string `json:"greeting"`
	BatchSize int    `json:"batch_size"`
	Enriched  bool   `json:"enriched"`
}

type event struct {
	ID              uint64    `json:"id"`
	Greeting        string    `json:"greeting"`
	Timestamp       time.Time `json:"ts"`
	ProducerVersion string    `json:"producer_version"`
	Hostname        string    `json:"hostname,omitempty"`
	Enriched        bool      `json:"enriched,omitempty"`
}

type buffer struct {
	mu     sync.Mutex
	data   []event
	capN   int
	nextID uint64
}

func (b *buffer) push(e event) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.data = append(b.data, e)
	if len(b.data) > b.capN {
		b.data = b.data[len(b.data)-b.capN:]
	}
}

func (b *buffer) snapshot() []event {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([]event, len(b.data))
	copy(out, b.data)
	return out
}

var totalEmitted atomic.Uint64

func main() {
	addr := env("PRODUCER_ADDR", ":7002")
	configURL := env("CONFIG_URL", "http://localhost:7001/config")

	cfg := waitForConfig(configURL, 30*time.Second)
	log.Printf("[producer v%s] config loaded: rate_ms=%d batch=%d enriched=%v", version, cfg.RateMs, cfg.BatchSize, cfg.Enriched)

	buf := &buffer{capN: 100}
	host, _ := os.Hostname()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go produce(ctx, buf, cfg, host)

	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	mux.HandleFunc("/events", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"producer_version": version,
			"events":           buf.snapshot(),
		})
	})
	mux.HandleFunc("/metrics", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"producer_version": version,
			"total_emitted":    totalEmitted.Load(),
			"buffer_size":      len(buf.snapshot()),
		})
	})

	log.Printf("[producer v%s] listening on %s", version, addr)
	srv := &http.Server{Addr: addr, Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	if err := srv.ListenAndServe(); err != nil {
		log.Fatalf("producer: %v", err)
	}
}

func produce(ctx context.Context, buf *buffer, cfg remoteConfig, host string) {
	tick := time.NewTicker(time.Duration(cfg.RateMs) * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			buf.mu.Lock()
			buf.nextID++
			id := buf.nextID
			buf.mu.Unlock()
			e := event{
				ID:              id,
				Greeting:        cfg.Greeting,
				Timestamp:       time.Now(),
				ProducerVersion: version,
				Hostname:        host,
				Enriched:        cfg.Enriched,
			}
			buf.push(e)
			totalEmitted.Add(1)
		}
	}
}

func waitForConfig(url string, timeout time.Duration) remoteConfig {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		cfg, err := fetchConfig(url)
		if err == nil {
			return cfg
		}
		log.Printf("[producer v%s] waiting for config at %s: %v", version, url, err)
		time.Sleep(1 * time.Second)
	}
	log.Fatalf("producer: config-service unreachable after %s", timeout)
	return remoteConfig{}
}

func fetchConfig(url string) (remoteConfig, error) {
	client := &http.Client{Timeout: 2 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		return remoteConfig{}, err
	}
	defer resp.Body.Close()
	var cfg remoteConfig
	if err := json.NewDecoder(resp.Body).Decode(&cfg); err != nil {
		return remoteConfig{}, err
	}
	return cfg, nil
}

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
