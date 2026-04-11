# Party Reservation Session-ID Fix

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Fix party followers being unable to join their leader's social lobby after reconnecting, because slot reservations are keyed by session ID which changes on reconnect.

**Architecture:** Add a `LoadAndDeleteReservationByUserID` fallback to the match label's reservation system, and move the reservation lookup **before** the `OpenSlots()` gate in `MatchJoinAttempt`. Currently, reservations are counted in `Size` (via `rebuildCache`), so when the lobby is at capacity, `OpenSlots()` returns 0 and rejects the join at line 404 — before the reservation lookup at line 416 ever runs. The fix: look up and consume the reservation first, then check slots. When a reservation is consumed, the slot it held becomes available.

**Tech Stack:** Go, nakama server, existing test infrastructure in `evr_match_test.go`

---

## File Map

| File                          | Action | Responsibility                                                           |
| ----------------------------- | ------ | ------------------------------------------------------------------------ |
| `server/evr_match_label.go`   | Modify | Add `LoadAndDeleteReservationByUserID` method                            |
| `server/evr_match.go:398-422` | Modify | Move reservation lookup before `OpenSlots()` check, add user-ID fallback |
| `server/evr_match_test.go`    | Modify | Add tests exposing the bug and verifying the fix                         |

---

### Task 1: Write failing test — reservation lookup misses after session change

**Files:**

- Modify: `server/evr_match_test.go`

- [ ] **Step 1: Add the `newTestMatchLabel` helper and failing test**

Add this helper and test after the existing `TestReconnectReservation_*` block (~line 1900):

```go
func newTestMatchLabel() *MatchLabel {
	state := &MatchLabel{
		CreatedAt:             time.Now(),
		GameServer:            &GameServerPresence{SessionID: uuid.Must(uuid.NewV4())},
		Open:                  true,
		RequiredFeatures:      make([]string, 0),
		Players:               make([]PlayerInfo, 0, SocialLobbyMaxSize),
		presenceMap:           make(map[string]*EvrMatchPresence, SocialLobbyMaxSize),
		reservationMap:        make(map[string]*slotReservation, 2),
		reconnectReservations: make(map[string]*reconnectReservation),
		presenceByEvrID:       make(map[evr.EvrId]*EvrMatchPresence, SocialLobbyMaxSize),
		goals:                 make([]*evr.MatchGoal, 0),
		TeamAlignments:        make(map[string]int, SocialLobbyMaxSize),
		joinTimestamps:        make(map[string]time.Time, SocialLobbyMaxSize),
		joinTimeMilliseconds:  make(map[string]int64, SocialLobbyMaxSize),
		participations:        make(map[string]*PlayerParticipation),
		emptyTicks:            0,
		tickRate:              10,
	}
	state.rebuildCache()
	return state
}

// TestSlotReservation_FollowerReconnectNewSessionID verifies that a party
// follower who reconnects (getting a new session ID) can still claim the
// slot reservation that was created under their original session ID.
func TestSlotReservation_FollowerReconnectNewSessionID(t *testing.T) {
	state := newTestMatchLabel()
	state.Mode = evr.ModeSocialPublic
	state.MaxSize = SocialLobbyMaxSize
	state.PlayerLimit = SocialLobbyMaxSize

	followerUserID := uuid.Must(uuid.NewV4())
	originalSessionID := uuid.Must(uuid.NewV4())
	newSessionID := uuid.Must(uuid.NewV4())

	state.reservationMap[originalSessionID.String()] = &slotReservation{
		Presence: &EvrMatchPresence{
			UserID:        followerUserID,
			SessionID:     originalSessionID,
			RoleAlignment: evr.TeamSocial,
			PartyID:       uuid.Must(uuid.NewV4()),
		},
		Expiry: time.Now().Add(5 * time.Minute),
	}
	state.rebuildCache()

	// Session-ID lookup should miss (different session).
	_, found := state.LoadAndDeleteReservation(newSessionID.String())
	if found {
		t.Fatal("LoadAndDeleteReservation should miss for new session ID")
	}

	// User-ID fallback should find it.
	presence, found := state.LoadAndDeleteReservationByUserID(followerUserID.String())
	if !found {
		t.Fatal("LoadAndDeleteReservationByUserID should find the reservation by user ID")
	}
	if presence.UserID != followerUserID {
		t.Errorf("Expected user ID %s, got %s", followerUserID, presence.UserID)
	}

	// Verify consumed.
	if _, stillThere := state.reservationMap[originalSessionID.String()]; stillThere {
		t.Error("Reservation should have been deleted")
	}
}

// TestSlotReservation_ExpiredReservationNotReturnedByUserID verifies that
// LoadAndDeleteReservationByUserID does not return expired reservations.
func TestSlotReservation_ExpiredReservationNotReturnedByUserID(t *testing.T) {
	state := newTestMatchLabel()
	state.Mode = evr.ModeSocialPublic
	state.MaxSize = SocialLobbyMaxSize
	state.PlayerLimit = SocialLobbyMaxSize

	followerUserID := uuid.Must(uuid.NewV4())
	originalSessionID := uuid.Must(uuid.NewV4())

	state.reservationMap[originalSessionID.String()] = &slotReservation{
		Presence: &EvrMatchPresence{
			UserID:        followerUserID,
			SessionID:     originalSessionID,
			RoleAlignment: evr.TeamSocial,
		},
		Expiry: time.Now().Add(-1 * time.Minute),
	}
	state.rebuildCache()

	_, found := state.LoadAndDeleteReservationByUserID(followerUserID.String())
	if found {
		t.Fatal("Expired reservation should not be returned")
	}
	if _, stillThere := state.reservationMap[originalSessionID.String()]; stillThere {
		t.Error("Expired reservation should have been cleaned up")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd /home/andrew/src/nakama && go test ./server/ -run "TestSlotReservation_" -v -count=1`

