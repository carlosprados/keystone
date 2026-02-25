// Package mqtt provides an MQTT adapter for the Keystone agent.
// It enables asynchronous communication with a control plane via MQTT messaging.
package mqtt

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
	pahomqtt "github.com/eclipse/paho.mqtt.golang"
)

// Adapter implements the MQTT control plane adapter.
type Adapter struct {
	cfg     Config
	handler adapter.CommandHandler
	topics  *Topics

	mu     sync.RWMutex
	client pahomqtt.Client

	// Lifecycle
	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
}

// Config holds the configuration for the MQTT adapter.
type Config struct {
	// Broker is the MQTT broker URL (e.g., "tcp://localhost:1883" or "ssl://localhost:8883").
	Broker string

	// DeviceID is the unique identifier for this agent.
	// Used in topic prefixes for multi-tenancy.
	DeviceID string

	// ClientID is the MQTT client ID. If empty, defaults to "keystone-{DeviceID}".
	ClientID string

	// TLS configuration (optional).
	TLSCert   string // Path to client certificate
	TLSKey    string // Path to client key
	TLSCA     string // Path to CA certificate
	TLSVerify bool   // Verify server certificate (default: true when TLS enabled)

	// Authentication (optional).
	Username string // Username for authentication
	Password string // Password for authentication

	// Connection settings
	KeepAlive        time.Duration // Keepalive interval (default: 30s)
	ConnectTimeout   time.Duration // Connection timeout (default: 30s)
	CleanSession     bool          // Start with clean session (default: true)
	AutoReconnect    bool          // Auto reconnect on disconnect (default: true)
	MaxReconnectWait time.Duration // Max wait between reconnects (default: 5m)

	// QoS levels
	CommandQoS  byte // QoS for command subscriptions (default: 1)
	ResponseQoS byte // QoS for response publishing (default: 1)
	EventQoS    byte // QoS for event publishing (default: 0)

	// Event publishing
	PublishStateInterval  time.Duration // Interval for state events (0 to disable)
	PublishHealthInterval time.Duration // Interval for health events (0 to disable)

	// Last Will and Testament (LWT)
	LWTEnabled bool   // Enable LWT (default: true)
	LWTTopic   string // LWT topic (default: keystone/{deviceId}/status)
	LWTPayload string // LWT payload (default: "offline")
	LWTRetain  bool   // Retain LWT message (default: true)
}

// DefaultConfig returns a Config with sensible defaults.
func DefaultConfig() Config {
	return Config{
		Broker:                "tcp://localhost:1883",
		DeviceID:              "keystone-agent",
		KeepAlive:             30 * time.Second,
		ConnectTimeout:        30 * time.Second,
		CleanSession:          true,
		AutoReconnect:         true,
		MaxReconnectWait:      5 * time.Minute,
		CommandQoS:            1,
		ResponseQoS:           1,
		EventQoS:              0,
		PublishStateInterval:  10 * time.Second,
		PublishHealthInterval: 30 * time.Second,
		LWTEnabled:            true,
		LWTRetain:             true,
		TLSVerify:             true,
	}
}

// New creates a new MQTT adapter.
func New(cfg Config, handler adapter.CommandHandler) *Adapter {
	return &Adapter{
		cfg:     cfg,
		handler: handler,
		topics:  NewTopics(cfg.DeviceID),
	}
}

// Name returns the adapter identifier.
func (a *Adapter) Name() string {
	return "mqtt"
}

