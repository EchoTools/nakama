package server

import (
	"context"

	"github.com/gofrs/uuid/v5"
	"github.com/heroiclabs/nakama/v3/server/evr"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type (
	ctxLobbyParametersKey struct{}
)

type SessionParameters struct {
	Node                  string     `json:"node"`
	VersionLock           evr.Symbol `json:"version_lock"`
	AppID                 evr.Symbol `json:"app_id"`
	GroupID               uuid.UUID  `json:"group_id"`
	Region                evr.Symbol `json:"region"`
	Mode                  evr.Symbol `json:"mode"`
	Level                 evr.Symbol `json:"level"`
	SupportedFeatures     []string   `json:"supported_features"`
	RequiredFeatures      []string   `json:"required_features"`
	CurrentMatchID        MatchID    `json:"current_match_id"`
	Role                  int        `json:"role"`
	PartyID               uuid.UUID  `json:"party_id"`
	PartyGroupID          string     `json:"party_group_id"`
	PartySize             int        `json:"party_size"`
	NextMatchID           MatchID    `json:"next_match_id"`
	DisableArenaBackfill  bool       `json:"disable_arena_backfill"`
	BackfillQueryAddon    string     `json:"backfill_query_addon"`
	MatchmakingQueryAddon string     `json:"matchmaking_query_addon"`
	CreateQueryAddon      string     `json:"create_query_addon"`
	Verbose               bool       `json:"verbose"`
	latencyHistory        LatencyHistory
}

func (s SessionParameters) ResponseFromError(err error) *evr.LobbySessionFailurev4 {

	respCode := evr.LobbySessionFailure_InternalError
	message := err.Error()
	if s, ok := status.FromError(err); ok {
		switch s.Code() {
		case codes.DeadlineExceeded:
			respCode = evr.LobbySessionFailure_Timeout0
		case codes.InvalidArgument:
			respCode = evr.LobbySessionFailure_BadRequest
		case codes.Aborted:
			respCode = evr.LobbySessionFailure_BadRequest
		case codes.ResourceExhausted:
			respCode = evr.LobbySessionFailure_ServerIsFull
		case codes.Unavailable:
			respCode = evr.LobbySessionFailure_ServerFindFailed
		case codes.NotFound:
			respCode = evr.LobbySessionFailure_ServerDoesNotExist
		case codes.PermissionDenied, codes.Unauthenticated:
			respCode = evr.LobbySessionFailure_KickedFromLobbyGroup
		case codes.FailedPrecondition:
			respCode = evr.LobbySessionFailure_ServerIsIncompatible
		}
	}
	return evr.NewLobbySessionFailure(s.Mode, s.GroupID, respCode, message).Version4()
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

func NewLobbyParametersFromRequest(ctx context.Context, r evr.LobbySessionRequest, gconfig MatchmakingSettings, config MatchmakingSettings, latencyHistory LatencyHistory) SessionParameters {
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
	lobbyGroupID := config.LobbyGroupID
	if lobbyGroupID == "" {
		lobbyGroupID = uuid.Must(uuid.NewV4()).String()
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
		DisableArenaBackfill:  gconfig.DisableArenaBackfill || config.DisableArenaBackfill,
		BackfillQueryAddon:    gconfig.BackfillQueryAddon + " " + config.BackfillQueryAddon,
		MatchmakingQueryAddon: gconfig.MatchmakingQueryAddon + " " + config.MatchmakingQueryAddon,
		CreateQueryAddon:      gconfig.CreateQueryAddon + " " + config.CreateQueryAddon,
		PartyGroupID:          lobbyGroupID,
		PartyID:               uuid.NewV5(uuid.Nil, lobbyGroupID),
		NextMatchID:           config.NextMatchID,
		Verbose:               gconfig.Verbose || config.Verbose,
		Node:                  node,
		latencyHistory:        latencyHistory,
	}
}
