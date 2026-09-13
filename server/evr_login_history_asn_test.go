package server

import (
	"context"
	"encoding/json"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/heroiclabs/nakama-common/api"
	"github.com/heroiclabs/nakama-common/runtime"
	"github.com/heroiclabs/nakama/v3/server/evr"
	"go.uber.org/zap"
)

// Fixture addresses. The ASNs are what the IP info providers report for them;
// nothing in these tests loads an IP->ASN range table.
const (
	residentialIP  = "73.162.100.1"   // Comcast
	residentialASN = 7922             // AS7922, not a configured CGNAT ASN
	starlinkIP     = "129.222.210.50" // Starlink exit
	starlinkASN    = 14593            // AS14593, configured in seededCGNATSettings
)

// loginASNTestModule is the storage the login event handler touches, in
// memory: login histories through occTestNakamaModule and an alt index with
// nothing in it.
type loginASNTestModule struct {
	*occTestNakamaModule
}

func newLoginASNTestModule() *loginASNTestModule {
	return &loginASNTestModule{occTestNakamaModule: newOCCTestNakamaModule()}
}

// StorageIndexList answers alt discovery with nothing.
func (m *loginASNTestModule) StorageIndexList(ctx context.Context, callerID, indexName, query string, limit int, order []string, cursor string) (*api.StorageObjects, string, error) {
	return &api.StorageObjects{}, "", nil
}

// AccountGetId serves the IP-authorization notification in the event handler;
// an account with no custom ID sends none.
func (m *loginASNTestModule) AccountGetId(ctx context.Context, userID string) (*api.Account, error) {
	return &api.Account{User: &api.User{Id: userID}}, nil
}

// storedLoginHistory is the persisted shape of a LoginHistory, read back as
// JSON so the assertions are about what storage holds.
type storedLoginHistory struct {
	Cache   []string `json:"cache"`
	History map[string]struct {
		ClientIP string `json:"client_ip"`
		ASN      int    `json:"asn"`
	} `json:"history"`
}

func mustXPID(t *testing.T, s string) evr.EvrId {
	t.Helper()
	xpid, err := evr.ParseEvrId(s)
	if err != nil {
		t.Fatalf("ParseEvrId(%q): %v", s, err)
	}
	return *xpid
}

// processLoginEvent delivers an EventUserAuthenticated payload the way the
// dispatcher does -- JSON, unmarshalled into the event type -- runs its
// handler, which is the write that persists a login's history, and returns
// what storage holds afterwards.
func processLoginEvent(t *testing.T, m *loginASNTestModule, payload string) storedLoginHistory {
	t.Helper()
	ctx := context.Background()
	logger := NewRuntimeGoLogger(zap.NewNop())

	evt := &EventUserAuthenticated{}
	if err := json.Unmarshal([]byte(payload), evt); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	if err := evt.Process(ctx, logger, &EventDispatcher{nk: m}); err != nil {
		t.Fatalf("EventUserAuthenticated.Process: %v", err)
	}

	objs, err := m.StorageRead(ctx, []*runtime.StorageRead{{Collection: LoginStorageCollection, Key: LoginHistoryStorageKey, UserID: evt.UserID}})
	if err != nil || len(objs) != 1 {
		t.Fatalf("read stored login history: %d objects, err %v", len(objs), err)
	}
	var stored storedLoginHistory
	if err := json.Unmarshal([]byte(objs[0].Value), &stored); err != nil {
		t.Fatalf("unmarshal stored login history: %v", err)
	}
	return stored
}

