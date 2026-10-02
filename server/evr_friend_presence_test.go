package server

import (
	"context"
	"testing"

	"github.com/gofrs/uuid/v5"
	"github.com/heroiclabs/nakama/v3/server/evr"
	"github.com/stretchr/testify/require"
)

func TestFriendPresenceTextNamesEveryModePlainly(t *testing.T) {
	cases := map[evr.Symbol]string{
		evr.ModeSocialPublic:          "Social Lobby",
		evr.ModeSocialPrivate:         "Social Lobby",
		evr.ModeSocialNPE:             "Social Lobby",
		evr.ModeArenaPublic:           "Public Arena Match",
		evr.ModeArenaPrivate:          "Private Arena Match",
		evr.ModeArenaPublicAI:         "Arena Match vs Bots",
		evr.ModeArenaTutorial:         "Arena Tutorial",
		evr.ModeCombatPublic:          "Public Combat Match",
		evr.ModeCombatPrivate:         "Private Combat Match",
		evr.ModeEchoCombatTournament:  "Combat Tournament Match",
		evr.ModeArenaTournment:        "Arena Tournament Match",
		evr.ModeArenaPracticeAI:       "Arena Practice",
		evr.ToSymbol("some_new_mode"): "In a Match",
	}
	for mode, want := range cases {
		if got := friendPresenceText(&MatchLabel{Mode: mode}); got != want {
			t.Errorf("mode %v: %q, want %q", mode, got, want)
		}
	}
	if got := friendPresenceText(nil); got != "In Main Menu" {
		t.Errorf("no match: %q, want In Main Menu", got)
	}
}

func TestSNSFriendPresenceNotifyRoundTrip(t *testing.T) {
	in := &evr.SNSFriendPresenceNotify{FriendID: 4242, PartyID: 77, Joinable: 1, StatusCode: 0, Text: []byte("Social Lobby")}
	data, err := evr.Marshal(in)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	msgs, err := evr.ParsePacket(data)
	if err != nil || len(msgs) != 1 {
		t.Fatalf("ParsePacket: %v (%d messages)", err, len(msgs))
	}
	out, ok := msgs[0].(*evr.SNSFriendPresenceNotify)
	if !ok {
		t.Fatalf("parsed %T", msgs[0])
	}
	if out.FriendID != 4242 || out.PartyID != 77 || out.Joinable != 1 || string(out.Text) != "Social Lobby" || out.TextLen != 12 {
		t.Fatalf("round trip: %+v", out)
	}
	if len(data) != 24+8+8+8+1+1+6+2+12 {
		t.Fatalf("frame is %d bytes, want the documented layout", len(data))
	}
}

func TestSNSPartyOfferedToAFriend(t *testing.T) {
	cases := []struct {
		name          string
		open          bool
		size, maxSize int
		admitted      bool
		want          bool
	}{
		{"open, room, admitted", true, 2, 4, true, true},
		{"locked", false, 2, 4, true, false},
		{"full", true, 4, 4, true, false},
		{"policy refuses (invite only, not invited)", true, 1, 4, snsPartyPolicyAdmits(snsPartyPolicyInviteOnly, false, true, true), false},
		{"invite admits under invite only", true, 1, 4, snsPartyPolicyAdmits(snsPartyPolicyInviteOnly, true, false, false), true},
		{"friends policy, a friend of the leader", true, 1, 4, snsPartyPolicyAdmits(snsPartyPolicyFriends, false, true, false), true},
		{"friends policy, a stranger", true, 1, 4, snsPartyPolicyAdmits(snsPartyPolicyFriends, false, false, false), false},
	}
	for _, c := range cases {
		if got := snsPartyOffered(c.open, c.size, c.maxSize, c.admitted); got != c.want {
			t.Errorf("%s: offered = %v, want %v", c.name, got, c.want)
		}
	}
}

// presenceEnv is the tablet fixture plus a match registry, so a friend can be put in a match and in a
// party, the way the tracker and registries hold them while the game runs.
type presenceEnv struct {
	*tabletEnv
	matches *mockFollowMatchRegistry
}

func newPresenceEnv(t *testing.T) *presenceEnv {
	e := &presenceEnv{tabletEnv: newTabletEnv(t), matches: newMockFollowMatchRegistry()}
	e.ep.nk.matchRegistry = e.matches
	e.ep.snsPartyPolicies = &MapOf[uuid.UUID, uint8]{}
	e.ep.snsPartyInvites = &MapOf[uuid.UUID, *snsPartyInviteList]{}
	return e
}

// online puts a session on its user's status stream, which is how the server knows the user is live.
func (e *presenceEnv) online(s *sessionWS) {
	e.tracker.Track(context.Background(), s.id, PresenceStream{Mode: StreamModeStatus, Subject: s.userID}, s.userID, PresenceMeta{})
}

