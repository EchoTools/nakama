package server

import (
	"context"
	"encoding/json"
	"time"

	"github.com/heroiclabs/nakama-common/api"
	"github.com/heroiclabs/nakama-common/runtime"
)

const (
	StorageCollectionSuspensionProfile = "SuspensionProfile"
	StorageKeySuspensionProfile        = "profile"
	StorageIndexSuspensionProfile      = "StorageIndexSuspensionProfile"
)

// SuspensionProfileRecord represents a single suspension in the user-readable profile format
// This is optimized for portal display and frontend consumption
type SuspensionProfileRecord struct {
	// Identifiers
	ID      string `json:"id"`
	GroupID string `json:"group_id"`

	// Suspension details (user-facing)
	UserNotice   string `json:"user_notice"`
	AuditorNotes string `json:"auditor_notes,omitempty"` // May be empty if user doesn't have access

	// Timing information
	CreatedAt  time.Time `json:"created_at"`
	ExpiryAt   time.Time `json:"expiry_at"`
	IsLifetime bool      `json:"is_lifetime"`
	Duration   string    `json:"duration"` // Human-readable format (e.g., "7d", "lifetime")

	// Enforcement information
	EnforcerUserID    string `json:"enforcer_user_id"`
	EnforcerDiscordID string `json:"enforcer_discord_id"`

	// Edit information
	LastEditedBy string    `json:"last_edited_by,omitempty"`
	LastEditedAt time.Time `json:"last_edited_at,omitempty"`

	// Void information (if this suspension was voided)
	VoidedAt  time.Time `json:"voided_at,omitempty"`
	VoidedBy  string    `json:"voided_by,omitempty"`
	VoidNotes string    `json:"void_notes,omitempty"`
}

// SuspensionProfile is the user-readable version of the enforcement journal
// Permission: OWNER_READ (400) - Only the player can read their own profile
// This is maintained in sync with GuildEnforcementJournal for portal display
type SuspensionProfile struct {
	UserID      string                    `json:"user_id"`
	Suspensions []SuspensionProfileRecord `json:"suspensions"`
	UpdatedAt   time.Time                 `json:"updated_at"`
	version     string
}

func NewSuspensionProfile(userID string) *SuspensionProfile {
	return &SuspensionProfile{
		UserID:      userID,
		Suspensions: make([]SuspensionProfileRecord, 0),
		UpdatedAt:   time.Now().UTC(),
		version:     "*",
	}
}

func (s *SuspensionProfile) StorageMeta() StorableMetadata {
	return StorableMetadata{
		Collection:      StorageCollectionSuspensionProfile,
		Key:             StorageKeySuspensionProfile,
		PermissionRead:  runtime.STORAGE_PERMISSION_OWNER_READ, // 400 - Only owner can read
		PermissionWrite: runtime.STORAGE_PERMISSION_NO_WRITE,   // Server-managed only
		Version:         s.version,
	}
}

func (s *SuspensionProfile) SetStorageMeta(meta StorableMetadata) {
	s.UserID = meta.UserID
	s.version = meta.Version
}

func (s SuspensionProfile) GetStorageVersion() string {
	return s.version
}

func (s *SuspensionProfile) StorageIndexes() []StorableIndexMeta {
	return []StorableIndexMeta{{
		Name:       StorageIndexSuspensionProfile,
		Collection: StorageCollectionSuspensionProfile,
		Key:        StorageKeySuspensionProfile,
		Fields:     []string{"user_id"},
		MaxEntries: 10000,
		IndexOnly:  true,
	}}
}

func SuspensionProfileFromStorageObject(obj *api.StorageObject) (*SuspensionProfile, error) {
	profile := &SuspensionProfile{}
	if err := json.Unmarshal([]byte(obj.GetValue()), profile); err != nil {
		return nil, err
	}
	profile.UserID = obj.GetUserId()
	profile.version = obj.GetVersion()
	return profile, nil
}

func (s SuspensionProfile) MarshalJSON() ([]byte, error) {
	s.UpdatedAt = time.Now().UTC()
	type Alias SuspensionProfile
	b, err := json.Marshal(&struct {
		*Alias
	}{
		Alias: (*Alias)(&s),
	})
	if err != nil {
		return nil, err
	}
	return b, nil
}

