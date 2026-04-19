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

type producerEvent struct {
	ID              uint64    `json:"id"`
	Greeting        string    `json:"greeting"`
	Ts              time.Time `json:"ts"`
	ProducerVersion string    `json:"producer_version"`
	Hostname        string    `json:"hostname,omitempty"`
	Enriched        bool      `json:"enriched,omitempty"`
}

type producerResponse struct {
	ProducerVersion string          `json:"producer_version"`
	Events          []producerEvent `json:"events"`
}

type stats struct {
	mu         sync.Mutex
	lastSeen   uint64
	last       producerEvent
	count      atomic.Uint64
	enriched   atomic.Uint64
	lastConfig remoteConfig
}

func main() {
	addr := env("CONSUMER_ADDR", ":7003")
	configURL := env("CONFIG_URL", "http://localhost:7001/config")
	producerURL := env("PRODUCER_URL", "http://localhost:7002/events")

	cfg := waitForConfig(configURL, 30*time.Second)
	log.Printf("[consumer v%s] config loaded: rate_ms=%d enriched=%v", version, cfg.RateMs, cfg.Enriched)

	s := &stats{lastConfig: cfg}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go consume(ctx, producerURL, cfg, s)

	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	mux.HandleFunc("/stats", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		s.mu.Lock()
		last := s.last
		lastCfg := s.lastConfig
		s.mu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]any{
			"consumer_version":  version,
			"events_processed":  s.count.Load(),
			"enriched_observed": s.enriched.Load(),
			"last_event":        last,
			"upstream_config":   lastCfg,
		})
	})

	log.Printf("[consumer v%s] listening on %s", version, addr)
	srv := &http.Server{Addr: addr, Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	if err := srv.ListenAndServe(); err != nil {
		log.Fatalf("consumer: %v", err)
	}
}

func consume(ctx context.Context, url string, cfg remoteConfig, s *stats) {
	tick := time.NewTicker(time.Duration(cfg.RateMs) * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			resp, err := fetchEvents(url)
			if err != nil {
				log.Printf("[consumer v%s] fetch error: %v", version, err)
				continue
			}
			for _, e := range resp.Events {
				s.mu.Lock()
				if e.ID > s.lastSeen {
					s.lastSeen = e.ID
					s.last = e
					s.count.Add(1)
					if e.Enriched {
						s.enriched.Add(1)
					}
				}
				s.mu.Unlock()
			}
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
		log.Printf("[consumer v%s] waiting for config at %s: %v", version, url, err)
		time.Sleep(1 * time.Second)
	}
	log.Fatalf("consumer: config-service unreachable after %s", timeout)
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

func fetchEvents(url string) (producerResponse, error) {
	client := &http.Client{Timeout: 2 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		return producerResponse{}, err
	}
	defer resp.Body.Close()
	var r producerResponse
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
		return producerResponse{}, err
	}
	return r, nil
}

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
