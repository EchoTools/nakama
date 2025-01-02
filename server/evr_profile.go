package server

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"time"

	"github.com/gofrs/uuid/v5"
	"github.com/heroiclabs/nakama-common/runtime"
	"github.com/heroiclabs/nakama/v3/server/evr"
	"github.com/intinig/go-openskill/types"
)

type GameProfile interface {
	GetVersion() string
	GetEvrID() evr.EvrId
	SetLogin(login evr.LoginProfile)
	SetClient(client evr.ClientProfile)
	SetServer(server evr.ServerProfile)
	GetServer() evr.ServerProfile
	GetClient() evr.ClientProfile
	GetLogin() evr.LoginProfile
	GetChannel() uuid.UUID
	SetChannel(c evr.GUID)
	UpdateDisplayName(displayName string)
	UpdateUnlocks(unlocks evr.UnlockedCosmetics) error
	IsModified() bool
	SetStale()
	ClearStale()
}

type GameProfileData struct {
	sync.RWMutex
	Login        evr.LoginProfile                          `json:"login"`
	Client       evr.ClientProfile                         `json:"client"`
	Server       evr.ServerProfile                         `json:"server"`
	Ratings      map[uuid.UUID]map[evr.Symbol]types.Rating `json:"ratings"`
	EarlyQuits   EarlyQuitStatistics                       `json:"early_quit"`
	GameSettings evr.RemoteLogGameSettings                 `json:"game_settings"`
	version      string                                    // The version of the profile from the DB
	isModified   bool                                      // Whether the profile is stale and needs to be updated
	jsonData     *json.RawMessage                          // The raw JSON data of the profile
}

func NewGameProfile(login evr.LoginProfile, client evr.ClientProfile, server evr.ServerProfile, version string) GameProfileData {
	return GameProfileData{
		Login:   login,
		Client:  client,
		Server:  server,
		version: version,
		Ratings: make(map[uuid.UUID]map[evr.Symbol]types.Rating),
	}
}

func (p *GameProfileData) GetCached() *json.RawMessage {
	p.Lock()
	defer p.Unlock()
	if p.jsonData != nil && p.isModified {
		return p.jsonData
	}

	data, err := json.Marshal(p)
	if err != nil {
		return nil
	}
	p.jsonData = (*json.RawMessage)(&data)
	return p.jsonData
}

func (p *GameProfileData) GetVersion() string {
	p.RLock()
	defer p.RUnlock()
	return p.version
}

func (p *GameProfileData) SetLogin(login evr.LoginProfile) {
	p.Lock()
	defer p.Unlock()
	p.Login = login
	p.SetStale()
}

func (p *GameProfileData) SetClient(client evr.ClientProfile) {
	p.Lock()
	defer p.Unlock()
	p.Client = client
	p.SetStale()
}

func (p *GameProfileData) SetServer(server evr.ServerProfile) {
	p.Lock()
	defer p.Unlock()
	p.Server = server
	p.SetStale()
}

func (p *GameProfileData) SetGameSettings(settings evr.RemoteLogGameSettings) {
	p.Lock()
	defer p.Unlock()
	p.GameSettings = settings
	p.SetStale()
}
func (p *GameProfileData) SetEarlyQuitStatistics(stats EarlyQuitStatistics) {
	p.Lock()
	defer p.Unlock()
	p.EarlyQuits = stats
	p.SetStale()
}

func (p *GameProfileData) GetServer() evr.ServerProfile {
	p.Lock()
	defer p.Unlock()
	return p.Server
}

func (p *GameProfileData) GetClient() evr.ClientProfile {
	p.Lock()
	defer p.Unlock()
	return p.Client
}

func (p *GameProfileData) GetLogin() evr.LoginProfile {
	p.Lock()
	defer p.Unlock()
	return p.Login
}

func (p *GameProfileData) GetEarlyQuitStatistics() *EarlyQuitStatistics {
	p.Lock()
	defer p.Unlock()
	return &p.EarlyQuits
}

func (p *GameProfileData) GetRating(groupID uuid.UUID, mode evr.Symbol) types.Rating {
	p.Lock()
	defer p.Unlock()
	if p.Ratings == nil || p.Ratings[groupID] == nil || p.Ratings[groupID][mode] == (types.Rating{}) {
		return NewDefaultRating()
	}
	return p.Ratings[groupID][mode]
}

func (p *GameProfileData) SetRating(groupID uuid.UUID, mode evr.Symbol, rating types.Rating) {
	p.Lock()
	defer p.Unlock()
	if p.Ratings == nil {
		p.Ratings = make(map[uuid.UUID]map[evr.Symbol]types.Rating)
	}
	if p.Ratings[groupID] == nil {
		p.Ratings[groupID] = make(map[evr.Symbol]types.Rating)
	}
	p.Ratings[groupID][mode] = rating
	p.SetStale()
}

func (p *GameProfileData) SetEvrID(evrID evr.EvrId) {
	p.Lock()
	defer p.Unlock()
	if p.Server.EvrID == evrID && p.Client.EvrID == evrID {
		return
	}
	p.Server.EvrID = evrID
	p.Client.EvrID = evrID
	p.SetStale()

}

func (p *GameProfileData) GetEvrID() evr.EvrId {
	p.Lock()
	defer p.Unlock()
	return p.Server.EvrID
}

func (p *GameProfileData) GetChannel() uuid.UUID {
	p.Lock()
	defer p.Unlock()
	return uuid.UUID(p.Server.Social.Channel)
}

