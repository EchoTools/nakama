package server

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/gofrs/uuid/v5"
	"github.com/heroiclabs/nakama-common/rtapi"
	"github.com/heroiclabs/nakama/v3/server/evr"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

// This file pins the SNS party data the game service keeps and relays for the in-game "tablet" party
// (SNSPartyDataUpdateRequest in, SNSPartyDataNotify out). Only game clients that read party data (social
// level 1 or more, the nevr-runtime clients) are sent it; the game service fills the keys the game reads
// for matches and headsets (lobbyid, matchtype, team, lobbytype, offline, and headsettype for a member's
// own data) over whatever the client wrote under those names. Every test decodes what each session's
// outgoing queue received. The fixtures are in evr_pipeline_party_handlers_test.go.

// accountless is a session whose user has no Discord id and no device, so the game service has no account
// id to address it by on the wire.
func (e *partyHandlerEnv) accountless(name string) *sessionWS {
	e.t.Helper()
	s := newPartyMemberSession(e.t, name, e.tracker, e.pr, e.ep)
	e.insertUser(s.userID, name, "")
	params, _ := LoadParams(s.Context())
	params.loginPayload = &evr.LoginProfile{NevrSocial: 1}
	e.sessions.sessions[s.id] = s
	return s
}

// updateRequest is a party data write as the game client sends it.
func updateRequest(scope uint64, seq uint32, raw string) *evr.SNSPartyDataUpdateRequest {
	return &evr.SNSPartyDataUpdateRequest{TargetParam: scope, Seq: seq, JsonLen: uint32(len(raw)), Json: []byte(raw)}
}

// The keys the game service fills for a user's data. A user with no live session is offline and has no
// headset; one with a session is online, and a member's data carries the game's headset number for the
// headset they logged in with (Quest 3 on the standalone build, Quest on PC (Link) on the PC build). The
// party's data (not a member's) has no headsettype. With no match, the keys are the game's no-match values.
func TestPartyServerKeysOfflineAndOnline(t *testing.T) {
	e := newPartyHandlerEnv(t)
	ctx := context.Background()

	nobody := uuid.Must(uuid.NewV4())
	keys := e.ep.partyServerKeys(ctx, nobody, true)
	require.Equal(t, map[string]any{
		"lobbyid": snsPartyNoLobbyID, "matchtype": snsPartyNoMatchType, "team": snsPartyNoTeam,
		"lobbytype": int(UnassignedLobby), "offline": true, "headsettype": 0,
	}, keys)

	s := e.session("online", true)
	params, _ := LoadParams(s.Context())
	params.loginPayload.SystemInfo.HeadsetType = "Meta Quest 3"
	params.loginPayload.BuildNumber = evr.StandaloneBuildNumber
	keys = e.ep.partyServerKeys(ctx, s.userID, true)
	require.Equal(t, false, keys["offline"])
	require.Equal(t, 3, keys["headsettype"], "Quest on the standalone build")
	params.loginPayload.BuildNumber = evr.StandaloneBuildNumber + 1
	require.Equal(t, 4, e.ep.partyServerKeys(ctx, s.userID, true)["headsettype"], "Quest on the PC build is on Link")

	partyKeys := e.ep.partyServerKeys(ctx, s.userID, false)
	require.NotContains(t, partyKeys, "headsettype", "the party's data has no headset")
	require.Equal(t, false, partyKeys["offline"])
}

