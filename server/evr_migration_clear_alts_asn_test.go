package server

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/heroiclabs/nakama-common/api"
	"github.com/heroiclabs/nakama/v3/server/evr"
)

// Addresses the migration fixtures log in from. Documentation ranges, so no
// test can be mistaken for a real player's address.
const (
	cachedResidentialIP = "203.0.113.7"   // seedLinkedAccount's address
	uncachedIP          = "198.51.100.77" // no provider has a cached answer for it
)

// recordingIPInfoProvider is an IPInfoProvider that separates the two ways a
// provider can be asked about an address, and records both.
//
// Get is the live path: in production it reads Redis and, on a miss, calls
// retrieve, which is an HTTP request to IPQS or ip-api. Here a miss on Get
// "fetches" from live, so a caller that uses Get where it should not gets a
// plausible answer and nothing but the call record gives it away.
//
// GetCached is the Redis-only path: cached is what the provider's Redis keys
// hold, and a miss is (nil, nil).
type recordingIPInfoProvider struct {
	mu sync.Mutex

	cached   map[string]int // ip -> ASN, what Redis holds
	live     map[string]int // ip -> ASN, what a live fetch would return
	cacheErr error          // a Redis failure, not a miss

	getCalls    []string
	cachedCalls []string
}

func (p *recordingIPInfoProvider) Name() string { return "recording" }

func (p *recordingIPInfoProvider) Get(_ context.Context, ip string) (IPInfo, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.getCalls = append(p.getCalls, ip)
	if asn, ok := p.cached[ip]; ok {
		return asnIPInfo{asn: asn}, nil
	}
	if asn, ok := p.live[ip]; ok {
		return asnIPInfo{asn: asn}, nil
	}
	return nil, nil
}

func (p *recordingIPInfoProvider) GetCached(_ context.Context, ip string) (IPInfo, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.cachedCalls = append(p.cachedCalls, ip)
	if p.cacheErr != nil {
		return nil, p.cacheErr
	}
	if asn, ok := p.cached[ip]; ok {
		return asnIPInfo{asn: asn}, nil
	}
	return nil, nil
}

func (p *recordingIPInfoProvider) liveCalls() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.getCalls...)
}

// installIPInfoCache makes a cache over providers the process-global IP info
// cache for the duration of the test. Not safe alongside a parallel test that
// reads the global; none of the migration tests is parallel.
func installIPInfoCache(t *testing.T, providers ...IPInfoProvider) *IPInfoCache {
	t.Helper()
	cache, err := NewIPInfoCache(nil, nil, providers...)
	if err != nil {
		t.Fatalf("NewIPInfoCache: %v", err)
	}
	prev := globalIPInfoCache.Load()
	globalIPInfoCache.Store(cache)
	t.Cleanup(func() { globalIPInfoCache.Store(prev) })
	return cache
}

// ensureAltClearPreconditions supplies what the migration refuses to run
// without, for tests that are about something else: a configured IP info cache
// that has every fixture address cached as residential, and a detector whose
// settings have arrived. A test that installed either itself keeps its own.
func ensureAltClearPreconditions(t *testing.T) {
	t.Helper()
	if globalIPInfoCache.Load() == nil {
		installIPInfoCache(t, &recordingIPInfoProvider{cached: map[string]int{
			cachedResidentialIP: residentialASN,
			"198.51.100.5":      residentialASN, // newRecomputeFixture
			"203.0.113.9":       residentialASN, // newRecomputeFixture
		}})
	}
	if GetCGNATDetector() == nil {
		installCGNATDetector(t, testDetector(t))
	}
}

