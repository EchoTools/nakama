package server

// Regression tests for unintended match termination.
//
// Returning nil from MatchLeave / MatchJoin / MatchLoop is not "shut the match
// down" — it makes MatchHandler call mh.Stop() WITHOUT ever calling
// MatchTerminate (see server/match_handler.go: only QueueTerminate reaches
// MatchTerminate). enqueueMatchTerminationTask therefore never runs: no match
// summary, no stored-label cleanup, no game-server notification, no player
// disconnects. The match just evaporates.
//
// So nil must be reserved for "the runtime handed us something that is not a
// MatchLabel". Two classes of site were returning it wrongly:
//
//  1. TRANSIENT FAILURES (a dispatcher.MatchLabelUpdate error from a DB blip, a
//     failed MatchStart dispatch). One flaky call must not end a live match for
//     everyone still in it. Log and keep running.
//
//  2. DELIBERATE SHUTDOWNS ("match is empty", "match did not start on time").
//     These must run the orderly MatchShutdown -> drain -> MatchTerminate
//     sequence so the game server is told and the label is cleaned up.

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/gofrs/uuid/v5"
	"github.com/heroiclabs/nakama-common/runtime"
	"github.com/heroiclabs/nakama/v3/server/evr"
)

var errTerminationTestDispatch = errors.New("simulated transient dispatcher failure")

// labelFailDispatcher fails every MatchLabelUpdate, simulating a transient
// database/label-update blip.
type labelFailDispatcher struct {
	runtime.MatchDispatcher
	calls int
}

func (d *labelFailDispatcher) BroadcastMessage(_ int64, _ []byte, _ []runtime.Presence, _ runtime.Presence, _ bool) error {
	return nil
}

func (d *labelFailDispatcher) BroadcastMessageDeferred(_ int64, _ []byte, _ []runtime.Presence, _ runtime.Presence, _ bool) error {
	return nil
}

func (d *labelFailDispatcher) MatchKick(_ []runtime.Presence) error { return nil }

func (d *labelFailDispatcher) MatchLabelUpdate(_ string) error {
	d.calls++
	return errTerminationTestDispatch
}

// broadcastFailDispatcher fails every outbound broadcast, which makes
// MatchStart's dispatchMessages (and therefore MatchStart itself) fail.
type broadcastFailDispatcher struct {
	runtime.MatchDispatcher
}

func (d *broadcastFailDispatcher) BroadcastMessage(_ int64, _ []byte, _ []runtime.Presence, _ runtime.Presence, _ bool) error {
	return errTerminationTestDispatch
}

func (d *broadcastFailDispatcher) BroadcastMessageDeferred(_ int64, _ []byte, _ []runtime.Presence, _ runtime.Presence, _ bool) error {
	return errTerminationTestDispatch
}

func (d *broadcastFailDispatcher) MatchKick(_ []runtime.Presence) error { return nil }
func (d *broadcastFailDispatcher) MatchLabelUpdate(_ string) error      { return nil }

// TestMatchLeave_LabelUpdateFailureKeepsMatchAlive: one player leaving a
// six-player match must not end the match for the other five just because the
// label update failed.
func TestMatchLeave_LabelUpdateFailureKeepsMatchAlive(t *testing.T) {
	state := reconnectTestState(evr.ModeSocialPublic)
	state.StartTime = time.Now().UTC().Add(-time.Minute)
	state.levelLoaded = true

	stayers := make([]*EvrMatchPresence, 0, 5)
	for i := 0; i < 5; i++ {
		p := reconnectTestPlayer("stay-"+string(rune('a'+i)), evr.TeamBlue)
		state.presenceMap[p.GetSessionId()] = p
		state.presenceByEvrID[p.EvrID] = p
		state.joinTimestamps[p.GetSessionId()] = time.Now().Add(-time.Minute)
		stayers = append(stayers, p)
	}
	leaver := reconnectTestPlayer("label-fail-leaver", evr.TeamOrange)
	state.presenceMap[leaver.GetSessionId()] = leaver
	state.presenceByEvrID[leaver.EvrID] = leaver
	state.joinTimestamps[leaver.GetSessionId()] = time.Now().Add(-time.Minute)
	state.rebuildCache()

	nk := &reconnectTestNakamaModule{}
	dispatcher := &labelFailDispatcher{}
	ctx := context.WithValue(context.Background(), runtime.RUNTIME_CTX_NODE, "test-node")
	m := &EvrMatch{}

	got := m.MatchLeave(ctx, reconnectTestLogger(), nil, nk, dispatcher, 1, state,
		[]runtime.Presence{reconnectTestPresence{EvrMatchPresence: leaver, reason: runtime.PresenceReasonLeave}})

	if got == nil {
		t.Fatal("MatchLeave returned nil after a transient label-update failure: the match handler will Stop() without MatchTerminate, killing the match for the 5 remaining players with no shutdown sequence")
	}
	after, ok := got.(*MatchLabel)
	if !ok {
		t.Fatalf("MatchLeave returned %T, want *MatchLabel", got)
	}
	if dispatcher.calls == 0 {
		t.Fatal("test did not exercise the label-update failure path")
	}
	if _, still := after.presenceMap[leaver.GetSessionId()]; still {
		t.Error("leaver was not removed from the presence map")
	}
	for _, p := range stayers {
		if _, ok := after.presenceMap[p.GetSessionId()]; !ok {
			t.Errorf("remaining player %s was dropped from the presence map", p.GetSessionId())
		}
	}
}