// A user in a match has the match's lobby id (upper case), its mode, the lobby type and their team in
// their keys, so the party's tablet can show where each member is.
func TestPartyServerKeysCarryTheMatchTheUserIsIn(t *testing.T) {
	e := newPartyHandlerEnv(t)
	s := e.session("player", true)
	registry := newMockFollowMatchRegistry()
	e.ep.nk.matchRegistry = registry
	matchID := MatchID{UUID: uuid.Must(uuid.NewV4()), Node: "testnode"}
	registry.SetMatch(matchID, &MatchLabel{
		ID: matchID, Mode: evr.ModeArenaPublic, LobbyType: PublicLobby,
		Players: []PlayerInfo{{UserID: uuid.Must(uuid.NewV4()).String(), Team: TeamIndex(evr.TeamBlue)}, {UserID: s.userID.String(), Team: TeamIndex(evr.TeamOrange)}},
	})
	e.tracker.Track(context.Background(), s.id,
		PresenceStream{Mode: StreamModeService, Subject: s.userID, Label: StreamLabelMatchService},
		s.userID, PresenceMeta{Status: matchID.String()})

	keys := e.ep.partyServerKeys(context.Background(), s.userID, true)
	require.Equal(t, strings.ToUpper(matchID.UUID.String()), keys["lobbyid"])
	require.Equal(t, int64(evr.ModeArenaPublic), keys["matchtype"])
	require.Equal(t, int(PublicLobby), keys["lobbytype"])
	require.Equal(t, int(evr.TeamOrange), keys["team"], "the user's own team, not the other player's")
}

// partyDataNotify builds what one scope's SNSPartyDataNotify carries: the stored script keys with the
// game service's keys over them and the writer's seq. The party scope is always member id 0 (0 is the
// party's data on the wire); a member's scope needs an account id, since 0 would be read as the party's.
func TestPartyDataNotifyBuildsOneScope(t *testing.T) {
	e := newPartyHandlerEnv(t)
	s := e.session("writer", true)
	state := newSNSPartyDataState()
	state.store(snsPartyDataScopeParty, s.id, 4, map[string]any{"mode": "arena", "offline": true, "team": 7})
	state.store(snsPartyDataScopeMember, s.id, 9, map[string]any{"ready": true})
	ctx := context.Background()

	n, err := e.ep.partyDataNotify(ctx, state, 5, snsPartyDataScopeParty, s.id, s.userID, 12345)
	require.NoError(t, err)
	require.Equal(t, uint64(5), n.PartyID)
	require.Zero(t, n.MemberID, "the party scope is sent as member 0 whatever id was passed")
	require.EqualValues(t, 4, n.Seq)
	keys := dataNotifyJSON(t, n)
	require.Equal(t, "arena", keys["mode"], "the writer's own keys are kept")
	require.Equal(t, false, keys["offline"], "the game service's keys win over the client's")
	require.EqualValues(t, snsPartyNoTeam, keys["team"])
	require.NotContains(t, keys, "headsettype")

	n, err = e.ep.partyDataNotify(ctx, state, 5, snsPartyDataScopeMember, s.id, s.userID, e.accounts[s])
	require.NoError(t, err)
	require.Equal(t, e.accounts[s], n.MemberID)
	require.EqualValues(t, 9, n.Seq)
	keys = dataNotifyJSON(t, n)
	require.Equal(t, true, keys["ready"])
	require.Contains(t, keys, "headsettype")

	_, err = e.ep.partyDataNotify(ctx, state, 5, snsPartyDataScopeMember, s.id, s.userID, 0)
	require.Error(t, err, "a member with no account id cannot be addressed")
}

// The party's data state is created by its first write and the same state is found after; reading it
// before any write gives an empty state that is not kept, so a read never creates party data.
func TestPartyDataStateIsCreatedByTheFirstWrite(t *testing.T) {
	e := newPartyHandlerEnv(t)
	party := uuid.Must(uuid.NewV4())

	empty := e.ep.partyDataStored(party)
	require.NotNil(t, empty)
	_, kept := e.ep.snsPartyData.Load(party)
	require.False(t, kept, "reading the data must not create it")
	data, seq := empty.snapshot(snsPartyDataScopeParty, uuid.Nil)
	require.Empty(t, data)
	require.Zero(t, seq)

	created := e.ep.partyDataState(party)
	require.Same(t, created, e.ep.partyDataState(party), "the second call finds the first's state")
	require.Same(t, created, e.ep.partyDataStored(party), "and so does a read")
}