Expected: FAIL — `LoadAndDeleteReservationByUserID` does not exist yet.

---

### Task 2: Implement `LoadAndDeleteReservationByUserID`

**Files:**

- Modify: `server/evr_match_label.go` (after line 92)

- [ ] **Step 1: Add the new method**

Add after the existing `LoadAndDeleteReservation` method:

```go
// LoadAndDeleteReservationByUserID searches the reservation map for a reservation
// matching the given user ID. This is a fallback for when the session ID has changed
// (e.g., party follower disconnected and reconnected with a new session).
// Expired reservations are cleaned up during the scan.
func (s *MatchLabel) LoadAndDeleteReservationByUserID(userID string) (*EvrMatchPresence, bool) {
	for sessionID, r := range s.reservationMap {
		if r.Presence.GetUserId() != userID {
			continue
		}
		if r.Expiry.Before(time.Now()) {
			delete(s.reservationMap, sessionID)
			continue
		}
		delete(s.reservationMap, sessionID)
		s.rebuildCache()
		return r.Presence, true
	}
	return nil, false
}
```

- [ ] **Step 2: Run unit tests**

Run: `cd /home/andrew/src/nakama && go test ./server/ -run "TestSlotReservation_" -v -count=1`

Expected: Both PASS.

- [ ] **Step 3: Commit**

```bash
git add server/evr_match_label.go server/evr_match_test.go
git commit -m "feat: add LoadAndDeleteReservationByUserID for reconnect fallback

Add unit tests exposing the bug: slot reservations keyed by session ID
are unreachable after a follower reconnects with a new session.

Co-Authored-By: Claude Opus 4.6 (1M context) <noreply@anthropic.com>"
```

---

### Task 3: Write failing integration test — full lobby rejects reconnected follower

**Files:**

- Modify: `server/evr_match_test.go`

- [ ] **Step 1: Write the integration test**

This test exercises the full `MatchJoinAttempt` path with a lobby at capacity. The reservation is counted in `Size` by `rebuildCache`, so `OpenSlots()` returns 0 and the join is rejected at line 404 — before the reservation lookup at line 416 runs.

