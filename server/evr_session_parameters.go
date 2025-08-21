package server

import (
	"context"
	"fmt"

	"github.com/gofrs/uuid/v5"
	"github.com/heroiclabs/nakama-common/api"
	"github.com/heroiclabs/nakama/v3/server/evr"
	"github.com/intinig/go-openskill/types"
	"go.uber.org/atomic"
)

// NextMatchInfo holds cached information about a user's next match
type NextMatchInfo struct {
	MatchID     MatchID
	Role        string
	Mode        evr.Symbol
	DiscordID   string
	HostUserID  string
}

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

	relayOutgoing       bool                // The user wants (some) outgoing messages relayed to them via discord
	enableAllRemoteLogs bool                // The user wants debug information
	serverTags          []string            // []string of the server tags
	serverGuilds        []string            // []string of the server guilds
	serverRegions       []string            // []string of the server regions
	urlParameters       map[string][]string // The URL parameters

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

	// Cached lobby session data to avoid repeated DB/storage calls
	friendsList                  []*api.Friend                                  // Cached friends list loaded at session init
	blockedPlayerIDs             []string                                       // Cached list of blocked player IDs from friends
	matchmakingRatingsByGroup    map[string]map[evr.Symbol]*atomic.Pointer[types.Rating] // Cached matchmaking ratings by group and mode
	rankPercentilesByGroup       map[string]map[evr.Symbol]*atomic.Float64      // Cached rank percentiles by group and mode
	nextMatchInfo                *NextMatchInfo                                 // Cached next match information
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

func (s *SessionParameters) GetBlockedPlayerIDs() []string {
	if s.blockedPlayerIDs == nil {
		return []string{}
	}
	return s.blockedPlayerIDs
}

func (s *SessionParameters) GetMatchmakingRating(groupID string, mode evr.Symbol) types.Rating {
	if s.matchmakingRatingsByGroup == nil {
		return NewDefaultRating()
	}
	if groupRatings, ok := s.matchmakingRatingsByGroup[groupID]; ok {
		if ratingPtr, ok := groupRatings[mode]; ok && ratingPtr != nil {
			if rating := ratingPtr.Load(); rating != nil {
				return *rating
			}
		}
	}
	return NewDefaultRating()
}

func (s *SessionParameters) SetMatchmakingRating(groupID string, mode evr.Symbol, rating types.Rating) {
	if s.matchmakingRatingsByGroup == nil {
		s.matchmakingRatingsByGroup = make(map[string]map[evr.Symbol]*atomic.Pointer[types.Rating])
	}
	if s.matchmakingRatingsByGroup[groupID] == nil {
		s.matchmakingRatingsByGroup[groupID] = make(map[evr.Symbol]*atomic.Pointer[types.Rating])
	}
	if s.matchmakingRatingsByGroup[groupID][mode] == nil {
		s.matchmakingRatingsByGroup[groupID][mode] = atomic.NewPointer(&rating)
	} else {
		s.matchmakingRatingsByGroup[groupID][mode].Store(&rating)
	}
}

func (s *SessionParameters) GetRankPercentile(groupID string, mode evr.Symbol) float64 {
	if s.rankPercentilesByGroup == nil {
		return 0.0
	}
	if groupPercentiles, ok := s.rankPercentilesByGroup[groupID]; ok {
		if percentilePtr, ok := groupPercentiles[mode]; ok && percentilePtr != nil {
			return percentilePtr.Load()
		}
	}
	return 0.0
}

func (s *SessionParameters) SetRankPercentile(groupID string, mode evr.Symbol, percentile float64) {
	if s.rankPercentilesByGroup == nil {
		s.rankPercentilesByGroup = make(map[string]map[evr.Symbol]*atomic.Float64)
	}
	if s.rankPercentilesByGroup[groupID] == nil {
		s.rankPercentilesByGroup[groupID] = make(map[evr.Symbol]*atomic.Float64)
	}
	if s.rankPercentilesByGroup[groupID][mode] == nil {
		s.rankPercentilesByGroup[groupID][mode] = atomic.NewFloat64(percentile)
	} else {
		s.rankPercentilesByGroup[groupID][mode].Store(percentile)
	}
}

func (s *SessionParameters) GetNextMatchInfo() *NextMatchInfo {
	return s.nextMatchInfo
}

func (s *SessionParameters) SetNextMatchInfo(info *NextMatchInfo) {
	s.nextMatchInfo = info
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
