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

type DisplayNameHistory struct {
	Histories    map[string]map[string]time.Time `json:"history"`   // map[groupID]map[displayName]lastUsedTime
	Reserved     map[string]struct{}             `json:"reserved"`  // staticly reserved names
	Username     string                          `json:"username"`  // the user's username
	ActiveCache  []string                        `json:"active"`    // (lowercased) names that the user has active/reserved
	HistoryCache []string                        `json:"cache"`     // (lowercased) used for searching
	IsActive     bool                            `json:"is_active"` // if the user has an actively linked headset
}

func (DisplayNameHistory) StorageID() StorageID {
	return StorageID{
		Collection: DisplayNameCollection,
		Key:        DisplayNameHistoryKey,
	}
}

func (DisplayNameHistory) StorageIndex() *StorageIndexMeta {
	return &StorageIndexMeta{
		Name:           DisplayNameHistoryCacheIndex,
		Collection:     DisplayNameCollection,
		Key:            DisplayNameHistoryKey,
		Fields:         []string{"active", "cache"},
		SortableFields: nil,
		MaxEntries:     1000000,
		IndexOnly:      false,
	}
}

func NewDisplayNameHistory() *DisplayNameHistory {
	return &DisplayNameHistory{
		Histories:    make(map[string]map[string]time.Time),
		ActiveCache:  make([]string, 0),
		HistoryCache: make([]string, 0),
		Reserved:     make(map[string]struct{}),
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

	cache := make(map[string]struct{})
	active := make(map[string]struct{})

	for groupID, names := range h.Histories {

		var latestName string
		var latestTime time.Time

		for name, updateTime := range names {

			// Remove any invalid display names
			if name == "" {
				delete(h.Histories[groupID], name)
				continue
			}

			// Add it to the cache
			cache[strings.ToLower(name)] = struct{}{}

			// Find the latest display name
			if updateTime.After(latestTime) {
				latestName = name
				latestTime = updateTime
			}
		}

		// Add latest, and recently used, display names to the active list
		if h.IsActive && time.Since(latestTime) < MaximumDisplayNameAge {
			active[strings.ToLower(latestName)] = struct{}{}
		}
	}

	// Add the reserved names to the cache
	for name := range h.Reserved {
		if name == "" {
			continue
		}
		active[strings.ToLower(name)] = struct{}{}
	}

	if h.Username != "" {
		// Add the username to the active list
		active[strings.ToLower(h.Username)] = struct{}{}
	}

	// Add the active names to the cache
	for name := range active {
		cache[name] = struct{}{}
	}

	// Build the caches
	caches := map[*map[string]struct{}]*[]string{
		&cache:  &h.HistoryCache,
		&active: &h.ActiveCache,
	}

	for cache, list := range caches {
		*list = make([]string, 0, len(*cache))
		for name := range *cache {
			*list = append(*list, name)
		}
		sort.Strings(*list)
	}
}

func (h *DisplayNameHistory) GetAll(displayName string) (map[string]time.Time, bool) {
	if h.Histories == nil {
		h.Histories = make(map[string]map[string]time.Time)
	}

	byGroup := make(map[string]time.Time)
	for groupID, names := range h.Histories {
		for name, lastUsed := range names {
			if time.Since(lastUsed) > MaximumDisplayNameAge {
				continue
			}

			if strings.ToLower(name) == displayName {
				byGroup[groupID] = lastUsed
			}
		}
	}

	return byGroup, len(byGroup) > 0
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
func (h *DisplayNameHistory) Update(groupID, displayName string, username string, isActive bool) {
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
	h.IsActive = isActive
}

// Returns the latest display name for the given groupID
func (h *DisplayNameHistory) Latest(groupID string) (string, time.Time) {
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
		h.Reserved = make(map[string]struct{})
	}

	h.Reserved[displayName] = struct{}{}
}

func (h *DisplayNameHistory) RemoveReserved(displayName string) {
	if h.Reserved == nil {
		h.Reserved = make(map[string]struct{})
	}

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

func DisplayNameHistoryUpdate(ctx context.Context, nk runtime.NakamaModule, userID string, groupID string, displayName string, username string, isActive bool) error {
	history, err := DisplayNameHistoryLoad(ctx, nk, userID)
	if err != nil {
		return fmt.Errorf("error getting display name history: %w", err)
	}

	history.Update(groupID, displayName, username, true)

	if err := DisplayNameHistoryStore(ctx, nk, userID, history); err != nil {
		return fmt.Errorf("error storing display name history: %w", err)
	}

	return nil
}

func DisplayNameCacheRegexSearch(ctx context.Context, nk runtime.NakamaModule, displayName string, limit int) (map[string]map[string]map[string]time.Time, error) {

	var useWildcardPrefix, useWildcardSuffix bool

	// Check if the display name has a wildcard prefix or suffix
	if strings.HasPrefix(displayName, "*") {
		displayName = displayName[1:]
		useWildcardPrefix = true
	}
	if strings.HasSuffix(displayName, "*") {
		displayName = displayName[:len(displayName)-1]
		useWildcardSuffix = true
	}

	// Sanitize the display name
	displayName = strings.ToLower(sanitizeDisplayName(displayName))

	// If the display name is empty, return nil
	if len(displayName) == 0 {
		return nil, fmt.Errorf("search string is empty")
	}

	// If the display name is less than 3 characters, don't use wildcards
	if len(displayName) < 3 && (useWildcardPrefix || useWildcardSuffix) {
		return nil, fmt.Errorf("search string is too short for wildcards")
	}

	pattern := Query.Escape(displayName)

	// Check if the display name is a partial match
	if useWildcardPrefix {
		pattern = fmt.Sprintf(".*%s", pattern)
	}
	if useWildcardSuffix {
		pattern = fmt.Sprintf("%s.*", pattern)
	}

	query := fmt.Sprintf(`+value.cache:/%s/`, pattern)

	// Perform the storage list operation

	var err error
	histories := make(map[string]*DisplayNameHistory, 10)
	var result *api.StorageObjects

	cursor := ""
	for {
		result, cursor, err = nk.StorageIndexList(ctx, SystemUserID, DisplayNameHistoryCacheIndex, query, 200, nil, cursor)
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

	matches := make(map[string]map[string]map[string]time.Time, len(histories)) // map[userID]map[groupID]map[displayName]lastUsedTime'

	matchFn := func(s string, p string) bool {
		s = strings.ToLower(s)
		if useWildcardPrefix && useWildcardSuffix {
			return strings.Contains(s, p)
		} else if useWildcardPrefix {
			return strings.HasSuffix(s, p)
		} else if useWildcardSuffix {
			return strings.HasPrefix(s, p)
		}
		return s == p
	}

	for userID, history := range histories {
		matches[userID] = make(map[string]map[string]time.Time)

		for groupID, e := range history.Histories {
			matches[userID][groupID] = make(map[string]time.Time)

			for name, lastUsed := range e {

				if matchFn(name, displayName) {
					matches[userID][groupID][name] = lastUsed
				}
			}
		}

		// Add exact matches for usernames
		if strings.ToLower(history.Username) == displayName {
			if _, ok := matches[userID][""]; !ok {
				matches[userID][""] = make(map[string]time.Time)
			}
			matches[userID][""][history.Username] = time.Time{}
		}

		// Add exact matches for reserved names
		for n := range history.Reserved {
			if strings.ToLower(n) == displayName {
				matches[userID][""][n] = time.Time{}
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

func DisplayNameHistoryActiveList(ctx context.Context, nk runtime.NakamaModule, displayName string) ([]string, error) {

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