func (p *GameProfileData) SetJerseyNumber(number int) {
	if p.Server.EquippedCosmetics.Number == number && p.Server.EquippedCosmetics.NumberBody == number {
		return
	}

	p.Server.EquippedCosmetics.Number = number
	p.Server.EquippedCosmetics.NumberBody = number
	p.SetStale()
}

func (p *GameProfileData) SetChannel(c evr.GUID) {
	if p.Server.Social.Channel == c && p.Client.Social.Channel == c {
		return
	}
	p.Server.Social.Channel = c
	p.Client.Social.Channel = p.Server.Social.Channel
	p.SetStale()
}

func (p *GameProfileData) UpdateDisplayName(displayName string) {
	if p.Server.DisplayName == displayName && p.Client.DisplayName == displayName {
		return
	}
	p.Server.DisplayName = displayName
	p.Client.DisplayName = displayName
	p.SetStale()
}

func (p *GameProfileData) LatestStatistics(useGlobal bool, useWeekly bool, useDaily bool) evr.PlayerStatistics {

	allStats := evr.PlayerStatistics{
		"arena":  make(map[string]evr.MatchStatistic),
		"combat": make(map[string]evr.MatchStatistic),
	}

	// Start with all time stats

	if useGlobal {
		for t, s := range p.Server.Statistics {
			if !strings.HasPrefix(t, "daily_") && !strings.HasPrefix(t, "weekly_") {
				allStats[t] = s
			}
		}
	}
	var latestWeekly, latestDaily string

	for t := range p.Server.Statistics {
		if strings.HasPrefix(t, "weekly_") && (latestWeekly == "" || t > latestWeekly) {
			latestWeekly = t
		}
		if strings.HasPrefix(t, "daily_") && (latestDaily == "" || t > latestDaily) {
			latestDaily = t
		}
	}

	if useWeekly && latestWeekly != "" {
		for k, v := range p.Server.Statistics[latestWeekly] {
			allStats["arena"][k] = v
		}
	}

	if useDaily && latestDaily != "" {
		for k, v := range p.Server.Statistics[latestDaily] {
			allStats["arena"][k] = v
		}
	}

	return allStats
}

func (p *GameProfileData) ExpireStatistics(dailyAge time.Duration, weeklyAge time.Duration) {
	updated := false
	for t := range p.Server.Statistics {
		if t == "arena" || t == "combat" {
			continue
		}
		if strings.HasPrefix(t, "daily_") {
			// Parse the date
			date, err := time.Parse("2006_01_02", strings.TrimPrefix(t, "daily_"))
			// Keep anything less than 48 hours old
			if err == nil && time.Since(date) < dailyAge {
				continue
			}
		} else if strings.HasPrefix(t, "weekly_") {
			// Parse the date
			date, err := time.Parse("2006_01_02", strings.TrimPrefix(t, "weekly_"))
			// Keep anything less than 2 weeks old
			if err == nil && time.Since(date) < weeklyAge {
				continue
			}
		}
		delete(p.Server.Statistics, t)
		updated = true
	}
	if updated {
		p.SetStale()
	}
}

func (p *GameProfileData) SetStale() {
	p.isModified = true
	p.Server.UpdateTime = time.Now().UTC().Unix()
	p.Client.ModifyTime = time.Now().UTC().Unix()
}

func (p *GameProfileData) IsModified() bool {
	return p.isModified
}

func (p *GameProfileData) ClearStale() {
	p.isModified = false
}

func (p *GameProfileData) DisableAFKTimeout(enable bool) {

	if enable {
		if p.Server.DeveloperFeatures == nil {
			p.Server.DeveloperFeatures = &evr.DeveloperFeatures{
				DisableAfkTimeout: true,
			}
			p.SetStale()
			return
		}
		if p.Server.DeveloperFeatures.DisableAfkTimeout {
			return
		}
		p.Server.DeveloperFeatures.DisableAfkTimeout = true
		p.SetStale()
		return
	}
	if p.Server.DeveloperFeatures == nil {
		return
	}
	if !p.Server.DeveloperFeatures.DisableAfkTimeout {
		return
	}
	p.Server.DeveloperFeatures.DisableAfkTimeout = false
	p.SetStale()
}

// Set the user's profile based on their groups
func (r *ProfileRegistry) GenerateCosmetics(ctx context.Context, wallet map[string]int64, enableAll bool) error {

	// Convert the cosmetics to a map to compare with the wallet map
	cosmeticMap := make(map[string]bool)

	for k, _ := range wallet {
		if k, ok := strings.CutPrefix(k, "cosmetic:"); ok {
			cosmeticMap[k] = true
		}
	}

	return nil
}

func enforceLoadoutEntitlements(logger runtime.Logger, loadout *evr.CosmeticLoadout, unlocked *evr.UnlockedCosmetics, defaults map[string]string) error {
	unlockMap := unlocked.ToMap()

	loadoutMap := loadout.ToMap()

	for k, v := range loadoutMap {
		for _, unlocks := range unlockMap {
			if _, found := unlocks[v]; !found {
				logger.Warn("User has item equip that does not exist: %s: %s", k, v)
				loadoutMap[k] = defaults[k]
			} else if !unlocks[v] {
				logger.Warn("User does not have entitlement to item: %s: %s", k, v)
			}
		}
	}
	loadout.FromMap(loadoutMap)
	return nil
}
