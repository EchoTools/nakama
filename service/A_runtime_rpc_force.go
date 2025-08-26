package service

import (
	"context"
	"database/sql"
	"strings"

	"github.com/heroiclabs/nakama-common/runtime"
	"go.uber.org/zap"
)

func CheckForceUserRPC(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, payload string) (string, error) {
	queryParameters := ctx.Value(runtime.RUNTIME_CTX_QUERY_PARAMS).(map[string][]string)

	loginSessionID := ""
	if v, ok := queryParameters["login_session_id"]; !ok {
		return "", runtime.NewError("missing login session id", StatusInvalidArgument)
	} else {
		loginSessionID = strings.ToLower(v[0])
	}

	presences, err := nk.StreamUserList(StreamModeService, loginSessionID, "", StreamLabelLoginService, false, true)
	if err != nil {

		return "", runtime.NewError("failed to get stream presences", StatusInternalError)
	}

	if len(presences) == 0 {
		return "", runtime.NewError("user is not connected", StatusNotFound)
	}

	userID := presences[0].GetUserId()

	// Check if they are part of the force user group
	groups, _, err := nk.GroupsList(ctx, "Force Users", "", nil, nil, 100, "")
	if err != nil {
		logger.Error("Failed to get groups", zap.Error(err))
		return "", runtime.NewError("failed to get groups", StatusInternalError)
	}

	if len(groups) == 0 {
		return "", runtime.NewError("force users group not found", StatusNotFound)
	}

	group := groups[0]

	members, _, err := nk.GroupUsersList(ctx, group.Id, 1000, nil, "")
	if err != nil {
		logger.Error("Failed to get group members", zap.Error(err))
		return "", runtime.NewError("failed to get group members", StatusInternalError)
	}

	for _, member := range members {
		if member.User.Id == userID {
			return "7965874", nil
		}
	}
	return "", runtime.NewError("-1", StatusPermissionDenied)
}
