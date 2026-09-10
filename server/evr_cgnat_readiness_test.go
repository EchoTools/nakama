package server

import (
	"context"
	"encoding/json"
	"maps"
	"sync"
	"testing"
	"time"

	"github.com/heroiclabs/nakama-common/api"
	"github.com/heroiclabs/nakama-common/runtime"
	"go.uber.org/zap"
)

// seededCGNATSettings is what FixDefaultServiceSettings seeds: Starlink and
// T-Mobile by ASN, RFC 6598 by CIDR. (productionCGNATSettings in
// evr_authenticate_alts_weaksignal_test.go is the same plus the stray "" prefix
// that #589 was about.)
func seededCGNATSettings() CGNATSettings {
	return CGNATSettings{
		ASNs:                     []int{14593, 21928}, // Starlink, T-Mobile
		CIDRs:                    []string{"100.64.0.0/10"},
		CommodityProfilePrefixes: []string{"Meta Quest 2::", "Meta Quest 3::", "Meta Quest 3S::"},
	}
}

// installCGNATDetector makes d the process-global detector for the duration of
// the test. The global is read by matchIgnoredAltPattern on every call, so a
// test that sets it must not run in parallel with one that reads it.
func installCGNATDetector(t *testing.T, d *CGNATDetector) {
	t.Helper()
	prev := GetCGNATDetector()
	SetCGNATDetector(d)
	t.Cleanup(func() { SetCGNATDetector(prev) })
}

// markASNDataLoaded records d's configured ASNs as loaded, for both families,
// with ranges4/ranges6 as everything they own (nil: nothing in that family).
//
// For tests whose subject is not the ASN layer but which still assert that a
// public address is a strong signal. Before #596 that held with no ASN data at
// all, because a missing table answered "not CGNAT"; it now holds only once the
// detector can actually rule the address out.
func markASNDataLoaded(t *testing.T, d *CGNATDetector, ranges4 []asnRange4, ranges6 []asnRange6) {
	t.Helper()
	d.mu.Lock()
	defer d.mu.Unlock()
	d.asnRanges4, d.asnRanges6 = ranges4, ranges6
	d.asnCovered4 = maps.Clone(d.cgnatASNs)
	d.asnCovered6 = maps.Clone(d.cgnatASNs)
	d.notifyStateChangedLocked()
}

// TestIsCGNAT_NoASNDataFailsClosed reproduces #596.
//
// The detector is configured exactly as production is, and no IP->ASN table has
// been loaded -- the state of every freshly recreated container between process
// start and the iptoasn.com download completing. Asked about a Starlink or
// T-Mobile exit address, the detector has no data to answer with. Missing data
// is not evidence of a negative, so the answer must fail closed: the address is
// treated as shared, and nothing downstream may use it to link two accounts.
func TestIsCGNAT_NoASNDataFailsClosed(t *testing.T) {
	d := withDetector(t, seededCGNATSettings())

	carrierIPs := []struct {
		ip   string
		desc string
	}{
		{"129.222.210.50", "Starlink (AS14593) IPv4"},
		{"172.56.91.132", "T-Mobile (AS21928) IPv4, the production case in cgnat_test_cases.json"},
		{"2406:2d40:100::1", "Starlink (AS14593) IPv6"},
	}

	for _, c := range carrierIPs {
		t.Run(c.desc, func(t *testing.T) {
			if !d.IsCGNAT(c.ip) {
				t.Errorf("IsCGNAT(%q) = false with ASNs %v configured and no ASN data loaded; the detector cannot rule this address out, so it must not report it as not-CGNAT", c.ip, seededCGNATSettings().ASNs)
			}
			if !d.IsWeakSignal(c.ip) {
				t.Errorf("IsWeakSignal(%q) = false with no ASN data loaded; an address the detector cannot classify must not be a strong alt-link signal", c.ip)
			}
			if !matchIgnoredAltPattern(c.ip) {
				t.Errorf("matchIgnoredAltPattern(%q) = false with no ASN data loaded; the address would enter LoginHistory.Cache and AltSearchPatterns as a discovery key", c.ip)
			}

			history := &LoginHistory{
				AlternateMatches: map[string][]*AlternateSearchMatch{
					"stranger": {{OtherUserID: "stranger", Items: []string{c.ip}}},
				},
			}
			if got := filterStrongAlts(history, []string{"stranger"}, d); len(got) != 0 {
				t.Errorf("filterStrongAlts kept %v, linked only by %s, with no ASN data loaded; want none", got, c.ip)
			}
		})
	}
}

