package server

import (
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/heroiclabs/nakama/v3/server/evr"
)

func TestLoginHistory_AltSearchPatterns(t *testing.T) {
	tests := []struct {
		name    string
		history *LoginHistory
		want    []string
	}{
		{
			name: "first login includes XPID even with empty XPIs map",
			history: &LoginHistory{
				userID: "new-user",
				History: map[string]*LoginHistoryEntry{
					"OVR-27670:45.33.90.154": {
						XPID:     evr.EvrId{PlatformCode: evr.OVR, AccountId: 27670},
						ClientIP: "45.33.90.154",
						LoginData: &evr.LoginProfile{
							HMDSerialNumber: "WMHD3157200FJE",
						},
					},
				},
				XPIs: nil, // empty on first login — rebuildCache hasn't run yet
			},
			want: []string{"45.33.90.154", "OVR-27670", "WMHD3157200FJE"},
		},
		{
			name: "subsequent login includes XPIs from map and history",
			history: &LoginHistory{
				userID: "returning-user",
				History: map[string]*LoginHistoryEntry{
					"OVR-27670:10.0.0.1": {
						XPID:     evr.EvrId{PlatformCode: evr.OVR, AccountId: 27670},
						ClientIP: "10.0.0.1", // private — will be filtered
						LoginData: &evr.LoginProfile{
							HMDSerialNumber: "SERIAL1",
						},
					},
				},
				XPIs: map[string]time.Time{
					"OVR-27670": time.Now(), // from previous save
				},
			},
			want: []string{"OVR-27670", "SERIAL1"}, // private IP filtered, XPI deduplicated
		},
		{
			name: "empty history returns nil",
			history: &LoginHistory{
				userID:  "empty-user",
				History: map[string]*LoginHistoryEntry{},
				XPIs:    nil,
			},
			want: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.history.AltSearchPatterns()
			slices.Sort(got)
			slices.Sort(tt.want)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("AltSearchPatterns() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestLoginHistoryCompare(t *testing.T) {
	tests := []struct {
		name string
		a    *LoginHistory
		b    *LoginHistory
		want int // expected number of matches
	}{
		{
			name: "matching XPID produces a match",
			a: &LoginHistory{
				userID: "user-a",
				History: map[string]*LoginHistoryEntry{
					"entry1": {
						XPID:     evr.EvrId{PlatformCode: evr.OVR, AccountId: 27670},
						ClientIP: "1.2.3.4",
						LoginData: &evr.LoginProfile{
							HMDSerialNumber: "SERIAL_A",
						},
					},
				},
			},
			b: &LoginHistory{
				userID: "user-b",
				History: map[string]*LoginHistoryEntry{
					"entry1": {
						XPID:     evr.EvrId{PlatformCode: evr.OVR, AccountId: 27670},
						ClientIP: "5.6.7.8",
						LoginData: &evr.LoginProfile{
							HMDSerialNumber: "SERIAL_B",
						},
					},
				},
			},
			want: 1,
		},
		{
			name: "no shared identifiers produces no match",
			a: &LoginHistory{
				userID: "user-a",
				History: map[string]*LoginHistoryEntry{
					"entry1": {
						XPID:     evr.EvrId{PlatformCode: evr.OVR, AccountId: 11111},
						ClientIP: "1.2.3.4",
						LoginData: &evr.LoginProfile{
							HMDSerialNumber: "SERIAL_A",
							SystemInfo: evr.SystemInfo{
								HeadsetType: "Rift S",
								CPUModel:    "Intel i7",
							},
						},
					},
				},
			},
			b: &LoginHistory{
				userID: "user-b",
				History: map[string]*LoginHistoryEntry{
					"entry1": {
						XPID:     evr.EvrId{PlatformCode: evr.OVR, AccountId: 22222},
						ClientIP: "5.6.7.8",
						LoginData: &evr.LoginProfile{
							HMDSerialNumber: "SERIAL_B",
							SystemInfo: evr.SystemInfo{
								HeadsetType: "Quest 3",
								CPUModel:    "Snapdragon",
							},
						},
					},
				},
			},
			want: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := loginHistoryCompare(tt.a, tt.b)
			if len(got) != tt.want {
				t.Errorf("loginHistoryCompare() returned %d matches, want %d", len(got), tt.want)
			}
		})
	}
}

// TestFirstLogin_EnforcementUserIDs_MissesAlts proves that on first login,
// AlternateIDs() is empty even though the history entry contains identifiers
// that would match suspended alt accounts via AltSearchPatterns(). This means
// enforcementUserIDs (built from AlternateIDs at login) won't include alts
// discovered by UpdateAlternates which runs later.
func TestFirstLogin_EnforcementUserIDs_MissesAlts(t *testing.T) {
	// Simulate first login: history has one entry (just added by Update),
	// but AlternateMatches is empty because UpdateAlternates hasn't run yet.
	loginHistory := &LoginHistory{
		userID: "new-user",
		History: map[string]*LoginHistoryEntry{
			"OVR-27670:45.33.90.154": {
				XPID:     evr.EvrId{PlatformCode: evr.OVR, AccountId: 27670},
				ClientIP: "45.33.90.154",
				LoginData: &evr.LoginProfile{
					HMDSerialNumber: "WMHD3157200FJE",
				},
			},
		},
		// AlternateMatches is nil — UpdateAlternates hasn't run yet
		AlternateMatches: nil,
		XPIs:             nil,
	}

	// AltSearchPatterns returns identifiers that WOULD find banned alts in the index
	patterns := loginHistory.AltSearchPatterns()
	if len(patterns) == 0 {
		t.Fatal("AltSearchPatterns() should return identifiers for index search")
	}
	if !slices.Contains(patterns, "WMHD3157200FJE") {
		t.Errorf("AltSearchPatterns() should contain HMD serial, got %v", patterns)
	}

	// But AlternateIDs() — used to build enforcementUserIDs — is empty
	firstIDs, _ := loginHistory.AlternateIDs()
	if len(firstIDs) != 0 {
		t.Errorf("AlternateIDs() should be empty on first login before UpdateAlternates, got %v", firstIDs)
	}

	// This is the gap: patterns exist to FIND alts, but the enforcement check
	// uses AlternateIDs which is empty until UpdateAlternates runs (after the check).
}

func TestFilterStrongAlts(t *testing.T) {
	detector := NewCGNATDetector(nil)
	detector.UpdateSettings(CGNATSettings{
		ASNs:                     []int{14593, 21928},
		CIDRs:                    []string{"100.64.0.0/10"},
		CommodityProfilePrefixes: []string{"Meta Quest 2::", "Meta Quest 3::", "Meta Quest 3S::"},
	})

	tests := []struct {
		name    string
		history *LoginHistory
		altIDs  []string
		want    []string
	}{
		{
			name: "HMD serial match is strong signal — not filtered",
			history: &LoginHistory{
				userID: "current-user",
				AlternateMatches: map[string][]*AlternateSearchMatch{
					"banned-alt": {
						{OtherUserID: "banned-alt", Items: []string{"WMHD3157200FJE"}},
					},
				},
			},
			altIDs: []string{"banned-alt"},
			want:   []string{"banned-alt"},
		},
		{
			name: "XPID match is strong signal — not filtered",
			history: &LoginHistory{
				userID: "current-user",
				AlternateMatches: map[string][]*AlternateSearchMatch{
					"banned-alt": {
						{OtherUserID: "banned-alt", Items: []string{"OVR-27670"}},
					},
				},
			},
			altIDs: []string{"banned-alt"},
			want:   []string{"banned-alt"},
		},
		{
			name: "CGNAT IP match is weak signal — filtered",
			history: &LoginHistory{
				userID: "current-user",
				AlternateMatches: map[string][]*AlternateSearchMatch{
					"banned-alt": {
						{OtherUserID: "banned-alt", Items: []string{"100.80.1.1"}},
					},
				},
			},
			altIDs: []string{"banned-alt"},
			want:   []string{},
		},
		{
			name: "commodity profile match is weak signal — filtered",
			history: &LoginHistory{
				userID: "current-user",
				AlternateMatches: map[string][]*AlternateSearchMatch{
					"banned-alt": {
						{OtherUserID: "banned-alt", Items: []string{"Meta Quest 3::WiFi::Adreno 740::Snapdragon::8::8::12000000000::0"}},
					},
				},
			},
			altIDs: []string{"banned-alt"},
			want:   []string{},
		},
		{
			name: "non-CGNAT IP is strong signal — not filtered",
			history: &LoginHistory{
				userID: "current-user",
				AlternateMatches: map[string][]*AlternateSearchMatch{
					"banned-alt": {
						{OtherUserID: "banned-alt", Items: []string{"45.33.90.154"}},
					},
				},
			},
			altIDs: []string{"banned-alt"},
			want:   []string{"banned-alt"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := filterStrongAlts(tt.history, tt.altIDs, detector)
			if got == nil {
				got = []string{}
			}
			slices.Sort(got)
			slices.Sort(tt.want)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("filterStrongAlts() = %v, want %v", got, tt.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// #516 — the machine fingerprint must be a DISCOVERY key, not only a
// comparison key.
// ---------------------------------------------------------------------------

// profileString joins system-profile components exactly the way
// LoginHistoryEntry.SystemProfile does. Building the expected strings from
// their parts rather than typing them out is deliberate: the separator is "::"
// and several components are empty, so a hand-written literal turns into a run
// of colons that is trivial to miscount — and a test asserting the wrong string
// fails for a reason that has nothing to do with the behaviour under test.
func profileString(components ...string) string {
	if len(components) != systemProfileComponents {
		panic("profileString: wrong component count")
	}
	return strings.Join(components, "::")
}

// richSystemInfo is a desktop profile with real hardware strings: the kind of
// fingerprint that identifies one machine rather than a class of them.
func richSystemInfo() evr.SystemInfo {
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

// TestAltSearchPatterns_IncludesSystemProfile is the direct regression test for
// #516: the fingerprint has to appear among the keys the index is queried with.
func TestAltSearchPatterns_IncludesSystemProfile(t *testing.T) {
	entry := &LoginHistoryEntry{
		XPID:     evr.EvrId{PlatformCode: evr.OVR, AccountId: 27670},
		ClientIP: "45.33.90.154",
		LoginData: &evr.LoginProfile{
			HMDSerialNumber: "WMHD3157200FJE",
			SystemInfo:      richSystemInfo(),
		},
	}
	h := &LoginHistory{
		userID:  "user-a",
		History: map[string]*LoginHistoryEntry{entry.Key(): entry},
	}

	patterns := h.AltSearchPatterns()
	want := entry.SystemProfile()
	if !slices.Contains(patterns, want) {
		t.Errorf("AltSearchPatterns() omits the system profile %q; got %v", want, patterns)
	}
}

// TestAltSearchPatterns_RotatedIdentifiersStillDiscoverable is the defect as
// reported. Two accounts share nothing but the machine: different IP, different
// HMD serial, different XPID — every key a cheater can rotate by hand.
//
// Discoverability is asserted the way the index actually decides it. The query
// in LoginAlternatePatternSearch is `+value.cache:<patterns>` against the
// indexed `cache` field, so the other account is returned iff one of this
// account's search patterns appears in that account's rebuilt cache. Anything
// the query does not return is never passed to loginHistoryCompare, so it can
// never form an edge no matter what loginHistoryCompare would have concluded.
func TestAltSearchPatterns_RotatedIdentifiersStillDiscoverable(t *testing.T) {
	sysinfo := richSystemInfo()

	bannedEntry := &LoginHistoryEntry{
		XPID:      evr.EvrId{PlatformCode: evr.OVR, AccountId: 11111},
		ClientIP:  "45.33.90.154",
		UpdatedAt: time.Now(),
		LoginData: &evr.LoginProfile{
			HMDSerialNumber: "SERIAL-OLD",
			SystemInfo:      sysinfo,
		},
	}
	banned := &LoginHistory{
		userID:  "banned-user",
		History: map[string]*LoginHistoryEntry{bannedEntry.Key(): bannedEntry},
	}
	banned.rebuildCache()

	// The burner: same machine, every hand-rotatable identifier changed.
	burnerEntry := &LoginHistoryEntry{
		XPID:      evr.EvrId{PlatformCode: evr.OVR, AccountId: 22222},
		ClientIP:  "198.51.100.7",
		UpdatedAt: time.Now(),
		LoginData: &evr.LoginProfile{
			HMDSerialNumber: "SERIAL-NEW",
			SystemInfo:      sysinfo,
		},
	}
	burner := &LoginHistory{
		userID:  "burner-user",
		History: map[string]*LoginHistoryEntry{burnerEntry.Key(): burnerEntry},
	}

	// Guard the premise: if the two accounts shared a rotatable key, this test
	// would pass for the wrong reason.
	for _, shared := range []string{bannedEntry.ClientIP, bannedEntry.LoginData.HMDSerialNumber, bannedEntry.XPID.Token()} {
		if slices.Contains(burner.AltSearchPatterns(), shared) {
			t.Fatalf("premise broken: the two accounts share rotatable key %q", shared)
		}
	}

	var hits []string
	for _, p := range burner.AltSearchPatterns() {
		if slices.Contains(banned.Cache, p) {
			hits = append(hits, p)
		}
	}
	if len(hits) == 0 {
		t.Fatalf("burner account is not discoverable from the banned account's index entry; "+
			"search patterns %v matched nothing in cache %v", burner.AltSearchPatterns(), banned.Cache)
	}

	// And once discovered, an edge actually forms.
	if matches := loginHistoryCompare(burner, banned); len(matches) == 0 {
		t.Error("loginHistoryCompare formed no edge between accounts sharing a machine")
	}
}

// TestAltSearchPatterns_ExcludesDegenerateSystemProfile guards the hazard the
// fix introduces if left unguarded. Every account that logs in with no
// SystemInfo produces the byte-identical profile string, so making the profile
// a discovery key without this filter would make every profile-less account a
// candidate alt of every other one.
func TestAltSearchPatterns_ExcludesDegenerateSystemProfile(t *testing.T) {
	newHistory := func(userID string, accountID uint64, ip string) *LoginHistory {
		e := &LoginHistoryEntry{
			XPID:      evr.EvrId{PlatformCode: evr.OVR, AccountId: accountID},
			ClientIP:  ip,
			UpdatedAt: time.Now(),
			LoginData: &evr.LoginProfile{HMDSerialNumber: "SERIAL-" + userID},
		}
		h := &LoginHistory{userID: userID, History: map[string]*LoginHistoryEntry{e.Key(): e}}
		h.rebuildCache()
		return h
	}

	a := newHistory("user-a", 1, "45.33.90.154")
	b := newHistory("user-b", 2, "198.51.100.7")

	// Both produce the same empty profile — that is the point.
	degenerate := profileString("Unknown", "", "", "", "0", "0", "0", "0")
	for name, h := range map[string]*LoginHistory{"a": a, "b": b} {
		for _, e := range h.History {
			if got := e.SystemProfile(); got != degenerate {
				t.Fatalf("premise broken: account %s profile is %q, expected the degenerate %q", name, got, degenerate)
			}
		}
		if slices.Contains(h.AltSearchPatterns(), degenerate) {
			t.Errorf("account %s searches on the degenerate profile %q", name, degenerate)
		}
		if slices.Contains(h.Cache, degenerate) {
			t.Errorf("account %s indexes the degenerate profile %q", name, degenerate)
		}
	}

	// Nothing links them: they share no machine and no rotatable key.
	for _, p := range a.AltSearchPatterns() {
		if slices.Contains(b.Cache, p) {
			t.Errorf("unrelated accounts linked by %q", p)
		}
	}
}

func TestIsDegenerateSystemProfile(t *testing.T) {
	tests := []struct {
		name    string
		pattern string
		want    bool
	}{
		{"no SystemInfo at all", profileString("Unknown", "", "", "", "0", "0", "0", "0"), true},
		{"placeholder headset, numbers only", profileString("Unknown", "", "", "", "16", "32", "68719476736", "17179869184"), true},
		{"empty headset, numbers only", profileString("", "", "", "", "16", "32", "68719476736", "17179869184"), true},
		{"real desktop profile", profileString("Valve Index", "Wired", "NVIDIA GeForce RTX 4080", "AMD Ryzen 9 7950X 16-Core Processor", "16", "32", "68719476736", "17179869184"), false},
		{"headset only", profileString("Meta Quest 3", "", "", "", "0", "0", "0", "0"), false},
		{"network type only", profileString("Unknown", "Wireless", "", "", "0", "0", "0", "0"), false},
		{"not a system profile — HMD serial", "WMHD3157200FJE", false},
		{"not a system profile — IP", "45.33.90.154", false},
		{"not a system profile — empty", "", false},
		{"wrong component count", "a::b::c", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isDegenerateSystemProfile(tt.pattern); got != tt.want {
				t.Errorf("isDegenerateSystemProfile(%q) = %v, want %v", tt.pattern, got, tt.want)
			}
		})
	}
}
