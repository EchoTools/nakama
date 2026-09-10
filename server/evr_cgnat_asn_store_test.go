package server

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/heroiclabs/nakama-common/api"
	"github.com/heroiclabs/nakama-common/runtime"
	"go.uber.org/zap"
)

// Fixture rows in iptoasn.com's ip2asn-v{4,6}.tsv format:
// range_start, range_end, AS number, country, AS description.
//
// Every test here feeds these through an injected fetcher. None touches the
// network or the /var/tmp cache, so none depends on iptoasn.com being up or on
// what an earlier run left on disk.
var asnFixtureV4 = []string{
	"1.0.0.0\t1.0.0.255\t13335\tUS\tCLOUDFLARENET",
	"10.0.0.0\t10.255.255.255\t0\tNone\tNot routed",
	"73.162.0.0\t73.162.255.255\t7922\tUS\tCOMCAST-7922",
	"95.88.0.0\t95.91.255.255\t3209\tDE\tVODANET",
	"129.222.0.0\t129.222.255.255\t14593\tUS\tSPACEX-STARLINK",
	"172.56.0.0\t172.56.255.255\t21928\tUS\tT-MOBILE-AS21928",
	"172.58.0.0\t172.58.255.255\t21928\tUS\tT-MOBILE-AS21928",
}

var asnFixtureV6 = []string{
	"2406:2d40::\t2406:2d40:ffff:ffff:ffff:ffff:ffff:ffff\t14593\tUS\tSPACEX-STARLINK",
	"2600:1000::\t2600:1000:ffff:ffff:ffff:ffff:ffff:ffff\t7018\tUS\tATT-INTERNET4",
	"2607:fb90::\t2607:fb90:ffff:ffff:ffff:ffff:ffff:ffff\t21928\tUS\tT-MOBILE-AS21928",
	"2a02:8108::\t2a02:8108:ffff:ffff:ffff:ffff:ffff:ffff\t3209\tDE\tVODANET",
}

// Probe addresses, one per fixture row that matters.
const (
	probeStarlink4 = "129.222.210.50"
	probeTMobile4  = "172.56.91.132"
	probeComcast4  = "73.162.100.1" // residential, never CGNAT
	probeVodafone4 = "95.90.254.10"
	probeStarlink6 = "2406:2d40:100::1"
	probeATT6      = "2600:1000::1" // not in any configured list
	probeVodafone6 = "2a02:8108::1"
)

func gzipTSV(t *testing.T, rows []string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	for _, r := range rows {
		if _, err := zw.Write([]byte(r + "\n")); err != nil {
			t.Fatalf("gzip fixture: %v", err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("gzip fixture: %v", err)
	}
	return buf.Bytes()
}

// fixtureASNFetcher stands in for the iptoasn.com download.
type fixtureASNFetcher struct {
	mu    sync.Mutex
	gz    map[asnFamily][]byte
	fail  map[asnFamily]error
	calls map[asnFamily]int

	// transientFailures fails this many fetches, of either family, before the
	// configured behaviour applies.
	transientFailures int
}

func newFixtureASNFetcher(t *testing.T) *fixtureASNFetcher {
	t.Helper()
	return &fixtureASNFetcher{
		gz: map[asnFamily][]byte{
			asnFamilyV4: gzipTSV(t, asnFixtureV4),
			asnFamilyV6: gzipTSV(t, asnFixtureV6),
		},
		fail:  map[asnFamily]error{},
		calls: map[asnFamily]int{},
	}
}

func (f *fixtureASNFetcher) fetch(_ context.Context, family asnFamily) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls[family]++
	if f.transientFailures > 0 {
		f.transientFailures--
		return nil, errors.New("iptoasn.com unreachable (transient)")
	}
	if err := f.fail[family]; err != nil {
		return nil, err
	}
	return f.gz[family], nil
}

func (f *fixtureASNFetcher) setFailure(family asnFamily, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.fail[family] = err
}

func (f *fixtureASNFetcher) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls[asnFamilyV4] + f.calls[asnFamilyV6]
}

// signalingStorageNK is occTestNakamaModule plus a notification per successful
// StorageWrite, so a test can wait for the background refresher to persist
// without sleeping. It records the last write request so permissions and owner
// can be asserted; occTestNakamaModule does not keep them.
//
// Defect class 1 does not bite here: the production code under test reaches
// StorageWrite through the runtime.NakamaModule interface, which dispatches to
// this override. Only occTestNakamaModule's own MultiUpdate would bypass it, and
// nothing on this path calls MultiUpdate.
type signalingStorageNK struct {
	*occTestNakamaModule
	writes chan string

	mu        sync.Mutex
	lastWrite *runtime.StorageWrite
}

