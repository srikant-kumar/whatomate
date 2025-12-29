package worker

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
	"github.com/shridarpatil/whatomate/internal/config"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/internal/queue"
	"github.com/shridarpatil/whatomate/pkg/whatsapp"
	"github.com/zerodha/logf"
	"gorm.io/gorm"
)

// Worker processes jobs from the queue
type Worker struct {
	Config   *config.Config
	DB       *gorm.DB
	Redis    *redis.Client
	Log      logf.Logger
	WhatsApp *whatsapp.Client
	Consumer *queue.RedisConsumer
}

// New creates a new Worker instance
func New(cfg *config.Config, db *gorm.DB, rdb *redis.Client, log logf.Logger) (*Worker, error) {
	consumer, err := queue.NewRedisConsumer(rdb, log)
	if err != nil {
		return nil, fmt.Errorf("failed to create consumer: %w", err)
	}

	return &Worker{
		Config:   cfg,
		DB:       db,
		Redis:    rdb,
		Log:      log,
		WhatsApp: whatsapp.New(log),
		Consumer: consumer,
	}, nil
}

// Run starts the worker and processes jobs until context is cancelled
func (w *Worker) Run(ctx context.Context) error {
	w.Log.Info("Worker starting")

	err := w.Consumer.Consume(ctx, w.handleCampaignJob)
	if err != nil && ctx.Err() == nil {
		return fmt.Errorf("consumer error: %w", err)
	}

	w.Log.Info("Worker stopped")
	return nil
}

// handleCampaignJob processes a single campaign job
func (w *Worker) handleCampaignJob(ctx context.Context, job *queue.CampaignJob) error {
	w.Log.Info("Processing campaign job", "campaign_id", job.CampaignID)

	if err := w.processCampaign(ctx, job.CampaignID); err != nil {
		w.Log.Error("Failed to process campaign", "error", err, "campaign_id", job.CampaignID)
		return err
	}

	w.Log.Info("Campaign job completed", "campaign_id", job.CampaignID)
	return nil
}

// processCampaign processes a campaign by sending messages to all recipients
func (w *Worker) processCampaign(ctx context.Context, campaignID uuid.UUID) error {
	w.Log.Info("Processing campaign", "campaign_id", campaignID)

	// Get campaign with template
	var campaign models.BulkMessageCampaign
	if err := w.DB.Where("id = ?", campaignID).Preload("Template").First(&campaign).Error; err != nil {
		w.Log.Error("Failed to load campaign for processing", "error", err, "campaign_id", campaignID)
		return fmt.Errorf("failed to load campaign: %w", err)
	}

	// Check if campaign is still in a startable state
	if campaign.Status != "queued" && campaign.Status != "processing" {
		w.Log.Info("Campaign not in processable state", "campaign_id", campaignID, "status", campaign.Status)
		return nil // Not an error, just skip
	}

	// Get WhatsApp account
	var account models.WhatsAppAccount
	if err := w.DB.Where("name = ? AND organization_id = ?", campaign.WhatsAppAccount, campaign.OrganizationID).First(&account).Error; err != nil {
		w.Log.Error("Failed to load WhatsApp account", "error", err, "account_name", campaign.WhatsAppAccount)
		w.DB.Model(&campaign).Update("status", "failed")
		return fmt.Errorf("failed to load WhatsApp account: %w", err)
	}

	// Update status to processing
	w.DB.Model(&campaign).Update("status", "processing")

	// Get all pending recipients
	var recipients []models.BulkMessageRecipient
	if err := w.DB.Where("campaign_id = ? AND status = ?", campaignID, "pending").Find(&recipients).Error; err != nil {
		w.Log.Error("Failed to load recipients", "error", err, "campaign_id", campaignID)
		w.DB.Model(&campaign).Update("status", "failed")
		return fmt.Errorf("failed to load recipients: %w", err)
	}

	w.Log.Info("Processing recipients", "campaign_id", campaignID, "count", len(recipients))

	sentCount := campaign.SentCount
	failedCount := campaign.FailedCount

	for _, recipient := range recipients {
		// Check context for cancellation
		select {
		case <-ctx.Done():
			w.Log.Info("Campaign processing cancelled by context", "campaign_id", campaignID)
			return ctx.Err()
		default:
		}

		// Check if campaign is still active (not paused/cancelled)
		var currentCampaign models.BulkMessageCampaign
		w.DB.Where("id = ?", campaignID).First(&currentCampaign)
		if currentCampaign.Status == "paused" || currentCampaign.Status == "cancelled" {
			w.Log.Info("Campaign stopped", "campaign_id", campaignID, "status", currentCampaign.Status)
			return nil
		}

		// Send template message
		messageID, err := w.sendTemplateMessage(ctx, &account, campaign.Template, &recipient)
		now := time.Now()

		if err != nil {
			w.Log.Error("Failed to send message", "error", err, "recipient", recipient.PhoneNumber)
			w.DB.Model(&recipient).Updates(map[string]interface{}{
				"status":        "failed",
				"error_message": err.Error(),
			})
			failedCount++
		} else {
			w.Log.Info("Message sent", "recipient", recipient.PhoneNumber, "message_id", messageID)
			w.DB.Model(&recipient).Updates(map[string]interface{}{
				"status":               "sent",
				"whats_app_message_id": messageID,
				"sent_at":              now,
			})
			sentCount++
		}

		// Update campaign counts
		w.DB.Model(&campaign).Updates(map[string]interface{}{
			"sent_count":   sentCount,
			"failed_count": failedCount,
		})

		// Small delay to avoid rate limiting (WhatsApp has rate limits)
		time.Sleep(100 * time.Millisecond)
	}

	// Mark campaign as completed
	now := time.Now()
	w.DB.Model(&campaign).Updates(map[string]interface{}{
		"status":       "completed",
		"completed_at": now,
		"sent_count":   sentCount,
		"failed_count": failedCount,
	})

	w.Log.Info("Campaign completed", "campaign_id", campaignID, "sent", sentCount, "failed", failedCount)
	return nil
}

// sendTemplateMessage sends a template message via WhatsApp Cloud API
func (w *Worker) sendTemplateMessage(ctx context.Context, account *models.WhatsAppAccount, template *models.Template, recipient *models.BulkMessageRecipient) (string, error) {
	waAccount := &whatsapp.Account{
		PhoneID:     account.PhoneID,
		BusinessID:  account.BusinessID,
		APIVersion:  account.APIVersion,
		AccessToken: account.AccessToken,
	}

	// Build template components with parameters
	var components []map[string]interface{}

	// Add body parameters if template has variables
	if recipient.TemplateParams != nil && len(recipient.TemplateParams) > 0 {
		bodyParams := []map[string]interface{}{}
		for i := 1; i <= 10; i++ {
			key := fmt.Sprintf("%d", i)
			if val, ok := recipient.TemplateParams[key]; ok {
				bodyParams = append(bodyParams, map[string]interface{}{
					"type": "text",
					"text": val,
				})
			}
		}
		if len(bodyParams) > 0 {
			components = append(components, map[string]interface{}{
				"type":       "body",
				"parameters": bodyParams,
			})
		}
	}

	return w.WhatsApp.SendTemplateMessageWithComponents(ctx, waAccount, recipient.PhoneNumber, template.Name, template.Language, components)
}

// Close cleans up worker resources
func (w *Worker) Close() error {
	if w.Consumer != nil {
		return w.Consumer.Close()
	}
	return nil
}
