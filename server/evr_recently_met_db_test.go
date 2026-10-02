package server

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gofrs/uuid/v5"
	"github.com/heroiclabs/nakama-common/api"
	"github.com/heroiclabs/nakama-common/runtime"
	"github.com/heroiclabs/nakama/v3/server/evr"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"
)

// The recently-met list is the game client's list of players you were in a match with. The game
// service (Nakama) keeps one storage object per player, written when that player leaves a match and
// read back when their game client asks for the list. These tests run the real storage and the real
// user_edge block lookup against the test database (NewDB skips them when none is reachable), and
// decode the game service's answer the way the game client does.

// recentlyMetEnv is a game service with a database, a session registry and the tracker the presence
// lookups read, plus a pipeline over it. Every user it makes exists in the database, so a block
// between two of them can be stored.
type recentlyMetEnv struct {
	t        *testing.T
	db       *sql.DB
	nk       *RuntimeGoNakamaModule
	ep       *EvrPipeline
	tablet   *tabletEnv
	matches  *mockFollowMatchRegistry
	nextAcct uint64
}

func newRecentlyMetEnv(t *testing.T) *recentlyMetEnv {
	t.Helper()
	db := NewDB(t)
	tablet := newTabletEnv(t)
	matches := newMockFollowMatchRegistry()
	nk := tablet.ep.nk
	nk.db = db
	nk.metrics = &testMetrics{}
	index, err := NewLocalStorageIndex(zap.NewNop(), nil, &StorageConfig{}, nk.metrics)
	require.NoError(t, err)
	nk.storageIndex = index
	nk.matchRegistry = matches
	tablet.ep.db = db
	tablet.ep.snsPartyInvites = &MapOf[uuid.UUID, *snsPartyInviteList]{}
	tablet.ep.snsPartyPolicies = &MapOf[uuid.UUID, uint8]{}
	return &recentlyMetEnv{t: t, db: db, nk: nk, ep: tablet.ep, tablet: tablet, matches: matches, nextAcct: 7000}
}

// register makes a user row (and removes it and its storage when the test ends).
func (e *recentlyMetEnv) register(id uuid.UUID) {
	e.t.Helper()
	InsertUser(e.t, e.db, id)
	e.t.Cleanup(func() {
		_, _ = e.db.Exec(`DELETE FROM storage WHERE user_id = $1`, id)
		_, _ = e.db.Exec(`DELETE FROM user_edge WHERE source_id = $1 OR destination_id = $1`, id)
		_, _ = e.db.Exec(`DELETE FROM users WHERE id = $1`, id)
	})
}

// user is a registered user the list can name, with a Discord account id and a display name.
func (e *recentlyMetEnv) user(name string) RecentlyMetUser {
	e.t.Helper()
	id := uuid.Must(uuid.NewV4())
	e.register(id)
	e.nextAcct++
	return RecentlyMetUser{UserID: id.String(), AccountID: e.nextAcct, DisplayName: name, LastMet: time.Now().UTC()}
}

// session is a connected game client for a registered user: a tracked session whose parameters
// carry the social level. It is online (a status presence) once online() is called.
func (e *recentlyMetEnv) session(name string, level int) *sessionWS {
	e.t.Helper()
	s := e.tablet.session(name, level >= 1)
	e.register(s.userID)
	return s
}

func (e *recentlyMetEnv) online(s *sessionWS) {
	e.tablet.tracker.Track(context.Background(), s.id, PresenceStream{Mode: StreamModeStatus, Subject: s.userID}, s.userID, PresenceMeta{})
}

// block stores `from` blocking `to`, the way nakama's block-friend call leaves it (user_edge state 3).
func (e *recentlyMetEnv) block(from, to string) {
	e.t.Helper()
	_, err := e.db.Exec(`INSERT INTO user_edge (source_id, destination_id, state, position, update_time)
VALUES ($1, $2, 3, $3, now())`, from, to, time.Now().UnixNano())
	require.NoError(e.t, err)
}

