package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/heroiclabs/nakama-common/api"
	"github.com/heroiclabs/nakama-common/runtime"
	"github.com/heroiclabs/nakama/v3/server/evr"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const (
	LoginStorageCollection = "Login"
	LoginHistoryStorageKey = "history"
	LoginHistoryCacheIndex = "Index_LoginHistory"
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
		"VRLINKHMDQUEST3": {}, // Quest link
	}
)

type LoginHistoryEntry struct {
	CreatedAt time.Time         `json:"create_time"`
	UpdatedAt time.Time         `json:"update_time"`
	XPID      evr.EvrId         `json:"xpi"`
	ClientIP  string            `json:"client_ip"`
	LoginData *evr.LoginProfile `json:"login_data"`
}

func (h *LoginHistoryEntry) Key() string {
	return h.XPID.Token() + ":" + h.ClientIP
}

func (h *LoginHistoryEntry) SystemProfile() string {
	components := []string{normalizeHeadsetType(h.LoginData.SystemInfo.HeadsetType), h.LoginData.SystemInfo.NetworkType, h.LoginData.SystemInfo.VideoCard, h.LoginData.SystemInfo.CPUModel, fmt.Sprintf("%d", h.LoginData.SystemInfo.NumPhysicalCores), fmt.Sprintf("%d", h.LoginData.SystemInfo.NumLogicalCores), fmt.Sprintf("%d", h.LoginData.SystemInfo.MemoryTotal), fmt.Sprintf("%d", h.LoginData.SystemInfo.DedicatedGPUMemory)}

	for i := range components {
		components[i] = strings.ReplaceAll(components[i], "::", ";")
	}

	return strings.Join(components, "::")
}

func (h *LoginHistoryEntry) Items() []string {
	return []string{h.ClientIP, h.LoginData.HMDSerialNumber, h.XPID.Token(), h.SystemProfile()}
}

func (h *LoginHistoryEntry) ItemMap() map[string]struct{} {
	return map[string]struct{}{
		h.ClientIP:                  {},
		h.LoginData.HMDSerialNumber: {},
		h.XPID.Token():              {},
		h.SystemProfile():           {}}
}

type LoginHistory struct {
	History                map[string]*LoginHistoryEntry      `json:"history"` // map[deviceID]DeviceHistoryEntry
	Cache                  []string                           `json:"cache"`   // list of IP addresses, EvrID's, HMD Serial Numbers, and System Data
	XPIs                   map[string]time.Time               `json:"xpis"`    // list of XPIs
	ClientIPs              map[string]time.Time               `json:"client_ips"`
	AuthorizedIPs          map[string]time.Time               `json:"authorized_client_ips"`
	PendingAuthorizations  map[string]*LoginHistoryEntry      `json:"pending_authorizations"`
	SecondDegreeAlternates []string                           `json:"second_degree"`
	AlternateMap           map[string][]*AlternateSearchMatch `json:"alternate_accounts"` // map of alternate user IDs and what they have in common
	GroupNotifications     map[string]map[string]time.Time    `json:"notified_groups"`    // list of groups that have been notified of this alternate login
	userID                 string                             // user ID
	version                string                             // storage record version
}

func (LoginHistory) StorageID() StorageID {
	return StorageID{
		Collection: LoginStorageCollection,
		Key:        LoginHistoryStorageKey,
	}
}

func (LoginHistory) StorageIndex() *StorageIndexMeta {
	return &StorageIndexMeta{
		Name:           LoginHistoryCacheIndex,
		Collection:     LoginStorageCollection,
		Key:            LoginHistoryStorageKey,
		Fields:         []string{"cache", "xpis", "client_ips", "second_order", "alternate_matches"},
		SortableFields: nil,
		MaxEntries:     1000000,
		IndexOnly:      false,
	}
}

func NewLoginHistory(userID string) *LoginHistory {
	return &LoginHistory{
		History:                make(map[string]*LoginHistoryEntry),
		Cache:                  make([]string, 0),
		XPIs:                   make(map[string]time.Time),
		ClientIPs:              make(map[string]time.Time),
		AuthorizedIPs:          make(map[string]time.Time),
		PendingAuthorizations:  make(map[string]*LoginHistoryEntry),
		SecondDegreeAlternates: make([]string, 0),
		AlternateMap:           make(map[string][]*AlternateSearchMatch),
		GroupNotifications:     make(map[string]map[string]time.Time),
		userID:                 userID,
	}
}

func (h *LoginHistory) Update(xpid evr.EvrId, clientIP string, loginData *evr.LoginProfile) {
	if h.History == nil {
		h.History = make(map[string]*LoginHistoryEntry)
	}

	e := &LoginHistoryEntry{
		UpdatedAt: time.Now(),
		XPID:      xpid,
		ClientIP:  clientIP,
		LoginData: loginData,
	}

	if h.History[e.Key()] != nil {
		e.CreatedAt = h.History[e.Key()].CreatedAt
	}

	h.History[e.Key()] = e
}

