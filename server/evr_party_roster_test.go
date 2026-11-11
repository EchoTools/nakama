package server

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/gofrs/uuid/v5"
	"github.com/heroiclabs/nakama-common/runtime"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
	"google.golang.org/grpc/codes"

	"github.com/heroiclabs/nakama/v3/server/evr"
)

// rosterTestNK is the storage double for roster tests: the OCC double plus the
// two calls it does not model, delete and the presence list party/mine reads.
// It overrides neither StorageWrite nor MultiUpdate (AGENTS.md defect class 1).
type rosterTestNK struct {
	*occTestNakamaModule
	online map[string]bool

	beforeDelete func()
}

func newRosterTestNK() *rosterTestNK {
	return &rosterTestNK{occTestNakamaModule: newOCCTestNakamaModule(), online: map[string]bool{}}
}

// StorageDelete answers a versioned delete that matched nothing the way the storage layer does
// (core_storage.go, rowsAffected == 0): a StatusError(InvalidArgument), NOT runtime.ErrStorageRejectedVersion.
// beforeDelete, if set, runs once before the first delete, to land a concurrent write in the gap between
// the caller's read and its delete.
func (n *rosterTestNK) StorageDelete(_ context.Context, deletes []*runtime.StorageDelete) error {
	if hook := n.beforeDelete; hook != nil {
		n.beforeDelete = nil
		hook()
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	for _, d := range deletes {
		k := occStorageKey(d.UserID, d.Collection, d.Key)
		cur, ok := n.objects[k]
		if d.Version != "" && d.Version != "*" && (!ok || cur.Version != d.Version) {
			return StatusError(codes.InvalidArgument, "Storage delete rejected.", errors.New("Storage delete rejected - not found, version check failed, or permission denied."))
		}
		delete(n.objects, k)
	}
	return nil
}

func (n *rosterTestNK) StreamUserList(mode uint8, subject, _, _ string, _, _ bool) ([]runtime.Presence, error) {
	if mode == StreamModeNotifications && n.online[subject] {
		return []runtime.Presence{&Presence{}}, nil
	}
	return nil, nil
}

func (n *rosterTestNK) roster(t *testing.T, partyID string) *PartyRoster {
	t.Helper()
	objs, err := n.StorageRead(context.Background(), []*runtime.StorageRead{{Collection: PartyRosterCollection, Key: partyID, UserID: SystemUserID}})
	require.NoError(t, err)
	if len(objs) == 0 {
		return nil
	}
	r := &PartyRoster{}
	require.NoError(t, json.Unmarshal([]byte(objs[0].Value), r))
	return r
}

func (n *rosterTestNK) rawRoster(t *testing.T, partyID string) string {
	t.Helper()
	objs, err := n.StorageRead(context.Background(), []*runtime.StorageRead{{Collection: PartyRosterCollection, Key: partyID, UserID: SystemUserID}})
	require.NoError(t, err)
	require.Len(t, objs, 1)
	return objs[0].Value
}

func (n *rosterTestNK) pointer(t *testing.T, userID string) string {
	t.Helper()
	objs, err := n.StorageRead(context.Background(), []*runtime.StorageRead{{Collection: PartyRosterPointerCollection, Key: partyRosterPointerKey, UserID: userID}})
	require.NoError(t, err)
	if len(objs) == 0 {
		return ""
	}
	ptr := partyRosterPointer{}
	require.NoError(t, json.Unmarshal([]byte(objs[0].Value), &ptr))
	return ptr.PartyID
}

func rosterMember(name string) (string, PartyRosterMember) {
	id := uuid.Must(uuid.NewV4()).String()
	return id, PartyRosterMember{UserID: id, Username: name}
}

func memberIDs(r *PartyRoster) []string {
	if r == nil {
		return nil
	}
	ids := make([]string, 0, len(r.Members))
	for _, m := range r.Members {
		ids = append(ids, m.UserID)
	}
	return ids
}

func TestPartyRosterJoinThenMine(t *testing.T) {
	nk := newRosterTestNK()
	store := NewPartyRosterStore(nk)
	partyID := uuid.Must(uuid.NewV4()).String()
	aID, a := rosterMember("a")
	bID, b := rosterMember("b")

	require.NoError(t, store.Join(partyID, "", a))
	require.NoError(t, store.Join(partyID, "", b))

	got, err := store.Mine(context.Background(), bID)
	require.NoError(t, err)
	require.NotNil(t, got)
	require.Equal(t, partyID, got.PartyID)
	require.Equal(t, []string{aID, bID}, memberIDs(got))
	require.Equal(t, aID, got.LeaderID, "the first member of a new roster leads it until told otherwise")
	require.False(t, got.UpdatedAt.IsZero())
}

func TestPartyRosterJoinTakesTheLeaderTheGameReports(t *testing.T) {
	nk := newRosterTestNK()
	store := NewPartyRosterStore(nk)
	partyID := uuid.Must(uuid.NewV4()).String()
	aID, a := rosterMember("a")
	bID, b := rosterMember("b")

	require.NoError(t, store.Join(partyID, aID, a))
	require.NoError(t, store.Join(partyID, bID, b))

	require.Equal(t, bID, nk.roster(t, partyID).LeaderID)
}

func TestPartyRosterLeaveRepairsTheLeaderAndDeletesAnEmptyRoster(t *testing.T) {
	nk := newRosterTestNK()
	store := NewPartyRosterStore(nk)
	partyID := uuid.Must(uuid.NewV4()).String()
	aID, a := rosterMember("a")
	bID, b := rosterMember("b")
	require.NoError(t, store.Join(partyID, "", a))
	require.NoError(t, store.Join(partyID, "", b))

	require.NoError(t, store.Leave(partyID, aID, ""))
	r := nk.roster(t, partyID)
	require.Equal(t, []string{bID}, memberIDs(r))
	require.Equal(t, bID, r.LeaderID, "the leader left: the remaining member leads")
	require.Empty(t, nk.pointer(t, aID), "the leaver no longer points at the party")
	require.Equal(t, partyID, nk.pointer(t, bID))

	require.NoError(t, store.Leave(partyID, bID, ""))
	require.Nil(t, nk.roster(t, partyID), "the last member left: the roster goes")
	got, err := store.Mine(context.Background(), bID)
	require.NoError(t, err)
	require.Nil(t, got)
}

func TestPartyRosterLeaveTakesTheGamesLeaderWhenItIsAMember(t *testing.T) {
	nk := newRosterTestNK()
	store := NewPartyRosterStore(nk)
	partyID := uuid.Must(uuid.NewV4()).String()
	aID, a := rosterMember("a")
	bID, b := rosterMember("b")
	cID, c := rosterMember("c")
	for _, m := range []PartyRosterMember{a, b, c} {
		require.NoError(t, store.Join(partyID, "", m))
	}

	require.NoError(t, store.Leave(partyID, aID, cID))

	require.Equal(t, cID, nk.roster(t, partyID).LeaderID, "the game promoted c, not the next in the roster")
	require.Equal(t, []string{bID, cID}, memberIDs(nk.roster(t, partyID)))
}

// Joining a party leaves the one the member was in, as it does in game.
func TestPartyRosterJoiningAnotherPartyLeavesThePrevious(t *testing.T) {
	nk := newRosterTestNK()
	store := NewPartyRosterStore(nk)
	oldParty := uuid.Must(uuid.NewV4()).String()
	newParty := uuid.Must(uuid.NewV4()).String()
	aID, a := rosterMember("a")
	bID, b := rosterMember("b")
	require.NoError(t, store.Join(oldParty, "", a))
	require.NoError(t, store.Join(oldParty, "", b))

	require.NoError(t, store.Join(newParty, "", a))

	require.Equal(t, []string{bID}, memberIDs(nk.roster(t, oldParty)), "a left the old roster")
	require.Equal(t, []string{aID}, memberIDs(nk.roster(t, newParty)))
	require.Equal(t, newParty, nk.pointer(t, aID))
	require.Equal(t, oldParty, nk.pointer(t, bID), "b was not touched")

	// Leaving the old party afterwards (a late close handler) must not take a's new pointer.
	require.NoError(t, store.Leave(oldParty, aID, ""))
	require.Equal(t, newParty, nk.pointer(t, aID))
}

// The pointer is a hint: a roster that does not list the user is not their party.
func TestPartyRosterMineIgnoresAPointerToARosterThatDoesNotListTheUser(t *testing.T) {
	nk := newRosterTestNK()
	store := NewPartyRosterStore(nk)
	partyID := uuid.Must(uuid.NewV4()).String()
	_, a := rosterMember("a")
	strangerID, _ := rosterMember("stranger")
	require.NoError(t, store.Join(partyID, "", a))

	pointer, err := json.Marshal(partyRosterPointer{PartyID: partyID})
	require.NoError(t, err)
	nk.seedObject(strangerID, PartyRosterPointerCollection, partyRosterPointerKey, string(pointer))

	got, err := store.Mine(context.Background(), strangerID)
	require.NoError(t, err)
	require.Nil(t, got)
}

func TestPartyRosterSetLeader(t *testing.T) {
	nk := newRosterTestNK()
	store := NewPartyRosterStore(nk)
	partyID := uuid.Must(uuid.NewV4()).String()
	_, a := rosterMember("a")
	bID, b := rosterMember("b")
	require.NoError(t, store.Join(partyID, "", a))
	require.NoError(t, store.Join(partyID, "", b))

	require.NoError(t, store.SetLeader(partyID, bID))
	require.Equal(t, bID, nk.roster(t, partyID).LeaderID)

	before := nk.roster(t, partyID)
	require.NoError(t, store.SetLeader(partyID, uuid.Must(uuid.NewV4()).String()))
	require.Equal(t, before, nk.roster(t, partyID), "a non-member cannot be made leader")
}

// Concurrent joins to one roster must all land: the write is optimistic and retried.
func TestPartyRosterConcurrentJoinsAllLand(t *testing.T) {
	nk := newRosterTestNK()
	store := NewPartyRosterStore(nk)
	partyID := uuid.Must(uuid.NewV4()).String()

	const joiners = 4 // fewer than partyRosterWriteAttempts, so a retry always has room
	start := make(chan struct{})
	var wg sync.WaitGroup
	errs := make(chan error, joiners)
	for i := range joiners {
		_, m := rosterMember(string(rune('a' + i)))
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			errs <- store.Join(partyID, "", m)
		}()
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}
	require.Len(t, nk.roster(t, partyID).Members, joiners, "a joiner's write was lost to a concurrent one")
}

