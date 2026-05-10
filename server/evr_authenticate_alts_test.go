package server

import (
	"reflect"
	"slices"
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
		XPIs:            nil,
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
