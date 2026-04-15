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
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
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
	IsModerator                  bool                          `json:"is_moderator"` // True if user is a moderator (enforcer or operator), regardless of division
	MatchmakingRating            *atomic.Pointer[types.Rating] `json:"matchmaking_rating"`
	EarlyQuitPenaltyLevel        int                           `json:"early_quit_penalty_level"`
	EarlyQuitMatchmakingTier     int32                         `json:"early_quit_matchmaking_tier"`
	EnableSBMM                   bool                          `json:"disable_sbmm"`
	EnableOrdinalRange           bool                          `json:"enable_ordinal_range"`
	EnableDivisions              bool                          `json:"enable_divisions"`
	MatchmakingRatingRange       float64                       `json:"rating_range"`
	MatchmakingDivisions         []string                      `json:"divisions"`
	// TODO: MatchmakingExcludedDivisions is populated and set as a ticket property
	// but the matchmaker query filter that would read it is commented out.
	// Wire this into the matchmaker query before considering it active.
	MatchmakingExcludedDivisions []string `json:"excluded_divisions"`
	MaxServerRTT                 int                           `json:"max_server_rtt"`
	MatchmakingTimestamp         time.Time                     `json:"matchmaking_timestamp"`
	MatchmakingTimeout           time.Duration                 `json:"matchmaking_timeout"`
	FailsafeTimeout              time.Duration                 `json:"failsafe_timeout"` // The failsafe timeout
	FallbackTimeout              time.Duration                 `json:"fallback_timeout"` // The fallback timeout
	DisplayName                  string                        `json:"display_name"`
	GamesPlayed                  int                           `json:"games_played"`                  // Total games played, loaded from GamesPlayed leaderboard
	HardDivision                 string                        `json:"hard_division"`                 // Skill division bracket for hard division filtering
	IsAmbassador                 bool                          `json:"is_ambassador"`                 // True if player is ambassadoring this match
	HasSuspensionHistoryFlag     bool                          `json:"has_suspension_history"`         // True if player has any suspension history (exempt: enforcers/operators always false)
	latencyHistory               *atomic.Pointer[LatencyHistory]
	unreachableServers           *atomic.Pointer[UnreachableServers]
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

// resolveDirectiveRole determines the entrant role when a join directive is
// present. Explicit directive roles (orange, blue, spectator, moderator)
// override the request. "any" or "" preserve the client's requested role,
// falling back to TeamUnassigned only when the request itself is unset.
func resolveDirectiveRole(requestRole int, directive *JoinDirective) int {
	if directive == nil {
		return requestRole
	}
	switch directive.Role {
	case "orange":
		return evr.TeamOrange
	case "blue":
		return evr.TeamBlue
	case "spectator":
		return evr.TeamSpectator
	case "moderator":
		return evr.TeamModerator
	case "any", "":
		// Preserve the client's requested role (e.g. spectator stream clients).
		// Default to unassigned only when the request role is unset.
		if requestRole >= 0 {
			return requestRole
		}
		return evr.TeamUnassigned
	default:
		return requestRole
	}
}