func TestPartyRosterNilStoreRecordsNothing(t *testing.T) {
	var store *PartyRosterStore
	_, a := rosterMember("a")
	require.NoError(t, store.Join("p", "", a))
	require.NoError(t, store.Leave("p", a.UserID, ""))
	require.NoError(t, store.SetLeader("p", a.UserID))
}

// The last member leaving deletes the roster with a versioned delete. When that loses to a write that
// landed in between, the storage layer answers with a StatusError, not ErrStorageRejectedVersion; the leave
// must read again and finish, not stop with the leaver still listed.
func TestPartyRosterLastLeaveRetriesAConflictedDelete(t *testing.T) {
	nk := newRosterTestNK()
	store := NewPartyRosterStore(nk)
	partyID := uuid.Must(uuid.NewV4()).String()
	aID, a := rosterMember("a")
	require.NoError(t, store.Join(partyID, "", a))

	// Something rewrites the roster (same members) between the leave's read and its delete.
	nk.beforeDelete = func() {
		nk.seedObject(SystemUserID, PartyRosterCollection, partyID, nk.rawRoster(t, partyID))
	}

	require.NoError(t, store.Leave(partyID, aID, ""))

	require.Nil(t, nk.roster(t, partyID), "the leaver stayed listed after a conflicted delete")
	require.Empty(t, nk.pointer(t, aID))
}

