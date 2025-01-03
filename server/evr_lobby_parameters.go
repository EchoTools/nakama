package server

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"go.uber.org/atomic"

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
	Node                   string                        `json:"node"`
	UserID                 uuid.UUID                     `json:"user_id"`
	SessionID              uuid.UUID                     `json:"session_id"`
	DiscordID              string                        `json:"discord_id"`
	VersionLock            evr.Symbol                    `json:"version_lock"`
	AppID                  evr.Symbol                    `json:"app_id"`
	GroupID                uuid.UUID                     `json:"group_id"`
	Region                 evr.Symbol                    `json:"region"`
	Mode                   evr.Symbol                    `json:"mode"`
	Level                  evr.Symbol                    `json:"level"`
	SupportedFeatures      []string                      `json:"supported_features"`
	RequiredFeatures       []string                      `json:"required_features"`
	CurrentMatchID         MatchID                       `json:"current_match_id"`
	NextMatchID            MatchID                       `json:"next_match_id"`
	Role                   int                           `json:"role"`
	PartySize              *atomic.Int64                 `json:"party_size"`
	PartyID                uuid.UUID                     `json:"party_id"`
	PartyGroupName         string                        `json:"party_group_name"`
	DisableArenaBackfill   bool                          `json:"disable_arena_backfill"`
	BackfillQueryAddon     string                        `json:"backfill_query_addon"`
	MatchmakingQueryAddon  string                        `json:"matchmaking_query_addon"`
	CreateQueryAddon       string                        `json:"create_query_addon"`
	Verbose                bool                          `json:"verbose"`
	BlockedIDs             []string                      `json:"blocked_ids"`
	Rating                 *atomic.Pointer[types.Rating] `json:"rating"`
	IsEarlyQuitter         bool                          `json:"quit_last_game_early"`
	RankPercentile         *atomic.Float64               `json:"rank_percentile"` // Updated when party is created
	RankPercentileMaxDelta float64                       `json:"rank_percentile_max_delta"`
	MaxServerRTT           int                           `json:"max_server_rtt"`
	MatchmakingTimestamp   time.Time                     `json:"matchmaking_timestamp"`
	MatchmakingTimeout     time.Duration                 `json:"matchmaking_timeout"`
	FailsafeTimeout        time.Duration                 `json:"failsafe_timeout"` // The failsafe timeout
	FallbackTimeout        time.Duration                 `json:"fallback_timeout"` // The fallback timeout
	DisplayName            string                        `json:"display_name"`
	profile                *GameProfileData
	latencyHistory         LatencyHistory
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
	if p.Rating == nil {
		return NewDefaultRating()
	}
	return *p.Rating.Load()
}

func (p *LobbySessionParameters) SetRating(rating types.Rating) {
	if p.Rating == nil {
		p.Rating = atomic.NewPointer(&rating)
	} else {
		p.Rating.Store(&rating)
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
		"is_early_quitter": strconv.FormatBool(s.IsEarlyQuitter),
		"role":             strconv.Itoa(s.Role),
	}
}