// TestLoginEvent_PersistedCacheKeepsResidentialIP: a login from an ordinary
// residential address must persist that address as an alt-discovery key. The
// detector is configured as production is and no IP->ASN range table is loaded
// anywhere -- which, under #598, made the detector report every public address
// as shared, so rebuildCache wrote the degraded cache to storage.
//
// The event carries exactly the fields authorizeSession sets
// (evr_pipeline_login.go, SendEvent at the end of authorizeSession) and no ASN:
// an address whose ASN is unknown is classified by the configured CIDRs alone.
func TestLoginEvent_PersistedCacheKeepsResidentialIP(t *testing.T) {
	withDetector(t, seededCGNATSettings())
	m := newLoginASNTestModule()

	stored := processLoginEvent(t, m, `{
		"user_id": "11111111-1111-1111-1111-111111111111",
		"xpid": "OVR-ORG-3930901337016247",
		"client_ip": "`+residentialIP+`",
		"login_data": {"hmdserialnumber": "1WMHH9ABC1234"},
		"is_websocket_authenticated": true
	}`)

	if !slices.Contains(stored.Cache, residentialIP) {
		t.Errorf("persisted LoginHistory.Cache = %v; the residential address %s was dropped, so no other account can discover this one through it", stored.Cache, residentialIP)
	}
}

// stubASNProvider is a network-free cached-ASN reader. An address it has no
// ASN for is a cache miss: (nil, nil).
type stubASNProvider struct {
	asns  map[string]int
	calls []string
}

func (p *stubASNProvider) GetCached(ctx context.Context, ip string) (IPInfo, error) {
	p.calls = append(p.calls, ip)
	asn, ok := p.asns[ip]
	if !ok {
		return nil, nil
	}
	return asnIPInfo{asn: asn}, nil
}

// asnIPInfo is StubIPInfo with an ASN.
type asnIPInfo struct {
	StubIPInfo
	asn int
}

func (i asnIPInfo) ASN() int { return i.asn }

// oldEntry is a login history entry as stored before ASNs were recorded. Its
// XPID and HMD serial are unique to accountID, so two entries can only ever
// match each other on their client IP.
func oldEntry(accountID uint64, ip string, updated time.Time) *LoginHistoryEntry {
	return &LoginHistoryEntry{
		CreatedAt: updated,
		UpdatedAt: updated,
		XPID:      evr.EvrId{PlatformCode: evr.OVR, AccountId: accountID},
		ClientIP:  ip,
		LoginData: &evr.LoginProfile{HMDSerialNumber: "SERIAL-" + strconv.FormatUint(accountID, 10)},
	}
}

// matchItems renders matches as their item lists, for failure messages.
func matchItems(matches []*AlternateSearchMatch) string {
	items := make([]string, 0, len(matches))
	for _, m := range matches {
		items = append(items, m.OtherUserID+":"+strings.Join(m.Items, ","))
	}
	return "[" + strings.Join(items, " ") + "]"
}

// historyOf builds a history from entries, keyed as LoginHistory.update keys them.
func historyOf(userID string, entries ...*LoginHistoryEntry) *LoginHistory {
	h := NewLoginHistory(userID)
	h.History = make(map[string]*LoginHistoryEntry, len(entries))
	for _, e := range entries {
		h.History[e.Key()] = e
	}
	return h
}

