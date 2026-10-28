package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gofrs/uuid/v5"
	"github.com/heroiclabs/nakama-common/api"
	"github.com/heroiclabs/nakama-common/runtime"
	"github.com/heroiclabs/nakama/v3/server/evr"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/wrapperspb"
)

// inviteTestNakama is the OCC storage double plus the one call the invite RPCs
// make that it lacks: MatchGet, which MatchLabelByID uses to learn whether a
// match is still alive.
type inviteTestNakama struct {
	*occTestNakamaModule
	mu    sync.Mutex
	alive map[string]string // match id -> label JSON
}

func newInviteTestNakama() *inviteTestNakama {
	return &inviteTestNakama{occTestNakamaModule: newOCCTestNakamaModule(), alive: map[string]string{}}
}

func (n *inviteTestNakama) MatchGet(_ context.Context, id string) (*api.Match, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	label, ok := n.alive[id]
	if !ok {
		return nil, nil
	}
	return &api.Match{MatchId: id, Label: wrapperspb.String(label)}, nil
}

func (n *inviteTestNakama) endMatch(id string) {
	n.mu.Lock()
	defer n.mu.Unlock()
	delete(n.alive, id)
}

func (n *inviteTestNakama) objectCount(collection string) int {
	n.occTestNakamaModule.mu.Lock()
	defer n.occTestNakamaModule.mu.Unlock()
	c := 0
	for _, o := range n.occTestNakamaModule.objects {
		if o.Collection == collection {
			c++
		}
	}
	return c
}

type inviteTestWorld struct {
	t      *testing.T
	nk     *inviteTestNakama
	now    time.Time
	groups map[string]uuid.UUID // user id -> active group
	allocs []inviteAllocRequest
	failAs error
	logs   *observer.ObservedLogs
	logger runtime.Logger
	ip     string
}

// newInviteWorld installs the seams the RPCs reach outside storage through and
// restores them on cleanup. Nothing here runs a server.
func newInviteWorld(t *testing.T) *inviteTestWorld {
	t.Helper()
	core, logs := observer.New(zapcore.InfoLevel)
	w := &inviteTestWorld{
		t:      t,
		nk:     newInviteTestNakama(),
		now:    time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC),
		groups: map[string]uuid.UUID{},
		logs:   logs,
		ip:     "203.0.113.7",
		logger: NewRuntimeGoLogger(zap.New(core)),
	}
	origNow, origAlloc, origLimiter, origGroup, origCode := inviteNow, inviteAllocate, inviteLimiter, inviteActiveGroup, inviteNewCode
	inviteNow = func() time.Time { return w.now }
	inviteLimiter = newInviteAttemptLimiter(func() time.Time { return w.now })
	inviteActiveGroup = func(_ context.Context, _ runtime.NakamaModule, userID string) (uuid.UUID, error) {
		return w.groups[userID], nil // uuid.Nil when the user has no active guild
	}
	inviteAllocate = func(ctx context.Context, logger runtime.Logger, nk runtime.NakamaModule, req inviteAllocRequest) (*MatchLabel, error) {
		w.allocs = append(w.allocs, req)
		if w.failAs != nil {
			return nil, w.failAs
		}
		id := MatchID{UUID: uuid.Must(uuid.NewV4()), Node: "testnode"}
		label := &MatchLabel{ID: id, Mode: req.Mode, Level: req.Level, GroupID: &req.GroupID}
		w.nk.mu.Lock()
		w.nk.alive[id.String()] = string(label.GetLabel())
		w.nk.mu.Unlock()
		return label, nil
	}
	t.Cleanup(func() {
		inviteNow, inviteAllocate, inviteLimiter, inviteActiveGroup, inviteNewCode = origNow, origAlloc, origLimiter, origGroup, origCode
	})
	return w
}

