package handlers

import (
	"time"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/valyala/fasthttp"
	"github.com/zerodha/fastglue"
)

// DashboardStats represents dashboard statistics
type DashboardStats struct {
	TotalMessages   int64   `json:"total_messages"`
	MessagesChange  float64 `json:"messages_change"`
	TotalContacts   int64   `json:"total_contacts"`
	ContactsChange  float64 `json:"contacts_change"`
	ChatbotSessions int64   `json:"chatbot_sessions"`
	ChatbotChange   float64 `json:"chatbot_change"`
	CampaignsSent   int64   `json:"campaigns_sent"`
	CampaignsChange float64 `json:"campaigns_change"`
}

// RecentMessageResponse represents a recent message in the dashboard
type RecentMessageResponse struct {
	ID          string               `json:"id"`
	ContactName string               `json:"contact_name"`
	Content     string               `json:"content"`
	Direction   models.Direction     `json:"direction"`
	CreatedAt   string               `json:"created_at"`
	Status      models.MessageStatus `json:"status"`
}

// GetDashboardStats returns dashboard statistics for the organization
func (a *App) GetDashboardStats(r *fastglue.Request) error {
	orgID, err := a.getOrgIDFromContext(r)
	if err != nil {
		return r.SendErrorEnvelope(fasthttp.StatusUnauthorized, "Unauthorized", nil, "")
	}

	now := time.Now()

	// Parse date range from query params
	fromStr := string(r.RequestCtx.QueryArgs().Peek("from"))
	toStr := string(r.RequestCtx.QueryArgs().Peek("to"))

	var periodStart, periodEnd time.Time
	if fromStr != "" && toStr != "" {
		// Parse custom date range
		periodStart, err = time.Parse("2006-01-02", fromStr)
		if err != nil {
			return r.SendErrorEnvelope(fasthttp.StatusBadRequest, "Invalid 'from' date format. Use YYYY-MM-DD", nil, "")
		}
		periodEnd, err = time.Parse("2006-01-02", toStr)
		if err != nil {
			return r.SendErrorEnvelope(fasthttp.StatusBadRequest, "Invalid 'to' date format. Use YYYY-MM-DD", nil, "")
		}
		// End of day for the to date
		periodEnd = periodEnd.Add(24*time.Hour - time.Nanosecond)
	} else {
		// Default to current month
		periodStart = time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
		periodEnd = now
	}

	// Calculate the previous period for comparison (same duration, before the current period)
	periodDuration := periodEnd.Sub(periodStart)
	previousPeriodStart := periodStart.Add(-periodDuration - time.Nanosecond)
	previousPeriodEnd := periodStart.Add(-time.Nanosecond)

	// Get message counts for the selected period
	var previousPeriodMessages, currentPeriodMessages int64
	a.DB.Model(&models.Message{}).
		Where("organization_id = ? AND created_at >= ? AND created_at <= ?", orgID, previousPeriodStart, previousPeriodEnd).
		Count(&previousPeriodMessages)

	a.DB.Model(&models.Message{}).
		Where("organization_id = ? AND created_at >= ? AND created_at <= ?", orgID, periodStart, periodEnd).
		Count(&currentPeriodMessages)

	messagesChange := calculatePercentageChange(previousPeriodMessages, currentPeriodMessages)

	// Get contact counts for the selected period
	var previousPeriodContacts, currentPeriodContacts int64
	a.DB.Model(&models.Contact{}).
		Where("organization_id = ? AND created_at >= ? AND created_at <= ?", orgID, previousPeriodStart, previousPeriodEnd).
		Count(&previousPeriodContacts)

	a.DB.Model(&models.Contact{}).
		Where("organization_id = ? AND created_at >= ? AND created_at <= ?", orgID, periodStart, periodEnd).
		Count(&currentPeriodContacts)

	contactsChange := calculatePercentageChange(previousPeriodContacts, currentPeriodContacts)

	// Get chatbot session counts for the selected period
	var previousPeriodSessions, currentPeriodSessions int64
	a.DB.Model(&models.ChatbotSession{}).
		Where("organization_id = ? AND created_at >= ? AND created_at <= ?", orgID, previousPeriodStart, previousPeriodEnd).
		Count(&previousPeriodSessions)

	a.DB.Model(&models.ChatbotSession{}).
		Where("organization_id = ? AND created_at >= ? AND created_at <= ?", orgID, periodStart, periodEnd).
		Count(&currentPeriodSessions)

	sessionsChange := calculatePercentageChange(previousPeriodSessions, currentPeriodSessions)

	// Get campaign counts for the selected period
	var previousPeriodCampaigns, currentPeriodCampaigns int64
	a.DB.Model(&models.BulkMessageCampaign{}).
		Where("organization_id = ? AND status IN ('completed', 'processing') AND created_at >= ? AND created_at <= ?", orgID, previousPeriodStart, previousPeriodEnd).
		Count(&previousPeriodCampaigns)

	a.DB.Model(&models.BulkMessageCampaign{}).
		Where("organization_id = ? AND status IN ('completed', 'processing') AND created_at >= ? AND created_at <= ?", orgID, periodStart, periodEnd).
		Count(&currentPeriodCampaigns)

	campaignsChange := calculatePercentageChange(previousPeriodCampaigns, currentPeriodCampaigns)

	stats := DashboardStats{
		TotalMessages:   currentPeriodMessages,
		MessagesChange:  messagesChange,
		TotalContacts:   currentPeriodContacts,
		ContactsChange:  contactsChange,
		ChatbotSessions: currentPeriodSessions,
		ChatbotChange:   sessionsChange,
		CampaignsSent:   currentPeriodCampaigns,
		CampaignsChange: campaignsChange,
	}

	// Get recent messages
	var messages []models.Message
	a.DB.Where("organization_id = ?", orgID).
		Preload("Contact").
		Order("created_at DESC").
		Limit(5).
		Find(&messages)

	recentMessages := make([]RecentMessageResponse, len(messages))
	for i, msg := range messages {
		contactName := "Unknown"
		if msg.Contact != nil {
			if msg.Contact.ProfileName != "" {
				contactName = msg.Contact.ProfileName
			} else {
				contactName = msg.Contact.PhoneNumber
			}
		}

		content := msg.Content
		if content == "" && msg.MessageType != models.MessageTypeText {
			content = "[" + string(msg.MessageType) + "]"
		}

		recentMessages[i] = RecentMessageResponse{
			ID:          msg.ID.String(),
			ContactName: contactName,
			Content:     content,
			Direction:   msg.Direction,
			CreatedAt:   msg.CreatedAt.Format(time.RFC3339),
			Status:      msg.Status,
		}
	}

	return r.SendEnvelope(map[string]interface{}{
		"stats":           stats,
		"recent_messages": recentMessages,
	})
}

