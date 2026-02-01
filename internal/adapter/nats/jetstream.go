package nats

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"time"

	"github.com/nats-io/nats.go/jetstream"
)

// JetStreamConfig holds the configuration for JetStream job queue.
type JetStreamConfig struct {
	// Enabled activates JetStream job processing.
	Enabled bool

	// StreamName is the name of the JetStream stream for jobs.
	// Default: "KEYSTONE_JOBS"
	StreamName string

	// ConsumerName is the durable consumer name.
	// Default: "keystone-{deviceId}"
	ConsumerName string

	// MaxDeliver is the maximum number of delivery attempts.
	// Default: 5
	MaxDeliver int

	// AckWait is the time to wait for acknowledgment before redelivery.
	// Default: 30s
	AckWait time.Duration

	// MaxAckPending limits the number of unacknowledged messages.
	// Default: 100
	MaxAckPending int

	// WorkerCount is the number of concurrent job processors.
	// Default: 1
	WorkerCount int
}

// DefaultJetStreamConfig returns sensible defaults for JetStream.
func DefaultJetStreamConfig() JetStreamConfig {
	return JetStreamConfig{
		Enabled:       false,
		StreamName:    "KEYSTONE_JOBS",
		MaxDeliver:    5,
		AckWait:       30 * time.Second,
		MaxAckPending: 100,
		WorkerCount:   1,
	}
}

// Job represents a persistent job in the queue.
type Job struct {
	// ID is a unique identifier for the job.
	ID string `json:"id"`

	// Type is the job type (apply, stop, restart, etc.).
	Type JobType `json:"type"`

	// DeviceID is the target device for this job.
	DeviceID string `json:"deviceId"`

	// Payload contains the job-specific data.
	Payload json.RawMessage `json:"payload"`

	// CreatedAt is when the job was created.
	CreatedAt time.Time `json:"createdAt"`

	// Priority (lower = higher priority). Default: 0
	Priority int `json:"priority,omitempty"`

	// Metadata for tracking and filtering.
	Metadata map[string]string `json:"metadata,omitempty"`
}

// JobType defines the type of job.
type JobType string

const (
	JobTypeApply        JobType = "apply"
	JobTypeStop         JobType = "stop"
	JobTypeRestart      JobType = "restart"
	JobTypeStopComp     JobType = "stop-comp"
	JobTypeAddRecipe    JobType = "add-recipe"
	JobTypeDeleteRecipe JobType = "delete-recipe"
)

// JobResult represents the result of processing a job.
type JobResult struct {
	JobID     string    `json:"jobId"`
	Success   bool      `json:"success"`
	Error     string    `json:"error,omitempty"`
	Data      any       `json:"data,omitempty"`
	ProcessedAt time.Time `json:"processedAt"`
}

// jetStreamManager handles JetStream operations.
type jetStreamManager struct {
	adapter  *Adapter
	js       jetstream.JetStream
	stream   jetstream.Stream
	consumer jetstream.Consumer
	cfg      JetStreamConfig
}