// inMatch records the user's match-service presence, which names the match they are in.
func (e *presenceEnv) inMatch(s *sessionWS, mode evr.Symbol) MatchID {
	id := MatchID{UUID: uuid.Must(uuid.NewV4()), Node: "testnode"}
	e.matches.SetMatch(id, &MatchLabel{ID: id, Mode: mode})
	e.tracker.Track(context.Background(), s.id, PresenceStream{Mode: StreamModeService, Subject: s.userID, Label: StreamLabelMatchService},
		s.userID, PresenceMeta{Status: id.String()})
	return id
}

// friendInOpenParty is a runtime-client friend, online, alone in an open tablet party.
func (e *presenceEnv) friendInOpenParty(snsID uint64) (*sessionWS, *PartyHandler) {
	friend := e.session("friend", true)
	e.online(friend)
	return friend, e.tabletParty(snsID, friend)
}

// With a database: both users exist, and `viewer` is a mutual friend of `friend` (user_edge state 0 in
// both directions, as an accepted friendship leaves it). Removed when the test ends.
func (e *presenceEnv) withFriendship(t *testing.T, viewer uuid.UUID, friend uuid.UUID) {
	db := NewDB(t)
	e.ep.db = db
	for _, id := range []uuid.UUID{viewer, friend} {
		InsertUser(t, db, id)
	}
	t.Cleanup(func() {
		_, _ = db.Exec("DELETE FROM users WHERE id = ANY($1::UUID[])", []string{viewer.String(), friend.String()})
		_ = db.Close()
	})
	for _, pair := range [][2]uuid.UUID{{viewer, friend}, {friend, viewer}} {
		_, err := db.Exec(`INSERT INTO user_edge (source_id, destination_id, state, position, update_time)
VALUES ($1, $2, 0, 1, now()) ON CONFLICT DO NOTHING`, pair[0], pair[1])
		require.NoError(t, err)
	}
}

// A friend who is in no match is in the main menu: that is what the game shows under their name.
func TestFriendInNoMatchReadsInMainMenu(t *testing.T) {
	e := newPresenceEnv(t)
	friend := e.session("friend", true)
	e.online(friend)
	viewer := uuid.Must(uuid.NewV4())

	require.Nil(t, e.ep.userCurrentMatch(context.Background(), friend.userID))
	n := e.ep.friendPresence(context.Background(), viewer, friend.userID, 1001, true)
	require.Equal(t, uint64(1001), n.FriendID)
	require.Equal(t, uint8(0), n.StatusCode, "online")
	require.Equal(t, "In Main Menu", string(n.Text))
	require.Zero(t, n.PartyID)
	require.Zero(t, n.Joinable)
}

// A friend in a public arena match reads "Public Arena Match", and the match they are in is the one
// the tracker names (their match-service presence), looked up in the match registry.
func TestFriendInAPublicArenaMatchReadsSo(t *testing.T) {
	e := newPresenceEnv(t)
	friend := e.session("friend", true)
	e.online(friend)
	matchID := e.inMatch(friend, evr.ModeArenaPublic)

	label := e.ep.userCurrentMatch(context.Background(), friend.userID)
	require.NotNil(t, label)
	require.Equal(t, matchID, label.ID)
	n := e.ep.friendPresence(context.Background(), uuid.Must(uuid.NewV4()), friend.userID, 1002, true)
	require.Equal(t, "Public Arena Match", string(n.Text))
}

// A friend whose match-service presence names nothing usable (not a match id, or a match that has
// since ended) is treated as in no match, so the game shows the main menu rather than a stale match.
func TestFriendWithAStaleMatchPresenceReadsInMainMenu(t *testing.T) {
	e := newPresenceEnv(t)
	friend := e.session("friend", true)
	service := PresenceStream{Mode: StreamModeService, Subject: friend.userID, Label: StreamLabelMatchService}

	e.tracker.Track(context.Background(), friend.id, service, friend.userID, PresenceMeta{Status: "not-a-match-id"})
	require.Nil(t, e.ep.userCurrentMatch(context.Background(), friend.userID))

	gone := MatchID{UUID: uuid.Must(uuid.NewV4()), Node: "testnode"} // never registered
	e.tracker.Track(context.Background(), friend.id, service, friend.userID, PresenceMeta{Status: gone.String()})
	require.Nil(t, e.ep.userCurrentMatch(context.Background(), friend.userID))
	n := e.ep.friendPresence(context.Background(), uuid.Must(uuid.NewV4()), friend.userID, 1, true)
	require.Equal(t, "In Main Menu", string(n.Text))
}