// calculatePercentageChange calculates the percentage change between two values
func calculatePercentageChange(previous, current int64) float64 {
	if previous == 0 {
		if current > 0 {
			return 100.0
		}
		return 0.0
	}
	return float64(current-previous) / float64(previous) * 100.0
}

// TimelineEntry represents a single entry in the analytics timeline
type TimelineEntry struct {
	Date      string `json:"date"`
	Sent      int64  `json:"sent"`
	Received  int64  `json:"received"`
	Delivered int64  `json:"delivered"`
	Read      int64  `json:"read"`
}

// GetMessageAnalytics returns message analytics for the organization
// GET /api/analytics/messages
func (a *App) GetMessageAnalytics(r *fastglue.Request) error {
	orgID, err := getOrganizationID(r)
	if err != nil {
		return r.SendErrorEnvelope(fasthttp.StatusUnauthorized, "Unauthorized", nil, "")
	}

	userID, _ := r.RequestCtx.UserValue("user_id").(uuid.UUID)

	// Check permission - need analytics:read to view analytics
	if !a.HasPermission(userID, models.ResourceAnalytics, models.ActionRead) {
		return r.SendErrorEnvelope(fasthttp.StatusForbidden, "Insufficient permissions", nil, "")
	}

	// Parse date range - use start_date and end_date to match docs
	startDateStr := string(r.RequestCtx.QueryArgs().Peek("start_date"))
	endDateStr := string(r.RequestCtx.QueryArgs().Peek("end_date"))

	now := time.Now()
	var periodStart, periodEnd time.Time

	if startDateStr != "" && endDateStr != "" {
		periodStart, err = time.Parse(time.RFC3339, startDateStr)
		if err != nil {
			// Try YYYY-MM-DD format
			periodStart, err = time.Parse("2006-01-02", startDateStr)
			if err != nil {
				return r.SendErrorEnvelope(fasthttp.StatusBadRequest, "Invalid date format. Use ISO 8601 format", nil, "")
			}
		}
		periodEnd, err = time.Parse(time.RFC3339, endDateStr)
		if err != nil {
			periodEnd, err = time.Parse("2006-01-02", endDateStr)
			if err != nil {
				return r.SendErrorEnvelope(fasthttp.StatusBadRequest, "Invalid date format. Use ISO 8601 format", nil, "")
			}
		}
		if periodEnd.Hour() == 0 && periodEnd.Minute() == 0 {
			periodEnd = periodEnd.Add(24*time.Hour - time.Nanosecond)
		}
	} else {
		// Default to current month
		periodStart = time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
		periodEnd = now
	}

	// Calculate summary stats
	var totalSent, totalReceived, totalDelivered, totalRead, totalFailed int64

	// Total sent (outgoing messages)
	a.DB.Model(&models.Message{}).
		Where("organization_id = ? AND direction = ? AND created_at >= ? AND created_at <= ?", orgID, models.DirectionOutgoing, periodStart, periodEnd).
		Count(&totalSent)

	// Total received (incoming messages)
	a.DB.Model(&models.Message{}).
		Where("organization_id = ? AND direction = ? AND created_at >= ? AND created_at <= ?", orgID, models.DirectionIncoming, periodStart, periodEnd).
		Count(&totalReceived)

	// Total delivered
	a.DB.Model(&models.Message{}).
		Where("organization_id = ? AND status = ? AND created_at >= ? AND created_at <= ?", orgID, models.MessageStatusDelivered, periodStart, periodEnd).
		Count(&totalDelivered)

	// Total read
	a.DB.Model(&models.Message{}).
		Where("organization_id = ? AND status = ? AND created_at >= ? AND created_at <= ?", orgID, models.MessageStatusRead, periodStart, periodEnd).
		Count(&totalRead)

	// Total failed
	a.DB.Model(&models.Message{}).
		Where("organization_id = ? AND status = ? AND created_at >= ? AND created_at <= ?", orgID, models.MessageStatusFailed, periodStart, periodEnd).
		Count(&totalFailed)

	// Calculate rates
	var deliveryRate, readRate float64
	if totalSent > 0 {
		deliveryRate = float64(totalDelivered) / float64(totalSent) * 100
		readRate = float64(totalRead) / float64(totalSent) * 100
	}

	// Messages by type
	type TypeCount struct {
		Type  string
		Count int64
	}
	var typeCounts []TypeCount
	a.DB.Model(&models.Message{}).
		Select("message_type as type, COUNT(*) as count").
		Where("organization_id = ? AND created_at >= ? AND created_at <= ?", orgID, periodStart, periodEnd).
		Group("message_type").
		Scan(&typeCounts)

	byType := make(map[string]int64)
	for _, tc := range typeCounts {
		byType[tc.Type] = tc.Count
	}

	// Build timeline (messages by date with breakdown)
	type DateResult struct {
		Date      string
		Sent      int64
		Received  int64
		Delivered int64
		Read      int64
	}
	var dateResults []DateResult
	a.DB.Raw(`
		SELECT
			DATE(created_at) as date,
			SUM(CASE WHEN direction = 'outgoing' THEN 1 ELSE 0 END) as sent,
			SUM(CASE WHEN direction = 'incoming' THEN 1 ELSE 0 END) as received,
			SUM(CASE WHEN status = 'delivered' THEN 1 ELSE 0 END) as delivered,
			SUM(CASE WHEN status = 'read' THEN 1 ELSE 0 END) as read
		FROM messages
		WHERE organization_id = ? AND created_at >= ? AND created_at <= ?
		GROUP BY DATE(created_at)
		ORDER BY date ASC
	`, orgID, periodStart, periodEnd).Scan(&dateResults)

	timeline := make([]map[string]any, len(dateResults))
	for i, dr := range dateResults {
		timeline[i] = map[string]any{
			"date":      dr.Date,
			"sent":      dr.Sent,
			"received":  dr.Received,
			"delivered": dr.Delivered,
			"read":      dr.Read,
		}
	}

	response := map[string]any{
		"summary": map[string]any{
			"total_sent":      totalSent,
			"total_received":  totalReceived,
			"total_delivered": totalDelivered,
			"total_read":      totalRead,
			"total_failed":    totalFailed,
			"delivery_rate":   deliveryRate,
			"read_rate":       readRate,
		},
		"by_type":  byType,
		"timeline": timeline,
	}

	return r.SendEnvelope(response)
}

