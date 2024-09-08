package server

import (
	"context"
	"fmt"
	"strings"

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
	Node                  string             `json:"node"`
	VersionLock           evr.Symbol         `json:"version_lock"`
	AppID                 evr.Symbol         `json:"app_id"`
	GroupID               uuid.UUID          `json:"group_id"`
	Region                evr.Symbol         `json:"region"`
	Mode                  evr.Symbol         `json:"mode"`
	Level                 evr.Symbol         `json:"level"`
	SupportedFeatures     []string           `json:"supported_features"`
	RequiredFeatures      []string           `json:"required_features"`
	CurrentMatchID        MatchID            `json:"current_match_id"`
	Role                  int                `json:"role"`
	PartyID               uuid.UUID          `json:"party_id"`
	PartyGroupName        string             `json:"party_group_name"`
	PartySize             int                `json:"party_size"`
	NextMatchID           MatchID            `json:"next_match_id"`
	StringProperties      map[string]string  `json:"string_properties"`
	NumericProperties     map[string]float64 `json:"numeric_properties"`
	DisableArenaBackfill  bool               `json:"disable_arena_backfill"`
	BackfillQueryAddon    string             `json:"backfill_query_addon"`
	MatchmakingQueryAddon string             `json:"matchmaking_query_addon"`
	CreateQueryAddon      string             `json:"create_query_addon"`
	Verbose               bool               `json:"verbose"`
	Friends               []*api.Friend      `json:"friends"`
	Rating                *types.Rating      `json:"rating"`
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

	// Load the global matchmaking config
	globalSettings, err := LoadMatchmakingSettings(ctx, p.runtimeModule, SystemUserID)
	if err != nil {
		return nil, fmt.Errorf("failed to load global matchmaking settings: %v", err)
	}

	// Load the user's matchmaking config
	userSettings, err := LoadMatchmakingSettings(ctx, p.runtimeModule, session.UserID().String())
	if err != nil {
		return nil, fmt.Errorf("failed to load user matchmaking settings: %v", err)
	}
	// Load friends to get blocked (ghosted) players
	cursor := ""
	friends := make([]*api.Friend, 0)
	for {
		users, err := ListFriends(ctx, logger, p.db, p.statusRegistry, session.userID, 100, nil, cursor)
		if err != nil {
			return nil, fmt.Errorf("failed to list friends: %v", err)
		}

		friends = append(friends, users.Friends...)

		cursor = users.Cursor
		if users.Cursor == "" {
			break
		}
	}

	sessionParams, ok := LoadParams(ctx)
	if !ok {
		return nil, fmt.Errorf("failed to load session parameters")
	}

	latencyHistory, err := LoadLatencyHistory(ctx, logger, p.db, session.userID)
	if err != nil {
		return nil, fmt.Errorf("failed to load latency history: %v", err)
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

	userID := session.userID.String()

	// Add blocked players who are online to the Matchmaking Query Addon
	stringProperties := make(map[string]string)

	blockedIDs := make([]string, 0)
	for _, f := range friends {
		if api.Friend_State(f.GetState().Value) == api.Friend_BLOCKED {
			if f.GetUser().GetOnline() {
				blockedIDs = append(blockedIDs, f.GetUser().GetId())
			}
		}
	}

	matchmakingQueryAddons := []string{
		globalSettings.MatchmakingQueryAddon,
		userSettings.MatchmakingQueryAddon,
	}

	backfillQueryAddons := []string{
		globalSettings.BackfillQueryAddon,
		userSettings.BackfillQueryAddon,
	}

	// Add each blocked user that is online to the backfill query addon
	if len(blockedIDs) > 0 {

		// Avoid players that are blocking this player.
		stringProperties["blocked"] = strings.Join(blockedIDs, " ")
		matchmakingQueryAddons = append(matchmakingQueryAddons, fmt.Sprintf(`-properties.blocked:/.*%s.*/`, Query.Escape(userID)))

		// Avoid backfilling matches with players that this player blocks.
		backfillQueryAddons = append(backfillQueryAddons, fmt.Sprintf(`-label.players.user_id:/(%s)/`, Query.Join(blockedIDs, "|")))
	}

	return &LobbySessionParameters{
		CurrentMatchID:        currentMatchID,
		VersionLock:           r.GetVersionLock(),
		AppID:                 r.GetAppID(),
		GroupID:               groupID,
		Region:                region,
		Mode:                  r.GetMode(),
		Level:                 r.GetLevel(),
		SupportedFeatures:     supportedFeatures,
		RequiredFeatures:      requiredFeatures,
		Role:                  r.GetEntrantRole(0),
		DisableArenaBackfill:  globalSettings.DisableArenaBackfill || userSettings.DisableArenaBackfill,
		BackfillQueryAddon:    strings.Join(backfillQueryAddons, " "),
		MatchmakingQueryAddon: strings.Join(matchmakingQueryAddons, " "),
		CreateQueryAddon:      globalSettings.CreateQueryAddon + " " + userSettings.CreateQueryAddon,
		PartyGroupName:        lobbyGroupName,
		PartyID:               uuid.NewV5(uuid.Nil, lobbyGroupName),
		NextMatchID:           userSettings.NextMatchID,
		Verbose:               sessionParams.AccountMetadata.DiscordDebugMessages,
		Node:                  node,
		latencyHistory:        latencyHistory,
	}, nil
}
