package server

import "github.com/heroiclabs/nakama-common/runtime"

const (
	CustomizationStorageCollection = "Customization"
	SavedOutfitsStorageKey         = "outfits"
)

var _ = Storable(&Wardrobe{})

type Wardrobe map[string]*AccountCosmetics

func (w Wardrobe) StorageMeta() StorageMeta {
	return StorageMeta{
		Collection:      CustomizationStorageCollection,
		Key:             SavedOutfitsStorageKey,
		PermissionRead:  runtime.STORAGE_PERMISSION_NO_READ,
		PermissionWrite: runtime.STORAGE_PERMISSION_OWNER_WRITE,
		Version:         "",
	}
}