// TestServiceSettingsLoad_ReachesCGNATDetector pins where operator settings
// enter the detector.
//
// ServiceSettingsLoad is the path that reads Global/settings: once at boot
// (NewEvrPipeline) and every 30 s after. The detector is constructed earlier,
// in InitializeEvrRuntimeModule, from ServiceSettings() -- which never returns
// nil, so before the first load it hands back a zero struct with no CIDRs, no
// ASNs and no commodity prefixes. If the load path stores the settings without
// passing them on, the detector runs unconfigured until something else happens
// to call ServiceSettingsUpdate (the Discord READY handler), and an operator's
// edit to the stored record never reaches it at all.
func TestServiceSettingsLoad_ReachesCGNATDetector(t *testing.T) {
	prevSettings := serviceSettings.Load()
	t.Cleanup(func() { serviceSettings.Store(prevSettings) })
	serviceSettings.Store(nil)

	d := NewCGNATDetector(nil)
	installCGNATDetector(t, d)

	stored := ServiceSettingsData{CGNAT: CGNATSettings{
		ASNs:                     []int{14593, 21928},
		CIDRs:                    []string{"100.64.0.0/10", "203.0.113.0/24"},
		CommodityProfilePrefixes: []string{"Meta Quest 3::"},
	}}
	raw, err := json.Marshal(stored)
	if err != nil {
		t.Fatalf("marshal settings: %v", err)
	}
	nk := newOCCTestNakamaModule()
	nk.seedObject(SystemUserID, ServiceSettingsStorageCollection, ServiceSettingStorageKey, string(raw))

	if _, err := ServiceSettingsLoad(context.Background(), NewRuntimeGoLogger(zap.NewNop()), nk); err != nil {
		t.Fatalf("ServiceSettingsLoad: %v", err)
	}

	d.mu.RLock()
	gotCIDRs := len(d.cidrNets)
	d.mu.RUnlock()
	if gotCIDRs != 2 {
		t.Errorf("detector holds %d CIDRs after ServiceSettingsLoad, want the 2 in the stored record", gotCIDRs)
	}
	if profile := "Meta Quest 3::WIFI::::Unknown::3::6::0::0"; !d.IsWeakSignal(profile) {
		t.Errorf("IsWeakSignal(%q) = false after ServiceSettingsLoad; the stored commodity prefix never reached the detector", profile)
	}
}

// TestASNDataReady_FalseUntilSettingsAndDataArrive walks the readiness
// accessor through the states a booting process passes through.
func TestASNDataReady_FalseUntilSettingsAndDataArrive(t *testing.T) {
	d := NewCGNATDetector(nil)
	d.fetchASN = newFixtureASNFetcher(t).fetch

	if d.ASNDataReady() {
		t.Error("ASNDataReady() = true for a detector no settings have reached")
	}

	d.UpdateSettings(seededCGNATSettings())
	if d.ASNDataReady() {
		t.Error("ASNDataReady() = true with ASNs configured and no ranges loaded")
	}

	done := make(chan error, 1)
	waitCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	go func() { done <- d.WaitASNDataReady(waitCtx) }()

	if err := d.RefreshASNData(context.Background(), nil); err != nil {
		t.Fatalf("RefreshASNData: %v", err)
	}
	if !d.ASNDataReady() {
		t.Error("ASNDataReady() = false after both datasets loaded")
	}
	if err := <-done; err != nil {
		t.Errorf("WaitASNDataReady returned %v; it should return nil once the data is loaded", err)
	}

	// Nothing configured by ASN: there is nothing to wait for.
	cidrOnly := NewCGNATDetector(nil)
	cidrOnly.UpdateSettings(CGNATSettings{CIDRs: []string{"100.64.0.0/10"}})
	if !cidrOnly.ASNDataReady() {
		t.Error("ASNDataReady() = false with no ASNs configured; there is no ASN data to wait for")
	}
}