// partyMembers lists the party's sessions on this node. It looks up account ids (a database lookup each)
// only when some member's client reads party data: a party of stock clients costs no lookups, here proved
// by a database that could not answer. A session the registry no longer has is not listed but is still
// counted as present, and the stored data of sessions that have left the party is dropped.
func TestPartyMembersListsSessionsAndResolvesAccountsOnlyForReaders(t *testing.T) {
	e := newPartyHandlerEnv(t)
	stockA, stockB := e.session("stocka", false), e.session("stockb", false)
	ph := e.party(100, stockA, stockB)
	good := e.ep.db
	dead := NewDB(t)
	require.NoError(t, dead.Close())
	e.ep.db = dead

	members, readers := e.ep.partyMembers(context.Background(), loggerForTest(t), ph.ID)
	require.False(t, readers)
	require.Len(t, members, 2)
	for _, m := range members {
		require.Zero(t, m.accountID, "no account lookup for a party nobody reads")
		require.Zero(t, m.level)
	}

	// A runtime client joins the party: accounts are resolved for everyone.
	e.ep.db = good
	reader := e.session("reader", true)
	e.tracker.Track(context.Background(), reader.id, PresenceStream{Mode: StreamModeParty, Subject: ph.ID, Label: "testnode"}, reader.userID, PresenceMeta{})
	// A presence whose session the registry has lost, and stored data for a session that has left.
	gone := uuid.Must(uuid.NewV4())
	e.tracker.Track(context.Background(), gone, PresenceStream{Mode: StreamModeParty, Subject: ph.ID, Label: "testnode"}, uuid.Must(uuid.NewV4()), PresenceMeta{})
	state := e.ep.partyDataState(ph.ID)
	left := uuid.Must(uuid.NewV4())
	state.store(snsPartyDataScopeMember, left, 1, map[string]any{"a": 1})
	state.store(snsPartyDataScopeMember, reader.id, 1, map[string]any{"b": 1})
	state.store(snsPartyDataScopeMember, gone, 1, map[string]any{"c": 1})

	members, readers = e.ep.partyMembers(context.Background(), loggerForTest(t), ph.ID)
	require.True(t, readers)
	require.Len(t, members, 3, "the session the registry lost is not listed")
	byUser := map[uuid.UUID]snsPartyMember{}
	for _, m := range members {
		byUser[m.userID] = m
	}
	require.Equal(t, e.accounts[reader], byUser[reader.userID].accountID)
	require.Equal(t, 1, byUser[reader.userID].level)
	require.Equal(t, e.accounts[stockA], byUser[stockA.userID].accountID, "the stock members' ids are resolved too once someone reads")
	data, _ := state.snapshot(snsPartyDataScopeMember, left)
	require.Empty(t, data, "the data of a session that left is dropped")
	data, _ = state.snapshot(snsPartyDataScopeMember, reader.id)
	require.NotEmpty(t, data)
	data, _ = state.snapshot(snsPartyDataScopeMember, gone)
	require.NotEmpty(t, data, "a present session is kept even when the registry has lost it")
}

// failingSession is a session whose queue is always full: every send fails.
type failingSession struct {
	Session
	id uuid.UUID
}

func (f *failingSession) ID() uuid.UUID       { return f.id }
func (f *failingSession) Logger() *zap.Logger { return zap.NewNop() }
func (f *failingSession) SendBytes([]byte, bool) error {
	return fmt.Errorf("queue full")
}

