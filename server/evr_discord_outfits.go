package server

import "github.com/heroiclabs/nakama-common/runtime"

const (
	CustomizationStorageCollection = "Customization"
	WardrobeStorageKey             = "wardrobe"
)

type Wardrobe struct {
	Outfits map[string]*AccountCosmetics `json:"outfits"`
}

func (w Wardrobe) StorageMeta() StorableMetadata {
	return StorableMetadata{
		Collection:      CustomizationStorageCollection,
		Key:             WardrobeStorageKey,
		PermissionRead:  runtime.STORAGE_PERMISSION_OWNER_READ,
		PermissionWrite: runtime.STORAGE_PERMISSION_NO_WRITE,
		Version:         "", // Always overwrite for maps without version tracking
	}
}

func (w Wardrobe) SetStorageMeta(meta StorableMetadata) {
	// Wardrobe is a map type with no version field, so nothing to set
}

func (w *Wardrobe) ensureOutfitsMap() {
	if w.Outfits == nil {
		w.Outfits = make(map[string]*AccountCosmetics)
	}
}

func (w *Wardrobe) GetOutfit(name string) (*AccountCosmetics, bool) {
	if w == nil {
		return nil, false
	}
	outfit, ok := w.Outfits[name]
	return outfit, ok
}

func (w *Wardrobe) SetOutfit(name string, cosmetics AccountCosmetics) {
	if w == nil {
		return
	}
	w.ensureOutfitsMap()
	copy := cosmetics
	w.Outfits[name] = &copy
}

func (w *Wardrobe) DeleteOutfit(name string) bool {
	if w == nil || w.Outfits == nil {
		return false
	}
	if _, ok := w.Outfits[name]; !ok {
		return false
	}
	delete(w.Outfits, name)
	return true
}