// TestASNDataReady_ConcurrentWithRefresh backs the "safe for concurrent use"
// claim #597 will rely on: readers poll ASNDataReady and IsCGNAT while a writer
// re-applies settings and refreshes. Run under -race. Per AGENTS.md defect
// class 2 the goroutines are released together from a barrier so they actually
// overlap, and this test was run against an ASNDataReady with its lock removed
// to confirm the race detector reports it.
func TestASNDataReady_ConcurrentWithRefresh(t *testing.T) {
	d := NewCGNATDetector(nil)
	d.fetchASN = newFixtureASNFetcher(t).fetch
	d.UpdateSettings(seededCGNATSettings())

	withVodafone := seededCGNATSettings()
	withVodafone.ASNs = append(withVodafone.ASNs, 3209)

	const readers, iterations = 4, 200
	start := make(chan struct{})
	var wg sync.WaitGroup
	for range readers {
		wg.Go(func() {
			<-start
			for range iterations {
				_ = d.ASNDataReady()
				_ = d.IsCGNAT("129.222.210.50")
			}
		})
	}
	wg.Go(func() {
		<-start
		for i := range iterations / 10 {
			if i%2 == 0 {
				d.UpdateSettings(withVodafone)
			} else {
				d.UpdateSettings(seededCGNATSettings())
			}
			_ = d.RefreshASNData(context.Background(), nil)
		}
	})
	close(start)
	wg.Wait()
}

// TestWaitASNDataReady_HonoursContext: a caller that must not proceed without
// the data gets an error when it gives up, never a silent "ready".
func TestWaitASNDataReady_HonoursContext(t *testing.T) {
	d := NewCGNATDetector(nil)
	d.UpdateSettings(seededCGNATSettings())

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := d.WaitASNDataReady(ctx); err == nil {
		t.Fatal("WaitASNDataReady returned nil for a detector with no ASN data and a cancelled context")
	}
}

// storageListRecordingNK fails loudly if anything starts walking LoginHistory.
type storageListRecordingNK struct {
	runtime.NakamaModule
	lists int
}

func (m *storageListRecordingNK) StorageList(ctx context.Context, callerID, userID, collection string, limit int, cursor string) ([]*api.StorageObject, string, error) {
	m.lists++
	return nil, "", nil
}

// TestRunCGNATCleanup_RefusesWhenASNDataNotReady: the cleanup BREAKS every link
// whose items are all weak. With no ASN data every public address fails closed
// to weak, so running then would break every IP-only link in the database. The
// primitive's closed direction is the wrong one for a caller that acts on a
// POSITIVE weak verdict; this caller must refuse instead.
func TestRunCGNATCleanup_RefusesWhenASNDataNotReady(t *testing.T) {
	d := NewCGNATDetector(nil)
	d.UpdateSettings(seededCGNATSettings())
	nk := &storageListRecordingNK{}

	broken, affected, _, err := runCGNATCleanup(context.Background(), NewRuntimeGoLogger(zap.NewNop()), nk, d)
	if err == nil {
		t.Error("runCGNATCleanup returned nil with no ASN data loaded; it must refuse")
	}
	if nk.lists != 0 || broken != 0 || affected != 0 {
		t.Errorf("runCGNATCleanup walked storage (%d list calls) and broke %d links across %d users before refusing", nk.lists, broken, affected)
	}
}

// TestMigrationBreakIgnoredAlts_RefusesWhenASNDataNotReady: same shape as the
// cleanup, reached through matchIgnoredAltPattern instead of IsWeakSignal.
func TestMigrationBreakIgnoredAlts_RefusesWhenASNDataNotReady(t *testing.T) {
	d := withDetector(t, seededCGNATSettings())
	if d.ASNDataReady() {
		t.Fatal("precondition: detector should have no ASN data")
	}
	nk := &storageListRecordingNK{}

	err := (&MigrationBreakIgnoredAlts{}).MigrateSystem(context.Background(), NewRuntimeGoLogger(zap.NewNop()), nil, nk)
	if err == nil {
		t.Error("MigrationBreakIgnoredAlts returned nil with no ASN data loaded; it must refuse")
	}
	if nk.lists != 0 {
		t.Errorf("MigrationBreakIgnoredAlts walked storage (%d list calls) before refusing", nk.lists)
	}
}
