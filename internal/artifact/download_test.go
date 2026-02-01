package artifact

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

func TestDefaultDownloadConfig(t *testing.T) {
	cfg := DefaultDownloadConfig()

	if cfg.ConnectTimeout != 30*time.Second {
		t.Errorf("expected connect timeout 30s, got %v", cfg.ConnectTimeout)
	}
	if cfg.MaxRetries != 10 {
		t.Errorf("expected max retries 10, got %d", cfg.MaxRetries)
	}
	if !cfg.EnableResume {
		t.Error("expected resume to be enabled by default")
	}
	if cfg.PartialSuffix != ".partial" {
		t.Errorf("expected partial suffix '.partial', got %q", cfg.PartialSuffix)
	}
}

func TestDownloadWithResume_BasicDownload(t *testing.T) {
	// Create a test server that serves a simple file
	content := []byte("Hello, World! This is test content for download.")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", fmt.Sprintf("%d", len(content)))
		w.WriteHeader(http.StatusOK)
		w.Write(content)
	}))
	defer server.Close()

	// Create temp directory
	tmpDir := t.TempDir()

	cfg := DefaultDownloadConfig()
	cfg.EnableProgress = false // Disable for tests
	cfg.OverallTimeout = 10 * time.Second

	ctx := context.Background()
	result, err := DownloadWithResume(ctx, tmpDir, server.URL+"/test.bin", cfg)
	if err != nil {
		t.Fatalf("download failed: %v", err)
	}

	if result.Size != int64(len(content)) {
		t.Errorf("expected size %d, got %d", len(content), result.Size)
	}

	// Verify file content
	data, err := os.ReadFile(result.Path)
	if err != nil {
		t.Fatalf("failed to read downloaded file: %v", err)
	}
	if string(data) != string(content) {
		t.Errorf("content mismatch: got %q, want %q", string(data), string(content))
	}
}

func TestDownloadWithResume_ResumeSupport(t *testing.T) {
	content := []byte("0123456789ABCDEFGHIJ") // 20 bytes
	var requestCount int32

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&requestCount, 1)

		rangeHeader := r.Header.Get("Range")
		if rangeHeader != "" {
			// Parse range header
			var start int
			fmt.Sscanf(rangeHeader, "bytes=%d-", &start)

			if start > 0 && start < len(content) {
				w.Header().Set("Content-Length", fmt.Sprintf("%d", len(content)-start))
				w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, len(content)-1, len(content)))
				w.WriteHeader(http.StatusPartialContent)
				w.Write(content[start:])
				return
			}
		}

		// Full content
		w.Header().Set("Content-Length", fmt.Sprintf("%d", len(content)))
		w.Header().Set("Accept-Ranges", "bytes")
		w.WriteHeader(http.StatusOK)
		w.Write(content)
	}))
	defer server.Close()

	tmpDir := t.TempDir()

	// Create a partial file
	partialPath := filepath.Join(tmpDir, "test.bin.partial")
	partialContent := content[:10] // First 10 bytes
	if err := os.WriteFile(partialPath, partialContent, 0644); err != nil {
		t.Fatalf("failed to create partial file: %v", err)
	}

	cfg := DefaultDownloadConfig()
	cfg.EnableProgress = false
	cfg.OverallTimeout = 10 * time.Second

	ctx := context.Background()
	result, err := DownloadWithResume(ctx, tmpDir, server.URL+"/test.bin", cfg)
	if err != nil {
		t.Fatalf("download failed: %v", err)
	}

	if !result.Resumed {
		t.Error("expected download to be resumed")
	}

	if result.ResumedFromByte != 10 {
		t.Errorf("expected resume from byte 10, got %d", result.ResumedFromByte)
	}

	// Verify complete file
	data, err := os.ReadFile(result.Path)
	if err != nil {
		t.Fatalf("failed to read file: %v", err)
	}
	if string(data) != string(content) {
		t.Errorf("content mismatch: got %q, want %q", string(data), string(content))
	}
}

func TestDownloadWithResume_Retry(t *testing.T) {
	var requestCount int32
	content := []byte("Test content for retry")

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		count := atomic.AddInt32(&requestCount, 1)

		// Fail first 2 requests, succeed on 3rd
		if count < 3 {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Length", fmt.Sprintf("%d", len(content)))
		w.WriteHeader(http.StatusOK)
		w.Write(content)
	}))
	defer server.Close()

	tmpDir := t.TempDir()

	cfg := DefaultDownloadConfig()
	cfg.EnableProgress = false
	cfg.OverallTimeout = 30 * time.Second
	cfg.RetryMinWait = 100 * time.Millisecond
	cfg.RetryMaxWait = 500 * time.Millisecond

	ctx := context.Background()
	result, err := DownloadWithResume(ctx, tmpDir, server.URL+"/test.bin", cfg)
	if err != nil {
		t.Fatalf("download failed: %v", err)
	}

	if result.Attempts != 3 {
		t.Errorf("expected 3 attempts, got %d", result.Attempts)
	}

	if atomic.LoadInt32(&requestCount) != 3 {
		t.Errorf("expected 3 requests, got %d", atomic.LoadInt32(&requestCount))
	}
}

