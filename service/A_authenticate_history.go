package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"slices"
	"strconv"
	"strings"
	"time"

	evr "github.com/echotools/nakama/v3/protocol"
	"github.com/heroiclabs/nakama-common/runtime"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const (
	LoginStorageCollection = "Login"
	LoginHistoryStorageKey = "history"
	LoginHistoryCacheIndex = "index_login_cache"
	MaxCacheSize           = 10000 // Maximum number of cache entries
)

var (
	IgnoredLoginValues = map[string]struct{}{
		"":                {},
		"1WMHH000X00000":  {},
		"N/A":             {},
		"UNK-0":           {},
		"OVR-ORG-0":       {},
		"unknown":         {},
		"1PASH5D1P17365":  {}, // Quest Link
		"WMHD315M3010GV":  {}, // Quest link
		"VRLINKHMDQUEST":  {}, // Quest link
		"VRLINKHMDQUEST2": {}, // Quest link
		"VRLINKHMDQUEST3": {}, // Quest link
	}

	ErrPendingAuthorizationNotFound = errors.New("pending authorization not found")
)

func matchIgnoredAltPattern(pattern string) bool {
	// Remove ignored values
	if _, ok := IgnoredLoginValues[pattern]; ok {
		return true
	} else if ip := net.ParseIP(pattern); ip != nil {
		// Check if the IP is a private IP address
		if ip.IsPrivate() {
			return true
		}
	}
	return false
}

type LoginHistoryEntry struct {
	CreatedAt time.Time         `json:"create_time"`
	UpdatedAt time.Time         `json:"update_time"`
	XPID      evr.XPID          `json:"xpi"`
	ClientIP  string            `json:"client_ip"`
	LoginData *evr.LoginProfile `json:"login_data"`
}

func (e *LoginHistoryEntry) Key() string {
	return loginHistoryEntryKey(e.XPID, e.ClientIP)
}

func loginHistoryEntryKey(xpid evr.XPID, clientIP string) string {
	return xpid.Token() + ":" + clientIP
}

func (e *LoginHistoryEntry) PendingCode() string {
	return fmt.Sprintf("%02d", e.CreatedAt.Nanosecond()%100)
}

func (h *LoginHistoryEntry) SystemProfile() string {
	components := []string{
		normalizeHeadsetType(h.LoginData.SystemInfo.HeadsetType),
		h.LoginData.SystemInfo.NetworkType,
		h.LoginData.SystemInfo.VideoCard,
		h.LoginData.SystemInfo.CPUModel,
		strconv.FormatInt(h.LoginData.SystemInfo.NumPhysicalCores, 10),
		strconv.FormatInt(h.LoginData.SystemInfo.NumLogicalCores, 10),
		strconv.FormatInt(h.LoginData.SystemInfo.MemoryTotal, 10),
		strconv.FormatInt(h.LoginData.SystemInfo.DedicatedGPUMemory, 10),
	}
	return strings.Join(components, "::")
}

func (h *LoginHistoryEntry) Patterns() []string {
	return []string{h.ClientIP, h.LoginData.HMDSerialNumber, h.XPID.Token()}
}

func (h *LoginHistoryEntry) Items() []string {
	return []string{h.ClientIP, h.LoginData.HMDSerialNumber, h.XPID.Token(), h.SystemProfile()}
}

type LoginHistory struct {
	Active                   map[string]*LoginHistoryEntry      `json:"active"`                     // map[deviceID]DeviceHistoryEntry
	History                  map[string]*LoginHistoryEntry      `json:"history"`                    // map[deviceID]DeviceHistoryEntry
	Cache                    []string                           `json:"cache"`                      // list of IP addresses, EvrID's, HMD Serial Numbers, and System Data
	XPIs                     map[string]time.Time               `json:"xpis"`                       // list of XPIs
	ClientIPs                map[string]time.Time               `json:"client_ips"`                 // map[clientIP]time.Time
	AuthorizedIPs            map[string]time.Time               `json:"authorized_client_ips"`      // map[clientIP]time.Time
	DeniedClientAddresses    []string                           `json:"denied_client_addrs"`        // list of denied IPs
	PendingAuthorizations    map[string]*LoginHistoryEntry      `json:"pending_authorizations"`     // map[XPID:ClientIP]LoginHistoryEntry
	SecondDegreeAlternates   []string                           `json:"second_degree"`              // []userID
	AlternateMatches         map[string][]*AlternateSearchMatch `json:"alternate_accounts"`         // map of alternate user IDs and what they have in common
	GroupNotifications       map[string]map[string]time.Time    `json:"notified_groups"`            // list of groups that have been notified of this alternate login
	IgnoreDisabledAlternates bool                               `json:"ignore_disabled_alternates"` // Ignore disabled alternates
	userID                   string                             // user ID
	version                  string                             // storage record version
}