func (w *inviteTestWorld) ctx(userID string) context.Context {
	ctx := context.WithValue(context.Background(), runtime.RUNTIME_CTX_USER_ID, userID) //nolint:staticcheck
	ctx = context.WithValue(ctx, runtime.RUNTIME_CTX_USERNAME, "user-"+userID[:4])      //nolint:staticcheck
	ctx = context.WithValue(ctx, runtime.RUNTIME_CTX_SESSION_ID, "session-"+userID[:4]) //nolint:staticcheck
	ctx = context.WithValue(ctx, runtime.RUNTIME_CTX_CLIENT_IP, w.ip)                   //nolint:staticcheck
	return ctx
}

func (w *inviteTestWorld) call(rpc func(context.Context, runtime.Logger, *sql.DB, runtime.NakamaModule, string) (string, error), userID string, payload any) (string, error) {
	w.t.Helper()
	b, err := json.Marshal(payload)
	if err != nil {
		w.t.Fatal(err)
	}
	return rpc(w.ctx(userID), w.logger, nil, w.nk, string(b))
}

type inviteCreated struct {
	Code      string `json:"code"`
	ExpiresAt string `json:"expires_at"`
	MatchID   string `json:"match_id"`
}

func (w *inviteTestWorld) create(userID string, payload map[string]any) inviteCreated {
	w.t.Helper()
	if _, ok := w.groups[userID]; !ok {
		w.groups[userID] = uuid.Must(uuid.NewV4())
	}
	out, err := w.call(withInviteRecord("create", InviteCreateRPC), userID, payload)
	if err != nil {
		w.t.Fatalf("invite/create: %v", err)
	}
	var c inviteCreated
	if err := json.Unmarshal([]byte(out), &c); err != nil {
		w.t.Fatalf("create answer %q: %v", out, err)
	}
	return c
}

func inviteToken(t *testing.T, err error) string {
	t.Helper()
	var rerr *runtime.Error
	if !errors.As(err, &rerr) {
		t.Fatalf("error %v is not a runtime.Error", err)
	}
	return rerr.Message
}

var defaultCreate = map[string]any{"experience": "arena", "mode": "arena_private", "map": evr.LevelArena.String(), "region": "default", "expires_s": 0}

const (
	userA = "aaaaaaaa-0000-4000-8000-000000000001"
	userB = "bbbbbbbb-0000-4000-8000-000000000002"
)

// invite/create returns a code and a match id and writes exactly one code object.
func TestInviteCreate_ReturnsCodeAndMatchAndWritesOneObject(t *testing.T) {
	w := newInviteWorld(t)
	c := w.create(userA, defaultCreate)

	if len(c.Code) != inviteCodeLength {
		t.Errorf("code %q has length %d, want %d", c.Code, len(c.Code), inviteCodeLength)
	}
	for _, r := range c.Code {
		if !strings.ContainsRune(inviteAlphabet, r) {
			t.Errorf("code %q holds %q, outside the alphabet", c.Code, r)
		}
	}
	if MatchIDFromStringOrNil(c.MatchID).IsNil() {
		t.Errorf("match_id %q is not a match id", c.MatchID)
	}
	want := w.now.Add(inviteDefaultExpiry)
	if got, err := time.Parse(time.RFC3339, c.ExpiresAt); err != nil || !got.Equal(want) || !strings.HasSuffix(c.ExpiresAt, "Z") {
		t.Errorf("expires_at = %q (%v), want %s in UTC", c.ExpiresAt, err, want.Format(time.RFC3339))
	}
	if n := w.nk.objectCount(InviteCodeCollection); n != 1 {
		t.Errorf("%d code objects stored, want exactly 1", n)
	}

	objs, _ := w.nk.StorageRead(context.Background(), []*runtime.StorageRead{{Collection: InviteCodeCollection, Key: c.Code, UserID: SystemUserID}})
	if len(objs) != 1 {
		t.Fatalf("the code object is not stored under the system user with key = code")
	}
	var stored StoredInvite
	if err := json.Unmarshal([]byte(objs[0].Value), &stored); err != nil {
		t.Fatal(err)
	}
	if stored.MatchID != c.MatchID || stored.OwnerID != userA || stored.GroupID != w.groups[userA].String() || stored.Revoked {
		t.Errorf("stored invite = %+v", stored)
	}
	if strings.Contains(stored.MatchID, c.Code) || strings.Contains(c.Code, stored.MatchID[:5]) {
		t.Error("the code must be an alias, not derived from the match id")
	}
	if len(w.allocs) != 1 || w.allocs[0].UserID != userA || w.allocs[0].Mode != evr.ModeArenaPrivate || w.allocs[0].GroupID != w.groups[userA] {
		t.Errorf("allocation = %+v, want one private arena allocation for the caller's active guild", w.allocs)
	}
}

