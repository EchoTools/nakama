package server

import (
	"encoding/json"
	"net"
	"strings"
	"testing"
	"time"
)

// --- Test Helpers ---

func testDetector(t *testing.T) *CGNATDetector {
	t.Helper()
	return &CGNATDetector{
		ipCounts:   make(map[string]map[string]time.Time),
		maxIPCount: defaultMaxIPMap,
	}
}

// testCGNATCIDRs stands in for configured carrier CGNAT ranges: a Starlink-like
// IPv4 /16, a T-Mobile-like IPv4 /16, and a Starlink-like IPv6 /32.
var testCGNATCIDRs = []string{"129.222.0.0/16", "172.56.0.0/16", "2406:2d40::/32"}

func testDetectorWithCIDRs(t *testing.T, cidrs ...string) *CGNATDetector {
	t.Helper()
	d := testDetector(t)
	for _, c := range cidrs {
		_, n, err := net.ParseCIDR(c)
		if err != nil {
			t.Fatalf("test CIDR %q: %v", c, err)
		}
		d.cidrNets = append(d.cidrNets, n)
	}
	return d
}

// --- CIDR Tests ---

func TestCIDR_RFC6598(t *testing.T) {
	d := testDetector(t)
	_, cidr, _ := net.ParseCIDR("100.64.0.0/10")
	d.cidrNets = []*net.IPNet{cidr}

	if !d.IsCGNAT("100.64.0.1") {
		t.Error("RFC 6598 address should be CGNAT")
	}
	if !d.IsCGNAT("100.127.255.254") {
		t.Error("end of RFC 6598 range should be CGNAT")
	}
}

func TestCIDR_CustomRange_IPv4(t *testing.T) {
	d := testDetector(t)
	_, cidr, _ := net.ParseCIDR("203.0.113.0/24")
	d.cidrNets = []*net.IPNet{cidr}

	if !d.IsCGNAT("203.0.113.50") {
		t.Error("IP in custom CIDR should be CGNAT")
	}
}

func TestCIDR_CustomRange_IPv6(t *testing.T) {
	d := testDetector(t)
	_, cidr, _ := net.ParseCIDR("2406:2d40::/32")
	d.cidrNets = []*net.IPNet{cidr}

	if !d.IsCGNAT("2406:2d40:100::1") {
		t.Error("IPv6 in custom CIDR should be CGNAT")
	}
}

func TestCIDR_NotInRange(t *testing.T) {
	d := testDetector(t)
	_, cidr, _ := net.ParseCIDR("100.64.0.0/10")
	d.cidrNets = []*net.IPNet{cidr}

	if d.IsCGNAT("73.162.100.1") {
		t.Error("IP outside CIDR should not be CGNAT")
	}
}

func TestCIDR_EmptyList(t *testing.T) {
	d := testDetector(t)
	if d.IsCGNAT("100.64.0.1") {
		t.Error("empty CIDR list should not flag anything")
	}
}

// --- Heuristic Tests ---

func TestHeuristic_BelowThreshold(t *testing.T) {
	d := testDetector(t)
	d.heuristicEnabled = true
	d.heuristicThreshold = 5
	d.heuristicWindowDays = 30

	for i := 0; i < 3; i++ {
		d.TrackLogin("10.0.0.1", "user-"+string(rune('a'+i)), "", nil)
	}
	// Should not panic or error, just track silently
	if len(d.ipCounts["10.0.0.1"]) != 3 {
		t.Errorf("expected 3 tracked users, got %d", len(d.ipCounts["10.0.0.1"]))
	}
}

func TestHeuristic_ExceedsThreshold(t *testing.T) {
	d := testDetector(t)
	d.heuristicEnabled = true
	d.heuristicThreshold = 5
	d.heuristicWindowDays = 30

	// Track 6 users - should exceed threshold of 5
	for i := 0; i < 6; i++ {
		d.TrackLogin("10.0.0.1", "user-"+string(rune('a'+i)), "", nil)
	}
	if len(d.ipCounts["10.0.0.1"]) != 6 {
		t.Errorf("expected 6 tracked users, got %d", len(d.ipCounts["10.0.0.1"]))
	}
}