func newSignalingStorageNK() *signalingStorageNK {
	return &signalingStorageNK{
		occTestNakamaModule: newOCCTestNakamaModule(),
		writes:              make(chan string, 16),
	}
}

func (m *signalingStorageNK) StorageWrite(ctx context.Context, writes []*runtime.StorageWrite) ([]*api.StorageObjectAck, error) {
	acks, err := m.occTestNakamaModule.StorageWrite(ctx, writes)
	if err != nil {
		return nil, err
	}
	for _, w := range writes {
		m.mu.Lock()
		m.lastWrite = w
		m.mu.Unlock()
		m.writes <- w.Key
	}
	return acks, nil
}

func waitForStorageWrite(t *testing.T, nk *signalingStorageNK, why string) {
	t.Helper()
	select {
	case <-nk.writes:
	case <-time.After(10 * time.Second):
		t.Fatalf("no storage write within 10s: %s", why)
	}
}

func readStoredASNRanges(t *testing.T, nk runtime.NakamaModule) cgnatASNRangesData {
	t.Helper()
	objs, err := nk.StorageRead(context.Background(), []*runtime.StorageRead{{
		Collection: CGNATASNRangesStorageCollection,
		Key:        CGNATASNRangesStorageKey,
		UserID:     SystemUserID,
	}})
	if err != nil {
		t.Fatalf("StorageRead: %v", err)
	}
	if len(objs) != 1 {
		t.Fatalf("found %d objects at %s/%s owned by SystemUserID, want 1", len(objs), CGNATASNRangesStorageCollection, CGNATASNRangesStorageKey)
	}
	var stored cgnatASNRangesData
	if err := json.Unmarshal([]byte(objs[0].Value), &stored); err != nil {
		t.Fatalf("stored value does not unmarshal: %v\n%s", err, objs[0].Value)
	}
	return stored
}

// newFixtureDetector is a detector configured with settings and wired to the
// fixture fetcher. Nothing is loaded.
func newFixtureDetector(t *testing.T, settings CGNATSettings) (*CGNATDetector, *fixtureASNFetcher) {
	t.Helper()
	f := newFixtureASNFetcher(t)
	d := NewCGNATDetector(nil)
	d.fetchASN = f.fetch
	d.UpdateSettings(settings)
	return d, f
}

type cgnatProbe struct {
	ip   string
	want bool
}

func assertCGNAT(t *testing.T, d *CGNATDetector, why string, probes ...cgnatProbe) {
	t.Helper()
	for _, p := range probes {
		if got := d.IsCGNAT(p.ip); got != p.want {
			t.Errorf("%s: IsCGNAT(%q) = %v, want %v", why, p.ip, got, p.want)
		}
	}
}

// TestRefreshASNData_KeepsOnlyConfiguredASNs: the detector holds the ranges of
// the configured ASNs and nothing else. The full datasets are ~711k rows; the
// detector only ever asks whether an address is in a configured ASN.
func TestRefreshASNData_KeepsOnlyConfiguredASNs(t *testing.T) {
	d, _ := newFixtureDetector(t, seededCGNATSettings())

	if err := d.RefreshASNData(context.Background(), nil); err != nil {
		t.Fatalf("RefreshASNData: %v", err)
	}

	d.mu.RLock()
	var held []int
	for _, r := range d.asnRanges4 {
		held = append(held, r.ASN)
	}
	for _, r := range d.asnRanges6 {
		held = append(held, r.ASN)
	}
	n4, n6 := len(d.asnRanges4), len(d.asnRanges6)
	d.mu.RUnlock()

	if n4 != 3 || n6 != 2 {
		t.Errorf("detector holds %d IPv4 and %d IPv6 ranges, want 3 and 2 (the Starlink and T-Mobile rows of the fixture)", n4, n6)
	}
	for _, asn := range held {
		if asn != 14593 && asn != 21928 {
			t.Errorf("detector holds a range for AS%d, which is not configured; held ASNs: %v", asn, held)
		}
	}

	assertCGNAT(t, d, "after a complete refresh",
		cgnatProbe{probeStarlink4, true},
		cgnatProbe{probeTMobile4, true},
		cgnatProbe{probeStarlink6, true},
		cgnatProbe{probeComcast4, false},
		cgnatProbe{probeVodafone4, false},
		cgnatProbe{probeATT6, false},
	)
	if !d.ASNDataReady() {
		t.Error("ASNDataReady() = false after both datasets loaded for every configured ASN")
	}
}