// TestMatchLoop_LabelUpdateFailureKeepsMatchAlive: the tail-of-loop label
// refresh is bookkeeping. A failure there must not end the match.
func TestMatchLoop_LabelUpdateFailureKeepsMatchAlive(t *testing.T) {
	state := reconnectTestState(evr.ModeSocialPublic)
	state.StartTime = time.Now().UTC().Add(-time.Minute)
	state.levelLoaded = true

	p := reconnectTestPlayer("loop-label-fail", evr.TeamBlue)
	state.presenceMap[p.GetSessionId()] = p
	state.presenceByEvrID[p.EvrID] = p
	state.joinTimestamps[p.GetSessionId()] = time.Now()

	state.rebuildCache()

	// An expired slot reservation forces updateLabel = true on this tick.
	// Inserted after rebuildCache, which prunes expired reservations itself.
	expired := reconnectTestPlayer("loop-label-expired", evr.TeamOrange)
	state.reservationMap[expired.GetSessionId()] = &slotReservation{
		Presence: expired,
		Expiry:   time.Now().Add(-time.Minute),
	}

	nk := &reconnectTestNakamaModule{}
	dispatcher := &labelFailDispatcher{}
	m := &EvrMatch{}

	got := m.MatchLoop(context.Background(), reconnectTestLogger(), nil, nk, dispatcher, 1, state, nil)
	if got == nil {
		t.Fatal("MatchLoop returned nil after a transient label-update failure: a live match with players in it is killed with no shutdown sequence")
	}
	if dispatcher.calls == 0 {
		t.Fatal("test did not exercise the label-update failure path")
	}
}

// TestMatchLoop_StartFailureKeepsMatchAlive: a failed MatchStart dispatch is
// retryable. Killing the match strands the players and the game server.
func TestMatchLoop_StartFailureKeepsMatchAlive(t *testing.T) {
	state := reconnectTestState(evr.ModeSocialPublic)
	state.levelLoaded = false

	p := reconnectTestPlayer("start-fail", evr.TeamBlue)
	state.presenceMap[p.GetSessionId()] = p
	state.presenceByEvrID[p.EvrID] = p
	state.joinTimestamps[p.GetSessionId()] = time.Now()
	state.rebuildCache()

	nk := &reconnectTestNakamaModule{}
	dispatcher := &broadcastFailDispatcher{}
	m := &EvrMatch{}

	got := m.MatchLoop(context.Background(), reconnectTestLogger(), nil, nk, dispatcher, 1, state, nil)
	if got == nil {
		t.Fatal("MatchLoop returned nil after a failed MatchStart dispatch: the match is killed with no shutdown sequence instead of retrying next tick")
	}
	if _, ok := got.(*MatchLabel); !ok {
		t.Fatalf("MatchLoop returned %T, want *MatchLabel", got)
	}
}