// seedAccountOnIP stores a searchable login history for userID: one entry from
// ip, with an XPID and HMD serial unique to accountID, and no ASN recorded --
// the shape of every history stored before ASNs were. links, when non-nil, are
// the stored AlternateMatches (other user -> items), and their keys the stored
// SecondDegreeAlternates.
func seedAccountOnIP(t *testing.T, m *altClearTestModule, userID, ip string, accountID uint64, links map[string][]string) {
	t.Helper()

	xpid := evr.EvrId{PlatformCode: evr.OVR, AccountId: accountID}
	h := NewLoginHistory(userID)
	h.History = map[string]*LoginHistoryEntry{
		loginHistoryEntryKey(xpid, ip): {
			CreatedAt: time.Now().Add(-24 * time.Hour),
			UpdatedAt: time.Now().Add(-time.Hour),
			XPID:      xpid,
			ClientIP:  ip,
			LoginData: &evr.LoginProfile{HMDSerialNumber: "HMD-" + userID},
		},
	}
	if links != nil {
		h.AlternateMatches = make(map[string][]*AlternateSearchMatch, len(links))
		for other, items := range links {
			h.AlternateMatches[other] = []*AlternateSearchMatch{{OtherUserID: other, Items: items}}
			h.SecondDegreeAlternates = append(h.SecondDegreeAlternates, other)
		}
	}

	data, err := json.Marshal(h)
	if err != nil {
		t.Fatalf("marshal seed history for %s: %v", userID, err)
	}
	if len(h.AltSearchPatterns()) == 0 {
		t.Fatalf("fixture is inert: %s has no search patterns, so phase 2 skips it as unsearchable before any ASN question arises", userID)
	}

	version := m.seedObject(userID, LoginStorageCollection, LoginHistoryStorageKey, string(data))
	m.listed = append(m.listed, &api.StorageObject{
		Collection: LoginStorageCollection,
		Key:        LoginHistoryStorageKey,
		UserId:     userID,
		Value:      string(data),
		Version:    version,
	})
}

// storedLinkFields returns the raw stored bytes of the two link fields, so
// "left as it was" is asserted on bytes rather than on a re-parse.
func storedLinkFields(t *testing.T, m *altClearTestModule, userID string) (alternates, secondDegree string) {
	t.Helper()
	var raw map[string]json.RawMessage
	if err := json.Unmarshal([]byte(m.storedValue(t, userID)), &raw); err != nil {
		t.Fatalf("unmarshal stored history for %s: %v", userID, err)
	}
	return string(raw["alternate_accounts"]), string(raw["second_degree"])
}

// storedASN returns the ASN stored on userID's entries for ip, 0 if none.
func storedASN(t *testing.T, m *altClearTestModule, userID, ip string) int {
	t.Helper()
	return m.storedHistory(t, userID).clientIPASNs()[ip]
}

// TestClearAltsMigration_ReadsASNFromCacheNeverFetchesLive: the migration
// learns an address's ASN from what the IP info providers already hold in
// Redis, and asks no provider anything live -- not for a cached address, and
// not for one with no cache entry, which is where IPInfoCache.Get would fall
// through to an HTTP request.
func TestClearAltsMigration_ReadsASNFromCacheNeverFetchesLive(t *testing.T) {
	const (
		resolvable = "11111111-1111-1111-1111-111111111111"
		missing    = "22222222-2222-2222-2222-222222222222"
	)
	installCGNATDetector(t, testDetector(t))
	provider := &recordingIPInfoProvider{
		cached: map[string]int{cachedResidentialIP: residentialASN},
		live:   map[string]int{uncachedIP: residentialASN},
	}
	installIPInfoCache(t, provider)

	nk := newAltClearTestModule()
	seedAccountOnIP(t, nk, resolvable, cachedResidentialIP, 1001, nil)
	seedAccountOnIP(t, nk, missing, uncachedIP, 1002, nil)

	runAltClearMigration(t, nk)

	if calls := provider.liveCalls(); len(calls) != 0 {
		t.Errorf("the migration asked a provider live for %v; it may only read what Redis already holds", calls)
	}
	if got := storedASN(t, nk, resolvable, cachedResidentialIP); got != residentialASN {
		t.Errorf("stored ASN for %s = %d, want AS%d from the provider's cache", cachedResidentialIP, got, residentialASN)
	}
	if got := storedASN(t, nk, missing, uncachedIP); got != 0 {
		t.Errorf("stored ASN for %s = %d; nothing is cached for it, so nothing may be recorded", uncachedIP, got)
	}
}

