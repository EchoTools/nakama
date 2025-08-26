package service

import (
	"context"
	"fmt"

	evr "github.com/echotools/nakama/v3/protocol"
	"github.com/echotools/nevr-common/v3/rtapi"
	"github.com/gofrs/uuid/v5"
	"github.com/heroiclabs/nakama/v3/server"
	"go.uber.org/zap"
)

func (p *EvrPipeline) lobbyEntrantConnected(logger *zap.Logger, session *sessionEVR, in *rtapi.Envelope) error {
	message := in.GetLobbyEntraConnected()

	baseLogger := logger.With(zap.String("mid", message.LobbySessionId))

	acceptedIDs := make([]string, 0, len(message.EntrantIds))
	rejectedIDs := make([]string, 0)

	lobbyUUID := uuid.FromStringOrNil(message.LobbySessionId)
	if lobbyUUID == uuid.Nil {
		baseLogger.Warn("Lobby ID is invalid")
		for _, entrantID := range message.EntrantIds {
			rejectedIDs = append(rejectedIDs, entrantID)
		}
	}

	matchID, _ := NewMatchID(lobbyUUID, p.node)

	for _, entrantID := range message.EntrantIds {
		logger := baseLogger.With(zap.String("entrant_id", entrantID))
		presence, err := PresenceByEntrantID(p.nk, matchID, uuid.FromStringOrNil(entrantID))
		if err != nil || presence == nil {
			logger.Warn("Failed to get player presence by entrant ID", zap.Error(err))
			rejectedIDs = append(rejectedIDs, entrantID)
			continue
		}

		logger = logger.With(zap.String("entrant_uid", presence.GetUserId()))

		s := p.sessionRegistry.Get(uuid.FromStringOrNil(presence.GetSessionId()))
		if s == nil {
			logger.Warn("Failed to get session by ID")
			rejectedIDs = append(rejectedIDs, entrantID)
			continue
		}

		ctx := s.Context()
		// Update tracker with the entrant's presence.
		for _, subject := range [...]uuid.UUID{presence.SessionID, presence.UserID, presence.XPID.UUID()} {
			session.tracker.Update(ctx, s.ID(), server.PresenceStream{Mode: StreamModeService, Subject: subject, Label: StreamLabelMatchService}, s.UserID(), server.PresenceMeta{Format: s.Format(), Hidden: false, Status: matchID.String()})
		}

		// Trigger the MatchJoin event.
		presenceStream := server.PresenceStream{Mode: server.StreamModeMatchAuthoritative, Subject: matchID.UUID, Label: matchID.Node}
		presenceMeta := server.PresenceMeta{
			Username: s.Username(),
			Format:   s.Format(),
			Status:   presence.GetStatus(),
		}
		if success, _ := p.tracker.Track(ctx, s.ID(), presenceStream, s.UserID(), presenceMeta); success {
			// Kick the user from any other matches they may be part of.
			// WARNING This cannot be used during transition. It will kick the player from their current match.
			//p.tracker.UntrackLocalByModes(session.ID(), matchStreamModes, stream)
		}

		acceptedIDs = append(acceptedIDs, entrantID)
	}

	messages := make([]evr.Message, 0, 2)
	if len(acceptedIDs) > 0 {
		envelope := &rtapi.Envelope{
			Message: &rtapi.Envelope_LobbyEntrantAccept{
				LobbyEntrantAccept: &rtapi.LobbyEntrantsAcceptMessage{
					EntrantIds: acceptedIDs,
				},
			},
		}
		message, err := evr.NewNEVRProtobufMessageV1(envelope)
		if err != nil {
			return fmt.Errorf("failed to create NEVRProtobufMessageV1: %w", err)
		}
		messages = append(messages, message)
	}
	if len(rejectedIDs) == 0 {
		envelope := &rtapi.Envelope{
			Message: &rtapi.Envelope_LobbyEntrantReject{
				LobbyEntrantReject: &rtapi.LobbyEntrantsRejectMessage{
					EntrantIds: rejectedIDs,
					Code:       int32(rtapi.LobbyEntrantsRejectMessage_BAD_REQUEST),
				},
			},
		}

		message, err := evr.NewNEVRProtobufMessageV1(envelope)
		if err != nil {
			return fmt.Errorf("failed to create NEVRProtobufMessageV1: %w", err)
		}
		messages = append(messages, message)
	}

	// Legacy support
	if len(acceptedIDs) > 0 {
		uuids := make([]uuid.UUID, 0, len(acceptedIDs))
		for _, id := range acceptedIDs {
			uuids = append(uuids, uuid.FromStringOrNil(id))
		}

		messages = append(messages,
			evr.NewGameServerJoinAllowed(uuids...), // Legacy message for backwards compatibility.
		)
	}
	if len(rejectedIDs) > 0 {
		uuids := make([]uuid.UUID, 0, len(acceptedIDs))
		for _, id := range acceptedIDs {
			uuids = append(uuids, uuid.FromStringOrNil(id))
		}
		messages = append(messages,
			evr.NewGameServerEntrantRejected(evr.PlayerRejectionReasonBadRequest, uuids...), // Legacy message for backwards compatibility.
		)
	}
	// End Legacy Support

	return session.SendEVR(Envelope{
		ServiceType: ServiceTypeServer,
		Messages:    messages,
		State:       RequireStateUnrequired,
	})
}