// TestRecordLoginASN_RecordsFromLoginLookup: the login's own IP info lookup
// supplies the ASN of its client IP, and the ASN reaches the event handler that
// persists the history.
func TestRecordLoginASN_RecordsFromLoginLookup(t *testing.T) {
	withDetector(t, seededCGNATSettings())
	xpid := mustXPID(t, "OVR-ORG-3930901337016247")
	h := NewLoginHistory("33333333-3333-3333-3333-333333333333")
	h.Update(xpid, starlinkIP, &evr.LoginProfile{}, true)

	loginASN := recordLoginASN(h, starlinkIP, asnIPInfo{asn: starlinkASN})

	if got := h.History[loginHistoryEntryKey(xpid, starlinkIP)].ASN; got != starlinkASN {
		t.Fatalf("entry ASN = %d after recordLoginASN, want AS%d from the login's lookup", got, starlinkASN)
	}
	if loginASN != starlinkASN {
		t.Errorf("recordLoginASN returned %d, want AS%d for the event", loginASN, starlinkASN)
	}
	if got := recordLoginASN(h, starlinkIP, nil); got != 0 {
		t.Errorf("recordLoginASN with no lookup result returned %d, want 0", got)
	}

	payload, err := json.Marshal(&EventUserAuthenticated{
		UserID:                   h.userID,
		XPID:                     xpid,
		ClientIP:                 starlinkIP,
		ClientIPASN:              loginASN,
		LoginPayload:             &evr.LoginProfile{},
		IsWebSocketAuthenticated: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	stored := processLoginEvent(t, newLoginASNTestModule(), string(payload))
	if got := stored.History[loginHistoryEntryKey(xpid, starlinkIP)].ASN; got != starlinkASN {
		t.Errorf("persisted entry ASN = %d, want AS%d", got, starlinkASN)
	}
	if slices.Contains(stored.Cache, starlinkIP) {
		t.Errorf("persisted cache %v holds %s, recorded as AS%d, a configured CGNAT ASN", stored.Cache, starlinkIP, starlinkASN)
	}
}

// TestClassify_FromRecordedASN: with no IP->ASN range data loaded anywhere, an
// address recorded as Starlink is weak and one recorded as residential is
// strong -- in the cache, in the discovery keys, and in comparison.
func TestClassify_FromRecordedASN(t *testing.T) {
	withDetector(t, seededCGNATSettings())
	now := time.Now()
	starlink, residential := oldEntry(1, starlinkIP, now), oldEntry(2, residentialIP, now)
	starlink.ASN, residential.ASN = starlinkASN, residentialASN
	h := historyOf("user-a", starlink, residential)

	h.rebuildCache()
	if slices.Contains(h.Cache, starlinkIP) || !slices.Contains(h.Cache, residentialIP) {
		t.Errorf("Cache = %v; want %s (AS%d) dropped and %s (AS%d) kept", h.Cache, starlinkIP, starlinkASN, residentialIP, residentialASN)
	}
	if patterns := h.AltSearchPatterns(); slices.Contains(patterns, starlinkIP) || !slices.Contains(patterns, residentialIP) {
		t.Errorf("AltSearchPatterns = %v; want %s dropped and %s kept", patterns, starlinkIP, residentialIP)
	}

	other := historyOf("user-b", oldEntry(3, starlinkIP, now), oldEntry(4, residentialIP, now))
	matches := loginHistoryCompare(h, other)
	if len(matches) != 1 || !slices.Equal(matches[0].Items, []string{residentialIP}) {
		t.Errorf("loginHistoryCompare = %s; want one match on %s only", matchItems(matches), residentialIP)
	}
}

// TestClassify_UnknownASNIsNotShared: an address with no recorded ASN is not
// treated as shared, and a configured CIDR still applies without one.
func TestClassify_UnknownASNIsNotShared(t *testing.T) {
	withDetector(t, seededCGNATSettings())
	const cgnatCIDRIP = "100.64.1.1" // in the configured 100.64.0.0/10
	h := historyOf("user-a", oldEntry(1, starlinkIP, time.Now()), oldEntry(2, cgnatCIDRIP, time.Now()))

	h.rebuildCache()
	if !slices.Contains(h.Cache, starlinkIP) {
		t.Errorf("Cache = %v dropped %s with no ASN recorded; unknown is not evidence the address is shared", h.Cache, starlinkIP)
	}
	if slices.Contains(h.Cache, cgnatCIDRIP) {
		t.Errorf("Cache = %v kept %s, which is in a configured CGNAT CIDR", h.Cache, cgnatCIDRIP)
	}
}

// TestASNBackfill_OldEntryIsBackfilledThenClassified is the no-regression case for
// histories stored before ASNs were recorded. Read back from JSON with no "asn"
// field, a Starlink entry is unknown and would link strangers again (#596); the
// backfill resolves it and from then on it is weak.
func TestASNBackfill_OldEntryIsBackfilledThenClassified(t *testing.T) {
	d := withDetector(t, seededCGNATSettings())
	now := time.Now()
	old := historyOf("user-a", oldEntry(1, starlinkIP, now), oldEntry(2, residentialIP, now.Add(-time.Hour)))
	old.AlternateMatches = map[string][]*AlternateSearchMatch{"stranger": {{OtherUserID: "stranger", Items: []string{starlinkIP}}}}
	raw, err := json.Marshal(old)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), `"asn"`) {
		t.Fatalf("premise: an entry with no ASN must store no asn field (omitempty), got %s", raw)
	}
	h := NewLoginHistory("user-a")
	if err := json.Unmarshal(raw, h); err != nil {
		t.Fatalf("an old stored history must still deserialize: %v", err)
	}

	resolver := &stubASNProvider{asns: map[string]int{starlinkIP: starlinkASN, residentialIP: residentialASN}}
	changed, unresolved, err := backfillLoginHistoryASNs(context.Background(), resolver, h)
	if err != nil {
		t.Fatalf("backfill: %v", err)
	}

	if !changed || unresolved != 0 {
		t.Fatalf("backfill = (changed %v, unresolved %d), want (true, 0)", changed, unresolved)
	}
	if got := h.clientIPASNs(); got[starlinkIP] != starlinkASN || got[residentialIP] != residentialASN {
		t.Errorf("recorded ASNs = %v after backfill", got)
	}
	h.rebuildCache()
	if slices.Contains(h.Cache, starlinkIP) || !slices.Contains(h.Cache, residentialIP) {
		t.Errorf("Cache = %v after backfill; want %s dropped and %s kept", h.Cache, starlinkIP, residentialIP)
	}
	if got := filterStrongAlts(h, []string{"stranger"}, d); len(got) != 0 {
		t.Errorf("filterStrongAlts kept %v, linked only by %s, backfilled as AS%d", got, starlinkIP, starlinkASN)
	}
}