func TestDownloadWithResume_FatalError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound) // 404 - fatal error
	}))
	defer server.Close()

	tmpDir := t.TempDir()

	cfg := DefaultDownloadConfig()
	cfg.EnableProgress = false
	cfg.MaxRetries = 3
	cfg.RetryMinWait = 50 * time.Millisecond

	ctx := context.Background()
	_, err := DownloadWithResume(ctx, tmpDir, server.URL+"/notfound.bin", cfg)
	if err == nil {
		t.Fatal("expected error for 404, got nil")
	}

	// Should fail immediately without retries for 4xx
	if !contains(err.Error(), "fatal") && !contains(err.Error(), "client error") {
		t.Errorf("expected fatal/client error, got: %v", err)
	}
}

func TestDownloadWithResume_Timeout(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(5 * time.Second) // Slow response
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	tmpDir := t.TempDir()

	cfg := DefaultDownloadConfig()
	cfg.EnableProgress = false
	cfg.OverallTimeout = 500 * time.Millisecond
	cfg.ConnectTimeout = 200 * time.Millisecond
	cfg.ReadTimeout = 200 * time.Millisecond

	ctx := context.Background()
	_, err := DownloadWithResume(ctx, tmpDir, server.URL+"/slow.bin", cfg)
	if err == nil {
		t.Fatal("expected timeout error, got nil")
	}
}

func TestDownloadWithResume_RateLimit(t *testing.T) {
	var requestCount int32
	content := []byte("Rate limited content")

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		count := atomic.AddInt32(&requestCount, 1)

		// Rate limit first request
		if count == 1 {
			w.Header().Set("Retry-After", "1")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}

		w.Header().Set("Content-Length", fmt.Sprintf("%d", len(content)))
		w.WriteHeader(http.StatusOK)
		w.Write(content)
	}))
	defer server.Close()

	tmpDir := t.TempDir()

	cfg := DefaultDownloadConfig()
	cfg.EnableProgress = false
	cfg.OverallTimeout = 10 * time.Second
	cfg.RetryMinWait = 100 * time.Millisecond

	ctx := context.Background()
	result, err := DownloadWithResume(ctx, tmpDir, server.URL+"/ratelimit.bin", cfg)
	if err != nil {
		t.Fatalf("download failed: %v", err)
	}

	if result.Attempts < 2 {
		t.Errorf("expected at least 2 attempts due to rate limit, got %d", result.Attempts)
	}
}

func TestIsRetryableError(t *testing.T) {
	tests := []struct {
		name      string
		err       error
		retryable bool
	}{
		{"nil error", nil, false},
		{"fatal error", &fatalError{fmt.Errorf("not found")}, false},
		{"rate limit", &rateLimitError{retryAfter: time.Second}, true},
		{"context cancelled", context.Canceled, false},
		{"deadline exceeded", context.DeadlineExceeded, false},
		{"connection reset", fmt.Errorf("connection reset by peer"), true},
		{"server error", fmt.Errorf("http server error: 503"), true},
		{"EOF", fmt.Errorf("unexpected EOF"), true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isRetryableError(tt.err)
			if got != tt.retryable {
				t.Errorf("isRetryableError(%v) = %v, want %v", tt.err, got, tt.retryable)
			}
		})
	}
}

func TestCalculateBackoff(t *testing.T) {
	minWait := 1 * time.Second
	maxWait := 30 * time.Second
	multiplier := 2.0

	// First attempt should be close to minWait
	backoff1 := calculateBackoff(1, minWait, maxWait, multiplier)
	if backoff1 < minWait || backoff1 > minWait+minWait/4 {
		t.Errorf("first backoff %v outside expected range [%v, %v]", backoff1, minWait, minWait+minWait/4)
	}

	// Later attempts should increase
	backoff5 := calculateBackoff(5, minWait, maxWait, multiplier)
	if backoff5 <= backoff1 {
		t.Errorf("backoff should increase: attempt 1=%v, attempt 5=%v", backoff1, backoff5)
	}

	// Should not exceed maxWait
	backoff10 := calculateBackoff(10, minWait, maxWait, multiplier)
	if backoff10 > maxWait+maxWait/4 {
		t.Errorf("backoff %v exceeds max %v", backoff10, maxWait)
	}
}

func TestFormatBytes(t *testing.T) {
	tests := []struct {
		bytes    int64
		expected string
	}{
		{0, "0 B"},
		{100, "100 B"},
		{1024, "1.0 KB"},
		{1536, "1.5 KB"},
		{1048576, "1.0 MB"},
		{1073741824, "1.0 GB"},
	}

	for _, tt := range tests {
		got := formatBytes(tt.bytes)
		if got != tt.expected {
			t.Errorf("formatBytes(%d) = %q, want %q", tt.bytes, got, tt.expected)
		}
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > 0 && containsAt(s, substr, 0))
}

func containsAt(s, substr string, start int) bool {
	for i := start; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