func TestHeuristic_WindowExpiry(t *testing.T) {
	d := testDetector(t)
	d.heuristicEnabled = true
	d.heuristicThreshold = 5
	d.heuristicWindowDays = 7

	// Add old entries manually
	d.ipCounts["10.0.0.1"] = map[string]time.Time{
		"old-user-1": time.Now().AddDate(0, 0, -10),
		"old-user-2": time.Now().AddDate(0, 0, -10),
		"old-user-3": time.Now().AddDate(0, 0, -10),
	}

	// Track a new login - should prune old entries
	d.TrackLogin("10.0.0.1", "new-user", "", nil)
	if len(d.ipCounts["10.0.0.1"]) != 1 {
		t.Errorf("expected 1 user after pruning, got %d", len(d.ipCounts["10.0.0.1"]))
	}
}

func TestHeuristic_Disabled(t *testing.T) {
	d := testDetector(t)
	d.heuristicEnabled = false

	d.TrackLogin("10.0.0.1", "user-a", "", nil)
	if len(d.ipCounts) != 0 {
		t.Error("disabled heuristic should not track")
	}
}

func TestHeuristic_DoesNotAutoExempt(t *testing.T) {
	d := testDetectorWithCIDRs(t, testCGNATCIDRs...)
	d.heuristicEnabled = true
	d.heuristicThreshold = 2
	d.heuristicWindowDays = 30

	// Track enough to exceed threshold on a non-CGNAT IP
	d.TrackLogin("73.162.100.1", "user-a", "", nil)
	d.TrackLogin("73.162.100.1", "user-b", "", nil)
	d.TrackLogin("73.162.100.1", "user-c", "", nil)

	// Heuristic fires but IsCGNAT should still return false
	if d.IsCGNAT("73.162.100.1") {
		t.Error("heuristic should not auto-exempt IPs from alt detection")
	}
}

func TestHeuristic_MemoryCap(t *testing.T) {
	d := testDetector(t)
	d.heuristicEnabled = true
	d.heuristicThreshold = 1000 // high threshold, won't fire
	d.heuristicWindowDays = 30
	d.maxIPCount = 10

	for i := 0; i < 20; i++ {
		ip := net.IPv4(10, 0, 0, byte(i)).String()
		d.TrackLogin(ip, "user-x", "", nil)
	}

	if len(d.ipCounts) > 10 {
		t.Errorf("expected at most 10 IPs, got %d", len(d.ipCounts))
	}
}

func TestHeuristic_IPv6(t *testing.T) {
	d := testDetector(t)
	d.heuristicEnabled = true
	d.heuristicThreshold = 5
	d.heuristicWindowDays = 30

	d.TrackLogin("2406:2d40:100::1", "user-a", "", nil)
	d.TrackLogin("2406:2d40:100::1", "user-b", "", nil)

	if len(d.ipCounts["2406:2d40:100::1"]) != 2 {
		t.Errorf("expected 2 IPv6 tracked users, got %d", len(d.ipCounts["2406:2d40:100::1"]))
	}
}

// --- Combined / IsWeakSignal Tests ---

func TestIsWeakSignal_CGNATIP_IPv4(t *testing.T) {
	d := testDetectorWithCIDRs(t, testCGNATCIDRs...)
	if !d.IsWeakSignal("129.222.210.50") {
		t.Error("CGNAT IPv4 should be weak")
	}
}

func TestIsWeakSignal_CGNATIP_IPv6(t *testing.T) {
	d := testDetectorWithCIDRs(t, testCGNATCIDRs...)
	if !d.IsWeakSignal("2406:2d40:100::1") {
		t.Error("CGNAT IPv6 should be weak")
	}
}

func TestIsWeakSignal_NormalIP(t *testing.T) {
	d := testDetectorWithCIDRs(t, testCGNATCIDRs...)
	if d.IsWeakSignal("73.162.100.1") {
		t.Error("normal residential IP should not be weak")
	}
}

func TestIsWeakSignal_CommodityProfile(t *testing.T) {
	d := testDetector(t)
	d.commodityProfilePrefixes = []string{"Meta Quest 2::", "Meta Quest 3::", "Meta Quest 3S::"}

	if !d.IsWeakSignal("Meta Quest 3::WIFI::::Unknown::3::6::0::0") {
		t.Error("commodity Quest 3 profile should be weak")
	}
	if !d.IsWeakSignal("Meta Quest 2::WIFI::::Unknown::3::8::0::0") {
		t.Error("commodity Quest 2 profile should be weak")
	}
}

func TestIsWeakSignal_UniqueProfile(t *testing.T) {
	d := testDetector(t)
	d.commodityProfilePrefixes = []string{"Meta Quest 2::", "Meta Quest 3::"}

	if d.IsWeakSignal("Rift S::ETHERNET::NVIDIA GeForce RTX 3080::AMD Ryzen 9 5900X::12::24::32768::10240") {
		t.Error("unique PC profile should not be weak")
	}
}

