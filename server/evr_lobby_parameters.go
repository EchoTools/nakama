package server

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"go.uber.org/atomic"

	"slices"

	"github.com/gofrs/uuid/v5"
	"github.com/heroiclabs/nakama-common/api"
	"github.com/heroiclabs/nakama-common/runtime"
	"github.com/heroiclabs/nakama/v3/server/evr"
	"github.com/intinig/go-openskill/types"
	"go.uber.org/zap"
)

type (
	ctxLobbyParametersKey struct{}
)

type LobbySessionParameters struct {
	Node                         string                        `json:"node"`
	UserID                       uuid.UUID                     `json:"user_id"`
	SessionID                    uuid.UUID                     `json:"session_id"`
	DiscordID                    string                        `json:"discord_id"`
	VersionLock                  evr.Symbol                    `json:"version_lock"`
	AppID                        evr.Symbol                    `json:"app_id"`
	GroupID                      uuid.UUID                     `json:"group_id"`
	RegionCode                   string                        `json:"region_code"`
	Mode                         evr.Symbol                    `json:"mode"`
	Level                        evr.Symbol                    `json:"level"`
	SupportedFeatures            []string                      `json:"supported_features"`
	RequiredFeatures             []string                      `json:"required_features"`
	CurrentMatchID               MatchID                       `json:"current_match_id"`
	NextMatchID                  MatchID                       `json:"next_match_id"`
	Role                         int                           `json:"role"`
	PartySize                    *atomic.Int64                 `json:"party_size"`
	PartyID                      uuid.UUID                     `json:"party_id"`
	PartyGroupName               string                        `json:"party_group_name"`
	DisableArenaBackfill         bool                          `json:"disable_arena_backfill"`
	BackfillQueryAddon           string                        `json:"backfill_query_addon"`
	MatchmakingQueryAddon        string                        `json:"matchmaking_query_addon"`
	CreateQueryAddon             string                        `json:"create_query_addon"`
	Verbose                      bool                          `json:"verbose"`
	BlockedIDs                   []string                      `json:"blocked_ids"`
	MatchmakingRating            *atomic.Pointer[types.Rating] `json:"matchmaking_rating"`
	EarlyQuitPenaltyLevel        int                           `json:"early_quit_penalty_level"`
	EarlyQuitMatchmakingTier     int32                         `json:"early_quit_matchmaking_tier"`
	EnableSBMM                   bool                          `json:"disable_sbmm"`
	EnableOrdinalRange           bool                          `json:"enable_ordinal_range"`
	EnableDivisions              bool                          `json:"enable_divisions"`
	MatchmakingRatingRange       float64                       `json:"rating_range"`
	MatchmakingDivisions         []string                      `json:"divisions"`
	MatchmakingExcludedDivisions []string                      `json:"excluded_divisions"`
	MaxServerRTT                 int                           `json:"max_server_rtt"`
	MatchmakingTimestamp         time.Time                     `json:"matchmaking_timestamp"`
	MatchmakingTimeout           time.Duration                 `json:"matchmaking_timeout"`
	FailsafeTimeout              time.Duration                 `json:"failsafe_timeout"` // The failsafe timeout
	FallbackTimeout              time.Duration                 `json:"fallback_timeout"` // The fallback timeout
	DisplayName                  string                        `json:"display_name"`
	latencyHistory               *atomic.Pointer[LatencyHistory]
}

func (p *LobbySessionParameters) GetPartySize() int {
	return int(p.PartySize.Load())
}

func (p *LobbySessionParameters) SetPartySize(size int) {
	p.PartySize.Store(int64(size))
}

func (p *LobbySessionParameters) GetRating() types.Rating {
	if p.MatchmakingRating == nil || p.MatchmakingRating.Load() == nil {
		return NewDefaultRating()
	}
	return *p.MatchmakingRating.Load()
}