// Start connects to the MQTT broker and sets up subscriptions.
func (a *Adapter) Start(ctx context.Context) error {
	a.ctx, a.cancel = context.WithCancel(ctx)

	opts := pahomqtt.NewClientOptions()

	// Broker URL
	opts.AddBroker(a.cfg.Broker)

	// Client ID
	clientID := a.cfg.ClientID
	if clientID == "" {
		clientID = fmt.Sprintf("keystone-%s", a.cfg.DeviceID)
	}
	opts.SetClientID(clientID)

	// Authentication
	if a.cfg.Username != "" {
		opts.SetUsername(a.cfg.Username)
		opts.SetPassword(a.cfg.Password)
	}

	// Connection settings
	opts.SetKeepAlive(a.cfg.KeepAlive)
	opts.SetConnectTimeout(a.cfg.ConnectTimeout)
	opts.SetCleanSession(a.cfg.CleanSession)
	opts.SetAutoReconnect(a.cfg.AutoReconnect)
	opts.SetMaxReconnectInterval(a.cfg.MaxReconnectWait)

	// TLS configuration
	if a.cfg.TLSCert != "" || a.cfg.TLSCA != "" {
		tlsCfg, err := a.buildTLSConfig()
		if err != nil {
			return fmt.Errorf("failed to configure TLS: %w", err)
		}
		opts.SetTLSConfig(tlsCfg)
	}

	// Last Will and Testament
	if a.cfg.LWTEnabled {
		lwtTopic := a.cfg.LWTTopic
		if lwtTopic == "" {
			lwtTopic = fmt.Sprintf("keystone/%s/status", a.cfg.DeviceID)
		}
		lwtPayload := a.cfg.LWTPayload
		if lwtPayload == "" {
			lwtPayload = "offline"
		}
		opts.SetWill(lwtTopic, lwtPayload, a.cfg.ResponseQoS, a.cfg.LWTRetain)
	}

	// Connection handlers
	opts.SetOnConnectHandler(func(c pahomqtt.Client) {
		log.Printf("[mqtt] connected to %s as %s", a.cfg.Broker, clientID)
		// Re-subscribe on reconnect
		if err := a.setupSubscriptionsWithClient(c); err != nil {
			log.Printf("[mqtt] failed to setup subscriptions: %v", err)
		}
		// Publish online status
		if a.cfg.LWTEnabled {
			lwtTopic := a.cfg.LWTTopic
			if lwtTopic == "" {
				lwtTopic = fmt.Sprintf("keystone/%s/status", a.cfg.DeviceID)
			}
			c.Publish(lwtTopic, a.cfg.ResponseQoS, a.cfg.LWTRetain, "online")
		}
	})

	opts.SetConnectionLostHandler(func(c pahomqtt.Client, err error) {
		log.Printf("[mqtt] connection lost: %v", err)
	})

	opts.SetReconnectingHandler(func(c pahomqtt.Client, opts *pahomqtt.ClientOptions) {
		log.Printf("[mqtt] reconnecting to %s", a.cfg.Broker)
	})

	// Create and connect client
	client := pahomqtt.NewClient(opts)
	token := client.Connect()
	if !token.WaitTimeout(a.cfg.ConnectTimeout) {
		return fmt.Errorf("connection timeout")
	}
	if err := token.Error(); err != nil {
		return fmt.Errorf("failed to connect to MQTT broker: %w", err)
	}

	a.mu.Lock()
	a.client = client
	a.mu.Unlock()

	// Start event publishers
	if a.cfg.PublishStateInterval > 0 {
		a.wg.Add(1)
		go a.publishStateLoop()
	}
	if a.cfg.PublishHealthInterval > 0 {
		a.wg.Add(1)
		go a.publishHealthLoop()
	}

	return nil
}

// Stop gracefully shuts down the MQTT adapter.
func (a *Adapter) Stop(ctx context.Context) error {
	log.Printf("[mqtt] stopping MQTT adapter")

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
		log.Printf("[mqtt] stop timed out waiting for goroutines")
	}

	// Publish offline status before disconnecting
	a.mu.Lock()
	client := a.client
	a.mu.Unlock()

	if client != nil && client.IsConnected() {
		if a.cfg.LWTEnabled {
			lwtTopic := a.cfg.LWTTopic
			if lwtTopic == "" {
				lwtTopic = fmt.Sprintf("keystone/%s/status", a.cfg.DeviceID)
			}
			client.Publish(lwtTopic, a.cfg.ResponseQoS, a.cfg.LWTRetain, "offline")
		}
		client.Disconnect(250) // 250ms graceful disconnect
	}

	a.mu.Lock()
	a.client = nil
	a.mu.Unlock()

	return nil
}