// stored is the list as it sits in storage, as "user:name" in list order.
func (e *recentlyMetEnv) stored(userID string) []string {
	e.t.Helper()
	list, err := readRecentlyMet(context.Background(), e.nk, userID)
	require.NoError(e.t, err)
	out := []string{}
	for _, u := range list.Users {
		out = append(out, u.UserID+":"+u.DisplayName)
	}
	return out
}

func metRow(u RecentlyMetUser) string { return u.UserID + ":" + u.DisplayName }

// What the game service stores for a list is read by the owner only and written by the server only,
// and the storage version round-trips through SetStorageMeta so a later write can be conditional.
func TestRecentlyMetListStorageMeta(t *testing.T) {
	l := &RecentlyMetList{}
	meta := l.StorageMeta()
	require.Equal(t, "RecentlyMet", meta.Collection)
	require.Equal(t, "list", meta.Key)
	require.Equal(t, runtime.STORAGE_PERMISSION_OWNER_READ, meta.PermissionRead, "the owner may read their list")
	require.Equal(t, runtime.STORAGE_PERMISSION_NO_WRITE, meta.PermissionWrite, "no game client writes it")
	require.Equal(t, "", meta.Version)

	meta.Version = "v1"
	l.SetStorageMeta(meta)
	require.Equal(t, "v1", l.StorageMeta().Version, "the version a read returned is the one the next write sends")
}

// A block is one row in user_edge (state 3). Either player blocking the other keeps them off each
// other's list, so the lookup reports both directions, and only blocks: a friendship, a pending
// invite, or a block involving someone outside the asked-about set is not reported.
func TestBlockedBetweenFindsBlocksInEitherDirectionOnly(t *testing.T) {
	e := newRecentlyMetEnv(t)
	me := e.user("me")
	iBlocked, blockedMe := e.user("i-blocked"), e.user("blocked-me")
	friend, stranger, outsider := e.user("friend"), e.user("stranger"), e.user("outsider")
	e.block(me.UserID, iBlocked.UserID)
	e.block(blockedMe.UserID, me.UserID)
	_, err := e.db.Exec(`INSERT INTO user_edge (source_id, destination_id, state, position, update_time) VALUES ($1, $2, 0, 1, now())`, me.UserID, friend.UserID)
	require.NoError(t, err)
	e.block(outsider.UserID, stranger.UserID) // a block that does not involve me

	got, err := blockedBetween(context.Background(), e.db, me.UserID,
		[]string{iBlocked.UserID, blockedMe.UserID, friend.UserID, stranger.UserID})
	require.NoError(t, err)
	require.Equal(t, map[string]bool{iBlocked.UserID: true, blockedMe.UserID: true}, got)

	none, err := blockedBetween(context.Background(), e.db, me.UserID, nil)
	require.NoError(t, err)
	require.Empty(t, none, "no one asked about, no database round trip, nothing blocked")
}

// A database that cannot answer is an error to the caller (not "nobody is blocked").
func TestBlockedBetweenReportsADatabaseError(t *testing.T) {
	e := newRecentlyMetEnv(t)
	closed := e.closedDB()
	_, err := blockedBetween(context.Background(), closed, uuid.Must(uuid.NewV4()).String(), []string{uuid.Must(uuid.NewV4()).String()})
	require.Error(t, err)
	require.Contains(t, err.Error(), "block lookup")
}

// closedDB is a second connection pool that has been closed, so any query on it fails.
func (e *recentlyMetEnv) closedDB() *sql.DB {
	e.t.Helper()
	db := NewDB(e.t)
	require.NoError(e.t, db.Close())
	return db
}