func (p *LobbySessionParameters) SetRating(rating types.Rating) {
	if p.MatchmakingRating == nil {
		p.MatchmakingRating = atomic.NewPointer(&rating)
	} else {
		p.MatchmakingRating.Store(&rating)
	}
}

func (s LobbySessionParameters) MetricsTags() map[string]string {
	return map[string]string{
		"mode":             s.Mode.String(),
		"group_id":         s.GroupID.String(),
		"early_quit_level": strconv.Itoa(s.EarlyQuitPenaltyLevel),
		"role":             strconv.Itoa(s.Role),
	}
}

func NewLobbyParametersFromRequest(ctx context.Context, logger *zap.Logger, nk runtime.NakamaModule, session *sessionWS, request evr.LobbySessionRequest) (*LobbySessionParameters, error) {

	var (
		p               = session.evrPipeline
		userID          = session.userID.String()
		mode            = request.GetMode()
		level           = request.GetLevel()
		versionLock     = request.GetVersionLock()
		appID           = request.GetAppID()
		serviceSettings = ServiceSettings()
		globalSettings  = serviceSettings.Matchmaking
	)
	sessionParams, ok := LoadParams(ctx)
	if !ok {
		return nil, fmt.Errorf("failed to load session parameters")
	}

	if sessionParams.profile == nil {
		return nil, fmt.Errorf("failed to load session parameters")
	}

	// Load the user's matchmaking config
	userSettings, err := LoadMatchmakingSettings(ctx, p.nk, userID)
	if err != nil {
		return nil, fmt.Errorf("failed to load user matchmaking settings: %w", err)
	}

	// Check for match lock (moderation feature) - takes priority over regular NextMatchDiscordID
	// Match lock is persistent and forces the player to follow the designated leader
	if userSettings.IsMatchLocked() {
		// Override NextMatchDiscordID with the locked leader
		logger.Info("Match lock active - forcing player to follow leader",
			zap.String("user_id", userID),
			zap.String("leader_discord_id", userSettings.MatchLockLeaderDiscordID),
			zap.String("operator_user_id", userSettings.MatchLockOperatorUserID),
			zap.String("reason", userSettings.MatchLockReason))
		userSettings.NextMatchDiscordID = userSettings.MatchLockLeaderDiscordID
	}

	if userSettings.NextMatchDiscordID != "" {
		// Get the host's user ID
		hostUserIDStr := p.discordCache.DiscordIDToUserID(userSettings.NextMatchDiscordID)

		// If the host userID exists, and is in a match, set the next match ID to the host's match ID
		if hostUserID := uuid.FromStringOrNil(hostUserIDStr); !hostUserID.IsNil() {

			// Get the MatchIDs for the user from it's presence
			presences, _ := p.nk.StreamUserList(StreamModeService, hostUserID.String(), "", StreamLabelMatchService, false, true)
			for _, presence := range presences {
				matchID := MatchIDFromStringOrNil(presence.GetStatus())
				if !matchID.IsNil() {
					userSettings.NextMatchID = matchID
				}
			}
		}
	}

	entrantRole := request.GetEntrantRole(0)

	nextMatchID := MatchID{}

	if !userSettings.NextMatchID.IsNil() {

		if label, err := MatchLabelByID(ctx, nk, userSettings.NextMatchID); err != nil {
			logger.Warn("Next match not found", zap.String("mid", userSettings.NextMatchID.String()))
		} else {
			mode = label.Mode

			// Match exists, set the next match ID and role
			nextMatchID = userSettings.NextMatchID

			if userSettings.NextMatchRole != "" {
				switch userSettings.NextMatchRole {
				case "orange":
					entrantRole = evr.TeamOrange
				case "blue":
					entrantRole = evr.TeamBlue
				case "spectator":
					entrantRole = evr.TeamSpectator
				case "moderator":
					entrantRole = evr.TeamModerator
				case "any":
					entrantRole = evr.TeamUnassigned
				}
			}

			userSettings.NextMatchRole = ""

		}

		// Clear the one-time next match settings, but NOT the match lock settings
		// Match lock is persistent and only removed by explicit unlock
		go func() {
			userSettings.NextMatchID = MatchID{}
			userSettings.NextMatchRole = ""
			// Only clear NextMatchDiscordID if not locked (lock uses its own field)
			if !userSettings.IsMatchLocked() {
				userSettings.NextMatchDiscordID = ""
			}
			// Use a background context with timeout so cleanup is not canceled with the parent request.
			ctxCleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if err := StorableWrite(ctxCleanup, p.nk, userID, userSettings); err != nil {
				logger.Warn("Failed to clear next match metadata", zap.Error(err))
			}
		}()
	}

	if _, isJoinRequest := request.(*evr.LobbyJoinSessionRequest); isJoinRequest {

		// Set mode based on the match to join.
		label, err := MatchLabelByID(ctx, nk, nextMatchID)
		if err != nil {
			logger.Debug("Failed to load next match", zap.Any("mid", nextMatchID), zap.Error(err))
		} else {
			mode = label.Mode
		}
	}

	matchmakingQueryAddons := []string{
		globalSettings.QueryAddons.Matchmaking,
		userSettings.LobbyBuilderQueryAddon,
	}

	backfillQueryAddons := []string{
		globalSettings.QueryAddons.Backfill,
		userSettings.BackfillQueryAddon,
	}

	createQueryAddons := []string{
		globalSettings.QueryAddons.Create,
		userSettings.CreateQueryAddon,
	}

	// Load friends to get blocked (ghosted) players
	cursor := ""
	friends := make([]*api.Friend, 0)

	for {

		var users []*api.Friend
		users, cursor, err = nk.FriendsList(ctx, session.UserID().String(), 100, nil, cursor)
		if err != nil {
			return nil, fmt.Errorf("failed to list friends: %w", err)
		}

		friends = append(friends, users...)

		if cursor == "" {
			break
		}
	}

	// Add blocked players who are online to the Matchmaking Query Addon
	blockedIDs := make([]string, 0)
	for _, f := range friends {
		if api.Friend_State(f.GetState().Value) == api.Friend_BLOCKED {
			if f.GetUser().GetOnline() {
				blockedIDs = append(blockedIDs, f.GetUser().GetId())
			}
		}
	}

	var lobbyGroupName string
	var partyID uuid.UUID

	if userSettings.LobbyGroupName != "" {
		lobbyGroupName = userSettings.LobbyGroupName
		partyID = uuid.NewV5(EntrantIDSalt, lobbyGroupName)
	}

	node := session.pipeline.node

	requiredFeatures := sessionParams.requiredFeatures
	supportedFeatures := sessionParams.supportedFeatures

	if request.GetFeatures() != nil {
		supportedFeatures = append(supportedFeatures, request.GetFeatures()...)
	}

	groupID := request.GetGroupID()
	if request.GetGroupID() == uuid.Nil {
		groupID = sessionParams.profile.GetActiveGroupID()
	}
	groupIDStr := groupID.String()
	region := "default"

	if r := request.GetRegion(); r != evr.UnspecifiedRegion {
		region = r.String()
	}

	currentMatchID := MatchID{}
	if request.GetCurrentLobbyID() != uuid.Nil {
		currentMatchID = MatchID{UUID: request.GetCurrentLobbyID(), Node: node}
	}

	matchmakingRating := NewDefaultRating()
	mmMode := mode

	if mode == evr.ModeSocialPublic || mode == evr.ModeArenaPublicAI {
		mmMode = evr.ModeArenaPublic
	}
	if (mode == evr.ModeArenaPublic || mode == evr.ModeCombatPublic) && globalSettings.EnableSBMM && groupID != uuid.Nil {

		if userSettings.StaticRatingMu != nil && userSettings.StaticRatingSigma != nil {
			matchmakingRating = types.Rating{Mu: *userSettings.StaticRatingMu, Sigma: *userSettings.StaticRatingSigma}
		} else {
			matchmakingRating, err = MatchmakingRatingLoad(ctx, p.nk, userID, groupIDStr, mmMode)
			if err != nil {
				logger.Warn("Failed to load matchmaking rating", zap.String("group_id", groupIDStr), zap.String("mode", mmMode.String()), zap.Error(err))
				matchmakingRating = NewDefaultRating()
			}
		}
	}

	maxServerRTT := globalSettings.MaxServerRTT

	if globalSettings.MaxServerRTT <= 60 {
		maxServerRTT = 180
	}

	matchmakingDivisions := make([]string, 0)
	matchmakingExcludedDivisions := make([]string, 0)

	if globalSettings.EnableDivisions && userSettings.Divisions != nil {
		matchmakingDivisions = userSettings.Divisions
	}

	if sessionParams.IsIGPOpen() {
		matchmakingDivisions = []string{"green"}
		matchmakingExcludedDivisions = []string{}
	}

	latencyHistory := sessionParams.latencyHistory.Load()
	if latencyHistory == nil {
		latencyHistory = NewLatencyHistory()
	}
	sessionParams.latencyHistory.Store(latencyHistory)
	
	// Set the maxRTT to at least the average of the player's latency history
	if averages := latencyHistory.AverageRTTs(false); len(averages) > 0 {
		averageRTT := 0
		count := 0
		for _, rtt := range averages {
			averageRTT += rtt
			count++
		}
		averageRTT /= count
		maxServerRTT = max(maxServerRTT, averageRTT)
	}

	earlyQuitPenaltyLevel := 0
	earlyQuitMatchmakingTier := int32(MatchmakingTier1)
	if serviceSettings.Matchmaking.EnableEarlyQuitPenalty {
		if config := sessionParams.earlyQuitConfig.Load(); config != nil {
			earlyQuitPenaltyLevel = config.GetPenaltyLevel()
			earlyQuitMatchmakingTier = config.GetTier()
		}
	}

	maximumFailsafeSecs := globalSettings.MatchmakingTimeoutSecs - p.config.GetMatchmaker().IntervalSec*2
	failsafeTimeoutSecs := min(maximumFailsafeSecs, globalSettings.FailsafeTimeoutSecs)

	params, _ := LoadParams(ctx)

	return &LobbySessionParameters{
		Node:                         node,
		UserID:                       session.userID,
		SessionID:                    session.id,
		DiscordID:                    sessionParams.DiscordID(),
		CurrentMatchID:               currentMatchID,
		VersionLock:                  versionLock,
		AppID:                        appID,
		GroupID:                      groupID,
		RegionCode:                   region,
		Mode:                         mode,
		Level:                        level,
		SupportedFeatures:            supportedFeatures,
		RequiredFeatures:             requiredFeatures,
		Role:                         entrantRole,
		DisableArenaBackfill:         globalSettings.DisableArenaBackfill || userSettings.DisableArenaBackfill,
		BackfillQueryAddon:           strings.Join(backfillQueryAddons, " "),
		MatchmakingQueryAddon:        strings.Join(matchmakingQueryAddons, " "),
		CreateQueryAddon:             strings.Join(createQueryAddons, " "),
		PartyGroupName:               lobbyGroupName,
		PartyID:                      partyID,
		PartySize:                    atomic.NewInt64(1),
		NextMatchID:                  nextMatchID,
		latencyHistory:               params.latencyHistory,
		BlockedIDs:                   blockedIDs,
		EnableSBMM:                   globalSettings.EnableSBMM,
		EnableDivisions:              globalSettings.EnableDivisions,
		EnableOrdinalRange:           globalSettings.EnableOrdinalRange,
		MatchmakingRating:            atomic.NewPointer(&matchmakingRating),
		MatchmakingRatingRange:       globalSettings.RatingRange,
		Verbose:                      sessionParams.profile.DiscordDebugMessages,
		EarlyQuitPenaltyLevel:        earlyQuitPenaltyLevel,
		EarlyQuitMatchmakingTier:     earlyQuitMatchmakingTier,
		MatchmakingDivisions:         matchmakingDivisions,
		MatchmakingExcludedDivisions: matchmakingExcludedDivisions,
		MaxServerRTT:                 maxServerRTT,
		MatchmakingTimestamp:         time.Now().UTC(),
		MatchmakingTimeout:           time.Duration(globalSettings.MatchmakingTimeoutSecs) * time.Second,
		FailsafeTimeout:              time.Duration(failsafeTimeoutSecs) * time.Second,
		FallbackTimeout:              time.Duration(globalSettings.FallbackTimeoutSecs) * time.Second,
		DisplayName:                  sessionParams.profile.GetGroupIGN(groupIDStr),
	}, nil
}