// TestRefreshASNData_PartialFailureIsAnError: before #596, RefreshASNData
// reported an error only when BOTH datasets failed, so a v4-only failure --
// the one that matters for nearly every player -- looked like success.
func TestRefreshASNData_PartialFailureIsAnError(t *testing.T) {
	d, f := newFixtureDetector(t, seededCGNATSettings())
	f.setFailure(asnFamilyV6, errors.New("iptoasn.com: 503"))

	err := d.RefreshASNData(context.Background(), nil)
	if err == nil {
		t.Fatal("RefreshASNData returned nil with the IPv6 dataset unavailable; a partial refresh is not a success")
	}
	if !strings.Contains(err.Error(), "v6") {
		t.Errorf("RefreshASNData error %q does not name the family that failed", err)
	}

	// The family that loaded is usable; the one that did not fails closed.
	assertCGNAT(t, d, "IPv4 loaded, IPv6 failed",
		cgnatProbe{probeStarlink4, true},
		cgnatProbe{probeComcast4, false},
		cgnatProbe{probeATT6, true},
	)
	if d.ASNDataReady() {
		t.Error("ASNDataReady() = true with the IPv6 dataset never loaded")
	}
}

// TestRefreshASNData_EmptyDatasetIsAnError: a download that decompresses to no
// routed rows at all (a truncated body, a format change) must not be recorded
// as "these ASNs have no ranges". That would mark coverage complete with an
// empty table and answer not-CGNAT for every carrier address -- and persist it.
func TestRefreshASNData_EmptyDatasetIsAnError(t *testing.T) {
	d, f := newFixtureDetector(t, seededCGNATSettings())
	f.gz[asnFamilyV4] = gzipTSV(t, []string{"10.0.0.0\t10.255.255.255\t0\tNone\tNot routed"})

	if err := d.RefreshASNData(context.Background(), nil); err == nil {
		t.Fatal("RefreshASNData returned nil for an IPv4 dataset with zero routed rows")
	}
	assertCGNAT(t, d, "empty IPv4 dataset", cgnatProbe{probeComcast4, true})
	if d.ASNDataReady() {
		t.Error("ASNDataReady() = true after an empty IPv4 dataset")
	}
}

// TestCGNATASNRanges_StorageRoundTrip: a refresh persists only the filtered
// ranges, as a system-owned object in the shape Global/settings uses, and a
// fresh detector reading it back answers identically without a download.
func TestCGNATASNRanges_StorageRoundTrip(t *testing.T) {
	nk := newSignalingStorageNK()
	d, _ := newFixtureDetector(t, seededCGNATSettings())

	if err := d.RefreshASNData(context.Background(), nk); err != nil {
		t.Fatalf("RefreshASNData: %v", err)
	}

	nk.mu.Lock()
	w := nk.lastWrite
	nk.mu.Unlock()
	if w == nil {
		t.Fatal("RefreshASNData did not write the filtered ranges to storage")
	}
	if w.UserID != SystemUserID || w.PermissionRead != 0 || w.PermissionWrite != 0 {
		t.Errorf("stored as owner=%q read=%d write=%d, want SystemUserID with permissions 0/0 like Global/settings", w.UserID, w.PermissionRead, w.PermissionWrite)
	}

	stored := readStoredASNRanges(t, nk)
	for fam, got := range map[string]cgnatASNFamilyData{"v4": stored.V4, "v6": stored.V6} {
		if len(got.ASNs) != 2 {
			t.Errorf("%s: stored ASN list %v, want the configured [14593 21928]", fam, got.ASNs)
		}
		for _, r := range got.Ranges {
			if r.ASN != 14593 && r.ASN != 21928 {
				t.Errorf("%s: stored a range for AS%d, which is not configured", fam, r.ASN)
			}
		}
	}
	if len(stored.V4.Ranges) != 3 || len(stored.V6.Ranges) != 2 {
		t.Errorf("stored %d IPv4 and %d IPv6 ranges, want 3 and 2", len(stored.V4.Ranges), len(stored.V6.Ranges))
	}

	fresh := NewCGNATDetector(nil)
	fresh.fetchASN = func(context.Context, asnFamily) ([]byte, error) {
		t.Error("loading from storage fetched from the network")
		return nil, errors.New("no network in this test")
	}
	fresh.UpdateSettings(seededCGNATSettings())
	if err := fresh.LoadASNRanges(context.Background(), nk); err != nil {
		t.Fatalf("LoadASNRanges: %v", err)
	}
	assertCGNAT(t, fresh, "loaded from storage",
		cgnatProbe{probeStarlink4, true},
		cgnatProbe{probeTMobile4, true},
		cgnatProbe{probeStarlink6, true},
		cgnatProbe{probeComcast4, false},
		cgnatProbe{probeATT6, false},
	)
	if !fresh.ASNDataReady() {
		t.Error("ASNDataReady() = false after loading stored ranges that cover every configured ASN")
	}
}

