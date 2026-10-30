package server

import (
	"sync"
	"testing"

	"github.com/gofrs/uuid/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// untrackRecorder records the matchmaking-stream untracks a cancel asks the tracker for.
type untrackRecorder struct {
	testTracker
	mu      sync.Mutex
	untrack []uuid.UUID
}

func (r *untrackRecorder) UntrackLocalByModes(sessionID uuid.UUID, modes map[uint8]struct{}, _ PresenceStream) {
	if _, ok := modes[StreamModeMatchmaking]; !ok {
		return
	}
	r.mu.Lock()
	r.untrack = append(r.untrack, sessionID)
	r.mu.Unlock()
}

func (r *untrackRecorder) untracked() []uuid.UUID {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]uuid.UUID(nil), r.untrack...)
}

func sessionTicketIDs(mm *LocalMatchmaker, sessionID uuid.UUID) []string {
	mm.Lock()
	defer mm.Unlock()
	var out []string
	for ticket := range mm.sessionTickets[sessionID.String()] {
		out = append(out, ticket)
	}
	return out
}

// A player with a live solo ticket joins a party; the leader then queues the party. The player is
// on exactly one ticket, the party's. Before the cancel the player was on two (#1413).
func TestCancelTicketsOnPartyJoin_JoinerIsPlacedOnlyByThePartyTicket(t *testing.T) {
	t.Parallel()

	lg, mm, sids, cleanup := makeTicketRebuildParty(t, 2)
	defer cleanup()
	leaderSID, joinerSID := sids[0], sids[1]
	partyID := uuid.Must(uuid.NewV4())

	soloTicket := submitSoloTicket(t, mm, joinerSID, uuid.Must(uuid.NewV4()))
	require.Equal(t, []string{soloTicket}, sessionTicketIDs(mm, joinerSID), "the joiner starts on its own solo ticket")

	tracker := &untrackRecorder{}
	cancelTicketsOnPartyJoin(loggerForTest(t), mm, tracker, joinerSID, partyID)

	assert.Empty(t, sessionTicketIDs(mm, joinerSID), "the joiner's own ticket is cancelled on joining")
	assert.Equal(t, []uuid.UUID{joinerSID}, tracker.untracked(),
		"the joiner's matchmaking stream is left, so its find loop stops waiting on the cancelled ticket")

	partyTicket := submitLeaderTicket(t, lg, leaderSID)
	assert.Equal(t, []string{partyTicket}, sessionTicketIDs(mm, joinerSID),
		"after the leader queues, the joiner is on the party ticket only: one placement")
}

// The cancel is the joiner's own: another player's ticket and a party ticket built before the
// join (which cannot name the joiner) survive it.
func TestCancelTicketsOnPartyJoin_LeavesOthersTicketsAlone(t *testing.T) {
	t.Parallel()

	lg, mm, sids, cleanup := makeTicketRebuildParty(t, 1)
	defer cleanup()
	leaderSID := sids[0]
	joinerSID := uuid.Must(uuid.NewV4())

	bystanderSID := uuid.Must(uuid.NewV4())
	bystanderTicket := submitSoloTicket(t, mm, bystanderSID, uuid.Must(uuid.NewV4()))
	partyTicket := submitLeaderTicket(t, lg, leaderSID)
	submitSoloTicket(t, mm, joinerSID, uuid.Must(uuid.NewV4()))

	cancelTicketsOnPartyJoin(loggerForTest(t), mm, &untrackRecorder{}, joinerSID, uuid.Must(uuid.NewV4()))

	assert.Empty(t, sessionTicketIDs(mm, joinerSID), "the joiner's own ticket is gone")
	assert.Equal(t, []string{bystanderTicket}, sessionTicketIDs(mm, bystanderSID), "a bystander's ticket is untouched")
	assert.Equal(t, []string{partyTicket}, sessionTicketIDs(mm, leaderSID), "the leader's party ticket is untouched")
}

// A joiner with no ticket is the common case: the cancel must be a quiet no-op.
func TestCancelTicketsOnPartyJoin_NoTicketIsANoOp(t *testing.T) {
	t.Parallel()

	_, mm, sids, cleanup := makeTicketRebuildParty(t, 2)
	defer cleanup()

	require.NotPanics(t, func() {
		cancelTicketsOnPartyJoin(loggerForTest(t), mm, &untrackRecorder{}, sids[1], uuid.Must(uuid.NewV4()))
	})
	assert.Empty(t, sessionTicketIDs(mm, sids[1]))
}
