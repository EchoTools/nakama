package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"

	"github.com/heroiclabs/nakama-common/runtime"
	"github.com/heroiclabs/nakama/v3/server/evr"
	"go.uber.org/zap"
)

const (
	StorageCollectionServerProfile = "ServerProfile"
	StorageKeyServerProfile        = "profile"
	ServerProfileIndexName         = "Index_ServerProfile"
	ServerProfileMaxEntries        = 1000 // Keep at least 1000 profiles indexed in memory
)

// ServerProfileStorage stores the generated ServerProfile for quick retrieval.
// It implements StorableIndexer to maintain an in-memory index of profiles.
// The profile is stored as raw JSON to avoid marshal/unmarshal overhead when
// serving profiles to other users via OtherUserProfileRequest.
type ServerProfileStorage struct {
	// Indexed field for searching by XPID
	XPlatformID string `json:"xplatformid"`

	// The actual profile data stored as raw JSON to avoid marshal/unmarshal overhead
	Profile json.RawMessage `json:"profile"`

	// Private fields for storage metadata
	userID  string
	version string
}

// StorageMeta implements the StorableAdapter interface
func (s *ServerProfileStorage) StorageMeta() StorableMetadata {
	return StorableMetadata{
		Collection:      StorageCollectionServerProfile,
		Key:             StorageKeyServerProfile,
		PermissionRead:  runtime.STORAGE_PERMISSION_NO_READ,
		PermissionWrite: runtime.STORAGE_PERMISSION_NO_WRITE,
		UserID:          s.userID,
		Version:         s.version,
	}
}

// SetStorageMeta implements the StorableAdapter interface
func (s *ServerProfileStorage) SetStorageMeta(meta StorableMetadata) {
	s.userID = meta.UserID
	s.version = meta.Version
}

// StorageIndexes implements the StorableIndexer interface
func (s *ServerProfileStorage) StorageIndexes() []StorableIndexMeta {
	return []StorableIndexMeta{{
		Name:           ServerProfileIndexName,
		Collection:     StorageCollectionServerProfile,
		Key:            StorageKeyServerProfile,
		Fields:         []string{"xplatformid"}, // Index by XPID for quick lookups
		SortableFields: nil,
		MaxEntries:     ServerProfileMaxEntries, // Keep at least 1000 profiles in memory
		IndexOnly:      false,                   // Return full objects, not just index data
	}}
}

// NewServerProfileStorage creates a new ServerProfileStorage from a ServerProfile
func NewServerProfileStorage(userID string, profile *evr.ServerProfile) (*ServerProfileStorage, error) {
	profileJSON, err := json.Marshal(profile)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal profile: %w", err)
	}
	return &ServerProfileStorage{
		XPlatformID: profile.EvrID.String(),
		Profile:     profileJSON,
		userID:      userID,
		version:     "", // Will be set on write
	}, nil
}

// ServerProfileLoad loads a ServerProfile, checking storage first, then generating from EVRProfile
func ServerProfileLoad(ctx context.Context, logger *zap.Logger, db *sql.DB, nk runtime.NakamaModule, userID string, xpID evr.EvrId, groupID string, modes []evr.Symbol, dailyWeeklyMode evr.Symbol) (*evr.ServerProfile, error) {
	// First, try to load from storage (which uses the index if available)
	storage := &ServerProfileStorage{}
	if err := StorableRead(ctx, nk, userID, storage, false); err == nil && len(storage.Profile) > 0 {
		// Found in storage, unmarshal and return
		profile := &evr.ServerProfile{}
		if err := json.Unmarshal(storage.Profile, profile); err != nil {
			logger.Warn("Failed to unmarshal cached server profile, regenerating", zap.Error(err))
		} else {
			return profile, nil
		}
	}

	// Not in storage, generate from EVRProfile
	evrProfile, err := EVRProfileLoad(ctx, nk, userID)
	if err != nil {
		return nil, fmt.Errorf("failed to load EVR profile: %w", err)
	}

	displayName := evrProfile.GetGroupIGN(groupID)

	// Generate the server profile
	serverProfile, err := NewUserServerProfile(ctx, logger, db, nk, evrProfile, xpID, groupID, modes, dailyWeeklyMode, displayName)
	if err != nil {
		return nil, fmt.Errorf("failed to generate server profile: %w", err)
	}

	// Store the generated profile
	if err := ServerProfileStore(ctx, nk, userID, serverProfile); err != nil {
		// Log but don't fail - we have the profile even if we can't cache it
		logger.Warn("Failed to store generated server profile", zap.Error(err))
	}

	return serverProfile, nil
}

// ServerProfileStore stores a ServerProfile to storage
func ServerProfileStore(ctx context.Context, nk runtime.NakamaModule, userID string, profile *evr.ServerProfile) error {
	storage, err := NewServerProfileStorage(userID, profile)
	if err != nil {
		return err
	}
	return StorableWrite(ctx, nk, userID, storage)
}

// ServerProfileLoadByXPID searches for a ServerProfile by XPID using the storage index.
// Returns the raw JSON profile data to avoid unnecessary marshal/unmarshal when
// the profile will be sent as json.RawMessage to other users.
func ServerProfileLoadByXPID(ctx context.Context, nk runtime.NakamaModule, xpID evr.EvrId) (json.RawMessage, string, error) {
	// Search the storage index for the XPID
	query := fmt.Sprintf("+xplatformid:%s", xpID.String())

	objects, _, err := nk.StorageIndexList(ctx, SystemUserID, ServerProfileIndexName, query, 1, nil, "")
	if err != nil {
		return nil, "", fmt.Errorf("failed to search server profile index: %w", err)
	}

	if len(objects.Objects) == 0 {
		return nil, "", nil // Not found
	}

	obj := objects.Objects[0]
	storage := &ServerProfileStorage{}
	if err := json.Unmarshal([]byte(obj.Value), storage); err != nil {
		return nil, "", fmt.Errorf("failed to unmarshal server profile storage: %w", err)
	}

	return storage.Profile, obj.UserId, nil
}

// ServerProfileDelete removes a ServerProfile from storage
func ServerProfileDelete(ctx context.Context, nk runtime.NakamaModule, userID string) error {
	return nk.StorageDelete(ctx, []*runtime.StorageDelete{{
		Collection: StorageCollectionServerProfile,
		Key:        StorageKeyServerProfile,
		UserID:     userID,
	}})
}

// ServerProfileInvalidate invalidates a cached server profile by deleting it
// This should be called whenever the EVRProfile or related data changes
func ServerProfileInvalidate(ctx context.Context, nk runtime.NakamaModule, userID string) error {
	return ServerProfileDelete(ctx, nk, userID)
}