func (h *LoginHistory) StorageMeta() StorableMetadata {
	return StorableMetadata{
		Collection:      LoginStorageCollection,
		Key:             LoginHistoryStorageKey,
		PermissionRead:  runtime.STORAGE_PERMISSION_NO_READ,
		PermissionWrite: runtime.STORAGE_PERMISSION_NO_WRITE,
		Version:         h.version,
	}
}

func (h *LoginHistory) SetStorageMeta(meta StorableMetadata) {
	h.userID = meta.UserID
	h.version = meta.Version
}

func (h *LoginHistory) StorageIndexes() []StorableIndexMeta {
	return []StorableIndexMeta{{
		Name:           LoginHistoryCacheIndex,
		Collection:     LoginStorageCollection,
		Key:            LoginHistoryStorageKey,
		Fields:         []string{"cache", "denied_client_addrs"},
		SortableFields: nil,
		MaxEntries:     10000000,
		IndexOnly:      true,
	}}
}

func NewLoginHistory(userID string) *LoginHistory {
	return &LoginHistory{
		userID:  userID,
		version: "*", // don't overwrite existing data
	}
}

func (h *LoginHistory) AlternateIDs() (firstDegree, secondDegree []string) {
	if len(h.AlternateMatches) == 0 && len(h.SecondDegreeAlternates) == 0 {
		return nil, nil
	}

	firstIDs := make([]string, 0, len(h.AlternateMatches))
	secondIDs := make([]string, 0, len(h.SecondDegreeAlternates))

	for userID := range h.AlternateMatches {
		firstIDs = append(firstIDs, userID)
	}

	for _, userID := range h.SecondDegreeAlternates {
		if _, found := h.AlternateMatches[userID]; !found {
			secondIDs = append(secondIDs, userID)
		}
	}

	slices.Sort(firstIDs)
	slices.Sort(secondIDs)

	return slices.Compact(firstIDs), slices.Compact(secondIDs)
}

func (h *LoginHistory) AlternateMaps() (firstDegree map[string]map[string]bool, secondDegree map[string]bool) {
	if len(h.AlternateMatches) == 0 && len(h.SecondDegreeAlternates) == 0 {
		return nil, nil
	}

	var (
		firstIDs  = make(map[string]map[string]bool, len(h.AlternateMatches))
		secondIDs = make(map[string]bool, len(h.SecondDegreeAlternates))
	)

	for userID, matches := range h.AlternateMatches {
		for _, m := range matches {
			for _, item := range m.Items {
				if _, found := firstIDs[userID]; !found {
					firstIDs[userID] = make(map[string]bool, len(matches))
				}
				firstIDs[userID][item] = true
			}
		}
	}

	for _, userID := range h.SecondDegreeAlternates {
		if _, found := h.AlternateMatches[userID]; !found {
			secondIDs[userID] = true
		}
	}

	return firstIDs, secondIDs
}

func (h *LoginHistory) LastSeen() time.Time {
	if len(h.History) == 0 {
		return time.Time{}
	}

	lastSeen := time.Time{}
	for _, e := range h.History {
		if e.UpdatedAt.After(lastSeen) {
			lastSeen = e.UpdatedAt
		}
	}
	return lastSeen
}

