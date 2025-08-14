package server

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"slices"

	"maps"

	"github.com/heroiclabs/nakama-common/api"
	"github.com/heroiclabs/nakama-common/runtime"
	"go.uber.org/zap"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const (
	DisplayNameCollection        = "DisplayName"
	DisplayNameHistoryKey        = "history"
	DisplayNameHistoryCacheIndex = "Index_DisplayNameHistory"
)

var (
	MaximumDisplayNameHistoryAge = time.Hour * 24 * 30 * 3 // 1 months
	MaximumDisplayNameActiveAge  = time.Hour * 24 * 30 * 1 // 1 months
)

type DisplayNameHistory struct {
	Username     string                          `json:"username"`      // the user's username
	Reserved     []string                        `json:"reserves"`      // staticly reserved names
	InGameNames  []string                        `json:"in_game_names"` // (lowercased) names that the user has in-game
	Histories    map[string]map[string]time.Time `json:"history"`       // map[groupID]map[displayName]lastUsedTime
	LastUsed     map[string]time.Time            `json:"ign_last_used"` // map[displayName]lastUsedTime
	ActiveCache  []string                        `json:"active"`        // (lowercased) names that the user has active/reserved
	HistoryCache []string                        `json:"cache"`         // (lowercased) used for searching
}

func (h *DisplayNameHistory) StorageMeta() StorableMetadata {
	return StorableMetadata{
		Collection:      DisplayNameCollection,
		Key:             DisplayNameHistoryKey,
		PermissionRead:  0,
		PermissionWrite: 0,
		Version:         "", // No version tracking for DisplayNameHistory
	}
}

func (h *DisplayNameHistory) SetStorageMeta(meta StorableMetadata) {
	// DisplayNameHistory doesn't track version, so nothing to set
}