func (p *EvrPipeline) lobbyEntrantRemoved(logger *zap.Logger, session *sessionEVR, in *rtapi.Envelope) error {
	message := in.GetLobbyEntrantRemove()
	matchID, _ := NewMatchID(uuid.FromStringOrNil(message.LobbySessionId), p.node)
	presence, err := PresenceByEntrantID(p.nk, matchID, uuid.FromStringOrNil(message.EntrantId))
	if err != nil {
		if err != ErrEntrantNotFound {
			logger.Warn("Failed to get player session by ID", zap.Error(err))
		}
	} else if presence != nil {
		// Leave the entrant stream first
		if err := p.nk.StreamUserLeave(StreamModeEntrant, message.EntrantId, "", matchID.Node, presence.GetUserId(), presence.GetSessionId()); err != nil {
			logger.Warn("Failed to leave entrant session stream", zap.Error(err))
		}
		// Trigger MatchLeave.
		if err := p.nk.StreamUserLeave(server.StreamModeMatchAuthoritative, matchID.UUID.String(), "", matchID.Node, presence.GetUserId(), presence.GetSessionId()); err != nil {
			logger.Warn("Failed to leave match stream", zap.Error(err))
		}
	}
	return nil
}

func (p *EvrPipeline) lobbySessionEvent(logger *zap.Logger, session *sessionEVR, in *rtapi.Envelope) error {
	message := in.GetLobbySessionEvent()
	matchID, _ := NewMatchID(uuid.FromStringOrNil(message.LobbySessionId), p.node)
	var opcode SignalOpCode
	switch rtapi.LobbySessionEventMessage_Code(message.Code) {
	case rtapi.LobbySessionEventMessage_LOCKED:
		opcode = SignalLockSession
	case rtapi.LobbySessionEventMessage_UNLOCKED:
		opcode = SignalUnlockSession
	case rtapi.LobbySessionEventMessage_STARTED:
		opcode = SignalStartedSession
	case rtapi.LobbySessionEventMessage_ENDED:
		opcode = SignalEndedSession
	default:
		return fmt.Errorf("unknown lobby session event code: %d", message.Code)
	}
	// Signal the session event to the match.
	signal := NewSignalEnvelope(session.userID.String(), opcode, nil)
	if _, err := p.matchRegistry.Signal(context.Background(), matchID.String(), signal.String()); err != nil {
		logger.Warn("Failed to signal match", zap.Error(err))
	}
	return nil
}