func (h *LoginHistory) Update(xpid evr.XPID, ip string, loginData *evr.LoginProfile, isAuthenticated bool) (isNew, allowed bool) {
	allowed = h.IsAuthorizedIP(ip)

	if isAuthenticated {
		isNew = h.AuthorizeIP(ip)
		allowed = true
	}

	// Check denied addresses
	for _, addr := range h.DeniedClientAddresses {
		if addr == ip {
			allowed = false
			break
		}
	}

	entry := &LoginHistoryEntry{
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
		XPID:      xpid,
		ClientIP:  ip,
		LoginData: loginData,
	}

	h.update(entry, isAuthenticated)

	h.cleanupPendingAuthorizations()

	return isNew, allowed
}

func (h *LoginHistory) update(entry *LoginHistoryEntry, active bool) {
	if h.History == nil {
		h.History = make(map[string]*LoginHistoryEntry)
	}
	if e, found := h.History[entry.Key()]; found {
		e.UpdatedAt = time.Now()
		e.LoginData = entry.LoginData
	} else {
		h.History[entry.Key()] = entry
	}

	// If the entry is active, add it to the active history
	if active {
		if h.Active == nil {
			h.Active = make(map[string]*LoginHistoryEntry)
		}
		if e, found := h.Active[entry.Key()]; found {
			e.UpdatedAt = time.Now()
			e.LoginData = entry.LoginData
		} else {
			h.Active[entry.Key()] = entry
		}
	}
}

func (h *LoginHistory) AuthorizeIPWithCode(ip, code string) error {
	for _, e := range h.PendingAuthorizations {
		if e.ClientIP == ip {

			if e.PendingCode() != code {
				return fmt.Errorf("invalid code %s for IP %s", code, ip)
			}

			_ = h.AuthorizeIP(ip)
			return nil
		}
	}

	return ErrPendingAuthorizationNotFound
}

func (h *LoginHistory) AuthorizeIP(ip string) bool {
	if h.AuthorizedIPs == nil {
		h.AuthorizedIPs = make(map[string]time.Time)
	}

	entry := h.RemovePendingAuthorizationIP(ip)
	if entry != nil {
		h.update(entry, true)
	}
	_, isExisting := h.AuthorizedIPs[ip]
	h.AuthorizedIPs[ip] = time.Now().UTC()

	return !isExisting
}

func (h *LoginHistory) IsAuthorizedIP(ip string) (isAuthorized bool) {
	if h.AuthorizedIPs != nil {
		if _, found := h.AuthorizedIPs[ip]; found {
			return true
		}
	}
	return false
}

func (h *LoginHistory) AddPendingAuthorizationIP(xpid evr.XPID, clientIP string, loginData *evr.LoginProfile) *LoginHistoryEntry {
	if h.PendingAuthorizations == nil {
		h.PendingAuthorizations = make(map[string]*LoginHistoryEntry)
	}

	// Clean up old pending authorizations periodically
	h.cleanupPendingAuthorizations()

	e := &LoginHistoryEntry{
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
		XPID:      xpid,
		ClientIP:  clientIP,
		LoginData: loginData,
	}

	h.PendingAuthorizations[clientIP] = e
	return e
}

// cleanupPendingAuthorizations removes expired pending authorizations
func (h *LoginHistory) cleanupPendingAuthorizations() {
	if h.PendingAuthorizations == nil {
		return
	}

	cutoff := time.Now().Add(-10 * time.Minute)
	for ip, e := range h.PendingAuthorizations {
		if net.ParseIP(ip) == nil || e.CreatedAt.Before(cutoff) {
			delete(h.PendingAuthorizations, ip)
		}
	}
}

func (h *LoginHistory) GetPendingAuthorizationIP(ip string) *LoginHistoryEntry {
	if h.PendingAuthorizations == nil {
		return nil
	}
	return h.PendingAuthorizations[ip]
}

func (h *LoginHistory) RemovePendingAuthorizationIP(ip string) *LoginHistoryEntry {
	if h.PendingAuthorizations == nil {
		return nil
	}
	if e, found := h.PendingAuthorizations[ip]; found {
		delete(h.PendingAuthorizations, ip)
		return e
	}
	return nil
}