func (p LobbySessionParameters) String() string {
	data, err := json.Marshal(p)
	if err != nil {
		return ""
	}
	return string(data)
}

func (p *LobbySessionParameters) BackfillSearchQuery(includeMMR bool, includeMaxRTT bool) string {
	// Prevent joining matches that have just started (less than 30 seconds old).
	const MatchStartTimeMinimumAgeSecs = 30

	minStartTime := p.MatchmakingTimestamp.UTC().Add(-time.Duration(MatchStartTimeMinimumAgeSecs) * time.Second).Format(time.RFC3339Nano)

	qparts := []string{
		"+label.open:T",
		fmt.Sprintf("+label.mode:%s", p.Mode.String()),
		fmt.Sprintf("+label.group_id:%s", Query.QuoteStringValue(p.GroupID.String())),
		//fmt.Sprintf("label.version_lock:%s", p.VersionLock.String()),
		p.BackfillQueryAddon,
	}

	// Do not wait to join social lobbies
	if p.Mode != evr.ModeSocialPublic {
		qparts = append(qparts, fmt.Sprintf(`+label.start_time:>="%s"`, minStartTime))
	}
	// For arena public matches, exclude matches older than the configured max age
	if p.Mode == evr.ModeArenaPublic {
		maxAgeSecs := ServiceSettings().Matchmaking.ArenaBackfillMaxAgeSecs
		if maxAgeSecs > 0 {
			// Exclude matches that started more than maxAgeSecs ago
			startTime := p.MatchmakingTimestamp.UTC().Add(-time.Duration(maxAgeSecs) * time.Second).Format(time.RFC3339Nano)
			qparts = append(qparts, fmt.Sprintf(`-label.start_time:<"%s"`, startTime))
		}
	}

	if !p.CurrentMatchID.IsNil() {
		// Do not backfill into the same match
		qparts = append(qparts, fmt.Sprintf("-label.id:%s", Query.QuoteStringValue(p.CurrentMatchID.String())))
	}

	if len(p.BlockedIDs) > 0 && slices.Contains([]evr.Symbol{evr.ModeSocialPublic, evr.ModeCombatPublic}, p.Mode) {
		// Add each blocked user that is online to the backfill query addon
		// Avoid backfilling matches with players that this player blocks.
		qparts = append(qparts, fmt.Sprintf("-label.players.user_id:%s", Query.CreateMatchPattern(p.BlockedIDs)))
	}

	if includeMMR {
		key := "rating_mu"
		val := p.GetRating().Mu
		rng := p.MatchmakingRatingRange

		qparts = append(qparts,
			// Exclusion
			fmt.Sprintf("-label.%s:<%f", key, val-rng),
			fmt.Sprintf("-label.%s:>%f", key, val+rng),
		)
	}

	if len(p.RequiredFeatures) > 0 {
		for _, f := range p.RequiredFeatures {
			qparts = append(qparts, fmt.Sprintf("+label.features:/.*%s.*/", Query.QuoteStringValue(f)))
		}
	}

	// Do not backfill into the same match
	if !p.CurrentMatchID.IsNil() {
		qparts = append(qparts, fmt.Sprintf("-label.id:%s", Query.QuoteStringValue(p.CurrentMatchID.String())))
	}

	// Ensure the match is not full
	playerLimit := 0
	switch p.Mode {
	case evr.ModeArenaPublic:
		playerLimit = DefaultPublicArenaTeamSize * 2
	case evr.ModeCombatPublic:
		playerLimit = DefaultPublicCombatTeamSize * 2
	case evr.ModeSocialPublic:
		playerLimit = DefaultLobbySize(evr.ModeSocialPublic)
	}

	if playerLimit > 0 {
		qparts = append(qparts, fmt.Sprintf("+label.player_count:<=%d", playerLimit-p.GetPartySize()))
	}

	if includeMaxRTT {
		// Exclude servers that cannot be reached (either unreachable or RTT too high)
		unreachableEndpointIDs := make([]string, 0)
		for ip, rtt := range p.latencyHistory.Load().LatestRTTs() {
			if rtt > p.MaxServerRTT {
				unreachableEndpointIDs = append(unreachableEndpointIDs, EncodeEndpointID(ip))
			}
		}
		if len(unreachableEndpointIDs) > 0 {
			qparts = append(qparts, fmt.Sprintf("-label.broadcaster.endpoint_id:%s", Query.CreateMatchPattern(unreachableEndpointIDs)))
		}
	}
	return strings.Join(qparts, " ")

}
func (p *LobbySessionParameters) FromMatchmakerEntry(entry *MatchmakerEntry) {

	// Break out the strings and numerics
	stringProperties := make(map[string]string)
	numericProperties := make(map[string]float64)

	for k, v := range entry.Properties {
		switch v := v.(type) {
		case string:
			stringProperties[k] = v
		case float64:
			numericProperties[k] = v
		}
	}

	mu := numericProperties["rating_mu"]
	sigma := numericProperties["rating_sigma"]
	rating := NewRating(0, mu, sigma)

	p.Mode = evr.ToSymbol(stringProperties["game_mode"])
	p.GroupID = uuid.FromStringOrNil(stringProperties["group_id"])
	p.VersionLock = evr.ToSymbol(stringProperties["version_lock"])
	p.BlockedIDs = strings.Split(stringProperties["blocked_ids"], " ")
	p.DisplayName = stringProperties["display_name"]
	p.SetRating(rating)
	p.MatchmakingTimestamp, _ = time.Parse(time.RFC3339, stringProperties["submission_time"])
	p.MaxServerRTT = 180

	serverRTTs := make(map[string]int)
	for k, v := range numericProperties {
		if strings.HasPrefix(k, RTTPropertyPrefix) {
			serverRTTs[strings.TrimPrefix(k, RTTPropertyPrefix)] = int(v)
		}
	}

}