// A player who has never left a match has no list: reading it gives an empty one, not an error.
// After a leave stores people, reading it back gives them with the same names and ids, newest first.
func TestStoreRecentlyMetWritesTheListAndReadsItBack(t *testing.T) {
	e := newRecentlyMetEnv(t)
	owner := e.user("owner")
	a, b := e.user("Alice"), e.user("Bob")

	empty, err := readRecentlyMet(context.Background(), e.nk, owner.UserID)
	require.NoError(t, err)
	require.Empty(t, empty.Users, "no list yet reads as empty")

	total, err := storeRecentlyMet(context.Background(), e.nk, e.db, owner.UserID, []RecentlyMetUser{a, b})
	require.NoError(t, err)
	require.Equal(t, 2, total, "the count of the list after the write")

	list, err := readRecentlyMet(context.Background(), e.nk, owner.UserID)
	require.NoError(t, err)
	require.Len(t, list.Users, 2)
	require.Equal(t, a.UserID, list.Users[0].UserID)
	require.Equal(t, a.AccountID, list.Users[0].AccountID, "the account id is the one the game client is shown")
	require.Equal(t, "Alice", list.Users[0].DisplayName)
	require.Equal(t, b.UserID, list.Users[1].UserID)
	require.NotEmpty(t, list.version, "the read carries the storage version for the next conditional write")
}

// The owner never appears on their own list, even when the leave names them.
func TestStoreRecentlyMetNeverListsTheOwner(t *testing.T) {
	e := newRecentlyMetEnv(t)
	owner, a := e.user("owner"), e.user("Alice")
	_, err := storeRecentlyMet(context.Background(), e.nk, e.db, owner.UserID, []RecentlyMetUser{owner, a})
	require.NoError(t, err)
	require.Equal(t, []string{metRow(a)}, e.stored(owner.UserID))
}

// Meeting someone again does not add a second row: they move to the front, under the name they have
// now, and the meeting time is the newer one. Everyone else keeps their order behind them.
func TestStoreRecentlyMetMeetingAgainMovesThemToTheFrontOnce(t *testing.T) {
	e := newRecentlyMetEnv(t)
	owner := e.user("owner")
	a, b, c := e.user("Alice"), e.user("Bob"), e.user("Carol")
	old := time.Now().UTC().Add(-time.Hour).Truncate(time.Second)
	for _, u := range []*RecentlyMetUser{&a, &b, &c} {
		u.LastMet = old
	}
	_, err := storeRecentlyMet(context.Background(), e.nk, e.db, owner.UserID, []RecentlyMetUser{a, b, c})
	require.NoError(t, err)

	renamed := b
	renamed.DisplayName = "Bobby"
	renamed.LastMet = time.Now().UTC().Truncate(time.Second)
	total, err := storeRecentlyMet(context.Background(), e.nk, e.db, owner.UserID, []RecentlyMetUser{renamed})
	require.NoError(t, err)
	require.Equal(t, 3, total, "no duplicate row for Bob")
	require.Equal(t, []string{metRow(renamed), metRow(a), metRow(c)}, e.stored(owner.UserID))

	list, err := readRecentlyMet(context.Background(), e.nk, owner.UserID)
	require.NoError(t, err)
	require.True(t, list.Users[0].LastMet.After(old), "the newer meeting time is the one kept")
}

// A player the owner blocked, or who blocked the owner, is not recorded when they were in the same
// match: a block in either direction keeps them off the list.
func TestStoreRecentlyMetLeavesOutPeopleBlockedEitherWay(t *testing.T) {
	e := newRecentlyMetEnv(t)
	owner := e.user("owner")
	ok, iBlocked, blockedMe := e.user("fine"), e.user("i-blocked"), e.user("blocked-me")
	e.block(owner.UserID, iBlocked.UserID)
	e.block(blockedMe.UserID, owner.UserID)

	total, err := storeRecentlyMet(context.Background(), e.nk, e.db, owner.UserID, []RecentlyMetUser{iBlocked, ok, blockedMe})
	require.NoError(t, err)
	require.Equal(t, 1, total)
	require.Equal(t, []string{metRow(ok)}, e.stored(owner.UserID))
}

