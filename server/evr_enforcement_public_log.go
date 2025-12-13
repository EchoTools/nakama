package server

import (
	"context"
	"encoding/json"
	"time"

	"github.com/heroiclabs/nakama-common/api"
	"github.com/heroiclabs/nakama-common/runtime"
)

const (
	StorageCollectionPublicEnforcementLog = "PublicEnforcementLog"
	StorageKeyPublicEnforcementLog        = "log"
	DefaultPublicLogRetentionDays         = 90 // 90 days default retention
)

// PublicEnforcementLogEntry represents a sanitized, public-facing enforcement record
// Contains only information that is appropriate for public disclosure
type PublicEnforcementLogEntry struct {
	ID           string    `json:"id"`            // Record ID
	GroupID      string    `json:"group_id"`      // Guild/Group identifier
	Timestamp    time.Time `json:"timestamp"`     // When the action occurred
	ActionType   string    `json:"action_type"`   // "kick", "suspension", "warning"
	Reason       string    `json:"reason"`        // User-facing reason (no internal notes)
	RuleViolated string    `json:"rule_violated"` // Standardized rule category
	Duration     string    `json:"duration"`      // Human-readable duration (e.g., "7d", "permanent")
	ExpiresAt    time.Time `json:"expires_at"`    // When the suspension expires (zero for kicks)
	IsActive     bool      `json:"is_active"`     // Whether the enforcement is still active
	Voided       bool      `json:"voided"`        // Whether this record was voided
	VoidedAt     time.Time `json:"voided_at"`     // When it was voided
}

// PublicEnforcementLog stores public enforcement records for a guild
type PublicEnforcementLog struct {
	GroupID       string                      `json:"group_id"`
	Enabled       bool                        `json:"enabled"`        // Whether public logging is enabled
	RetentionDays int                         `json:"retention_days"` // How long to keep logs
	Entries       []PublicEnforcementLogEntry `json:"entries"`
	LastCleanup   time.Time                   `json:"last_cleanup"`
	version       string
}

func NewPublicEnforcementLog(groupID string) *PublicEnforcementLog {
	return &PublicEnforcementLog{
		GroupID:       groupID,
		Enabled:       false, // Opt-in by default
		RetentionDays: DefaultPublicLogRetentionDays,
		Entries:       make([]PublicEnforcementLogEntry, 0),
		LastCleanup:   time.Now().UTC(),
		version:       "*",
	}
}

func (p *PublicEnforcementLog) StorageMeta() StorableMetadata {
	return StorableMetadata{
		Collection:      StorageCollectionPublicEnforcementLog,
		Key:             StorageKeyPublicEnforcementLog,
		PermissionRead:  runtime.STORAGE_PERMISSION_PUBLIC_READ, // Public read
		PermissionWrite: runtime.STORAGE_PERMISSION_NO_WRITE,    // Only server can write
		Version:         p.version,
		UserID:          p.GroupID, // Use GroupID as the storage owner
	}
}

func (p *PublicEnforcementLog) SetStorageMeta(meta StorableMetadata) {
	p.GroupID = meta.UserID
	p.version = meta.Version
}

func (p *PublicEnforcementLog) GetStorageVersion() string {
	return p.version
}

func PublicEnforcementLogFromStorageObject(obj *api.StorageObject) (*PublicEnforcementLog, error) {
	log := &PublicEnforcementLog{}
	if err := json.Unmarshal([]byte(obj.GetValue()), log); err != nil {
		return nil, err
	}
	log.GroupID = obj.GetUserId()
	log.version = obj.GetVersion()
	return log, nil
}

