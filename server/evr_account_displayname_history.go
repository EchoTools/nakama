package server

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/heroiclabs/nakama-common/api"
	"github.com/heroiclabs/nakama-common/runtime"
)

const (
	DisplayNameCollection        = "DisplayName"
	DisplayNameHistoryKey        = "history"
	DisplayNameHistoryCacheIndex = "Index_DisplayNameHistory"
)

var (
	MaximumDisplayNameAge = time.Hour * 24 * 30 * 2 // 2 months
)

var _ = IndexedStorable(&DisplayNameHistory{})

type DisplayNameHistory struct {
	Username     string                          `json:"username"` // the user's username
	Reserved     []string                        `json:"reserves"` // staticly reserved names
	InGameNames  []string                        `json:"igns"`     // (lowercased) names that the user has in-game
	Histories    map[string]map[string]time.Time `json:"history"`  // map[groupID]map[displayName]lastUsedTime
	ActiveCache  []string                        `json:"active"`   // (lowercased) names that the user has active/reserved
	HistoryCache []string                        `json:"cache"`    // (lowercased) used for searching
}

func (DisplayNameHistory) StorageMeta() StorageMeta {
	return StorageMeta{
		Collection: DisplayNameCollection,
		Key:        DisplayNameHistoryKey,
	}
}

func (DisplayNameHistory) StorageIndexes() []StorageIndexMeta {
	return []StorageIndexMeta{{
		Name:           DisplayNameHistoryCacheIndex,
		Collection:     DisplayNameCollection,
		Key:            DisplayNameHistoryKey,
		Fields:         []string{"active", "cache", "reserves", "username", "igns"},
		SortableFields: nil,
		MaxEntries:     1000000,
		IndexOnly:      false,
	}}
}

func NewDisplayNameHistory() *DisplayNameHistory {
	return &DisplayNameHistory{
		Histories:    make(map[string]map[string]time.Time),
		ActiveCache:  make([]string, 0),
		HistoryCache: make([]string, 0),
		Reserved:     make([]string, 0),
	}
}

func (h *DisplayNameHistory) MarshalJSON() ([]byte, error) {
	// Update the caches
	h.compile()
	type Alias DisplayNameHistory
	aux := &struct{ *Alias }{Alias: (*Alias)(h)}
	return json.Marshal(aux)
}

func (h *DisplayNameHistory) compile() {

	// build the active list
	active := make(map[string]struct{})

	// Add the in-game names to the active list
	for _, name := range h.InGameNames {
		if name == "" {
			continue
		}
		active[strings.ToLower(name)] = struct{}{}
	}

	// Add the reserved names to the cache
	for _, name := range h.Reserved {
		if name == "" {
			continue
		}
		// And to active list
		active[strings.ToLower(name)] = struct{}{}
	}

	if h.Username != "" {
		// Add the username to the active list
		active[strings.ToLower(h.Username)] = struct{}{}
	}

	cache := make(map[string]struct{})

	// Add the active names to the cache
	for name := range active {
		if name != "" {
			cache[name] = struct{}{}
		}
	}

	for groupID, names := range h.Histories {

		for name, _ := range names {

			// Remove any invalid display names
			if name == "" {
				delete(h.Histories[groupID], name)
				continue
			}

			// Add it to the cache
			cache[strings.ToLower(name)] = struct{}{}
		}
	}

	// Build the caches
	h.HistoryCache = make([]string, 0, len(cache))
	for name := range cache {
		h.HistoryCache = append(h.HistoryCache, name)
	}
	h.ActiveCache = make([]string, 0, len(active))
	for name := range active {
		h.ActiveCache = append(h.ActiveCache, name)
	}
	// Sort the caches
	sort.Strings(h.HistoryCache)
	sort.Strings(h.ActiveCache)
}

