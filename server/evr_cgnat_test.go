package server

import (
	"bytes"
	"net"
	"testing"
	"time"
)

// --- Test Helpers ---

func testDetector(t *testing.T) *CGNATDetector {
	t.Helper()
	return &CGNATDetector{
		ipCounts:   make(map[string]map[string]time.Time),
		maxIPCount: defaultMaxIPMap,
		cgnatASNs:  make(map[int]bool),
	}
}

func testDetectorWithASN(t *testing.T, ranges4 []asnRange4, ranges6 []asnRange6, asns []int) *CGNATDetector {
	t.Helper()
	d := testDetector(t)
	d.asnRanges4 = ranges4
	d.asnRanges6 = ranges6
	for _, asn := range asns {
		d.cgnatASNs[asn] = true
	}
	return d
}

// Synthetic Starlink-like IPv4 range: 129.222.0.0 - 129.222.255.255 = AS14593
var testRanges4 = []asnRange4{
	{Start: ipv4ToUint32(net.ParseIP("10.0.0.0").To4()), End: ipv4ToUint32(net.ParseIP("10.255.255.255").To4()), ASN: 99999},
	{Start: ipv4ToUint32(net.ParseIP("100.64.0.0").To4()), End: ipv4ToUint32(net.ParseIP("100.127.255.255").To4()), ASN: 55555},
	{Start: ipv4ToUint32(net.ParseIP("129.222.0.0").To4()), End: ipv4ToUint32(net.ParseIP("129.222.255.255").To4()), ASN: 14593},
	{Start: ipv4ToUint32(net.ParseIP("172.56.0.0").To4()), End: ipv4ToUint32(net.ParseIP("172.56.255.255").To4()), ASN: 21928},
	{Start: ipv4ToUint32(net.ParseIP("192.168.0.0").To4()), End: ipv4ToUint32(net.ParseIP("192.168.255.255").To4()), ASN: 88888},
}

// Synthetic Starlink IPv6 range: 2406:2d40:: - 2406:2d40:ffff:... = AS14593
var testRanges6 = []asnRange6{
	{
		Start: ipv6ToBytes("2406:2d40::"),
		End:   ipv6ToBytes("2406:2d40:ffff:ffff:ffff:ffff:ffff:ffff"),
		ASN:   14593,
	},
	{
		Start: ipv6ToBytes("2600:1000::"),
		End:   ipv6ToBytes("2600:1000:ffff:ffff:ffff:ffff:ffff:ffff"),
		ASN:   7018,
	},
}

func ipv6ToBytes(s string) [16]byte {
	ip := net.ParseIP(s)
	var b [16]byte
	copy(b[:], ip.To16())
	return b
}

// --- ASN Lookup Tests ---

func TestASNLookup_KnownCGNATASN_IPv4(t *testing.T) {
	d := testDetectorWithASN(t, testRanges4, nil, []int{14593})
	if !d.IsCGNAT("129.222.210.50") {
		t.Error("expected Starlink IPv4 to be CGNAT")
	}
}

func TestASNLookup_KnownCGNATASN_IPv6(t *testing.T) {
	d := testDetectorWithASN(t, nil, testRanges6, []int{14593})
	if !d.IsCGNAT("2406:2d40:100::1") {
		t.Error("expected Starlink IPv6 to be CGNAT")
	}
}

func TestASNLookup_NonCGNATASN(t *testing.T) {
	d := testDetectorWithASN(t, testRanges4, testRanges6, []int{14593})
	// 10.0.0.1 resolves to ASN 99999, not in CGNAT list
	if d.IsCGNAT("10.0.0.1") {
		t.Error("non-CGNAT ASN should not be flagged")
	}
	// IPv6 in AT&T range (AS7018), not in CGNAT list
	if d.IsCGNAT("2600:1000::1") {
		t.Error("non-CGNAT IPv6 ASN should not be flagged")
	}
}

func TestASNLookup_EmptyDatabase(t *testing.T) {
	d := testDetector(t)
	if d.IsCGNAT("129.222.210.50") {
		t.Error("empty ASN database should not flag anything")
	}
}

func TestASNLookup_BinarySearchEdgeCases_IPv4(t *testing.T) {
	d := testDetectorWithASN(t, testRanges4, nil, []int{14593})

	cases := []struct {
		ip   string
		want bool
		desc string
	}{
		{"129.222.0.0", true, "exact start of Starlink range"},
		{"129.222.255.255", true, "exact end of Starlink range"},
		{"129.221.255.255", false, "one before Starlink range"},
		{"129.223.0.0", false, "one after Starlink range"},
		{"1.0.0.0", false, "before all ranges"},
		{"250.0.0.0", false, "after all ranges"},
	}
	for _, tc := range cases {
		got := d.IsCGNAT(tc.ip)
		if got != tc.want {
			t.Errorf("%s (%s): got %v, want %v", tc.desc, tc.ip, got, tc.want)
		}
	}
}

