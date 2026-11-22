package server

import (
	"testing"
	"time"

	"github.com/gofrs/uuid/v5"
	"github.com/heroiclabs/nakama-common/runtime"
	"github.com/heroiclabs/nakama/v3/server/evr"
	"github.com/stretchr/testify/require"
	"go.uber.org/atomic"
)

// expireJoinMarkerForTest accepts session into match and then runs the match handler's sweep past the
// join deadline, so the sweep removes the marker as an expired join.
func expireJoinMarkerForTest(t *testing.T, match, session uuid.UUID) {
	t.Helper()
	mh := &MatchHandler{
		logger: loggerForTest(t),
		ID:     match,
		Core:   stubLoopCore{},
		IDStr:  match.String() + ".testnode",
		Rate:   10,
		JoinMarkerList: &MatchJoinMarkerList{
			expiryDelayMs: 15000,
			tickRate:      10,
			joinMarkers:   map[uuid.UUID]*MatchJoinMarker{},
		},
		PresenceList: NewMatchPresenceList(),
		callCh:       make(chan func(*MatchHandler), 4),
		stopCh:       make(chan struct{}),
		stopped:      atomic.NewBool(false),
		state:        struct{}{},
	}
	presence := &MatchPresence{Node: "testnode", UserID: uuid.Must(uuid.NewV4()), SessionID: session,
		Username: "u", Reason: runtime.PresenceReasonUnknown}
	mh.JoinMarkerList.Add(presence, 0, uuid.Must(uuid.NewV4()).String())
	// Sweeps run on ticks that are a multiple of Rate*30; 300 is one, and the marker expired at tick 150.
	mh.tick = 300
	loop(mh)
}

// expiredJoinBackfillMatch is an open arena match with players already in it, and a team with open slots.
func expiredJoinBackfillMatch(id uuid.UUID, players int, now time.Time) *BackfillMatch {
	return &BackfillMatch{
		Label: &MatchLabel{ID: MatchID{UUID: id, Node: "testnode"}, Mode: evr.ModeArenaPublic, Open: true,
			GameServer: &GameServerPresence{}, StartTime: now, PlayerCount: players, PlayerLimit: 8},
		OpenSlots:  map[int]int{evr.TeamBlue: 2, evr.TeamOrange: 2},
		TeamCounts: map[int]int{evr.TeamBlue: 1, evr.TeamOrange: 1},
	}
}

// expiredJoinCandidate is a ticket whose members are the given sessions, prepared the way a backfill
// cycle prepares it (sessions resolved, no blacklist).
func expiredJoinCandidate(sessions ...uuid.UUID) *preparedBackfillCandidate {
	c := &BackfillCandidate{Mode: evr.ModeArenaPublic, TeamAlignment: evr.TeamUnassigned, MaxRTT: 100}
	for _, sid := range sessions {
		c.Entries = append(c.Entries, &MatchmakerEntry{Presence: &MatchmakerPresence{SessionId: sid.String(), UserId: uuid.Must(uuid.NewV4()).String()}})
	}
	return &preparedBackfillCandidate{BackfillCandidate: c, partySize: len(sessions)}
}

// A session whose join marker expired in a match is not offered that match by backfill, and is still
// offered the other match. The expired match is the better one here, so only the exclusion can change
// the answer.
func TestBackfillSkipsMatchAJoinExpiredFrom(t *testing.T) {
	expired, other := uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4())
	session := uuid.Must(uuid.NewV4())
	expireJoinMarkerForTest(t, expired, session)

	now := time.Now()
	b := &PostMatchmakerBackfill{}
	bctx := &backfillContext{now: now}
	matches := b.prepareMatches([]*BackfillMatch{expiredJoinBackfillMatch(expired, 6, now), expiredJoinBackfillMatch(other, 2, now)}, bctx)

	result := b.findBestBackfillMatchOptimized(expiredJoinCandidate(session), matches, bctx)
	require.NotNil(t, result)
	require.Equal(t, other, result.Match.Label.ID.UUID, "the session is not offered the match its join expired from")
}

