package server

import (
	"context"
	"fmt"
	"strings"

	"github.com/gofrs/uuid/v5"
	"github.com/heroiclabs/nakama-common/api"
	"github.com/heroiclabs/nakama/v3/server/evr"
)

type (
	ctxLobbyParametersKey struct{}
)

type SessionParameters struct {
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
	PartyGroupID          string             `json:"party_group_id"`
	PartySize             int                `json:"party_size"`
	NextMatchID           MatchID            `json:"next_match_id"`
	DisableArenaBackfill  bool               `json:"disable_arena_backfill"`
	BackfillQueryAddon    string             `json:"backfill_query_addon"`
	StringProperties      map[string]string  `json:"string_properties"`
	NumericProperties     map[string]float64 `json:"numeric_properties"`
	MatchmakingQueryAddon string             `json:"matchmaking_query_addon"`
	CreateQueryAddon      string             `json:"create_query_addon"`
	Verbose               bool               `json:"verbose"`
	Friends               []*api.Friend      `json:"friends"`
	latencyHistory        LatencyHistory
}

func (s SessionParameters) MetricsTags() map[string]string {
	return map[string]string{
		"mode":         s.Mode.String(),
		"level":        s.Level.String(),
		"region":       s.Region.String(),
		"version_lock": s.VersionLock.String(),
		"group_id":     s.GroupID.String(),
	}
}

func NewLobbyParametersFromRequest(ctx context.Context, r evr.LobbySessionRequest, globalSettings *MatchmakingSettings, userSettings *MatchmakingSettings, profile *GameProfileData, latencyHistory LatencyHistory, friends []*api.Friend) SessionParameters {
	if userSettings == nil {
		userSettings = &MatchmakingSettings{}
	}

	if globalSettings == nil {
		globalSettings = &MatchmakingSettings{}
	}

	var lobbyGroupID string
	if userSettings != nil {
		lobbyGroupID = userSettings.LobbyGroupID
	}
	if lobbyGroupID == "" {
		lobbyGroupID = uuid.Must(uuid.NewV4()).String()
	}

	node := ctx.Value(ctxNodeKey{}).(string)
	requiredFeatures, ok := ctx.Value(ctxRequiredFeaturesKey{}).([]string)
	if !ok {
		requiredFeatures = make([]string, 0)
	}

	supportedFeatures, ok := ctx.Value(ctxSupportedFeaturesKey{}).([]string)
	if !ok {
		supportedFeatures = make([]string, 0)
	}

	if r.GetFeatures() != nil {
		supportedFeatures = append(supportedFeatures, r.GetFeatures()...)
	}

	metadata := ctx.Value(ctxAccountMetadataKey{}).(AccountMetadata)
	groupID := r.GetGroupID()
	if r.GetGroupID() == uuid.Nil {
		groupID = metadata.GetActiveGroupID()
	}

	region := r.GetRegion()
	if region == evr.UnspecifiedRegion {
		region = evr.DefaultRegion
	}

	currentMatchID := MatchID{}
	if r.GetCurrentLobbyID() != uuid.Nil {
		currentMatchID = MatchID{UUID: r.GetCurrentLobbyID(), Node: node}
	}

	userID, ok := ctx.Value(ctxUserIDKey{}).(uuid.UUID)
	if !ok {
		userID = uuid.Nil
	}

	if globalSettings == nil {
		globalSettings = &MatchmakingSettings{}
	}

	if userSettings == nil {
		userSettings = &MatchmakingSettings{}
	}

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

	return SessionParameters{
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
		PartyGroupID:          lobbyGroupID,
		PartyID:               uuid.NewV5(uuid.Nil, lobbyGroupID),
		NextMatchID:           userSettings.NextMatchID,
		Verbose:               metadata.DiscordDebugMessages,
		Node:                  node,
		latencyHistory:        latencyHistory,
	}
}