func (h *LoginHistory) NotifyGroup(groupID string, threshold time.Time) bool {

	userIDs := make([]string, 0, len(h.AlternateMatches)+len(h.SecondDegreeAlternates))
	for k := range h.AlternateMatches {
		userIDs = append(userIDs, k)
	}

	if len(h.SecondDegreeAlternates) > 0 {
		userIDs = append(userIDs, h.SecondDegreeAlternates...)
	}

	if len(userIDs) == 0 {
		return false
	}

	slices.Sort(userIDs)
	userIDs = slices.Compact(userIDs)

	if h.GroupNotifications == nil {
		h.GroupNotifications = make(map[string]map[string]time.Time)
	}
	if _, found := h.GroupNotifications[groupID]; !found {
		h.GroupNotifications[groupID] = make(map[string]time.Time)
	}

	updated := false
	// Check if the group has already been notified for all of the userIDs within the threshold
	for _, userID := range userIDs {
		if t, found := h.GroupNotifications[groupID][userID]; !found || t.Before(threshold) {
			h.GroupNotifications[groupID][userID] = time.Now()
			updated = true
		}
	}

	return updated
}

func (h *LoginHistory) SearchPatterns() (patterns []string) {
	if len(h.History) == 0 {
		return nil
	}

	patterns = make([]string, 0, len(h.History)*3)
	seen := make(map[string]struct{}, len(h.History)*3)

	for _, e := range h.History {
		for _, s := range e.Patterns() {
			if _, found := seen[s]; !found && !matchIgnoredAltPattern(s) {
				patterns = append(patterns, s)
				seen[s] = struct{}{}
			}
		}
	}
	return patterns
}

func (h *LoginHistory) UpdateAlternates(ctx context.Context, logger runtime.Logger, nk runtime.NakamaModule, excludeUserIDs ...string) (hasDisabledAlts bool, err error) {

	// Update alternates
	matches, otherHistories, err := LoginAlternateSearch(ctx, nk, h, true)
	if err != nil {
		return false, fmt.Errorf("error searching for alternate logins: %w", err)
	}
	if len(matches) == 0 {
		return false, nil
	}

	// Rebuild it
	h.AlternateMatches = make(map[string][]*AlternateSearchMatch, len(matches))
	secondMap := make(map[string]bool, len(matches))
	for _, m := range matches {
		h.AlternateMatches[m.OtherUserID] = append(h.AlternateMatches[m.OtherUserID], m)
		if otherHistory, found := otherHistories[m.OtherUserID]; found {
			// add second-level alternates
			for id := range otherHistory.AlternateMatches {
				secondMap[id] = true
			}
		}
	}

	// prune the second degree alts
	delete(secondMap, h.userID)
	h.SecondDegreeAlternates = make([]string, 0, len(matches))
	for userID := range h.AlternateMatches {
		delete(secondMap, userID)
	}

	// Remove excluded user IDs
	for _, userID := range append(excludeUserIDs, h.userID) {
		delete(secondMap, userID)
		delete(h.AlternateMatches, userID)
	}

	// Check if the player has disabled alternates
	if !h.IgnoreDisabledAlternates {
		userIDs := make([]string, 0, len(matches))
		for userID := range h.AlternateMatches {
			userIDs = append(userIDs, userID)
		}
		if accounts, err := nk.AccountsGetId(ctx, userIDs); err != nil {
			return false, fmt.Errorf("error getting accounts for user IDs %v: %w", userIDs, err)
		} else {
			for _, a := range accounts {
				if a.GetDisableTime() != nil && !a.GetDisableTime().AsTime().IsZero() {
					hasDisabledAlts = true
				}
			}
		}
	}

	// Rebuild the second degree alternates
	for userID := range secondMap {
		h.SecondDegreeAlternates = append(h.SecondDegreeAlternates, userID)
	}

	slices.Sort(h.SecondDegreeAlternates)

	// Remove duplicates
	h.SecondDegreeAlternates = slices.Compact(h.SecondDegreeAlternates)

	// Convert excludeUserIDs to a map for O(1) lookups
	excludeUserIDsMap := make(map[string]bool, len(excludeUserIDs))
	for _, excludeID := range excludeUserIDs {
		excludeUserIDsMap[excludeID] = true
	}

	// Update the login histories of first-degree alternates (bidirectional relationship)
	for alternateUserID := range h.AlternateMatches {
		// Skip if this userID is in the excluded list
		if excludeUserIDsMap[alternateUserID] {
			continue
		}

		// Load the alternate's login history
		alternateHistory := NewLoginHistory(alternateUserID)
		if err := StorableReadNk(ctx, nk, alternateUserID, alternateHistory, false); err != nil {
			// Log warning but continue - don't fail the entire operation
			logger.WithFields(map[string]interface{}{
				"current_user_id":   h.userID,
				"alternate_user_id": alternateUserID,
				"error":             err,
			}).Warn("Failed to load alternate user's login history for bidirectional update")
			continue
		}

		// Check if current user is already in the alternate's matches
		if alternateHistory.AlternateMatches == nil {
			alternateHistory.AlternateMatches = make(map[string][]*AlternateSearchMatch)
		}

		// Find matches between current user and the alternate
		currentUserMatches := loginHistoryCompare(alternateHistory, h)
		if len(currentUserMatches) > 0 {
			// Update the alternate's matches to include current user
			alternateHistory.AlternateMatches[h.userID] = currentUserMatches

			// Save the updated alternate history
			if err := StorableWriteNk(ctx, nk, alternateUserID, alternateHistory); err != nil {
				// Log warning but continue - don't fail the entire operation
				logger.WithFields(map[string]interface{}{
					"current_user_id":   h.userID,
					"alternate_user_id": alternateUserID,
					"error":             err,
				}).Warn("Failed to save alternate user's login history for bidirectional update")
				continue
			}
		}
	}

	// Check if the alternates have changed
	return hasDisabledAlts, nil
}