func TestIsWeakSignal_HMDSerial(t *testing.T) {
	d := testDetector(t)
	d.commodityProfilePrefixes = []string{"Meta Quest 3::"}

	if d.IsWeakSignal("1WMHH9ABC1234") {
		t.Error("HMD serial should never be weak")
	}
}

func TestIsWeakSignal_XPID(t *testing.T) {
	d := testDetector(t)
	if d.IsWeakSignal("OVR-ORG-3930901337016247") {
		t.Error("XPID should never be weak")
	}
}

func TestIsWeakSignal_Unknown(t *testing.T) {
	d := testDetector(t)
	if !d.IsWeakSignal("unknown") {
		t.Error("'unknown' should be weak")
	}
	if !d.IsWeakSignal("") {
		t.Error("empty string should be weak")
	}
}

// --- Settings Tests ---

func TestUpdateSettings_ParsesCIDRs(t *testing.T) {
	d := testDetector(t)
	d.UpdateSettings(CGNATSettings{
		CIDRs: []string{"100.64.0.0/10", "2406:2d40::/32"},
	})

	if !d.IsCGNAT("100.64.0.1") {
		t.Error("IPv4 CIDR should work after UpdateSettings")
	}
	if !d.IsCGNAT("2406:2d40:100::1") {
		t.Error("IPv6 CIDR should work after UpdateSettings")
	}
}

func TestUpdateSettings_InvalidCIDRLogged(t *testing.T) {
	d := NewCGNATDetector(nil) // nil logger won't crash on Warn
	d.UpdateSettings(CGNATSettings{
		CIDRs: []string{"not-a-cidr", "100.64.0.0/10"},
	})

	// Valid CIDR should still work
	if len(d.cidrNets) != 1 {
		t.Errorf("expected 1 valid CIDR, got %d", len(d.cidrNets))
	}
	if !d.IsCGNAT("100.64.0.1") {
		t.Error("valid CIDR should still work despite invalid one")
	}
}

func TestUpdateSettings_HotReload(t *testing.T) {
	d := testDetector(t)

	// Initially no CIDRs
	if d.IsCGNAT("100.64.0.1") {
		t.Error("should not be CGNAT before adding CIDR")
	}

	// Hot-reload with new CIDR
	d.UpdateSettings(CGNATSettings{
		CIDRs: []string{"100.64.0.0/10"},
	})

	if !d.IsCGNAT("100.64.0.1") {
		t.Error("should be CGNAT after hot-reload")
	}
}

// --- Enforcement Guard Tests ---

func TestFilterStrongAlts_AllWeakSignals(t *testing.T) {
	d := testDetectorWithCIDRs(t, testCGNATCIDRs...)

	history := &LoginHistory{
		AlternateMatches: map[string][]*AlternateSearchMatch{
			"alt-user-1": {
				{OtherUserID: "alt-user-1", Items: []string{"129.222.210.50"}},
			},
		},
	}

	result := filterStrongAlts(history, []string{"alt-user-1"}, d)
	if len(result) != 0 {
		t.Errorf("expected 0 strong alts, got %d", len(result))
	}
}

func TestFilterStrongAlts_MixedSignals(t *testing.T) {
	d := testDetectorWithCIDRs(t, testCGNATCIDRs...)

	history := &LoginHistory{
		AlternateMatches: map[string][]*AlternateSearchMatch{
			"alt-user-1": {
				{OtherUserID: "alt-user-1", Items: []string{"129.222.210.50", "1WMHH9ABC1234"}},
			},
		},
	}

	result := filterStrongAlts(history, []string{"alt-user-1"}, d)
	if len(result) != 1 {
		t.Errorf("expected 1 strong alt (HMD serial), got %d", len(result))
	}
}

func TestFilterStrongAlts_AllStrongSignals(t *testing.T) {
	d := testDetectorWithCIDRs(t, testCGNATCIDRs...)

	history := &LoginHistory{
		AlternateMatches: map[string][]*AlternateSearchMatch{
			"alt-user-1": {
				{OtherUserID: "alt-user-1", Items: []string{"1WMHH9ABC1234", "OVR-ORG-123456"}},
			},
		},
	}

	result := filterStrongAlts(history, []string{"alt-user-1"}, d)
	if len(result) != 1 {
		t.Errorf("expected 1 strong alt, got %d", len(result))
	}
}