// The list holds the 50 most recent people: filling it past that drops the oldest meetings, and the
// people met last are at the front.
func TestStoreRecentlyMetKeepsTheMostRecentFifty(t *testing.T) {
	e := newRecentlyMetEnv(t)
	owner := e.user("owner")
	first := []RecentlyMetUser{}
	for i := 0; i < 30; i++ {
		first = append(first, e.user(fmt.Sprintf("first-%02d", i)))
	}
	second := []RecentlyMetUser{}
	for i := 0; i < 30; i++ {
		second = append(second, e.user(fmt.Sprintf("second-%02d", i)))
	}
	_, err := storeRecentlyMet(context.Background(), e.nk, e.db, owner.UserID, first)
	require.NoError(t, err)
	total, err := storeRecentlyMet(context.Background(), e.nk, e.db, owner.UserID, second)
	require.NoError(t, err)
	require.Equal(t, recentlyMetCap, total)

	got := e.stored(owner.UserID)
	require.Len(t, got, 50)
	require.Equal(t, metRow(second[0]), got[0], "the newest group is at the front")
	require.Equal(t, metRow(second[29]), got[29])
	require.Equal(t, metRow(first[0]), got[30], "the older group follows, in its own order")
	require.Equal(t, metRow(first[19]), got[49], "the 10 oldest were dropped")
}

// racingNK interferes with the list the moment the game service writes it, as a second game server
// leaving a match for the same player would: `before` runs ahead of the first write only.
type racingNK struct {
	*RuntimeGoNakamaModule
	writes       atomic.Int32
	before       func()
	alwaysReject bool
}

func (r *racingNK) StorageWrite(ctx context.Context, w []*runtime.StorageWrite) ([]*api.StorageObjectAck, error) {
	n := r.writes.Add(1)
	if r.alwaysReject {
		return nil, runtime.ErrStorageRejectedVersion
	}
	if n == 1 && r.before != nil {
		r.before()
	}
	return r.RuntimeGoNakamaModule.StorageWrite(ctx, w)
}

// Two leaves for one player at once: the second write finds the list changed under it and is
// rejected on its version. It reads again and adds to the changed list, so neither leave is lost.
func TestStoreRecentlyMetRetriesOnceWhenAnotherWriteGotThereFirst(t *testing.T) {
	e := newRecentlyMetEnv(t)
	owner := e.user("owner")
	a, b := e.user("Alice"), e.user("Bob")
	_, err := storeRecentlyMet(context.Background(), e.nk, e.db, owner.UserID, []RecentlyMetUser{a})
	require.NoError(t, err)

	racing := &racingNK{RuntimeGoNakamaModule: e.nk}
	racing.before = func() {
		other := e.user("Zed")
		_, werr := storeRecentlyMet(context.Background(), e.nk, e.db, owner.UserID, []RecentlyMetUser{other})
		require.NoError(t, werr)
	}
	total, err := storeRecentlyMet(context.Background(), racing, e.db, owner.UserID, []RecentlyMetUser{b})
	require.NoError(t, err)
	require.Equal(t, 3, total, "Alice, the other writer's Zed, and Bob")
	require.Equal(t, int32(2), racing.writes.Load(), "our write was rejected (the other writer's write went around racing), then retried")
	got := e.stored(owner.UserID)
	require.Len(t, got, 3)
	require.Equal(t, metRow(b), got[0])
}

// A write that is rejected again after the one retry is an error, not an endless loop.
func TestStoreRecentlyMetGivesUpAfterOneRetry(t *testing.T) {
	e := newRecentlyMetEnv(t)
	owner, a := e.user("owner"), e.user("Alice")
	racing := &racingNK{RuntimeGoNakamaModule: e.nk, alwaysReject: true}
	_, err := storeRecentlyMet(context.Background(), racing, e.db, owner.UserID, []RecentlyMetUser{a})
	require.Error(t, err)
	require.True(t, errors.Is(err, runtime.ErrStorageRejectedVersion))
	require.Equal(t, int32(2), racing.writes.Load(), "one write and one retry")
	require.Empty(t, e.stored(owner.UserID), "nothing was stored")
}

// failingReadNK cannot read storage.
type failingReadNK struct{ *RuntimeGoNakamaModule }

func (failingReadNK) StorageRead(context.Context, []*runtime.StorageRead) ([]*api.StorageObject, error) {
	return nil, errors.New("storage down")
}