// TestClearAltsMigration_UnbackfilledStrangersAreNotLinked is #596 as the
// migration would commit it: two strangers whose only common value is a
// Starlink exit address, in histories stored before ASNs were recorded.
// Unknown is classified by the CIDRs alone, so without the ASN the address is
// a strong signal and a discovery key, and the one-shot run would link them for
// good.
func TestClearAltsMigration_UnbackfilledStrangersAreNotLinked(t *testing.T) {
	const (
		userA = "11111111-1111-1111-1111-111111111111"
		userB = "22222222-2222-2222-2222-222222222222"
	)
	for _, tc := range []struct {
		name   string
		cached map[string]int
	}{
		{name: "asn cached", cached: map[string]int{starlinkIP: starlinkASN}},
		{name: "cache miss", cached: map[string]int{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			withDetector(t, seededCGNATSettings())
			installIPInfoCache(t, &recordingIPInfoProvider{cached: tc.cached})

			base := newAltClearTestModule()
			seedAccountOnIP(t, base, userA, starlinkIP, 2001, nil)
			seedAccountOnIP(t, base, userB, starlinkIP, 2002, nil)
			nk := &altIndexTestModule{altClearTestModule: base}

			runAltClearMigration(t, nk)

			for _, pair := range [2][2]string{{userA, userB}, {userB, userA}} {
				if links := base.storedHistory(t, pair[0]).AlternateMatches; len(links[pair[1]]) != 0 {
					t.Errorf("%s is linked to %s on %s after the migration; they share only a Starlink exit address (AS%d)",
						pair[0], pair[1], matchItems(links[pair[1]]), starlinkASN)
				}
			}
		})
	}
}

// TestClearAltsMigration_UnresolvedAccountKeepsItsLinks: an account holding an
// address with no cached ASN keeps its stored links exactly as they were --
// neither cleared nor rebuilt. The run cannot classify that address, and the
// one-shot marker would make whatever it wrote final.
//
// The index answers nothing, so a rebuild of this account would find no
// alternates and a clear would be persisted: the cold-cache erasure.
func TestClearAltsMigration_UnresolvedAccountKeepsItsLinks(t *testing.T) {
	const (
		userID = "33333333-3333-3333-3333-333333333333"
		linked = "44444444-4444-4444-4444-444444444444"
	)
	installCGNATDetector(t, testDetector(t))
	installIPInfoCache(t, &recordingIPInfoProvider{cached: map[string]int{}})

	nk := newAltClearTestModule()
	seedAccountOnIP(t, nk, userID, uncachedIP, 3001, map[string][]string{linked: {uncachedIP}})
	alternatesBefore, secondBefore := storedLinkFields(t, nk, userID)
	if alternatesBefore == "null" || secondBefore == "null" {
		t.Fatalf("fixture carries no links (%s / %s); the assertion below would be vacuous", alternatesBefore, secondBefore)
	}

	runAltClearMigration(t, nk)

	alternatesAfter, secondAfter := storedLinkFields(t, nk, userID)
	if alternatesAfter != alternatesBefore {
		t.Errorf("alternate_accounts changed for an account with an unresolved address:\n before: %s\n after:  %s", alternatesBefore, alternatesAfter)
	}
	if secondAfter != secondBefore {
		t.Errorf("second_degree changed for an account with an unresolved address:\n before: %s\n after:  %s", secondBefore, secondAfter)
	}
}

// TestClearAltsMigration_CacheMissIsLoggedAndRunContinues: a miss is logged at
// INFO with the account's ID, so the accounts left as they were can be grepped
// out of the log, and it does not stop the run -- the resolvable account listed
// after it is still processed.
func TestClearAltsMigration_CacheMissIsLoggedAndRunContinues(t *testing.T) {
	const (
		missing    = "55555555-5555-5555-5555-555555555555"
		resolvable = "66666666-6666-6666-6666-666666666666"
		linkTarget = "99999999-9999-9999-9999-999999999999"
	)
	installCGNATDetector(t, testDetector(t))
	installIPInfoCache(t, &recordingIPInfoProvider{cached: map[string]int{cachedResidentialIP: residentialASN}})

	nk := newAltClearTestModule()
	seedAccountOnIP(t, nk, missing, uncachedIP, 5001, map[string][]string{linkTarget: {uncachedIP}})
	nk.seedLinkedAccount(t, resolvable, linkTarget) // listed after the miss

	logger := runAltClearMigration(t, nk)

	if !loggedForUser(logger, "info", missing, "asn_unresolved") {
		t.Errorf("no INFO line carries user_id %s and asn_unresolved; the accounts a run left as they were must be greppable", missing)
	}
	if got := completionField(t, logger, "walked"); got != 2 {
		t.Errorf("walked = %d, want 2", got)
	}
	if links := nk.storedHistory(t, resolvable).AlternateMatches; len(links) != 0 {
		t.Errorf("%s, listed after the miss, still has its stale links %v: the run did not keep going", resolvable, links)
	}
}

