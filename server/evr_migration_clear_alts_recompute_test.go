package server

import (
	"context"
	"encoding/json"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/heroiclabs/nakama-common/api"
	"github.com/heroiclabs/nakama/v3/server/evr"
)

// --- The #589 fixture ---------------------------------------------------
//
// Issue #589: `commodity_profile_prefixes` carried an empty string, so
// strings.HasPrefix(x, "") was true for every x and CGNATDetector.IsWeakSignal
// returned true for every non-IP item. matchIgnoredAltPattern
// (server/evr_authenticate_history.go:50) consults IsWeakSignal for non-IP
// patterns, so XPIDs, HMD serials and system profiles were stripped out of
// LoginHistory.Cache (rebuildCache, :607) and out of AltSearchPatterns
// (server/evr_authenticate_alts.go:106). The indexed discovery field was left
// holding IP addresses and nothing else -- which is why all 7,254 production
// alt links rest on a shared IP and not one carries an XPID or a serial.
//
// The code defect is fixed elsewhere. These tests are about the OTHER half:
// the stored records are still degraded, and only a migration recomputes them.

// altIndexTestModule is altClearTestModule with two inert methods replaced by
// ones that model the machine the migration actually runs against.
//
// StorageIndexList in the base returns nil unconditionally -- "no alternates
// found". That is the right double for the questions the original tests ask
// (does a successful rebuild persist the clear, does a rejected batch roll
// back) but it cannot express #589 at all, because the whole defect lives in
// WHAT THE INDEX CONTAINS. Discovery reads the STORED `value.cache` field of
// OTHER accounts, so an account whose stored cache was degraded is invisible
// to every search no matter how correct the searcher's own patterns are. This
// override answers from the stored cache, so that dependency is real.
//
// StorageList in the base returns a frozen snapshot seeded at fixture time.
// The migration's phase 2 must observe phase 1's writes -- both the repaired
// caches and the bumped versions -- so this override lists live objects.
type altIndexTestModule struct {
	*altClearTestModule
}

func (m *altIndexTestModule) StorageList(ctx context.Context, callerID, userID, collection string, limit int, cursor string) ([]*api.StorageObject, string, error) {
	if cursor != "" {
		return nil, "", nil
	}
	return m.liveLoginObjects(), "", nil
}

func (m *altIndexTestModule) StorageIndexList(ctx context.Context, callerID, indexName, query string, limit int, order []string, cursor string) (*api.StorageObjects, string, error) {
	if cursor != "" {
		return &api.StorageObjects{}, "", nil
	}

	// LoginAlternatePatternSearch builds "+value.cache:/(a|b|c)/" from
	// Query.CreateMatchPattern (server/evr_authenticate_alts.go:129), which
	// regex-escapes each term. A stored cache entry is a hit when its escaped
	// form appears among those alternatives.
	matched := make([]*api.StorageObject, 0)
	for _, obj := range m.liveLoginObjects() {
		var probe struct {
			Cache []string `json:"cache"`
		}
		if err := json.Unmarshal([]byte(obj.Value), &probe); err != nil {
			continue
		}
		for _, item := range probe.Cache {
			if item == "" {
				continue
			}
			if containsTerm(query, regexEscapeForBluge(item)) {
				matched = append(matched, obj)
				break
			}
		}
	}
	return &api.StorageObjects{Objects: matched}, "", nil
}

// liveLoginObjects returns the current Login/history objects, ordered by user
// ID so page composition is deterministic.
func (m *altIndexTestModule) liveLoginObjects() []*api.StorageObject {
	m.mu.Lock()
	defer m.mu.Unlock()

	out := make([]*api.StorageObject, 0, len(m.objects))
	for _, obj := range m.objects {
		if obj.Collection != LoginStorageCollection || obj.Key != LoginHistoryStorageKey {
			continue
		}
		// Field-by-field, not *obj: api.StorageObject is a protobuf message
		// and carries a mutex, so copying the struct trips go vet.
		out = append(out, &api.StorageObject{
			Collection: obj.Collection,
			Key:        obj.Key,
			UserId:     obj.UserId,
			Value:      obj.Value,
			Version:    obj.Version,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].UserId < out[j].UserId })
	return out
}

