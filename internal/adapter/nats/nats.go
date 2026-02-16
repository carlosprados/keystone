// Package nats provides a NATS adapter for the Keystone agent.
// It enables asynchronous communication with a control plane via NATS messaging.
package nats

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"sync"
	"time"

	"github.com/carlosprados/keystone/internal/adapter"
	"github.com/nats-io/nats.go"
)

// Adapter implements the NATS control plane adapter.
type Adapter struct {
	cfg      Config
	handler  adapter.CommandHandler
	subjects *Subjects

	mu   sync.RWMutex
	nc   *nats.Conn
	subs []*nats.Subscription

	// JetStream
	jsManager *jetStreamManager

	// Lifecycle
	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
}

// Config holds the configuration for the NATS adapter.
type Config struct {
	// URL is the NATS server URL (e.g., "nats://localhost:4222").
	URL string

	// DeviceID is the unique identifier for this agent.
	// Used in subject prefixes for multi-tenancy.
	DeviceID string

	// TLS configuration (optional).
	TLSCert   string // Path to client certificate
	TLSKey    string // Path to client key
	TLSCA     string // Path to CA certificate
	TLSVerify bool   // Verify server certificate (default: true when TLS enabled)

	// Authentication (optional, mutually exclusive).
	// Priority: NKey > Credentials > Token > UserPass
	CredentialsFile string // Path to NATS credentials file (.creds)
	NKeyFile        string // Path to NKey seed file
	Token           string // Authentication token
	Username        string // Username for user/pass auth
	Password        string // Password for user/pass auth

	// Reconnect settings
	MaxReconnects   int           // Max reconnection attempts (-1 for infinite)
	ReconnectWait   time.Duration // Wait between reconnects
	ReconnectJitter time.Duration // Random jitter added to reconnect wait

	// Timeouts
	ConnectTimeout time.Duration // Connection timeout
	RequestTimeout time.Duration // Default timeout for request/reply

	// Event publishing
	PublishStateInterval  time.Duration // Interval for state events (0 to disable)
	PublishHealthInterval time.Duration // Interval for health events (0 to disable)

	// JetStream configuration for persistent job queue
	JetStream JetStreamConfig
}

// DefaultConfig returns a Config with sensible defaults.
func DefaultConfig() Config {
	return Config{
		URL:                   "nats://localhost:4222",
		DeviceID:              "keystone-agent",
		MaxReconnects:         -1, // Infinite
		ReconnectWait:         2 * time.Second,
		ReconnectJitter:       500 * time.Millisecond,
		ConnectTimeout:        5 * time.Second,
		RequestTimeout:        30 * time.Second,
		PublishStateInterval:  10 * time.Second,
		PublishHealthInterval: 30 * time.Second,
		JetStream:             DefaultJetStreamConfig(),
	}
}

// New creates a new NATS adapter.
func New(cfg Config, handler adapter.CommandHandler) *Adapter {
	return &Adapter{
		cfg:      cfg,
		handler:  handler,
		subjects: NewSubjects(cfg.DeviceID),
		subs:     make([]*nats.Subscription, 0),
	}
}

// Name returns the adapter identifier.
func (a *Adapter) Name() string {
	return "nats"
}

// Start connects to NATS and sets up subscriptions.
func (a *Adapter) Start(ctx context.Context) error {
	a.ctx, a.cancel = context.WithCancel(ctx)

	opts := []nats.Option{
		nats.Name(fmt.Sprintf("keystone-agent-%s", a.cfg.DeviceID)),
		nats.MaxReconnects(a.cfg.MaxReconnects),
		nats.ReconnectWait(a.cfg.ReconnectWait),
		nats.ReconnectJitter(a.cfg.ReconnectJitter, a.cfg.ReconnectJitter),
		nats.Timeout(a.cfg.ConnectTimeout),
		nats.DisconnectErrHandler(func(nc *nats.Conn, err error) {
			if err != nil {
				log.Printf("[nats] disconnected: %v", err)
			}
		}),
		nats.ReconnectHandler(func(nc *nats.Conn) {
			log.Printf("[nats] reconnected to %s", nc.ConnectedUrl())
		}),
		nats.ClosedHandler(func(nc *nats.Conn) {
			log.Printf("[nats] connection closed")
		}),
		nats.ErrorHandler(func(nc *nats.Conn, sub *nats.Subscription, err error) {
			log.Printf("[nats] error: %v", err)
		}),
	}

	// TLS configuration (mTLS or server verification only)
	if a.cfg.TLSCert != "" || a.cfg.TLSCA != "" || a.cfg.TLSVerify {
		tlsCfg, err := a.buildTLSConfig()
		if err != nil {
			return fmt.Errorf("failed to configure TLS: %w", err)
		}
		opts = append(opts, nats.Secure(tlsCfg))
	}

	// Authentication options
	authOpts := a.buildAuthOptions()
	opts = append(opts, authOpts...)

	// Connect
	nc, err := nats.Connect(a.cfg.URL, opts...)
	if err != nil {
		return fmt.Errorf("failed to connect to NATS: %w", err)
	}

	a.mu.Lock()
	a.nc = nc
	a.mu.Unlock()

	log.Printf("[nats] connected to %s as device %s", a.cfg.URL, a.cfg.DeviceID)

	// Set up command subscriptions
	if err := a.setupSubscriptions(); err != nil {
		nc.Close()
		return fmt.Errorf("failed to setup subscriptions: %w", err)
	}

	// Set up JetStream for persistent job queue (if enabled)
	if err := a.setupJetStream(ctx); err != nil {
		log.Printf("[nats] JetStream setup failed (continuing without): %v", err)
		// Don't fail startup - JetStream is optional
	}

	// Start event publishers
	if a.cfg.PublishStateInterval > 0 {
		a.wg.Add(1)
		go a.publishStateLoop()
	}
	if a.cfg.PublishHealthInterval > 0 {
		a.wg.Add(1)
		go a.publishHealthLoop()
	}

	// Start JetStream job processor (if enabled)
	a.startJobProcessor(a.ctx)

	return nil
}