// setupJetStream initializes JetStream for job processing.
func (a *Adapter) setupJetStream(ctx context.Context) error {
	if !a.cfg.JetStream.Enabled {
		return nil
	}

	js, err := jetstream.New(a.nc)
	if err != nil {
		return fmt.Errorf("failed to create JetStream context: %w", err)
	}

	cfg := a.cfg.JetStream
	if cfg.ConsumerName == "" {
		cfg.ConsumerName = fmt.Sprintf("keystone-%s", a.cfg.DeviceID)
	}

	// Subject for this device's jobs
	jobSubject := fmt.Sprintf("keystone.%s.jobs", a.cfg.DeviceID)

	// Create or get the stream
	stream, err := js.CreateOrUpdateStream(ctx, jetstream.StreamConfig{
		Name:        cfg.StreamName,
		Description: "Keystone agent job queue",
		Subjects:    []string{"keystone.*.jobs"},
		Retention:   jetstream.WorkQueuePolicy,
		MaxMsgs:     10000,
		MaxBytes:    100 * 1024 * 1024, // 100MB
		MaxAge:      7 * 24 * time.Hour, // 7 days
		Storage:     jetstream.FileStorage,
		Replicas:    1,
		Discard:     jetstream.DiscardOld,
	})
	if err != nil {
		return fmt.Errorf("failed to create/get stream: %w", err)
	}

	log.Printf("[nats] JetStream stream %s ready", cfg.StreamName)

	// Create durable consumer for this device
	consumer, err := stream.CreateOrUpdateConsumer(ctx, jetstream.ConsumerConfig{
		Name:          cfg.ConsumerName,
		Durable:       cfg.ConsumerName,
		Description:   fmt.Sprintf("Keystone agent consumer for device %s", a.cfg.DeviceID),
		FilterSubject: jobSubject,
		AckPolicy:     jetstream.AckExplicitPolicy,
		AckWait:       cfg.AckWait,
		MaxDeliver:    cfg.MaxDeliver,
		MaxAckPending: cfg.MaxAckPending,
		DeliverPolicy: jetstream.DeliverAllPolicy,
	})
	if err != nil {
		return fmt.Errorf("failed to create consumer: %w", err)
	}

	log.Printf("[nats] JetStream consumer %s ready (subject: %s)", cfg.ConsumerName, jobSubject)

	a.jsManager = &jetStreamManager{
		adapter:  a,
		js:       js,
		stream:   stream,
		consumer: consumer,
		cfg:      cfg,
	}

	return nil
}

// startJobProcessor starts the job processing loop.
func (a *Adapter) startJobProcessor(ctx context.Context) {
	if a.jsManager == nil {
		return
	}

	for i := 0; i < a.cfg.JetStream.WorkerCount; i++ {
		a.wg.Add(1)
		go a.jobWorker(ctx, i)
	}
}

// jobWorker processes jobs from the queue.
func (a *Adapter) jobWorker(ctx context.Context, workerID int) {
	defer a.wg.Done()

	log.Printf("[nats] job worker %d started", workerID)

	// Use Consume for continuous message processing
	cons := a.jsManager.consumer

	iter, err := cons.Messages()
	if err != nil {
		log.Printf("[nats] worker %d: failed to get message iterator: %v", workerID, err)
		return
	}
	defer iter.Stop()

	for {
		select {
		case <-ctx.Done():
			log.Printf("[nats] job worker %d stopping", workerID)
			return
		default:
		}

		// Fetch next message with timeout
		msg, err := iter.Next()
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			// Log non-context errors and continue
			if err.Error() != "nats: timeout" {
				log.Printf("[nats] worker %d: fetch error: %v", workerID, err)
			}
			continue
		}

		// Process the job
		result := a.processJob(ctx, msg.Data())

		// Acknowledge or NAK based on result
		if result.Success {
			if err := msg.Ack(); err != nil {
				log.Printf("[nats] worker %d: ack failed: %v", workerID, err)
			}
		} else {
			// NAK with delay for retry
			if err := msg.NakWithDelay(5 * time.Second); err != nil {
				log.Printf("[nats] worker %d: nak failed: %v", workerID, err)
			}
		}

		// Publish result to results subject
		a.publishJobResult(result)
	}
}