func TestFilterStrongAlts_NoDetector(t *testing.T) {
	history := &LoginHistory{
		AlternateMatches: map[string][]*AlternateSearchMatch{
			"alt-user-1": {
				{OtherUserID: "alt-user-1", Items: []string{"129.222.210.50"}},
			},
		},
	}

	result := filterStrongAlts(history, []string{"alt-user-1"}, nil)
	if len(result) != 1 {
		t.Errorf("nil detector should keep all alts, got %d", len(result))
	}
}

func TestFilterStrongAlts_CommodityProfileOnly(t *testing.T) {
	d := testDetector(t)
	d.commodityProfilePrefixes = []string{"Meta Quest 3::"}

	history := &LoginHistory{
		AlternateMatches: map[string][]*AlternateSearchMatch{
			"alt-user-1": {
				{OtherUserID: "alt-user-1", Items: []string{"Meta Quest 3::WIFI::::Unknown::3::6::0::0", "unknown"}},
			},
		},
	}

	result := filterStrongAlts(history, []string{"alt-user-1"}, d)
	if len(result) != 0 {
		t.Errorf("commodity profile only should be filtered, got %d", len(result))
	}
}

func TestFilterStrongAlts_TransitionalState(t *testing.T) {
	// Old false links exist, detector active, cleanup not yet run
	d := testDetectorWithCIDRs(t, testCGNATCIDRs...)
	d.commodityProfilePrefixes = []string{"Meta Quest 2::"}

	// Simulates the T-Mobile CGNAT production case
	history := &LoginHistory{
		AlternateMatches: map[string][]*AlternateSearchMatch{
			"innocent-user": {
				{OtherUserID: "innocent-user", Items: []string{"172.56.91.132", "Meta Quest 2::WIFI::::Unknown::3::8::0::0", "unknown"}},
			},
		},
	}

	result := filterStrongAlts(history, []string{"innocent-user"}, d)
	if len(result) != 0 {
		t.Errorf("transitional state: weak-only links should be filtered at read time, got %d", len(result))
	}
}

// --- matchIgnoredAltPattern Tests ---

func TestMatchIgnoredAltPattern_CGNATIP(t *testing.T) {
	d := testDetectorWithCIDRs(t, testCGNATCIDRs...)
	SetCGNATDetector(d)
	defer SetCGNATDetector(nil)

	if !matchIgnoredAltPattern("129.222.210.50") {
		t.Error("Starlink IP should be ignored in alt pattern")
	}
}

func TestMatchIgnoredAltPattern_CommodityProfile(t *testing.T) {
	d := testDetector(t)
	d.commodityProfilePrefixes = []string{"Meta Quest 2::", "Meta Quest 3::"}
	SetCGNATDetector(d)
	defer SetCGNATDetector(nil)

	if !matchIgnoredAltPattern("Meta Quest 3::WIFI::::Unknown::3::6::0::0") {
		t.Error("commodity Quest 3 profile should be ignored in alt pattern")
	}
	if !matchIgnoredAltPattern("Meta Quest 2::WIFI::::Unknown::3::8::0::0") {
		t.Error("commodity Quest 2 profile should be ignored in alt pattern")
	}
}

func TestMatchIgnoredAltPattern_UniqueProfile(t *testing.T) {
	d := testDetector(t)
	d.commodityProfilePrefixes = []string{"Meta Quest 2::", "Meta Quest 3::"}
	SetCGNATDetector(d)
	defer SetCGNATDetector(nil)

	if matchIgnoredAltPattern("Rift S::ETHERNET::NVIDIA GeForce RTX 3080::AMD Ryzen 9 5900X::12::24::32768::10240") {
		t.Error("unique PC profile should NOT be ignored")
	}
}

func TestMatchIgnoredAltPattern_NormalIP(t *testing.T) {
	d := testDetectorWithCIDRs(t, testCGNATCIDRs...)
	SetCGNATDetector(d)
	defer SetCGNATDetector(nil)

	if matchIgnoredAltPattern("73.162.100.1") {
		t.Error("normal residential IP should NOT be ignored")
	}
}

func TestMatchIgnoredAltPattern_PrivateIP(t *testing.T) {
	// Private IPs handled by existing IsPrivate() check, no detector needed
	if !matchIgnoredAltPattern("192.168.1.1") {
		t.Error("private IP should be ignored")
	}
	if !matchIgnoredAltPattern("10.0.0.1") {
		t.Error("private IP should be ignored")
	}
}

