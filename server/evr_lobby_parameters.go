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
	"github.com/intinig/go-openskill/types"
	"go.uber.org/zap"
)

type (
	ctxLobbyParametersKey struct{}
)

type LobbySessionParameters struct {
	Node                   string        `json:"node"`
	UserID                 uuid.UUID     `json:"user_id"`
	SessionID              uuid.UUID     `json:"session_id"`
	DiscordID              string        `json:"discord_id"`
	VersionLock            evr.Symbol    `json:"version_lock"`
	AppID                  evr.Symbol    `json:"app_id"`
	GroupID                uuid.UUID     `json:"group_id"`
	Region                 evr.Symbol    `json:"region"`
	Mode                   evr.Symbol    `json:"mode"`
	Level                  evr.Symbol    `json:"level"`
	SupportedFeatures      []string      `json:"supported_features"`
	RequiredFeatures       []string      `json:"required_features"`
	CurrentMatchID         MatchID       `json:"current_match_id"`
	NextMatchID            MatchID       `json:"next_match_id"`
	Role                   int           `json:"role"`
	PartySize              *atomic.Int64 `json:"party_size"`
	PartyID                uuid.UUID     `json:"party_id"`
	PartyGroupName         string        `json:"party_group_name"`
	DisableArenaBackfill   bool          `json:"disable_arena_backfill"`
	BackfillQueryAddon     string        `json:"backfill_query_addon"`
	MatchmakingQueryAddon  string        `json:"matchmaking_query_addon"`
	CreateQueryAddon       string        `json:"create_query_addon"`
	Verbose                bool          `json:"verbose"`
	BlockedIDs             []string      `json:"blocked_ids"`
	Rating                 types.Rating  `json:"rating"`
	IsEarlyQuitter         bool          `json:"quit_last_game_early"`
	EarlyQuitPenaltyExpiry time.Time     `json:"early_quit_penalty_expiry"`
	RankPercentile         float64       `json:"rank_percentile"`
	RankPercentileMaxDelta float64       `json:"rank_percentile_max_delta"`
	MaxServerRTT           int           `json:"max_server_rtt"`
	MatchmakingTimestamp   time.Time     `json:"matchmaking_timestamp"`
	MatchmakingTimeout     time.Duration `json:"matchmaking_timeout"`
	FailsafeTimeout        time.Duration `json:"failsafe_timeout"` // The failsafe timeout
	FallbackTimeout        time.Duration `json:"fallback_timeout"` // The fallback timeout
	DisplayName            string        `json:"display_name"`

	latencyHistory LatencyHistory
}

func (p *LobbySessionParameters) GetPartySize() int {
	return int(p.PartySize.Load())
}