// The server finds a user's game settings from their live session: the one on their status stream. A
// user with no live session (or whose session is gone from the registry) has none.
func TestUserSessionParamsComeFromTheLiveSession(t *testing.T) {
	e := newPresenceEnv(t)
	friend := e.session("friend", true)

	require.Nil(t, e.ep.userSessionParams(friend.userID), "no status presence: not online")

	e.online(friend)
	got := e.ep.userSessionParams(friend.userID)
	want, _ := LoadParams(friend.Context())
	require.Same(t, want, got)

	delete(e.sessions.sessions, friend.id)
	require.Nil(t, e.ep.userSessionParams(friend.userID), "the session is gone from the registry")
}

// A friend's party id is shown to a viewer only when the viewer could join it now. Everything that
// would make the join fail withholds the id (the game's own list showed it only then): the friend in no
// party, a party the registry no longer has, a locked party, a full one.
func TestFriendPartyIsWithheldUnlessTheViewerCouldJoin(t *testing.T) {
	e := newPresenceEnv(t)
	viewer := uuid.Must(uuid.NewV4())
	friend, ph := e.friendInOpenParty(555)
	params, _ := LoadParams(friend.Context())

	id, ok := e.ep.friendPartyFor(context.Background(), viewer, params)
	require.True(t, ok, "open, room left, policy everyone (the default): joinable")
	require.Equal(t, uint64(555), id)

	_, ok = e.ep.friendPartyFor(context.Background(), viewer, nil)
	require.False(t, ok, "no session")
	_, ok = e.ep.friendPartyFor(context.Background(), viewer, &SessionParameters{})
	require.False(t, ok, "in no party")
	_, ok = e.ep.friendPartyFor(context.Background(), viewer, &SessionParameters{currentPartyID: ph.ID})
	require.False(t, ok, "a party with no SNS id has nothing to show")
	_, ok = e.ep.friendPartyFor(context.Background(), viewer, &SessionParameters{currentPartyID: uuid.Must(uuid.NewV4()), currentSNSPartyID: 9})
	require.False(t, ok, "the party is gone from the registry")

	ph.Lock()
	ph.Open = false
	ph.Unlock()
	id, ok = e.ep.friendPartyFor(context.Background(), viewer, params)
	require.False(t, ok, "a locked party is not joinable")
	require.Zero(t, id)

	ph.Lock()
	ph.Open = true
	ph.MaxSize = 1 // the friend is the only member: full
	ph.Unlock()
	_, ok = e.ep.friendPartyFor(context.Background(), viewer, params)
	require.False(t, ok, "a full party is not joinable")
}

// The party's join policy decides who is shown the id (nevr-runtime docs/design/2026-10-01-social-nakama-proposal.md §4):
// invite only withholds it from a stranger, but a viewer who holds an invite is shown it.
func TestFriendPartyIsShownToAnInvitedViewerUnderInviteOnly(t *testing.T) {
	e := newPresenceEnv(t)
	viewer := uuid.Must(uuid.NewV4())
	friend, ph := e.friendInOpenParty(556)
	params, _ := LoadParams(friend.Context())

	e.ep.snsPartyPolicies.Store(ph.ID, snsPartyPolicyInviteOnly)
	_, ok := e.ep.friendPartyFor(context.Background(), viewer, params)
	require.False(t, ok, "invite only, no invite")

	list := &snsPartyInviteList{}
	list.Add(&snsPartyInvite{PartyUUID: ph.ID, SNSPartyID: 556})
	e.ep.snsPartyInvites.Store(viewer, list)
	id, ok := e.ep.friendPartyFor(context.Background(), viewer, params)
	require.True(t, ok, "invite only, with an invite")
	require.Equal(t, uint64(556), id)
}

// The friends policy reads the real friendship from the database: the leader's friend is shown the
// party, anyone else is not.
func TestFriendPartyIsShownToTheLeadersFriend(t *testing.T) {
	e := newPresenceEnv(t)
	friend, ph := e.friendInOpenParty(557)
	params, _ := LoadParams(friend.Context())
	e.ep.snsPartyPolicies.Store(ph.ID, snsPartyPolicyFriends)
	viewer := uuid.Must(uuid.NewV4())
	e.withFriendship(t, viewer, friend.userID)

	id, ok := e.ep.friendPartyFor(context.Background(), viewer, params)
	require.True(t, ok)
	require.Equal(t, uint64(557), id)

	stranger := uuid.Must(uuid.NewV4())
	InsertUser(t, e.ep.db, stranger)
	t.Cleanup(func() { _, _ = e.ep.db.Exec("DELETE FROM users WHERE id = $1", stranger) })
	_, ok = e.ep.friendPartyFor(context.Background(), stranger, params)
	require.False(t, ok, "not the leader's friend")
}

