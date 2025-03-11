package server

import (
	"context"
	"database/sql"
	"encoding/json"

	"github.com/heroiclabs/nakama-common/runtime"
)

type MatchmakingPresence struct {
	UserID     string                  `json:"user_id"`
	SessionID  string                  `json:"session_id"`
	Username   string                  `json:"username"`
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

func MatchmakerStateRPC(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, payload string) (string, error) {

	matchmaker := globalMatchmaker.Load()
	if matchmaker == nil {
		return "", runtime.NewError("Matchmaker not initialized", StatusInternalError)
	}

	stats := matchmaker.GetStats()
	extracted := matchmaker.Extract()

	response := map[string]interface{}{
		"stats": stats,
		"index": extracted,
	}

	data, err := json.Marshal(response)
	if err != nil {
		return "", err
	}

	return string(data), nil
}