func NewLobbyParametersFromRequest(ctx context.Context, logger *zap.Logger, nk runtime.NakamaModule, session *sessionWS, request evr.LobbySessionRequest) (*LobbySessionParameters, error) {

	serviceSettings := ServiceSettings()
	if serviceSettings == nil {
		return nil, fmt.Errorf("service settings not loaded")
	}

	var (
		p              = session.evrPipeline
		userID         = session.userID.String()
		mode           = request.GetMode()
		level          = request.GetLevel()
		versionLock    = request.GetVersionLock()
		appID          = request.GetAppID()
		globalSettings = serviceSettings.Matchmaking
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

	if userSettings.IsMatchLocked() {
		logger.Info("Match lock active - forcing player to follow leader",
			zap.String("user_id", userID),
			zap.String("leader_discord_id", userSettings.MatchLockLeaderDiscordID),
			zap.String("operator_user_id", userSettings.MatchLockOperatorUserID),
			zap.String("reason", userSettings.MatchLockReason))
	}

	joinDirective, err := LoadJoinDirective(ctx, p.nk, userID)
	if err != nil && status.Code(err) != codes.NotFound {
		return nil, fmt.Errorf("failed to load join directive: %w", err)
	}

	if joinDirective != nil {
		logger.Info("Loaded join directive",
			zap.String("match_id", joinDirective.MatchID.String()),
			zap.String("role", joinDirective.Role),
			zap.String("host_discord_id", joinDirective.HostDiscordID))
	}

	if userSettings.IsMatchLocked() {
		if joinDirective == nil {
			joinDirective = &JoinDirective{}
		}
		joinDirective.HostDiscordID = userSettings.MatchLockLeaderDiscordID
	}

	if joinDirective != nil && joinDirective.HostDiscordID != "" {
		hostUserIDStr := p.discordCache.DiscordIDToUserID(joinDirective.HostDiscordID)
		if hostUserID := uuid.FromStringOrNil(hostUserIDStr); !hostUserID.IsNil() {
			presences, _ := p.nk.StreamUserList(StreamModeService, hostUserID.String(), "", StreamLabelMatchService, false, true)
			for _, presence := range presences {
				matchID := MatchIDFromStringOrNil(presence.GetStatus())
				if !matchID.IsNil() {
					joinDirective.MatchID = matchID
				}
			}
		}
	}

	entrantRole := request.GetEntrantRole(0)

	nextMatchID := MatchID{}

	if joinDirective != nil && !joinDirective.MatchID.IsNil() {

		if label, err := MatchLabelByID(ctx, nk, joinDirective.MatchID); err != nil {
			logger.Debug("Next match not found", zap.String("mid", joinDirective.MatchID.String()))
		} else {
			mode = label.Mode

			nextMatchID = joinDirective.MatchID

			resolvedRole := resolveDirectiveRole(entrantRole, joinDirective)
			logger.Info("Resolved join directive role",
				zap.Int("client_role", entrantRole),
				zap.Int("resolved_role", resolvedRole),
				zap.String("directive_role", joinDirective.Role),
				zap.String("match_id", joinDirective.MatchID.String()))
			entrantRole = resolvedRole
		}

		if !userSettings.IsMatchLocked() {
			go func() {
				ctxCleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				if err := DeleteJoinDirective(ctxCleanup, p.nk, userID); err != nil {
					logger.Warn("Failed to delete join directive", zap.Error(err))
				} else {
					logger.Debug("Deleted join directive")
				}
			}()
		}
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

	if userSettings.LobbyGroupName != "" {
		lobbyGroupName = userSettings.LobbyGroupName
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

	// Load the player's total games played for new-player detection.
	gamesPlayed := 0
	if groupID != uuid.Nil {
		gamesPlayed, err = GamesPlayedLoad(ctx, p.nk, userID, groupIDStr, evr.ModeArenaPublic)
		if err != nil {
			logger.Warn("Failed to load games played",
				zap.String("user_id", userID),
				zap.String("group_id", groupIDStr),
				zap.String("mode", evr.ModeArenaPublic.String()),
				zap.Error(err),
			)
		}
	}

	// Determine if user is a moderator (enforcer or operator), independent of division.
	// Computed early because toxic separation exempts moderators.
	isModerator := sessionParams.isGlobalOperator
	if !isModerator && groupID != uuid.Nil {
		if gg, ok := sessionParams.guildGroups[groupID.String()]; ok {
			isModerator = gg.IsEnforcer(userID)
		}
	}

	// Check suspension history for toxic player separation.
	// Enforcers and global operators are exempt — they may have suspension
	// history from admin work, not from being toxic.
	hasSuspensionHistory := false
	if globalSettings.ToxicSeparationEnabled() && globalSettings.NewPlayerMaxGames > 0 && !isModerator {
		journal := NewGuildEnforcementJournal(userID)
		if err := StorableRead(ctx, p.nk, userID, journal, true); err != nil {
			logger.Warn("Failed to load enforcement journal for toxic separation", zap.Error(err))
		} else {
			for _, records := range journal.RecordsByGroupID {
				for _, r := range records {
					if r.IsSuspension() {
						hasSuspensionHistory = true
						break
					}
				}
				if hasSuspensionHistory {
					break
				}
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

	lobbyParams := &LobbySessionParameters{
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
		PartySize:                    atomic.NewInt64(1),
		NextMatchID:                  nextMatchID,
		IsModerator:                  isModerator,
		latencyHistory:               params.latencyHistory,
		unreachableServers:           params.unreachableServers,
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
		GamesPlayed:                  gamesPlayed,
		HasSuspensionHistoryFlag:     hasSuspensionHistory,
	}

	// Assign hard skill division based on mu and games played.
	// Division is always computed (even when hard divisions are disabled) so
	// the label appears on tickets for monitoring.
	lobbyParams.HardDivision = AssignDivision(
		matchmakingRating.Mu,
		gamesPlayed,
		globalSettings.NewPlayerMaxGames,
		globalSettings.DivisionBoundaries,
		globalSettings.DivisionNames,
	)

	// Ambassador program: if the player is an active, eligible ambassador and
	// not on cooldown, override their division to one below and reduce
	// effective mu so they help rather than carry.
	if globalSettings.AmbassadorProgramEnabled() && globalSettings.HardDivisionsEnabled() {
		ambState := NewAmbassadorState()
		if err := StorableRead(ctx, nk, session.UserID().String(), ambState, false); err != nil {
			if !isStorageNotFoundError(err) {
				logger.Warn("Failed to read ambassador state", zap.Error(err))
			}
		} else if ambState.IsActive {
			if IsEligibleAmbassador(gamesPlayed, matchmakingRating.Mu, globalSettings.AmbassadorMinGamesPlayed, globalSettings.AmbassadorMinMu) &&
				ShouldAmbassadorThisMatch(ambState, globalSettings.AmbassadorCooldownMatches) {
				ambDiv := GetAmbassadorDivision(lobbyParams.HardDivision, globalSettings.DivisionNames)
				if ambDiv != "" {
					lobbyParams.HardDivision = ambDiv
					matchmakingRating.Mu = ApplyAmbassadorMuReduction(matchmakingRating.Mu, globalSettings.AmbassadorMuReduction)
					lobbyParams.MatchmakingRating.Store(&matchmakingRating)
					lobbyParams.IsAmbassador = true
					sessionParams.isAmbassadorMatch.Store(true)
				}
			}
		}
	}

	// Check for an existing matchmaking credit to preserve queue position
	// across re-queues (crashes, cancels, party follower failures).
	if isMatchmakingCreditMode(mode) {
		userIDStr := session.UserID().String()
		if credit := getMatchmakingCredit(userIDStr, mode); credit != nil {
			lobbyParams.MatchmakingTimestamp = credit.Timestamp
		} else {
			setMatchmakingCredit(userIDStr, &matchmakingCredit{
				Mode:      mode,
				Timestamp: lobbyParams.MatchmakingTimestamp,
				Expiry:    time.Now().UTC().Add(lobbyParams.MatchmakingTimeout + 2*time.Minute),
			})
		}
	}

	return lobbyParams, nil
}

func (p LobbySessionParameters) String() string {
	data, err := json.Marshal(p)
	if err != nil {
		return ""
	}
	return string(data)
}

func calculateExpandedRatingRange(baseRange float64, matchmakingTimestamp time.Time) float64 {
	if matchmakingTimestamp.IsZero() {
		return baseRange
	}

	waitTime := time.Since(matchmakingTimestamp)
	waitMinutes := waitTime.Minutes()

	expansionPerMinute := 0.5
	maxExpansion := 5.0
	if settings := ServiceSettings(); settings != nil {
		if settings.Matchmaking.RatingRangeExpansionPerMinute > 0 {
			expansionPerMinute = settings.Matchmaking.RatingRangeExpansionPerMinute
		}
		if settings.Matchmaking.MaxRatingRangeExpansion > 0 {
			maxExpansion = settings.Matchmaking.MaxRatingRangeExpansion
		}
	}

	expansion := waitMinutes * expansionPerMinute
	if expansion > maxExpansion {
		expansion = maxExpansion
	}

	return baseRange + expansion
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
	// For arena matches, exclude matches in round_closing (about to end).
	if p.Mode == evr.ModeArenaPublic {
		qparts = append(qparts, fmt.Sprintf(`-label.game_status:%s`, GameStatusRoundClosing.String()))
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
		rng := calculateExpandedRatingRange(p.MatchmakingRatingRange, p.MatchmakingTimestamp)

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
		// Exclude servers that cannot be reached (either unreachable or RTT too high).
		// Only consider latency data from the past week to avoid stale entries.
		unreachableEndpointIDs := make([]string, 0)
		latencyCutoff := time.Now().Add(-7 * 24 * time.Hour)
		lh := p.latencyHistory.Load()
		if lh != nil {
			for ip, rtt := range lh.LatestRTTs(latencyCutoff) {
				if rtt > p.MaxServerRTT {
					unreachableEndpointIDs = append(unreachableEndpointIDs, EncodeEndpointID(ip))
				}
			}
		}

		// Also exclude servers this player has previously failed to connect to.
		if p.unreachableServers != nil {
			if u := p.unreachableServers.Load(); u != nil {
				for ip := range u.UnreachableIPs() {
					unreachableEndpointIDs = append(unreachableEndpointIDs, EncodeEndpointID(ip))
				}
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
	// Use the submission_time from the ticket but clamp it: never allow a timestamp
	// in the future or more than 10 minutes in the past (prevents clients from
	// spoofing old timestamps to expand their matchmaking rating range).
	if ts, err := time.Parse(time.RFC3339, stringProperties["submission_time"]); err == nil {
		now := time.Now().UTC()
		if ts.After(now) {
			ts = now
		} else if now.Sub(ts) > 10*time.Minute {
			ts = now.Add(-10 * time.Minute)
		}
		p.MatchmakingTimestamp = ts
	} else {
		p.MatchmakingTimestamp = time.Now().UTC()
	}
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
		"is_moderator":              strconv.FormatBool(p.IsModerator),
		"division":                  p.HardDivision,
		"is_ambassador":             strconv.FormatBool(p.IsAmbassador),
		"has_suspension_history":    strconv.FormatBool(p.HasSuspensionHistoryFlag),
	}
	var minTeamSize, maxTeamSize float64
	switch p.Mode {
	case evr.ModeCombatPublic:
		minTeamSize = 3
		maxTeamSize = 5
	default:
		minTeamSize = 4
		maxTeamSize = 4
	}

	numericProperties := map[string]float64{
		"timestamp":        float64(p.MatchmakingTimestamp.UTC().Unix()),
		"max_rtt":          float64(p.MaxServerRTT),
		"failsafe_timeout": p.FailsafeTimeout.Seconds(),
		"min_team_size":    minTeamSize,
		"max_team_size":    maxTeamSize,
		"count_multiple":   float64(ticketParams.CountMultiple),
		"max_count":        float64(ticketParams.MaxCount),
		"games_played":     float64(p.GamesPlayed),
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
			rng := calculateExpandedRatingRange(p.MatchmakingRatingRange, p.MatchmakingTimestamp)

			if val != 0.0 {
				lower := val - rng
				upper := val + rng
				numericProperties[key+"_min"] = lower
				numericProperties[key+"_max"] = upper

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

	// Build the set of servers this player cannot reach so they are excluded
	// from the RTT properties (and thus from server selection).
	var unreachableIPs map[string]struct{}
	if p.unreachableServers != nil {
		if u := p.unreachableServers.Load(); u != nil {
			unreachableIPs = u.UnreachableIPs()
		}
	}

	var rttDeltas map[string]int
	if ss := ServiceSettings(); ss != nil {
		rttDeltas = ss.Matchmaking.ServerSelection.RTTDelta
	}
	latencyCutoff := time.Now().Add(-7 * 24 * time.Hour)
	lhAvg := p.latencyHistory.Load()
	if lhAvg != nil {
		for ip, v := range lhAvg.AverageRTTs(true, latencyCutoff) {
			// Skip servers this player has failed to connect to.
			if _, blocked := unreachableIPs[ip]; blocked {
				continue
			}
			rtt := v
			if delta, ok := rttDeltas[ip]; ok {
				rtt += delta
			}
			numericProperties[RTTPropertyPrefix+EncodeEndpointID(ip)] = float64(rtt)
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