// A storage failure is an error from both the read and the store; the block lookup failing stops the
// store before it writes anything (a list is never written without knowing who is blocked).
func TestStoreRecentlyMetReportsStorageAndBlockLookupFailures(t *testing.T) {
	e := newRecentlyMetEnv(t)
	owner, a := e.user("owner"), e.user("Alice")

	_, err := readRecentlyMet(context.Background(), failingReadNK{e.nk}, owner.UserID)
	require.Error(t, err, "an unreadable list is an error, not an empty list")
	_, err = storeRecentlyMet(context.Background(), failingReadNK{e.nk}, e.db, owner.UserID, []RecentlyMetUser{a})
	require.Error(t, err)

	_, err = storeRecentlyMet(context.Background(), e.nk, e.closedDB(), owner.UserID, []RecentlyMetUser{a})
	require.Error(t, err)
	require.Empty(t, e.stored(owner.UserID), "no write happened")
}

// leaveEnv is a match a player is leaving: three others were in it with them, plus one who had left
// before they arrived. Returns the match state and the people in it.
type leaveEnv struct {
	state            *MatchLabel
	leaver           *sessionWS
	alice, bob, gone RecentlyMetUser
	logs             *observer.ObservedLogs
	logger           runtime.Logger
}

func (e *recentlyMetEnv) leave(level int) *leaveEnv {
	e.t.Helper()
	leaver := e.session("leaver", level)
	e.tablet.sessions.sessions[leaver.id] = leaver
	alice, bob, gone := e.user("Alice"), e.user("Bob"), e.user("Gone")
	join := time.Now().Add(-10 * time.Minute)
	part := func(u RecentlyMetUser, joined, left time.Time) *PlayerParticipation {
		return &PlayerParticipation{UserID: u.UserID, DiscordID: fmt.Sprint(u.AccountID), DisplayName: u.DisplayName, JoinTime: joined, LeaveTime: left}
	}
	state := &MatchLabel{participations: map[string]*PlayerParticipation{
		leaver.userID.String(): {UserID: leaver.userID.String(), DiscordID: "5555", DisplayName: "leaver", JoinTime: join},
		alice.UserID:           part(alice, join.Add(-time.Hour), time.Time{}),
		bob.UserID:             part(bob, join.Add(time.Minute), time.Now().Add(-time.Minute)),
		gone.UserID:            part(gone, join.Add(-time.Hour), join.Add(-time.Minute)),
	}}
	core, logs := observer.New(zapcore.DebugLevel)
	return &leaveEnv{state: state, leaver: leaver, alice: alice, bob: bob, gone: gone, logs: logs, logger: NewRuntimeGoLogger(zap.New(core))}
}

// When a player with a runtime game client (social level 1) leaves a match, everyone whose time in the
// match overlapped theirs goes on their list: the ones still there and the ones who left after they
// arrived, not the one who left before. The game service logs how many it recorded.
func TestRecordRecentlyMetStoresWhoTheLeaverMet(t *testing.T) {
	e := newRecentlyMetEnv(t)
	l := e.leave(1)

	recordRecentlyMet(l.logger, e.nk, e.db, l.state, l.leaver.userID.String(), l.leaver.id.String())

	require.Eventually(t, func() bool { return l.logs.FilterMessage("Recently met recorded").Len() == 1 }, 10*time.Second, 20*time.Millisecond)
	got := e.stored(l.leaver.userID.String())
	require.ElementsMatch(t, []string{metRow(l.alice), metRow(l.bob)}, got, "Alice stayed, Bob left after the leaver arrived; Gone left before")
	fields := l.logs.FilterMessage("Recently met recorded").All()[0].ContextMap()
	require.EqualValues(t, 2, fields["met"])
	require.EqualValues(t, 2, fields["total"])
	require.Empty(t, e.stored(l.alice.UserID), "only the leaver's list is written; Alice's is written when she leaves")
}

