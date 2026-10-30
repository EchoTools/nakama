package server

import (
	"github.com/gofrs/uuid/v5"
	"go.uber.org/zap"
)

// cancelTicketsOnPartyJoin ends a joiner's own matchmaking when a party admits it. From then on
// the leader's ticket speaks for the party, so a ticket the joiner queued alone is a second
// placement: both can match, and the player is sent to two matches (#1413, 2006wlw 04:57Z, the
// matchmade join followed 7.6 s later by the party-follow join).
//
// Call it after the party has admitted the joiner and before snsPartyTrackAndJoin, so the window
// in which a leader ticket could already name the joiner is as short as the call order allows.
// RemoveSessionAll removes every ticket the session is on, a party ticket naming the joiner
// included, and a ticket the leader built inside that window would go with it; the leader's ticket
// loop rebuilds on the joiner's next find (cancelTicketForLateArrival), the same path a late
// arrival already takes.
func cancelTicketsOnPartyJoin(logger *zap.Logger, matchmaker Matchmaker, tracker Tracker, sessionID, partyID uuid.UUID) {
	// Leave the matchmaking stream first, as lobbyPendingSessionCancel does: the joiner's find loop
	// watches that presence and stops waiting on the ticket when it goes.
	tracker.UntrackLocalByModes(sessionID, map[uint8]struct{}{StreamModeMatchmaking: {}}, PresenceStream{})
	if err := matchmaker.RemoveSessionAll(sessionID.String()); err != nil {
		logger.Warn("Failed to cancel the joiner's matchmaking tickets on party join",
			zap.String("sid", sessionID.String()), zap.String("party_id", partyID.String()), zap.Error(err))
		return
	}
	logger.Info("Cancelled the joiner's own matchmaking on party join",
		zap.String("sid", sessionID.String()), zap.String("party_id", partyID.String()))
}
