package server

import "github.com/heroiclabs/nakama-common/runtime"

const (
	CustomizationStorageCollection = "Customization"
	SavedOutfitsStorageKey         = "outfits"
)

type Wardrobe map[string]*AccountCosmetics

func (w Wardrobe) StorageMeta() StorableMetadata {
	return StorableMetadata{
		Collection:      CustomizationStorageCollection,
		Key:             SavedOutfitsStorageKey,
		PermissionRead:  runtime.STORAGE_PERMISSION_NO_READ,
		PermissionWrite: runtime.STORAGE_PERMISSION_OWNER_WRITE,
		Version:         "*", // Always overwrite for maps without version tracking
	}
}

func (w Wardrobe) SetStorageMeta(meta StorableMetadata) {
	// Wardrobe is a map type with no version field, so nothing to set
}
