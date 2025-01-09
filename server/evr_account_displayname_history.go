package server

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/heroiclabs/nakama-common/api"
	"github.com/heroiclabs/nakama-common/runtime"
)

const (
	DisplayNameCollection        = "DisplayNames"
	DisplayNameHistoryKey        = "history"
	DisplayNameHistoryCacheIndex = "Index_DisplayNameHistory"
)

var (
	MaximumDisplayNameAge = time.Hour * 24 * 30 * 2 // 2 months
)

type DisplayNameHistory struct {
	Histories map[string]map[string]time.Time `json:"history"`  // map[groupID]map[displayName]lastUsedTime
	Reserved  map[string]struct{}             `json:"reserved"` // staticly reserved names
	Active    map[string]map[string]time.Time `json:"active"`   // names that the user has reserved
}

func NewDisplayNameHistory() *DisplayNameHistory {
	return &DisplayNameHistory{
		Histories: make(map[string]map[string]time.Time),
		Active:    make(map[string]map[string]time.Time),
		Reserved:  make(map[string]struct{}),
	}
}

func (h *DisplayNameHistory) MarshalJSON() ([]byte, error) {

	// Update Active list
	h.Active = make(map[string]map[string]time.Time)
	for groupID, history := range h.Histories {
		for displayName, lastUsed := range history {
			if time.Since(lastUsed) < MaximumDisplayNameAge {
				if _, ok := h.Active[groupID]; !ok {
					h.Active[groupID] = make(map[string]time.Time)
				}
				if h.Active[groupID][displayName].Before(lastUsed) {
					h.Active[groupID][displayName] = lastUsed
				}
			}
		}
	}
	type Alias DisplayNameHistory
	aux := &struct{ *Alias }{Alias: (*Alias)(h)}
	return json.Marshal(aux)
}

func (h *DisplayNameHistory) UnmarshalJSON(data []byte) error {
	type Alias DisplayNameHistory
	aux := &struct{ *Alias }{Alias: (*Alias)(h)}

	if err := json.Unmarshal(data, &aux); err != nil {
		return err
	}
	if h.Active == nil {
		h.Active = make(map[string]map[string]time.Time)
	}

	if h.Reserved == nil {
		h.Reserved = make(map[string]struct{})
	}

	if h.Histories == nil {
		h.Histories = make(map[string]map[string]time.Time)
	}
	return nil
}

// Set the display name for the given groupID
func (h *DisplayNameHistory) Set(groupID, displayName string) {
	if _, ok := h.Histories[groupID]; !ok {
		h.Histories[groupID] = make(map[string]time.Time)
	}
	h.Histories[groupID][displayName] = time.Now()
}

// Returns the latest display name for the given groupID
func (h *DisplayNameHistory) Latest(groupID string) string {
	// Return the latest display name by date for the group
	var latest string
	var latestTime time.Time
	for name, updateTime := range h.Histories[groupID] {
		if updateTime.After(latestTime) {
			latest = name
			latestTime = updateTime
		}
	}
	return latest
}

func (h *DisplayNameHistory) AddReserved(displayName string) {
	h.Reserved[displayName] = struct{}{}
}

func (h *DisplayNameHistory) RemoveReserved(displayName string) {
	delete(h.Reserved, displayName)
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

func DisplayNameHistorySet(ctx context.Context, nk runtime.NakamaModule, userID string, groupID string, displayName string, isInactive bool) error {
	history, err := DisplayNameHistoryLoad(ctx, nk, userID)
	if err != nil {
		return fmt.Errorf("error getting display name history: %w", err)
	}

	// If it's inactive, the active list will be cleared.
	history.Set(groupID, displayName)

	if err := DisplayNameHistoryStore(ctx, nk, userID, history); err != nil {
		return fmt.Errorf("error storing display name history: %w", err)
	}

	return nil
}

func DisplayNameCacheRegexSearch(ctx context.Context, nk runtime.NakamaModule, escapedPattern string, limit int) (map[string]*DisplayNameHistory, error) {
	query := fmt.Sprintf(`value.history:/%s/ value.active.%s:>"%s"`, escapedPattern, escapedPattern, escapedPattern, time.Now().Add(-MaximumDisplayNameAge).Format(time.RFC3339))
	// Perform the storage list operation

	cursor := ""

	hardLimit := 1000
	results := make([]*api.StorageObject, 100)
	sorting := []string{
		fmt.Sprintf("value.active.%s", escapedPattern),
	}
	var err error
	var result *api.StorageObjects
	for {
		result, cursor, err = nk.StorageIndexList(ctx, SystemUserID, DisplayNameHistoryCacheIndex, query, 100, sorting, cursor)
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
