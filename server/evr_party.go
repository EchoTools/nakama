package server

import (
	"errors"

	"github.com/gofrs/uuid/v5"
	"github.com/intinig/go-openskill/types"
	"go.uber.org/zap"
)

var (
	ErrNoParty                    = errors.New("no party")
	ErrNoEntrants                 = errors.New("no entrants")
	ErrLeaderAndFollowerSameMatch = errors.New("leader and follower are in the same match")
	ErrLeaderNotInMatch           = errors.New("leader is not in a match")
	ErrLeaderMatchNotPublic       = errors.New("leader's match is not public")
	ErrLeaderMatchNotOpen         = errors.New("leader's match is not open")
	ErrFollowerIsLeader           = errors.New("follower is the leader")
	ErrFollowerNotInMatch         = errors.New("follower is not in a match")
	ErrUnknownError               = errors.New("unknown error")
	ErrJoinFailed                 = errors.New("join failed")
)

func EntrantPresencesFromSessionIDs(logger *zap.Logger, sessionRegistry SessionRegistry, partyID, groupID uuid.UUID, rating types.Rating, role int, sessionIDs ...uuid.UUID) ([]*EvrMatchPresence, error) {
	entrants := make([]*EvrMatchPresence, 0, len(sessionIDs))
	for _, sessionID := range sessionIDs {
		session := sessionRegistry.Get(sessionID)
		if session == nil {
			logger.Warn("Session not found", zap.String("sid", sessionID.String()))
			continue
		}

		sessionCtx := session.Context()
		params, ok := LoadParams(sessionCtx)
		if !ok {
			logger.Warn("Session parameters not found", zap.String("sid", sessionID.String()))
		}

		displayName := params.AccountMetadata.GetGroupDisplayNameOrDefault(groupID.String())

		if rating.Mu == 0 || rating.Sigma == 0 || rating.Z == 0 {
			rating = NewDefaultRating()
		}

		entrant := &EvrMatchPresence{
			Node:              params.Node,
			UserID:            session.UserID(),
			SessionID:         session.ID(),
			LoginSessionID:    params.LoginSession.ID(),
			Username:          session.Username(),
			DisplayName:       displayName,
			EvrID:             params.EvrID,
			PartyID:           partyID,
			RoleAlignment:     role,
			DiscordID:         params.DiscordID,
			ClientIP:          session.ClientIP(),
			ClientPort:        session.ClientPort(),
			IsPCVR:            params.IsPCVR,
			Rating:            rating,
			DisableEncryption: params.DisableEncryption,
			DisableMAC:        params.DisableMAC,
		}

		entrants = append(entrants, entrant)
	}

	if len(entrants) == 0 {
		return nil, ErrNoEntrants
	}

	return entrants, nil
}