// TestClearAltsMigration_MarkerWrittenDespiteCacheMisses: a miss is not a
// failure of the run. Retrying cannot resolve it -- an address gains a cache
// entry only when someone logs in from it again -- so a marker withheld for it
// would re-walk every account on every boot.
func TestClearAltsMigration_MarkerWrittenDespiteCacheMisses(t *testing.T) {
	installCGNATDetector(t, testDetector(t))
	installIPInfoCache(t, &recordingIPInfoProvider{cached: map[string]int{}})

	nk := newAltClearTestModule()
	seedAccountOnIP(t, nk, "77777777-7777-7777-7777-777777777777", uncachedIP, 7001, nil)

	logger := runAltClearMigration(t, nk)

	if got := completionField(t, logger, "asn_unresolved"); got != 1 {
		t.Fatalf("asn_unresolved = %d, want 1: the fixture must actually miss", got)
	}
	if storedMarker(t, nk) == nil {
		t.Error("no completion marker after a clean run with a cache miss; every boot would walk the whole table again")
	}
}

// TestClearAltsMigration_CacheReadErrorAbortsWithoutMarker: a Redis failure is
// not a miss. Reading it as one would leave every account it touched as it was
// and then mark the run complete, so the run stops and stays owed.
func TestClearAltsMigration_CacheReadErrorAbortsWithoutMarker(t *testing.T) {
	installCGNATDetector(t, testDetector(t))
	installIPInfoCache(t, &recordingIPInfoProvider{cacheErr: errors.New("failed to get data from redis: i/o timeout")})

	nk := newAltClearTestModule()
	nk.seedLinkedAccount(t, "88888888-8888-8888-8888-888888888888", "99999999-9999-9999-9999-999999999999")

	if _, err := runAltClearMigrationExpectingError(t, nk); !strings.Contains(err.Error(), "i/o timeout") {
		t.Errorf("error = %q, want it to carry the cache read failure", err)
	}
	if marker := storedMarker(t, nk); marker != nil {
		t.Errorf("a run that could not read the cache recorded a completion marker (%+v)", marker)
	}
}

// TestClearAltsMigration_LinkedPairInOnePageBothCommit: two accounts that link
// to each other and are walked in the same page must both have their own rows
// committed. UpdateAlternates writes the far side of each link; a write that
// moves the other row's version on under the page snapshot rejects that row's
// own write, and with a one-shot marker there is no later run to retry it.
func TestClearAltsMigration_LinkedPairInOnePageBothCommit(t *testing.T) {
	nk, userA, userB, _, _ := newRecomputeFixture(t)
	ensureAltClearPreconditions(t)

	logger := runAltClearMigration(t, nk)

	walked, rebuilt, conflicted := completionField(t, logger, "walked"), completionField(t, logger, "rebuilt"), completionField(t, logger, "conflicted")
	t.Logf("walked=%d rebuilt=%d conflicted=%d", walked, rebuilt, conflicted)
	if rebuilt != 2 || conflicted != 0 {
		t.Errorf("walked=%d rebuilt=%d conflicted=%d, want rebuilt=2 conflicted=0: the pair's own rows did not commit", walked, rebuilt, conflicted)
	}
	for _, pair := range [2][2]string{{userA, userB}, {userB, userA}} {
		stored := nk.storedHistory(t, pair[0])
		if len(stored.AlternateMatches[pair[1]]) == 0 {
			t.Errorf("%s has no stored link to %s", pair[0], pair[1])
		}
	}
}