```go
// TestMatchJoinAttempt_PartyFollowerReconnectFindsReservation exercises the full
// MatchJoinAttempt flow: leader joins creating a reservation for a follower,
// lobby fills to capacity, then the follower reconnects with a new session ID.
// The join must succeed via user-ID reservation fallback.
func TestMatchJoinAttempt_PartyFollowerReconnectFindsReservation(t *testing.T) {
	state := newTestMatchLabel()
	state.Mode = evr.ModeSocialPublic
	state.LobbyType = PublicLobby
	state.MaxSize = SocialLobbyMaxSize
	state.PlayerLimit = SocialLobbyMaxSize

	leaderUserID := uuid.Must(uuid.NewV4())
	leaderSessionID := uuid.Must(uuid.NewV4())
	followerUserID := uuid.Must(uuid.NewV4())
	followerOriginalSID := uuid.Must(uuid.NewV4())
	followerNewSID := uuid.Must(uuid.NewV4())
	partyID := uuid.Must(uuid.NewV4())

	leader := &EvrMatchPresence{
		Node:          "testnode",
		SessionID:     leaderSessionID,
		UserID:        leaderUserID,
		EvrID:         evr.EvrId{PlatformCode: 4, AccountId: 1},
		Username:      "leader",
		PartyID:       partyID,
		RoleAlignment: evr.TeamSocial,
		SessionExpiry: 9999999999,
		ClientIP:      "127.0.0.1",
		ClientPort:    "1001",
	}
	followerReservation := &EvrMatchPresence{
		Node:          "testnode",
		SessionID:     followerOriginalSID,
		UserID:        followerUserID,
		EvrID:         evr.EvrId{PlatformCode: 4, AccountId: 2},
		Username:      "follower",
		PartyID:       partyID,
		RoleAlignment: evr.TeamSocial,
		SessionExpiry: 9999999999,
		ClientIP:      "127.0.0.2",
		ClientPort:    "1002",
	}

	leaderMeta := &EntrantMetadata{
		Presence:     leader,
		Reservations: []*EvrMatchPresence{followerReservation},
	}

	m := &EvrMatch{}
	ctx := context.Background()
	logger := NewRuntimeGoLogger(NewJSONLogger(os.Stdout, zapcore.ErrorLevel, JSONFormat))

	// Leader joins — creates reservation for follower
	resultState, allowed, reason := m.MatchJoinAttempt(ctx, logger, nil, nil, nil, 0, state, leader, leaderMeta.ToMatchMetadata())
	if !allowed {
		t.Fatalf("Leader join should be allowed, got: %s", reason)
	}
	state = resultState.(*MatchLabel)

	if _, exists := state.reservationMap[followerOriginalSID.String()]; !exists {
		t.Fatal("Reservation should exist for follower's original session ID")
	}

	// Fill the lobby to capacity: leader=1, reservation=1, add 10 fillers = 12
	for i := 0; i < 10; i++ {
		filler := &EvrMatchPresence{
			Node:          "testnode",
			SessionID:     uuid.Must(uuid.NewV4()),
			UserID:        uuid.Must(uuid.NewV4()),
			EvrID:         evr.EvrId{PlatformCode: 4, AccountId: uint64(100 + i)},
			Username:      fmt.Sprintf("filler%d", i),
			RoleAlignment: evr.TeamSocial,
			SessionExpiry: 9999999999,
			ClientIP:      fmt.Sprintf("127.0.1.%d", i),
			ClientPort:    fmt.Sprintf("200%d", i),
		}
		fillerMeta := NewJoinMetadata(filler)
		resultState, allowed, reason = m.MatchJoinAttempt(ctx, logger, nil, nil, nil, 0, state, filler, fillerMeta.ToMatchMetadata())
		if !allowed {
			t.Fatalf("Filler %d join rejected: %s", i, reason)
		}
		state = resultState.(*MatchLabel)
	}

	// Lobby is at capacity (12/12)
	if state.OpenSlots() != 0 {
		t.Fatalf("Expected 0 open slots, got %d", state.OpenSlots())
	}

	// Follower reconnects with a NEW session ID
	followerReconnected := &EvrMatchPresence{
		Node:          "testnode",
		SessionID:     followerNewSID,
		UserID:        followerUserID,
		EvrID:         evr.EvrId{PlatformCode: 4, AccountId: 2},
		Username:      "follower",
		PartyID:       partyID,
		RoleAlignment: evr.TeamSocial,
		SessionExpiry: 9999999999,
		ClientIP:      "127.0.0.2",
		ClientPort:    "1002",
	}
	followerMeta := NewJoinMetadata(followerReconnected)

	// Without the fix: rejected with "lobby full" because OpenSlots()=0
	// blocks the join before reservation lookup runs.
	// With the fix: reservation is looked up BEFORE the slot check,
	// consumed (freeing a slot), and the join succeeds.
	resultState, allowed, reason = m.MatchJoinAttempt(ctx, logger, nil, nil, nil, 0, state, followerReconnected, followerMeta.ToMatchMetadata())
	if !allowed {
		t.Fatalf("Follower reconnect should succeed via reservation fallback, but rejected: %s", reason)
	}
	state = resultState.(*MatchLabel)

	// Verify reservation was consumed
	if _, exists := state.reservationMap[followerOriginalSID.String()]; exists {
		t.Error("Original reservation should have been consumed")
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `cd /home/andrew/src/nakama && go test ./server/ -run TestMatchJoinAttempt_PartyFollowerReconnectFindsReservation -v -count=1`

Expected: FAIL with reason `lobby full` — the `OpenSlots()` gate at line 400-406 rejects the join before the reservation lookup at line 416 is reached.

---

### Task 4: Move reservation lookup before OpenSlots check and add user-ID fallback

**Files:**

- Modify: `server/evr_match.go:398-422`

- [ ] **Step 1: Restructure MatchJoinAttempt**

Replace lines 398-422:

```go
	// Ensure the match has enough slots available.
	// Reconnecting users can reclaim their own reconnect reservation slot.
	availableSlots := state.OpenSlots()
	if reconnectReservation != nil {
		availableSlots++
	}
	if availableSlots < len(meta.Presences()) {
		restoreReconnectReservation()
		return state, false, ErrJoinRejectReasonLobbyFull.Error()
	}

	if reconnectReservation != nil {
		delete(state.reconnectReservations, meta.Presence.GetUserId())
		state.rebuildCache()
		consumeReconnectReservation = true
	}

	// If this is a reservation, load the reservation
	if e, found := state.LoadAndDeleteReservation(meta.Presence.GetSessionId()); found {
		meta.Presence.PartyID = e.PartyID
		meta.Presence.RoleAlignment = e.RoleAlignment

		state.rebuildCache()
		logger = logger.WithField("has_reservation", true)
	}