// Every invite game is private: a public mode request is allocated as its private twin.
func TestInviteCreate_AlwaysAllocatesPrivate(t *testing.T) {
	for _, tc := range []struct {
		experience, mode string
		want             evr.Symbol
	}{
		{"arena", "", evr.ModeArenaPrivate},
		{"arena", "arena", evr.ModeArenaPrivate},         // the public mode's short name
		{"arena", "echo_arena", evr.ModeArenaPrivate},    // the public mode's token
		{"arena", "arena_private", evr.ModeArenaPrivate}, // already private
		{"arena", "arena_mode_7", evr.ModeArenaPrivate},  // text this service does not know is ignored
		{"combat", "combat", evr.ModeCombatPrivate},
		{"combat", "echo_combat", evr.ModeCombatPrivate},
		{"combat", "", evr.ModeCombatPrivate},
		{"social", "social_2.0", evr.ModeSocialPrivate},
		{"social", "", evr.ModeSocialPrivate},
	} {
		w := newInviteWorld(t)
		w.create(userA, map[string]any{"experience": tc.experience, "mode": tc.mode})
		if len(w.allocs) != 1 || w.allocs[0].Mode != tc.want {
			t.Errorf("experience=%q mode=%q allocated %+v, want %s", tc.experience, tc.mode, w.allocs, tc.want)
		}
	}

	// A mode of another experience than the one chosen is a contradiction.
	w := newInviteWorld(t)
	w.groups[userA] = uuid.Must(uuid.NewV4())
	_, err := w.call(withInviteRecord("create", InviteCreateRPC), userA, map[string]any{"experience": "arena", "mode": "echo_combat"})
	if err == nil || inviteToken(t, err) != "invalid" || len(w.allocs) != 0 {
		t.Errorf("arena experience with a combat mode: err = %v, allocations = %d, want invalid and none", err, len(w.allocs))
	}
}

func TestInviteCreate_ExpiryDefaultsClampsAndRefusesNegative(t *testing.T) {
	cases := []struct {
		expiresS int
		want     time.Duration
		refused  bool
	}{
		{0, inviteDefaultExpiry, false},
		{3600, time.Hour, false},
		{int((10 * 24 * time.Hour).Seconds()), inviteMaxExpiry, false},
		{-1, 0, true},
	}
	for _, tc := range cases {
		w := newInviteWorld(t)
		w.groups[userA] = uuid.Must(uuid.NewV4())
		out, err := w.call(withInviteRecord("create", InviteCreateRPC), userA, map[string]any{"experience": "arena", "expires_s": tc.expiresS})
		if tc.refused {
			if err == nil || inviteToken(t, err) != "invalid" {
				t.Errorf("expires_s=%d: err = %v, want invalid", tc.expiresS, err)
			}
			continue
		}
		if err != nil {
			t.Fatalf("expires_s=%d: %v", tc.expiresS, err)
		}
		var c inviteCreated
		_ = json.Unmarshal([]byte(out), &c)
		if got, _ := time.Parse(time.RFC3339, c.ExpiresAt); !got.Equal(w.now.Add(tc.want)) {
			t.Errorf("expires_s=%d: expires_at = %s, want now+%s", tc.expiresS, c.ExpiresAt, tc.want)
		}
	}
}

