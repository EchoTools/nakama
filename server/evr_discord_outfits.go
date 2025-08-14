package server

import "github.com/heroiclabs/nakama-common/runtime"

const (
	CustomizationStorageCollection = "Customization"
	SavedOutfitsStorageKey         = "outfits"
)

type Wardrobe map[string]*AccountCosmetics

// CreateStorableAdapter creates a StorableAdapter for Wardrobe
func (w Wardrobe) CreateStorableAdapter() *StorableAdapter {
	return NewStorableAdapter(w, CustomizationStorageCollection, SavedOutfitsStorageKey).
		WithPermissions(runtime.STORAGE_PERMISSION_NO_READ, runtime.STORAGE_PERMISSION_OWNER_WRITE)
}