// TestMatchLeave_EmptyStartedMatchShutsDownOrderly: "match is empty, shut down"
// is a real shutdown decision, so it must run the shutdown sequence rather than
// bare-returning nil (which skips MatchTerminate entirely).
func TestMatchLeave_EmptyStartedMatchShutsDownOrderly(t *testing.T) {
	state := reconnectTestState(evr.ModeSocialPublic)
	state.StartTime = time.Now().UTC().Add(-time.Minute)
	state.levelLoaded = true
	state.rebuildCache() // presenceMap is empty

	ghost := reconnectTestPlayer("empty-started-ghost", evr.TeamBlue)

	nk := &reconnectTestNakamaModule{}
	dispatcher := &reconnectTestDispatcher{}
	ctx := context.WithValue(context.Background(), runtime.RUNTIME_CTX_NODE, "test-node")
	m := &EvrMatch{}

	got := m.MatchLeave(ctx, reconnectTestLogger(), nil, nk, dispatcher, 7, state,
		[]runtime.Presence{reconnectTestPresence{EvrMatchPresence: ghost, reason: runtime.PresenceReasonLeave}})

	if got == nil {
		t.Fatal("MatchLeave bare-returned nil for an empty started match: the handler Stop()s without MatchTerminate, so no summary, no label cleanup and no game-server notification ever happen")
	}
	after, ok := got.(*MatchLabel)
	if !ok {
		t.Fatalf("MatchLeave returned %T, want *MatchLabel", got)
	}
	if after.terminateTick == 0 {
		t.Error("expected the orderly shutdown to arm the drain deadline (terminateTick)")
	}
	if after.Open {
		t.Error("expected the match to be closed to new joins after shutdown")
	}
}

// TestMatchLoop_NeverStartedMatchShutsDownOrderly: same for the 15-minute
// "match did not start on time" reclaim.
func TestMatchLoop_NeverStartedMatchShutsDownOrderly(t *testing.T) {
	state := reconnectTestState(evr.ModeSocialPublic)
	state.CreatedAt = time.Now().UTC().Add(-16 * time.Minute)
	state.levelLoaded = false
	state.rebuildCache() // presenceMap is empty, never started

	nk := &reconnectTestNakamaModule{}
	dispatcher := &reconnectTestDispatcher{}
	m := &EvrMatch{}

	got := m.MatchLoop(context.Background(), reconnectTestLogger(), nil, nk, dispatcher, 1, state, nil)
	if got == nil {
		t.Fatal("MatchLoop bare-returned nil for a match that never started: the handler Stop()s without MatchTerminate, leaking the stored label and stranding the game server")
	}
	after, ok := got.(*MatchLabel)
	if !ok {
		t.Fatalf("MatchLoop returned %T, want *MatchLabel", got)
	}
	if after.terminateTick == 0 {
		t.Error("expected the orderly shutdown to arm the drain deadline (terminateTick)")
	}
	if after.Open {
		t.Error("expected the match to be closed to new joins after shutdown")
	}
}

// TestMatchShutdown_LabelUpdateFailureStillArmsTheDrain: MatchShutdown is the
// entry point of the orderly teardown, and it is now what the "match is empty"
// and "match did not start on time" decisions route through. Bailing out with
// nil when the label update fails defeats the whole point: the handler Stop()s
// without MatchTerminate, so the drain never completes, the label is never
// stored and the game server is never told to kick its players. The label
// refresh is bookkeeping; the teardown must proceed regardless.
func TestMatchShutdown_LabelUpdateFailureStillArmsTheDrain(t *testing.T) {
	state := reconnectTestState(evr.ModeSocialPublic)
	state.StartTime = time.Now().UTC().Add(-time.Minute)
	state.levelLoaded = true

	p := reconnectTestPlayer("shutdown-label-fail", evr.TeamBlue)
	state.presenceMap[p.GetSessionId()] = p
	state.presenceByEvrID[p.EvrID] = p
	state.joinTimestamps[p.GetSessionId()] = time.Now().Add(-time.Minute)
	state.rebuildCache()

	nk := &reconnectTestNakamaModule{}
	dispatcher := &labelFailDispatcher{}
	ctx := context.WithValue(context.Background(), runtime.RUNTIME_CTX_NODE, "test-node")
	m := &EvrMatch{}

	got := m.MatchShutdown(ctx, reconnectTestLogger(), nil, nk, dispatcher, 11, state, 5)
	if got == nil {
		t.Fatal("MatchShutdown returned nil after a transient label-update failure: the handler Stop()s without MatchTerminate, so the drain never runs, the stored label is never cleaned up and the game server is never told to kick its players")
	}
	after, ok := got.(*MatchLabel)
	if !ok {
		t.Fatalf("MatchShutdown returned %T, want *MatchLabel", got)
	}
	if dispatcher.calls == 0 {
		t.Fatal("test did not exercise the label-update failure path")
	}
	if after.terminateTick == 0 {
		t.Error("expected the drain deadline (terminateTick) to be armed despite the label-update failure")
	}
	if after.Open {
		t.Error("expected the match to be closed to new joins")
	}
}

