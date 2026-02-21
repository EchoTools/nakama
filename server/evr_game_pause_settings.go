package server

import (
	"context"

	"github.com/heroiclabs/nakama-common/runtime"
	"github.com/heroiclabs/nakama/v3/server/evr"
)

const (
	StorageCollectionGamePauseSettings = "GameSettings"
	StorageKeyGamePauseSettings        = "pause_settings"
)

type GamePauseSettingsStorage struct {
	evr.GamePauseSettings

	version string
}

func (s *GamePauseSettingsStorage) StorageMeta() StorableMetadata {
	return StorableMetadata{
		Collection:      StorageCollectionGamePauseSettings,
		Key:             StorageKeyGamePauseSettings,
		PermissionRead:  runtime.STORAGE_PERMISSION_NO_READ,
		PermissionWrite: runtime.STORAGE_PERMISSION_NO_WRITE,
		Version:         s.version,
	}
}

func (s *GamePauseSettingsStorage) SetStorageMeta(meta StorableMetadata) {
	s.version = meta.Version
}

func GamePauseSettingsLoad(ctx context.Context, nk runtime.NakamaModule, userID string) (*GamePauseSettingsStorage, error) {
	settings := &GamePauseSettingsStorage{}
	if err := StorableRead(ctx, nk, userID, settings, false); err != nil {
		return nil, err
	}
	return settings, nil
}

func GamePauseSettingsStore(ctx context.Context, nk runtime.NakamaModule, userID string, settings *GamePauseSettingsStorage) error {
	return StorableWrite(ctx, nk, userID, settings)
}