// LatestByGroupID returns the latest display names for each groupID.
func (h *DisplayNameHistory) LatestByGroupID() map[string]string {
	if h.Histories == nil {
		h.Histories = make(map[string]map[string]time.Time)
	}

	latestMap := make(map[string]time.Time)

	latest := make(map[string]string)
	for groupID, names := range h.Histories {
		for name, lastUsed := range names {
			if lastUsed.Before(latestMap[groupID]) || time.Since(lastUsed) > MaximumDisplayNameAge {
				// Ignore old display names
				// Ignore display names that are not the latest
				continue
			}
			latestMap[groupID] = lastUsed
			latest[groupID] = name

		}
	}

	return latest
}

func (h *DisplayNameHistory) Set(groupID, displayName string, lastUsed time.Time, username string) {
	if h.Histories == nil {
		h.Histories = make(map[string]map[string]time.Time)
	}
	if _, ok := h.Histories[groupID]; !ok {
		h.Histories[groupID] = make(map[string]time.Time)
	}
	h.Histories[groupID][displayName] = lastUsed

	if username != "" {
		h.Username = username
	}
}

// Set the display name for the given groupID
func (h *DisplayNameHistory) Update(groupID, displayName string, username string, isInGame bool) {
	if h.Histories == nil {
		h.Histories = make(map[string]map[string]time.Time)
	}

	if _, ok := h.Histories[groupID]; !ok {
		h.Histories[groupID] = make(map[string]time.Time)
	}
	h.Histories[groupID][displayName] = time.Now()
	if username != "" {
		h.Username = username
	}
	if isInGame {
		h.InGameNames = append(h.InGameNames, displayName)
	}
}

// Returns the latest display name for the given groupID
func (h *DisplayNameHistory) LatestGroup(groupID string) (string, time.Time) {
	if h.Histories == nil {
		h.Histories = make(map[string]map[string]time.Time)
	}

	// Return the latest display name by date for the group
	var latest string
	var latestTime time.Time
	for name, updateTime := range h.Histories[groupID] {
		if updateTime.After(latestTime) {
			latest = name
			latestTime = updateTime
		}
	}
	return latest, latestTime
}

func (h *DisplayNameHistory) AddReserved(displayName string) {
	if h.Reserved == nil {
		h.Reserved = make([]string, 1)
	}

	h.Reserved = append(h.Reserved, displayName)
}

