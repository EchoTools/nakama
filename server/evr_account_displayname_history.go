package server

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/heroiclabs/nakama-common/api"
	"github.com/heroiclabs/nakama-common/runtime"
)

const (
	DisplayNameCollection        = "DisplayNames"
	DisplayNameHistoryKey        = "history"
	DisplayNameHistoryCacheIndex = "Index_DisplayNameHistory"
)

type DisplayNameHistoryEntry struct {
	DisplayName string    `json:"display_name"`
	UpdateTime  time.Time `json:"update_time"`
}

type DisplayNameHistory struct {
	Histories map[string][]DisplayNameHistoryEntry `json:"history"`  // map[groupID]DisplayNameHistoryEntry
	Cache     []string                             `json:"cache"`    // All past display names
	Reserved  []string                             `json:"reserved"` // staticly reserved names
	Active    []string                             `json:"active"`   // names that the user has reserved
}

func NewDisplayNameHistory() *DisplayNameHistory {
	return &DisplayNameHistory{
		Histories: make(map[string][]DisplayNameHistoryEntry),
		Active:    make([]string, 0),
		Reserved:  make([]string, 0),
	}
}

// Returns true if the display name history was updated
func (h *DisplayNameHistory) Set(groupID, displayName string) bool {
	if h.Histories == nil {
		h.Histories = make(map[string][]DisplayNameHistoryEntry)
	}

	if _, ok := h.Histories[groupID]; !ok {
		h.Histories[groupID] = make([]DisplayNameHistoryEntry, 0)
	}

	if len(h.Histories[groupID]) > 0 {
		if h.Histories[groupID][len(h.Histories[groupID])-1].DisplayName == displayName {
			return false
		}
	}

	h.Histories[groupID] = append(h.Histories[groupID], DisplayNameHistoryEntry{
		DisplayName: displayName,
		UpdateTime:  time.Now(),
	})

	h.updateCache()
	h.updateActive()
	return true
}

func (h *DisplayNameHistory) updateCache() {
	// Limit the history to the past two months
	h.Cache = make([]string, 0, len(h.Histories))

	for _, items := range h.Histories {
		for i := 0; i < len(items); i++ {
			if items[i].UpdateTime.AddDate(0, 2, 0).Before(time.Now()) {
				items = append(items[:i], items[i+1:]...)
				i--
				continue
			}
			if s := strings.ToLower(items[i].DisplayName); !slices.Contains(h.Cache, s) {
				h.Cache = append(h.Cache, s)
			}
		}
	}

	for _, name := range h.Reserved {
		if s := strings.ToLower(name); !slices.Contains(h.Cache, s) {
			h.Cache = append(h.Cache, s)
		}
	}
}

func (h *DisplayNameHistory) AddReserved(displayName string) {
	if !slices.Contains(h.Reserved, displayName) {
		h.Reserved = append(h.Reserved, displayName)
		h.updateActive()
	}
	if s := strings.ToLower(displayName); !slices.Contains(h.Cache, s) {
		h.Cache = append(h.Cache, s)
	}
}

func (h *DisplayNameHistory) RemoveStaticReserved(displayName string) {
	if i := slices.Index(h.Reserved, displayName); i != -1 {
		h.Reserved = append(h.Reserved[:i], h.Reserved[i+1:]...)
		h.updateActive()
	}
}

func (h *DisplayNameHistory) updateActive() {
	h.Active = make([]string, 0)
	for _, items := range h.Histories {
		if len(items) == 0 {
			continue
		}
		current := strings.ToLower(items[len(items)-1].DisplayName)
		h.Active = append(h.Active, current)
	}
	for _, name := range h.Reserved {
		h.Active = append(h.Active, strings.ToLower(name))
	}
	slices.Sort(h.Active)
	h.Active = slices.Compact(h.Active)
}