func (h *LoginHistory) Insert(entry *LoginHistoryEntry) {
	if h.History == nil {
		h.History = make(map[string]*LoginHistoryEntry)
	}

	h.History[entry.Key()] = entry
}

func (h *LoginHistory) AuthorizeIPWithCode(ip, code string) error {
	if h.AuthorizedIPs == nil {
		h.AuthorizedIPs = make(map[string]time.Time)
	}

	found := false
	var entry *LoginHistoryEntry
	for k, e := range h.PendingAuthorizations {
		if e.ClientIP == ip {
			found = true
			// Always delete the entry
			delete(h.PendingAuthorizations, k)
			entry = e
			break
		}
	}

	if !found {
		return fmt.Errorf("no pending authorization found for IP %s", ip)
	}

	pendingCode := fmt.Sprintf("%02d", entry.CreatedAt.Nanosecond()%100)
	if pendingCode != code {

		return fmt.Errorf("invalid code %s for IP %s", code, ip)
	}

	h.AuthorizedIPs[ip] = time.Now().UTC()
	return nil
}

func (h *LoginHistory) AuthorizeIP(ip string) bool {
	if h.AuthorizedIPs == nil {
		h.AuthorizedIPs = make(map[string]time.Time)
	}
	isNew := false
	if _, found := h.AuthorizedIPs[ip]; !found {
		isNew = true
	}
	h.AuthorizedIPs[ip] = time.Now().UTC()
	if h.PendingAuthorizations != nil {
		delete(h.PendingAuthorizations, ip)
	}
	return isNew
}

func (h *LoginHistory) IsAuthorizedIP(ip string) bool {
	if h.AuthorizedIPs == nil {
		return false
	}
	_, found := h.AuthorizedIPs[ip]
	return found
}

func (h *LoginHistory) AddPendingAuthorizationIP(xpid evr.EvrId, clientIP string, loginData *evr.LoginProfile) *LoginHistoryEntry {
	if h.PendingAuthorizations == nil {
		h.PendingAuthorizations = make(map[string]*LoginHistoryEntry)
	}
	e := &LoginHistoryEntry{
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
		XPID:      xpid,
		ClientIP:  clientIP,
		LoginData: loginData,
	}

	h.PendingAuthorizations[e.Key()] = e
	return e
}

func (h *LoginHistory) GetPendingAuthorizationIP(ip string) *LoginHistoryEntry {
	if h.PendingAuthorizations == nil {
		return nil
	}
	return h.PendingAuthorizations[ip]
}

func (h *LoginHistory) RemovePendingAuthorizationIP(ip string) {
	if h.PendingAuthorizations == nil {
		return
	}
	delete(h.PendingAuthorizations, ip)
}