// SyncFromJournal creates a SuspensionProfile from a GuildEnforcementJournal
// This builds the user-readable profile from the internal journal
func (s *SuspensionProfile) SyncFromJournal(journal *GuildEnforcementJournal) {
	s.UserID = journal.UserID
	s.Suspensions = make([]SuspensionProfileRecord, 0)

	// Iterate through all records in the journal
	for groupID, records := range journal.RecordsByGroupID {
		for _, record := range records {
			// Build the profile record
			profileRec := SuspensionProfileRecord{
				ID:                record.ID,
				GroupID:           groupID,
				UserNotice:        record.UserNoticeText,
				AuditorNotes:      record.AuditorNotes,
				CreatedAt:         record.CreatedAt,
				ExpiryAt:          record.Expiry,
				IsLifetime:        record.IsLifetime(),
				Duration:          FormatDuration(record.Expiry.Sub(record.CreatedAt)),
				EnforcerUserID:    record.EnforcerUserID,
				EnforcerDiscordID: record.EnforcerDiscordID,
			}

			// Add editor information if available
			if len(record.EditLog) > 0 {
				lastEdit := record.EditLog[len(record.EditLog)-1]
				profileRec.LastEditedBy = lastEdit.EditorUserID
				profileRec.LastEditedAt = lastEdit.EditedAt
			}

			// Add void information if applicable
			if void, found := journal.GetVoid(groupID, record.ID); found {
				profileRec.VoidedAt = void.VoidedAt
				profileRec.VoidedBy = void.AuthorID
				profileRec.VoidNotes = void.Notes
			}

			s.Suspensions = append(s.Suspensions, profileRec)
		}
	}

	s.UpdatedAt = time.Now().UTC()
}

// SyncJournalAndProfile updates both the enforcement journal and suspension profile in storage
// This ensures they stay in sync whenever enforcement data changes
func SyncJournalAndProfile(ctx context.Context, nk runtime.NakamaModule, userID string, journal *GuildEnforcementJournal) error {
	// Create profile from journal
	profile := NewSuspensionProfile(userID)
	profile.SyncFromJournal(journal)

	// Write both to storage
	if err := StorableWrite(ctx, nk, userID, journal); err != nil {
		return err
	}

	if err := StorableWrite(ctx, nk, userID, profile); err != nil {
		return err
	}

	return nil
}

// SyncJournalAndProfileWithRetry syncs journal and profile with retry logic for concurrent writes.
// This handles version conflicts by retrying with exponential backoff.
func SyncJournalAndProfileWithRetry(ctx context.Context, nk runtime.NakamaModule, userID string, journal *GuildEnforcementJournal) error {
	const maxRetries = 3
	var lastErr error

	for attempt := 0; attempt < maxRetries; attempt++ {
		// Load existing profile from storage to get the current version, or create if it doesn't exist
		profile := NewSuspensionProfile(userID)
		if err := StorableRead(ctx, nk, userID, profile, true); err != nil {
			// If we can't read (and create) the profile, try to just create a new one
			lastErr = err
			profile = NewSuspensionProfile(userID)
		}
		// Update profile with latest journal data
		profile.SyncFromJournal(journal)

		// Try to write both to storage
		if err := StorableWrite(ctx, nk, userID, journal); err != nil {
			lastErr = err
			// Check if this is a version conflict
			if attempt < maxRetries-1 {
				// Exponential backoff: 10ms, 20ms, 40ms
				backoff := time.Duration(10*(1<<uint(attempt))) * time.Millisecond
				time.Sleep(backoff)
				continue
			}
			return err
		}

		if err := StorableWrite(ctx, nk, userID, profile); err != nil {
			lastErr = err
			// Check if this is a version conflict
			if attempt < maxRetries-1 {
				// Exponential backoff: 10ms, 20ms, 40ms
				backoff := time.Duration(10*(1<<uint(attempt))) * time.Millisecond
				time.Sleep(backoff)
				continue
			}
			return err
		}

		// Success
		return nil
	}

	return lastErr
}