// TestASNBackfill_Idempotent: a second pass over a backfilled history does no
// lookups and changes nothing, so #597 may run it over every history on every
// boot and a login may run it on every login.
func TestASNBackfill_Idempotent(t *testing.T) {
	h := historyOf("user-a", oldEntry(1, starlinkIP, time.Now()), oldEntry(2, residentialIP, time.Now()))
	resolver := &stubASNProvider{asns: map[string]int{starlinkIP: starlinkASN, residentialIP: residentialASN}}

	if changed, _, _ := backfillLoginHistoryASNs(context.Background(), resolver, h); !changed {
		t.Fatal("first backfill changed nothing")
	}
	lookups := len(resolver.calls)

	changed, unresolved, err := backfillLoginHistoryASNs(context.Background(), resolver, h)
	if err != nil {
		t.Fatalf("backfill: %v", err)
	}
	if changed || unresolved != 0 {
		t.Errorf("second backfill = (changed %v, unresolved %d), want (false, 0)", changed, unresolved)
	}
	if len(resolver.calls) != lookups {
		t.Errorf("second backfill looked up %v again", resolver.calls[lookups:])
	}
}

// TestASNBackfill_FailedLookupRecordsNothing: a nil IPInfo -- every provider
// failed -- leaves the address unknown rather than writing unknown down, and
// the next pass tries it again. A private address is never looked up.
func TestASNBackfill_FailedLookupRecordsNothing(t *testing.T) {
	h := historyOf("user-a", oldEntry(1, starlinkIP, time.Now()), oldEntry(2, "192.168.1.44", time.Now()))
	resolver := &stubASNProvider{} // answers nothing

	for pass := 1; pass <= 2; pass++ {
		changed, unresolved, err := backfillLoginHistoryASNs(context.Background(), resolver, h)
		if err != nil {
			t.Fatalf("backfill: %v", err)
		}
		if changed || unresolved != 1 {
			t.Errorf("pass %d: backfill = (changed %v, unresolved %d), want (false, 1)", pass, changed, unresolved)
		}
	}
	if !slices.Equal(resolver.calls, []string{starlinkIP, starlinkIP}) {
		t.Errorf("lookups = %v; want %s tried once per pass and the private address never", resolver.calls, starlinkIP)
	}
	if asns := h.clientIPASNs(); len(asns) != 0 {
		t.Errorf("recorded ASNs = %v after lookups that returned nothing", asns)
	}
}

