package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
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
	VersionLock            evr.Symbol    `json:"version_lock"`
	AppID                  evr.Symbol    `json:"app_id"`
	GroupID                uuid.UUID     `json:"group_id"`
	Region                 evr.Symbol    `json:"region"`
	Mode                   evr.Symbol    `json:"mode"`
	Level                  evr.Symbol    `json:"level"`
	SupportedFeatures      []string      `json:"supported_features"`
	RequiredFeatures       []string      `json:"required_features"`
	CurrentMatchID         MatchID       `json:"current_match_id"`
	Role                   int           `json:"role"`
	PartySize              *atomic.Int64 `json:"party_size"`
	PartyID                uuid.UUID     `json:"party_id"`
	PartyGroupName         string        `json:"party_group_name"`
	NextMatchID            MatchID       `json:"next_match_id"`
	DisableArenaBackfill   bool          `json:"disable_arena_backfill"`
	BackfillQueryAddon     string        `json:"backfill_query_addon"`
	MatchmakingQueryAddon  string        `json:"matchmaking_query_addon"`
	CreateQueryAddon       string        `json:"create_query_addon"`
	Verbose                bool          `json:"verbose"`
	BlockedIDs             []string      `json:"blocked_ids"`
	Rating                 types.Rating  `json:"rating"`
	IsEarlyQuitter         bool          `json:"quit_last_game_early"`
	EarlyQuitPenaltyExpiry time.Time     `json:"early_quit_penalty_expiry"`
	latencyHistory         LatencyHistory
	ProfileStatistics      evr.PlayerStatistics
	RankPercentile         float64 `json:"rank_percentile"`
}

func (p *LobbySessionParameters) GetPartySize() int {
	return int(p.PartySize.Load())
}

func (p *LobbySessionParameters) SetPartySize(size int) {
	p.PartySize.Store(int64(size))
}

