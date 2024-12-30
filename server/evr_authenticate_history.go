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
	"go.uber.org/zap"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const (
	LoginStorageCollection = "Devices"
	LoginHistoryStorageKey = "history"
	LoginHistoryCacheIndex = "Index_DeviceHistory"
)

var (
	IgnoredLoginValues = map[string]struct{}{
		"":               {},
		"1WMHH000X00000": {},
		"N/A":            {},
		"UNK-0":          {},
		"unknown":        {},
		"1PASH5D1P17365": {},
	}
)

type LoginHistoryEntry struct {
	UpdatedAt time.Time         `json:"update_time"`
	XPID      evr.EvrId         `json:"xpi"`
	ClientIP  string            `json:"client_ip"`
	LoginData *evr.LoginProfile `json:"login_data"`
}

func (h *LoginHistoryEntry) Key() string {
	return h.XPID.Token() + ":" + h.ClientIP
}

func (h *LoginHistoryEntry) SystemProfile() string {
	components := []string{h.LoginData.SystemInfo.HeadsetType, h.LoginData.SystemInfo.NetworkType, h.LoginData.SystemInfo.VideoCard, h.LoginData.SystemInfo.CPUModel, fmt.Sprintf("%d", h.LoginData.SystemInfo.NumPhysicalCores), fmt.Sprintf("%d", h.LoginData.SystemInfo.NumLogicalCores), fmt.Sprintf("%d", h.LoginData.SystemInfo.MemoryTotal), fmt.Sprintf("%d", h.LoginData.SystemInfo.DedicatedGPUMemory)}

	for i := range components {
		components[i] = strings.ReplaceAll(components[i], "::", ";")
	}

	return strings.Join(components, "::")
}

type LoginHistory struct {
	History          map[string]*LoginHistoryEntry `json:"history"` // map[deviceID]DeviceHistoryEntry
	Cache            []string                      `json:"cache"`   // list of IP addresses, EvrID's, HMD Serial Numbers, and System Data
	XPIs             map[string]time.Time          `json:"xpis"`    // list of XPIs
	ClientIPs        map[string]time.Time          `json:"client_ips"`
	AuthorizedIPs    map[string]time.Time          `json:"authorized_ips"`
	AlternateUserIDs []string                      `json:"alternates"`
	NotifiedGroupIDs map[string]time.Time          `json:"notified_groups"` // list of groups that have been notified of this alternate login
	userID           string                        // user ID
	version          string                        // storage record version
}

func NewLoginHistory() *LoginHistory {
	return &LoginHistory{
		History:          make(map[string]*LoginHistoryEntry),
		Cache:            make([]string, 0),
		XPIs:             make(map[string]time.Time),
		ClientIPs:        make(map[string]time.Time),
		AuthorizedIPs:    make(map[string]time.Time),
		AlternateUserIDs: make([]string, 0),
		NotifiedGroupIDs: make(map[string]time.Time),
	}
}

// Returns true if the display name history was updated
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
	h.History[e.Key()] = e
}

func (h *LoginHistory) Insert(entry *LoginHistoryEntry) {
	if h.History == nil {
		h.History = make(map[string]*LoginHistoryEntry)
	}

	h.History[entry.Key()] = entry
}

func (h *LoginHistory) AuthorizeIP(ip string) {
	if h.AuthorizedIPs == nil {
		h.AuthorizedIPs = make(map[string]time.Time)
	}

	h.AuthorizedIPs[ip] = time.Now().UTC()
}

func (h *LoginHistory) IsAuthorizedIP(ip string) bool {
	if h.AuthorizedIPs == nil {
		return false
	}
	_, found := h.AuthorizedIPs[ip]
	return found
}

func (h *LoginHistory) NotifyGroup(groupID string) bool {
	if h.NotifiedGroupIDs == nil {
		h.NotifiedGroupIDs = make(map[string]time.Time)
	}
	if len(h.AlternateUserIDs) == 0 {
		return false
	}

	if _, found := h.NotifiedGroupIDs[groupID]; found {
		return false
	}
	h.NotifiedGroupIDs[groupID] = time.Now().UTC()
	return true
}

func (h *LoginHistory) UpdateAlternateUserIDs(ctx context.Context, nk runtime.NakamaModule) error {
	matches, err := LoginAlternateSearch(ctx, nk, h)
	if err != nil {
		return fmt.Errorf("error searching for alternate logins: %w", err)
	}

	// collect the userIDs of all the alternate logins
	alternateMap := make(map[string]struct{})
	for _, m := range matches {
		alternateMap[m.OtherUserID] = struct{}{}

		otherHistory, err := LoginHistoryLoad(ctx, nk, m.OtherUserID)
		if err != nil {
			return fmt.Errorf("error loading alternate login history: %w", err)
		}

		for _, k := range otherHistory.AlternateUserIDs {
			alternateMap[k] = struct{}{}
		}
	}

	h.AlternateUserIDs = make([]string, 0)
	for k := range alternateMap {
		h.AlternateUserIDs = append(h.AlternateUserIDs, k)
	}

	slices.Sort(h.AlternateUserIDs)

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
		return NewLoginHistory(), nil
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

	history.rebuildCache()

	// Keep the history size under 5MB

	bytes := make([]byte, 0)
	var err error
	for {

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

		history.rebuildCache()
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

func DeviceCacheRegexSearch(ctx context.Context, nk runtime.NakamaModule, pattern string, limit int) (map[string]*LoginHistory, error) {
	query := fmt.Sprintf("+value.cache:/%s/", pattern)
	// Perform the storage list operation

	cursor := ""

	hardLimit := 1000
	results := make([]*api.StorageObject, 0, 100)
	var err error
	var result *api.StorageObjects
	for {
		result, cursor, err = nk.StorageIndexList(ctx, SystemUserID, LoginHistoryCacheIndex, query, 100, []string{"value.active"}, cursor)
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

	History := make(map[string]*LoginHistory, len(result.Objects))
	for _, obj := range result.Objects {
		var history LoginHistory
		if err := json.Unmarshal([]byte(obj.Value), &history); err != nil {
			return nil, fmt.Errorf("error unmarshalling display name history: %w", err)
		}
		History[obj.UserId] = &history
	}

	return History, nil
}

func GetUserIDByDeviceID(ctx context.Context, logger *zap.Logger, db *sql.DB, deviceID string) (string, error) {
	found := true

	// Look for an existing account.
	query := "SELECT user_id FROM user_device WHERE id = $1"
	var dbUserID string
	err := db.QueryRowContext(ctx, query, deviceID).Scan(&dbUserID)
	if err != nil {
		if err == sql.ErrNoRows {
			found = false
		} else {
			logger.Error("Error looking up user by device ID.", zap.Error(err), zap.String("deviceID", deviceID))
			return "", status.Error(codes.Internal, "Error finding user account.")
		}
	}

	if found {
		return dbUserID, nil
	}

	return "", status.Error(codes.NotFound, "User account not found.")
}