// A stock game client never asks for the list, so leaving a match writes nothing for it, and the game
// service says why at debug level.
func TestRecordRecentlyMetWritesNothingForAStockGameClient(t *testing.T) {
	e := newRecentlyMetEnv(t)
	l := e.leave(0)

	recordRecentlyMet(l.logger, e.nk, e.db, l.state, l.leaver.userID.String(), l.leaver.id.String())

	require.Equal(t, 1, l.logs.FilterMessageSnippet("Recently met not recorded").Len(), "the skip is logged")
	require.Empty(t, e.stored(l.leaver.userID.String()))
	var rows int
	require.NoError(t, e.db.QueryRow(`SELECT count(*) FROM storage WHERE user_id = $1 AND collection = $2`, l.leaver.userID, StorageCollectionRecentlyMet).Scan(&rows))
	require.Zero(t, rows, "no storage object was created")
}

// A player who disconnects has no session left to ask, so nothing is recorded for the leave.
func TestRecordRecentlyMetWritesNothingForAGoneSession(t *testing.T) {
	e := newRecentlyMetEnv(t)
	l := e.leave(1)
	delete(e.tablet.sessions.sessions, l.leaver.id)

	recordRecentlyMet(l.logger, e.nk, e.db, l.state, l.leaver.userID.String(), l.leaver.id.String())

	require.Empty(t, e.stored(l.leaver.userID.String()))
	require.Equal(t, 1, l.logs.FilterMessageSnippet("Recently met not recorded").Len())
}

// Leaving a match alone, or one nobody else's time overlapped, records nothing and logs nothing.
func TestRecordRecentlyMetWritesNothingWhenNobodyWasMet(t *testing.T) {
	e := newRecentlyMetEnv(t)
	l := e.leave(1)
	l.state.participations = map[string]*PlayerParticipation{l.leaver.userID.String(): l.state.participations[l.leaver.userID.String()]}

	recordRecentlyMet(l.logger, e.nk, e.db, l.state, l.leaver.userID.String(), l.leaver.id.String())

	require.Empty(t, e.stored(l.leaver.userID.String()))
	require.Zero(t, l.logs.Len(), "no met, no write, no log")
}

// Someone who blocked the leaver (or whom the leaver blocked) is in the match but is not recorded.
func TestRecordRecentlyMetLeavesOutBlockedPlayers(t *testing.T) {
	e := newRecentlyMetEnv(t)
	l := e.leave(1)
	e.block(l.alice.UserID, l.leaver.userID.String())

	recordRecentlyMet(l.logger, e.nk, e.db, l.state, l.leaver.userID.String(), l.leaver.id.String())

	require.Eventually(t, func() bool { return l.logs.FilterMessage("Recently met recorded").Len() == 1 }, 10*time.Second, 20*time.Millisecond)
	require.Equal(t, []string{metRow(l.bob)}, e.stored(l.leaver.userID.String()))
}

// A write that fails is a warning in the game service's log, never a panic on the match loop; the
// warning names the leaver so it can be found.
func TestRecordRecentlyMetLogsAFailedWrite(t *testing.T) {
	e := newRecentlyMetEnv(t)
	l := e.leave(1)

	recordRecentlyMet(l.logger, e.nk, e.closedDB(), l.state, l.leaver.userID.String(), l.leaver.id.String())

	require.Eventually(t, func() bool { return l.logs.FilterMessage("Recently met not recorded").Len() == 1 }, 10*time.Second, 20*time.Millisecond)
	rec := l.logs.FilterMessage("Recently met not recorded").All()[0]
	require.Equal(t, zapcore.WarnLevel, rec.Level)
	require.Equal(t, l.leaver.userID.String(), rec.ContextMap()["user_id"])
	require.Empty(t, e.stored(l.leaver.userID.String()))
}

// refresh sends the game client's refresh request through the pipeline and decodes the answer.
func (e *recentlyMetEnv) refresh(viewer *sessionWS) []evr.RecentlyMetEntry {
	e.t.Helper()
	require.NoError(e.t, e.ep.snsRecentlyMetRefreshRequest(context.Background(), zap.NewNop(), viewer, &evr.SNSRecentlyMetRefreshRequest{}))
	sent := drain(viewer.outgoingCh)
	require.Len(e.t, sent, 1, "one SNSRecentlyMetListResponse")
	msgs, err := evr.ParsePacket(sent[0])
	require.NoError(e.t, err)
	require.Len(e.t, msgs, 1)
	resp, ok := msgs[0].(*evr.SNSRecentlyMetListResponse)
	require.True(e.t, ok, "got %T", msgs[0])
	return resp.Entries
}