// setupSubscriptions creates all command subscriptions.
func (a *Adapter) setupSubscriptions() error {
	a.mu.RLock()
	client := a.client
	a.mu.RUnlock()
	return a.setupSubscriptionsWithClient(client)
}

func (a *Adapter) setupSubscriptionsWithClient(client pahomqtt.Client) error {
	if client == nil {
		return fmt.Errorf("not connected")
	}

	subs := map[string]pahomqtt.MessageHandler{
		a.topics.CmdApply:      a.handleApply,
		a.topics.CmdStop:       a.handleStop,
		a.topics.CmdStatus:     a.handleStatus,
		a.topics.CmdComponents: a.handleComponents,
		a.topics.CmdGraph:      a.handleGraph,
		a.topics.CmdRestart:    a.handleRestart,
		a.topics.CmdStopComp:   a.handleStopComponent,
		a.topics.CmdHealth:     a.handleHealth,
		a.topics.CmdRecipes:    a.handleRecipes,
		a.topics.CmdAddRecipe:  a.handleAddRecipe,
	}

	for topic, handler := range subs {
		token := client.Subscribe(topic, a.cfg.CommandQoS, handler)
		if !token.WaitTimeout(10 * time.Second) {
			return fmt.Errorf("subscription timeout for %s", topic)
		}
		if err := token.Error(); err != nil {
			return fmt.Errorf("failed to subscribe to %s: %w", topic, err)
		}
		log.Printf("[mqtt] subscribed to %s", topic)
	}

	return nil
}

// Command handlers

func (a *Adapter) handleApply(client pahomqtt.Client, msg pahomqtt.Message) {
	var req ApplyRequest
	if err := json.Unmarshal(msg.Payload(), &req); err != nil {
		a.respond(a.topics.RespApply, "", NewErrorResponse("", fmt.Errorf("invalid request: %w", err)))
		return
	}

	log.Printf("[mqtt] cmd/apply planPath=%s dry=%v", req.PlanPath, req.Dry)

	var err error
	if req.PlanPath != "" {
		err = a.handler.ApplyPlan(req.PlanPath, req.Dry)
	} else if req.Content != "" {
		err = a.handler.ApplyPlanContent(req.Content, req.Dry)
	} else {
		err = fmt.Errorf("planPath or content required")
	}

	if err != nil {
		a.respond(a.topics.RespApply, req.CorrelationID, NewErrorResponse(req.CorrelationID, err))
		return
	}

	a.respond(a.topics.RespApply, req.CorrelationID, NewSuccessResponse(req.CorrelationID, a.handler.GetPlanStatus()))
}

func (a *Adapter) handleStop(client pahomqtt.Client, msg pahomqtt.Message) {
	var req SimpleRequest
	_ = json.Unmarshal(msg.Payload(), &req)

	log.Printf("[mqtt] cmd/stop")

	if err := a.handler.StopPlan(); err != nil {
		a.respond(a.topics.RespStop, req.CorrelationID, NewErrorResponse(req.CorrelationID, err))
		return
	}

	a.respond(a.topics.RespStop, req.CorrelationID, NewSuccessResponse(req.CorrelationID, a.handler.GetPlanStatus()))
}

func (a *Adapter) handleStatus(client pahomqtt.Client, msg pahomqtt.Message) {
	var req SimpleRequest
	_ = json.Unmarshal(msg.Payload(), &req)

	log.Printf("[mqtt] cmd/status")
	a.respond(a.topics.RespStatus, req.CorrelationID, NewSuccessResponse(req.CorrelationID, a.handler.GetPlanStatus()))
}

func (a *Adapter) handleComponents(client pahomqtt.Client, msg pahomqtt.Message) {
	var req SimpleRequest
	_ = json.Unmarshal(msg.Payload(), &req)

	log.Printf("[mqtt] cmd/components")
	a.respond(a.topics.RespComponents, req.CorrelationID, NewSuccessResponse(req.CorrelationID, &ComponentsResponse{
		Components: a.handler.GetComponents(),
	}))
}