// TestClearAltsMigration_RefusesWithoutIPInfoCache: a cache-only read with no
// cache resolves nothing, so the run would skip every account and then mark
// itself complete. It refuses instead: no walk, no marker.
func TestClearAltsMigration_RefusesWithoutIPInfoCache(t *testing.T) {
	for _, tc := range []struct {
		name    string
		install func(t *testing.T)
	}{
		{name: "nil", install: func(t *testing.T) {
			prev := globalIPInfoCache.Load()
			globalIPInfoCache.Store(nil)
			t.Cleanup(func() { globalIPInfoCache.Store(prev) })
		}},
		{name: "unconfigured", install: func(t *testing.T) { installIPInfoCache(t) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			installCGNATDetector(t, testDetector(t))
			tc.install(t)
			assertAltClearRefuses(t, &MigrationClearAlternateMatches{readyWait: 50 * time.Millisecond})
		})
	}
}

// TestClearAltsMigration_RefusesWhenSettingsNeverArrive: until settings reach
// the detector it knows no CGNAT ASNs or CIDRs, so every Starlink address
// would read as strong. The run waits for them, bounded, and refuses if they
// do not come.
func TestClearAltsMigration_RefusesWhenSettingsNeverArrive(t *testing.T) {
	installCGNATDetector(t, NewCGNATDetector(nil)) // settings never applied
	installIPInfoCache(t, &recordingIPInfoProvider{cached: map[string]int{cachedResidentialIP: residentialASN}})
	assertAltClearRefuses(t, &MigrationClearAlternateMatches{readyWait: 50 * time.Millisecond})
}

func assertAltClearRefuses(t *testing.T, m *MigrationClearAlternateMatches) {
	t.Helper()
	base := newAltClearTestModule()
	base.seedLinkedAccount(t, "11111111-1111-1111-1111-111111111111", "22222222-2222-2222-2222-222222222222")
	nk := &listCountingModule{altClearTestModule: base}

	if err := m.MigrateSystem(context.Background(), newCaptureLogger(), nil, nk); err == nil {
		t.Error("MigrateSystem returned nil; want a refusal")
	}
	if nk.listCalls != 0 {
		t.Errorf("StorageList called %d times; a refused run must not walk storage", nk.listCalls)
	}
	if marker := storedMarker(t, base); marker != nil {
		t.Errorf("a refused run recorded a completion marker (%+v)", marker)
	}
}

// loggedForUser reports whether a line at level carries user_id userID and the
// field key.
func loggedForUser(l *captureLogger, level, userID, key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, e := range *l.events {
		if e.level != level || e.fields["user_id"] != userID {
			continue
		}
		if _, ok := e.fields[key]; ok {
			return true
		}
	}
	return false
}

// TestClearAltsMigration_RerunOverConvergedLinksWritesNothing: the operator
// re-run (marker cleared) over converged data writes no login history at all --
// not the account's own row, and not the far side of its links. An
// unconditional far-side write in UpdateAlternates rewrites every linked row
// and moves its version on, which is what rejects a row the run then writes
// itself.
func TestClearAltsMigration_RerunOverConvergedLinksWritesNothing(t *testing.T) {
	nk, userA, userB, _, _ := newRecomputeFixture(t)
	ensureAltClearPreconditions(t)

	runAltClearMigration(t, nk)
	for _, pair := range [2][2]string{{userA, userB}, {userB, userA}} {
		if len(nk.storedHistory(t, pair[0]).AlternateMatches[pair[1]]) == 0 {
			t.Fatalf("first run did not link %s to %s; there is nothing converged to re-run over", pair[0], pair[1])
		}
	}

	clearMigrationMarker(t, nk.altClearTestModule)
	nk.writeBatches = nil
	runAltClearMigration(t, nk)

	var historyWrites []string
	for _, batch := range nk.writeBatches {
		for _, userID := range batch {
			if userID != SystemUserID {
				historyWrites = append(historyWrites, userID)
			}
		}
	}
	if len(historyWrites) != 0 {
		t.Errorf("the re-run over converged links wrote login histories %v; a write that changes nothing must not be made", historyWrites)
	}
}
