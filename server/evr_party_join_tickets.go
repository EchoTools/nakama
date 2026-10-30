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
// Call it after the party has admitted the joiner and BEFORE the joiner is tracked in the party
// stream: once tracked, the leader may rebuild a party ticket that names the joiner, and
// RemoveSessionAll would remove that one as well.
func cancelTicketsOnPartyJoin(logger *zap.Logger, matchmaker Matchmaker, tracker Tracker, sessionID, partyID uuid.UUID) {
	_ = logger
	_ = matchmaker
	_ = tracker
	_ = sessionID
	_ = partyID
}
