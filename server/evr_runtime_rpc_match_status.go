package server

import (
	"context"
	"database/sql"
	"encoding/json"

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
	PublicLabel *MatchLabel `json:"public_label,omitempty"` // Public view without sensitive data
	Error       string      `json:"error,omitempty"`
}

// MatchStatusRPC retrieves the current status and metadata for a match by ID
func MatchStatusRPC(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, payload string) (string, error) {
	// Parse the request
	var request MatchStatusRequest
	if err := json.Unmarshal([]byte(payload), &request); err != nil {
		return "", runtime.NewError("failed to unmarshal request: "+err.Error(), StatusInvalidArgument)
	}

	if request.MatchID == "" {
		return "", runtime.NewError("match_id is required", StatusInvalidArgument)
	}

	// Get match information
	matches, err := nk.MatchList(ctx, 1, true, "", nil, nil, request.MatchID)
	if err != nil {
		logger.Error("Failed to list matches: %v", err)
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

	if len(matches) > 0 {
		match := matches[0]
		response.Found = true

		// Parse the match label
		var matchLabel MatchLabel
		if err := json.Unmarshal([]byte(match.Label.Value), &matchLabel); err != nil {
			logger.Warn("Failed to unmarshal match label: %v", err)
			response.Error = "failed to parse match label"
		} else {
			// Check if the user has permission to see full match details
			userID, _ := ctx.Value(runtime.RUNTIME_CTX_USER_ID).(string)
			hasFullAccess := false

			if userID != "" {
				// User has full access if they are the spawner, owner, or a guild enforcer
				if matchLabel.SpawnedBy == userID || matchLabel.Owner == userID {
					hasFullAccess = true
				} else if matchLabel.GroupID != nil {
					// Check if user is an enforcer in the guild group
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
			// Always provide public view
			response.PublicLabel = matchLabel.PublicView()
		}
	}

	responseBytes, err := json.Marshal(response)
	if err != nil {
		return "", runtime.NewError("failed to marshal response: "+err.Error(), StatusInternalError)
	}

	return string(responseBytes), nil
}