// The exclusion lasts five minutes from the expiry and no longer: inside the window the expired match is
// not offered even when it is the only one, and after it the match is offered again.
func TestBackfillOffersExpiredMatchAgainAfterTheWindow(t *testing.T) {
	expired := uuid.Must(uuid.NewV4())
	session := uuid.Must(uuid.NewV4())
	expireJoinMarkerForTest(t, expired, session)
	b := &PostMatchmakerBackfill{}

	inside := time.Now().Add(5*time.Minute - time.Second)
	bctx := &backfillContext{now: inside}
	matches := b.prepareMatches([]*BackfillMatch{expiredJoinBackfillMatch(expired, 6, inside)}, bctx)
	require.Nil(t, b.findBestBackfillMatchOptimized(expiredJoinCandidate(session), matches, bctx),
		"inside the five-minute window the only match is not offered")

	after := time.Now().Add(5*time.Minute + time.Second)
	bctx = &backfillContext{now: after}
	matches = b.prepareMatches([]*BackfillMatch{expiredJoinBackfillMatch(expired, 6, after)}, bctx)
	result := b.findBestBackfillMatchOptimized(expiredJoinCandidate(session), matches, bctx)
	require.NotNil(t, result, "after the window the same match is offered again")
	require.Equal(t, expired, result.Match.Label.ID.UUID)
}

// A party is placed whole, so one member whose join expired from a match keeps the whole party out of it.
func TestBackfillKeepsPartyOutOfMatchWhenAMemberExpiredFromIt(t *testing.T) {
	expired, other := uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4())
	member, mate := uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4())
	expireJoinMarkerForTest(t, expired, member)

	now := time.Now()
	b := &PostMatchmakerBackfill{}
	bctx := &backfillContext{now: now}
	matches := b.prepareMatches([]*BackfillMatch{expiredJoinBackfillMatch(expired, 6, now), expiredJoinBackfillMatch(other, 2, now)}, bctx)

	result := b.findBestBackfillMatchOptimized(expiredJoinCandidate(member, mate), matches, bctx)
	require.NotNil(t, result)
	require.Equal(t, other, result.Match.Label.ID.UUID, "the party is not placed into the match its member's join expired from")
}

// The exclusion is per session and per match: a session that did not expire from the match is still
// offered it.
func TestBackfillOffersMatchToSessionThatDidNotExpireFromIt(t *testing.T) {
	expired := uuid.Must(uuid.NewV4())
	expiredSession, fresh := uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4())
	expireJoinMarkerForTest(t, expired, expiredSession)

	now := time.Now()
	b := &PostMatchmakerBackfill{}
	bctx := &backfillContext{now: now}
	matches := b.prepareMatches([]*BackfillMatch{expiredJoinBackfillMatch(expired, 6, now)}, bctx)

	result := b.findBestBackfillMatchOptimized(expiredJoinCandidate(fresh), matches, bctx)
	require.NotNil(t, result)
	require.Equal(t, expired, result.Match.Label.ID.UUID)
}

// Recording an expiry drops every window that has already ended, so the store holds only open windows
// as of the latest expiry and cannot grow without bound.
func TestRememberExpiredJoinPrunesEndedWindows(t *testing.T) {
	expiredJoins.Lock()
	saved := expiredJoins.until
	expiredJoins.until = map[expiredJoinKey]time.Time{}
	expiredJoins.Unlock()
	t.Cleanup(func() {
		expiredJoins.Lock()
		expiredJoins.until = saved
		expiredJoins.Unlock()
	})

	t0 := time.Now()
	ended := expiredJoinKey{uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4())}
	open := expiredJoinKey{uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4())}
	rememberExpiredJoin(ended.match, ended.session, t0)
	rememberExpiredJoin(open.match, open.session, t0.Add(expiredJoinBackoff/2))
	latest := expiredJoinKey{uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4())}
	rememberExpiredJoin(latest.match, latest.session, t0.Add(expiredJoinBackoff))

	expiredJoins.Lock()
	defer expiredJoins.Unlock()
	require.NotContains(t, expiredJoins.until, ended, "a window that ended is dropped when the next expiry is recorded")
	require.Contains(t, expiredJoins.until, open, "a window still open is kept")
	require.Contains(t, expiredJoins.until, latest)
	require.Len(t, expiredJoins.until, 2)
}
