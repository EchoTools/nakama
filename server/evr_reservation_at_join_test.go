package server

import (
	"context"
	"testing"
	"time"

	rtapi "buf.build/gen/go/echotools/nevr-api/protocolbuffers/go/gameservice/v1"
	"github.com/gofrs/uuid/v5"
	"github.com/heroiclabs/nakama/v3/server/evr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/atomic"
)

// ===========================================================================
// Reservations are created ATOMICALLY at the join, never on a game-server event
// ===========================================================================
//
// Root cause (prod v3.27.2-evr.319): lobbyEntrantConnected — the handler for the
// game server confirming ONE player connected — used to fire
// `go p.createPartyReservations(...)` when the party leader connected. That
// deferred a Nakama capacity decision (slot reservations: state.reservationMap /
// OpenSlots, consumed in MatchJoinAttempt) to an async game-server round-trip,
// opening the exact race it must avoid: the leader's seat is taken, but the
// followers' reserved seats are not yet held, so other players fill the lobby in
// the gap. The deferred reservation then lands on a full lobby and trips the
// ReservationViolated path (evr_match.go:451), producing the storm.
//
// The correct model: party reservations are created atomically at the leader's
// join, inside MatchJoinAttempt, via the existing entrants[1:] path
// (appendPartyReservationPlaceholders, evr_lobby_find.go:271 -> LobbyJoinEntrants,
// evr_lobby_joinentrant.go:79-80 Presence=entrants[0]/Reservations=entrants[1:] ->
// MatchJoinAttempt). It is capacity-gated (evr_match.go:447): the whole party
// (leader + reservations, EntrantMetadata.Presences()) must fit, or the join is
// rejected and NO reservation is written. Reservations are never created on a
// game-server-connect event.
//
// These two tests pin that contract:
//   1. TestMatchJoinAttempt_LeaderPartyReservations_AtomicAndCapacityGated:
//      the atomic path creates the party's reservations at the leader's join for
//      a social lobby, and creates NONE when the lobby has no room for the party.
//   2. TestLobbyEntrantConnected_DoesNotCreatePartyReservations: the game-server
//      connect handler creates no party reservation (regression guard for the
//      removed trigger).

// atJoinSocialLabel builds a social-public MatchLabel with the given capacity and
// a number of already-seated players, so OpenSlots() == MaxSize-existingPlayers.
func atJoinSocialLabel(t *testing.T, maxSize, playerLimit, existingPlayers int) *MatchLabel {
	t.Helper()
	gid := uuid.Must(uuid.NewV4())
	state := &MatchLabel{
		ID:          MatchID{UUID: uuid.Must(uuid.NewV4()), Node: "testnode"},
		CreatedAt:   time.Now().UTC(),
		Open:        true,
		LobbyType:   PublicLobby,
		Mode:        evr.ModeSocialPublic,
		Level:       evr.LevelSocial,
		GroupID:     &gid,
		MaxSize:     maxSize,
		PlayerLimit: playerLimit,
		TeamSize:    0,
		GameServer: &GameServerPresence{
			SessionID:  uuid.Must(uuid.NewV4()),
			OperatorID: uuid.Must(uuid.NewV4()),
			GroupIDs:   []uuid.UUID{gid},
		},
		Players:               make([]PlayerInfo, 0, maxSize),
		presenceMap:           make(map[string]*EvrMatchPresence, maxSize),
		reservationMap:        make(map[string]*slotReservation, 2),
		reconnectReservations: make(map[string]*reconnectReservation),
		presenceByEvrID:       make(map[evr.EvrId]*EvrMatchPresence, maxSize),
		TeamAlignments:        make(map[string]int, maxSize),
		joinTimestamps:        make(map[string]time.Time, maxSize),
		joinTimeMilliseconds:  make(map[string]int64, maxSize),
		tickRate:              10,
	}
	for i := 0; i < existingPlayers; i++ {
		p := &EvrMatchPresence{
			Node:          "testnode",
			SessionID:     uuid.Must(uuid.NewV4()),
			UserID:        uuid.Must(uuid.NewV4()),
			EvrID:         evr.EvrId{PlatformCode: 4, AccountId: uint64(1000 + i)},
			Username:      "seated",
			RoleAlignment: evr.TeamSocial,
		}
		state.presenceMap[p.GetSessionId()] = p
	}
	state.rebuildCache()
	return state
}

