package server

import "encoding/json"

type ProfileOptions struct {
	DisableAFKTimeout     bool   `json:"disable_afk_timeout"`    // Disable AFK detection
	AllowBrokenCosmetics  bool   `json:"allow_broken_cosmetics"` // Allow broken cosmetics
	EnableAllCosmetics    bool   `json:"enable_all_cosmetics"`   // Enable all cosmetics
	GoldDisplayNameActive bool   `json:"gold_display_name"`      // The gold name display name
	version               string // Version of the options
}

func (h *ProfileOptions) StorageMeta() StorableMetadata {
	return StorableMetadata{
		Collection:      LatencyHistoryStorageCollection,
		Collection:      ProfileOptionsStorageCollection,
		Key:             ProfileOptionsStorageKey,
		PermissionRead:  0,
		PermissionWrite: 0,
		Version:         h.version,
	}
}

func (h *ProfileOptions) SetStorageMeta(meta StorableMetadata) {
	h.version = meta.Version
}

func (h *LatencyHistory) String() string {
	h.RLock()
	defer h.RUnlock()
	data, err := json.Marshal(h)
	if err != nil {
		return ""
	}
	return string(data)
}