// Create's refusals are the contract's tokens, not free text.
func TestInviteCreate_RefusalsAreContractTokens(t *testing.T) {
	cases := []struct {
		name  string
		setup func(w *inviteTestWorld)
		body  map[string]any
		want  string
	}{
		{"unknown experience", nil, map[string]any{"experience": "bowling"}, "invalid"},
		{"no experience", nil, map[string]any{}, "invalid"},
		{"not in a guild", func(w *inviteTestWorld) { delete(w.groups, userA) }, map[string]any{"experience": "arena"}, "denied"},
		{"guild refuses (permission denied)", func(w *inviteTestWorld) { w.failAs = status.Error(codes.PermissionDenied, "x") }, map[string]any{"experience": "arena"}, "denied"},
		{"create rate limited", func(w *inviteTestWorld) { w.failAs = status.Error(codes.ResourceExhausted, "x") }, map[string]any{"experience": "arena"}, "rate"},
		{"no server available", func(w *inviteTestWorld) { w.failAs = errors.New("no servers") }, map[string]any{"experience": "arena"}, "unavailable"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := newInviteWorld(t)
			w.groups[userA] = uuid.Must(uuid.NewV4())
			if tc.setup != nil {
				tc.setup(w)
			}
			_, err := w.call(withInviteRecord("create", InviteCreateRPC), userA, tc.body)
			if err == nil {
				t.Fatal("want a refusal")
			}
			if got := inviteToken(t, err); got != tc.want {
				t.Errorf("token = %q, want %q", got, tc.want)
			}
			if n := w.nk.objectCount(InviteCodeCollection); n != 0 {
				t.Errorf("a refused create left %d code objects", n)
			}
		})
	}
}

func TestInviteCreate_LimitIsFiveLiveCodes(t *testing.T) {
	w := newInviteWorld(t)
	var codes []string
	for i := 0; i < inviteMaxLivePerUser; i++ {
		codes = append(codes, w.create(userA, defaultCreate).Code)
	}
	_, err := w.call(withInviteRecord("create", InviteCreateRPC), userA, defaultCreate)
	if err == nil || inviteToken(t, err) != "limit" {
		t.Fatalf("sixth create: err = %v, want limit", err)
	}
	if len(w.allocs) != inviteMaxLivePerUser {
		t.Errorf("a refused create still allocated a match: %d allocations", len(w.allocs))
	}

	// Another user is unaffected.
	w.create(userB, defaultCreate)

	// A revoked code frees a slot.
	if _, err := w.call(withInviteRecord("revoke", InviteRevokeRPC), userA, map[string]any{"code": codes[0]}); err != nil {
		t.Fatal(err)
	}
	w.create(userA, defaultCreate)

	// Expired codes and codes whose match ended do not count.
	_, err = w.call(withInviteRecord("create", InviteCreateRPC), userA, defaultCreate)
	if err == nil || inviteToken(t, err) != "limit" {
		t.Fatalf("create after refilling: err = %v, want limit", err)
	}
	c := w.resolve(userA, codes[1])
	w.nk.endMatch(c.MatchID)
	w.create(userA, defaultCreate)

	w.now = w.now.Add(inviteDefaultExpiry + time.Second)
	for i := 0; i < inviteMaxLivePerUser; i++ {
		w.create(userA, defaultCreate)
	}
}

type inviteResolved struct {
	MatchID string `json:"match_id"`
	Mode    string `json:"mode"`
	Level   string `json:"level"`
}

func (w *inviteTestWorld) resolve(userID, code string) inviteResolved {
	w.t.Helper()
	out, err := w.call(withInviteRecord("resolve", InviteResolveRPC), userID, map[string]any{"code": code})
	if err != nil {
		w.t.Fatalf("invite/resolve %q: %v", code, err)
	}
	var r inviteResolved
	if err := json.Unmarshal([]byte(out), &r); err != nil {
		w.t.Fatal(err)
	}
	return r
}

func (w *inviteTestWorld) resolveErr(userID, code string) string {
	w.t.Helper()
	_, err := w.call(withInviteRecord("resolve", InviteResolveRPC), userID, map[string]any{"code": code})
	if err == nil {
		w.t.Fatalf("invite/resolve %q succeeded, want a refusal", code)
	}
	return inviteToken(w.t, err)
}

func TestInviteResolve_LiveCodeAnswersWithTheMatch(t *testing.T) {
	w := newInviteWorld(t)
	c := w.create(userA, defaultCreate)
	// Another user may resolve it: a code is an address, the join is the authorization (D2).
	r := w.resolve(userB, c.Code)
	if r.MatchID != c.MatchID || r.Mode != evr.ModeArenaPrivate.String() || r.Level == "" {
		t.Errorf("resolved %+v, want match %s mode %s", r, c.MatchID, evr.ModeArenaPrivate)
	}
}