// sendPartyData sends the notifies, in order, to the members whose client reads them (social level 1 or
// more), skipping the excluded session; it returns how many members were sent them. A member whose send
// fails is skipped and the rest still get theirs.
func TestSendPartyDataGatesOnTheClientsSocialLevel(t *testing.T) {
	e := newPartyHandlerEnv(t)
	reader, other, stock, excluded := e.session("reader", true), e.session("other", true), e.session("stock", false), e.session("excluded", true)
	failing := &failingSession{id: uuid.Must(uuid.NewV4())}
	members := []snsPartyMember{
		{session: reader, userID: reader.userID, level: 1},
		{session: stock, userID: stock.userID, level: 0},
		{session: excluded, userID: excluded.userID, level: 1},
		{session: failing, userID: uuid.Must(uuid.NewV4()), level: 1},
		{session: other, userID: other.userID, level: 2},
	}
	a, b := &evr.SNSPartyDataNotify{PartyID: 1, MemberID: 0, Json: []byte(`{}`)}, &evr.SNSPartyDataNotify{PartyID: 1, MemberID: 9, Json: []byte(`{}`)}

	sent := sendPartyData(loggerForTest(t), members, excluded.id, a, b)

	require.Equal(t, 2, sent, "the reader and the level 2 client; not the stock, excluded or failing")
	for _, s := range []*sessionWS{reader, other} {
		got := sentTo(t, s)
		require.Len(t, got, 2)
		require.EqualValues(t, 0, got[0].(*evr.SNSPartyDataNotify).MemberID, "in the order given")
		require.EqualValues(t, 9, got[1].(*evr.SNSPartyDataNotify).MemberID)
	}
	require.Empty(t, sentTo(t, stock), "a stock client is sent no party data")
	require.Empty(t, sentTo(t, excluded))
	require.Zero(t, sendPartyData(loggerForTest(t), members, uuid.Nil), "nothing to send")
}

// The party leader writes the party's data: the game service stores it, sends it to every other member
// whose client reads it (the leader, who wrote it, and a stock member are sent none) with the game
// service's keys over the leader's, and answers the leader SNSPartyUpdateSuccess.
func TestSNSPartyDataUpdateByTheLeaderIsRelayedToReaders(t *testing.T) {
	e := newPartyHandlerEnv(t)
	leader, reader, stock := e.session("leader", true), e.session("reader", true), e.session("stock", false)
	ph := e.party(110, leader, reader, stock)

	require.NoError(t, e.ep.snsPartyDataUpdateRequest(leader.Context(), loggerForTest(t), leader,
		updateRequest(snsPartyDataScopeParty, 1, `{"mode":"arena","offline":true}`)))

	require.Equal(t, []evr.Message{&evr.SNSPartyUpdateSuccess{PartyID: 110}}, sentTo(t, leader))
	got := sentTo(t, reader)
	require.Equal(t, []string{typeDataNotify}, typeNames(got))
	n := got[0].(*evr.SNSPartyDataNotify)
	require.EqualValues(t, 110, n.PartyID)
	require.Zero(t, n.MemberID)
	require.EqualValues(t, 1, n.Seq)
	keys := dataNotifyJSON(t, n)
	require.Equal(t, "arena", keys["mode"])
	require.Equal(t, false, keys["offline"], "the game service's offline replaces the client's")
	require.Empty(t, sentTo(t, stock))
	stored, seq := e.ep.partyDataStored(ph.ID).snapshot(snsPartyDataScopeParty, leader.id)
	require.EqualValues(t, 1, seq)
	require.Equal(t, "arena", stored["mode"])
}

// A member writes their own data: the others (readers only) are sent it as that member's data, by account
// id, with the member's headsettype filled in, and the member is answered SNSPartyUpdateMemberSuccess.
func TestSNSPartyDataUpdateByAMemberIsRelayedAsTheirData(t *testing.T) {
	e := newPartyHandlerEnv(t)
	leader, member := e.session("leader", true), e.session("member", true)
	e.party(111, leader, member)

	require.NoError(t, e.ep.snsPartyDataUpdateRequest(member.Context(), loggerForTest(t), member,
		updateRequest(snsPartyDataScopeMember, 3, `{"ready":true}`)))

	require.Equal(t, []evr.Message{&evr.SNSPartyUpdateMemberSuccess{PartyID: 111}}, sentTo(t, member))
	got := sentTo(t, leader)
	require.Equal(t, []string{typeDataNotify}, typeNames(got))
	n := got[0].(*evr.SNSPartyDataNotify)
	require.Equal(t, e.accounts[member], n.MemberID)
	require.EqualValues(t, 3, n.Seq)
	keys := dataNotifyJSON(t, n)
	require.Equal(t, true, keys["ready"])
	require.Contains(t, keys, "headsettype")
}