// seedDegradedAccount stores a login history in the exact shape #589 left
// behind: the raw History entries are intact (they are the source of truth and
// the defect never touched them), but the indexed `cache` carries only the IP.
//
// The record is built by marshalling a correct history and then overwriting
// the cache field, because LoginHistory.MarshalJSON calls rebuildCache and
// would otherwise refuse to produce a degraded record.
func seedDegradedAccount(t *testing.T, m *altClearTestModule, userID, clientIP, serial string, xpid evr.EvrId) {
	t.Helper()

	h := NewLoginHistory(userID)
	h.History = map[string]*LoginHistoryEntry{
		loginHistoryEntryKey(xpid, clientIP): {
			CreatedAt: time.Now().Add(-24 * time.Hour),
			UpdatedAt: time.Now().Add(-time.Hour),
			XPID:      xpid,
			ClientIP:  clientIP,
			LoginData: &evr.LoginProfile{HMDSerialNumber: serial},
		},
	}

	healthy, err := json.Marshal(h)
	if err != nil {
		t.Fatalf("marshal seed history for %s: %v", userID, err)
	}

	// Guard the fixture: a healthy record must index the strong signals, or
	// "the degraded record is missing them" asserts nothing.
	if !slices.Contains(h.Cache, serial) || !slices.Contains(h.Cache, xpid.Token()) {
		t.Fatalf("fixture is inert: a healthy cache for %s is %v, which already lacks the serial or the XPID", userID, h.Cache)
	}

	var raw map[string]json.RawMessage
	if err := json.Unmarshal(healthy, &raw); err != nil {
		t.Fatalf("unmarshal seed history for %s: %v", userID, err)
	}
	degradedCache, err := json.Marshal([]string{clientIP})
	if err != nil {
		t.Fatalf("marshal degraded cache for %s: %v", userID, err)
	}
	raw["cache"] = degradedCache
	degraded, err := json.Marshal(raw)
	if err != nil {
		t.Fatalf("marshal degraded history for %s: %v", userID, err)
	}

	version := m.seedObject(userID, LoginStorageCollection, LoginHistoryStorageKey, string(degraded))
	m.listed = append(m.listed, &api.StorageObject{
		Collection: LoginStorageCollection,
		Key:        LoginHistoryStorageKey,
		UserId:     userID,
		Value:      string(degraded),
		Version:    version,
	})
}

// altMatchItems flattens the items a stored link was formed on.
func altMatchItems(matches []*AlternateSearchMatch) []string {
	seen := make(map[string]bool)
	out := make([]string, 0)
	for _, m := range matches {
		for _, item := range m.Items {
			if !seen[item] {
				seen[item] = true
				out = append(out, item)
			}
		}
	}
	sort.Strings(out)
	return out
}

// newRecomputeFixture builds two accounts that share an XPID and an HMD serial
// but log in from DIFFERENT public IPs, with both records degraded.
//
// Different IPs is the whole point. Under #589 the only surviving cache key
// was the IP, so a pair like this one -- the shape alt detection exists to
// catch, one person on two accounts from two networks -- carries zero stored
// links. Recomputing it is the migration's job.
func newRecomputeFixture(t *testing.T) (*altIndexTestModule, string, string, evr.EvrId, string) {
	t.Helper()

	// A realistic prefix list: non-empty entries only. This is the world
	// after the one-character fix in server/evr_cgnat.go lands.
	d := testDetector(t)
	d.commodityProfilePrefixes = []string{"Meta Quest 3::", "Meta Quest 2::"}
	SetCGNATDetector(d)
	t.Cleanup(func() { SetCGNATDetector(nil) })

	const (
		userA  = "11111111-1111-1111-1111-111111111111"
		userB  = "22222222-2222-2222-2222-222222222222"
		serial = "WMHD1234567890"
	)
	xpid := evr.EvrId{PlatformCode: 4, AccountId: 1000}

	// Guard: an XPID or serial that matchIgnoredAltPattern already drops
	// would make every assertion below vacuous.
	if matchIgnoredAltPattern(serial) {
		t.Fatalf("fixture is inert: serial %q is in the ignored set", serial)
	}
	if matchIgnoredAltPattern(xpid.Token()) {
		t.Fatalf("fixture is inert: XPID %q is in the ignored set", xpid.Token())
	}

	base := newAltClearTestModule()
	seedDegradedAccount(t, base, userA, "198.51.100.5", serial, xpid)
	seedDegradedAccount(t, base, userB, "203.0.113.9", serial, xpid)

	return &altIndexTestModule{altClearTestModule: base}, userA, userB, xpid, serial
}