// Stop gracefully shuts down the NATS adapter.
func (a *Adapter) Stop(ctx context.Context) error {
	log.Printf("[nats] stopping NATS adapter")

	// Cancel background goroutines
	if a.cancel != nil {
		a.cancel()
	}

	// Wait for goroutines to finish
	done := make(chan struct{})
	go func() {
		a.wg.Wait()
		close(done)
	}()

	select {
	case <-done:
	case <-ctx.Done():
		log.Printf("[nats] stop timed out waiting for goroutines")
	}

	// Unsubscribe and close
	a.mu.Lock()
	defer a.mu.Unlock()

	for _, sub := range a.subs {
		_ = sub.Unsubscribe()
	}
	a.subs = nil

	if a.nc != nil {
		a.nc.Close()
		a.nc = nil
	}

	return nil
}

// setupSubscriptions creates all command subscriptions.
func (a *Adapter) setupSubscriptions() error {
	subs := []struct {
		subject string
		handler nats.MsgHandler
	}{
		{a.subjects.CmdApply, a.handleApply},
		{a.subjects.CmdStop, a.handleStop},
		{a.subjects.CmdStatus, a.handleStatus},
		{a.subjects.CmdGraph, a.handleGraph},
		{a.subjects.CmdRestart, a.handleRestart},
		{a.subjects.CmdStopComp, a.handleStopComponent},
		{a.subjects.CmdHealth, a.handleHealth},
		{a.subjects.CmdRecipes, a.handleRecipes},
		{a.subjects.CmdAddRecipe, a.handleAddRecipe},
	}

	a.mu.Lock()
	defer a.mu.Unlock()

	for _, s := range subs {
		sub, err := a.nc.Subscribe(s.subject, s.handler)
		if err != nil {
			return fmt.Errorf("failed to subscribe to %s: %w", s.subject, err)
		}
		a.subs = append(a.subs, sub)
		log.Printf("[nats] subscribed to %s", s.subject)
	}

	return nil
}

// Command handlers

func (a *Adapter) handleApply(msg *nats.Msg) {
	var req ApplyRequest
	if err := json.Unmarshal(msg.Data, &req); err != nil {
		a.respond(msg, NewErrorResponse(fmt.Errorf("invalid request: %w", err)))
		return
	}

	log.Printf("[nats] cmd.apply planPath=%s dry=%v", req.PlanPath, req.Dry)

	var err error
	if req.PlanPath != "" {
		err = a.handler.ApplyPlan(req.PlanPath, req.Dry)
	} else if req.Content != "" {
		err = a.handler.ApplyPlanContent(req.Content, req.Dry)
	} else {
		err = fmt.Errorf("planPath or content required")
	}

	if err != nil {
		a.respond(msg, NewErrorResponse(err))
		return
	}

	a.respond(msg, NewSuccessResponse(a.handler.GetPlanStatus()))
}

func (a *Adapter) handleStop(msg *nats.Msg) {
	log.Printf("[nats] cmd.stop")

	if err := a.handler.StopPlan(); err != nil {
		a.respond(msg, NewErrorResponse(err))
		return
	}

	a.respond(msg, NewSuccessResponse(a.handler.GetPlanStatus()))
}

func (a *Adapter) handleStatus(msg *nats.Msg) {
	log.Printf("[nats] cmd.status")
	a.respond(msg, NewSuccessResponse(a.handler.GetPlanStatus()))
}

func (a *Adapter) handleGraph(msg *nats.Msg) {
	log.Printf("[nats] cmd.graph")
	a.respond(msg, NewSuccessResponse(a.handler.GetPlanGraph()))
}

