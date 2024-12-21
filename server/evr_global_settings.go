package server

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/heroiclabs/nakama-common/runtime"
	"go.uber.org/atomic"
)

const (
	GlobalSettingsStorageCollection = "Global"
	GlobalSettingsKey               = "settings"
)

var globalSettings = atomic.NewPointer(&GlobalSettingsData{})

type GlobalSettingsData struct {
	ServiceGuildID string `json:"service_guild_id"` // Central/Support guild ID
}

func GlobalSettings() *GlobalSettingsData {
	return globalSettings.Load()
}

func (g *GlobalSettingsData) String() string {
	data, _ := json.Marshal(g)
	return string(data)
}

func LoadGlobalSettingsData(ctx context.Context, nk runtime.NakamaModule) (*GlobalSettingsData, error) {

	objs, err := nk.StorageRead(ctx, []*runtime.StorageRead{
		{
			Collection: GlobalSettingsStorageCollection,
			Key:        GlobalSettingsKey,
			UserID:     SystemUserID,
		},
	})
	if err != nil {
		return nil, fmt.Errorf("failed to read global settings: %w", err)
	}

	data := GlobalSettingsData{}

	if len(objs) == 0 {
		_, err := nk.StorageWrite(ctx, []*runtime.StorageWrite{{
			Collection:      GlobalSettingsStorageCollection,
			Key:             GlobalSettingsKey,
			UserID:          SystemUserID,
			PermissionRead:  0,
			PermissionWrite: 0,
			Value:           data.String(),
		}})
		if err != nil {
			return nil, fmt.Errorf("failed to write global settings: %w", err)
		}
	} else {
		if err := json.Unmarshal([]byte(objs[0].Value), &data); err != nil {
			return nil, fmt.Errorf("failed to unmarshal global settings: %w", err)
		}
	}
	globalSettings.Store(&data)

	return &data, nil
}
