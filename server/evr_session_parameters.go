package server

import (
	"context"
	"fmt"

	"github.com/heroiclabs/nakama/v3/server/evr"
	"go.uber.org/atomic"
)

type SessionParameters struct {
	node          string     // The node name
	xpID          evr.EvrId  // The Cross-Platform ID
	loginSession  *sessionWS // The login session
	lobbySession  *sessionWS // The match session
	serverSession *sessionWS // The server session

	IsWebsocketAuthenticated bool   // The session was authenticated successfully via HTTP headers or query parameters
	authDiscordID            string // The Discord ID use for authentication
	authPassword             string // The Password use for authentication
	userDisplayNameOverride  string // The display name override (user-defined)

	externalServerAddr string // The external server address (IP:port)
	geoHashPrecision   int    // The geohash precision
	isVPN              bool   // The user is using a VPN
	ipInfo             IPInfo // The IPQS data

	supportedFeatures []string          // features from the urlparam
	requiredFeatures  []string          // required_features from the urlparam
	disableEncryption bool              // The user has disabled encryption
	disableMAC        bool              // The user has disabled MAC
	loginPayload      *evr.LoginProfile // The login payload
	isGlobalDeveloper bool              // The user is a developer
	isGlobalOperator  bool              // The user is a moderator

	relayOutgoing bool                // The user wants (some) outgoing messages relayed to them via discord
	debug         bool                // The user wants debug information
	serverTags    []string            // []string of the server tags
	serverGuilds  []string            // []string of the server guilds
	serverRegions []string            // []string of the server regions
	urlParameters map[string][]string // The URL parameters

	profile                 *EVRProfile                                  // The account
	matchmakingSettings     *MatchmakingSettings                         // The matchmaking settings
	displayNameHistory      *DisplayNameHistory                          // The display name history
	guildGroups             map[string]*GuildGroup                       // map[string]*GuildGroup
	earlyQuitConfig         *atomic.Pointer[EarlyQuitConfig]             // The early quit config
	isGoldNameTag           *atomic.Bool                                 // If this user should have a gold name tag
	lastMatchmakingError    *atomic.Error                                // The last matchmaking error
	latencyHistory          *atomic.Pointer[LatencyHistory]              // The latency history
	isIGPOpen               *atomic.Bool                                 // The user has IGPU open
	activeSuspensionRecords map[string]map[string]GuildEnforcementRecord // The active suspension records map[groupID]map[userID]GuildEnforcementRecord
}

func (s SessionParameters) UserID() string {
	if s.profile == nil {
		return ""
	}
	return s.profile.UserID()
}

func (s SessionParameters) DisplayName(groupID string) string {
	if s.profile == nil {
		return ""
	}
	if s.userDisplayNameOverride != "" {
		return s.userDisplayNameOverride
	}
	return s.profile.GetGroupDisplayNameOrDefault(groupID)
}

func (s *SessionParameters) IsIGPOpen() bool {
	if s.isIGPOpen == nil {
		return false
	}
	return s.isIGPOpen.Load()
}

func (s *SessionParameters) MetricsTags() map[string]string {
	return map[string]string{
		"websocket_auth": fmt.Sprintf("%t", s.IsWebsocketAuthenticated),
		"is_vr":          fmt.Sprintf("%t", s.IsVR()),
		"is_pcvr":        fmt.Sprintf("%t", s.IsPCVR()),
		"build_version":  fmt.Sprintf("%d", s.BuildNumber()),
		"device_type":    s.DeviceType(),
	}
}

func (s *SessionParameters) IsVR() bool {
	if s.loginPayload == nil {
		return false
	}
	return s.loginPayload.SystemInfo.HeadsetType != "No VR"
}

func (s *SessionParameters) IsPCVR() bool {
	if s.loginPayload == nil {
		return false
	}
	return s.loginPayload.BuildNumber != evr.StandaloneBuildNumber
}

func (s *SessionParameters) BuildNumber() evr.BuildNumber {
	if s.loginPayload == nil {
		return 0
	}
	return s.loginPayload.BuildNumber
}

func (s *SessionParameters) DeviceType() string {
	if s.loginPayload == nil {
		return "Unknown"
	}
	return normalizeHeadsetType(s.loginPayload.SystemInfo.HeadsetType)
}

func (s *SessionParameters) DiscordID() string {
	if s.profile == nil {
		return ""
	}
	return s.profile.DiscordID()
}

func (s *SessionParameters) GeoHash() string {
	if s.ipInfo == nil {
		return ""
	}
	return s.ipInfo.GeoHash(2)
}

func StoreParams(ctx context.Context, params *SessionParameters) {
	ctx.Value(ctxSessionParametersKey{}).(*atomic.Pointer[SessionParameters]).Store(params)
}

func LoadParams(ctx context.Context) (parameters SessionParameters, found bool) {
	params, ok := ctx.Value(ctxSessionParametersKey{}).(*atomic.Pointer[SessionParameters])
	if !ok {
		return SessionParameters{}, false
	}
	return *params.Load(), true
}