// TestBootCGNATDetector_LoadsStoredRangesSynchronously: the boot path reads the
// stored ranges before returning, so the detector is answering from data the
// moment settings reach it -- no download on the critical path, no cold window.
func TestBootCGNATDetector_LoadsStoredRangesSynchronously(t *testing.T) {
	prevSettings := serviceSettings.Load()
	t.Cleanup(func() { serviceSettings.Store(prevSettings) })
	installCGNATDetector(t, GetCGNATDetector()) // restore whatever boot replaces

	// Persist ranges the way a previous process would have.
	nk := newSignalingStorageNK()
	prev, _ := newFixtureDetector(t, seededCGNATSettings())
	if err := prev.RefreshASNData(context.Background(), nk); err != nil {
		t.Fatalf("seeding stored ranges: %v", err)
	}

	// Boot order in production: InitializeEvrRuntimeModule builds the detector
	// before NewEvrPipeline has loaded settings.
	serviceSettings.Store(nil)
	d := bootCGNATDetector(context.Background(), NewRuntimeGoLogger(zap.NewNop()), nk)
	if GetCGNATDetector() != d {
		t.Fatal("bootCGNATDetector did not install the detector it returned")
	}
	d.fetchASN = func(context.Context, asnFamily) ([]byte, error) {
		t.Error("boot fetched from the network")
		return nil, errors.New("no network in this test")
	}

	if d.ASNDataReady() {
		t.Error("ASNDataReady() = true before any settings were applied; an unconfigured detector cannot say which ASNs it needs")
	}
	assertCGNAT(t, d, "before settings", cgnatProbe{probeComcast4, true})

	ServiceSettingsUpdate(&ServiceSettingsData{CGNAT: seededCGNATSettings()})

	if !d.ASNDataReady() {
		t.Error("ASNDataReady() = false immediately after settings arrived, with stored ranges covering them")
	}
	assertCGNAT(t, d, "settings applied, stored ranges loaded at boot",
		cgnatProbe{probeStarlink4, true},
		cgnatProbe{probeStarlink6, true},
		cgnatProbe{probeComcast4, false},
	)
}

// TestASNListChange_TriggersRebuild: adding an ASN in settings rebuilds the
// filtered set in the background and persists it. Until that completes the
// detector does not know about the new ASN, so it is not ready.
func TestASNListChange_TriggersRebuild(t *testing.T) {
	prevSettings := serviceSettings.Load()
	t.Cleanup(func() { serviceSettings.Store(prevSettings) })

	nk := newSignalingStorageNK()
	f := newFixtureASNFetcher(t)
	d := NewCGNATDetector(nil)
	d.fetchASN = f.fetch
	installCGNATDetector(t, d)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go d.RunASNRefresher(ctx, nk)

	ServiceSettingsUpdate(&ServiceSettingsData{CGNAT: seededCGNATSettings()})
	waitForStorageWrite(t, nk, "first settings application should trigger the boot refresh")
	if !d.ASNDataReady() {
		t.Fatal("ASNDataReady() = false after the first refresh completed")
	}

	withVodafone := seededCGNATSettings()
	withVodafone.ASNs = append(withVodafone.ASNs, 3209)
	ServiceSettingsUpdate(&ServiceSettingsData{CGNAT: withVodafone})
	waitForStorageWrite(t, nk, "adding AS3209 should trigger a rebuild")

	if !d.ASNDataReady() {
		t.Error("ASNDataReady() = false after the rebuild for the new list completed")
	}
	assertCGNAT(t, d, "after rebuild with AS3209 added",
		cgnatProbe{probeVodafone4, true},
		cgnatProbe{probeVodafone6, true},
		cgnatProbe{probeStarlink4, true},
		cgnatProbe{probeComcast4, false},
	)
	stored := readStoredASNRanges(t, nk)
	if len(stored.V4.ASNs) != 3 || len(stored.V6.ASNs) != 3 {
		t.Errorf("stored ASN lists v4=%v v6=%v after the rebuild, want all three configured ASNs", stored.V4.ASNs, stored.V6.ASNs)
	}
}