func (a *Adapter) handleGraph(client pahomqtt.Client, msg pahomqtt.Message) {
	var req SimpleRequest
	_ = json.Unmarshal(msg.Payload(), &req)

	log.Printf("[mqtt] cmd/graph")
	a.respond(a.topics.RespGraph, req.CorrelationID, NewSuccessResponse(req.CorrelationID, a.handler.GetPlanGraph()))
}

func (a *Adapter) handleRestart(client pahomqtt.Client, msg pahomqtt.Message) {
	var req RestartRequest
	if err := json.Unmarshal(msg.Payload(), &req); err != nil {
		a.respond(a.topics.RespRestart, "", NewErrorResponse("", fmt.Errorf("invalid request: %w", err)))
		return
	}

	if req.Component == "" {
		a.respond(a.topics.RespRestart, req.CorrelationID, NewErrorResponse(req.CorrelationID, fmt.Errorf("component name required")))
		return
	}

	log.Printf("[mqtt] cmd/restart component=%s dry=%v", req.Component, req.Dry)

	// Dry-run
	if req.Dry {
		result := a.handler.RestartComponentDry(req.Component)
		a.respond(a.topics.RespRestart, req.CorrelationID, NewSuccessResponse(req.CorrelationID, result))
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
		a.respond(a.topics.RespRestart, req.CorrelationID, NewErrorResponse(req.CorrelationID, err))
		return
	}

	a.respond(a.topics.RespRestart, req.CorrelationID, NewSuccessResponse(req.CorrelationID, result))
}

func (a *Adapter) handleStopComponent(client pahomqtt.Client, msg pahomqtt.Message) {
	var req StopComponentRequest
	if err := json.Unmarshal(msg.Payload(), &req); err != nil {
		a.respond(a.topics.RespStopComp, "", NewErrorResponse("", fmt.Errorf("invalid request: %w", err)))
		return
	}

	if req.Component == "" {
		a.respond(a.topics.RespStopComp, req.CorrelationID, NewErrorResponse(req.CorrelationID, fmt.Errorf("component name required")))
		return
	}

	log.Printf("[mqtt] cmd/stop-comp component=%s", req.Component)

	if err := a.handler.StopComponent(req.Component); err != nil {
		a.respond(a.topics.RespStopComp, req.CorrelationID, NewErrorResponse(req.CorrelationID, err))
		return
	}

	a.respond(a.topics.RespStopComp, req.CorrelationID, NewSuccessResponse(req.CorrelationID, map[string]string{"status": "stopped", "component": req.Component}))
}

func (a *Adapter) handleHealth(client pahomqtt.Client, msg pahomqtt.Message) {
	var req SimpleRequest
	_ = json.Unmarshal(msg.Payload(), &req)

	log.Printf("[mqtt] cmd/health")
	a.respond(a.topics.RespHealth, req.CorrelationID, NewSuccessResponse(req.CorrelationID, a.handler.GetHealth()))
}

func (a *Adapter) handleRecipes(client pahomqtt.Client, msg pahomqtt.Message) {
	var req SimpleRequest
	_ = json.Unmarshal(msg.Payload(), &req)

	log.Printf("[mqtt] cmd/recipes")

	recipes, err := a.handler.ListRecipes()
	if err != nil {
		a.respond(a.topics.RespRecipes, req.CorrelationID, NewErrorResponse(req.CorrelationID, err))
		return
	}

	a.respond(a.topics.RespRecipes, req.CorrelationID, NewSuccessResponse(req.CorrelationID, &RecipesResponse{Recipes: recipes}))
}