```

With:

```go
	// Look up slot reservation BEFORE the capacity check.
	// Reservations are counted in Size (via rebuildCache), so consuming one
	// frees a slot. Without this ordering, a full lobby rejects the join
	// at the OpenSlots gate before the reservation lookup is reached.
	// First try by session ID (fast path). If that misses — e.g. the follower
	// disconnected and reconnected with a new session — fall back to user ID.
	var slotReservationPresence *EvrMatchPresence
	if e, found := state.LoadAndDeleteReservation(meta.Presence.GetSessionId()); found {
		slotReservationPresence = e
	} else if e, found := state.LoadAndDeleteReservationByUserID(meta.Presence.GetUserId()); found {
		slotReservationPresence = e
	}
	if slotReservationPresence != nil {
		meta.Presence.PartyID = slotReservationPresence.PartyID
		meta.Presence.RoleAlignment = slotReservationPresence.RoleAlignment
		state.rebuildCache()
		logger = logger.WithField("has_reservation", true)
	}

	// Ensure the match has enough slots available.
	// Reconnecting users can reclaim their own reconnect reservation slot.
	availableSlots := state.OpenSlots()
	if reconnectReservation != nil {
		availableSlots++
	}
	if availableSlots < len(meta.Presences()) {
		restoreReconnectReservation()
		return state, false, ErrJoinRejectReasonLobbyFull.Error()
	}

	if reconnectReservation != nil {
		delete(state.reconnectReservations, meta.Presence.GetUserId())
		state.rebuildCache()
		consumeReconnectReservation = true
	}
```

- [ ] **Step 2: Run the integration test**

Run: `cd /home/andrew/src/nakama && go test ./server/ -run TestMatchJoinAttempt_PartyFollowerReconnectFindsReservation -v -count=1`

Expected: PASS.

- [ ] **Step 3: Run all reservation and join tests**

Run: `cd /home/andrew/src/nakama && go test ./server/ -run "TestSlotReservation_|TestMatchJoinAttempt_|TestReconnectReservation_|TestEvrMatch_MatchJoinAttempt" -v -count=1`

Expected: All PASS.

- [ ] **Step 4: Run the full server test suite**

Run: `cd /home/andrew/src/nakama && go test ./server/ -count=1 -timeout 120s`

Expected: All PASS.

- [ ] **Step 5: Commit**

```bash
git add server/evr_match.go server/evr_match_test.go
git commit -m "fix: party followers can now join leader's social lobby after reconnecting

When a party leader joins a social lobby, slot reservations are created
for each follower keyed by session ID. If a follower disconnected and
reconnected (getting a new session ID), the reservation lookup missed
and the join was rejected as 'lobby full'.

Players experienced this as: joining a pub with friends, getting
repeated 'server full' errors, the client disconnecting and
reconnecting in a loop trying to follow the party leader into a full
lobby. Each reconnect gave a new session ID, making the reserved slot
permanently unreachable. The only resolution was someone else leaving.

Two changes:
1. Add LoadAndDeleteReservationByUserID — a fallback that finds
   reservations by user ID when the session-ID lookup misses.
2. Move the reservation lookup BEFORE the OpenSlots() capacity check
   in MatchJoinAttempt. Reservations are counted in Size by
   rebuildCache, so consuming one frees a slot. Previously, the
   capacity check ran first and rejected the join before the
   reservation lookup was reached.

Co-Authored-By: Claude Opus 4.6 (1M context) <noreply@anthropic.com>"
```
