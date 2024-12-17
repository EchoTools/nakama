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
	"github.com/heroiclabs/nakama/v3/server/evr"
)

const (
	LoginStorageCollection = "Devices"
	LoginHistoryStorageKey = "history"
	LoginHistoryCacheIndex = "Index_DeviceHistory"
)

var (
	ignoredLoginCacheKeys = map[string]struct{}{
		"N/A":     {},
		"":        {},
		"UNK-0":   {},
		"unknown": {},
	}
)

type LoginHistoryEntry struct {
	UpdatedAt  time.Time         `json:"update_time"`
	DeviceAuth DeviceAuth        `json:"device_auth"`
	LoginData  *evr.LoginProfile `json:"login_data"`
}

func (h *LoginHistoryEntry) SystemProfile() string {
	components := []string{
		h.LoginData.SystemInfo.HeadsetType,
		h.LoginData.SystemInfo.NetworkType,
		h.LoginData.SystemInfo.VideoCard,
		h.LoginData.SystemInfo.CPUModel,
		fmt.Sprintf("%d", h.LoginData.SystemInfo.NumPhysicalCores),
		fmt.Sprintf("%d", h.LoginData.SystemInfo.NumLogicalCores),
		fmt.Sprintf("%d", h.LoginData.SystemInfo.MemoryTotal),
		fmt.Sprintf("%d", h.LoginData.SystemInfo.DedicatedGPUMemory),
	}

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
	AlternateUserIDs []string                      `json:"alternates"`
	version          string                        // storage record version
}

func NewLoginHistory() *LoginHistory {
	return &LoginHistory{
		History:          make(map[string]*LoginHistoryEntry),
		Cache:            make([]string, 0),
		XPIs:             make(map[string]time.Time),
		ClientIPs:        make(map[string]time.Time),
		AlternateUserIDs: make([]string, 0),
	}
}

// Returns true if the display name history was updated
func (h *LoginHistory) Update(deviceAuth DeviceAuth, loginData *evr.LoginProfile) {
	if h.History == nil {
		h.History = make(map[string]*LoginHistoryEntry)
	}

	h.History[deviceAuth.Token()] = &LoginHistoryEntry{
		UpdatedAt:  time.Now(),
		DeviceAuth: deviceAuth,
		LoginData:  loginData,
	}
}

func (h *LoginHistory) Insert(entry *LoginHistoryEntry) {
	if h.History == nil {
		h.History = make(map[string]*LoginHistoryEntry)
	}

	h.History[entry.DeviceAuth.Token()] = entry
}

func (h *LoginHistory) rebuildCache() {
	h.Cache = make([]string, 0, len(h.History)*4)
	h.XPIs = make(map[string]time.Time, len(h.History))
	h.ClientIPs = make(map[string]time.Time, len(h.History))
	for _, e := range h.History {
		h.Cache = append(h.Cache, e.DeviceAuth.ClientIP)
		h.Cache = append(h.Cache, e.DeviceAuth.EvrID.String())
		h.Cache = append(h.Cache, e.DeviceAuth.HMDSerialNumber)
		h.Cache = append(h.Cache, e.SystemProfile())

		if !e.DeviceAuth.EvrID.IsNil() {
			evrIDStr := e.DeviceAuth.EvrID.String()
			if t, found := h.XPIs[evrIDStr]; !found || e.UpdatedAt.After(t) {
				h.XPIs[evrIDStr] = e.UpdatedAt
			}
		}

		if e.DeviceAuth.ClientIP != "" {
			if t, found := h.ClientIPs[e.DeviceAuth.ClientIP]; !found || e.UpdatedAt.After(t) {
				h.ClientIPs[e.DeviceAuth.ClientIP] = e.UpdatedAt
			}
		}
	}
	slices.Sort(h.Cache)
	h.Cache = slices.Compact(h.Cache)
	for i := 0; i < len(h.Cache); i++ {
		if _, ok := ignoredLoginCacheKeys[h.Cache[i]]; ok {
			h.Cache = append(h.Cache[:i], h.Cache[i+1:]...)
			i--
		}
	}
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

	if _, err := nk.StorageWrite(ctx, []*runtime.StorageWrite{
		{
			Collection: LoginStorageCollection,
			Key:        LoginHistoryStorageKey,
			Value:      string(bytes),
			UserID:     userID,
			Version:    history.version,
		},
	}); err != nil {
		return fmt.Errorf("error writing display name history: %w", err)
	}

	return nil
}

func LoginHistoryUpdate(ctx context.Context, nk runtime.NakamaModule, userID string, deviceAuth DeviceAuth, loginData *evr.LoginProfile) error {
	history, err := LoginHistoryLoad(ctx, nk, userID)
	if err != nil {
		return fmt.Errorf("error getting display name history: %w", err)
	}

	history.Update(deviceAuth, loginData)

	matches, err := LoginAlternateSearch(ctx, nk, userID, history)
	if err != nil {
		return fmt.Errorf("error searching for alternate logins: %w", err)
	}

	if history.AlternateUserIDs == nil {
		history.AlternateUserIDs = make([]string, 0)
	}

	for _, m := range matches {
		if !slices.Contains(history.AlternateUserIDs, m.OtherUserID) {
			history.AlternateUserIDs = append(history.AlternateUserIDs, m.OtherUserID)
		}
	}

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
