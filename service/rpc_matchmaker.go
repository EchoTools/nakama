package service

import (
	"context"
	"database/sql"
	"encoding/json"

	"github.com/heroiclabs/nakama-common/api"
	"github.com/heroiclabs/nakama-common/runtime"
	"github.com/heroiclabs/nakama/v3/server"
)

type MatchmakingPresence struct {
	UserID     string
	SessionID  string
	Username   string
	Parameters *LobbySessionParameters `json:"parameters"`
}

type MatchmakerStreamRequest struct {
	GroupID string `json:"group_id"`
}

type MatchmakerStreamResponse struct {
	Success   bool                  `json:"success"`
	Presences []MatchmakingPresence `json:"presences"`
}

func (r *MatchmakerStreamResponse) String() string {
	b, _ := json.Marshal(r)
	return string(b)
}

func MatchmakerStreamRPC(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, payload string) (string, error) {

	userID, ok := ctx.Value(runtime.RUNTIME_CTX_USER_ID).(string)
	if !ok {
		return "", runtime.NewError("No user ID in context", StatusUnauthenticated)
	}

	sessionID, ok := ctx.Value(runtime.RUNTIME_CTX_SESSION_ID).(string)
	if !ok {
		return "", runtime.NewError("No session ID in context", StatusUnauthenticated)
	}

	request := &MatchmakerStreamRequest{}
	if err := json.Unmarshal([]byte(payload), request); err != nil {
		return "", err
	}

	success, err := nk.StreamUserJoin(StreamModeMatchmaking, request.GroupID, "", "", userID, sessionID, true, false, "")
	if err != nil {
		return "", err
	}

	if !success {
		return "", runtime.NewError("Failed to join matchmaker stream", StatusInternalError)
	}

	presences, err := nk.StreamUserList(StreamModeMatchmaking, request.GroupID, "", "", false, true)
	if err != nil {
		return "", err
	}

	responsePresences := make([]MatchmakingPresence, 0, len(presences))

	for _, presence := range presences {
		var parameters LobbySessionParameters
		if err := json.Unmarshal([]byte(presence.GetStatus()), &parameters); err != nil {
			return "", err
		}

		responsePresences = append(responsePresences, MatchmakingPresence{
			UserID:     presence.GetUserId(),
			SessionID:  presence.GetSessionId(),
			Username:   presence.GetUsername(),
			Parameters: &parameters,
		})
	}

	response := &MatchmakerStreamResponse{
		Success:   true,
		Presences: responsePresences,
	}

	return response.String(), nil
}

type MatchmakerStateResponse struct {
	Stats *api.MatchmakerStats        `json:"stats"`
	Index []*server.MatchmakerExtract `json:"index"`
}

/*
func MatchmakerStateRPC(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, payload string) (string, error) {

	deps := ctx.Value(PipelineDependenciesKey{}).(*PipelineDependencies)
	globalMatchmaker := deps.GetMatchmaker()
	matchmaker := globalMatchmaker.Load()
	if matchmaker == nil {
		return "", runtime.NewError("server.Matchmaker not initialized", StatusInternalError)
	}

	response := MatchmakerStateResponse{
		Stats: matchmaker.GetStats(),
		Index: matchmaker.Extract(),
	}

	data, err := json.Marshal(response)
	if err != nil {
		return "", err
	}

	return string(data), nil
}
*/

type BuildMatchRequest struct {
	Entries []*server.MatchmakerEntry `json:"entries"`
}

type BuildMatchResponse struct {
	Label json.RawMessage `json:"label"`
}

func (h *RPCHandler) BuildMatchRPC(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, payload string) (string, error) {

	userID, ok := ctx.Value(runtime.RUNTIME_CTX_USER_ID).(string)
	if !ok {
		return "", runtime.NewError("No user ID in context", StatusUnauthenticated)
	}

	logger.WithFields(map[string]any{
		"user_id": userID,
	}).Debug("Creating match via RPC")

	request := &BuildMatchRequest{}
	if err := json.Unmarshal([]byte(payload), request); err != nil {
		return "", err
	}

	lobbyBuilder := NewLobbyBuilder(logger, nk, h.sessionRegistry, h.matchRegistry, h.tracker, h.metrics)

	matchID, err := lobbyBuilder.buildMatch(lobbyBuilder.logger, request.Entries)
	if err != nil {
		return "", err
	}

	match, err := nk.MatchGet(ctx, matchID.String())
	if err != nil {
		return "", err
	}

	response := BuildMatchResponse{
		Label: []byte(match.GetLabel().GetValue()),
	}

	data, err := json.Marshal(response)
	if err != nil {
		return "", err
	}

	return string(data), nil
}