// TestASNBackfill_FillsSiblingEntries: an address already recorded on one entry
// is filled onto its other entries (another XPID, or the Active and pending
// maps) without a lookup.
func TestASNBackfill_FillsSiblingEntries(t *testing.T) {
	known := oldEntry(1, starlinkIP, time.Now())
	known.ASN = starlinkASN
	sibling := oldEntry(2, starlinkIP, time.Now())
	h := historyOf("user-a", known, sibling)
	pending := oldEntry(3, starlinkIP, time.Now())
	h.PendingAuthorizations = map[string]*LoginHistoryEntry{starlinkIP: pending}
	resolver := &stubASNProvider{}

	changed, unresolved, err := backfillLoginHistoryASNs(context.Background(), resolver, h)
	if err != nil {
		t.Fatalf("backfill: %v", err)
	}
	if !changed || unresolved != 0 || len(resolver.calls) != 0 {
		t.Errorf("backfill = (changed %v, unresolved %d) with lookups %v; want (true, 0) and none", changed, unresolved, resolver.calls)
	}
	if sibling.ASN != starlinkASN || pending.ASN != starlinkASN {
		t.Errorf("sibling ASN %d, pending ASN %d; want AS%d on both", sibling.ASN, pending.ASN, starlinkASN)
	}
}

// TestLoginHistoryCompare_EitherSidesASN: an address two accounts share is
// classified by whichever side has its ASN recorded, so a backfilled login
// does not link to a not-yet-backfilled account over a Starlink address.
func TestLoginHistoryCompare_EitherSidesASN(t *testing.T) {
	withDetector(t, seededCGNATSettings())
	backfilled := oldEntry(1, starlinkIP, time.Now())
	backfilled.ASN = starlinkASN
	a := historyOf("user-a", backfilled)
	b := historyOf("user-b", oldEntry(2, starlinkIP, time.Now()))

	for _, pair := range [][2]*LoginHistory{{a, b}, {b, a}} {
		if matches := loginHistoryCompare(pair[0], pair[1]); len(matches) != 0 {
			t.Errorf("loginHistoryCompare(%s, %s) = %s; the shared address is AS%d on %s's side", pair[0].userID, pair[1].userID, matchItems(matches), starlinkASN, a.userID)
		}
	}
}

// TestLoginHistoryCompare_ConflictingASNsAreSymmetric: an address that moved
// between networks carries a different ASN on each side. The pair's edge must
// not depend on which account is passed first: the newest record wins,
// whichever history holds it.
func TestLoginHistoryCompare_ConflictingASNsAreSymmetric(t *testing.T) {
	withDetector(t, seededCGNATSettings())
	now := time.Now()
	older := oldEntry(1, starlinkIP, now.Add(-time.Hour))
	older.ASN = residentialASN
	newer := oldEntry(2, starlinkIP, now)
	newer.ASN = starlinkASN
	a, b := historyOf("user-a", older), historyOf("user-b", newer)

	ab, ba := loginHistoryCompare(a, b), loginHistoryCompare(b, a)
	if len(ab) != 0 || len(ba) != 0 {
		t.Errorf("loginHistoryCompare(a, b) = %s, (b, a) = %s; the newest record of %s is AS%d, a CGNAT ASN, so neither direction may link on it",
			matchItems(ab), matchItems(ba), starlinkIP, starlinkASN)
	}
}

