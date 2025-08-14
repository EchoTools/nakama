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
	"github.com/intinig/go-openskill/rating"
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
	MatchmakingOrdinal           *atomic.Float64               `json:"matchmaking_ordinal"`
	EarlyQuitPenaltyLevel        int                           `json:"early_quit_penalty_level"`
	EnableSBMM                   bool                          `json:"disable_sbmm"`
	EnableRankPercentileRange    bool                          `json:"enable_rank_percentile_range"`
	EnableOrdinalRange           bool                          `json:"enable_ordinal_range"`
	EnableDivisions              bool                          `json:"enable_divisions"`
	MatchmakingOrdinalRange      float64                       `json:"ordinal_range"`
	RankPercentile               *atomic.Float64               `json:"rank_percentile"` // Updated when party is created
	RankPercentileMaxDelta       float64                       `json:"rank_percentile_max_delta"`
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

func (p *LobbySessionParameters) GetRankPercentile() float64 {
	if p.RankPercentile == nil {
		return 0.0
	}
	return p.RankPercentile.Load()
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

func (p *LobbySessionParameters) GetOrdinal() float64 {
	if p.MatchmakingOrdinal == nil {
		return 0.0
	}
	return p.MatchmakingOrdinal.Load()
}

func (p *LobbySessionParameters) SetOrdinal(ordinal float64) {
	if p.MatchmakingOrdinal == nil {
		p.MatchmakingOrdinal = atomic.NewFloat64(ordinal)
	} else {
		p.MatchmakingOrdinal.Store(ordinal)
	}
}

func (p *LobbySessionParameters) SetRankPercentile(percentile float64) {
	if p.RankPercentile == nil {
		p.RankPercentile = atomic.NewFloat64(percentile)
	} else {
		p.RankPercentile.Store(percentile)
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

		// Always clear the settings
		go func() {
			userSettings.NextMatchID = MatchID{}
			userSettings.NextMatchRole = ""
			userSettings.NextMatchDiscordID = ""
			if err := StorableWrite(ctx, p.nk, userID, userSettings); err != nil {
				logger.Warn("Failed to clear next match metadata", zap.Error(err))
			}
		}()
	}

	if _, isJoinRequest := request.(*evr.LobbyJoinSessionRequest); isJoinRequest {

		// Set mode based on the match to join.
		label, err := MatchLabelByID(ctx, nk, nextMatchID)
		if err != nil {
			logger.Warn("Failed to load next match", zap.Error(err))
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

	rankPercentileMaxDelta := 1.0
	rankPercentile := globalSettings.RankPercentile.Default
	matchmakingRating := NewDefaultRating()
	matchmakingOrdinal := 0.0
	mmMode := mode

	if mode == evr.ModeSocialPublic || mode == evr.ModeArenaPublicAI {
		mmMode = evr.ModeArenaPublic
	}
	if (mode == evr.ModeArenaPublic || mode == evr.ModeCombatPublic) && globalSettings.EnableSBMM && groupID != uuid.Nil {

		if globalSettings.RankPercentile.MaxDelta > 0 {
			rankPercentileMaxDelta = globalSettings.RankPercentile.MaxDelta
		}

		if userSettings.StaticBaseRankPercentile > 0 {
			rankPercentile = userSettings.StaticBaseRankPercentile
		} else {
			rankPercentile, err = CalculateSmoothedPlayerRankPercentile(ctx, logger, p.db, p.nk, userID, groupIDStr, mmMode)
			if err != nil {
				return nil, fmt.Errorf("failed to calculate smoothed player rank percentile: %w", err)
			}

			if err := MatchmakingRankPercentileStore(ctx, p.nk, userID, session.Username(), groupIDStr, mmMode, rankPercentile); err != nil {
				logger.Warn("Failed to store user rank percentile", zap.Error(err))
			}
		}

		matchmakingRating, err = MatchmakingRatingLoad(ctx, p.nk, userID, groupIDStr, mmMode)
		if err != nil {
			logger.Warn("Failed to load matchmaking rating", zap.String("group_id", groupIDStr), zap.String("mode", mmMode.String()), zap.Error(err))
			matchmakingRating = NewDefaultRating()
		}

		matchmakingOrdinal = rating.Ordinal(matchmakingRating)
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
	if !serviceSettings.Matchmaking.EnableEarlyQuitPenalty {
		if config := sessionParams.earlyQuitConfig.Load(); config != nil {
			earlyQuitPenaltyLevel = config.GetPenaltyLevel()
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
		EnableRankPercentileRange:    globalSettings.EnableRankPercentileRange,
		EnableOrdinalRange:           globalSettings.EnableOrdinalRange,
		MatchmakingRating:            atomic.NewPointer(&matchmakingRating),
		MatchmakingOrdinal:           atomic.NewFloat64(matchmakingOrdinal),
		MatchmakingOrdinalRange:      globalSettings.OrdinalRange,
		Verbose:                      sessionParams.profile.DiscordDebugMessages,
		EarlyQuitPenaltyLevel:        earlyQuitPenaltyLevel,
		RankPercentile:               atomic.NewFloat64(rankPercentile),
		RankPercentileMaxDelta:       rankPercentileMaxDelta,
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
	qparts := []string{
		"+label.open:T",
		fmt.Sprintf("+label.mode:%s", p.Mode.String()),
		fmt.Sprintf("+label.group_id:%s", Query.QuoteStringValue(p.GroupID.String())),
		//fmt.Sprintf("label.version_lock:%s", p.VersionLock.String()),
		fmt.Sprintf("-label.start_time:>=%d", time.Now().UTC().Add(-30*time.Second).Unix()),
		p.BackfillQueryAddon,
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
		qparts = append(qparts,
			// Exclusion
			fmt.Sprintf("-label.rating_ordinal:<%f", p.GetOrdinal()-p.MatchmakingOrdinalRange),
			fmt.Sprintf("-label.rating_ordinal:>%f", p.GetOrdinal()+p.MatchmakingOrdinalRange),
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
		validRTTs := make([]string, 0)
		for ip, rtt := range p.latencyHistory.Load().LatestRTTs() {
			if rtt <= p.MaxServerRTT {
				validRTTs = append(validRTTs, ip)
			}
		}
		if len(validRTTs) > 0 {
			qparts = append(qparts, fmt.Sprintf("+label.broadcaster.endpoint:%s", Query.CreateMatchPattern(validRTTs)))
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
	p.SetRankPercentile(numericProperties["rank_percentile"])
	p.MatchmakingTimestamp, _ = time.Parse(time.RFC3339, stringProperties["submission_time"])
	p.RankPercentileMaxDelta = numericProperties["rank_percentile_max"]
	p.MaxServerRTT = 180

	serverRTTs := make(map[string]int)
	for k, v := range numericProperties {
		if strings.HasPrefix(k, RTTPropertyPrefix) {
			serverRTTs[strings.TrimPrefix(k, RTTPropertyPrefix)] = int(v)
		}
	}

}

func (p *LobbySessionParameters) MatchmakingParameters(ticketParams *MatchmakingTicketParameters) (string, map[string]string, map[string]float64) {

	submissionTime := time.Now().UTC().Format(time.RFC3339)
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
		"timestamp":                 float64(time.Now().UTC().Unix()),
		"rank_percentile_max_delta": p.RankPercentileMaxDelta,
		"max_rtt":                   float64(p.MaxServerRTT),
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
		numericProperties["rating_ordinal"] = p.GetOrdinal()

		if p.EnableOrdinalRange && ticketParams.IncludeSBMMRanges {
			if ordinal := p.GetOrdinal(); ordinal != 0.0 {
				ordinalLower := ordinal - p.MatchmakingOrdinalRange
				ordinalUpper := ordinal + p.MatchmakingOrdinalRange
				numericProperties["rating_ordinal_min"] = ordinalLower
				numericProperties["rating_ordinal_max"] = ordinalUpper

				qparts = append(qparts,
					// Exclusion
					fmt.Sprintf("-properties.rating_ordinal:<%f", ordinalLower),
					fmt.Sprintf("-properties.rating_ordinal:>%f", ordinalUpper),

					// Reverse
					fmt.Sprintf("-properties.rating_ordinal_min:>%f", ordinal),
					fmt.Sprintf("-properties.rating_ordinal_max:<%f", ordinal),
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

		if p.EnableRankPercentileRange {

			if rankPercentile := p.GetRankPercentile(); rankPercentile > 0.0 {
				numericProperties["rank_percentile"] = rankPercentile

				if p.RankPercentileMaxDelta > 0 {
					rankLower := min(rankPercentile-p.RankPercentileMaxDelta, 1.0-2.0*p.RankPercentileMaxDelta)
					rankUpper := max(rankPercentile+p.RankPercentileMaxDelta, 2.0*p.RankPercentileMaxDelta)
					rankLower = max(rankLower, 0.0)
					rankUpper = min(rankUpper, 1.0)
					numericProperties["rank_percentile_min"] = rankLower
					numericProperties["rank_percentile_max"] = rankUpper

					qparts = append(qparts,
						// Exclusion
						fmt.Sprintf("-properties.rank_percentile:<%f", rankLower),
						fmt.Sprintf("-properties.rank_percentile:>%f", rankUpper),

						// Reverse
						fmt.Sprintf("-properties.rank_percentile_min:>%f", rankPercentile),
						fmt.Sprintf("-properties.rank_percentile_max:<%f", rankPercentile),
					)
				}

			}
		}

	}

	//maxDelta := 60 // milliseconds
	for k, v := range p.latencyHistory.Load().AverageRTTs(true) {
		numericProperties[RTTPropertyPrefix+k] = float64(v)
		//qparts = append(qparts, fmt.Sprintf("properties.%s:<=%d", k, v+maxDelta))
	}

	if ticketParams.IncludeRequireCommonServer {
		// Create a string list of validRTTs
		acceptableServers := make([]string, 0)
		for ip, rtt := range p.latencyHistory.Load().LatestRTTs() {
			if rtt <= p.MaxServerRTT {
				acceptableServers = append(acceptableServers, ip)
			}
		}

		stringProperties["servers"] = strings.Join(acceptableServers, " ")

		// Add the acceptable servers to the query
		if len(acceptableServers) > 0 {
			qparts = append(qparts, fmt.Sprintf("+properties.servers:%s", Query.CreateMatchPattern(acceptableServers)))
		}

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
