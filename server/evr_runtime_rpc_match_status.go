package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"

	"github.com/heroiclabs/nakama-common/runtime"
)

// MatchStatusRequest represents the request to get match status
type MatchStatusRequest struct {
	MatchID string `json:"match_id"`
}

// MatchStatusResponse represents the response with match status information
type MatchStatusResponse struct {
	MatchID     string      `json:"match_id"`
	Found       bool        `json:"found"`
	MatchLabel  *MatchLabel `json:"match_label,omitempty"`
	PublicLabel *MatchLabel `json:"public_label,omitempty"`
	Error       string      `json:"error,omitempty"`
}

func MatchStatusRPC(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, payload string) (string, error) {
	var request MatchStatusRequest
	if payload != "" {
		if err := json.Unmarshal([]byte(payload), &request); err != nil {
			return "", runtime.NewError("failed to unmarshal request: "+err.Error(), StatusInvalidArgument)
		}
	}

	if request.MatchID == "" {
		if qp, ok := ctx.Value(runtime.RUNTIME_CTX_QUERY_PARAMS).(map[string][]string); ok {
			if vals, exists := qp["match_id"]; exists && len(vals) > 0 {
				request.MatchID = vals[0]
			}
		}
	}

	if request.MatchID == "" {
		return "", runtime.NewError("match_id is required", StatusInvalidArgument)
	}

	if !strings.Contains(request.MatchID, ".") {
		if node, ok := ctx.Value(runtime.RUNTIME_CTX_NODE).(string); ok && node != "" {
			request.MatchID = request.MatchID + "." + node
		}
	}

	match, err := nk.MatchGet(ctx, request.MatchID)
	if err != nil {
		logger.Error("Failed to get match %s: %v", request.MatchID, err)
		response := MatchStatusResponse{
			MatchID: request.MatchID,
			Found:   false,
			Error:   "failed to query match",
		}
		responseBytes, _ := json.Marshal(response)
		return string(responseBytes), nil
	}

	response := MatchStatusResponse{
		MatchID: request.MatchID,
		Found:   false,
	}

	if match != nil {
		response.Found = true

		var matchLabel MatchLabel
		if err := json.Unmarshal([]byte(match.Label.Value), &matchLabel); err != nil {
			logger.Warn("Failed to unmarshal match label: %v", err)
			response.Error = "failed to parse match label"
		} else {
			userID, _ := ctx.Value(runtime.RUNTIME_CTX_USER_ID).(string)
			hasFullAccess := false

			if userID != "" {
				if matchLabel.SpawnedBy == userID || matchLabel.Owner.String() == userID {
					hasFullAccess = true
				} else if matchLabel.GroupID != nil {
					if gg, err := GuildGroupLoad(ctx, nk, matchLabel.GroupID.String()); err == nil {
						if gg.HasRole(userID, gg.RoleMap.Enforcer) {
							hasFullAccess = true
						}
					}
				}
			}

			if hasFullAccess {
				response.MatchLabel = &matchLabel
			}
			response.PublicLabel = matchLabel.PublicView()
		}
	}

	responseBytes, err := json.Marshal(response)
	if err != nil {
		return "", runtime.NewError("failed to marshal response: "+err.Error(), StatusInternalError)
	}

	return string(responseBytes), nil
}