// A friend in a joinable party is sent with the party id and the joinable flag; the text still says
// where they are.
func TestFriendPresenceCarriesAJoinablePartyAndTheMatch(t *testing.T) {
	e := newPresenceEnv(t)
	friend, _ := e.friendInOpenParty(558)
	e.inMatch(friend, evr.ModeSocialPublic)

	n := e.ep.friendPresence(context.Background(), uuid.Must(uuid.NewV4()), friend.userID, 2001, true)
	require.Equal(t, uint64(2001), n.FriendID)
	require.Equal(t, uint64(558), n.PartyID)
	require.Equal(t, uint8(1), n.Joinable)
	require.Equal(t, "Social Lobby", string(n.Text))
}

// An offline friend is sent only a status code (2): no text, no party, even if a stale party or match
// is still on record for them.
func TestOfflineFriendGetsOnlyAStatusCode(t *testing.T) {
	e := newPresenceEnv(t)
	friend, _ := e.friendInOpenParty(559)
	e.inMatch(friend, evr.ModeArenaPublic)

	n := e.ep.friendPresence(context.Background(), uuid.Must(uuid.NewV4()), friend.userID, 3001, false)
	require.Equal(t, uint64(3001), n.FriendID)
	require.Equal(t, uint8(2), n.StatusCode)
	require.Empty(t, n.Text)
	require.Zero(t, n.PartyID)
	require.Zero(t, n.Joinable)
}

// sentPresence decodes what the viewer's game client was sent.
func sentPresence(t *testing.T, s *sessionWS) []*evr.SNSFriendPresenceNotify {
	out := []*evr.SNSFriendPresenceNotify{}
	for _, b := range drain(s.outgoingCh) {
		msgs, err := evr.ParsePacket(b)
		require.NoError(t, err)
		for _, m := range msgs {
			n, ok := m.(*evr.SNSFriendPresenceNotify)
			require.True(t, ok, "got %T", m)
			out = append(out, n)
		}
	}
	return out
}

// A nevr-runtime client (social level 1) is sent one presence message per friend: the online friend
// with where they are and their joinable party, the offline friend with only the offline status.
func TestRuntimeClientIsSentOnePresencePerFriend(t *testing.T) {
	e := newPresenceEnv(t)
	viewer := e.session("viewer", true)
	online, _ := e.friendInOpenParty(560)
	e.inMatch(online, evr.ModeArenaPublic)
	offline := e.session("offline", true)

	e.ep.sendFriendPresence(viewer.Context(), loggerForTest(t), viewer, []friendPresenceTarget{
		{userID: online.userID, accountID: 11, online: true},
		{userID: offline.userID, accountID: 22, online: false},
	})
	got := sentPresence(t, viewer)
	require.Len(t, got, 2)
	require.Equal(t, uint64(11), got[0].FriendID)
	require.Equal(t, "Public Arena Match", string(got[0].Text))
	require.Equal(t, uint64(560), got[0].PartyID)
	require.Equal(t, uint8(1), got[0].Joinable)
	require.Equal(t, uint64(22), got[1].FriendID)
	require.Equal(t, uint8(2), got[1].StatusCode)
	require.Empty(t, got[1].Text)
}

// A stock client (social level 0) does not parse presence messages, so it is sent none; neither is a
// session whose parameters are not loaded.
func TestStockClientIsSentNoPresence(t *testing.T) {
	e := newPresenceEnv(t)
	stock := e.session("stock", false)
	friend, _ := e.friendInOpenParty(561)
	targets := []friendPresenceTarget{{userID: friend.userID, accountID: 11, online: true}}

	e.ep.sendFriendPresence(stock.Context(), loggerForTest(t), stock, targets)
	require.Empty(t, drain(stock.outgoingCh))

	runtime := e.session("runtime", true)
	e.ep.sendFriendPresence(context.Background(), loggerForTest(t), runtime, targets)
	require.Empty(t, drain(runtime.outgoingCh), "no session parameters in the context")
}

// The build a game client declared at login is what the server reports for it; a client that declared
// none (the stock game) reports an empty build.
func TestNevrRuntimeBuildIsWhatTheClientDeclared(t *testing.T) {
	require.Equal(t, "", (&SessionParameters{}).NevrRuntimeBuild(), "no login payload")
	require.Equal(t, "", (&SessionParameters{loginPayload: &evr.LoginProfile{}}).NevrRuntimeBuild(), "stock client")
	p := &SessionParameters{loginPayload: &evr.LoginProfile{NevrIdentity: &evr.NevrIdentity{Build: "v4.0.0-145-g09a0ed6-dirty"}}}
	require.Equal(t, "v4.0.0-145-g09a0ed6-dirty", p.NevrRuntimeBuild())
}