func (s LobbySessionParameters) MetricsTags() map[string]string {
	return map[string]string{
		"mode":     s.Mode.String(),
		"group_id": s.GroupID.String(),
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
	if userSettings.LobbyGroupName != uuid.Nil.String() {
		lobbyGroupName = userSettings.LobbyGroupName
	}
	if lobbyGroupName == "" {
		lobbyGroupName = uuid.Must(uuid.NewV4()).String()
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

	rating := profile.GetRating()

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
	if len(blockedIDs) > 0 {

		// Avoid backfilling matches with players that this player blocks.
		backfillQueryAddons = append(backfillQueryAddons, fmt.Sprintf(`-label.players.user_id:/(%s)/`, Query.Join(blockedIDs, "|")))
	}

	eqstats := profile.GetEarlyQuitStatistics()
	penaltyExpiry := time.Unix(eqstats.PenaltyExpiry, 0)

	percentile, _, err := OverallPercentile(ctx, logger, p.runtimeModule, session.userID.String())
	if err != nil {
		return nil, fmt.Errorf("failed to get overall percentile: %w", err)
	}
	if percentile == 0 {
		percentile = 0.50
	}

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

	return &LobbySessionParameters{
		Node:                   node,
		UserID:                 session.userID,
		SessionID:              session.id,
		CurrentMatchID:         currentMatchID,
		VersionLock:            versionLock,
		AppID:                  appID,
		GroupID:                groupID,
		Region:                 region,
		Mode:                   mode,
		Level:                  level,
		SupportedFeatures:      supportedFeatures,
		RequiredFeatures:       requiredFeatures,
		Role:                   r.GetEntrantRole(0),
		DisableArenaBackfill:   globalSettings.DisableArenaBackfill || userSettings.DisableArenaBackfill,
		BackfillQueryAddon:     strings.Join(backfillQueryAddons, " "),
		MatchmakingQueryAddon:  strings.Join(matchmakingQueryAddons, " "),
		CreateQueryAddon:       strings.Join(createQueryAddons, " "),
		PartyGroupName:         lobbyGroupName,
		PartyID:                uuid.NewV5(uuid.Nil, lobbyGroupName),
		PartySize:              atomic.NewInt64(1),
		NextMatchID:            userSettings.NextMatchID,
		latencyHistory:         latencyHistory,
		BlockedIDs:             blockedIDs,
		Rating:                 rating,
		Verbose:                sessionParams.AccountMetadata.DiscordDebugMessages,
		ProfileStatistics:      profile.LatestStatistics(true, true, false),
		EarlyQuitPenaltyExpiry: penaltyExpiry,
		IsEarlyQuitter:         isEarlyQuitter,
		RankPercentile:         percentile,
	}, nil
}

func (p LobbySessionParameters) String() string {
	data, _ := json.Marshal(p)
	return string(data)
}

func (p *LobbySessionParameters) BackfillSearchQuery(maxRTT int) string {
	qparts := []string{
		"+label.open:T",
		fmt.Sprintf("+label.mode:%s", p.Mode.String()),
		fmt.Sprintf("+label.group_id:/%s/", Query.Escape(p.GroupID.String())),
		//fmt.Sprintf("label.version_lock:%s", p.VersionLock.String()),
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

	if maxRTT > 0 {
		// Ignore all matches with too high latency
		for ip, rtt := range p.latencyHistory.LatestRTTs() {
			if rtt > maxRTT {
				qparts = append(qparts, fmt.Sprintf("-label.broadcaster.endpoint:/.*%s.*/", ip))
			}
		}
	}
	return strings.Join(qparts, " ")

}

func (p *LobbySessionParameters) MatchmakingParameters(sessionParams *SessionParameters) (string, map[string]string, map[string]float64) {

	displayName := sessionParams.AccountMetadata.GetGroupDisplayNameOrDefault(p.GroupID.String())

	submissionTime := time.Now().UTC().Format(time.RFC3339)
	stringProperties := map[string]string{
		"mode":            p.Mode.String(),
		"group_id":        p.GroupID.String(),
		"version_lock":    p.VersionLock.String(),
		"blocked_ids":     strings.Join(p.BlockedIDs, " "),
		"display_name":    displayName,
		"submission_time": submissionTime,
	}

	numericProperties := map[string]float64{
		"rating_mu":       p.Rating.Mu,
		"rating_sigma":    p.Rating.Sigma,
		"rating_z":        float64(p.Rating.Z),
		"rank_percentile": p.RankPercentile,
	}

	qparts := []string{
		"+properties.mode:" + p.Mode.String(),
		fmt.Sprintf("+properties.group_id:/%s/", Query.Escape(p.GroupID.String())),
		fmt.Sprintf(`-properties.blocked_ids:/.*%s.*/`, Query.Escape(p.UserID)),
		//"+properties.version_lock:" + p.VersionLock.String(),
		p.MatchmakingQueryAddon,
	}

	// If the user has an early quit penalty, only match them with players who have submitted before the penalty expiry
	//if p.EarlyQuitPenaltyExpiry.After(time.Now()) {
	//	qparts = append(qparts, fmt.Sprintf(`-properties.submission_time:<="%s"`, submissionTime))
	//}

	// Add the user's weekly stats to their numericProperties
	for mode, stats := range p.ProfileStatistics {
		for k, s := range stats {
			v, ok := s.Value.(float64)
			if !ok {
				continue
			}
			numericProperties[fmt.Sprintf("stats_%s_%s", mode, k)] = v
		}
	}

	//maxDelta := 60 // milliseconds
	for k, v := range AverageLatencyHistories(p.latencyHistory) {
		numericProperties[k] = float64(v)
		//qparts = append(qparts, fmt.Sprintf("properties.%s:<=%d", k, v+maxDelta))
	}

	query := strings.Join(qparts, " ")

	return query, stringProperties, numericProperties
}

func (p LobbySessionParameters) MatchmakingStream() PresenceStream {
	return PresenceStream{Mode: StreamModeMatchmaking, Subject: p.GroupID}
}

func (p LobbySessionParameters) PresenceMeta() PresenceMeta {
	return PresenceMeta{
		Status: p.String(),
	}
}

func AverageLatencyHistories(histories LatencyHistory) map[string]int {
	averages := make(map[string]int)
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

		k := ipToKey(net.ParseIP(ip))
		rtt = mroundRTT(rtt, 10)

		averages[k] = rtt
	}

	return averages
}

func OverallPercentile(ctx context.Context, logger *zap.Logger, nk runtime.NakamaModule, userID string) (float64, map[string]*api.LeaderboardRecord, error) {

	statNames := []string{
		"ArenaGamesPlayed",
		"ArenaWins",
		"ArenaLosses",
		"ArenaWinPercentage",
		"AssistsPerGame",
		"AveragePointsPerGame",
		"AverageTopSpeedPerGame",
		"BlockPercentage",
		"GoalScorePercentage",
		"GoalsPerGame",
	}

	statRecords := make(map[string]*api.LeaderboardRecord)
	for _, s := range statNames {
		id := fmt.Sprintf("daily:%s", s)

		records, _, _, _, err := nk.LeaderboardRecordsList(ctx, id, []string{userID}, 10000, "", 0)
		if err != nil {
			continue
		}

		if len(records) == 0 {
			continue
		}

		statRecords[s] = records[0]
	}

	if len(statRecords) == 0 {
		return 0, nil, nil
	}

	// Combine all the stat ranks into a single percentile.
	percentiles := make([]float64, 0, len(statRecords))
	for _, r := range statRecords {
		percentile := float64(r.GetRank()) / float64(r.GetNumScore())
		percentiles = append(percentiles, percentile)
	}

	// Calculate the overall percentile.
	overallPercentile := 0.0
	for _, p := range percentiles {
		overallPercentile += p
	}
	overallPercentile /= float64(len(percentiles))

	logger.Info("Overall percentile", zap.Float64("percentile", overallPercentile))

	return overallPercentile, statRecords, nil
}