// A member who joins while the last member's delete is in flight keeps the party: the retry sees them.
func TestPartyRosterLeaveRetryKeepsAMemberWhoJoinedMeanwhile(t *testing.T) {
	nk := newRosterTestNK()
	store := NewPartyRosterStore(nk)
	partyID := uuid.Must(uuid.NewV4()).String()
	aID, a := rosterMember("a")
	bID, b := rosterMember("b")
	require.NoError(t, store.Join(partyID, "", a))

	nk.beforeDelete = func() {
		r := nk.roster(t, partyID)
		r.Members = append(r.Members, b)
		data, err := json.Marshal(r)
		require.NoError(t, err)
		nk.seedObject(SystemUserID, PartyRosterCollection, partyID, string(data))
	}

	require.NoError(t, store.Leave(partyID, aID, ""))

	r := nk.roster(t, partyID)
	require.NotNil(t, r, "the joiner's party was deleted with the leaver")
	require.Equal(t, []string{bID}, memberIDs(r))
}

// --- party/mine ---

func rosterRPCContext(userID string) context.Context {
	ctx := context.WithValue(context.Background(), runtime.RUNTIME_CTX_USER_ID, userID) //nolint:staticcheck // runtime context keys are strings
	ctx = context.WithValue(ctx, runtime.RUNTIME_CTX_USERNAME, "caller")                //nolint:staticcheck // runtime context keys are strings
	ctx = context.WithValue(ctx, runtime.RUNTIME_CTX_SESSION_ID, "sess-1")              //nolint:staticcheck
	return context.WithValue(ctx, runtime.RUNTIME_CTX_CLIENT_IP, "203.0.113.9")         //nolint:staticcheck
}