func (a *Adapter) handleRestart(msg *nats.Msg) {
	var req RestartRequest
	if err := json.Unmarshal(msg.Data, &req); err != nil {
		a.respond(msg, NewErrorResponse(fmt.Errorf("invalid request: %w", err)))
		return
	}

	if req.Component == "" {
		a.respond(msg, NewErrorResponse(fmt.Errorf("component name required")))
		return
	}

	log.Printf("[nats] cmd.restart component=%s dry=%v", req.Component, req.Dry)

	// Dry-run
	if req.Dry {
		result := a.handler.RestartComponentDry(req.Component)
		a.respond(msg, NewSuccessResponse(result))
		return
	}

	// Parse timeout
	timeout := 60 * time.Second
	if req.Timeout != "" {
		if d, err := time.ParseDuration(req.Timeout); err == nil && d > 0 {
			timeout = d
		}
	}

	wait := req.Wait
	if wait == "" {
		wait = "pid"
	}

	result, err := a.handler.RestartComponent(req.Component, wait, timeout)
	if err != nil {
		a.respond(msg, NewErrorResponse(err))
		return
	}

	a.respond(msg, NewSuccessResponse(result))
}

func (a *Adapter) handleStopComponent(msg *nats.Msg) {
	var req StopComponentRequest
	if err := json.Unmarshal(msg.Data, &req); err != nil {
		a.respond(msg, NewErrorResponse(fmt.Errorf("invalid request: %w", err)))
		return
	}

	if req.Component == "" {
		a.respond(msg, NewErrorResponse(fmt.Errorf("component name required")))
		return
	}

	log.Printf("[nats] cmd.stop-comp component=%s", req.Component)

	if err := a.handler.StopComponent(req.Component); err != nil {
		a.respond(msg, NewErrorResponse(err))
		return
	}

	a.respond(msg, NewSuccessResponse(map[string]string{"status": "stopped", "component": req.Component}))
}

func (a *Adapter) handleHealth(msg *nats.Msg) {
	log.Printf("[nats] cmd.health")
	a.respond(msg, NewSuccessResponse(a.handler.GetHealth()))
}

func (a *Adapter) handleRecipes(msg *nats.Msg) {
	log.Printf("[nats] cmd.recipes")

	recipes, err := a.handler.ListRecipes()
	if err != nil {
		a.respond(msg, NewErrorResponse(err))
		return
	}

	a.respond(msg, NewSuccessResponse(&RecipesResponse{Recipes: recipes}))
}

func (a *Adapter) handleAddRecipe(msg *nats.Msg) {
	var req AddRecipeRequest
	if err := json.Unmarshal(msg.Data, &req); err != nil {
		a.respond(msg, NewErrorResponse(fmt.Errorf("invalid request: %w", err)))
		return
	}

	if req.Content == "" {
		a.respond(msg, NewErrorResponse(fmt.Errorf("recipe content required")))
		return
	}

	log.Printf("[nats] cmd.add-recipe force=%v", req.Force)

	name, version, err := a.handler.AddRecipe(req.Content, req.Force)
	if err != nil {
		a.respond(msg, NewErrorResponse(err))
		return
	}

	a.respond(msg, NewSuccessResponse(&AddRecipeResponse{Name: name, Version: version}))
}

// respond sends a JSON response to a request message.
func (a *Adapter) respond(msg *nats.Msg, resp *Response) {
	if msg.Reply == "" {
		// No reply subject, nothing to do
		return
	}

	data, err := json.Marshal(resp)
	if err != nil {
		log.Printf("[nats] failed to marshal response: %v", err)
		return
	}

	if err := msg.Respond(data); err != nil {
		log.Printf("[nats] failed to send response: %v", err)
	}
}

// Event publishers

func (a *Adapter) publishStateLoop() {
	defer a.wg.Done()

	ticker := time.NewTicker(a.cfg.PublishStateInterval)
	defer ticker.Stop()

	for {
		select {
		case <-a.ctx.Done():
			return
		case <-ticker.C:
			a.publishState()
		}
	}
}

func (a *Adapter) publishState() {
	status := a.handler.GetPlanStatus()

	event := StateEvent{
		Timestamp:  time.Now().UTC(),
		DeviceID:   a.cfg.DeviceID,
		Components: status.Components,
		PlanStatus: status.Status,
		PlanPath:   status.PlanPath,
	}

	data, err := json.Marshal(event)
	if err != nil {
		log.Printf("[nats] failed to marshal state event: %v", err)
		return
	}

	a.mu.RLock()
	nc := a.nc
	a.mu.RUnlock()

	if nc == nil {
		return
	}

	if err := nc.Publish(a.subjects.EventState, data); err != nil {
		log.Printf("[nats] failed to publish state event: %v", err)
	}
}