// A client counts its own writes (seq). A write no newer than the stored one from the same session is
// dropped: it is answered success, nothing is stored and nobody is sent it. A write from a new session
// (a new leader) starts over, whatever its seq.
func TestSNSPartyDataUpdateDropsAStaleWrite(t *testing.T) {
	e := newPartyHandlerEnv(t)
	leader, reader := e.session("leader", true), e.session("reader", true)
	ph := e.party(112, leader, reader)
	send := func(s *sessionWS, seq uint32, raw string) {
		require.NoError(t, e.ep.snsPartyDataUpdateRequest(s.Context(), loggerForTest(t), s, updateRequest(snsPartyDataScopeParty, seq, raw)))
	}

	send(leader, 5, `{"v":"new"}`)
	require.Len(t, sentTo(t, reader), 1)
	sentTo(t, leader)

	send(leader, 3, `{"v":"old"}`)
	require.Equal(t, []evr.Message{&evr.SNSPartyUpdateSuccess{PartyID: 112}}, sentTo(t, leader), "a stale write is still answered success")
	require.Empty(t, sentTo(t, reader), "and relayed to nobody")
	stored, seq := e.ep.partyDataStored(ph.ID).snapshot(snsPartyDataScopeParty, leader.id)
	require.EqualValues(t, 5, seq)
	require.Equal(t, "new", stored["v"])

	// The reader becomes the leader and writes seq 1: its session is new, so the write is kept.
	require.NoError(t, e.pr.PartyPromote(context.Background(), ph.ID, "testnode", leader.id.String(), "testnode",
		&rtapi.UserPresence{UserId: reader.userID.String(), SessionId: reader.id.String(), Username: reader.Username()}))
	send(reader, 1, `{"v":"from the new leader"}`)
	stored, seq = e.ep.partyDataStored(ph.ID).snapshot(snsPartyDataScopeParty, reader.id)
	require.EqualValues(t, 1, seq)
	require.Equal(t, "from the new leader", stored["v"])
	require.Equal(t, []string{typeDataNotify}, typeNames(sentTo(t, leader)), "the old leader is sent the new leader's data")
}