func NewLobbyParametersFromRequest(ctx context.Context, logger *zap.Logger, session *sessionWS, r evr.LobbySessionRequest) (*LobbySessionParameters, error) {
	p := session.evrPipeline

	userID := session.userID.String()
	mode := r.GetMode()
	level := r.GetLevel()
	versionLock := r.GetVersionLock()
	appID := r.GetAppID()

	sessionParams, ok := LoadParams(ctx)
	if !ok {
		return nil, fmt.Errorf("failed to load session parameters")
	}

	if sessionParams.accountMetadata == nil {
		return nil, fmt.Errorf("failed to load session parameters")
	}

	// Load the global matchmaking config
	serviceSettings := ServiceSettings()
	globalSettings := serviceSettings.Matchmaking
	globalSettingsVersion := serviceSettings.version

	// Load the user's matchmaking config
	userSettings, err := LoadMatchmakingSettings(ctx, p.runtimeModule, userID)
	if err != nil {
		return nil, fmt.Errorf("failed to load user matchmaking settings: %w", err)
	}

	if userSettings.NextMatchDiscordID != "" {
		// Get the host's user ID
		hostUserIDStr := p.discordCache.DiscordIDToUserID(userSettings.NextMatchDiscordID)

		// If the host userID exists, and is in a match, set the next match ID to the host's match ID
		if hostUserID := uuid.FromStringOrNil(hostUserIDStr); !hostUserID.IsNil() {

			// Get the MatchIDs for the user from it's presence
			presences, _ := p.runtimeModule.StreamUserList(StreamModeService, hostUserID.String(), "", StreamLabelMatchService, false, true)
			for _, presence := range presences {
				matchID := MatchIDFromStringOrNil(presence.GetStatus())
				if !matchID.IsNil() {
					userSettings.NextMatchID = matchID
				}
			}
		}
	}

	rating := userSettings.GetRating(mode)
	entrantRole := r.GetEntrantRole(0)

	nextMatchID := MatchID{}

	if !userSettings.NextMatchID.IsNil() {

		// Check that the match exists
		if _, _, err := p.matchRegistry.GetMatch(ctx, userSettings.NextMatchID.String()); err != nil {
			logger.Warn("Next match not found", zap.String("mid", userSettings.NextMatchID.String()))
		} else {
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
			if _, err := SaveToStorage(ctx, p.runtimeModule, userID, userSettings); err != nil {
				logger.Warn("Failed to clear next match metadata", zap.Error(err))
			}
		}()
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
		users, err := ListFriends(ctx, logger, p.db, p.statusRegistry, session.userID, 100, nil, cursor)
		if err != nil {
			return nil, fmt.Errorf("failed to list friends: %w", err)
		}

		friends = append(friends, users.Friends...)

		cursor = users.Cursor
		if users.Cursor == "" {
			break
		}
	}

	latencyHistory, err := LoadLatencyHistory(ctx, logger, p.db, session.userID)
	if err != nil {
		return nil, fmt.Errorf("failed to load latency history: %w", err)
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

	if r.GetFeatures() != nil {
		supportedFeatures = append(supportedFeatures, r.GetFeatures()...)
	}

	groupID := r.GetGroupID()
	if r.GetGroupID() == uuid.Nil {
		groupID = sessionParams.accountMetadata.GetActiveGroupID()
	}

	region := r.GetRegion()
	if region == evr.UnspecifiedRegion {
		region = evr.DefaultRegion
	}

	currentMatchID := MatchID{}
	if r.GetCurrentLobbyID() != uuid.Nil {
		currentMatchID = MatchID{UUID: r.GetCurrentLobbyID(), Node: node}
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

	// Add each blocked user that is online to the backfill query addon
	if len(blockedIDs) > 0 && mode != evr.ModeSocialPublic {

		// Avoid backfilling matches with players that this player blocks.
		backfillQueryAddons = append(backfillQueryAddons, fmt.Sprintf(`-label.players.user_id:/(%s)/`, Query.Join(blockedIDs, "|")))
	}

	rankPercentileMaxDelta := 1.0

	if globalSettings.RankPercentile.MaxDelta > 0 {
		rankPercentileMaxDelta = globalSettings.RankPercentile.MaxDelta
	}

	// Only calculate it, if it's out of date.
	rankPercentile := globalSettings.RankPercentile.Default

	if userSettings.StaticBaseRankPercentile > 0 {
		rankPercentile = userSettings.StaticBaseRankPercentile
	} else if userSettings.RankPercentile == 0 || globalSettingsVersion != userSettings.GlobalSettingsVersion {

		rankPercentile, err = CalculateSmoothedPlayerRankPercentile(ctx, logger, p.runtimeModule, userID, mode)
		if err != nil {
			return nil, fmt.Errorf("failed to calculate smoothed player rank percentile: %w", err)
		}
	}

	if rankPercentile != userSettings.RankPercentile {
		userSettings.RankPercentile = rankPercentile
		userSettings.GlobalSettingsVersion = globalSettingsVersion
		if _, err := SaveToStorage(ctx, p.runtimeModule, userID, userSettings); err != nil {
			logger.Warn("Failed to save user settings", zap.Error(err))
		}
	}

	maxServerRTT := 180

	if globalSettings.MaxServerRTT > 0 {
		maxServerRTT = globalSettings.MaxServerRTT
	}

	// Set the maxRTT to at least the average of the player's latency history
	averageRTT := 0
	count := 0
	if avgRTT := AverageLatencyHistories(latencyHistory); len(avgRTT) > 0 {
		for _, rtt := range avgRTT {
			averageRTT += rtt
			count++
		}
		averageRTT /= count
	}

	maxServerRTT = max(maxServerRTT, averageRTT)

	isEarlyQuitter := sessionParams.isEarlyQuitter.Load()

	maximumFailsafeSecs := globalSettings.MatchmakingTimeoutSecs - p.config.GetMatchmaker().IntervalSec*2
	failsafeTimeoutSecs := min(maximumFailsafeSecs, globalSettings.FailsafeTimeoutSecs)

	return &LobbySessionParameters{
		Node:                   node,
		UserID:                 session.userID,
		SessionID:              session.id,
		DiscordID:              sessionParams.DiscordID(),
		CurrentMatchID:         currentMatchID,
		VersionLock:            versionLock,
		AppID:                  appID,
		GroupID:                groupID,
		Region:                 region,
		Mode:                   mode,
		Level:                  level,
		SupportedFeatures:      supportedFeatures,
		RequiredFeatures:       requiredFeatures,
		Role:                   entrantRole,
		DisableArenaBackfill:   globalSettings.DisableArenaBackfill || userSettings.DisableArenaBackfill,
		BackfillQueryAddon:     strings.Join(backfillQueryAddons, " "),
		MatchmakingQueryAddon:  strings.Join(matchmakingQueryAddons, " "),
		CreateQueryAddon:       strings.Join(createQueryAddons, " "),
		PartyGroupName:         lobbyGroupName,
		PartyID:                partyID,
		PartySize:              atomic.NewInt64(1),
		NextMatchID:            nextMatchID,
		latencyHistory:         latencyHistory,
		BlockedIDs:             blockedIDs,
		Rating:                 atomic.NewPointer(&rating),
		Verbose:                sessionParams.accountMetadata.DiscordDebugMessages,
		IsEarlyQuitter:         isEarlyQuitter,
		RankPercentile:         atomic.NewFloat64(rankPercentile),
		RankPercentileMaxDelta: rankPercentileMaxDelta,
		MaxServerRTT:           maxServerRTT,
		MatchmakingTimestamp:   time.Now().UTC(),
		MatchmakingTimeout:     time.Duration(globalSettings.MatchmakingTimeoutSecs) * time.Second,
		FailsafeTimeout:        time.Duration(failsafeTimeoutSecs) * time.Second,
		FallbackTimeout:        time.Duration(globalSettings.FallbackTimeoutSecs) * time.Second,
		DisplayName:            sessionParams.accountMetadata.GetGroupDisplayNameOrDefault(groupID.String()),
	}, nil
}

func (p LobbySessionParameters) String() string {
	data, err := json.Marshal(p)
	if err != nil {
		return ""
	}
	return string(data)
}

func (p *LobbySessionParameters) BackfillSearchQuery(includeRankRange bool, includeMaxRTT bool) string {
	qparts := []string{
		"+label.open:T",
		fmt.Sprintf("+label.mode:%s", p.Mode.String()),
		fmt.Sprintf("+label.group_id:/%s/", Query.Escape(p.GroupID.String())),
		//fmt.Sprintf("label.version_lock:%s", p.VersionLock.String()),
		p.BackfillQueryAddon,
	}

	if includeRankRange {
		rankLower := min(p.GetRankPercentile()-p.RankPercentileMaxDelta, 1.0-2.0*p.RankPercentileMaxDelta)
		rankUpper := max(p.GetRankPercentile()+p.RankPercentileMaxDelta, 2.0*p.RankPercentileMaxDelta)
		rankLower = max(rankLower, 0.0)
		rankUpper = min(rankUpper, 1.0)

		qparts = append(qparts, fmt.Sprintf("+label.rank_percentile:>=%f +label.rank_percentile:<=%f", rankLower, rankUpper))
	}

	if len(p.RequiredFeatures) > 0 {
		for _, f := range p.RequiredFeatures {
			qparts = append(qparts, fmt.Sprintf("+label.features:/.*%s.*/", Query.Escape(f)))
		}
	}

	// Do not backfill into the same match
	if !p.CurrentMatchID.IsNil() {
		qparts = append(qparts, fmt.Sprintf("-label.id:%s", Query.Escape(p.CurrentMatchID.String())))
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
		// Ignore all matches with too high latency
		for ip, rtt := range p.latencyHistory.LatestRTTs() {
			if rtt > p.MaxServerRTT {
				qparts = append(qparts, fmt.Sprintf("-label.broadcaster.endpoint:/.*%s.*/", Query.Escape(ip)))
			}
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
	rating := rating.NewWithOptions(&types.OpenSkillOptions{
		Mu:    &mu,
		Sigma: &sigma,
	})

	p.Mode = evr.ToSymbol(stringProperties["game_mode"])
	p.GroupID = uuid.FromStringOrNil(stringProperties["group_id"])
	p.VersionLock = evr.ToSymbol(stringProperties["version_lock:"])
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

	// Rebuild the latency history
	p.latencyHistory = make(LatencyHistory)
	for ip, rtt := range serverRTTs {
		p.latencyHistory[ip] = make(map[int64]int)
		p.latencyHistory[ip][time.Now().UTC().Unix()] = rtt
	}
}

func (p *LobbySessionParameters) MatchmakingParameters(ticketParams *MatchmakingTicketParameters) (string, map[string]string, map[string]float64) {

	submissionTime := time.Now().UTC().Format(time.RFC3339)
	stringProperties := map[string]string{
		"game_mode":       p.Mode.String(),
		"group_id":        p.GroupID.String(),
		"version_lock":    p.VersionLock.String(),
		"blocked_ids":     strings.Join(p.BlockedIDs, " "),
		"display_name":    p.DisplayName,
		"submission_time": submissionTime,
	}

	rating := p.GetRating()
	numericProperties := map[string]float64{
		"rating_mu":                 rating.Mu,
		"rating_sigma":              rating.Sigma,
		"rank_percentile":           p.GetRankPercentile(),
		"timestamp":                 float64(time.Now().UTC().Unix()),
		"rank_percentile_max_delta": p.RankPercentileMaxDelta,
		"max_rtt":                   float64(p.MaxServerRTT),
	}

	qparts := []string{
		"+properties.game_mode:" + p.Mode.String(),
		fmt.Sprintf("+properties.group_id:%s", Query.Escape(p.GroupID.String())),
		fmt.Sprintf(`-properties.blocked_ids:/.*%s.*/`, Query.Escape(p.UserID.String())),
		//"+properties.version_lock:" + p.VersionLock.String(),
		p.MatchmakingQueryAddon,
	}

	// If the user has an early quit penalty, only match them with players who have submitted after now
	if ticketParams.IncludeEarlyQuitPenalty {
		// Only match with players who have submitted after this player starts matchmaking
		qparts = append(qparts, fmt.Sprintf(`-properties.submission_time:>="%s"`, submissionTime))
		// Until the match clock is correct, this has to be disabled
		/*
			if p.EarlyQuitPenaltyExpiry.After(time.Now()) {
				qparts = append(qparts, fmt.Sprintf(`-properties.submission_time:<"%s"`, submissionTime))
			}
		*/
	}
	rankPercentile := p.GetRankPercentile()
	if ticketParams.IncludeRankRange && p.RankPercentileMaxDelta > 0 {
		rankLower := min(rankPercentile-p.RankPercentileMaxDelta, 1.0-2.0*p.RankPercentileMaxDelta)
		rankUpper := max(rankPercentile+p.RankPercentileMaxDelta, 2.0*p.RankPercentileMaxDelta)
		rankLower = max(rankLower, 0.0)
		rankUpper = min(rankUpper, 1.0)
		numericProperties["rank_percentile_min"] = rankLower
		numericProperties["rank_percentile_max"] = rankUpper

		qparts = append(qparts,

			fmt.Sprintf("-properties.rank_percentile_min:>%f", rankPercentile),
			fmt.Sprintf("-properties.rank_percentile_max:<%f", rankPercentile),
		)

		qparts = append(qparts,
			fmt.Sprintf("-properties.rank_percentile:<%f", rankLower),
			fmt.Sprintf("-properties.rank_percentile:>%f", rankUpper))
	}

	//maxDelta := 60 // milliseconds
	for k, v := range AverageLatencyHistories(p.latencyHistory) {
		numericProperties[RTTPropertyPrefix+k] = float64(v)
		//qparts = append(qparts, fmt.Sprintf("properties.%s:<=%d", k, v+maxDelta))
	}

	if ticketParams.IncludeRequireCommonServer {
		// Create a string list of validRTTs
		acceptableServers := make([]string, 0)
		for ip, rtt := range p.latencyHistory.LatestRTTs() {
			if rtt <= p.MaxServerRTT {
				acceptableServers = append(acceptableServers, ip)
			}
		}

		stringProperties["servers"] = strings.Join(acceptableServers, " ")

		// Add the acceptable servers to the query
		if len(acceptableServers) > 0 {
			qparts = append(qparts, fmt.Sprintf("+properties.servers:/.*(%s).*/", Query.Join(acceptableServers, "|")))
		}

	}

	// Remove blanks from qparts
	for i := 0; i < len(qparts); i++ {
		if qparts[i] == "" {
			qparts = append(qparts[:i], qparts[i+1:]...)
			i--
		}
	}

	query := strings.Trim(strings.Join(qparts, " "), " ")

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

func AverageLatencyHistories(histories LatencyHistory) map[string]int {
	averages := make(map[string]int)

	// Only consider the last 3 days of latency data
	threedays := time.Now().Add(-time.Hour * 72).UTC().Unix()

	for ip, history := range histories {
		// Average the RTT
		rtt := 0

		if len(history) == 0 {
			continue
		}
		for ts, v := range history {
			if ts < threedays {
				continue
			}
			rtt += v
		}
		rtt /= len(history)
		if rtt == 0 {
			continue
		}

		rtt = mroundRTT(rtt, 10)

		averages[ip] = rtt
	}

	return averages
}

func recordRatingToLeaderboard(ctx context.Context, nk runtime.NakamaModule, userID, displayName string, mode evr.Symbol, rating types.Rating) error {
	modeprefix := ""
	switch mode {
	case evr.ModeArenaPublic:
		modeprefix = "Arena"
	case evr.ModeCombatPublic:
		modeprefix = "Combat"
	}

	scores := map[string]float64{
		fmt.Sprintf("%s:%s:%s", mode.String(), modeprefix+"RatingSigma", "alltime"): rating.Sigma,
		fmt.Sprintf("%s:%s:%s", mode.String(), modeprefix+"RatingMu", "alltime"):    rating.Mu,
	}

	for id, value := range scores {
		score, subscore := ValueToScore(value)

		// Write the record
		if _, err := nk.LeaderboardRecordWrite(ctx, id, userID, displayName, score, subscore, nil, nil); err != nil {
			// Try to create the leaderboard
			err = nk.LeaderboardCreate(ctx, id, true, "desc", "set", "", nil, true)
			if err != nil {
				return fmt.Errorf("Leaderboard create error: %w", err)
			} else {
				// Retry the write
				_, err := nk.LeaderboardRecordWrite(ctx, id, userID, displayName, score, subscore, nil, nil)
				if err != nil {
					return fmt.Errorf("Leaderboard record write error: %w", err)
				}
			}
		}
	}

	return nil
}

func recordPercentileToLeaderboard(ctx context.Context, nk runtime.NakamaModule, userID, username string, mode evr.Symbol, percentile float64) error {

	modeprefix := ""
	switch mode {
	case evr.ModeArenaPublic:
		modeprefix = "Arena"
	case evr.ModeCombatPublic:
		modeprefix = "Combat"
	}

	id := fmt.Sprintf("%s:%s:%s", mode.String(), modeprefix+"RankPercentile", "alltime")

	score, subscore := ValueToScore(percentile)

	// Write the record
	_, err := nk.LeaderboardRecordWrite(ctx, id, userID, username, score, subscore, nil, nil)

	if err != nil {
		// Try to create the leaderboard
		err = nk.LeaderboardCreate(ctx, id, true, "asc", "set", "", nil, true)

		if err != nil {
			return fmt.Errorf("Leaderboard create error: %w", err)
		} else {
			// Retry the write
			_, err := nk.LeaderboardRecordWrite(ctx, id, userID, username, score, subscore, nil, nil)
			if err != nil {
				return fmt.Errorf("Leaderboard record write error: %w", err)
			}
		}
	}

	return nil
}