func (p *LobbySessionParameters) MatchmakingParameters(ticketParams *MatchmakingTicketParameters) (string, map[string]string, map[string]float64) {

	submissionTime := p.MatchmakingTimestamp.UTC().Format(time.RFC3339)
	stringProperties := map[string]string{
		"game_mode":          p.Mode.String(),
		"group_id":           p.GroupID.String(),
		"version_lock":       p.VersionLock.String(),
		"display_name":       p.DisplayName,
		"submission_time":    submissionTime,
		"divisions":          strings.Join(p.MatchmakingDivisions, ","),
		"excluded_divisions": strings.Join(p.MatchmakingExcludedDivisions, ","),
	}

	numericProperties := map[string]float64{
		"timestamp": float64(p.MatchmakingTimestamp.UTC().Unix()),
		"max_rtt":   float64(p.MaxServerRTT),
	}

	qparts := []string{
		"+properties.game_mode:" + p.Mode.String(),
		fmt.Sprintf("+properties.group_id:%s", Query.QuoteStringValue(p.GroupID.String())),
		fmt.Sprintf(`-properties.blocked_ids:/.*%s.*/`, Query.QuoteStringValue(p.UserID.String())),
		//"+properties.version_lock:" + p.VersionLock.String(),
		p.MatchmakingQueryAddon,
	}

	if len(p.MatchmakingDivisions) > 0 {

	}

	if !slices.Contains([]evr.Symbol{evr.ModeCombatPublic}, p.Mode) {
		stringProperties["blocked_ids"] = strings.Join(p.BlockedIDs, " ")
	}

	// If the user has an early quit penalty, only match them with players who have submitted after now
	/*
		if p.IsEarlyQuitter && ticketParams.IncludeEarlyQuitPenalty {
			qparts = append(qparts, fmt.Sprintf(`-properties.submission_time:<="%s"`, submissionTime))
		}
	*/

	// If the user has a matchmaking Division, use it instead of SBMM

	if p.EnableSBMM {

		rating := p.GetRating()
		numericProperties["rating_mu"] = rating.Mu
		numericProperties["rating_sigma"] = rating.Sigma

		if p.EnableOrdinalRange && ticketParams.IncludeSBMMRanges {
			key := "rating_mu"
			val := rating.Mu
			rng := p.MatchmakingRatingRange

			if val != 0.0 {
				lower := val - rng
				upper := val + rng
				numericProperties[key+"_min"] = lower
				numericProperties[key+"_max"] = upper

				qparts = append(qparts,
					// Exclusion
					fmt.Sprintf("-properties.%s:<%f", key, lower),
					fmt.Sprintf("-properties.%s:>%f", key, upper),

					// Reverse
					fmt.Sprintf("-properties.%s_min:>%f", key, val),
					fmt.Sprintf("-properties.%s_max:<%f", key, val),
				)
			}
		}

		/*
			if p.EnableDivisions && len(p.MatchmakingDivisions) != 0 {
				qparts = append(qparts,
					fmt.Sprintf("+properties.divisions:%s", Query.MatchItem(p.MatchmakingDivisions)),
					fmt.Sprintf(`-properties.excluded_divisions:%s`, Query.MatchItem(p.MatchmakingDivisions)))
			}
		*/

	}

	//maxDelta := 60 // milliseconds

	//maxDelta := 60 // milliseconds
	rttDeltas := ServiceSettings().Matchmaking.ServerSelection.RTTDelta
	for ip, v := range p.latencyHistory.Load().AverageRTTs(true) {
		rtt := v
		if delta, ok := rttDeltas[ip]; ok {
			rtt += delta
		}
		numericProperties[RTTPropertyPrefix+EncodeEndpointID(ip)] = float64(rtt)
		//qparts = append(qparts, fmt.Sprintf("properties.%s:<=%d", k, v+maxDelta))
	}

	// Remove blanks from qparts
	for i := 0; i < len(qparts); i++ {
		if strings.TrimSpace(qparts[i]) == "" {
			qparts = slices.Delete(qparts, i, i+1)
			i--
		}
	}

	query := strings.Join(qparts, " ")

	stringProperties["query"] = query

	return query, stringProperties, numericProperties
}

func (p LobbySessionParameters) MatchmakingStream() PresenceStream {
	return PresenceStream{Mode: StreamModeMatchmaking, Subject: p.GroupID}
}

func (p LobbySessionParameters) GuildGroupStream() PresenceStream {
	return PresenceStream{Mode: StreamModeGuildGroup, Subject: p.GroupID, Label: p.Mode.String()}
}

func (p LobbySessionParameters) PresenceMeta() PresenceMeta {
	return PresenceMeta{
		Status: p.String(),
	}
}
