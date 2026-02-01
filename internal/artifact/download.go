package artifact

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
)

// DownloadConfig holds configuration for robust artifact downloads.
type DownloadConfig struct {
	// Timeouts
	ConnectTimeout   time.Duration // Dial timeout (default: 30s)
	ReadTimeout      time.Duration // Per-read timeout (default: 60s)
	OverallTimeout   time.Duration // Total operation timeout (default: 30m)
	IdleTimeout      time.Duration // Idle connection timeout (default: 90s)
	TLSTimeout       time.Duration // TLS handshake timeout (default: 15s)
	KeepAlive        time.Duration // TCP keepalive interval (default: 30s)
	ExpectContinue   time.Duration // Expect 100-continue timeout (default: 1s)

	// Retry configuration
	MaxRetries       int           // Maximum retry attempts (default: 10)
	RetryMinWait     time.Duration // Minimum wait between retries (default: 1s)
	RetryMaxWait     time.Duration // Maximum wait between retries (default: 30s)
	RetryMultiplier  float64       // Backoff multiplier (default: 2.0)

	// Resume configuration
	EnableResume     bool          // Enable resume via Range headers (default: true)
	PartialSuffix    string        // Suffix for partial download files (default: ".partial")

	// Progress reporting
	ProgressInterval time.Duration // Interval for progress logging (default: 5s)
	EnableProgress   bool          // Enable progress logging (default: true)

	// HTTP options
	HTTPOptions      HTTPOptions   // Headers and tokens
}

// DefaultDownloadConfig returns a configuration suitable for edge deployments.
func DefaultDownloadConfig() DownloadConfig {
	return DownloadConfig{
		ConnectTimeout:   30 * time.Second,
		ReadTimeout:      60 * time.Second,
		OverallTimeout:   30 * time.Minute,
		IdleTimeout:      90 * time.Second,
		TLSTimeout:       15 * time.Second,
		KeepAlive:        30 * time.Second,
		ExpectContinue:   1 * time.Second,

		MaxRetries:      10,
		RetryMinWait:    1 * time.Second,
		RetryMaxWait:    30 * time.Second,
		RetryMultiplier: 2.0,

		EnableResume:     true,
		PartialSuffix:    ".partial",

		ProgressInterval: 5 * time.Second,
		EnableProgress:   true,
	}
}

// DownloadProgress tracks download progress.
type DownloadProgress struct {
	TotalBytes      int64
	DownloadedBytes int64
	ResumedFrom     int64
	StartTime       time.Time
	LastUpdate      time.Time
	Speed           float64 // bytes per second
	Attempts        int
}

// ProgressCallback is called periodically during download.
type ProgressCallback func(progress DownloadProgress)

// DownloadResult contains information about a completed download.
type DownloadResult struct {
	Path            string
	Size            int64
	SHA256          string
	Duration        time.Duration
	Attempts        int
	Resumed         bool
	ResumedFromByte int64
}

// progressWriter wraps an io.Writer to track bytes written.
type progressWriter struct {
	w       io.Writer
	written *int64
}

func (pw *progressWriter) Write(p []byte) (int, error) {
	n, err := pw.w.Write(p)
	atomic.AddInt64(pw.written, int64(n))
	return n, err
}

// DownloadWithResume downloads a file with resume support and robust retry logic.
func DownloadWithResume(ctx context.Context, destDir, uri string, cfg DownloadConfig) (*DownloadResult, error) {
	if err := os.MkdirAll(destDir, 0o755); err != nil {
		return nil, fmt.Errorf("create dest dir: %w", err)
	}

	u, err := url.Parse(uri)
	if err != nil {
		return nil, fmt.Errorf("parse URI: %w", err)
	}

	base := filepath.Base(u.Path)
	if base == "." || base == "/" || base == "" {
		base = "artifact.bin"
	}

	destPath := filepath.Join(destDir, base)
	partialPath := destPath + cfg.PartialSuffix

	// Create HTTP client with proper timeouts
	transport := &http.Transport{
		DialContext: (&net.Dialer{
			Timeout:   cfg.ConnectTimeout,
			KeepAlive: cfg.KeepAlive,
		}).DialContext,
		TLSHandshakeTimeout:   cfg.TLSTimeout,
		ResponseHeaderTimeout: cfg.ReadTimeout,
		IdleConnTimeout:       cfg.IdleTimeout,
		ExpectContinueTimeout: cfg.ExpectContinue,
		MaxIdleConns:          10,
		MaxIdleConnsPerHost:   5,
		MaxConnsPerHost:       10,
		ForceAttemptHTTP2:     true,
	}

	client := &http.Client{
		Transport: transport,
		Timeout:   0, // We handle timeout via context
	}

	// Apply overall timeout if configured
	var cancel context.CancelFunc
	if cfg.OverallTimeout > 0 {
		ctx, cancel = context.WithTimeout(ctx, cfg.OverallTimeout)
		defer cancel()
	}

	startTime := time.Now()
	var lastErr error
	var resumed bool
	var resumedFrom int64

	for attempt := 1; attempt <= cfg.MaxRetries; attempt++ {
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("download cancelled: %w", ctx.Err())
		default:
		}

		if attempt > 1 {
			// Calculate backoff with jitter
			backoff := calculateBackoff(attempt, cfg.RetryMinWait, cfg.RetryMaxWait, cfg.RetryMultiplier)
			log.Printf("[artifact] retry %d/%d for %s in %v (last error: %v)",
				attempt, cfg.MaxRetries, base, backoff, lastErr)

			select {
			case <-ctx.Done():
				return nil, fmt.Errorf("download cancelled during backoff: %w", ctx.Err())
			case <-time.After(backoff):
			}
		}

		// Check for existing partial file
		var startByte int64
		if cfg.EnableResume {
			if fi, err := os.Stat(partialPath); err == nil {
				startByte = fi.Size()
				log.Printf("[artifact] found partial file %s (%d bytes), attempting resume", base, startByte)
			}
		}

		result, err := downloadAttempt(ctx, client, uri, destPath, partialPath, startByte, attempt, cfg)
		if err == nil {
			result.Duration = time.Since(startTime)
			result.Resumed = startByte > 0
			result.ResumedFromByte = startByte
			return result, nil
		}

		lastErr = err

		// Check if error is retryable
		if !isRetryableError(err) {
			// Clean up partial file on fatal error
			_ = os.Remove(partialPath)
			return nil, fmt.Errorf("fatal download error (attempt %d): %w", attempt, err)
		}

		// Track if we resumed
		if startByte > 0 {
			resumed = true
			resumedFrom = startByte
		}
	}

	// All retries exhausted
	return nil, fmt.Errorf("download failed after %d attempts (resumed=%v, resumedFrom=%d): %w",
		cfg.MaxRetries, resumed, resumedFrom, lastErr)
}