func (a *Adapter) publishHealthLoop() {
	defer a.wg.Done()

	ticker := time.NewTicker(a.cfg.PublishHealthInterval)
	defer ticker.Stop()

	for {
		select {
		case <-a.ctx.Done():
			return
		case <-ticker.C:
			a.publishHealth()
		}
	}
}

func (a *Adapter) publishHealth() {
	health := a.handler.GetHealth()

	event := HealthEvent{
		Timestamp: time.Now().UTC(),
		DeviceID:  a.cfg.DeviceID,
		Status:    health.Status,
		Uptime:    health.Uptime,
	}

	data, err := json.Marshal(event)
	if err != nil {
		log.Printf("[nats] failed to marshal health event: %v", err)
		return
	}

	a.mu.RLock()
	nc := a.nc
	a.mu.RUnlock()

	if nc == nil {
		return
	}

	if err := nc.Publish(a.subjects.EventHealth, data); err != nil {
		log.Printf("[nats] failed to publish health event: %v", err)
	}
}

// buildTLSConfig creates a TLS configuration from the adapter config.
func (a *Adapter) buildTLSConfig() (*tls.Config, error) {
	tlsCfg := &tls.Config{
		MinVersion: tls.VersionTLS12,
	}

	// Load client certificate if provided (for mTLS)
	if a.cfg.TLSCert != "" && a.cfg.TLSKey != "" {
		cert, err := tls.LoadX509KeyPair(a.cfg.TLSCert, a.cfg.TLSKey)
		if err != nil {
			return nil, fmt.Errorf("failed to load client certificate: %w", err)
		}
		tlsCfg.Certificates = []tls.Certificate{cert}
		log.Printf("[nats] loaded client certificate from %s", a.cfg.TLSCert)
	}

	// Load CA certificate for server verification
	if a.cfg.TLSCA != "" {
		caCert, err := os.ReadFile(a.cfg.TLSCA)
		if err != nil {
			return nil, fmt.Errorf("failed to read CA certificate: %w", err)
		}

		caCertPool := x509.NewCertPool()
		if !caCertPool.AppendCertsFromPEM(caCert) {
			return nil, fmt.Errorf("failed to parse CA certificate from %s", a.cfg.TLSCA)
		}

		tlsCfg.RootCAs = caCertPool
		log.Printf("[nats] loaded CA certificate from %s", a.cfg.TLSCA)
	}

	// Skip verification only if explicitly disabled
	tlsCfg.InsecureSkipVerify = !a.cfg.TLSVerify

	return tlsCfg, nil
}

// buildAuthOptions returns NATS connection options for authentication.
// Priority: NKey > Credentials > Token > UserPass
func (a *Adapter) buildAuthOptions() []nats.Option {
	var opts []nats.Option

	switch {
	case a.cfg.NKeyFile != "":
		// NKey authentication (most secure)
		opt, err := nats.NkeyOptionFromSeed(a.cfg.NKeyFile)
		if err != nil {
			log.Printf("[nats] warning: failed to load NKey from %s: %v", a.cfg.NKeyFile, err)
		} else {
			opts = append(opts, opt)
			log.Printf("[nats] using NKey authentication from %s", a.cfg.NKeyFile)
		}

	case a.cfg.CredentialsFile != "":
		// Credentials file (JWT + NKey)
		opts = append(opts, nats.UserCredentials(a.cfg.CredentialsFile))
		log.Printf("[nats] using credentials file authentication from %s", a.cfg.CredentialsFile)

	case a.cfg.Token != "":
		// Token authentication
		opts = append(opts, nats.Token(a.cfg.Token))
		log.Printf("[nats] using token authentication")

	case a.cfg.Username != "":
		// Username/password authentication
		opts = append(opts, nats.UserInfo(a.cfg.Username, a.cfg.Password))
		log.Printf("[nats] using username/password authentication for user %s", a.cfg.Username)
	}

	return opts
}

// Publish sends a message to a subject (for external use).
func (a *Adapter) Publish(subject string, data []byte) error {
	a.mu.RLock()
	nc := a.nc
	a.mu.RUnlock()

	if nc == nil {
		return fmt.Errorf("not connected")
	}

	return nc.Publish(subject, data)
}

// Request sends a request and waits for a response (for external use).
func (a *Adapter) Request(subject string, data []byte, timeout time.Duration) (*nats.Msg, error) {
	a.mu.RLock()
	nc := a.nc
	a.mu.RUnlock()

	if nc == nil {
		return nil, fmt.Errorf("not connected")
	}

	if timeout <= 0 {
		timeout = a.cfg.RequestTimeout
	}

	return nc.Request(subject, data, timeout)
}

// Connected returns true if the adapter is connected to NATS.
func (a *Adapter) Connected() bool {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.nc != nil && a.nc.IsConnected()
}
