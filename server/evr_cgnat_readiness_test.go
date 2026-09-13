package server

import (
	"context"
	"encoding/json"
	"testing"

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
	if profile := "Meta Quest 3::WIFI::::Unknown::3::6::0::0"; !d.IsWeakSignal(profile, 0) {
		t.Errorf("IsWeakSignal(%q) = false after ServiceSettingsLoad; the stored commodity prefix never reached the detector", profile)
	}
	if !d.IsCGNAT(starlinkIP, starlinkASN) {
		t.Errorf("IsCGNAT(%q, AS%d) = false after ServiceSettingsLoad; the stored ASN list never reached the detector", starlinkIP, starlinkASN)
	}
	if err := d.WaitSettingsApplied(canceledContext()); err != nil {
		t.Errorf("WaitSettingsApplied after ServiceSettingsLoad = %v; the startup cleanup would never run", err)
	}
}

// TestWaitSettingsApplied_BlocksUntilSettingsArrive: the startup cleanup reads
// CleanupOnStartup and classifies with the configured lists, so it must not
// start before settings reach the detector -- and must not hang when they
// never do.
func TestWaitSettingsApplied_BlocksUntilSettingsArrive(t *testing.T) {
	d := NewCGNATDetector(nil)

	if err := d.WaitSettingsApplied(canceledContext()); err == nil {
		t.Fatal("WaitSettingsApplied returned nil before any settings were applied")
	}

	d.UpdateSettings(seededCGNATSettings())
	d.UpdateSettings(seededCGNATSettings()) // a second application must not re-close the channel

	if err := d.WaitSettingsApplied(canceledContext()); err != nil {
		t.Errorf("WaitSettingsApplied = %v after settings were applied", err)
	}
}

// TestBootCGNATDetector_AppliesOnlyLoadedSettings: at boot the detector takes
// settings only if Global/settings has actually been loaded. Before the first
// load ServiceSettings() is a zero struct, and applying it would mark the
// detector configured -- and release the startup cleanup -- with nothing in it.
func TestBootCGNATDetector_AppliesOnlyLoadedSettings(t *testing.T) {
	prevSettings := serviceSettings.Load()
	t.Cleanup(func() { serviceSettings.Store(prevSettings) })
	installCGNATDetector(t, nil)

	serviceSettings.Store(nil)
	if err := bootCGNATDetector(nil).WaitSettingsApplied(canceledContext()); err == nil {
		t.Error("bootCGNATDetector applied settings before any were loaded")
	}

	serviceSettings.Store(&ServiceSettingsData{CGNAT: seededCGNATSettings()})
	d := bootCGNATDetector(nil)
	if GetCGNATDetector() != d {
		t.Error("bootCGNATDetector did not install the detector it built")
	}
	if !d.IsCGNAT(starlinkIP, starlinkASN) {
		t.Errorf("IsCGNAT(%q, AS%d) = false; the loaded settings were not applied at boot", starlinkIP, starlinkASN)
	}
}

// canceledContext is already done, so a wait on it returns immediately unless
// its condition already holds.
func canceledContext() context.Context {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	return ctx
}