func atJoinPresence(role int, account uint64) *EvrMatchPresence {
	return &EvrMatchPresence{
		Node:           "testnode",
		SessionID:      uuid.Must(uuid.NewV4()),
		LoginSessionID: uuid.Must(uuid.NewV4()),
		UserID:         uuid.Must(uuid.NewV4()),
		EvrID:          evr.EvrId{PlatformCode: 4, AccountId: account},
		Username:       "atjoin",
		RoleAlignment:  role,
	}
}

// TestMatchJoinAttempt_LeaderPartyReservations_AtomicAndCapacityGated proves the
// leader-join social case is handled by the atomic entrants[1:] path: the party's
// reservations are written in MatchJoinAttempt (from EntrantMetadata.Reservations),
// and they are capacity-gated — a lobby with no room for the whole party rejects
// the join and writes NO reservation. This is the path that makes the
// lobbyEntrantConnected trigger redundant (Chesterton point 1).
func TestMatchJoinAttempt_LeaderPartyReservations_AtomicAndCapacityGated(t *testing.T) {
	tests := []struct {
		name              string
		maxSize           int
		playerLimit       int
		existingPlayers   int
		reservations      int // party followers reserved alongside the leader
		wantAccepted      bool
		wantReason        string
		wantReservedCount int
	}{
		{
			name:              "room for the whole party: leader join reserves followers atomically",
			maxSize:           12,
			playerLimit:       12,
			existingPlayers:   0,
			reservations:      2,
			wantAccepted:      true,
			wantReservedCount: 2,
		},
		{
			name:              "one open slot, party of two: capacity-gated, no reservation written",
			maxSize:           12,
			playerLimit:       12,
			existingPlayers:   11, // OpenSlots() == 1; leader + 1 follower needs 2
			reservations:      1,
			wantAccepted:      false,
			wantReason:        ErrJoinRejectReasonLobbyFull.Error(),
			wantReservedCount: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			state := atJoinSocialLabel(t, tt.maxSize, tt.playerLimit, tt.existingPlayers)

			leader := atJoinPresence(evr.TeamSocial, 1)
			reservations := make([]*EvrMatchPresence, 0, tt.reservations)
			for i := 0; i < tt.reservations; i++ {
				reservations = append(reservations, atJoinPresence(evr.TeamSocial, uint64(100+i)))
			}

			// Mirror LobbyJoinEntrants: Presence = entrants[0] (leader),
			// Reservations = entrants[1:] (followers).
			meta := EntrantMetadata{Presence: leader, Reservations: reservations}

			m := &EvrMatch{}
			gotState, accepted, reason := m.MatchJoinAttempt(
				context.Background(), reconnectTestLogger(), nil, nil, nil, 10,
				state, leader, meta.ToMatchMetadata())

			label, ok := gotState.(*MatchLabel)
			require.Truef(t, ok, "MatchJoinAttempt returned non-*MatchLabel: %T", gotState)

			require.Equalf(t, tt.wantAccepted, accepted, "accepted mismatch (reason=%q)", reason)
			if tt.wantReason != "" {
				require.Equal(t, tt.wantReason, reason)
			}

			require.Lenf(t, label.reservationMap, tt.wantReservedCount,
				"reservationMap size mismatch: reservations must be written atomically only when the whole party fits")

			if tt.wantAccepted {
				for _, r := range reservations {
					assert.Containsf(t, label.reservationMap, r.SessionID.String(),
						"follower %s must hold an atomic slot reservation after the leader's join", r.SessionID)
				}
			} else {
				// Capacity gate: a full lobby must not leave a partial reservation.
				assert.Emptyf(t, label.reservationMap,
					"no reservation may be written into a lobby without room for the whole party (this is the prod race the fix closes)")
			}
		})
	}
}