func (h *LoginHistory) NotifyGroup(groupID string, threshold time.Time) bool {

	firstIDs := make([]string, 0, len(h.AlternateMap)+len(h.SecondDegreeAlternates))
	for k := range h.AlternateMap {
		firstIDs = append(firstIDs, k)
	}

	userIDs := append(firstIDs, h.SecondDegreeAlternates...)

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

func (h *LoginHistory) UpdateAlternates(ctx context.Context, nk runtime.NakamaModule) error {
	matches, err := LoginAlternateSearch(ctx, nk, h)
	if err != nil {
		return fmt.Errorf("error searching for alternate logins: %w", err)
	}

	h.AlternateMap = make(map[string][]*AlternateSearchMatch, len(matches))
	h.SecondDegreeAlternates = make([]string, 0)

	for _, m := range matches {
		if _, found := h.AlternateMap[m.otherHistory.userID]; !found {
			// add second-level alternates
			for id := range m.otherHistory.AlternateMap {
				if id == h.userID {
					continue
				}
				h.SecondDegreeAlternates = append(h.SecondDegreeAlternates, id)
			}
		}
		h.AlternateMap[m.otherHistory.userID] = append(h.AlternateMap[m.otherHistory.userID], m)
	}

	slices.Sort(h.SecondDegreeAlternates)
	h.SecondDegreeAlternates = slices.Compact(h.SecondDegreeAlternates)
	return nil
}

func (h *LoginHistory) rebuildCache() {
	h.Cache = make([]string, 0, len(h.History)*4)
	h.XPIs = make(map[string]time.Time, len(h.History))
	h.ClientIPs = make(map[string]time.Time, len(h.History))
	for _, e := range h.History {
		h.Cache = append(h.Cache, e.ClientIP)

		h.Cache = append(h.Cache, e.LoginData.HMDSerialNumber)
		if !e.XPID.IsNil() {
			h.Cache = append(h.Cache, e.XPID.Token())
		}
		h.Cache = append(h.Cache, e.SystemProfile())

		if !e.XPID.IsNil() {
			evrIDStr := e.XPID.String()
			if t, found := h.XPIs[evrIDStr]; !found || e.UpdatedAt.After(t) {
				h.XPIs[evrIDStr] = e.UpdatedAt
			}
		}

		if e.ClientIP != "" {
			if t, found := h.ClientIPs[e.ClientIP]; !found || e.UpdatedAt.After(t) {
				h.ClientIPs[e.ClientIP] = e.UpdatedAt
			}
		}
	}
	slices.Sort(h.Cache)
	h.Cache = slices.Compact(h.Cache)
	for i := 0; i < len(h.Cache); i++ {
		if _, ok := IgnoredLoginValues[h.Cache[i]]; ok {
			h.Cache = append(h.Cache[:i], h.Cache[i+1:]...)
			i--
		}
	}
}

func (h *LoginHistory) Store(ctx context.Context, nk runtime.NakamaModule) error {
	return LoginHistoryStore(ctx, nk, h.userID, h)
}

func LoginHistoryLoad(ctx context.Context, nk runtime.NakamaModule, userID string) (*LoginHistory, error) {
	if userID == "" || userID == SystemUserID {
		return nil, fmt.Errorf("invalid user ID: %s", userID)
	}

	objects, err := nk.StorageRead(ctx, []*runtime.StorageRead{
		{
			Collection: LoginStorageCollection,
			Key:        LoginHistoryStorageKey,
			UserID:     userID,
		},
	})
	if err != nil {
		return nil, fmt.Errorf("error reading display name cache: %w", err)
	}

	if len(objects) == 0 {
		return NewLoginHistory(userID), nil
	}

	var history LoginHistory
	if err := json.Unmarshal([]byte(objects[0].Value), &history); err != nil {
		return nil, fmt.Errorf("error unmarshalling display name cache: %w", err)
	}
	history.userID = userID
	history.version = objects[0].Version

	return &history, nil
}

func LoginHistoryStore(ctx context.Context, nk runtime.NakamaModule, userID string, history *LoginHistory) error {

	// Clear authorized IPs that haven't been used in over 30 days
	for ip, t := range history.AuthorizedIPs {
		if time.Since(t) > 30*24*time.Hour {
			delete(history.AuthorizedIPs, ip)
		}
	}

	// Keep the history size under 5MB
	bytes := make([]byte, 0)
	var err error
	for {
		history.rebuildCache()
		bytes, err = json.Marshal(history)
		if err != nil {
			return fmt.Errorf("error marshalling display name history: %w", err)
		}

		if len(bytes) < 5*1024*1024 {
			break
		}

		// Remove the oldest entries
		for i := 0; i < 3; i++ {
			oldest := time.Now()
			oldestKey := ""
			for k, e := range history.History {
				if e.UpdatedAt.Before(oldest) {
					oldest = e.UpdatedAt
					oldestKey = k
				}
			}
			delete(history.History, oldestKey)
		}
	}

	acks, err := nk.StorageWrite(ctx, []*runtime.StorageWrite{
		{
			Collection: LoginStorageCollection,
			Key:        LoginHistoryStorageKey,
			Value:      string(bytes),
			UserID:     userID,
			Version:    history.version,
		},
	})

	if err != nil {
		return fmt.Errorf("error writing display name history: %w", err)
	}

	if acks[0].Version != history.version {
		history.version = acks[0].Version
	}

	return nil
}

func LoginHistoryUpdate(ctx context.Context, nk runtime.NakamaModule, userID string, xpi evr.EvrId, clientIP string, loginData *evr.LoginProfile) error {
	history, err := LoginHistoryLoad(ctx, nk, userID)
	if err != nil {
		return fmt.Errorf("error getting display name history: %w", err)
	}

	history.Update(xpi, clientIP, loginData)

	if err := LoginHistoryStore(ctx, nk, userID, history); err != nil {
		return fmt.Errorf("error storing display name history: %w", err)
	}

	return nil
}

func DeviceCacheRegexSearch(ctx context.Context, nk runtime.NakamaModule, pattern string, limit int, cursor string) (map[string]*LoginHistory, error) {

	query := fmt.Sprintf("+value.cache:/%s/", pattern)
	// Perform the storage list operation

	result, cursor, err := nk.StorageIndexList(ctx, SystemUserID, LoginHistoryCacheIndex, query, limit, []string{"value.active"}, cursor)
	if err != nil {
		return nil, fmt.Errorf("error listing display name history: %w", err)
	}

	histories := make(map[string]*LoginHistory, len(result.Objects))

	for _, obj := range result.Objects {
		var history LoginHistory
		if err := json.Unmarshal([]byte(obj.Value), &history); err != nil {
			return nil, fmt.Errorf("error unmarshalling display name history: %w", err)
		}
		histories[obj.UserId] = &history
	}

	return histories, nil
}

func AccountGetDeviceID(ctx context.Context, db *sql.DB, nk runtime.NakamaModule, deviceID string) (*api.Account, error) {
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
		return nk.AccountGetId(ctx, dbUserID)
	}

	return nil, status.Error(codes.NotFound, "User account not found.")
}