// The game client's list shows people online first (with what they are doing), then people offline,
// each group newest meeting first. Online people carry their presence text; offline ones have none.
// A person in a party the viewer could join also carries that party and the joinable flag.
func TestRecentlyMetEntriesPutOnlinePeopleFirstWithTheirPresence(t *testing.T) {
	e := newRecentlyMetEnv(t)
	viewer := e.session("viewer", 1)
	offline1 := e.user("Offline-newest")
	idle, inLobby, partied := e.session("idle", 1), e.session("lobby", 1), e.session("partied", 1)
	offline2 := e.user("Offline-oldest")
	for _, s := range []*sessionWS{idle, inLobby, partied} {
		e.online(s)
	}
	lobby := MatchID{UUID: uuid.Must(uuid.NewV4()), Node: "testnode"}
	e.matches.SetMatch(lobby, &MatchLabel{Mode: evr.ModeSocialPublic})
	e.tablet.tracker.Track(context.Background(), inLobby.id, PresenceStream{Mode: StreamModeService, Subject: inLobby.userID, Label: StreamLabelMatchService},
		inLobby.userID, PresenceMeta{Status: lobby.String()})
	e.tablet.tabletParty(4242, partied)

	acct := map[string]uint64{}
	users := []RecentlyMetUser{offline1}
	for i, s := range []*sessionWS{idle, inLobby, partied} {
		u := RecentlyMetUser{UserID: s.userID.String(), AccountID: uint64(9100 + i), DisplayName: s.Username()}
		acct[s.Username()] = u.AccountID
		users = append(users, u)
	}
	users = append(users, offline2)

	entries := e.ep.recentlyMetEntries(context.Background(), viewer.userID, users)
	names := []string{}
	for _, en := range entries {
		names = append(names, string(en.Name))
	}
	require.Equal(t, []string{"idle", "lobby", "partied", "Offline-newest", "Offline-oldest"}, names, "online first, each group in list order")
	require.Equal(t, []uint8{0, 0, 0, 2, 2}, []uint8{entries[0].Status, entries[1].Status, entries[2].Status, entries[3].Status, entries[4].Status},
		"0 online, 2 offline")
	require.Equal(t, "In Main Menu", string(entries[0].Text))
	require.Equal(t, "Social Lobby", string(entries[1].Text))
	require.Empty(t, entries[3].Text, "offline people have no presence text")
	require.Equal(t, acct["idle"], entries[0].AccountID)
	require.Zero(t, entries[0].PartyID)
	require.Zero(t, entries[0].Joinable)
	require.EqualValues(t, 4242, entries[2].PartyID, "the open party is offered")
	require.EqualValues(t, 1, entries[2].Joinable)
}