func (h *DisplayNameHistory) RemoveReserved(displayName string) {
	if h.Reserved == nil {
		h.Reserved = make([]string, 0)
	}

	for i, name := range h.Reserved {
		if name == displayName {
			h.Reserved = append(h.Reserved[:i], h.Reserved[i+1:]...)
			break
		}
	}
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

func DisplayNameHistoryUpdate(ctx context.Context, nk runtime.NakamaModule, userID string, groupID string, displayName string, username string) error {
	history, err := DisplayNameHistoryLoad(ctx, nk, userID)
	if err != nil {
		return fmt.Errorf("error getting display name history: %w", err)
	}

	history.Update(groupID, displayName, username, false)

	if err := DisplayNameHistoryStore(ctx, nk, userID, history); err != nil {
		return fmt.Errorf("error storing display name history: %w", err)
	}

	return nil
}

func DisplayNameCacheRegexSearch(ctx context.Context, nk runtime.NakamaModule, pattern string, limit int) (map[string]map[string]map[string]time.Time, error) {

	query := fmt.Sprintf(`+value.cache:/%s/`, pattern)

	// Perform the storage list operation

	var err error
	histories := make(map[string]*DisplayNameHistory, 10)
	var result *api.StorageObjects

	cursor := ""
	for {
		result, cursor, err = nk.StorageIndexList(ctx, SystemUserID, DisplayNameHistoryCacheIndex, query, 100, nil, cursor)
		if err != nil {
			return nil, fmt.Errorf("error listing display name history: %w", err)
		}

		for _, obj := range result.Objects {
			var history DisplayNameHistory
			if err := json.Unmarshal([]byte(obj.Value), &history); err != nil {
				return nil, fmt.Errorf("error unmarshalling display name history: %w", err)
			}
			histories[obj.UserId] = &history
		}

		if cursor == "" {
			break
		}
	}

	matches := make(map[string]map[string]map[string]time.Time, len(histories)) // map[userID]map[groupID]map[displayName]lastUsedTime

	const globalGroupID = ""

	for userID, history := range histories {
		matches[userID] = make(map[string]map[string]time.Time)
		matches[userID][globalGroupID] = make(map[string]time.Time)

		for groupID, e := range history.Histories {
			matches[userID][groupID] = make(map[string]time.Time)

			for name, lastUsed := range e {
				matches[userID][groupID][name] = lastUsed
			}
		}

		// Add exact matches for usernames
		if strings.ToLower(history.Username) == pattern {
			matches[userID][globalGroupID][history.Username] = time.Time{}
		}

		// Add exact matches for reserved names
		for _, name := range history.Reserved {
			if strings.ToLower(name) == pattern {
				matches[userID][globalGroupID][name] = time.Time{}
			}
		}
	}

	// Remove any empty matches
	for userID, groupMatches := range matches {

		for groupID, names := range groupMatches {
			if len(names) == 0 {
				delete(groupMatches, groupID)
			}
		}

		if len(groupMatches) == 0 {
			delete(matches, userID)
		}
	}

	if len(matches) > limit {
		// Keep the most recent matches by userID

		type match struct {
			userID   string
			lastUsed time.Time
			names    map[string]map[string]time.Time
		}
		sorted := make([]match, 0, len(matches))
		for userID, namesByGroupID := range matches {
			match := match{userID: userID, names: namesByGroupID}

			// reduce to the most recent, plus reserved
			for _, names := range namesByGroupID {
				for _, lastUsed := range names {

					// Find the most recent last used time; if it's zero, it's a reserved name
					if lastUsed.IsZero() || lastUsed.After(match.lastUsed) {
						match.lastUsed = lastUsed
					}
				}
			}
			sorted = append(sorted, match)
		}

		// Sort the matches by last used time
		sort.Slice(sorted, func(i, j int) bool {
			if sorted[i].lastUsed.IsZero() {
				return true
			} else if sorted[i].lastUsed.IsZero() {
				return false
			}

			return sorted[i].lastUsed.After(sorted[j].lastUsed)
		})

		matches = make(map[string]map[string]map[string]time.Time, limit)

		// Include all reserved names
		for i := 0; i < len(matches); i++ {
			if sorted[i].lastUsed.IsZero() {
				matches[sorted[i].userID] = sorted[i].names
			}
			sorted = append(sorted[:i], sorted[i+1:]...)
			i--
		}

		limit := min(limit, len(sorted))
		// Add the most recent matches, up to the limit
		for _, m := range sorted[:limit] {
			matches[m.userID] = m.names
		}
	}

	return matches, nil
}

func DisplayNameOwnerSearch(ctx context.Context, nk runtime.NakamaModule, displayName string) ([]string, error) {
	displayName = sanitizeDisplayName(displayName)
	displayName = strings.ToLower(displayName)
	displayName = Query.Escape(displayName)

	query := fmt.Sprintf("+value.active:%s", displayName)

	var (
		err     error
		result  *api.StorageObjects
		cursor  string
		userIDs = make([]string, 0, 1)
	)
	for {

		result, cursor, err = nk.StorageIndexList(ctx, SystemUserID, DisplayNameHistoryCacheIndex, query, 100, nil, cursor)
		if err != nil {
			return nil, fmt.Errorf("error listing display name history: %w", err)
		}

		for _, obj := range result.Objects {
			userIDs = append(userIDs, obj.UserId)
		}

		if cursor == "" {
			break
		}
	}
	return userIDs, nil
}
