package service

import (
	"context"
	"fmt"

	evr "github.com/echotools/nakama/v3/protocol"
	"go.uber.org/atomic"
)

// Keys used for storing/retrieving user information in the context of a request after authentication.
type ctxSessionParametersKey struct{} // The Session Parameters

type SessionParameters struct {
	node string   // The node name
	xpID evr.XPID // The Cross-Platform ID

	externalServerAddr string // The external server address (IP:port)
	geoHashPrecision   int    // The geohash precision
	isVPN              bool   // The user is using a VPN
	ipInfo             IPInfo // The IPQS data

	supportedFeatures []string          // features from the urlparam
	requiredFeatures  []string          // required_features from the urlparam
	loginPayload      *evr.LoginProfile // The login payload

	relayOutgoing       bool     // The user wants (some) outgoing messages relayed to them via discord
	enableAllRemoteLogs bool     // The user wants debug information
	serverTags          []string // []string of the server tags
	serverGuilds        []string // []string of the server guilds
	serverRegions       []string // []string of the server regions

	profile                      *EVRProfile                      // The account
	matchmakingSettings          *MatchmakingSettings             // The matchmaking settings
	guildGroups                  map[string]*GuildGroup           // map[string]*GuildGroup
	earlyQuitConfig              *atomic.Pointer[EarlyQuitConfig] // The early quit config
	isGoldNameTag                *atomic.Bool                     // If this user should have a gold name tag
	lastMatchmakingError         *atomic.Error                    // The last matchmaking error
	latencyHistory               *atomic.Pointer[LatencyHistory]  // The latency history
	isIGPOpen                    *atomic.Bool                     // The user has IGPU open
	gameModeSuspensionsByGroupID ActiveGuildEnforcements          // The active suspension records
	ignoreDisabledAlternates     bool                             // Ignore disabled
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
	return s.profile.GetGroupIGN(groupID)
}

func (s *SessionParameters) IsIGPOpen() bool {
	if s.isIGPOpen == nil {
		return false
	}
	return s.isIGPOpen.Load()
}

func (s *SessionParameters) MetricsTags() map[string]string {
	return map[string]string{
		"is_vr":         fmt.Sprintf("%t", s.IsVR()),
		"is_pcvr":       fmt.Sprintf("%t", s.IsPCVR()),
		"build_version": fmt.Sprintf("%d", s.BuildNumber()),
		"device_type":   s.DeviceType(),
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