// Writes the game service refuses are answered with the failure that matches the scope (a party write
// SNSPartyUpdateFailure, a member write SNSPartyUpdateMemberFailure), nothing is stored and nobody else
// is sent anything: 1 for no party (or one the game service lost) or a member with no account id; 2 for a
// scope the game does not have, or a party write from a member who is not the leader; 3 for JSON that is
// not an object or is over 4096 bytes.
func TestSNSPartyDataUpdateRefusals(t *testing.T) {
	e := newPartyHandlerEnv(t)
	leader, member := e.session("leader", true), e.session("member", true)
	nameless := e.accountless("nameless")
	ph := e.party(113, leader, member, nameless)
	solo := e.session("solo", true)
	lost := e.session("lost", true)
	lostParams, _ := LoadParams(lost.Context())
	lostParams.currentPartyID = uuid.Must(uuid.NewV4())

	partyFail := func(code uint8) evr.Message { return &evr.SNSPartyUpdateFailure{ErrorCode: code} }
	memberFail := func(code uint8) evr.Message { return &evr.SNSPartyUpdateMemberFailure{ErrorCode: code} }
	cases := []struct {
		name string
		from *sessionWS
		req  *evr.SNSPartyDataUpdateRequest
		want evr.Message
	}{
		{"party write, in no party", solo, updateRequest(snsPartyDataScopeParty, 1, `{}`), partyFail(1)},
		{"member write, in no party", solo, updateRequest(snsPartyDataScopeMember, 1, `{}`), memberFail(1)},
		{"party the game service lost", lost, updateRequest(snsPartyDataScopeParty, 1, `{}`), partyFail(1)},
		{"unknown scope", leader, updateRequest(2, 1, `{}`), partyFail(2)},
		{"party write by a member", member, updateRequest(snsPartyDataScopeParty, 1, `{"a":1}`), partyFail(2)},
		{"not JSON", leader, updateRequest(snsPartyDataScopeParty, 1, `nope`), partyFail(3)},
		{"JSON array", leader, updateRequest(snsPartyDataScopeParty, 1, `[1,2]`), partyFail(3)},
		{"JSON null", member, updateRequest(snsPartyDataScopeMember, 1, `null`), memberFail(3)},
		{"over 4096 bytes", member, updateRequest(snsPartyDataScopeMember, 1, `{"k":"`+strings.Repeat("x", snsPartyDataMaxBytes)+`"}`), memberFail(3)},
		{"member write with no account id", nameless, updateRequest(snsPartyDataScopeMember, 1, `{"a":1}`), memberFail(1)},
	}
	for _, c := range cases {
		require.NoError(t, e.ep.snsPartyDataUpdateRequest(c.from.Context(), loggerForTest(t), c.from, c.req), c.name)
		require.Equal(t, []evr.Message{c.want}, sentTo(t, c.from), c.name)
		for _, other := range []*sessionWS{leader, member, nameless} {
			if other != c.from {
				require.Empty(t, sentTo(t, other), "%s: others are sent nothing", c.name)
			}
		}
	}
	_, stored := e.ep.snsPartyData.Load(ph.ID)
	if stored {
		data, _ := e.ep.partyDataStored(ph.ID).snapshot(snsPartyDataScopeParty, leader.id)
		require.Empty(t, data, "a refused write is not stored")
	}

	require.Error(t, e.ep.snsPartyDataUpdateRequest(leader.Context(), loggerForTest(t), leader, &evr.SNSPartyLockRequest{}))
}

// Called after a joiner is on the party stream and before the join is announced: the joiner is sent the
// party's data (the leader's, unless the joiner leads) and then every other member's own data; the other
// members are sent the joiner's. A member the game service has no account id for is skipped, not fatal.
func TestSNSPartyDataJoiningSendsTheJoinerAndTheOthersTheirData(t *testing.T) {
	e := newPartyHandlerEnv(t)
	leader, member, nameless, joiner := e.session("leader", true), e.session("member", true), e.accountless("nameless"), e.session("joiner", true)
	ph := e.party(120, leader, member, nameless, joiner)
	state := e.ep.partyDataState(ph.ID)
	state.store(snsPartyDataScopeMember, member.id, 2, map[string]any{"ready": true})

	e.ep.snsPartyDataJoining(context.Background(), loggerForTest(t), joiner, ph.ID, 120)

	got := sentTo(t, joiner)
	require.Equal(t, []string{typeDataNotify, typeDataNotify, typeDataNotify}, typeNames(got), "party, then the leader's and the member's own")
	require.Zero(t, got[0].(*evr.SNSPartyDataNotify).MemberID, "the party's data first")
	ids := []uint64{got[1].(*evr.SNSPartyDataNotify).MemberID, got[2].(*evr.SNSPartyDataNotify).MemberID}
	require.ElementsMatch(t, []uint64{e.accounts[leader], e.accounts[member]}, ids, "the member with no account id is skipped")
	for _, existing := range []*sessionWS{leader, member} {
		got = sentTo(t, existing)
		require.Len(t, got, 1)
		require.Equal(t, e.accounts[joiner], got[0].(*evr.SNSPartyDataNotify).MemberID, "the joiner's data")
	}
}