func TestInviteResolve_NormalizesWhatWasTyped(t *testing.T) {
	w := newInviteWorld(t)
	c := w.create(userA, defaultCreate)
	typed := strings.ToLower(c.Code[:2] + " " + c.Code[2:3] + "-" + c.Code[3:])
	if r := w.resolve(userB, typed); r.MatchID != c.MatchID {
		t.Errorf("typed %q resolved %+v, want the match for %s", typed, r, c.Code)
	}
}

func TestInviteResolve_DistinctRefusals(t *testing.T) {
	w := newInviteWorld(t)

	if got := w.resolveErr(userB, "ABCDE"); got != "invalid" {
		t.Errorf("unknown code: %q, want invalid", got)
	}
	for _, bad := range []string{"", "0OIL1", "ABCD", strings.Repeat("A", 64)} {
		if got := w.resolveErr(userB, bad); got != "invalid" {
			t.Errorf("malformed %q: %q, want invalid", bad, got)
		}
	}

	expiring := w.create(userA, map[string]any{"experience": "arena", "expires_s": 60})
	revoked := w.create(userA, defaultCreate)
	ended := w.create(userA, defaultCreate)
	if _, err := w.call(withInviteRecord("revoke", InviteRevokeRPC), userA, map[string]any{"code": revoked.Code}); err != nil {
		t.Fatal(err)
	}
	w.nk.endMatch(ended.MatchID)
	w.now = w.now.Add(2 * time.Minute)

	if got := w.resolveErr(userB, expiring.Code); got != "expired" {
		t.Errorf("expired code: %q, want expired", got)
	}
	if got := w.resolveErr(userB, revoked.Code); got != "closed" {
		t.Errorf("revoked code: %q, want closed", got)
	}
	if got := w.resolveErr(userB, ended.Code); got != "closed" {
		t.Errorf("code whose match ended: %q, want closed", got)
	}
}

func TestInviteRevoke_OnlyTheOwner(t *testing.T) {
	w := newInviteWorld(t)
	c := w.create(userA, defaultCreate)

	_, err := w.call(withInviteRecord("revoke", InviteRevokeRPC), userB, map[string]any{"code": c.Code})
	if err == nil || inviteToken(t, err) != "denied" {
		t.Fatalf("revoke by a non-owner: err = %v, want denied", err)
	}
	if r := w.resolve(userB, c.Code); r.MatchID != c.MatchID {
		t.Error("a refused revoke closed the code")
	}

	if _, err := w.call(withInviteRecord("revoke", InviteRevokeRPC), userA, map[string]any{"code": c.Code}); err != nil {
		t.Fatalf("revoke by the owner: %v", err)
	}
	if _, err := w.call(withInviteRecord("revoke", InviteRevokeRPC), userA, map[string]any{"code": c.Code}); err != nil {
		t.Errorf("revoking twice: %v, want ok", err)
	}
	if got := w.resolveErr(userB, c.Code); got != "closed" {
		t.Errorf("after revoke: %q, want closed", got)
	}
	_, err = w.call(withInviteRecord("revoke", InviteRevokeRPC), userA, map[string]any{"code": "ABCDE"})
	if err == nil || inviteToken(t, err) != "invalid" {
		t.Errorf("revoking an unknown code: err = %v, want invalid", err)
	}
}