// GetChatbotAnalytics returns chatbot analytics for the organization
// GET /api/analytics/chatbot
func (a *App) GetChatbotAnalytics(r *fastglue.Request) error {
	orgID, err := getOrganizationID(r)
	if err != nil {
		return r.SendErrorEnvelope(fasthttp.StatusUnauthorized, "Unauthorized", nil, "")
	}

	userID, _ := r.RequestCtx.UserValue("user_id").(uuid.UUID)

	// Check permission - need analytics:read to view analytics
	if !a.HasPermission(userID, models.ResourceAnalytics, models.ActionRead) {
		return r.SendErrorEnvelope(fasthttp.StatusForbidden, "Insufficient permissions", nil, "")
	}

	// Parse date range - use start_date and end_date to match docs
	startDateStr := string(r.RequestCtx.QueryArgs().Peek("start_date"))
	endDateStr := string(r.RequestCtx.QueryArgs().Peek("end_date"))

	now := time.Now()
	var periodStart, periodEnd time.Time

	if startDateStr != "" && endDateStr != "" {
		periodStart, err = time.Parse(time.RFC3339, startDateStr)
		if err != nil {
			// Try YYYY-MM-DD format
			periodStart, err = time.Parse("2006-01-02", startDateStr)
			if err != nil {
				return r.SendErrorEnvelope(fasthttp.StatusBadRequest, "Invalid date format. Use ISO 8601 format", nil, "")
			}
		}
		periodEnd, err = time.Parse(time.RFC3339, endDateStr)
		if err != nil {
			periodEnd, err = time.Parse("2006-01-02", endDateStr)
			if err != nil {
				return r.SendErrorEnvelope(fasthttp.StatusBadRequest, "Invalid date format. Use ISO 8601 format", nil, "")
			}
		}
		if periodEnd.Hour() == 0 && periodEnd.Minute() == 0 {
			periodEnd = periodEnd.Add(24*time.Hour - time.Nanosecond)
		}
	} else {
		// Default to current month
		periodStart = time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
		periodEnd = now
	}

	// Calculate summary stats
	var totalConversations, autoResolved, transferredToAgent, abandoned int64

	// Total conversations (sessions)
	a.DB.Model(&models.ChatbotSession{}).
		Where("organization_id = ? AND started_at >= ? AND started_at <= ?", orgID, periodStart, periodEnd).
		Count(&totalConversations)

	// Auto resolved (completed sessions)
	a.DB.Model(&models.ChatbotSession{}).
		Where("organization_id = ? AND status = ? AND started_at >= ? AND started_at <= ?", orgID, models.SessionStatusCompleted, periodStart, periodEnd).
		Count(&autoResolved)

	// Transferred to agent (cancelled sessions - assuming cancelled means transferred)
	a.DB.Model(&models.ChatbotSession{}).
		Where("organization_id = ? AND status = ? AND started_at >= ? AND started_at <= ?", orgID, models.SessionStatusCancelled, periodStart, periodEnd).
		Count(&transferredToAgent)

	// Abandoned (timeout sessions)
	a.DB.Model(&models.ChatbotSession{}).
		Where("organization_id = ? AND status = ? AND started_at >= ? AND started_at <= ?", orgID, models.SessionStatusTimeout, periodStart, periodEnd).
		Count(&abandoned)

	// Calculate resolution rate
	var resolutionRate float64
	if totalConversations > 0 {
		resolutionRate = float64(autoResolved) / float64(totalConversations) * 100
	}

	// Average messages per conversation (placeholder - would need to join with messages)
	avgMessagesPerConversation := 5.2 // Default placeholder

	// Average resolution time in seconds
	type AvgResult struct {
		Avg float64
	}
	var avgResult AvgResult
	a.DB.Model(&models.ChatbotSession{}).
		Select("AVG(EXTRACT(EPOCH FROM (completed_at - started_at))) as avg").
		Where("organization_id = ? AND status = ? AND started_at >= ? AND started_at <= ?", orgID, models.SessionStatusCompleted, periodStart, periodEnd).
		Scan(&avgResult)
	avgResolutionTimeSeconds := int64(avgResult.Avg)

	// By flow statistics
	type FlowStatResult struct {
		FlowName       string
		Conversations  int64
		CompletedCount int64
	}
	var flowStats []FlowStatResult
	a.DB.Raw(`
		SELECT
			COALESCE(cf.name, 'Unknown') as flow_name,
			COUNT(*) as conversations,
			SUM(CASE WHEN cs.status = 'completed' THEN 1 ELSE 0 END) as completed_count
		FROM chatbot_sessions cs
		LEFT JOIN chatbot_flows cf ON cf.id = cs.current_flow_id
		WHERE cs.organization_id = ? AND cs.started_at >= ? AND cs.started_at <= ?
		GROUP BY cf.name
		ORDER BY conversations DESC
	`, orgID, periodStart, periodEnd).Scan(&flowStats)

	byFlow := make([]map[string]any, len(flowStats))
	for i, fs := range flowStats {
		completionRate := 0.0
		if fs.Conversations > 0 {
			completionRate = float64(fs.CompletedCount) / float64(fs.Conversations) * 100
		}
		byFlow[i] = map[string]any{
			"flow_name":       fs.FlowName,
			"conversations":   fs.Conversations,
			"completion_rate": completionRate,
		}
	}

	// Top keywords (placeholder - would need keyword tracking)
	topKeywords := []map[string]any{
		{"keyword": "order", "count": 450},
		{"keyword": "shipping", "count": 230},
		{"keyword": "return", "count": 180},
	}

	// AI usage (placeholder - would need AI usage tracking)
	aiUsage := map[string]any{
		"total_requests":         500,
		"avg_tokens_per_request": 250,
		"total_tokens":           125000,
		"estimated_cost":         2.50,
	}

	response := map[string]any{
		"summary": map[string]any{
			"total_conversations":          totalConversations,
			"auto_resolved":                autoResolved,
			"transferred_to_agent":         transferredToAgent,
			"abandoned":                    abandoned,
			"resolution_rate":              resolutionRate,
			"avg_messages_per_conversation": avgMessagesPerConversation,
			"avg_resolution_time_seconds":  avgResolutionTimeSeconds,
		},
		"by_flow":      byFlow,
		"top_keywords": topKeywords,
		"ai_usage":     aiUsage,
	}

	return r.SendEnvelope(response)
}