func (a *Adapter) handleAddRecipe(client pahomqtt.Client, msg pahomqtt.Message) {
	var req AddRecipeRequest
	if err := json.Unmarshal(msg.Payload(), &req); err != nil {
		a.respond(a.topics.RespAddRecipe, "", NewErrorResponse("", fmt.Errorf("invalid request: %w", err)))
		return
	}

	if req.Content == "" {
		a.respond(a.topics.RespAddRecipe, req.CorrelationID, NewErrorResponse(req.CorrelationID, fmt.Errorf("recipe content required")))
		return
	}

	log.Printf("[mqtt] cmd/add-recipe force=%v", req.Force)

	name, version, err := a.handler.AddRecipe(req.Content, req.Force)
	if err != nil {
		a.respond(a.topics.RespAddRecipe, req.CorrelationID, NewErrorResponse(req.CorrelationID, err))
		return
	}

	a.respond(a.topics.RespAddRecipe, req.CorrelationID, NewSuccessResponse(req.CorrelationID, &AddRecipeResponse{Name: name, Version: version}))
}

// respond sends a JSON response to the specified topic.
func (a *Adapter) respond(topic, correlationID string, resp *Response) {
	a.mu.RLock()
	client := a.client
	a.mu.RUnlock()

	if client == nil || !client.IsConnected() {
		log.Printf("[mqtt] cannot respond: not connected")
		return
	}

	data, err := json.Marshal(resp)
	if err != nil {
		log.Printf("[mqtt] failed to marshal response: %v", err)
		return
	}

	token := client.Publish(topic, a.cfg.ResponseQoS, false, data)
	if !token.WaitTimeout(5 * time.Second) {
		log.Printf("[mqtt] response publish timeout to %s", topic)
		return
	}
	if err := token.Error(); err != nil {
		log.Printf("[mqtt] failed to publish response to %s: %v", topic, err)
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
		log.Printf("[mqtt] failed to marshal state event: %v", err)
		return
	}

	a.mu.RLock()
	client := a.client
	a.mu.RUnlock()

	if client == nil || !client.IsConnected() {
		return
	}

	token := client.Publish(a.topics.EventState, a.cfg.EventQoS, false, data)
	if err := token.Error(); err != nil {
		log.Printf("[mqtt] failed to publish state event: %v", err)
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
		log.Printf("[mqtt] failed to marshal health event: %v", err)
		return
	}

	a.mu.RLock()
	client := a.client
	a.mu.RUnlock()

	if client == nil || !client.IsConnected() {
		return
	}

	token := client.Publish(a.topics.EventHealth, a.cfg.EventQoS, false, data)
	if err := token.Error(); err != nil {
		log.Printf("[mqtt] failed to publish health event: %v", err)
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
		log.Printf("[mqtt] loaded client certificate from %s", a.cfg.TLSCert)
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
		log.Printf("[mqtt] loaded CA certificate from %s", a.cfg.TLSCA)
	}

	// Skip verification only if explicitly disabled
	tlsCfg.InsecureSkipVerify = !a.cfg.TLSVerify

	return tlsCfg, nil
}

// Publish sends a message to a topic (for external use).
func (a *Adapter) Publish(topic string, payload []byte, qos byte, retain bool) error {
	a.mu.RLock()
	client := a.client
	a.mu.RUnlock()

	if client == nil || !client.IsConnected() {
		return fmt.Errorf("not connected")
	}

	token := client.Publish(topic, qos, retain, payload)
	if !token.WaitTimeout(10 * time.Second) {
		return fmt.Errorf("publish timeout")
	}
	return token.Error()
}

// Subscribe subscribes to a topic with a custom handler (for external use).
func (a *Adapter) Subscribe(topic string, qos byte, handler pahomqtt.MessageHandler) error {
	a.mu.RLock()
	client := a.client
	a.mu.RUnlock()

	if client == nil || !client.IsConnected() {
		return fmt.Errorf("not connected")
	}

	token := client.Subscribe(topic, qos, handler)
	if !token.WaitTimeout(10 * time.Second) {
		return fmt.Errorf("subscribe timeout")
	}
	return token.Error()
}

// Connected returns true if the adapter is connected to the MQTT broker.
func (a *Adapter) Connected() bool {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.client != nil && a.client.IsConnected()
}