// TestClearAltsMigration_RecomputesXPIDAndSerialLinks is the #589 gate.
//
// Two accounts sharing an XPID and an HMD serial across different IPs have no
// stored link, because the degraded cache made them mutually invisible to
// discovery. After the migration both sides must carry the link, and the link
// must be formed ON the XPID and the serial -- not on some incidental key.
func TestClearAltsMigration_RecomputesXPIDAndSerialLinks(t *testing.T) {
	nk, userA, userB, xpid, serial := newRecomputeFixture(t)

	// Precondition: nothing is linked yet, which is the production state.
	for _, userID := range []string{userA, userB} {
		if got := nk.storedHistory(t, userID).AlternateMatches; len(got) != 0 {
			t.Fatalf("fixture is not degraded: %s already has links %v", userID, got)
		}
	}

	runAltClearMigration(t, nk)

	for _, pair := range [2][2]string{{userA, userB}, {userB, userA}} {
		self, other := pair[0], pair[1]
		stored := nk.storedHistory(t, self)

		matches, ok := stored.AlternateMatches[other]
		if !ok {
			t.Fatalf("%s has no link to %s after the migration; stored links are %v — the recompute did not restore the XPID/serial edge", self, other, stored.AlternateMatches)
		}

		items := altMatchItems(matches)
		if !slices.Contains(items, serial) {
			t.Errorf("%s -> %s link items = %v, missing the HMD serial %q", self, other, items, serial)
		}
		if !slices.Contains(items, xpid.String()) {
			t.Errorf("%s -> %s link items = %v, missing the XPID %q", self, other, items, xpid.String())
		}

		// The indexed cache must be repaired too, or the next search over
		// this record is blind again.
		if !slices.Contains(stored.Cache, serial) || !slices.Contains(stored.Cache, xpid.Token()) {
			t.Errorf("%s stored cache = %v, still missing the serial or the XPID", self, stored.Cache)
		}
	}
}

// TestClearAltsMigration_IsIdempotent runs the migration twice and requires the
// second run to change nothing.
//
// The completion marker means production only gets one run, so this is no
// longer the per-boot cost argument it was written as. It is now the gate on
// the operator re-run path: clearing the marker must be a safe thing to do, and
// it is only safe if a second pass over converged data is a no-op. The marker
// is cleared between the two runs below for exactly that reason -- that is the
// operator action, not a test convenience.
func TestClearAltsMigration_IsIdempotent(t *testing.T) {
	nk, userA, userB, _, _ := newRecomputeFixture(t)

	runAltClearMigration(t, nk)
	after := map[string]string{
		userA: nk.storedValue(t, userA),
		userB: nk.storedValue(t, userB),
	}

	clearMigrationMarker(t, nk.altClearTestModule)
	logger := runAltClearMigration(t, nk)

	for _, userID := range []string{userA, userB} {
		if got := nk.storedValue(t, userID); got != after[userID] {
			t.Errorf("second run changed %s\n first:  %s\n second: %s", userID, after[userID], got)
		}
	}
	if got := completionField(t, logger, "rebuilt"); got != 0 {
		t.Errorf("rebuilt = %d on the second run, want 0: the migration persisted rows it did not need to", got)
	}
	// conflicted must be asserted alongside rebuilt, not instead of it.
	// rebuilt counts rows that COMMITTED, so it also reads 0 when every row
	// was submitted and rejected — which is what happens if the migration
	// submits rows it did not need to submit, because UpdateAlternates has
	// already bumped their versions out from under the batch. Without this
	// line, restoring the original "write whenever the account had links"
	// condition leaves the test passing.
	if got := completionField(t, logger, "conflicted"); got != 0 {
		t.Errorf("conflicted = %d on the second run, want 0: the migration submitted writes it did not need to", got)
	}
	if got := completionField(t, logger, "cache_repaired"); got != 0 {
		t.Errorf("cache_repaired = %d on the second run, want 0: the cache pass rewrote rows it did not need to", got)
	}
	// Positive control: the second run must still have examined both rows,
	// or "changed nothing" is only true because it did nothing.
	if got := completionField(t, logger, "walked"); got != 2 {
		t.Errorf("walked = %d on the second run, want 2", got)
	}
}

// storedValue returns the raw stored JSON, so "changed nothing" is asserted on
// bytes rather than on a re-parse that could hide a difference.
func (m *altClearTestModule) storedValue(t *testing.T, userID string) string {
	t.Helper()
	m.mu.Lock()
	defer m.mu.Unlock()
	obj, ok := m.objects[occStorageKey(userID, LoginStorageCollection, LoginHistoryStorageKey)]
	if !ok {
		t.Fatalf("no stored login history for %s", userID)
	}
	return obj.Value
}

// containsTerm reports whether the index query offers term as one of its
// alternatives. Matching the whole alternative rather than doing a substring
// test keeps a short term from spuriously matching inside a longer one.
func containsTerm(query, term string) bool {
	start := strings.Index(query, "/(")
	end := strings.LastIndex(query, ")/")
	if start < 0 || end <= start+2 {
		return false
	}
	return slices.Contains(strings.Split(query[start+2:end], "|"), term)
}
