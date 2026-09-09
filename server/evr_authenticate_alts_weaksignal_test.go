package server

import (
	"slices"
	"testing"
	"time"

	"github.com/heroiclabs/nakama/v3/server/evr"
)

// --- An empty commodity prefix must not swallow every non-IP signal ---------
//
// The defect these tests pin was not in Go source. It was one value in the
// live Global/settings record: cgnat.commodity_profile_prefixes carried an
// empty string alongside the real "Meta Quest N::" entries.
//
// strings.HasPrefix(anything, "") is always true, so CGNATDetector.IsWeakSignal
// returned true for EVERY non-IP string. matchIgnoredAltPattern
// (evr_authenticate_history.go:64) drops an item when IsWeakSignal is true AND
// net.ParseIP fails, so IPs survived and XPIDs, HMD serials and system profiles
// were all filtered -- out of LoginHistory.Cache, and therefore out of the
// `+value.cache:` discovery query that AltSearchPatterns feeds
// (evr_authenticate_alts.go:129). The strongest identifier the system has could
// not surface a candidate at all.
//
// Measured in production 2026-09-08: 7,254 of 7,254 alternate-account links
// rested on a shared IP; zero carried an XPID, HMD serial or system profile.
// Two accounts demonstrably sharing Oculus account OVR-ORG-2097 behind
// different IPs were not linkable.
//
// Tests are written against the CONTRACT ("an XPID is a strong signal and
// reaches the cache"), not against the empty-prefix cause, so they survive any
// future reshaping of where the guard lives.

// withDetector installs a detector for the duration of one test and restores
// whatever was there before. The detector is a process-wide global
// (evr_cgnat.go:39-42), and matchIgnoredAltPattern reads it on every call, so a
// test that leaves one installed changes the behaviour of every later test in
// the package. Do not run these in parallel.
func withDetector(t *testing.T, settings CGNATSettings) *CGNATDetector {
	t.Helper()
	prev := GetCGNATDetector()
	d := NewCGNATDetector(nil)
	d.UpdateSettings(settings)
	SetCGNATDetector(d)
	t.Cleanup(func() { SetCGNATDetector(prev) })
	return d
}

// productionCGNATSettings is the shape the live service actually carried: the
// seeded defaults from FixDefaultServiceSettings (evr_global_settings.go:563-572,
// reached via ServiceSettingsLoad at evr_global_settings.go:315) with the empty
// string that was found in the stored record. Note that the seed itself is clean
// -- the "" was in the operator-edited stored value, not in the defaults.
func productionCGNATSettings() CGNATSettings {
	return CGNATSettings{
		ASNs:                     []int{14593, 21928},
		CIDRs:                    []string{"100.64.0.0/10"},
		CommodityProfilePrefixes: []string{"", "Meta Quest 2::", "Meta Quest 3::", "Meta Quest 3S::"},
	}
}

// desktopSystemInfo is a machine-specific profile: the kind that identifies one
// machine rather than a class of them, so nothing but the commodity filter
// could plausibly drop it.
func desktopSystemInfo() evr.SystemInfo {
	return evr.SystemInfo{
		HeadsetType:        "Valve Index",
		NetworkType:        "Wired",
		VideoCard:          "NVIDIA GeForce RTX 4080",
		CPUModel:           "AMD Ryzen 9 7950X 16-Core Processor",
		NumPhysicalCores:   16,
		NumLogicalCores:    32,
		MemoryTotal:        68719476736,
		DedicatedGPUMemory: 17179869184,
	}
}

func weakSignalEntry(accountID uint64, ip, serial string, si evr.SystemInfo) *LoginHistoryEntry {
	return &LoginHistoryEntry{
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
		XPID:      evr.EvrId{PlatformCode: evr.OVR_ORG, AccountId: accountID},
		ClientIP:  ip,
		LoginData: &evr.LoginProfile{HMDSerialNumber: serial, SystemInfo: si},
	}
}

func weakSignalHistory(userID string, entries ...*LoginHistoryEntry) *LoginHistory {
	h := &LoginHistory{userID: userID, History: make(map[string]*LoginHistoryEntry, len(entries))}
	for _, e := range entries {
		h.History[e.Key()] = e
	}
	h.rebuildCache()
	return h
}