// A miss is rate limited and costs more than a hit; the limit is per user and per source address.
func TestInviteResolve_MissesAreRateLimited(t *testing.T) {
	w := newInviteWorld(t)
	c := w.create(userA, defaultCreate)

	// Hits are cheap: many in a row are fine.
	for i := 0; i < 20; i++ {
		w.resolve(userB, c.Code)
	}

	// Misses are not: the guesser runs out well before 20 attempts.
	misses, limited := 0, false
	for i := 0; i < 20 && !limited; i++ {
		switch got := w.resolveErr(userB, "ABCDE"); got {
		case "invalid":
			misses++
		case "rate":
			limited = true
		default:
			t.Fatalf("miss %d: %q", i, got)
		}
	}
	if !limited {
		t.Fatal("20 straight misses were never rate limited")
	}
	if misses > 10 {
		t.Errorf("%d misses allowed before the limit, want a budget well under 10", misses)
	}
	// A limited caller cannot use even a correct code until the bucket refills.
	if got := w.resolveErr(userB, c.Code); got != "rate" {
		t.Errorf("a limited caller's valid code: %q, want rate", got)
	}
	w.now = w.now.Add(10 * time.Minute)
	w.resolve(userB, c.Code)

	// Fresh users behind one source address share that address's budget: no
	// user here has made an attempt before, so only the address can limit them.
	w.ip = "198.51.100.9"
	for i := 0; i < 20; i++ {
		fresh := uuid.Must(uuid.NewV4()).String()
		if w.resolveErr(fresh, "ABCDE") == "rate" {
			goto perUser
		}
	}
	t.Fatal("20 users behind one address were never limited")

perUser:
	// One user rotating addresses is limited by the user's own budget.
	for i := 0; i < 20; i++ {
		w.ip = "192.0.2." + string(rune('1'+i%9))
		if w.resolveErr(userB, "ABCDE") == "rate" {
			return
		}
	}
	t.Error("one user rotating addresses was never limited")
}

// Codes are generated from the alphabet, without look-alikes, of the measured length.
func TestInviteCodeAlphabet(t *testing.T) {
	if len(inviteAlphabet) != 31 {
		t.Errorf("alphabet has %d symbols, want 31", len(inviteAlphabet))
	}
	for _, banned := range "0O1IL" {
		if strings.ContainsRune(inviteAlphabet, banned) {
			t.Errorf("alphabet holds the look-alike %q", banned)
		}
	}
	seen := map[string]bool{}
	for i := 0; i < 2000; i++ {
		c, err := newInviteCode()
		if err != nil {
			t.Fatal(err)
		}
		if len(c) != inviteCodeLength {
			t.Fatalf("code %q has length %d", c, len(c))
		}
		for _, r := range c {
			if !strings.ContainsRune(inviteAlphabet, r) {
				t.Fatalf("code %q holds %q", c, r)
			}
		}
		seen[c] = true
	}
	if len(seen) < 1900 {
		t.Errorf("only %d distinct codes in 2000: the generator is not random", len(seen))
	}
}

// One structured record per RPC: who, what with resolved ids, outcome, latency, size.
// The code itself is a bearer secret and never appears in it.
func TestInviteRPCsWriteOneRecordEachAndNeverTheCode(t *testing.T) {
	w := newInviteWorld(t)
	c := w.create(userA, defaultCreate)
	w.resolve(userB, c.Code)
	w.resolveErr(userB, "ABCDE")
	if _, err := w.call(withInviteRecord("revoke", InviteRevokeRPC), userB, map[string]any{"code": c.Code}); err == nil {
		t.Fatal("revoke by a non-owner should be refused")
	}

	recs := w.logs.FilterMessage("RPC").All()
	if len(recs) != 4 {
		t.Fatalf("%d RPC records for 4 calls: %v", len(recs), w.logs.All())
	}
	type want struct{ rpc, user, outcome string }
	for i, wnt := range []want{
		{"invite/create", userA, "ok"},
		{"invite/resolve", userB, "ok"},
		{"invite/resolve", userB, "invalid"},
		{"invite/revoke", userB, "denied"},
	} {
		f := recs[i].ContextMap()
		if f["rpc_id"] != wnt.rpc || f["user_id"] != wnt.user || f["outcome"] != wnt.outcome {
			t.Errorf("record %d = %v, want %+v", i, f, wnt)
		}
		for _, k := range []string{"session_id", "client_ip", "latency_ms", "result_size"} {
			if _, ok := f[k]; !ok {
				t.Errorf("record %d lacks %s: %v", i, k, f)
			}
		}
	}
	create := recs[0].ContextMap()
	if create["match_id"] != c.MatchID || create["group_id"] != w.groups[userA].String() {
		t.Errorf("create record does not carry the resolved ids: %v", create)
	}
	resolve := recs[1].ContextMap()
	if resolve["match_id"] != c.MatchID {
		t.Errorf("resolve record lacks the resolved match id: %v", resolve)
	}
	for _, r := range w.logs.All() {
		blob, _ := json.Marshal(r.ContextMap())
		if strings.Contains(r.Message+string(blob), c.Code) {
			t.Errorf("the code appeared in a log record: %s %s", r.Message, blob)
		}
	}
	if fp, _ := resolve["code_fp"].(string); fp == "" || fp != inviteCodeFingerprint(c.Code) {
		t.Errorf("resolve record code_fp = %v, want the fingerprint", resolve["code_fp"])
	}
}