// TestLobbyEntrantConnected_DoesNotCreatePartyReservations drives the real
// lobbyEntrantConnected handler for a party leader whose party has an online
// follower, and asserts that NO SignalCreatePartyReservations is emitted. Before
// the fix, lobbyEntrantConnected fired `go p.createPartyReservations(...)` on the
// leader's game-server connect, which is exactly the deferred, capacity-blind
// reservation the fix removes. Reservations are made atomically at MatchJoinAttempt
// (see TestMatchJoinAttempt_LeaderPartyReservations_AtomicAndCapacityGated) and at
// createReservationForNewPartyMember for late joiners — never on this event.
func TestLobbyEntrantConnected_DoesNotCreatePartyReservations(t *testing.T) {
	env := mkGroupReservationEnv(t, "monarch12")

	// The party leader's PLAYER session, carrying currentPartyID so the (removed)
	// trigger's gate would resolve. Its id/userID match the promoted registry leader.
	leaderParams := &SessionParameters{
		xpID:           evr.EvrId{},
		profile:        &EVRProfile{},
		currentPartyID: env.ph.ID,
	}
	baseCtx := context.WithValue(context.Background(), ctxSessionParametersKey{}, atomic.NewPointer(leaderParams))
	leaderCtx, leaderCancel := context.WithCancel(baseCtx)
	t.Cleanup(leaderCancel)

	leaderSession := &sessionWS{}
	leaderSession.id = env.leaderSID
	leaderSession.userID = env.leaderUID
	leaderSession.username = atomic.NewString("leader")
	leaderSession.ctx = leaderCtx
	leaderSession.ctxCancelFn = leaderCancel
	leaderSession.logger = loggerForTest(t)
	leaderSession.format = SessionFormatProtobuf
	leaderSession.outgoingCh = make(chan []byte, 16)
	leaderSession.tracker = env.tracker
	leaderSession.pipeline = &Pipeline{node: "testnode", tracker: env.tracker, partyRegistry: env.pr}
	leaderSession.evrPipeline = env.ep
	env.sessions.sessions[env.leaderSID] = leaderSession

	// An online follower on the party stream with a live session — the member the
	// (removed) trigger would have reserved via createPartyReservations.
	follower := newPartyMemberSession(t, "follower", env.tracker, env.pr, env.ep)
	env.sessions.sessions[follower.id] = follower
	env.tracker.Track(context.Background(), env.leaderSID, env.ph.Stream, env.leaderUID, PresenceMeta{Username: "leader"})
	env.tracker.Track(context.Background(), follower.id, env.ph.Stream, follower.userID, PresenceMeta{Username: "follower"})

	// Seed the leader on the entrant stream so PresenceByEntrantID resolves it —
	// this is what makes lobbyEntrantConnected reach the leader-connected code path
	// (and, before the fix, fire the reservation trigger).
	matchUUID := uuid.Must(uuid.NewV4())
	entrantID := uuid.Must(uuid.NewV4())
	entrantPresence := &EvrMatchPresence{
		Node:           "testnode",
		SessionID:      env.leaderSID,
		LoginSessionID: uuid.Must(uuid.NewV4()),
		UserID:         env.leaderUID,
		EvrID:          evr.EvrId{PlatformCode: 4, AccountId: 42},
		Username:       "leader",
		RoleAlignment:  evr.TeamSocial,
		EntrantID:      entrantID,
	}
	env.tracker.Track(context.Background(), env.leaderSID,
		PresenceStream{Mode: StreamModeEntrant, Subject: entrantID, Label: "testnode"},
		env.leaderUID,
		PresenceMeta{Status: entrantPresence.GetStatus()})

	// The game server's session (whose connect message we are handling). Distinct
	// from the leader's player session. Buffered outgoingCh so SendEvr succeeds.
	gsSession := &sessionWS{}
	gsSession.id = uuid.Must(uuid.NewV4())
	gsSession.userID = uuid.Must(uuid.NewV4())
	gsSession.ctx = context.Background()
	gsSession.logger = loggerForTest(t)
	gsSession.format = SessionFormatProtobuf
	gsSession.outgoingCh = make(chan []byte, 16)
	gsSession.tracker = env.tracker
	gsSession.pipeline = &Pipeline{node: "testnode", tracker: env.tracker}

	envelope := &rtapi.Envelope{
		Message: &rtapi.Envelope_LobbyEntrantConnected{
			LobbyEntrantConnected: &rtapi.LobbyEntrantsConnectedMessage{
				LobbySessionId: matchUUID.String(),
				EntrantIds:     []string{entrantID.String()},
			},
		},
	}

	err := env.ep.lobbyEntrantConnected(loggerForTest(t), gsSession, envelope)
	require.NoError(t, err)

	// The trigger was dispatched as a goroutine; give any such goroutine ample
	// time to fire. Post-fix there is no trigger, so no signal must ever appear.
	require.Never(t, func() bool { return env.registry.signalCount() > 0 },
		400*time.Millisecond, 20*time.Millisecond,
		"lobbyEntrantConnected must NOT create party reservations — reservations are made "+
			"atomically at MatchJoinAttempt (entrants[1:]), never on a game-server connect event")
}
