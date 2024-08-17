package server

import (
	"errors"
	"fmt"

	"github.com/gofrs/uuid/v5"
	"github.com/heroiclabs/nakama/v3/server/evr"
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
		node, ok := sessionCtx.Value(ctxNodeKey{}).(string)
		if !ok {
			return nil, fmt.Errorf("failed to get node from session context")
		}

		evrID, ok := sessionCtx.Value(ctxEvrIDKey{}).(evr.EvrId)
		if !ok {
			return nil, fmt.Errorf("failed to get evrID from session context")
		}

		discordID, ok := sessionCtx.Value(ctxDiscordIDKey{}).(string)
		if !ok {
			return nil, fmt.Errorf("failed to get discordID from session context")
		}

		loginSession, ok := sessionCtx.Value(ctxLoginSessionKey{}).(Session)
		if !ok {
			return nil, fmt.Errorf("failed to get login session from session context")
		}

		isPCVR, ok := sessionCtx.Value(ctxIsPCVRKey{}).(bool)
		if !ok {
			return nil, fmt.Errorf("failed to get headset type from session context")
		}

		metadata, ok := sessionCtx.Value(ctxAccountMetadataKey{}).(AccountMetadata)
		if !ok {
			return nil, fmt.Errorf("failed to get account metadata from session context")
		}

		displayName := metadata.GetGroupDisplayNameOrDefault(groupID.String())

		r := types.Rating{}
		if rating != nil {
			r = *rating
		}

		entrant := &EvrMatchPresence{

			Node:           node,
			UserID:         session.UserID(),
			SessionID:      session.ID(),
			LoginSessionID: loginSession.ID(),
			Username:       session.Username(),
			DisplayName:    displayName,
			EvrID:          evrID,
			PartyID:        partyID,
			RoleAlignment:  role,
			DiscordID:      discordID,
			ClientIP:       session.ClientIP(),
			ClientPort:     session.ClientPort(),
			IsPCVR:         isPCVR,
			Rating:         r,
		}

		entrants = append(entrants, entrant)
	}
	return entrants, nil
}