// TestMatchJoin_UnknownPresenceDoesNotKillMatch: MatchJoinAttempt and MatchJoin
// are separately queued on the handler goroutine, so a presence can be evicted
// from the presence map between them (see the same-user duplicate-EvrID eviction
// in MatchJoinAttempt). A miss must skip that one presence, not end the match
// for everybody.
func TestMatchJoin_UnknownPresenceDoesNotKillMatch(t *testing.T) {
	state := reconnectTestState(evr.ModeSocialPublic)
	state.StartTime = time.Now().UTC().Add(-time.Minute)
	state.levelLoaded = true

	known := reconnectTestPlayer("join-known", evr.TeamBlue)
	state.presenceMap[known.GetSessionId()] = known
	state.presenceByEvrID[known.EvrID] = known
	state.joinTimestamps[known.GetSessionId()] = time.Now()
	// Pre-seed participation so the join does not need a database.
	state.participations[known.GetUserId()] = &PlayerParticipation{}
	state.rebuildCache()

	// Evicted between MatchJoinAttempt and MatchJoin: no longer in presenceMap.
	evicted := reconnectTestPlayer("join-evicted", evr.TeamOrange)

	nk := &reconnectTestNakamaModule{}
	dispatcher := &reconnectTestDispatcher{}
	m := &EvrMatch{}

	// The evicted presence comes first so a bare `return nil` aborts before the
	// legitimate join is processed.
	got := m.MatchJoin(context.Background(), reconnectTestLogger(), nil, nk, dispatcher, 1, state, []runtime.Presence{
		reconnectTestPresence{EvrMatchPresence: evicted, reason: runtime.PresenceReasonJoin},
		reconnectTestPresence{EvrMatchPresence: known, reason: runtime.PresenceReasonJoin},
	})

	if got == nil {
		t.Fatal("MatchJoin returned nil for a presence missing from the presence map: the handler Stop()s without MatchTerminate, killing the whole match")
	}
	after, ok := got.(*MatchLabel)
	if !ok {
		t.Fatalf("MatchJoin returned %T, want *MatchLabel", got)
	}
	if _, ok := after.presenceMap[known.GetSessionId()]; !ok {
		t.Error("the legitimate join was dropped")
	}
	if _, ok := after.presenceMap[evicted.GetSessionId()]; ok {
		t.Error("the unknown presence must not be resurrected into the presence map")
	}
}

// TestMatchJoinAttempt_EvictionClearsJoinTimeMilliseconds: the same-user
// duplicate-EvrID eviction removes the stale session from presenceMap,
// presenceByEvrID and joinTimestamps but used to leave joinTimeMilliseconds
// behind, unlike the MatchLeave cleanup. That stale entry is what makes the
// scoreboard attribute the new session's join clock to the dead one.
func TestMatchJoinAttempt_EvictionClearsJoinTimeMilliseconds(t *testing.T) {
	state := reconnectTestState(evr.ModeArenaPublic)

	userID := uuid.NewV5(uuid.Nil, "user-evict-jtms")
	stale := reconnectTestPlayer("evict-jtms-old", evr.TeamBlue)
	stale.UserID = userID
	stale.EvrID = evr.EvrId{PlatformCode: 4, AccountId: 515151}
	state.presenceMap[stale.GetSessionId()] = stale
	state.presenceByEvrID[stale.EvrID] = stale
	state.joinTimestamps[stale.GetSessionId()] = time.Now().Add(-30 * time.Second)
	state.joinTimeMilliseconds[stale.GetSessionId()] = 12345
	state.rebuildCache()

	rejoined := reconnectTestPlayer("evict-jtms-new", evr.TeamBlue)
	rejoined.UserID = userID
	rejoined.EvrID = stale.EvrID
	rejoined.SessionID = uuid.NewV5(uuid.Nil, "session-evict-jtms-new")

	nk := &reconnectTestNakamaModule{}
	m := &EvrMatch{}
	metadata := NewJoinMetadata(rejoined).ToMatchMetadata()

	gotState, allowed, reason := m.MatchJoinAttempt(context.Background(), reconnectTestLogger(), nil, nk, &reconnectTestDispatcher{}, 1, state, rejoined, metadata)
	if !allowed {
		t.Fatalf("expected the same-user duplicate EVR-ID eviction to allow the rejoin, got rejection: %s", reason)
	}
	after := gotState.(*MatchLabel)

	if _, ok := after.joinTimeMilliseconds[stale.GetSessionId()]; ok {
		t.Error("evicted session left a stale joinTimeMilliseconds entry behind")
	}
}

