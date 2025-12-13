package server

import (
	"context"
	"encoding/json"
	"time"

	"github.com/heroiclabs/nakama-common/api"
	"github.com/heroiclabs/nakama-common/runtime"
)

const (
	StorageCollectionEnforcementMetrics = "EnforcementMetrics"
	StorageKeyEnforcementMetrics        = "metrics"
)

// EnforcementActionMetrics tracks statistics about enforcement actions
type EnforcementActionMetrics struct {
	GroupID             string                  `json:"group_id"`
	TotalKicks          int                     `json:"total_kicks"`
	TotalSuspensions    int                     `json:"total_suspensions"`
	TotalVoidings       int                     `json:"total_voidings"`
	KicksByRule         map[string]int          `json:"kicks_by_rule"`
	SuspensionsByRule   map[string]int          `json:"suspensions_by_rule"`
	NotificationsSent   int                     `json:"notifications_sent"`
	NotificationsFailed int                     `json:"notifications_failed"`
	ActionsByDate       map[string]DailyMetrics `json:"actions_by_date"` // YYYY-MM-DD format
	LastUpdated         time.Time               `json:"last_updated"`
	version             string
}

// DailyMetrics tracks enforcement actions for a specific day
type DailyMetrics struct {
	Date                string          `json:"date"` // YYYY-MM-DD
	Kicks               int             `json:"kicks"`
	Suspensions         int             `json:"suspensions"`
	Voidings            int             `json:"voidings"`
	UniqueUsersAffected int             `json:"unique_users_affected"`
	AffectedUsers       map[string]bool `json:"-"` // Not serialized, used for counting
}

func NewEnforcementActionMetrics(groupID string) *EnforcementActionMetrics {
	return &EnforcementActionMetrics{
		GroupID:           groupID,
		KicksByRule:       make(map[string]int),
		SuspensionsByRule: make(map[string]int),
		ActionsByDate:     make(map[string]DailyMetrics),
		LastUpdated:       time.Now().UTC(),
		version:           "*",
	}
}

func (m *EnforcementActionMetrics) StorageMeta() StorableMetadata {
	return StorableMetadata{
		Collection:      StorageCollectionEnforcementMetrics,
		Key:             StorageKeyEnforcementMetrics,
		PermissionRead:  runtime.STORAGE_PERMISSION_NO_READ,
		PermissionWrite: runtime.STORAGE_PERMISSION_NO_WRITE,
		Version:         m.version,
		UserID:          m.GroupID,
	}
}

func (m *EnforcementActionMetrics) SetStorageMeta(meta StorableMetadata) {
	m.GroupID = meta.UserID
	m.version = meta.Version
}

func (m *EnforcementActionMetrics) GetStorageVersion() string {
	return m.version
}

func EnforcementActionMetricsFromStorageObject(obj *api.StorageObject) (*EnforcementActionMetrics, error) {
	metrics := &EnforcementActionMetrics{}
	if err := json.Unmarshal([]byte(obj.GetValue()), metrics); err != nil {
		return nil, err
	}
	metrics.GroupID = obj.GetUserId()
	metrics.version = obj.GetVersion()
	return metrics, nil
}

// RecordKick records a kick action in the metrics
func (m *EnforcementActionMetrics) RecordKick(userID, rule string) {
	m.TotalKicks++
	if rule != "" {
		m.KicksByRule[rule]++
	}
	m.recordActionForDate(userID, "kick")
	m.LastUpdated = time.Now().UTC()
}

// RecordSuspension records a suspension action in the metrics
func (m *EnforcementActionMetrics) RecordSuspension(userID, rule string) {
	m.TotalSuspensions++
	if rule != "" {
		m.SuspensionsByRule[rule]++
	}
	m.recordActionForDate(userID, "suspension")
	m.LastUpdated = time.Now().UTC()
}

// RecordVoiding records a voiding action in the metrics
func (m *EnforcementActionMetrics) RecordVoiding(userID string) {
	m.TotalVoidings++
	m.recordActionForDate(userID, "voiding")
	m.LastUpdated = time.Now().UTC()
}

