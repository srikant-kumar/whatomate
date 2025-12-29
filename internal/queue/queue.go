package queue

import (
	"context"
	"time"

	"github.com/google/uuid"
)

// JobType represents the type of job
type JobType string

const (
	// JobTypeCampaign is for processing bulk message campaigns
	JobTypeCampaign JobType = "campaign"
)

// CampaignJob represents a campaign processing job
type CampaignJob struct {
	CampaignID uuid.UUID `json:"campaign_id"`
	EnqueuedAt time.Time `json:"enqueued_at"`
}

// Job represents a generic job in the queue
type Job struct {
	ID         string      `json:"id"`
	Type       JobType     `json:"type"`
	Payload    interface{} `json:"payload"`
	EnqueuedAt time.Time   `json:"enqueued_at"`
}

// Queue defines the interface for job queue operations
type Queue interface {
	// EnqueueCampaign adds a campaign processing job to the queue
	EnqueueCampaign(ctx context.Context, campaignID uuid.UUID) error

	// Close closes the queue connection
	Close() error
}

// Consumer defines the interface for consuming jobs from the queue
type Consumer interface {
	// Consume starts consuming jobs from the queue
	// The handler function is called for each job
	// Returns when context is cancelled
	Consume(ctx context.Context, handler func(ctx context.Context, job *CampaignJob) error) error

	// Close closes the consumer connection
	Close() error
}