// A joiner who is the party's leader is not sent the party's data (it wrote it); it still gets the members'.
// With nobody in the party reading party data, or a party the registry does not have, nothing is sent.
func TestSNSPartyDataJoiningSpecialCases(t *testing.T) {
	e := newPartyHandlerEnv(t)
	leader, member := e.session("leader", true), e.session("member", true)
	ph := e.party(121, leader, member)

	e.ep.snsPartyDataJoining(context.Background(), loggerForTest(t), leader, ph.ID, 121)
	got := sentTo(t, leader)
	require.Len(t, got, 1, "only the other member's data, not the party's")
	require.Equal(t, e.accounts[member], got[0].(*evr.SNSPartyDataNotify).MemberID)
	require.Len(t, sentTo(t, member), 1)

	stockLeader, stockJoiner := e.session("stockleader", false), e.session("stockjoiner", false)
	stockParty := e.party(122, stockLeader, stockJoiner)
	e.ep.snsPartyDataJoining(context.Background(), loggerForTest(t), stockJoiner, stockParty.ID, 122)
	require.Empty(t, sentTo(t, stockLeader))
	require.Empty(t, sentTo(t, stockJoiner))

	e.ep.snsPartyDataJoining(context.Background(), loggerForTest(t), leader, uuid.Must(uuid.NewV4()), 123)
	require.Empty(t, sentTo(t, leader))
}

// When a member enters a match, their data is re-sent to the whole party (readers only) so the match keys
// follow them: a leader's party data and their own member data, a plain member's own member data alone.
// The data goes to every reader, the sender included, and to no stock client.
func TestSNSPartyDataMatchChangedResendsTheMembersData(t *testing.T) {
	e := newPartyHandlerEnv(t)
	leader, member, stock := e.session("leader", true), e.session("member", true), e.session("stock", false)
	e.party(130, leader, member, stock)

	e.ep.snsPartyDataMatchChanged(context.Background(), loggerForTest(t), leader)
	for _, reader := range []*sessionWS{leader, member} {
		got := sentTo(t, reader)
		require.Equal(t, []string{typeDataNotify, typeDataNotify}, typeNames(got))
		require.Zero(t, got[0].(*evr.SNSPartyDataNotify).MemberID, "the party's data, then the leader's own")
		require.Equal(t, e.accounts[leader], got[1].(*evr.SNSPartyDataNotify).MemberID)
	}
	require.Empty(t, sentTo(t, stock))

	e.ep.snsPartyDataMatchChanged(context.Background(), loggerForTest(t), member)
	for _, reader := range []*sessionWS{leader, member} {
		got := sentTo(t, reader)
		require.Len(t, got, 1, "a member re-sends only their own data")
		require.Equal(t, e.accounts[member], got[0].(*evr.SNSPartyDataNotify).MemberID)
	}
	require.Empty(t, sentTo(t, stock))
}

// Nothing is sent when the player is in no SNS party, the party is gone from the registry, or nobody in
// it reads party data.
func TestSNSPartyDataMatchChangedWithNobodyToTell(t *testing.T) {
	e := newPartyHandlerEnv(t)
	solo := e.session("solo", true)
	e.ep.snsPartyDataMatchChanged(context.Background(), loggerForTest(t), solo)
	require.Empty(t, sentTo(t, solo))

	params, _ := LoadParams(solo.Context())
	params.currentPartyID, params.currentSNSPartyID = uuid.Must(uuid.NewV4()), 9 // not in the registry
	e.ep.snsPartyDataMatchChanged(context.Background(), loggerForTest(t), solo)
	require.Empty(t, sentTo(t, solo))

	a, b := e.session("a", false), e.session("b", false)
	e.party(131, a, b)
	e.ep.snsPartyDataMatchChanged(context.Background(), loggerForTest(t), a)
	require.Empty(t, sentTo(t, a))
	require.Empty(t, sentTo(t, b))
}