// TestIsWeakSignal_EmptyCommodityPrefixIsNotAUniversalMatch is the headline
// guard. An empty entry in the operator-configured prefix list must be a no-op,
// not a match-everything rule.
//
// The "still true" rows are the control: they stop the fix from being "make
// every item strong", which would reopen the false-positive problem the
// commodity filter exists to solve.
func TestIsWeakSignal_EmptyCommodityPrefixIsNotAUniversalMatch(t *testing.T) {
	d := withDetector(t, productionCGNATSettings())

	commodityProfile := profileString("Meta Quest 2", "WIFI", "", "Unknown", "3", "8", "0", "0")
	desktopProfile := (&LoginHistoryEntry{LoginData: &evr.LoginProfile{SystemInfo: desktopSystemInfo()}}).SystemProfile()

	tests := []struct {
		name string
		item string
		want bool
	}{
		// The three strong signals. The doc comment on IsWeakSignal says
		// "HMD serials and XPIDs are always strong signals"; one empty
		// prefix made all three weak.
		{"XPID", "OVR-ORG-2097", false},
		{"XPID of the second account on the same Oculus login", "OVR-ORG-4242", false},
		{"HMD serial", "WMHD3157200FJE", false},
		{"machine-specific system profile", desktopProfile, false},
		{"public non-CGNAT IPv4", "45.33.90.154", false},

		// Controls: the filter must still do its job.
		{"commodity Quest profile", commodityProfile, true},
		{"empty string", "", true},
		{"literal unknown", "unknown", true},
		{"CGNAT IPv4 in 100.64.0.0/10", "100.71.3.9", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := d.IsWeakSignal(tt.item); got != tt.want {
				t.Errorf("IsWeakSignal(%q) = %v, want %v; commodity prefixes configured as %q -- an empty prefix matches every string, so it must be skipped rather than applied",
					tt.item, got, tt.want, productionCGNATSettings().CommodityProfilePrefixes)
			}
		})
	}
}

// TestMatchIgnoredAltPattern_EmptyCommodityPrefix asserts the same thing one
// layer up, where the consequence actually lands: matchIgnoredAltPattern is
// what rebuildCache and AltSearchPatterns consult before keeping an item.
func TestMatchIgnoredAltPattern_EmptyCommodityPrefix(t *testing.T) {
	withDetector(t, productionCGNATSettings())

	commodityProfile := profileString("Meta Quest 2", "WIFI", "", "Unknown", "3", "8", "0", "0")
	desktopProfile := (&LoginHistoryEntry{LoginData: &evr.LoginProfile{SystemInfo: desktopSystemInfo()}}).SystemProfile()

	tests := []struct {
		name string
		item string
		want bool
	}{
		{"XPID is kept", "OVR-ORG-2097", false},
		{"HMD serial is kept", "WMHD3157200FJE", false},
		{"machine-specific profile is kept", desktopProfile, false},
		{"public IP is kept", "45.33.90.154", false},

		{"commodity profile is dropped", commodityProfile, true},
		{"degenerate profile is dropped", profileString("Unknown", "", "", "", "0", "0", "0", "0"), true},
		{"known-bad serial is dropped", "1WMHH000X00000", true},
		{"private IP is dropped", "192.168.1.44", true},
		{"CGNAT IP is dropped", "100.71.3.9", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := matchIgnoredAltPattern(tt.item); got != tt.want {
				t.Errorf("matchIgnoredAltPattern(%q) = %v, want %v; an item dropped here never reaches LoginHistory.Cache and can never surface an alt candidate",
					tt.item, got, tt.want)
			}
		})
	}
}

// TestRebuildCache_ContainsAllFourItems is the contract test.
//
// LoginHistory.Cache is documented at evr_authenticate_history.go:161 as "list
// of IP addresses, EvrID's, HMD Serial Numbers, and System Data", and
// LoginHistoryEntry.Items() returns exactly those four. rebuildCache must put
// all four in the cache. Asserted against the contract, not against the cause,
// so it holds whatever the fix turns out to be.
//
// Run under the production detector settings, because that is the configuration
// in which the contract was being violated -- with a nil detector (which is what
// every other test in this package gets) the assertion passes even on the broken
// build, which is precisely why nothing caught this.
func TestRebuildCache_ContainsAllFourItems(t *testing.T) {
	withDetector(t, productionCGNATSettings())

	entry := weakSignalEntry(2097, "45.33.90.154", "WMHD3157200FJE", desktopSystemInfo())
	h := weakSignalHistory("user-a", entry)

	want := []struct {
		kind string
		item string
	}{
		{"client IP", entry.ClientIP},
		{"HMD serial", entry.LoginData.HMDSerialNumber},
		{"XPID", entry.XPID.Token()},
		{"system profile", entry.SystemProfile()},
	}

	for _, w := range want {
		if !slices.Contains(h.Cache, w.item) {
			t.Errorf("rebuildCache() dropped the %s %q from LoginHistory.Cache.\n  Items()  = %q\n  Cache    = %q\nThe cache field is documented as \"IP addresses, EvrID's, HMD Serial Numbers, and System Data\" and is the only thing the alt discovery query (+value.cache:) can match on.",
				w.kind, w.item, entry.Items(), h.Cache)
		}
	}
}

// TestAltSearchPatterns_ContainsXPIDAndSerial is the discovery-side half of the
// same contract. A key that is written into the cache but never searched for is
// inert, and a key that is searched for but never written is equally inert;
// both ends have to carry the XPID.
//
// The system profile is deliberately NOT expected here -- it is a comparison
// key only, by design (see the comment on AltSearchPatterns).
func TestAltSearchPatterns_ContainsXPIDAndSerial(t *testing.T) {
	withDetector(t, productionCGNATSettings())

	entry := weakSignalEntry(2097, "45.33.90.154", "WMHD3157200FJE", desktopSystemInfo())
	h := weakSignalHistory("user-a", entry)

	patterns := h.AltSearchPatterns()
	for _, want := range []string{entry.XPID.Token(), entry.LoginData.HMDSerialNumber, entry.ClientIP} {
		if !slices.Contains(patterns, want) {
			t.Errorf("AltSearchPatterns() omits %q; got %q. LoginAlternatePatternSearch queries +value.cache on exactly these, so an omitted key can never surface an alt candidate.",
				want, patterns)
		}
	}
}