func rosterRPCLogger() (runtime.Logger, *observer.ObservedLogs) {
	core, logs := observer.New(zap.InfoLevel)
	return NewRuntimeGoLogger(zap.New(core)), logs
}

type partyMineResponse struct {
	Party *PartyMineParty `json:"party"`
}

func callPartyMine(t *testing.T, nk runtime.NakamaModule, userID string) (partyMineResponse, *observer.ObservedLogs) {
	t.Helper()
	logger, logs := rosterRPCLogger()
	out, err := PartyMineRPC(rosterRPCContext(userID), logger, nil, nk, "")
	require.NoError(t, err)
	var resp partyMineResponse
	require.NoError(t, json.Unmarshal([]byte(out), &resp))
	return resp, logs
}

// The point of #732: every member offline, and the party is still there.
func TestPartyMineShowsAPartyWhoseMembersAreAllOffline(t *testing.T) {
	nk := newRosterTestNK()
	store := NewPartyRosterStore(nk)
	partyID := uuid.Must(uuid.NewV4()).String()
	aID, a := rosterMember("alpha")
	bID, b := rosterMember("bravo")
	require.NoError(t, store.Join(partyID, "", a))
	require.NoError(t, store.Join(partyID, "", b))

	resp, _ := callPartyMine(t, nk, bID)

	require.NotNil(t, resp.Party)
	require.Equal(t, partyID, resp.Party.PartyID)
	require.Equal(t, aID, resp.Party.LeaderID)
	require.Equal(t, []PartyMineMember{
		{UserID: aID, Username: "alpha", Leader: true, Online: false},
		{UserID: bID, Username: "bravo", Leader: false, Online: false},
	}, resp.Party.Members)
}

func TestPartyMineReportsWhoIsOnline(t *testing.T) {
	nk := newRosterTestNK()
	store := NewPartyRosterStore(nk)
	partyID := uuid.Must(uuid.NewV4()).String()
	aID, a := rosterMember("alpha")
	bID, b := rosterMember("bravo")
	require.NoError(t, store.Join(partyID, "", a))
	require.NoError(t, store.Join(partyID, "", b))
	nk.online[bID] = true

	resp, _ := callPartyMine(t, nk, aID)

	require.False(t, resp.Party.Members[0].Online)
	require.True(t, resp.Party.Members[1].Online)
}