func DisplayNameHistoryLoad(ctx context.Context, nk runtime.NakamaModule, userID string) (*DisplayNameHistory, error) {
	objects, err := nk.StorageRead(ctx, []*runtime.StorageRead{
		{
			Collection: DisplayNameCollection,
			Key:        DisplayNameHistoryKey,
			UserID:     userID,
		},
	})
	if err != nil {
		return nil, fmt.Errorf("error reading display name cache: %w", err)
	}

	if len(objects) == 0 {
		return NewDisplayNameHistory(), nil
	}

	var history DisplayNameHistory
	if err := json.Unmarshal([]byte(objects[0].Value), &history); err != nil {
		return nil, fmt.Errorf("error unmarshalling display name cache: %w", err)
	}

	return &history, nil
}

func DisplayNameHistoryStore(ctx context.Context, nk runtime.NakamaModule, userID string, history *DisplayNameHistory) error {
	bytes, err := json.Marshal(history)
	if err != nil {
		return fmt.Errorf("error marshalling display name history: %w", err)
	}

	if _, err := nk.StorageWrite(ctx, []*runtime.StorageWrite{
		{
			Collection: DisplayNameCollection,
			Key:        DisplayNameHistoryKey,
			Value:      string(bytes),
			UserID:     userID,
		},
	}); err != nil {
		return fmt.Errorf("error writing display name history: %w", err)
	}

	return nil
}

func DisplayNameHistorySet(ctx context.Context, nk runtime.NakamaModule, userID string, guildID string, displayName string) error {
	history, err := DisplayNameHistoryLoad(ctx, nk, userID)
	if err != nil {
		return fmt.Errorf("error getting display name history: %w", err)
	}

	updated := history.Set(guildID, displayName)

	if updated {
		if err := DisplayNameHistoryStore(ctx, nk, userID, history); err != nil {
			return fmt.Errorf("error storing display name history: %w", err)
		}
	}
	return nil
}

func DisplayNameCacheRegexSearch(ctx context.Context, nk runtime.NakamaModule, pattern string, limit int) (map[string]*DisplayNameHistory, error) {
	query := fmt.Sprintf("+value.cache:/%s/", pattern)
	// Perform the storage list operation

	cursor := ""

	hardLimit := 1000
	results := make([]*api.StorageObject, 100)
	var err error
	var result *api.StorageObjects
	for {
		result, cursor, err = nk.StorageIndexList(ctx, SystemUserID, DisplayNameHistoryCacheIndex, query, 100, []string{"value.active"}, cursor)
		if err != nil {
			return nil, fmt.Errorf("error listing display name history: %w", err)
		}
		if len(result.Objects) == 0 || len(results) >= hardLimit {
			break
		}
		results = append(results, result.Objects...)

		if len(results) >= limit {
			results = results[:limit]
			break
		}

		if cursor == "" {
			break
		}
	}

	histories := make(map[string]*DisplayNameHistory, len(result.Objects))
	for _, obj := range result.Objects {
		var history DisplayNameHistory
		if err := json.Unmarshal([]byte(obj.Value), &history); err != nil {
			return nil, fmt.Errorf("error unmarshalling display name history: %w", err)
		}
		histories[obj.UserId] = &history
	}

	return histories, nil
}

func DisplayNameHistoryActiveList(ctx context.Context, nk runtime.NakamaModule, displayName string) ([]string, error) {
	// Perform the storage list operation
	query := fmt.Sprintf("+value.active:%s", Query.Escape(displayName))

	cursor := ""
	var results []*api.StorageObject
	for {

		result, cursor, err := nk.StorageIndexList(ctx, SystemUserID, DisplayNameHistoryCacheIndex, query, 100, nil, cursor)
		if err != nil {
			return nil, fmt.Errorf("error listing display name history: %w", err)
		}

		if len(result.Objects) == 0 {
			break
		}

		results = append(results, result.Objects...)

		if cursor == "" {
			break
		}
	}

	userIDs := make([]string, 0, len(results))
	for _, entry := range results {
		userIDs = append(userIDs, entry.UserId)
	}

	return userIDs, nil
}