func TestMatchIgnoredAltPattern_HMDSerial(t *testing.T) {
	d := testDetector(t)
	d.commodityProfilePrefixes = []string{"Meta Quest 3::"}
	SetCGNATDetector(d)
	defer SetCGNATDetector(nil)

	if matchIgnoredAltPattern("1WMHH9ABC1234") {
		t.Error("HMD serial should NOT be ignored")
	}
}

func TestMatchIgnoredAltPattern_XPID(t *testing.T) {
	d := testDetector(t)
	SetCGNATDetector(d)
	defer SetCGNATDetector(nil)

	if matchIgnoredAltPattern("OVR-ORG-3930901337016247") {
		t.Error("XPID should NOT be ignored")
	}
}

func TestMatchIgnoredAltPattern_NoDetector(t *testing.T) {
	SetCGNATDetector(nil)

	// Without detector, only existing filters apply (private IPs, known values)
	if matchIgnoredAltPattern("129.222.210.50") {
		t.Error("without detector, Starlink IP should pass through (existing behavior)")
	}
	if !matchIgnoredAltPattern("192.168.1.1") {
		t.Error("private IP should still be filtered without detector")
	}
}

// --- Cleanup Logic Tests (unit-level, no storage) ---

func TestPairKey_Ordering(t *testing.T) {
	// pairKey should produce the same key regardless of argument order
	k1 := pairKey("aaa", "bbb")
	k2 := pairKey("bbb", "aaa")
	if k1 != k2 {
		t.Errorf("pairKey should be order-independent: %q != %q", k1, k2)
	}
	if k1 != "aaa:bbb" {
		t.Errorf("expected 'aaa:bbb', got %q", k1)
	}
}

// Test that the cleanup logic correctly identifies weak-signal-only links
func TestCleanupLogic_IdentifiesWeakOnlyLinks(t *testing.T) {
	d := testDetectorWithCIDRs(t, testCGNATCIDRs...)
	d.commodityProfilePrefixes = []string{"Meta Quest 2::", "Meta Quest 3::"}

	// Simulate the T-Mobile production case
	matches := []*AlternateSearchMatch{
		{OtherUserID: "innocent", Items: []string{"172.56.91.132", "Meta Quest 2::WIFI::::Unknown::3::8::0::0", "unknown"}},
	}

	allWeak := true
	for _, m := range matches {
		for _, item := range m.Items {
			if !d.IsWeakSignal(item) {
				allWeak = false
				break
			}
		}
	}
	if !allWeak {
		t.Error("T-Mobile CGNAT IP + Quest profile + unknown should all be weak signals")
	}
}

func TestCleanupLogic_PreservesStrongSignalLinks(t *testing.T) {
	d := testDetectorWithCIDRs(t, testCGNATCIDRs...)
	d.commodityProfilePrefixes = []string{"Meta Quest 3::"}

	// Real alt case: shares XPID (strong signal)
	matches := []*AlternateSearchMatch{
		{OtherUserID: "real-alt", Items: []string{"108.236.102.152", "Meta Quest 3::WIFI::::Unknown::3::6::0::0", "OVR-ORG-3930901337016247", "unknown"}},
	}

	allWeak := true
	for _, m := range matches {
		for _, item := range m.Items {
			if !d.IsWeakSignal(item) {
				allWeak = false
				break
			}
		}
	}
	if allWeak {
		t.Error("link with XPID should NOT be all-weak — XPID is a strong signal")
	}
}

func TestCleanupLogic_NonCGNATIPIsStrong(t *testing.T) {
	d := testDetectorWithCIDRs(t, testCGNATCIDRs...)

	// Non-CGNAT IP (Comcast residential)
	if d.IsWeakSignal("73.162.100.1") {
		t.Error("non-CGNAT residential IP should be a strong signal")
	}
}

// --- Production Test Case Verification ---
// These tests verify our detection matches the expected outcomes from
// server/testdata/cgnat_test_cases.json

func TestProductionCase_TMobileCGNAT(t *testing.T) {
	d := testDetectorWithCIDRs(t, testCGNATCIDRs...)
	d.commodityProfilePrefixes = []string{"Meta Quest 2::"}

	// Case: d0ac5390 <-> 8c21297d, shared: 172.56.91.132 + Quest 2 profile + unknown
	history := &LoginHistory{
		AlternateMatches: map[string][]*AlternateSearchMatch{
			"8c21297d-9c14-44c5-b6ae-efc90845d8fb": {
				{OtherUserID: "8c21297d-9c14-44c5-b6ae-efc90845d8fb", Items: []string{"172.56.91.132", "Meta Quest 2::WIFI::::Unknown::3::8::0::0", "unknown"}},
			},
		},
	}
	result := filterStrongAlts(history, []string{"8c21297d-9c14-44c5-b6ae-efc90845d8fb"}, d)
	if len(result) != 0 {
		t.Error("T-Mobile CGNAT case should be filtered (expected: link_broken)")
	}
}