// TestUpdateSettings_UnchangedASNListRequestsNoRefresh: settings are re-applied
// every 30 s by the ServiceSettingsLoad poll and every 15 s by the Discord
// status ticker. Only a change to the ASN list may cost a download.
func TestUpdateSettings_UnchangedASNListRequestsNoRefresh(t *testing.T) {
	d := NewCGNATDetector(nil)

	d.UpdateSettings(seededCGNATSettings())
	if got := len(d.refreshRequests); got != 1 {
		t.Fatalf("first settings application queued %d refresh requests, want 1", got)
	}
	<-d.refreshRequests

	reordered := seededCGNATSettings()
	reordered.ASNs = []int{21928, 14593}
	reordered.CIDRs = append(reordered.CIDRs, "203.0.113.0/24")
	d.UpdateSettings(reordered)
	if got := len(d.refreshRequests); got != 0 {
		t.Errorf("re-applying the same ASN set queued %d refresh requests, want 0", got)
	}
}

// TestASNListChange_FailedRefreshKeepsRetainedASNs pins what the detector
// believes when the list changes and the rebuild then fails.
//
// The stored ranges were filtered for the OLD list. They are still true for
// every ASN the old and new lists share -- a range's owner does not depend on
// what else is configured -- so those keep answering CGNAT. An ASN the new list
// adds has no data, so no address can be ruled out: not ready, fail closed.
// Shrinking the list back needs no refresh, because the data covers it.
func TestASNListChange_FailedRefreshKeepsRetainedASNs(t *testing.T) {
	d, f := newFixtureDetector(t, seededCGNATSettings())
	if err := d.RefreshASNData(context.Background(), nil); err != nil {
		t.Fatalf("initial refresh: %v", err)
	}

	withVodafone := seededCGNATSettings()
	withVodafone.ASNs = append(withVodafone.ASNs, 3209)
	d.UpdateSettings(withVodafone)
	f.setFailure(asnFamilyV4, errors.New("iptoasn.com unreachable"))
	f.setFailure(asnFamilyV6, errors.New("iptoasn.com unreachable"))
	if err := d.RefreshASNData(context.Background(), nil); err == nil {
		t.Fatal("RefreshASNData returned nil with both datasets unavailable")
	}

	if d.ASNDataReady() {
		t.Error("ASNDataReady() = true with AS3209 configured and no data ever loaded for it")
	}
	assertCGNAT(t, d, "AS3209 added, rebuild failed",
		cgnatProbe{probeStarlink4, true}, // retained ASN: still known CGNAT
		cgnatProbe{probeVodafone4, true}, // added ASN: cannot be ruled out
		cgnatProbe{probeComcast4, true},  // cannot be ruled out either
	)

	onlyStarlink := seededCGNATSettings()
	onlyStarlink.ASNs = []int{14593}
	d.UpdateSettings(onlyStarlink)
	if !d.ASNDataReady() {
		t.Error("ASNDataReady() = false for a list the loaded ranges fully cover")
	}
	assertCGNAT(t, d, "list shrunk to AS14593",
		cgnatProbe{probeStarlink4, true},
		cgnatProbe{probeTMobile4, false}, // no longer configured
		cgnatProbe{probeComcast4, false},
	)
}

// TestRunASNRefresher_RetriesAfterFailure: a failed refresh leaves the detector
// not-ready, which fails closed -- so it must not stay that way until the next
// restart just because iptoasn.com was down once.
func TestRunASNRefresher_RetriesAfterFailure(t *testing.T) {
	nk := newSignalingStorageNK()
	d, f := newFixtureDetector(t, seededCGNATSettings())
	d.refreshRetryInterval = time.Millisecond

	// The first attempt (one fetch per family) fails; the source then recovers.
	f.transientFailures = 2

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go d.RunASNRefresher(ctx, nk)

	waitForStorageWrite(t, nk, "the refresher should retry a failed refresh")
	if calls := f.callCount(); calls < 4 {
		t.Errorf("fetcher called %d times, want at least 4 (a failed attempt and a successful retry)", calls)
	}
	if !d.ASNDataReady() {
		t.Error("ASNDataReady() = false after a successful retry")
	}
}
