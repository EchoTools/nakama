package server

import (
	"context"
	"fmt"

	rtapi "buf.build/gen/go/echotools/nevr-api/protocolbuffers/go/gameservice/v1"
	"github.com/gofrs/uuid/v5"
	"github.com/heroiclabs/nakama/v3/server/evr"
	"go.uber.org/zap"
)

func (p *EvrPipeline) lobbyEntrantConnected(logger *zap.Logger, session *sessionWS, in *rtapi.Envelope) error {
	message := in.GetLobbyEntrantConnected()
	if message == nil {
		return fmt.Errorf("envelope missing LobbyEntrantConnected payload")
	}

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
	matchIDStr := matchID.String()

	// Fetch the match label once before the entrant loop — matchID is the
	// same for all entrants so there's no reason to call MatchLabelByID per-entrant.
	matchLabel, matchLabelErr := MatchLabelByID(session.Context(), p.nk, matchID)
	if matchLabelErr != nil {
		baseLogger.Warn("Failed to get match label for guild group stream", zap.Error(matchLabelErr))
	}

	for _, entrantID := range message.EntrantIds {
		logger := baseLogger.With(zap.String("entrant_id", entrantID))
		presence, err := PresenceByEntrantID(p.nk, matchID, uuid.FromStringOrNil(entrantID))
		if err != nil || presence == nil {
			logger.Warn("Failed to get player presence by entrant ID", zap.Error(err))
			rejectedIDs = append(rejectedIDs, entrantID)
			continue
		}

		logger = logger.With(zap.String("entrant_uid", presence.GetUserId()))

		s := p.nk.sessionRegistry.Get(uuid.FromStringOrNil(presence.GetSessionId()))
		if s == nil {
			logger.Warn("Failed to get session by ID")
			rejectedIDs = append(rejectedIDs, entrantID)
			continue
		}

		ctx := s.Context()

		// Update service streams — this is the authoritative "player is in this match" state.
		// All 4 subjects: SessionID, LoginSessionID, UserID, EvrID.
		for _, subject := range [...]uuid.UUID{presence.SessionID, presence.LoginSessionID, presence.UserID, presence.EvrID.UUID()} {
			if !session.tracker.Update(ctx, s.ID(), PresenceStream{Mode: StreamModeService, Subject: subject, Label: StreamLabelMatchService}, s.UserID(), PresenceMeta{Format: s.Format(), Hidden: false, Status: matchIDStr}) {
				logger.Warn("Failed to update service stream for entrant",
					zap.String("subject", subject.String()),
					zap.String("entrant_uid", presence.GetUserId()))
			}
		}

		// Update guild group stream and leave matchmaking streams.
		if matchLabel != nil {
			guildGroupStream := PresenceStream{Mode: StreamModeGuildGroup, Subject: matchLabel.GetGroupID(), Label: matchLabel.Mode.String()}
			session.tracker.Update(ctx, s.ID(), guildGroupStream, s.UserID(), PresenceMeta{Format: s.Format(), Username: s.Username(), Hidden: false, Status: matchIDStr})
			session.tracker.UntrackLocalByModes(s.ID(), map[uint8]struct{}{StreamModeMatchmaking: {}, StreamModeGuildGroup: {}}, guildGroupStream)
		}

		// Trigger the MatchJoin event.
		presenceStream := PresenceStream{Mode: StreamModeMatchAuthoritative, Subject: matchID.UUID, Label: matchID.Node}
		presenceMeta := PresenceMeta{
			Username: s.Username(),
			Format:   s.Format(),
			Status:   presence.GetStatus(),
		}
		if success, _ := p.nk.tracker.Track(ctx, s.ID(), presenceStream, s.UserID(), presenceMeta); success {
			// Kick the user from any other matches they may be part of.
			// WARNING This cannot be used during transition. It will kick the player from their current match.
			//p.tracker.UntrackLocalByModes(session.ID(), matchStreamModes, stream)
		}

		// Observer: player connected to game server (non-social matches only).
		// Social lobbies already transition to StateSocialReady in LobbyJoinEntrants.
		if ws, ok := s.(*sessionWS); ok {
			if lc := getMatchLifecycle(ws); lc != nil && lc.State() == StateJoining {
				lc.TransitionTo(StateInMatch, "joined match", WithMatchID(matchIDStr))
			}
		}

		acceptedIDs = append(acceptedIDs, entrantID)

		// Deliberately NO party-reservation creation here. Slot reservations are
		// Nakama-internal capacity accounting (state.reservationMap / OpenSlots,
		// consumed in MatchJoinAttempt) and must be created ATOMICALLY at the
		// join, capacity-gated, not on a game-server connect event. The leader's
		// party is reserved synchronously in MatchJoinAttempt via the entrants[1:]
		// path (appendPartyReservationPlaceholders, evr_lobby_find.go:271 ->
		// LobbyJoinEntrants, evr_lobby_joinentrant.go:79-80). Late joiners are
		// handled by createReservationForNewPartyMember (evr_pipeline_party.go).
		// Firing reservation creation here (as this handler once did) deferred a
		// capacity decision to an async round-trip and raced other players into
		// the seats, tripping ReservationViolated (evr_match.go:451) in prod
		// (v3.27.2-evr.319). Do not re-add it.
	}

	messages := make([]evr.Message, 0, 4)

	// Send protobuf messages first
	if len(acceptedIDs) > 0 {
		envelope := &rtapi.Envelope{
			Message: &rtapi.Envelope_LobbyEntrantsAccept{
				LobbyEntrantsAccept: &rtapi.LobbyEntrantsAcceptMessage{
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
	if len(rejectedIDs) > 0 {
		envelope := &rtapi.Envelope{
			Message: &rtapi.Envelope_LobbyEntrantReject{
				LobbyEntrantReject: &rtapi.LobbyEntrantsRejectMessage{
					EntrantIds: rejectedIDs,
					Code:       int32(rtapi.LobbyEntrantsRejectMessage_CODE_BAD_REQUEST),
				},
			},
		}

		message, err := evr.NewNEVRProtobufMessageV1(envelope)
		if err != nil {
			return fmt.Errorf("failed to create NEVRProtobufMessageV1: %w", err)
		}
		messages = append(messages, message)
	}
	// Legacy support - send alongside protobuf messages for backwards compatibility.
	if len(acceptedIDs) > 0 {
		uuids := make([]uuid.UUID, 0, len(acceptedIDs))
		for _, id := range acceptedIDs {
			uuids = append(uuids, uuid.FromStringOrNil(id))
		}
		messages = append(messages, evr.NewGameServerJoinAllowed(uuids...))
	}
	if len(rejectedIDs) > 0 {
		uuids := make([]uuid.UUID, 0, len(rejectedIDs))
		for _, id := range rejectedIDs {
			uuids = append(uuids, uuid.FromStringOrNil(id))
		}
		messages = append(messages, evr.NewGameServerEntrantRejected(evr.PlayerRejectionReasonBadRequest, uuids...))
	}
	return session.SendEvr(messages...)
}

// createPartyReservations creates slot reservations in the given match for
// all party members who are not already in the match. Called from
// lobbyEntrantConnected when the connected entrant is the party leader.
//
// IMPORTANT: This function is dispatched as a goroutine. The caller MUST
// pass context.WithoutCancel(s.Context()) so that reservation creation
// completes even if the triggering player session disconnects. Using the
// raw session context would cancel the goroutine mid-creation if the
// player disconnects, leaving some members with reservations and others
// without.
func (p *EvrPipeline) createPartyReservations(ctx context.Context, logger *zap.Logger, matchID MatchID, leaderSessionID uuid.UUID, partyID uuid.UUID) {
	// Verify the party still exists and the leader is still a member.
	// The partyID was captured at lobbyEntrantConnected time and may be
	// stale if the player left the party between then and now.
	ph, ok := p.nk.partyRegistry.Get(partyID)
	if !ok {
		logger.Debug("Party no longer exists, skipping reservation creation",
			zap.String("party_id", partyID.String()))
		return
	}
	leaderStillMember := false
	for _, member := range ph.members.List() {
		if member.PresenceID != nil && member.PresenceID.SessionID == leaderSessionID {
			leaderStillMember = true
			break
		}
	}
	if !leaderStillMember {
		logger.Debug("Leader no longer in party, skipping reservation creation",
			zap.String("party_id", partyID.String()),
			zap.String("leader_sid", leaderSessionID.String()))
		return
	}

	// List party members via the party stream.
	stream := PresenceStream{Mode: StreamModeParty, Subject: partyID, Label: p.node}
	presences := p.nk.tracker.ListByStream(stream, true, true)

	members := make([]*EvrMatchPresence, 0, len(presences))
	for _, pp := range presences {
		if pp.ID.SessionID == leaderSessionID {
			continue // skip the leader
		}
		memberSession := p.nk.sessionRegistry.Get(pp.ID.SessionID)
		if memberSession == nil {
			continue // skip dead sessions (BAC-013)
		}

		member := &EvrMatchPresence{
			SessionID:     pp.ID.SessionID,
			UserID:        pp.UserID,
			Username:      memberSession.Username(),
			PartyID:       partyID,
			RoleAlignment: evr.TeamSocial,
			Node:          p.node,
		}

		// Load additional fields from session params if available.
		if memberParams, ok := LoadParams(memberSession.Context()); ok {
			member.EvrID = memberParams.xpID
			member.DisplayName = memberParams.profile.DisplayName()
		}

		members = append(members, member)
	}

	if len(members) == 0 {
		return
	}

	payload := SignalCreatePartyReservationsPayload{Members: members}
	if _, err := SignalMatch(ctx, p.nk, matchID, SignalCreatePartyReservations, payload); err != nil {
		logger.Warn("Failed to signal match for party reservations",
			zap.Error(err),
			zap.String("match_id", matchID.String()),
			zap.String("party_id", partyID.String()))
		return
	}

	logger.Info("Created party reservations",
		zap.Int("members", len(members)),
		zap.String("match_id", matchID.String()),
		zap.String("party_id", partyID.String()))
}

func (p *EvrPipeline) lobbyEntrantsRemove(logger *zap.Logger, session *sessionWS, in *rtapi.Envelope) error {
	message := in.GetLobbyEntrantRemoved()
	if message == nil {
		return fmt.Errorf("envelope missing LobbyEntrantRemoved payload")
	}
	matchID, _ := NewMatchID(uuid.FromStringOrNil(message.LobbySessionId), p.node)
	presence, err := PresenceByEntrantID(p.nk, matchID, uuid.FromStringOrNil(message.EntrantId))
	if err != nil {
		if err != ErrEntrantNotFound {
			logger.Warn("Failed to get player session by ID", zap.Error(err))
		}
	} else if presence != nil {
		// Leave the match stream first so MatchLeave fires while the entrant stream is still present.
		if err := p.nk.StreamUserLeave(StreamModeMatchAuthoritative, matchID.UUID.String(), "", matchID.Node, presence.GetUserId(), presence.GetSessionId()); err != nil {
			logger.Warn("Failed to leave match stream", zap.Error(err))
		}
		// Then leave the entrant stream.
		if err := p.nk.StreamUserLeave(StreamModeEntrant, message.EntrantId, "", matchID.Node, presence.GetUserId(), presence.GetSessionId()); err != nil {
			logger.Warn("Failed to leave entrant session stream", zap.Error(err))
		}
	}
	return nil
}

func (p *EvrPipeline) lobbySessionEvent(logger *zap.Logger, session *sessionWS, in *rtapi.Envelope) error {
	message := in.GetLobbySessionEvent()
	if message == nil {
		return fmt.Errorf("envelope missing LobbySessionEvent payload")
	}
	matchID, _ := NewMatchID(uuid.FromStringOrNil(message.LobbySessionId), p.node)
	var opcode SignalOpCode
	switch rtapi.LobbySessionEventMessage_Code(message.Code) {
	case rtapi.LobbySessionEventMessage_CODE_LOCKED:
		opcode = SignalLockSession
	case rtapi.LobbySessionEventMessage_CODE_UNLOCKED:
		opcode = SignalUnlockSession
	case rtapi.LobbySessionEventMessage_CODE_STARTED:
		opcode = SignalStartedSession
	case rtapi.LobbySessionEventMessage_CODE_ENDED:
		opcode = SignalEndedSession
	default:
		return fmt.Errorf("unknown lobby session event code: %d", message.Code)
	}
	// Signal the session event to the match.
	signal := NewSignalEnvelope(session.userID.String(), opcode, nil)
	if _, err := p.nk.matchRegistry.Signal(context.Background(), matchID.String(), signal.String()); err != nil {
		logger.Warn("Failed to signal match", zap.Error(err))
	}
	return nil
}