func (h *LoginHistory) GetXPI(xpid evr.XPID) (time.Time, bool) {
	if h.XPIs != nil {
		if t, found := h.XPIs[xpid.String()]; found {
			return t, true
		}
	}
	return time.Time{}, false
}

func (h *LoginHistory) rebuildCache() {
	historyLen := len(h.History)
	h.Cache = make([]string, 0, historyLen*4)
	h.XPIs = make(map[string]time.Time, historyLen)
	h.ClientIPs = make(map[string]time.Time, historyLen)

	cacheSet := make(map[string]bool, historyLen*4)

	// Process each history entry in one pass
	for _, e := range h.History {
		// Process items for cache
		for _, s := range e.Items() {
			if _, found := cacheSet[s]; !found && !matchIgnoredAltPattern(s) {
				h.Cache = append(h.Cache, s)
				cacheSet[s] = true
			}
		}

		// Process XPIs
		if !e.XPID.IsNil() {
			evrIDStr := e.XPID.String()
			if t, found := h.XPIs[evrIDStr]; !found || t.Before(e.UpdatedAt) {
				h.XPIs[evrIDStr] = e.UpdatedAt
			}
		}

		// Process ClientIPs
		if e.ClientIP != "" {
			if t, found := h.ClientIPs[e.ClientIP]; !found || t.Before(e.UpdatedAt) {
				h.ClientIPs[e.ClientIP] = e.UpdatedAt
			}
		}
	}

	// Add denied client addresses to cache
	if h.DeniedClientAddresses != nil {
		for _, addr := range h.DeniedClientAddresses {
			if _, found := cacheSet[addr]; !found {
				h.Cache = append(h.Cache, addr)
				cacheSet[addr] = true
			}
		}
	}

	// Sort and compact the cache
	slices.Sort(h.Cache)
	h.Cache = slices.Compact(h.Cache)

	// Limit cache size to prevent unbounded growth
	if len(h.Cache) > MaxCacheSize {
		h.Cache = h.Cache[:MaxCacheSize]
	}
}