func TestPartyMineWithNoPartyIsNull(t *testing.T) {
	nk := newRosterTestNK()
	callerID, _ := rosterMember("loner")

	logger, _ := rosterRPCLogger()
	out, err := PartyMineRPC(rosterRPCContext(callerID), logger, nil, nk, "")

	require.NoError(t, err)
	require.JSONEq(t, `{"party":null}`, out)
}

func TestPartyMineRequiresAUser(t *testing.T) {
	logger, _ := rosterRPCLogger()
	_, err := PartyMineRPC(context.Background(), logger, nil, newRosterTestNK(), "")
	require.Error(t, err)
}

// An all-offline party nobody has touched for the retention window is gone, and
// is deleted rather than kept forever; one with a member online is not.
func TestPartyMineRetention(t *testing.T) {
	setup := func(t *testing.T, age time.Duration) (*rosterTestNK, string, string) {
		nk := newRosterTestNK()
		store := NewPartyRosterStore(nk)
		store.now = func() time.Time { return time.Now().UTC().Add(-age) }
		partyID := uuid.Must(uuid.NewV4()).String()
		aID, a := rosterMember("alpha")
		require.NoError(t, store.Join(partyID, "", a))
		return nk, partyID, aID
	}

	t.Run("inside the window", func(t *testing.T) {
		nk, partyID, aID := setup(t, partyRosterRetention-time.Hour)
		resp, _ := callPartyMine(t, nk, aID)
		require.NotNil(t, resp.Party)
		require.NotNil(t, nk.roster(t, partyID))
	})
	t.Run("past the window, nobody online: gone and deleted", func(t *testing.T) {
		nk, partyID, aID := setup(t, partyRosterRetention+time.Hour)
		resp, _ := callPartyMine(t, nk, aID)
		require.Nil(t, resp.Party)
		require.Nil(t, nk.roster(t, partyID), "an expired roster is deleted, not hidden forever")
	})
	t.Run("past the window, a member online: kept", func(t *testing.T) {
		nk, partyID, aID := setup(t, partyRosterRetention+time.Hour)
		nk.online[aID] = true
		resp, _ := callPartyMine(t, nk, aID)
		require.NotNil(t, resp.Party)
		require.NotNil(t, nk.roster(t, partyID))
	})
}

// The expiry delete is versioned: a join that lands between party/mine's read and its delete keeps the
// party, and the caller is shown it.
func TestPartyMineExpiryLosesToAJoinThatLandsFirst(t *testing.T) {
	nk := newRosterTestNK()
	store := NewPartyRosterStore(nk)
	store.now = func() time.Time { return time.Now().UTC().Add(-(partyRosterRetention + time.Hour)) }
	partyID := uuid.Must(uuid.NewV4()).String()
	aID, a := rosterMember("alpha")
	bID, b := rosterMember("bravo")
	require.NoError(t, store.Join(partyID, "", a))

	nk.beforeDelete = func() {
		require.NoError(t, NewPartyRosterStore(nk).Join(partyID, "", b)) // now, so fresh
	}

	resp, _ := callPartyMine(t, nk, aID)

	require.NotNil(t, resp.Party, "the join that landed first was wiped by the expiry")
	require.Len(t, resp.Party.Members, 2)
	require.Equal(t, bID, resp.Party.Members[1].UserID)
	require.NotNil(t, nk.roster(t, partyID))
}

// AGENTS.md logging rule: one structured record per call, attributable, with the
// outcome and result size, and no more than the call carried.
func TestPartyMineWritesOneRequestRecord(t *testing.T) {
	nk := newRosterTestNK()
	store := NewPartyRosterStore(nk)
	partyID := uuid.Must(uuid.NewV4()).String()
	aID, a := rosterMember("alpha")
	require.NoError(t, store.Join(partyID, "", a))

	_, logs := callPartyMine(t, nk, aID)

	records := logs.FilterMessage("RPC").All()
	require.Len(t, records, 1)
	f := records[0].ContextMap()
	require.Equal(t, "party/mine", f["rpc_id"])
	require.Equal(t, aID, f["user_id"])
	require.Equal(t, "caller", f["username"])
	require.Equal(t, "sess-1", f["session_id"])
	require.Equal(t, "203.0.113.9", f["client_ip"])
	require.Equal(t, partyID, f["party_id"])
	require.EqualValues(t, 1, f["members"])
	require.Equal(t, "ok", f["outcome"])
	require.Contains(t, f, "latency_ms")
	require.Positive(t, f["result_size"])
}