// downloadAttempt performs a single download attempt.
func downloadAttempt(ctx context.Context, client *http.Client, uri, destPath, partialPath string, startByte int64, attempt int, cfg DownloadConfig) (*DownloadResult, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", uri, nil)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}

	// Attach custom headers
	attachRequestHeaders(req, cfg.HTTPOptions)

	// Request range if resuming
	if startByte > 0 && cfg.EnableResume {
		req.Header.Set("Range", fmt.Sprintf("bytes=%d-", startByte))
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("http request: %w", err)
	}
	defer resp.Body.Close()

	// Handle response status
	switch resp.StatusCode {
	case http.StatusOK:
		// Full content - start from beginning
		startByte = 0
	case http.StatusPartialContent:
		// Resume supported - continue from startByte
		if startByte == 0 {
			return nil, fmt.Errorf("unexpected 206 without range request")
		}
	case http.StatusRequestedRangeNotSatisfiable:
		// Range not satisfiable - file may be complete or corrupted
		// Remove partial and retry from scratch
		_ = os.Remove(partialPath)
		return nil, fmt.Errorf("range not satisfiable, retrying from scratch")
	case http.StatusTooManyRequests:
		// Rate limited - check Retry-After header
		if retryAfter := resp.Header.Get("Retry-After"); retryAfter != "" {
			if seconds, err := strconv.Atoi(retryAfter); err == nil {
				return nil, &rateLimitError{retryAfter: time.Duration(seconds) * time.Second}
			}
		}
		return nil, fmt.Errorf("rate limited (429)")
	default:
		if resp.StatusCode >= 400 && resp.StatusCode < 500 {
			// Client error - not retryable
			return nil, &fatalError{fmt.Errorf("http client error: %s", resp.Status)}
		}
		if resp.StatusCode >= 500 {
			// Server error - retryable
			return nil, fmt.Errorf("http server error: %s", resp.Status)
		}
		return nil, fmt.Errorf("unexpected http status: %s", resp.Status)
	}

	// Determine expected size
	var expectedSize int64 = -1
	if cl := resp.Header.Get("Content-Length"); cl != "" {
		if size, err := strconv.ParseInt(cl, 10, 64); err == nil {
			expectedSize = size + startByte
		}
	}

	// Open partial file for writing
	var out *os.File
	if startByte > 0 && resp.StatusCode == http.StatusPartialContent {
		// Append to existing partial
		out, err = os.OpenFile(partialPath, os.O_APPEND|os.O_WRONLY, 0o644)
		if err != nil {
			return nil, fmt.Errorf("open partial file for append: %w", err)
		}
	} else {
		// Create new file (truncate if exists)
		out, err = os.OpenFile(partialPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
		if err != nil {
			return nil, fmt.Errorf("create partial file: %w", err)
		}
		startByte = 0
	}
	defer out.Close()

	// Setup progress tracking
	var written int64
	pw := &progressWriter{w: out, written: &written}

	// Progress reporter goroutine
	if cfg.EnableProgress && cfg.ProgressInterval > 0 {
		progressCtx, progressCancel := context.WithCancel(ctx)
		defer progressCancel()

		go func() {
			ticker := time.NewTicker(cfg.ProgressInterval)
			defer ticker.Stop()
			lastBytes := int64(0)
			lastTime := time.Now()

			for {
				select {
				case <-progressCtx.Done():
					return
				case <-ticker.C:
					currentBytes := atomic.LoadInt64(&written) + startByte
					now := time.Now()
					elapsed := now.Sub(lastTime).Seconds()
					speed := float64(currentBytes-lastBytes-startByte) / elapsed

					var progress string
					if expectedSize > 0 {
						pct := float64(currentBytes) / float64(expectedSize) * 100
						progress = fmt.Sprintf("[artifact] downloading: %.1f%% (%s/%s) @ %s/s",
							pct, formatBytes(currentBytes), formatBytes(expectedSize), formatBytes(int64(speed)))
					} else {
						progress = fmt.Sprintf("[artifact] downloading: %s @ %s/s",
							formatBytes(currentBytes), formatBytes(int64(speed)))
					}
					log.Print(progress)

					lastBytes = currentBytes - startByte
					lastTime = now
				}
			}
		}()
	}

	// Copy with timeout-aware reader
	reader := &timeoutReader{
		r:       resp.Body,
		timeout: cfg.ReadTimeout,
		ctx:     ctx,
	}

	n, err := io.Copy(pw, reader)
	if err != nil {
		return nil, fmt.Errorf("download copy: %w", err)
	}

	// Verify size if known
	totalSize := startByte + n
	if expectedSize > 0 && totalSize != expectedSize {
		return nil, fmt.Errorf("size mismatch: got %d, expected %d", totalSize, expectedSize)
	}

	// Close file before rename
	if err := out.Close(); err != nil {
		return nil, fmt.Errorf("close partial file: %w", err)
	}

	// Atomic rename to final destination
	if err := os.Rename(partialPath, destPath); err != nil {
		return nil, fmt.Errorf("rename to final: %w", err)
	}

	// Calculate SHA256
	sha, err := calculateSHA256(destPath)
	if err != nil {
		log.Printf("[artifact] warning: could not calculate SHA256: %v", err)
	}

	log.Printf("[artifact] download complete: %s (%s)", filepath.Base(destPath), formatBytes(totalSize))

	return &DownloadResult{
		Path:     destPath,
		Size:     totalSize,
		SHA256:   sha,
		Attempts: attempt,
	}, nil
}

// timeoutReader wraps a reader with per-read timeout.
type timeoutReader struct {
	r       io.Reader
	timeout time.Duration
	ctx     context.Context
}

func (tr *timeoutReader) Read(p []byte) (int, error) {
	// Check context first
	select {
	case <-tr.ctx.Done():
		return 0, tr.ctx.Err()
	default:
	}

	// Read with deadline - note: http.Response.Body already respects context
	return tr.r.Read(p)
}

// calculateBackoff returns the backoff duration with jitter.
func calculateBackoff(attempt int, minWait, maxWait time.Duration, multiplier float64) time.Duration {
	// Exponential backoff
	backoff := float64(minWait) * pow(multiplier, float64(attempt-1))

	// Add jitter (up to 25%)
	jitter := backoff * 0.25 * (float64(time.Now().UnixNano()%100) / 100)
	backoff += jitter

	if backoff > float64(maxWait) {
		backoff = float64(maxWait)
	}

	return time.Duration(backoff)
}

func pow(base, exp float64) float64 {
	result := 1.0
	for i := 0; i < int(exp); i++ {
		result *= base
	}
	return result
}

// isRetryableError checks if an error should trigger a retry.
func isRetryableError(err error) bool {
	if err == nil {
		return false
	}

	// Fatal errors should not retry
	var fatal *fatalError
	if errors.As(err, &fatal) {
		return false
	}

	// Rate limit errors are retryable
	var rateLimit *rateLimitError
	if errors.As(err, &rateLimit) {
		return true
	}

	// Context cancelled is not retryable
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}

	// Network errors are generally retryable
	var netErr net.Error
	if errors.As(err, &netErr) {
		return true
	}

	// Connection errors
	errStr := err.Error()
	retryablePatterns := []string{
		"connection reset",
		"connection refused",
		"connection timed out",
		"no such host",
		"temporary failure",
		"server error",
		"EOF",
		"broken pipe",
		"network is unreachable",
		"i/o timeout",
	}
	for _, pattern := range retryablePatterns {
		if strings.Contains(strings.ToLower(errStr), pattern) {
			return true
		}
	}

	return true // Default to retryable for unknown errors
}

// fatalError represents an error that should not be retried.
type fatalError struct {
	err error
}

func (e *fatalError) Error() string {
	return e.err.Error()
}

func (e *fatalError) Unwrap() error {
	return e.err
}

// rateLimitError represents a rate limiting error.
type rateLimitError struct {
	retryAfter time.Duration
}

func (e *rateLimitError) Error() string {
	return fmt.Sprintf("rate limited, retry after %v", e.retryAfter)
}

// calculateSHA256 calculates the SHA256 hash of a file.
func calculateSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()

	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}

	return hex.EncodeToString(h.Sum(nil)), nil
}

// formatBytes formats bytes as human-readable string.
func formatBytes(b int64) string {
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%d B", b)
	}
	div, exp := int64(unit), 0
	for n := b / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(b)/float64(div), "KMGTPE"[exp])
}