// The refresh request is answered with the player's stored list, as the game client reads it: names
// and account ids in list order, online people first. A person who blocked the viewer (or whom the
// viewer blocked) since they met is left out, and the log says how many were dropped.
func TestRecentlyMetRefreshAnswersWithTheStoredListMinusBlocked(t *testing.T) {
	e := newRecentlyMetEnv(t)
	viewer := e.session("viewer", 1)
	a, b, c, d := e.user("Alice"), e.user("Bob"), e.user("Carol"), e.session("Dan", 1)
	dan := RecentlyMetUser{UserID: d.userID.String(), AccountID: 9999, DisplayName: "Dan"}
	e.online(d)
	_, err := storeRecentlyMet(context.Background(), e.nk, e.db, viewer.userID.String(), []RecentlyMetUser{a, b, c, dan})
	require.NoError(t, err)
	e.block(viewer.userID.String(), b.UserID) // after the meeting
	e.block(c.UserID, viewer.userID.String())

	core, logs := observer.New(zapcore.InfoLevel)
	require.NoError(t, e.ep.snsRecentlyMetRefreshRequest(context.Background(), zap.New(core), viewer, &evr.SNSRecentlyMetRefreshRequest{}))
	msgs, err := evr.ParsePacket(drain(viewer.outgoingCh)[0])
	require.NoError(t, err)
	entries := msgs[0].(*evr.SNSRecentlyMetListResponse).Entries

	got := []string{}
	for _, en := range entries {
		got = append(got, fmt.Sprintf("%s:%d:%d", en.Name, en.AccountID, en.Status))
	}
	require.Equal(t, []string{"Dan:9999:0", fmt.Sprintf("Alice:%d:2", a.AccountID)}, got,
		"Dan (online) first, then Alice; Bob and Carol are blocked")
	sent := logs.FilterMessage("Recently met list sent").All()
	require.Len(t, sent, 1)
	require.EqualValues(t, 2, sent[0].ContextMap()["count"])
	require.EqualValues(t, 1, sent[0].ContextMap()["online"])
	require.EqualValues(t, 2, sent[0].ContextMap()["blocked_dropped"])
}

// A player with no list yet is answered with an empty list (count 0), not silence.
func TestRecentlyMetRefreshWithNoListIsAnEmptyList(t *testing.T) {
	e := newRecentlyMetEnv(t)
	viewer := e.session("viewer", 1)
	require.Empty(t, e.refresh(viewer))
}

// If the list cannot be read the game client still gets an answer, an empty list, and the game
// service warns.
func TestRecentlyMetRefreshWithUnreadableStorageIsAnEmptyList(t *testing.T) {
	e := newRecentlyMetEnv(t)
	viewer := e.session("viewer", 1)
	a := e.user("Alice")
	_, err := storeRecentlyMet(context.Background(), e.nk, e.db, viewer.userID.String(), []RecentlyMetUser{a})
	require.NoError(t, err)
	e.nk.db = e.closedDB() // storage reads go through the game service's own connection
	core, logs := observer.New(zapcore.WarnLevel)

	require.NoError(t, e.ep.snsRecentlyMetRefreshRequest(context.Background(), zap.New(core), viewer, &evr.SNSRecentlyMetRefreshRequest{}))
	msgs, err := evr.ParsePacket(drain(viewer.outgoingCh)[0])
	require.NoError(t, err)
	require.Empty(t, msgs[0].(*evr.SNSRecentlyMetListResponse).Entries)
	require.Equal(t, 1, logs.FilterMessage("Recently met list unreadable").Len())
}

// If the block lookup fails the list is still sent. This pins what the code does today: it falls
// back to "nobody is blocked", so a blocked person is shown while the database cannot answer
// (server/evr_recently_met.go:245-249). The warning is logged.
func TestRecentlyMetRefreshWhenTheBlockLookupFailsSendsTheListUnfiltered(t *testing.T) {
	e := newRecentlyMetEnv(t)
	viewer := e.session("viewer", 1)
	blocked := e.user("Blocked")
	_, err := storeRecentlyMet(context.Background(), e.nk, e.db, viewer.userID.String(), []RecentlyMetUser{blocked})
	require.NoError(t, err)
	e.block(viewer.userID.String(), blocked.UserID)
	e.ep.db = e.closedDB() // only the block lookup goes through the pipeline's connection
	core, logs := observer.New(zapcore.WarnLevel)

	require.NoError(t, e.ep.snsRecentlyMetRefreshRequest(context.Background(), zap.New(core), viewer, &evr.SNSRecentlyMetRefreshRequest{}))
	msgs, err := evr.ParsePacket(drain(viewer.outgoingCh)[0])
	require.NoError(t, err)
	entries := msgs[0].(*evr.SNSRecentlyMetListResponse).Entries
	require.Len(t, entries, 1, "current behaviour: the blocked person is shown when the lookup fails")
	require.Equal(t, 1, logs.FilterMessage("Recently met block lookup failed").Len())
}