// --- the in-game flow feeds the roster ---

// rosterEnv is the SNS party handler environment without a database: the
// account-id lookups the handlers make fail, which they already tolerate.
func newRosterEnv(t *testing.T) (*partyHandlerEnv, *rosterTestNK) {
	t.Helper()
	logger := loggerForTest(t)
	tracker := &handlerTracker{partyStreamTracker: newPartyStreamTracker()}
	mm, mmCleanup := createLightMatchmaker(t, logger)
	t.Cleanup(mmCleanup)
	pr := NewLocalPartyRegistry(logger, cfg, mm, tracker, untrackingStreamManager{tracker: tracker}, &DummyMessageRouter{}, "testnode").(*LocalPartyRegistry)
	sessions := &sessionMapRegistry{sessions: map[uuid.UUID]Session{}}
	nk := newRosterTestNK()
	ep := &EvrPipeline{
		node:   "testnode",
		db:     stubDB(t),
		config: NewConfig(logger),
		nk: &RuntimeGoNakamaModule{
			logger: logger, partyRegistry: pr, tracker: tracker, sessionRegistry: sessions, node: "testnode",
		},
		snsPartyIDToUUID: &MapOf[uint64, uuid.UUID]{},
		snsPartyUUIDToID: &MapOf[uuid.UUID, uint64]{},
		evrUUIDToUserID:  &MapOf[uuid.UUID, uuid.UUID]{},
		snsPartyInvites:  &MapOf[uuid.UUID, *snsPartyInviteList]{},
		snsPartyPolicies: &MapOf[uuid.UUID, uint8]{},
		snsPartyData:     &MapOf[uuid.UUID, *snsPartyDataState]{},
		partyRoster:      NewPartyRosterStore(nk),
	}
	return &partyHandlerEnv{t: t, ep: ep, pr: pr, tracker: tracker, sessions: sessions,
		accounts: map[*sessionWS]uint64{}, nextID: 1000}, nk
}

func (e *partyHandlerEnv) rosterSession(name string) *sessionWS {
	e.t.Helper()
	s := newPartyMemberSession(e.t, name, e.tracker, e.pr, e.ep)
	account := uint64(800_000_000_000_000_000) + partyHandlerAccountSeq.Add(1)
	e.accounts[s] = account
	params, _ := LoadParams(s.Context())
	params.loginPayload = &evr.LoginProfile{NevrSocial: 1}
	params.xpID = evr.EvrId{PlatformCode: evr.DSC, AccountId: account}
	e.sessions.sessions[s.id] = s
	return s
}

// seedRoster records every session as joined, as snsPartyTrackAndJoin does for each.
func seedRoster(t *testing.T, e *partyHandlerEnv, ph *PartyHandler, sessions ...*sessionWS) {
	t.Helper()
	for _, s := range sessions {
		params, _ := LoadParams(s.Context())
		require.NoError(t, e.ep.snsPartyTrackAndJoin(context.Background(), loggerForTest(t), s, ph.ID, 50, params))
	}
}

func TestSNSPartyJoinAndCreateRecordTheRoster(t *testing.T) {
	e, nk := newRosterEnv(t)
	leader, joiner := e.rosterSession("leader"), e.rosterSession("joiner")
	ph := e.party(50, leader)

	seedRoster(t, e, ph, leader, joiner)

	r := nk.roster(t, ph.ID.String())
	require.NotNil(t, r, "joining a party writes its roster")
	require.ElementsMatch(t, []string{leader.userID.String(), joiner.userID.String()}, memberIDs(r))
	require.Equal(t, leader.userID.String(), r.LeaderID, "the game's leader leads the roster")
	require.Equal(t, ph.ID.String(), nk.pointer(t, joiner.userID.String()))
}