// TestAltDetection_SharedXPIDOnlyIsDiscoverable is the end-to-end regression
// test: it encodes the real-world failure that was reported.
//
// Two accounts share ONE thing -- the Oculus account (XPID). Different IPs,
// different headsets, different serials, different machines. In production
// (users 2006wlw and shotterdash, both on OVR-ORG-2097) they were not linkable,
// because the XPID never reached either account's cache.
//
// Discoverability is asserted the way the index actually decides it: the query
// in LoginAlternatePatternSearch is `+value.cache:<AltSearchPatterns()>` against
// the indexed cache field, so account A surfaces account B exactly when one of
// A's search patterns appears in B's rebuilt cache. Nothing the query does not
// return is ever passed to loginHistoryCompare, so the comparison forming an
// edge is not a substitute for discovery -- both are asserted.
func TestAltDetection_SharedXPIDOnlyIsDiscoverable(t *testing.T) {
	withDetector(t, productionCGNATSettings())

	sharedXPID := evr.EvrId{PlatformCode: evr.OVR_ORG, AccountId: 2097}

	entryA := &LoginHistoryEntry{
		CreatedAt: time.Now(), UpdatedAt: time.Now(),
		XPID:     sharedXPID,
		ClientIP: "45.33.90.154",
		LoginData: &evr.LoginProfile{
			HMDSerialNumber: "SERIAL-AAAA",
			SystemInfo:      desktopSystemInfo(),
		},
	}
	// A different machine entirely: nothing here is shared with A except the
	// Oculus account.
	otherMachine := evr.SystemInfo{
		HeadsetType: "Meta Quest 3", NetworkType: "WIFI",
		VideoCard: "", CPUModel: "Unknown",
		NumPhysicalCores: 4, NumLogicalCores: 8,
	}
	entryB := &LoginHistoryEntry{
		CreatedAt: time.Now(), UpdatedAt: time.Now(),
		XPID:     sharedXPID,
		ClientIP: "198.51.100.7",
		LoginData: &evr.LoginProfile{
			HMDSerialNumber: "SERIAL-BBBB",
			SystemInfo:      otherMachine,
		},
	}

	a := weakSignalHistory("user-2006wlw", entryA)
	b := weakSignalHistory("user-shotterdash", entryB)

	// Guard the premise: the ONLY thing these two share is the XPID. If they
	// shared anything else the test could pass for an unrelated reason.
	for _, notShared := range []string{entryA.ClientIP, entryA.LoginData.HMDSerialNumber, entryA.SystemProfile()} {
		if slices.Contains(b.Cache, notShared) {
			t.Fatalf("premise broken: account B's cache contains account A's %q (cache %q)", notShared, b.Cache)
		}
	}

	// 1. Discovery: A's search patterns must hit B's indexed cache, on the XPID.
	var hits []string
	for _, p := range a.AltSearchPatterns() {
		if slices.Contains(b.Cache, p) {
			hits = append(hits, p)
		}
	}
	if !slices.Contains(hits, sharedXPID.Token()) {
		t.Errorf("two accounts on the same Oculus account %s are not discoverable as alts.\n  A.AltSearchPatterns() = %q\n  B.Cache               = %q\n  overlap               = %q\nThe discovery query is +value.cache:<patterns>, so with no overlap B is never returned, never compared, and produces zero edges.",
			sharedXPID.Token(), a.AltSearchPatterns(), b.Cache, hits)
	}

	// 2. And symmetrically, because the link is written on both sides.
	var reverseHits []string
	for _, p := range b.AltSearchPatterns() {
		if slices.Contains(a.Cache, p) {
			reverseHits = append(reverseHits, p)
		}
	}
	if !slices.Contains(reverseHits, sharedXPID.Token()) {
		t.Errorf("the reverse direction does not discover either: B.AltSearchPatterns() = %q, A.Cache = %q, overlap = %q",
			b.AltSearchPatterns(), a.Cache, reverseHits)
	}

	// 3. Comparison: once surfaced, the edge must actually name the XPID.
	matches := loginHistoryCompare(a, b)
	if len(matches) == 0 {
		t.Fatalf("loginHistoryCompare formed no edge between two accounts sharing XPID %s", sharedXPID.Token())
	}
	found := false
	for _, m := range matches {
		if slices.Contains(m.Items, sharedXPID.Token()) {
			found = true
		}
	}
	if !found {
		t.Errorf("loginHistoryCompare formed an edge but did not report the shared XPID %s among its items; got %+v. This is the production shape: every computed alternate_accounts link listed IP items only.",
			sharedXPID.Token(), matches)
	}
}