func TestASNLookup_BinarySearchEdgeCases_IPv6(t *testing.T) {
	d := testDetectorWithASN(t, nil, testRanges6, []int{14593})

	cases := []struct {
		ip   string
		want bool
		desc string
	}{
		{"2406:2d40::", true, "exact start of Starlink IPv6 range"},
		{"2406:2d40:ffff:ffff:ffff:ffff:ffff:ffff", true, "exact end"},
		{"2406:2d3f:ffff:ffff:ffff:ffff:ffff:ffff", false, "one before"},
		{"2406:2d41::", false, "one after"},
	}
	for _, tc := range cases {
		got := d.IsCGNAT(tc.ip)
		if got != tc.want {
			t.Errorf("%s (%s): got %v, want %v", tc.desc, tc.ip, got, tc.want)
		}
	}
}

func TestASNLookup_UnparseableInput(t *testing.T) {
	d := testDetectorWithASN(t, testRanges4, nil, []int{14593})

	for _, input := range []string{"", "garbage", "not.an.ip", "999.999.999.999"} {
		if d.IsCGNAT(input) {
			t.Errorf("unparseable input %q should return false", input)
		}
	}
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
	d := testDetectorWithASN(t, testRanges4, nil, []int{14593})
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

func TestIsCGNAT_CIDRWithoutASN(t *testing.T) {
	d := testDetector(t)
	_, cidr, _ := net.ParseCIDR("100.64.0.0/10")
	d.cidrNets = []*net.IPNet{cidr}
	// No ASN data loaded

	if !d.IsCGNAT("100.64.0.1") {
		t.Error("CIDR match alone should be sufficient")
	}
}

func TestIsCGNAT_ASNWithoutCIDR(t *testing.T) {
	d := testDetectorWithASN(t, testRanges4, nil, []int{14593})
	// No CIDRs configured

	if !d.IsCGNAT("129.222.210.50") {
		t.Error("ASN match alone should be sufficient")
	}
}

func TestIsWeakSignal_CGNATIP_IPv4(t *testing.T) {
	d := testDetectorWithASN(t, testRanges4, nil, []int{14593})
	if !d.IsWeakSignal("129.222.210.50") {
		t.Error("CGNAT IPv4 should be weak")
	}
}

func TestIsWeakSignal_CGNATIP_IPv6(t *testing.T) {
	d := testDetectorWithASN(t, nil, testRanges6, []int{14593})
	if !d.IsWeakSignal("2406:2d40:100::1") {
		t.Error("CGNAT IPv6 should be weak")
	}
}

func TestIsWeakSignal_NormalIP(t *testing.T) {
	d := testDetectorWithASN(t, testRanges4, nil, []int{14593})
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
		ASNs:  []int{14593},
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
	d := testDetectorWithASN(t, testRanges4, nil, []int{14593})

	// Initially no CIDRs
	if d.IsCGNAT("100.64.0.1") {
		t.Error("should not be CGNAT before adding CIDR")
	}

	// Hot-reload with new CIDR
	d.UpdateSettings(CGNATSettings{
		CIDRs: []string{"100.64.0.0/10"},
		ASNs:  []int{14593},
	})

	if !d.IsCGNAT("100.64.0.1") {
		t.Error("should be CGNAT after hot-reload")
	}
}

// --- Enforcement Guard Tests ---

func TestFilterStrongAlts_AllWeakSignals(t *testing.T) {
	d := testDetectorWithASN(t, testRanges4, nil, []int{14593})

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
	d := testDetectorWithASN(t, testRanges4, nil, []int{14593})

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
	d := testDetectorWithASN(t, testRanges4, nil, []int{14593})

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
	d := testDetectorWithASN(t, testRanges4, nil, []int{14593, 21928})
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

// --- IPv4/IPv6 conversion tests ---

func TestIPv4ToUint32(t *testing.T) {
	ip := net.ParseIP("192.168.1.1").To4()
	got := ipv4ToUint32(ip)
	want := uint32(0xC0A80101)
	if got != want {
		t.Errorf("ipv4ToUint32: got %08x, want %08x", got, want)
	}
}

func TestIPv6ByteComparison(t *testing.T) {
	a := ipv6ToBytes("2406:2d40::")
	b := ipv6ToBytes("2406:2d40:ffff::")
	c := ipv6ToBytes("2406:2d41::")

	if bytes.Compare(a[:], b[:]) >= 0 {
		t.Error("a should be less than b")
	}
	if bytes.Compare(b[:], c[:]) >= 0 {
		t.Error("b should be less than c")
	}
}
