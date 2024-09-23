package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"strings"
	"time"

	"github.com/gofrs/uuid/v5"
	"github.com/heroiclabs/nakama-common/api"
	"github.com/heroiclabs/nakama/v3/server/evr"
	"github.com/intinig/go-openskill/types"
	"go.uber.org/zap"
)

type (
	ctxLobbyParametersKey struct{}
)

type LobbySessionParameters struct {
	Node                  string       `json:"node"`
	UserID                uuid.UUID    `json:"user_id"`
	SessionID             uuid.UUID    `json:"session_id"`
	VersionLock           evr.Symbol   `json:"version_lock"`
	AppID                 evr.Symbol   `json:"app_id"`
	GroupID               uuid.UUID    `json:"group_id"`
	Region                evr.Symbol   `json:"region"`
	Mode                  evr.Symbol   `json:"mode"`
	Level                 evr.Symbol   `json:"level"`
	SupportedFeatures     []string     `json:"supported_features"`
	RequiredFeatures      []string     `json:"required_features"`
	CurrentMatchID        MatchID      `json:"current_match_id"`
	Role                  int          `json:"role"`
	PartyID               uuid.UUID    `json:"party_id"`
	PartyGroupName        string       `json:"party_group_name"`
	PartySize             int          `json:"party_size"`
	NextMatchID           MatchID      `json:"next_match_id"`
	DisableArenaBackfill  bool         `json:"disable_arena_backfill"`
	BackfillQueryAddon    string       `json:"backfill_query_addon"`
	MatchmakingQueryAddon string       `json:"matchmaking_query_addon"`
	CreateQueryAddon      string       `json:"create_query_addon"`
	Verbose               bool         `json:"verbose"`
	BlockedIDs            []string     `json:"blocked_ids"`
	Rating                types.Rating `json:"rating"`
	latencyHistory        LatencyHistory
}

func (s LobbySessionParameters) MetricsTags() map[string]string {
	return map[string]string{
		"mode":         s.Mode.String(),
		"level":        s.Level.String(),
		"region":       s.Region.String(),
		"version_lock": s.VersionLock.String(),
		"group_id":     s.GroupID.String(),
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

		// Avoid players that are blocking this player.

		// Avoid backfilling matches with players that this player blocks.
		backfillQueryAddons = append(backfillQueryAddons, fmt.Sprintf(`-label.players.user_id:/(%s)/`, Query.Join(blockedIDs, "|")))
	}

	return &LobbySessionParameters{
		Node:                  node,
		UserID:                session.userID,
		SessionID:             session.id,
		CurrentMatchID:        currentMatchID,
		VersionLock:           versionLock,
		AppID:                 appID,
		GroupID:               groupID,
		Region:                region,
		Mode:                  mode,
		Level:                 level,
		SupportedFeatures:     supportedFeatures,
		RequiredFeatures:      requiredFeatures,
		Role:                  r.GetEntrantRole(0),
		DisableArenaBackfill:  globalSettings.DisableArenaBackfill || userSettings.DisableArenaBackfill,
		BackfillQueryAddon:    strings.Join(backfillQueryAddons, " "),
		MatchmakingQueryAddon: strings.Join(matchmakingQueryAddons, " "),
		CreateQueryAddon:      strings.Join(createQueryAddons, " "),
		PartyGroupName:        lobbyGroupName,
		PartyID:               uuid.NewV5(uuid.Nil, lobbyGroupName),
		PartySize:             1,
		NextMatchID:           userSettings.NextMatchID,
		latencyHistory:        latencyHistory,
		BlockedIDs:            blockedIDs,
		Rating:                rating,
		Verbose:               sessionParams.AccountMetadata.DiscordDebugMessages,
	}, nil
}

func (p LobbySessionParameters) String() string {
	data, _ := json.Marshal(p)
	return string(data)
}

func (p *LobbySessionParameters) MatchmakingParameters(sessionParams *SessionParameters, lobbyParams *LobbySessionParameters) (string, map[string]string, map[string]float64) {
	averageRTTs := AverageLatencyHistories(p.latencyHistory)

	displayName := sessionParams.AccountMetadata.GetGroupDisplayNameOrDefault(lobbyParams.GroupID.String())

	stringProperties := map[string]string{
		"mode":         p.Mode.String(),
		"group_id":     p.GroupID.String(),
		"version_lock": p.VersionLock.String(),
		"blocked":      strings.Join(p.BlockedIDs, " "),
		"display_name": displayName,
	}

	numericProperties := map[string]float64{
		"rating_mu":    p.Rating.Mu,
		"rating_sigma": p.Rating.Sigma,
		"rating_z":     float64(p.Rating.Z),
	}

	qparts := []string{
		"+properties.mode:" + p.Mode.String(),
		fmt.Sprintf("+properties.group_id:/%s/", Query.Escape(p.GroupID.String())),
		fmt.Sprintf(`-properties.blocked:/.*%s.*/`, Query.Escape(p.UserID)),
		//"+properties.version_lock:" + p.VersionLock.String(),
		p.MatchmakingQueryAddon,
	}

	maxDelta := 60 // milliseconds

	for k, v := range averageRTTs {
		numericProperties[k] = float64(v)
		qparts = append(qparts, fmt.Sprintf("properties.%s:<=%d", k, v+maxDelta))
	}

	query := strings.Join(qparts, " ")

	return query, stringProperties, numericProperties
}

func (p LobbySessionParameters) GroupStream() PresenceStream {
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