// processJob handles a single job.
func (a *Adapter) processJob(ctx context.Context, data []byte) *JobResult {
	var job Job
	if err := json.Unmarshal(data, &job); err != nil {
		return &JobResult{
			JobID:       "unknown",
			Success:     false,
			Error:       fmt.Sprintf("invalid job format: %v", err),
			ProcessedAt: time.Now().UTC(),
		}
	}

	log.Printf("[nats] processing job id=%s type=%s", job.ID, job.Type)

	result := &JobResult{
		JobID:       job.ID,
		ProcessedAt: time.Now().UTC(),
	}

	var err error
	var resultData any

	switch job.Type {
	case JobTypeApply:
		var req ApplyRequest
		if err = json.Unmarshal(job.Payload, &req); err != nil {
			break
		}
		if req.PlanPath != "" {
			err = a.handler.ApplyPlan(req.PlanPath, req.Dry)
		} else if req.Content != "" {
			err = a.handler.ApplyPlanContent(req.Content, req.Dry)
		} else {
			err = fmt.Errorf("planPath or content required")
		}
		if err == nil {
			resultData = a.handler.GetPlanStatus()
		}

	case JobTypeStop:
		err = a.handler.StopPlan()
		if err == nil {
			resultData = a.handler.GetPlanStatus()
		}

	case JobTypeRestart:
		var req RestartRequest
		if err = json.Unmarshal(job.Payload, &req); err != nil {
			break
		}
		if req.Dry {
			resultData = a.handler.RestartComponentDry(req.Component)
		} else {
			timeout := 60 * time.Second
			if req.Timeout != "" {
				if d, e := time.ParseDuration(req.Timeout); e == nil && d > 0 {
					timeout = d
				}
			}
			wait := req.Wait
			if wait == "" {
				wait = "pid"
			}
			resultData, err = a.handler.RestartComponent(req.Component, wait, timeout)
		}

	case JobTypeStopComp:
		var req StopComponentRequest
		if err = json.Unmarshal(job.Payload, &req); err != nil {
			break
		}
		err = a.handler.StopComponent(req.Component)

	case JobTypeAddRecipe:
		var req AddRecipeRequest
		if err = json.Unmarshal(job.Payload, &req); err != nil {
			break
		}
		var name, version string
		name, version, err = a.handler.AddRecipe(req.Content, req.Force)
		if err == nil {
			resultData = &AddRecipeResponse{Name: name, Version: version}
		}

	default:
		err = fmt.Errorf("unknown job type: %s", job.Type)
	}

	if err != nil {
		result.Success = false
		result.Error = err.Error()
		log.Printf("[nats] job %s failed: %v", job.ID, err)
	} else {
		result.Success = true
		result.Data = resultData
		log.Printf("[nats] job %s completed successfully", job.ID)
	}

	return result
}

// publishJobResult publishes the result of a job.
func (a *Adapter) publishJobResult(result *JobResult) {
	subject := fmt.Sprintf("keystone.%s.jobs.results", a.cfg.DeviceID)

	data, err := json.Marshal(result)
	if err != nil {
		log.Printf("[nats] failed to marshal job result: %v", err)
		return
	}

	a.mu.RLock()
	nc := a.nc
	a.mu.RUnlock()

	if nc == nil {
		return
	}

	if err := nc.Publish(subject, data); err != nil {
		log.Printf("[nats] failed to publish job result: %v", err)
	}
}

// PublishJob publishes a job to the queue (for testing/external use).
func (a *Adapter) PublishJob(ctx context.Context, job *Job) error {
	if a.jsManager == nil {
		return fmt.Errorf("JetStream not enabled")
	}

	if job.ID == "" {
		job.ID = fmt.Sprintf("%s-%d", job.Type, time.Now().UnixNano())
	}
	if job.CreatedAt.IsZero() {
		job.CreatedAt = time.Now().UTC()
	}
	if job.DeviceID == "" {
		job.DeviceID = a.cfg.DeviceID
	}

	data, err := json.Marshal(job)
	if err != nil {
		return fmt.Errorf("failed to marshal job: %w", err)
	}

	subject := fmt.Sprintf("keystone.%s.jobs", job.DeviceID)

	_, err = a.jsManager.js.Publish(ctx, subject, data)
	if err != nil {
		return fmt.Errorf("failed to publish job: %w", err)
	}

	log.Printf("[nats] job %s published to %s", job.ID, subject)
	return nil
}

// GetPendingJobs returns info about pending jobs in the queue.
func (a *Adapter) GetPendingJobs(ctx context.Context) (int, error) {
	if a.jsManager == nil {
		return 0, fmt.Errorf("JetStream not enabled")
	}

	info, err := a.jsManager.consumer.Info(ctx)
	if err != nil {
		return 0, err
	}

	return int(info.NumPending), nil
}