func (p *LobbySessionParameters) SetPartySize(size int) {
	p.PartySize.Store(int64(size))
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

	if sessionParams == nil || sessionParams.AccountMetadata == nil {
		return nil, fmt.Errorf("failed to load session parameters")
	}

	// Load the global matchmaking config
	globalSettings, err := LoadMatchmakingSettings(ctx, p.runtimeModule, SystemUserID)
	if err != nil {
		return nil, fmt.Errorf("failed to load global matchmaking settings: %w", err)
	}

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

			// get the match ID of the host
			stream := PresenceStream{Mode: StreamModeService, Subject: hostUserID, Label: StreamLabelMatchService}
			for _, p := range session.pipeline.tracker.ListByStream(stream, false, true) {
				memberMatchID := MatchIDFromStringOrNil(p.GetStatus())
				if !memberMatchID.IsNil() {
					userSettings.NextMatchID = memberMatchID
				}
			}
		}
	}

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
			if err := SaveToStorage(ctx, p.runtimeModule, userID, userSettings); err != nil {
				logger.Warn("Failed to clear next match metadata", zap.Error(err))
			}
		}()
	}

	matchmakingQueryAddons := []string{
		globalSettings.MatchmakingQueryAddon,
		userSettings.MatchmakingQueryAddon,
	}

	backfillQueryAddons := []string{
		globalSettings.BackfillQueryAddon,
		userSettings.BackfillQueryAddon,
	}

	createQueryAddons := []string{
		globalSettings.CreateQueryAddon,
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

	requiredFeatures := sessionParams.RequiredFeatures
	supportedFeatures := sessionParams.SupportedFeatures

	if r.GetFeatures() != nil {
		supportedFeatures = append(supportedFeatures, r.GetFeatures()...)
	}

	groupID := r.GetGroupID()
	if r.GetGroupID() == uuid.Nil {
		groupID = sessionParams.AccountMetadata.GetActiveGroupID()
	}

	region := r.GetRegion()
	if region == evr.UnspecifiedRegion {
		region = evr.DefaultRegion
	}

	currentMatchID := MatchID{}
	if r.GetCurrentLobbyID() != uuid.Nil {
		currentMatchID = MatchID{UUID: r.GetCurrentLobbyID(), Node: node}
	}

	profile, err := session.evrPipeline.profileRegistry.Load(ctx, session.userID)
	if err != nil {
		return nil, fmt.Errorf("failed to load profile: %w", err)
	}

	rating := profile.GetRating(groupID, mode)

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

	eqstats := profile.GetEarlyQuitStatistics()
	penaltyExpiry := time.Unix(eqstats.PenaltyExpiry, 0)

	rankPercentileMaxDelta := 1.0

	if globalSettings.RankPercentileMaxDelta > 0 {
		rankPercentileMaxDelta = globalSettings.RankPercentileMaxDelta
	}

	if userSettings.RankPercentileMaxDelta > 0 {
		rankPercentileMaxDelta = userSettings.RankPercentileMaxDelta
	}

	rankStatsPeriod := "weekly"

	if globalSettings.RankResetSchedule != "" {
		rankStatsPeriod = globalSettings.RankResetSchedule
	}
	if userSettings.RankResetSchedule != "" {
		rankStatsPeriod = userSettings.RankResetSchedule
	}

	rankStatsPeriodDamping := "daily"
	if globalSettings.RankResetScheduleDamping != "" {
		rankStatsPeriod = globalSettings.RankResetScheduleDamping
	}

	if userSettings.RankResetScheduleDamping != "" {
		rankStatsPeriod = userSettings.RankResetScheduleDamping
	}

	rankStatsDefaultPercentile := 0.3
	if globalSettings.RankPercentileDefault > 0 {
		rankStatsDefaultPercentile = globalSettings.RankPercentileDefault
	}

	if userSettings.RankPercentileDefault > 0 {
		rankStatsDefaultPercentile = userSettings.RankPercentileDefault
	}

	basePercentile := rankStatsDefaultPercentile
	dampingPercentile := rankStatsDefaultPercentile

	if globalSettings.RankBoardWeights != nil {
		if boardWeights, ok := globalSettings.RankBoardWeights[mode.String()]; ok {

			basePercentile, err = RecalculatePlayerRankPercentile(ctx, logger, p.runtimeModule, session.userID.String(), mode, rankStatsPeriod, rankStatsDefaultPercentile, boardWeights)
			if err != nil {
				return nil, fmt.Errorf("failed to get overall percentile: %w", err)
			}

			dampingPercentile, err = RecalculatePlayerRankPercentile(ctx, logger, p.runtimeModule, session.userID.String(), mode, rankStatsPeriodDamping, rankStatsDefaultPercentile, boardWeights)
			if err != nil {
				return nil, fmt.Errorf("failed to get daily percentile: %w", err)
			}
		}
	}

	if globalSettings.StaticBaseRankPercentile > 0 {
		basePercentile = globalSettings.StaticBaseRankPercentile
	}

	if userSettings.StaticBaseRankPercentile > 0 {
		basePercentile = userSettings.StaticBaseRankPercentile
	}

	if dampingPercentile == 0 {
		dampingPercentile = basePercentile
	}

	smoothingFactor := globalSettings.RankPercentileDampingFactor
	if userSettings.RankPercentileDampingFactor > 0 {
		// Ensure the percentile is at least 0.2
		basePercentile = basePercentile + (dampingPercentile-basePercentile)*smoothingFactor
	}

	maxServerRTT := 180

	if globalSettings.MaxServerRTT > 0 {
		maxServerRTT = globalSettings.MaxServerRTT
	}

	if userSettings.MaxServerRTT > 0 {
		maxServerRTT = userSettings.MaxServerRTT
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

	isEarlyQuitter := false
	// Check if the last game was quit early
	if len(eqstats.History) > 0 {
		var lastGame int64
		for ts := range eqstats.History {
			if ts > lastGame {
				lastGame = ts
			}
		}
		if eqstats.History[lastGame] {
			isEarlyQuitter = true
		}
	}

	maximumFailsafeSecs := globalSettings.MatchmakingTimeoutSecs - p.config.GetMatchmaker().IntervalSec*2
	failsafeTimeoutSecs := min(maximumFailsafeSecs, globalSettings.FailsafeTimeoutSecs)

	return &LobbySessionParameters{
		Node:                   node,
		UserID:                 session.userID,
		SessionID:              session.id,
		DiscordID:              sessionParams.DiscordID,
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
		Rating:                 rating,
		Verbose:                sessionParams.AccountMetadata.DiscordDebugMessages,
		EarlyQuitPenaltyExpiry: penaltyExpiry,
		IsEarlyQuitter:         isEarlyQuitter,
		RankPercentile:         basePercentile,
		RankPercentileMaxDelta: rankPercentileMaxDelta,
		MaxServerRTT:           maxServerRTT,
		MatchmakingTimestamp:   time.Now().UTC(),
		MatchmakingTimeout:     time.Duration(globalSettings.MatchmakingTimeoutSecs) * time.Second,
		FailsafeTimeout:        time.Duration(failsafeTimeoutSecs) * time.Second,
		FallbackTimeout:        time.Duration(globalSettings.FallbackTimeoutSecs) * time.Second,
		DisplayName:            sessionParams.AccountMetadata.GetGroupDisplayNameOrDefault(groupID.String()),
	}, nil
}

func (p LobbySessionParameters) String() string {
	data, _ := json.Marshal(p)
	return string(data)
}

func (p *LobbySessionParameters) BackfillSearchQuery(includeRankRange bool, includeMaxRTT bool) string {
	qparts := []string{
		"+label.open:T",
		fmt.Sprintf("+label.mode:%s", p.Mode.String()),
		fmt.Sprintf("+label.group_id:/%s/", Query.Escape(p.GroupID.String())),
		//fmt.Sprintf("label.version_lock:%s", p.VersionLock.String()),
	}

	if includeRankRange {
		rankLower := min(p.RankPercentile-p.RankPercentileMaxDelta, 1.0-2.0*p.RankPercentileMaxDelta)
		rankUpper := max(p.RankPercentile+p.RankPercentileMaxDelta, 2.0*p.RankPercentileMaxDelta)
		rankLower = max(rankLower, 0.0)
		rankUpper = min(rankUpper, 1.0)

		// Ensure that the rank minRank percentile range is 1.0-2.0*p.RankPercentileMaxDelta

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

	p.Mode = evr.ToSymbol(stringProperties["game_mode"])
	p.GroupID = uuid.FromStringOrNil(stringProperties["group_id"])
	p.VersionLock = evr.ToSymbol(stringProperties["version_lock:"])
	p.BlockedIDs = strings.Split(stringProperties["blocked_ids"], " ")
	p.DisplayName = stringProperties["display_name"]
	p.EarlyQuitPenaltyExpiry, _ = time.Parse(time.RFC3339, stringProperties["early_quit_penalty_expiry"])
	p.Rating = types.Rating{Mu: numericProperties["rating_mu"], Sigma: numericProperties["rating_sigma"]}
	p.RankPercentile = numericProperties["rank_percentile"]
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
		"game_mode":                 p.Mode.String(),
		"group_id":                  p.GroupID.String(),
		"version_lock":              p.VersionLock.String(),
		"blocked_ids":               strings.Join(p.BlockedIDs, " "),
		"display_name":              p.DisplayName,
		"submission_time":           submissionTime,
		"early_quit_penalty_expiry": p.EarlyQuitPenaltyExpiry.Format(time.RFC3339),
	}

	numericProperties := map[string]float64{
		"rating_mu":                 p.Rating.Mu,
		"rating_sigma":              p.Rating.Sigma,
		"rank_percentile":           p.RankPercentile,
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

	if ticketParams.IncludeRankRange && p.RankPercentileMaxDelta > 0 {
		rankLower := min(p.RankPercentile-p.RankPercentileMaxDelta, 1.0-2.0*p.RankPercentileMaxDelta)
		rankUpper := max(p.RankPercentile+p.RankPercentileMaxDelta, 2.0*p.RankPercentileMaxDelta)
		rankLower = max(rankLower, 0.0)
		rankUpper = min(rankUpper, 1.0)
		numericProperties["rank_percentile_min"] = rankLower
		numericProperties["rank_percentile_max"] = rankUpper

		qparts = append(qparts,

			fmt.Sprintf("-properties.rank_percentile_min:>%f", p.RankPercentile),
			fmt.Sprintf("-properties.rank_percentile_max:<%f", p.RankPercentile),
		)

		qparts = append(qparts, fmt.Sprintf("-properties.rank_percentile:<%f -properties.rank_percentile:>%f", rankLower, rankUpper))
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

func recordPercentileToLeaderboard(ctx context.Context, nk runtime.NakamaModule, userID, username string, mode evr.Symbol, percentile float64) error {
	periods := []string{"alltime", "daily", "weekly"}

	modeprefix := ""
	switch mode {
	case evr.ModeArenaPublic:
		modeprefix = "Arena"
	case evr.ModeCombatPublic:
		modeprefix = "Combat"
	}

	for _, period := range periods {
		id := fmt.Sprintf("%s:%s:%s", mode.String(), modeprefix+"RankPercentile", period)

		score, subScore := ValueToScore(percentile)

		// Write the record
		_, err := nk.LeaderboardRecordWrite(ctx, id, userID, username, score, subScore, nil, nil)

		if err != nil {
			// Try to create the leaderboard
			err = nk.LeaderboardCreate(ctx, id, true, "asc", "set", PeriodicityToSchedule(period), nil, true)

			if err != nil {
				return fmt.Errorf("Leaderboard create error: %w", err)
			} else {
				// Retry the write
				_, err := nk.LeaderboardRecordWrite(ctx, id, userID, username, score, subScore, nil, nil)
				if err != nil {
					return fmt.Errorf("Leaderboard record write error: %w", err)
				}
			}
		}
	}

	return nil
}
