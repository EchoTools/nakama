package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/heroiclabs/nakama-common/runtime"
	"github.com/heroiclabs/nakama/v3/server/evr"
	"go.uber.org/zap"
	"golang.org/x/sync/singleflight"
)

const (
	StorageCollectionServerProfile = "ServerProfile"
	StorageKeyServerProfile        = "profile"
	ServerProfileIndexName         = "Index_ServerProfile"
	ServerProfileMaxEntries        = 1000 // Keep at least 1000 profiles indexed in memory
)

// Singleflight groups to prevent thundering herd on concurrent profile requests.
// When multiple clients request the same profile simultaneously (e.g., 8 players
// in a lobby all requesting each other's profiles), only one goroutine does the
// actual work while others wait for and share the result.
var (
	profileLoadByXPIDGroup singleflight.Group
	profileLoadByUserGroup singleflight.Group
)

// profileLoadResult holds the result of a singleflight profile load operation
type profileLoadResult struct {
	profile json.RawMessage
	userID  string
}

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
		PermissionRead:  runtime.STORAGE_PERMISSION_PUBLIC_READ,
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

// NewServerProfileStorageFromJSON creates a new ServerProfileStorage from pre-marshalled JSON.
// This avoids a redundant marshal when the JSON is already available.
func NewServerProfileStorageFromJSON(userID string, xpID evr.EvrId, profileJSON json.RawMessage) *ServerProfileStorage {
	return &ServerProfileStorage{
		XPlatformID: xpID.String(),
		Profile:     profileJSON,
		userID:      userID,
		version:     "", // Will be set on write
	}
}

// ServerProfileLoad loads a ServerProfile, checking storage first, then generating from EVRProfile.
// Uses singleflight to prevent concurrent generation for the same user.
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

	// Not in storage - use singleflight to prevent concurrent generation
	result, err, _ := profileLoadByUserGroup.Do(userID, func() (interface{}, error) {
		// Double-check storage inside singleflight (another goroutine may have just written it)
		storage := &ServerProfileStorage{}
		if err := StorableRead(ctx, nk, userID, storage, false); err == nil && len(storage.Profile) > 0 {
			profile := &evr.ServerProfile{}
			if err := json.Unmarshal(storage.Profile, profile); err == nil {
				return profile, nil
			}
		}

		// Generate from EVRProfile
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

		// Store the generated profile (ignore version conflicts from concurrent writes)
		if err := ServerProfileStore(ctx, nk, userID, serverProfile); err != nil {
			if !isVersionConflictError(err) {
				logger.Warn("Failed to store generated server profile", zap.Error(err))
			}
		}

		return serverProfile, nil
	})

	if err != nil {
		return nil, err
	}

	return result.(*evr.ServerProfile), nil
}

// ServerProfileStore stores a ServerProfile to storage.
// Version conflicts are expected when concurrent requests generate the same profile
// and should be handled gracefully by the caller.
func ServerProfileStore(ctx context.Context, nk runtime.NakamaModule, userID string, profile *evr.ServerProfile) error {
	storage, err := NewServerProfileStorage(userID, profile)
	if err != nil {
		return err
	}
	return StorableWrite(ctx, nk, userID, storage)
}

// ServerProfileStoreJSON stores a pre-marshalled ServerProfile JSON to storage.
// This is more efficient when the JSON is already available (e.g., from generation).
func ServerProfileStoreJSON(ctx context.Context, nk runtime.NakamaModule, userID string, xpID evr.EvrId, profileJSON json.RawMessage) error {
	storage := NewServerProfileStorageFromJSON(userID, xpID, profileJSON)
	return StorableWrite(ctx, nk, userID, storage)
}

// ServerProfileLoadByXPID searches for a ServerProfile by XPID using the storage index.
// If not found, it generates the profile from EVRProfile and caches it.
// Returns the raw JSON profile data to avoid unnecessary marshal/unmarshal when
// the profile will be sent as json.RawMessage to other users.
//
// Uses singleflight to deduplicate concurrent requests for the same XPID, preventing
// thundering herd when multiple players request the same user's profile simultaneously.
func ServerProfileLoadByXPID(ctx context.Context, logger *zap.Logger, db *sql.DB, nk runtime.NakamaModule, xpID evr.EvrId, groupID string, modes []evr.Symbol, dailyWeeklyMode evr.Symbol) (json.RawMessage, string, error) {
	// Search the storage index for the XPID
	query := fmt.Sprintf("+xplatformid:%s", xpID.String())

	objects, _, err := nk.StorageIndexList(ctx, SystemUserID, ServerProfileIndexName, query, 1, nil, "")
	if err != nil {
		return nil, "", fmt.Errorf("failed to search server profile index: %w", err)
	}

	if len(objects.Objects) > 0 {
		obj := objects.Objects[0]
		storage := &ServerProfileStorage{}
		if err := json.Unmarshal([]byte(obj.Value), storage); err != nil {
			logger.Warn("Failed to unmarshal cached server profile, regenerating", zap.Error(err))
		} else {
			return storage.Profile, obj.UserId, nil
		}
	}

	// Not found in cache - use singleflight to prevent concurrent generation
	key := xpID.String()
	result, err, _ := profileLoadByXPIDGroup.Do(key, func() (interface{}, error) {
		// Double-check the index inside singleflight (another goroutine may have just written it)
		objects, _, err := nk.StorageIndexList(ctx, SystemUserID, ServerProfileIndexName, query, 1, nil, "")
		if err == nil && len(objects.Objects) > 0 {
			obj := objects.Objects[0]
			storage := &ServerProfileStorage{}
			if err := json.Unmarshal([]byte(obj.Value), storage); err == nil {
				return &profileLoadResult{profile: storage.Profile, userID: obj.UserId}, nil
			}
		}

		// Look up user by device ID
		userID, err := GetUserIDByDeviceID(ctx, db, xpID.String())
		if err != nil {
			return nil, fmt.Errorf("failed to get user ID by XPID: %w", err)
		}
		if userID == "" {
			return &profileLoadResult{}, nil // User not found
		}

		// Load EVRProfile and generate ServerProfile
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

		// Marshal the profile to return as raw JSON
		profileJSON, err := json.Marshal(serverProfile)
		if err != nil {
			return nil, fmt.Errorf("failed to marshal server profile: %w", err)
		}

		// Store the generated profile using pre-marshalled JSON (avoid double marshal)
		// Ignore version conflicts - they just mean another request already stored it
		if err := ServerProfileStoreJSON(ctx, nk, userID, xpID, profileJSON); err != nil {
			if !isVersionConflictError(err) {
				logger.Warn("Failed to store generated server profile", zap.Error(err))
			}
		}

		return &profileLoadResult{profile: profileJSON, userID: userID}, nil
	})

	if err != nil {
		return nil, "", err
	}

	r := result.(*profileLoadResult)
	return r.profile, r.userID, nil
}

// isVersionConflictError checks if an error is a storage version conflict.
// These are expected during concurrent profile writes and should be ignored
// since the profiles being written are identical.
func isVersionConflictError(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(err.Error(), "version check failed")
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