func TestInviteRPCsAreRegisteredWithAuth(t *testing.T) {
	regs := buildEVRRPCRegistrations(&RPCHandler{}, nil)
	for _, id := range []string{"invite/create", "invite/resolve", "invite/revoke"} {
		found := false
		for _, r := range regs {
			if r.ID != id {
				continue
			}
			found = true
			if r.Permission == nil || !r.Permission.RequireAuth || len(r.Permission.AllowedGroups) != 0 {
				t.Errorf("%s permission = %+v, want RequireAuth and no group gate", id, r.Permission)
			}
		}
		if !found {
			t.Errorf("%s is not in the RPC table", id)
		}
	}
}

func (w *inviteTestWorld) ownerIndex(userID string) []string {
	w.t.Helper()
	objs, _ := w.nk.StorageRead(context.Background(), []*runtime.StorageRead{{Collection: InviteOwnerCollection, Key: userID, UserID: SystemUserID}})
	if len(objs) == 0 {
		return nil
	}
	var idx storedInviteOwner
	if err := json.Unmarshal([]byte(objs[0].Value), &idx); err != nil {
		w.t.Fatal(err)
	}
	return idx.Codes
}

// The owner's index names only codes that can still be live: a revoked code
// leaves it at once, and expired or ended ones leave at the next create, so it
// stays bounded by the live-code limit.
func TestInviteOwnerIndexHoldsOnlyLiveCodes(t *testing.T) {
	w := newInviteWorld(t)
	a := w.create(userA, defaultCreate)
	b := w.create(userA, defaultCreate)
	c := w.create(userA, map[string]any{"experience": "arena", "expires_s": 60})
	if got := w.ownerIndex(userA); len(got) != 3 {
		t.Fatalf("index = %v, want the 3 codes", got)
	}

	if _, err := w.call(withInviteRecord("revoke", InviteRevokeRPC), userA, map[string]any{"code": a.Code}); err != nil {
		t.Fatal(err)
	}
	if got := w.ownerIndex(userA); len(got) != 2 || got[0] == a.Code || got[1] == a.Code {
		t.Errorf("after revoke index = %v, want %s gone", got, a.Code)
	}

	w.nk.endMatch(b.MatchID)
	w.now = w.now.Add(2 * time.Minute) // c has expired
	d := w.create(userA, defaultCreate)
	if got := w.ownerIndex(userA); len(got) != 1 || got[0] != d.Code {
		t.Errorf("after the next create index = %v, want only %s (not %s expired, %s ended)", got, d.Code, c.Code, b.Code)
	}
}

// A code that is already taken is never overwritten: the create draws another.
func TestInviteCreate_NeverOverwritesATakenCode(t *testing.T) {
	w := newInviteWorld(t)
	first := w.create(userA, defaultCreate)

	draws := []string{first.Code, first.Code, "ZZZZZ"}
	inviteNewCode = func() (string, error) {
		c := draws[0]
		draws = draws[1:]
		return c, nil
	}
	second := w.create(userB, defaultCreate)
	if second.Code != "ZZZZZ" {
		t.Errorf("second code = %q, want the third draw after two collisions", second.Code)
	}
	if r := w.resolve(userB, first.Code); r.MatchID != first.MatchID {
		t.Error("the first user's code was overwritten by a colliding create")
	}
}