func (h *LoginHistory) MarshalJSON() ([]byte, error) {
	if h.userID == "" {
		return nil, fmt.Errorf("missing user ID")
	}

	// Alias to avoid recursion during marshaling
	type Alias LoginHistory
	aux := &struct {
		*Alias
	}{
		Alias: (*Alias)(h),
	}

	// Keep the history size under 5MB
	var bytes []byte
	var err error
	maxSize := 5 * 1024 * 1024

	for {
		// Rebuild the cache
		h.rebuildCache()

		// Clear authorized IPs that haven't been used in over 30 days
		for ip := range h.AuthorizedIPs {
			// Check if the IP is still in use
			lastUse, found := h.ClientIPs[ip]
			if !found || time.Since(lastUse) > 30*24*time.Hour {
				delete(h.AuthorizedIPs, ip)
			}
		}

		for ip, e := range h.PendingAuthorizations {
			if net.ParseIP(ip) == nil {
				delete(h.PendingAuthorizations, ip)
			} else if time.Since(e.CreatedAt) > 10*time.Minute {
				delete(h.PendingAuthorizations, ip)
			}
		}

		bytes, err = json.Marshal(aux)
		if err != nil {
			return nil, fmt.Errorf("error marshalling display name history: %w", err)
		}

		if len(bytes) < maxSize {
			return bytes, nil
		}

		// Estimate how many entries to remove based on current size
		currentSize := len(bytes)
		excessSize := currentSize - maxSize
		historyCount := len(h.History)

		if historyCount == 0 {
			// No more entries to remove, but still too large
			return bytes, nil
		}

		// Estimate entries to remove (with safety margin)
		entriesPerByte := float64(historyCount) / float64(currentSize)
		entriesToRemove := int(float64(excessSize)*entriesPerByte*1.2) + 1 // 20% safety margin

		if entriesToRemove > historyCount {
			entriesToRemove = historyCount
		}
		if entriesToRemove < 1 {
			entriesToRemove = 1
		}

		// Remove the oldest entries
		oldestEntries := make([]string, 0, entriesToRemove)
		oldestTimes := make([]time.Time, 0, entriesToRemove)

		for k, e := range h.History {
			updateTime := e.UpdatedAt
			inserted := false

			for i, t := range oldestTimes {
				if updateTime.Before(t) {
					// Insert at position i
					oldestEntries = append(oldestEntries[:i], append([]string{k}, oldestEntries[i:]...)...)
					oldestTimes = append(oldestTimes[:i], append([]time.Time{updateTime}, oldestTimes[i:]...)...)
					inserted = true
					break
				}
			}

			if !inserted && len(oldestEntries) < entriesToRemove {
				oldestEntries = append(oldestEntries, k)
				oldestTimes = append(oldestTimes, updateTime)
			}

			// Keep only the entries we need
			if len(oldestEntries) > entriesToRemove {
				oldestEntries = oldestEntries[:entriesToRemove]
				oldestTimes = oldestTimes[:entriesToRemove]
			}
		}

		// Remove the oldest entries
		for _, key := range oldestEntries {
			delete(h.History, key)
		}
	}
}

func LoginHistoryRegexSearch(ctx context.Context, nk runtime.NakamaModule, pattern string, limit int) ([]string, error) {

	query := fmt.Sprintf("+value.cache:/%s/", pattern)
	// Perform the storage list operation

	cursor := ""

	userIDs := make([]string, 0, limit)
	for {
		result, cursor, err := nk.StorageIndexList(ctx, SystemUserID, LoginHistoryCacheIndex, query, limit, nil, cursor)
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

func AccountGetDeviceID(ctx context.Context, db *sql.DB, nk runtime.NakamaModule, deviceID string) (*EVRProfile, error) {
	found := true

	// Look for an existing account.
	query := "SELECT user_id FROM user_device WHERE id = $1"
	var dbUserID string
	err := db.QueryRowContext(ctx, query, deviceID).Scan(&dbUserID)
	if err != nil {
		if err == sql.ErrNoRows {
			found = false
		} else {
			return nil, status.Error(codes.Internal, "Error finding user account by device id.")
		}
	}

	if found {
		if account, err := nk.AccountGetId(ctx, dbUserID); err != nil {
			return nil, status.Error(codes.Internal, "Error finding user account by device id.")
		} else {
			return BuildEVRProfileFromAccount(account)
		}
	}

	return nil, status.Error(codes.NotFound, "User account not found.")
}