func TestProductionCase_IPOnly(t *testing.T) {
	d := testDetector(t)
	// 166.205.97.60 is AT&T mobile. This is an IP-only link: the address is
	// "strong" until its range is in the CGNAT CIDRs, or the heuristic warns.
	if d.IsCGNAT("166.205.97.60") {
		t.Error("AT&T IP should not be CGNAT without its range in the CIDR list")
	}

	// But if we add it to CIDR:
	d.UpdateSettings(CGNATSettings{CIDRs: []string{"166.205.0.0/16"}})
	if !d.IsCGNAT("166.205.97.60") {
		t.Error("AT&T IP should be CGNAT after adding CIDR")
	}
}

func TestProductionCase_RealAltPreserved(t *testing.T) {
	d := testDetectorWithCIDRs(t, testCGNATCIDRs...)
	d.commodityProfilePrefixes = []string{"Meta Quest 3::"}

	// Case: 165071a9 <-> 001d2cb1, shared: IP + Quest 3 + XPID + unknown
	// The XPID (OVR-ORG-3930901337016247) is a strong signal
	history := &LoginHistory{
		AlternateMatches: map[string][]*AlternateSearchMatch{
			"001d2cb1-6ea3-4295-b89f-beb5caad52ff": {
				{OtherUserID: "001d2cb1-6ea3-4295-b89f-beb5caad52ff", Items: []string{
					"108.236.102.152",
					"Meta Quest 3::WIFI::::Unknown::3::6::0::0",
					"OVR-ORG-3930901337016247",
					"unknown",
				}},
			},
		},
	}
	result := filterStrongAlts(history, []string{"001d2cb1-6ea3-4295-b89f-beb5caad52ff"}, d)
	if len(result) != 1 {
		t.Error("real alt with XPID should be preserved (expected: link_preserved)")
	}
}

func TestProductionCase_VodafoneGermany(t *testing.T) {
	// Vodafone Germany is not CGNAT per se, but a dynamic IP pool. It is
	// covered once its range is in the CGNAT CIDRs, or by the heuristic.
	d := testDetectorWithCIDRs(t, append(testCGNATCIDRs, "95.88.0.0/14")...)
	d.commodityProfilePrefixes = []string{"Meta Quest 2::"}

	// Case: e19ea28b <-> 8ebb578c, shared: 83 Vodafone IPs + Quest 2 + unknown
	history := &LoginHistory{
		AlternateMatches: map[string][]*AlternateSearchMatch{
			"8ebb578c-b805-4dd2-995a-69a398b9e056": {
				{OtherUserID: "8ebb578c-b805-4dd2-995a-69a398b9e056", Items: []string{"95.90.254.10", "Meta Quest 2::WIFI::::Unknown::3::8::0::0", "unknown"}},
			},
		},
	}
	result := filterStrongAlts(history, []string{"8ebb578c-b805-4dd2-995a-69a398b9e056"}, d)
	if len(result) != 0 {
		t.Error("Vodafone dynamic IP case should be filtered when its range is in the CGNAT CIDRs")
	}
}

// TestCGNATSettings_StoredASNsKeyIsDropped: CGNAT classification is by CIDR
// only. A stored Global/settings record written before that still carries an
// "asns" list; it must load without error and must not be written back.
func TestCGNATSettings_StoredASNsKeyIsDropped(t *testing.T) {
	stored := `{"cgnat":{"asns":[14593,21928],"cidrs":["100.64.0.0/10"]}}`

	var data ServiceSettingsData
	if err := json.Unmarshal([]byte(stored), &data); err != nil {
		t.Fatalf("a stored record with an asns list must still load: %v", err)
	}
	if len(data.CGNAT.CIDRs) != 1 || data.CGNAT.CIDRs[0] != "100.64.0.0/10" {
		t.Errorf("CIDRs = %v, want [100.64.0.0/10]", data.CGNAT.CIDRs)
	}

	out, err := json.Marshal(data.CGNAT)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(out), `"asns"`) {
		t.Errorf("CGNAT settings written back still carry an asns list: %s", out)
	}
}