// AddEntry adds a new public enforcement log entry from a private record
// Filters out sensitive information (enforcer identity, internal notes)
func (p *PublicEnforcementLog) AddEntry(record GuildEnforcementRecord, voided bool, voidedAt time.Time) {
	if !p.Enabled {
		return // Don't log if public logging is disabled
	}

	if !record.IsPubliclyVisible {
		return // Don't log if the record is marked as private
	}

	actionType := "kick"
	duration := "immediate"
	if record.IsSuspension() {
		actionType = "suspension"
		duration = FormatDuration(record.Expiry.Sub(record.CreatedAt))
	}

	entry := PublicEnforcementLogEntry{
		ID:           record.ID,
		GroupID:      record.GroupID,
		Timestamp:    record.CreatedAt,
		ActionType:   actionType,
		Reason:       record.UserNoticeText, // Only user-facing text, not internal notes
		RuleViolated: record.RuleViolated,
		Duration:     duration,
		ExpiresAt:    record.Expiry,
		IsActive:     !record.IsExpired(),
		Voided:       voided,
		VoidedAt:     voidedAt,
	}

	p.Entries = append(p.Entries, entry)
}

// CleanupExpiredEntries removes entries older than the retention period
func (p *PublicEnforcementLog) CleanupExpiredEntries() int {
	if p.RetentionDays <= 0 {
		return 0 // No cleanup if retention is disabled
	}

	cutoffTime := time.Now().UTC().AddDate(0, 0, -p.RetentionDays)
	originalCount := len(p.Entries)

	// Filter out entries older than retention period
	validEntries := make([]PublicEnforcementLogEntry, 0, len(p.Entries))
	for _, entry := range p.Entries {
		if entry.Timestamp.After(cutoffTime) {
			validEntries = append(validEntries, entry)
		}
	}

	p.Entries = validEntries
	p.LastCleanup = time.Now().UTC()

	return originalCount - len(p.Entries)
}

// GetActiveEntries returns only currently active enforcement entries
func (p *PublicEnforcementLog) GetActiveEntries() []PublicEnforcementLogEntry {
	active := make([]PublicEnforcementLogEntry, 0)
	now := time.Now().UTC()

	for _, entry := range p.Entries {
		if entry.Voided {
			continue // Skip voided entries
		}
		if entry.ActionType == "suspension" && !entry.ExpiresAt.IsZero() && now.After(entry.ExpiresAt) {
			continue // Skip expired suspensions
		}
		active = append(active, entry)
	}

	return active
}

// GetRecentEntries returns entries from the last N days
func (p *PublicEnforcementLog) GetRecentEntries(days int) []PublicEnforcementLogEntry {
	if days <= 0 {
		return p.Entries // Return all if days is 0 or negative
	}

	cutoffTime := time.Now().UTC().AddDate(0, 0, -days)
	recent := make([]PublicEnforcementLogEntry, 0)

	for _, entry := range p.Entries {
		if entry.Timestamp.After(cutoffTime) {
			recent = append(recent, entry)
		}
	}

	return recent
}

// LoadPublicEnforcementLog loads the public enforcement log for a guild
func LoadPublicEnforcementLog(ctx context.Context, nk runtime.NakamaModule, groupID string) (*PublicEnforcementLog, error) {
	log := NewPublicEnforcementLog(groupID)
	if err := StorableRead(ctx, nk, groupID, log, true); err != nil {
		// If not found, return a new empty log (don't treat as error)
		return log, nil
	}
	return log, nil
}

// SavePublicEnforcementLog saves the public enforcement log for a guild
func SavePublicEnforcementLog(ctx context.Context, nk runtime.NakamaModule, log *PublicEnforcementLog) error {
	// Cleanup old entries before saving
	log.CleanupExpiredEntries()
	return StorableWrite(ctx, nk, log.GroupID, log)
}

// RecordPublicEnforcement adds an enforcement action to the public log if enabled
func RecordPublicEnforcement(ctx context.Context, nk runtime.NakamaModule, record GuildEnforcementRecord, voided bool, voidedAt time.Time) error {
	log, err := LoadPublicEnforcementLog(ctx, nk, record.GroupID)
	if err != nil {
		return err
	}

	if !log.Enabled {
		return nil // Public logging is disabled, skip silently
	}

	log.AddEntry(record, voided, voidedAt)
	return SavePublicEnforcementLog(ctx, nk, log)
}