func (h *DisplayNameHistory) StorageIndexes() []StorableIndexMeta {
	return []StorableIndexMeta{{
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
		InGameNames:  make([]string, 0),
		LastUsed:     make(map[string]time.Time),
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

	// Remove expired in-game names
	for i := 0; i < len(h.InGameNames); i++ {
		lastUsed := h.LastUsed[h.InGameNames[i]]
		if time.Since(lastUsed) > MaximumDisplayNameActiveAge {
			h.InGameNames = slices.Delete(h.InGameNames, i, i+1)
			i--
		}
	}

	// Remove any duplicate in-game names
	sort.Strings(h.InGameNames)
	h.InGameNames = slices.Compact(h.InGameNames)

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
		if h.LastUsed == nil {
			h.LastUsed = make(map[string]time.Time)
		}
		h.LastUsed[displayName] = time.Now()
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

func (h *DisplayNameHistory) ReplaceInGameNames(names []string) {
	if h.InGameNames == nil {
		h.InGameNames = make([]string, 0)
	}

	h.InGameNames = names[:]
	slices.Sort(h.InGameNames)
	h.InGameNames = slices.Compact(h.InGameNames)
}

func DisplayNameHistoryLoad(ctx context.Context, nk runtime.NakamaModule, userID string) (*DisplayNameHistory, error) {
	history := NewDisplayNameHistory()

	if err := StorableRead(ctx, nk, userID, history, false); err != nil {
		if status.Code(err) == codes.NotFound {
			return history, nil
		}
		return nil, fmt.Errorf("error reading display name cache: %w", err)
	}

	return history, nil
}

func DisplayNameHistoryStore(ctx context.Context, nk runtime.NakamaModule, userID string, history *DisplayNameHistory) error {
	if err := StorableWrite(ctx, nk, userID, history); err != nil {
		return fmt.Errorf("error writing display name history: %w", err)
	}

	return nil
}

func DisplayNameHistoryUpdate(ctx context.Context, nk runtime.NakamaModule, userID string, groupID string, displayName string, username string, isInGame bool) error {
	history, err := DisplayNameHistoryLoad(ctx, nk, userID)
	if err != nil {
		return fmt.Errorf("error getting display name history: %w", err)
	}

	history.Update(groupID, displayName, username, isInGame)

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
			history := &DisplayNameHistory{}
			if err := json.Unmarshal([]byte(obj.Value), history); err != nil {
				return nil, fmt.Errorf("error unmarshalling display name history: %w", err)
			}
			histories[obj.UserId] = history
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

			maps.Copy(matches[userID][groupID], e)
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
			sorted = slices.Delete(sorted, i, i+1)
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

func DisplayNameOwnerSearch(ctx context.Context, nk runtime.NakamaModule, displayNames []string) (map[string][]string, error) {
	nameMap := make(map[string]string, len(displayNames))
	sanitized := make([]string, len(displayNames))
	for _, dn := range displayNames {
		s := sanitizeDisplayName(dn)
		s = strings.ToLower(s)
		sanitized = append(sanitized, s)
		nameMap[s] = dn
	}

	// Remove duplicates from the display names.
	slices.Sort(sanitized)
	displayNames = slices.Compact(sanitized)
	for i := 0; i < len(sanitized); i++ {
		// Remove any display names that are empty.
		if sanitized[i] == "" {
			sanitized = slices.Delete(sanitized, i, i+1)
			i--
		}
	}

	query := fmt.Sprintf("+value.active:%s", Query.CreateMatchPattern(sanitized))

	var (
		err      error
		result   *api.StorageObjects
		cursor   string
		ownerMap = make(map[string][]string, len(displayNames))
	)
	for {

		result, cursor, err = nk.StorageIndexList(ctx, SystemUserID, DisplayNameHistoryCacheIndex, query, 100, nil, cursor)
		if err != nil {
			return nil, fmt.Errorf("error listing display name history: %w", err)
		}
		updates := make(map[string]*DisplayNameHistory, len(result.Objects))
		for _, obj := range result.Objects {
			// Ignore old records
			if time.Since(obj.UpdateTime.AsTime()) > MaximumDisplayNameActiveAge {
				continue
			}
			history := &DisplayNameHistory{}
			if err := json.Unmarshal([]byte(obj.Value), history); err != nil {
				return nil, fmt.Errorf("error unmarshalling display name history: %w", err)
			}
			for s, dn := range nameMap {
				if time.Since(history.LastUsed[s]) > MaximumDisplayNameActiveAge {
					// Store the history back to prune it.
					updates[obj.UserId] = history
					continue
				}
				// Add the display name to the owner map
				if _, ok := ownerMap[dn]; !ok {
					ownerMap[dn] = make([]string, 0, 1)
				}
				ownerMap[dn] = append(ownerMap[dn], obj.UserId)
			}
		}
		// Store the history back to prune it.
		for userID, history := range updates {
			if err := DisplayNameHistoryStore(ctx, nk, userID, history); err != nil {
				return nil, fmt.Errorf("error storing display name history: %w", err)
			}
		}

		if cursor == "" {
			break
		}
	}
	return ownerMap, nil
}

// Attempt to assign a display name to a user, as the exclusive owner of the name.
func DisplayNameAssignOwner(ctx context.Context, logger *zap.Logger, nk runtime.NakamaModule, userID, groupID, displayName, username string) error {
	ownerMap, err := DisplayNameOwnerSearch(ctx, nk, []string{displayName})
	if err != nil {
		// If it errors, set the display name to their username
		logger.Error("Error deconflicting display name", zap.String("display_name", displayName), zap.Error(err))
		return err
	}
	if len(ownerMap) > 0 {
		userIDs := ownerMap[displayName]
		// No one owns the display name, so assign it to this user
		if len(userIDs) > 1 {
			logger.Warn("Display name in use by multiple users", zap.String("display_name", displayName), zap.Strings("user_ids", userIDs))
		}

		if len(userIDs) > 0 && !slices.Contains(userIDs, userID) {
			// Display name is in use by another user, so do not assign it
			logger.Debug("Display name already assigned to another user.", zap.String("display_name", displayName), zap.Strings("user_ids", userIDs))
			return fmt.Errorf("display name already assigned to another user: %s", displayName)
		}

		if len(userIDs) == 0 {
			// the display name is not in use, so assign it to this user
			logger.Debug("Assigning display name to user.", zap.String("display_name", displayName), zap.String("user_id", userID))
		}
	}
	// Update the display name history. do not update the account because it will be done when the player logs in to the game.
	if err := DisplayNameHistoryUpdate(ctx, nk, userID, groupID, displayName, username, true); err != nil {
		return fmt.Errorf("error adding display name history entry: %w", err)
	}
	return nil
}