// TestLoginEvent_RecordsASNOnEntry: the ASN the login resolved for its client
// IP is stored on the login history entry the event handler persists.
func TestLoginEvent_RecordsASNOnEntry(t *testing.T) {
	withDetector(t, seededCGNATSettings())
	m := newLoginASNTestModule()
	xpid := mustXPID(t, "OVR-ORG-3930901337016247")

	stored := processLoginEvent(t, m, `{
		"user_id": "22222222-2222-2222-2222-222222222222",
		"xpid": "OVR-ORG-3930901337016247",
		"client_ip": "`+starlinkIP+`",
		"client_ip_asn": 14593,
		"login_data": {"hmdserialnumber": "1WMHH9ABC1234"},
		"is_websocket_authenticated": true
	}`)

	entry, ok := stored.History[loginHistoryEntryKey(xpid, starlinkIP)]
	if !ok {
		t.Fatalf("no stored history entry for %s; entries: %v", loginHistoryEntryKey(xpid, starlinkIP), stored.History)
	}
	if entry.ASN != starlinkASN {
		t.Errorf("stored entry for %s has ASN %d; the login resolved AS%d for it", starlinkIP, entry.ASN, starlinkASN)
	}
}

// TestASNBackfill_CacheMissNeverFetchesLive: the backfill reads each address's
// ASN from what the providers already cached, and an address with no cache
// entry is left unknown. It never takes IPInfoCache.Get's path, which on a
// Redis miss makes a live request to IPQS or ip-api.
func TestASNBackfill_CacheMissNeverFetchesLive(t *testing.T) {
	provider := &recordingIPInfoProvider{
		cached: map[string]int{residentialIP: residentialASN},
		live:   map[string]int{starlinkIP: starlinkASN},
	}
	cache, err := NewIPInfoCache(nil, nil, provider)
	if err != nil {
		t.Fatal(err)
	}
	h := historyOf("user-a", oldEntry(1, residentialIP, time.Now()), oldEntry(2, starlinkIP, time.Now()))

	if _, _, err := backfillLoginHistoryASNs(context.Background(), cache, h); err != nil {
		t.Fatalf("backfill: %v", err)
	}

	if calls := provider.liveCalls(); len(calls) != 0 {
		t.Errorf("backfill asked a provider live for %v; it may only read what Redis already holds", calls)
	}
	asns := h.clientIPASNs()
	if asns[residentialIP] != residentialASN {
		t.Errorf("ASN for %s = %d, want AS%d from the provider's cache", residentialIP, asns[residentialIP], residentialASN)
	}
	if asns[starlinkIP] != 0 {
		t.Errorf("ASN for %s = %d; nothing is cached for it, so nothing may be recorded", starlinkIP, asns[starlinkIP])
	}
}

// TestRecordLoginASN_LooksUpNoOtherAddress: a login records the ASN its own IP
// info lookup returned for its own address, and looks nothing else up -- not
// live, and not from the cache.
func TestRecordLoginASN_LooksUpNoOtherAddress(t *testing.T) {
	provider := &recordingIPInfoProvider{
		cached: map[string]int{residentialIP: residentialASN},
		live:   map[string]int{residentialIP: residentialASN},
	}
	// Installed as the process-global cache: a login has no resolver to be
	// handed, so the lookup this guards against would have to reach for one.
	installIPInfoCache(t, provider)
	xpid := mustXPID(t, "OVR-ORG-3930901337016247")
	h := NewLoginHistory("66666666-6666-6666-6666-666666666666")
	h.Update(xpid, starlinkIP, &evr.LoginProfile{}, true)
	other := oldEntry(1, residentialIP, time.Now().Add(-time.Hour))
	h.History[other.Key()] = other

	recordLoginASN(h, starlinkIP, asnIPInfo{asn: starlinkASN})

	if len(provider.getCalls) != 0 || len(provider.cachedCalls) != 0 {
		t.Errorf("the login looked up live %v and cached %v; it may use only its own lookup of %s", provider.getCalls, provider.cachedCalls, starlinkIP)
	}
	if got := h.clientIPASNs(); got[starlinkIP] != starlinkASN || got[residentialIP] != 0 {
		t.Errorf("recorded ASNs = %v; want only %s as AS%d", got, starlinkIP, starlinkASN)
	}
}
