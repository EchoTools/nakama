package server

import (
	"errors"

	"github.com/gofrs/uuid/v5"
	"github.com/intinig/go-openskill/types"
	"go.uber.org/zap"
)

var (
	ErrNoParty                    = errors.New("no party")
	ErrLeaderAndFollowerSameMatch = errors.New("leader and follower are in the same match")
	ErrLeaderNotInMatch           = errors.New("leader is not in a match")
	ErrLeaderMatchNotPublic       = errors.New("leader's match is not public")
	ErrLeaderMatchNotOpen         = errors.New("leader's match is not open")
	ErrFollowerIsLeader           = errors.New("follower is the leader")
	ErrFollowerNotInMatch         = errors.New("follower is not in a match")
	ErrUnknownError               = errors.New("unknown error")
	ErrJoinFailed                 = errors.New("join failed")
)

func EntrantPresencesFromSessionIDs(logger *zap.Logger, sessionRegistry SessionRegistry, partyID, groupID uuid.UUID, rating *types.Rating, role int, sessionIDs ...uuid.UUID) ([]*EvrMatchPresence, error) {
	entrants := make([]*EvrMatchPresence, 0, len(sessionIDs))
	for _, sessionID := range sessionIDs {
		session := sessionRegistry.Get(sessionID)
		if session == nil {
			logger.Warn("Session not found", zap.String("sid", sessionID.String()))
			continue
		}

		sessionCtx := session.Context()
		params := sessionCtx.Value(ctxSessionParametersKey{}).(*SessionParameters)

		displayName := params.AccountMetadata().GetGroupDisplayNameOrDefault(groupID.String())

		r := types.Rating{}
		if rating != nil {
			r = *rating
		}

		entrant := &EvrMatchPresence{

			Node:           params.node,
			UserID:         session.UserID(),
			SessionID:      session.ID(),
			LoginSessionID: params.LoginSession().ID(),
			Username:       session.Username(),
			DisplayName:    displayName,
			EvrID:          params.EvrID(),
			PartyID:        partyID,
			RoleAlignment:  role,
			DiscordID:      params.DiscordID(),
			ClientIP:       session.ClientIP(),
			ClientPort:     session.ClientPort(),
			IsPCVR:         params.IsPCVR(),
			Rating:         r,
		}

		entrants = append(entrants, entrant)
	}
	return entrants, nil
}