// TestMatchJoin_OrphanPresenceDoesNotLeakJoinTimeMilliseconds closes the other
// half of the eviction cleanup. MatchJoinAttempt evicts the stale session and
// deletes its joinTimeMilliseconds, but the MatchJoin for that same session is
// already queued behind it on the handler goroutine. MatchJoin used to write
// joinTimeMilliseconds BEFORE the presence-map lookup, so the orphan's entry was
// written straight back and then skipped over by the `continue`. Nothing ever
// removes it afterwards: MatchLeave only deletes the key for sessions it finds
// in the presence map, so the entry survives for the life of the match.
func TestMatchJoin_OrphanPresenceDoesNotLeakJoinTimeMilliseconds(t *testing.T) {
	state := reconnectTestState(evr.ModeArenaPublic)
	state.StartTime = time.Now().UTC().Add(-time.Minute)
	state.levelLoaded = true
	// A live scoreboard is what makes MatchJoin record a join clock time at all.
	state.GameState = &GameState{SessionScoreboard: NewSessionScoreboard(RoundDuration, time.Now())}
	state.rebuildCache()

	// The orphan is deliberately NOT in the presence map: MatchJoinAttempt
	// already evicted it.
	orphan := reconnectTestPlayer("join-orphan-jtms", evr.TeamBlue)

	nk := &reconnectTestNakamaModule{}
	m := &EvrMatch{}
	ctx := context.WithValue(context.Background(), runtime.RUNTIME_CTX_NODE, "test-node")

	got := m.MatchJoin(ctx, reconnectTestLogger(), nil, nk, &reconnectTestDispatcher{}, 1, state,
		[]runtime.Presence{reconnectTestPresence{EvrMatchPresence: orphan, reason: runtime.PresenceReasonJoin}})
	if got == nil {
		t.Fatal("MatchJoin returned nil for an orphan presence, killing the match for everyone")
	}
	after, ok := got.(*MatchLabel)
	if !ok {
		t.Fatalf("MatchJoin returned %T, want *MatchLabel", got)
	}

	if v, ok := after.joinTimeMilliseconds[orphan.GetSessionId()]; ok {
		t.Errorf("the skipped orphan session left joinTimeMilliseconds=%d behind; nothing will ever delete it", v)
	}
}

// TestMatchLeave_GameServerLeavingEmptyMatchClearsServer: the normal end-of-life
// sequence is "all the players leave, then the game server disconnects", so the
// game server's own leave arrives with an empty presence map. The empty-match
// branch used to sit above the game-server check and win that race, running the
// shutdown with state.server still pointing at the departed game server: a
// LobbySessionEvent CODE_ENDED dispatched to a presence that has already gone, a
// stored label still advertising the game server, a 5s grace instead of 2s, and
// a MatchTerminate snapshot carrying a live serverSessionID.
func TestMatchLeave_GameServerLeavingEmptyMatchClearsServer(t *testing.T) {
	state := reconnectTestState(evr.ModeSocialPublic)
	state.StartTime = time.Now().UTC().Add(-time.Minute)
	state.levelLoaded = true
	state.rebuildCache() // presenceMap is empty: every player already left

	gameServer := reconnectTestPresence{
		EvrMatchPresence: &EvrMatchPresence{
			Node:      "test-node",
			SessionID: state.GameServer.SessionID,
			UserID:    state.GameServer.OperatorID,
			Username:  "broadcaster:gameserver",
		},
		reason: runtime.PresenceReasonDisconnect,
	}

	nk := &reconnectTestNakamaModule{}
	dispatcher := &drainTestDispatcher{}
	ctx := context.WithValue(context.Background(), runtime.RUNTIME_CTX_NODE, "test-node")
	m := &EvrMatch{}

	const leaveTick = int64(3)
	got := m.MatchLeave(ctx, reconnectTestLogger(), nil, nk, dispatcher, leaveTick, state,
		[]runtime.Presence{gameServer})
	if got == nil {
		t.Fatal("MatchLeave returned nil when the game server left an empty match")
	}
	after, ok := got.(*MatchLabel)
	if !ok {
		t.Fatalf("MatchLeave returned %T, want *MatchLabel", got)
	}

	if after.server != nil {
		t.Error("state.server still points at the departed game server; the shutdown will dispatch to a presence that has already left")
	}
	if wantGrace := 2 * state.tickRate; after.terminateTick-leaveTick != wantGrace {
		t.Errorf("drain grace is %d ticks, want %d (2s): the game-server-left path uses a 2s grace, the empty-match path 5s",
			after.terminateTick-leaveTick, wantGrace)
	}
	if dispatcher.broadcastCount() != 0 {
		t.Errorf("shutdown broadcast %d message(s) to the departed game server presence", dispatcher.broadcastCount())
	}
	if after.Open {
		t.Error("expected the match to be closed to new joins after shutdown")
	}
}