// RecordNotification records a notification attempt
func (m *EnforcementActionMetrics) RecordNotification(sent bool) {
	if sent {
		m.NotificationsSent++
	} else {
		m.NotificationsFailed++
	}
	m.LastUpdated = time.Now().UTC()
}

// recordActionForDate records an action for today's date
func (m *EnforcementActionMetrics) recordActionForDate(userID, actionType string) {
	dateStr := time.Now().UTC().Format("2006-01-02")

	daily, exists := m.ActionsByDate[dateStr]
	if !exists {
		daily = DailyMetrics{
			Date:          dateStr,
			AffectedUsers: make(map[string]bool),
		}
	}

	switch actionType {
	case "kick":
		daily.Kicks++
	case "suspension":
		daily.Suspensions++
	case "voiding":
		daily.Voidings++
	}

	// Track unique users
	if userID != "" {
		if daily.AffectedUsers == nil {
			daily.AffectedUsers = make(map[string]bool)
		}
		daily.AffectedUsers[userID] = true
		daily.UniqueUsersAffected = len(daily.AffectedUsers)
	}

	m.ActionsByDate[dateStr] = daily
}

// GetRecentDailyMetrics returns metrics for the last N days
func (m *EnforcementActionMetrics) GetRecentDailyMetrics(days int) []DailyMetrics {
	if days <= 0 {
		days = 7 // Default to last 7 days
	}

	cutoffDate := time.Now().UTC().AddDate(0, 0, -days).Format("2006-01-02")
	result := make([]DailyMetrics, 0)

	for dateStr, metrics := range m.ActionsByDate {
		if dateStr >= cutoffDate {
			result = append(result, metrics)
		}
	}

	return result
}

// CleanupOldMetrics removes daily metrics older than retention period
func (m *EnforcementActionMetrics) CleanupOldMetrics(retentionDays int) int {
	if retentionDays <= 0 {
		retentionDays = 90 // Default 90 days retention
	}

	cutoffDate := time.Now().UTC().AddDate(0, 0, -retentionDays).Format("2006-01-02")
	removed := 0

	for dateStr := range m.ActionsByDate {
		if dateStr < cutoffDate {
			delete(m.ActionsByDate, dateStr)
			removed++
		}
	}

	return removed
}

// LoadEnforcementMetrics loads the enforcement metrics for a guild
func LoadEnforcementMetrics(ctx context.Context, nk runtime.NakamaModule, groupID string) (*EnforcementActionMetrics, error) {
	metrics := NewEnforcementActionMetrics(groupID)
	if err := StorableRead(ctx, nk, groupID, metrics, true); err != nil {
		// If not found, return a new empty metrics object (don't treat as error)
		return metrics, nil
	}
	return metrics, nil
}

// SaveEnforcementMetrics saves the enforcement metrics for a guild
func SaveEnforcementMetrics(ctx context.Context, nk runtime.NakamaModule, metrics *EnforcementActionMetrics) error {
	// Cleanup old metrics before saving
	metrics.CleanupOldMetrics(90)
	return StorableWrite(ctx, nk, metrics.GroupID, metrics)
}

// RecordEnforcementMetrics records an enforcement action in the metrics
func RecordEnforcementMetrics(ctx context.Context, nk runtime.NakamaModule, record GuildEnforcementRecord, notificationSent bool) error {
	metrics, err := LoadEnforcementMetrics(ctx, nk, record.GroupID)
	if err != nil {
		return err
	}

	if record.IsSuspension() {
		metrics.RecordSuspension(record.UserID, record.RuleViolated)
	} else {
		metrics.RecordKick(record.UserID, record.RuleViolated)
	}

	metrics.RecordNotification(notificationSent)

	return SaveEnforcementMetrics(ctx, nk, metrics)
}

// RecordVoidingMetrics records a voiding action in the metrics
func RecordVoidingMetrics(ctx context.Context, nk runtime.NakamaModule, groupID, userID string) error {
	metrics, err := LoadEnforcementMetrics(ctx, nk, groupID)
	if err != nil {
		return err
	}

	metrics.RecordVoiding(userID)

	return SaveEnforcementMetrics(ctx, nk, metrics)
}