// The point of #732 on the server: a client that disconnects sends no leave, and
// the party must outlive that. Every session closing leaves the roster whole.
func TestSNSPartySessionsClosingKeepTheRoster(t *testing.T) {
	e, nk := newRosterEnv(t)
	leader, member := e.rosterSession("leader"), e.rosterSession("member")
	ph := e.party(50, leader, member)
	seedRoster(t, e, ph, leader, member)

	for _, s := range []*sessionWS{leader, member} {
		s.ctxCancelFn()
	}
	require.Eventually(t, func() bool { return !e.inParty(leader, ph) && !e.inParty(member, ph) },
		5*time.Second, 10*time.Millisecond, "the closed sessions leave the in-memory party")

	r := nk.roster(t, ph.ID.String())
	require.NotNil(t, r, "the party ended in memory and its roster went with it")
	require.ElementsMatch(t, []string{leader.userID.String(), member.userID.String()}, memberIDs(r))
	require.Equal(t, ph.ID.String(), nk.pointer(t, member.userID.String()))
}

func TestSNSPartyExplicitLeaveRemovesTheMemberFromTheRoster(t *testing.T) {
	e, nk := newRosterEnv(t)
	leader, member := e.rosterSession("leader"), e.rosterSession("member")
	ph := e.party(50, leader, member)
	seedRoster(t, e, ph, leader, member)

	require.NoError(t, e.ep.snsPartyLeaveRequest(member.Context(), loggerForTest(t), member, &evr.SNSPartyLeaveRequest{}))

	r := nk.roster(t, ph.ID.String())
	require.Equal(t, []string{leader.userID.String()}, memberIDs(r))
	require.Empty(t, nk.pointer(t, member.userID.String()))
}

func TestSNSPartyLastExplicitLeaveDeletesTheRoster(t *testing.T) {
	e, nk := newRosterEnv(t)
	leader := e.rosterSession("leader")
	ph := e.party(50, leader)
	seedRoster(t, e, ph, leader)

	require.NoError(t, e.ep.snsPartyLeaveRequest(leader.Context(), loggerForTest(t), leader, &evr.SNSPartyLeaveRequest{}))

	require.Nil(t, nk.roster(t, ph.ID.String()))
}

func TestSNSPartyKickRemovesTheTargetFromTheRoster(t *testing.T) {
	e, nk := newRosterEnv(t)
	leader, member := e.rosterSession("leader"), e.rosterSession("member")
	ph := e.party(50, leader, member)
	seedRoster(t, e, ph, leader, member)
	memberParams, _ := LoadParams(member.Context())

	require.NoError(t, e.ep.snsPartyKickRequest(leader.Context(), loggerForTest(t), leader,
		&evr.SNSPartyKickRequest{TargetUserUUID: memberParams.xpID.UUID()}))

	require.Equal(t, []string{leader.userID.String()}, memberIDs(nk.roster(t, ph.ID.String())))
	require.Empty(t, nk.pointer(t, member.userID.String()))
}

func TestSNSPartyPassOwnershipMovesTheRosterLeader(t *testing.T) {
	e, nk := newRosterEnv(t)
	leader, member := e.rosterSession("leader"), e.rosterSession("member")
	ph := e.party(50, leader, member)
	seedRoster(t, e, ph, leader, member)
	memberParams, _ := LoadParams(member.Context())

	require.NoError(t, e.ep.snsPartyPassOwnershipRequest(leader.Context(), loggerForTest(t), leader,
		&evr.SNSPartyPassOwnershipRequest{TargetUserUUID: memberParams.xpID.UUID()}))

	r := nk.roster(t, ph.ID.String())
	require.NotNil(t, r)
	require.Equal(t, member.userID.String(), r.LeaderID)
}
