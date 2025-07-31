package server

import (
	"encoding/json"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/heroiclabs/nakama/v3/server/evr"
)

func TestNewLoginHistory(t *testing.T) {
	tests := []struct {
		name   string
		userID string
		want   *LoginHistory
	}{
		{
			name:   "creates new history with user ID",
			userID: "test-user-123",
			want: &LoginHistory{
				userID:  "test-user-123",
				version: "*",
			},
		},
		{
			name:   "creates new history with empty user ID",
			userID: "",
			want: &LoginHistory{
				userID:  "",
				version: "*",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := NewLoginHistory(tt.userID)
			if got.userID != tt.want.userID {
				t.Errorf("NewLoginHistory() userID = %v, want %v", got.userID, tt.want.userID)
			}
			if got.version != tt.want.version {
				t.Errorf("NewLoginHistory() version = %v, want %v", got.version, tt.want.version)
			}
			// Check that all maps are initialized to nil (lazy initialization)
			if got.Active != nil {
				t.Errorf("NewLoginHistory() Active should be nil for lazy initialization")
			}
			if got.History != nil {
				t.Errorf("NewLoginHistory() History should be nil for lazy initialization")
			}
		})
	}
}

// Test helper functions
func TestMatchIgnoredAltPattern(t *testing.T) {
	tests := []struct {
		name    string
		pattern string
		want    bool
	}{
		{
			name:    "ignores empty string",
			pattern: "",
			want:    true,
		},
		{
			name:    "ignores known ignored value",
			pattern: "1WMHH000X00000",
			want:    true,
		},
		{
			name:    "ignores private IP",
			pattern: "192.168.1.1",
			want:    true,
		},
		{
			name:    "does not ignore localhost",
			pattern: "127.0.0.1",
			want:    false,
		},
		{
			name:    "does not ignore public IP",
			pattern: "8.8.8.8",
			want:    false,
		},
		{
			name:    "does not ignore regular string",
			pattern: "ABC123XYZ",
			want:    false,
		},
		{
			name:    "ignores invalid IP format as non-IP",
			pattern: "not.an.ip",
			want:    false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := matchIgnoredAltPattern(tt.pattern)
			if got != tt.want {
				t.Errorf("matchIgnoredAltPattern(%v) = %v, want %v", tt.pattern, got, tt.want)
			}
		})
	}
}

func TestLoginHistoryEntryKey(t *testing.T) {
	xpid := evr.EvrId{PlatformCode: evr.OVR, AccountId: 12345}
	ip := "192.168.1.1"
	
	got := loginHistoryEntryKey(xpid, ip)
	want := "OVR-12345:192.168.1.1"
	
	if got != want {
		t.Errorf("loginHistoryEntryKey() = %v, want %v", got, want)
	}
}

func TestLoginHistoryEntry_Key(t *testing.T) {
	tests := []struct {
		name  string
		entry *LoginHistoryEntry
		want  string
	}{
		{
			name: "generates key from XPID and ClientIP",
			entry: &LoginHistoryEntry{
				XPID:     evr.EvrId{PlatformCode: evr.OVR, AccountId: 12345},
				ClientIP: "192.168.1.1",
			},
			want: "OVR-12345:192.168.1.1",
		},
		{
			name: "handles empty IP",
			entry: &LoginHistoryEntry{
				XPID:     evr.EvrId{PlatformCode: evr.STM, AccountId: 67890},
				ClientIP: "",
			},
			want: "STM-67890:",
		},
		{
			name: "handles nil XPID",
			entry: &LoginHistoryEntry{
				XPID:     evr.EvrId{},
				ClientIP: "10.0.0.1",
			},
			want: "UNK-0:10.0.0.1",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.entry.Key()
			if got != tt.want {
				t.Errorf("LoginHistoryEntry.Key() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestLoginHistoryEntry_PendingCode(t *testing.T) {
	// Test deterministic code generation
	createdAt := time.Date(2023, 1, 1, 12, 0, 0, 123456789, time.UTC)
	entry := &LoginHistoryEntry{
		CreatedAt: createdAt,
	}

	got := entry.PendingCode()
	want := "89" // 123456789 % 100 = 89

	if got != want {
		t.Errorf("LoginHistoryEntry.PendingCode() = %v, want %v", got, want)
	}

	// Test format is always 2 digits
	if len(got) != 2 {
		t.Errorf("LoginHistoryEntry.PendingCode() length = %v, want 2", len(got))
	}
}

func TestLoginHistoryEntry_SystemProfile(t *testing.T) {
	tests := []struct {
		name      string
		entry     *LoginHistoryEntry
		wantParts []string
	}{
		{
			name: "creates system profile string",
			entry: &LoginHistoryEntry{
				LoginData: &evr.LoginProfile{
					SystemInfo: evr.SystemInfo{
						HeadsetType:        "Oculus Rift S",
						NetworkType:        "WiFi",
						VideoCard:          "NVIDIA RTX 3080",
						CPUModel:           "Intel i7-9700K",
						NumPhysicalCores:   8,
						NumLogicalCores:    8,
						MemoryTotal:        16777216000,
						DedicatedGPUMemory: 10737418240,
					},
				},
			},
			wantParts: []string{
				"Oculus Rift S", // normalized headset type
				"WiFi",
				"NVIDIA RTX 3080",
				"Intel i7-9700K",
				"8",
				"8",
				"16777216000",
				"10737418240",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.entry.SystemProfile()
			parts := strings.Split(got, "::")
			if len(parts) != 8 {
				t.Errorf("LoginHistoryEntry.SystemProfile() parts count = %v, want 8", len(parts))
			}
			// Note: We can't easily test the normalized headset type without access to normalizeHeadsetType()
			// but we can test the structure
			if !strings.Contains(got, "::") {
				t.Errorf("LoginHistoryEntry.SystemProfile() should contain '::' separators")
			}
		})
	}
}

func TestLoginHistoryEntry_Patterns(t *testing.T) {
	entry := &LoginHistoryEntry{
		XPID:     evr.EvrId{PlatformCode: evr.OVR, AccountId: 12345},
		ClientIP: "192.168.1.1",
		LoginData: &evr.LoginProfile{
			HMDSerialNumber: "ABC123XYZ",
		},
	}

	got := entry.Patterns()
	want := []string{"192.168.1.1", "ABC123XYZ", "OVR-12345"}

	if !reflect.DeepEqual(got, want) {
		t.Errorf("LoginHistoryEntry.Patterns() = %v, want %v", got, want)
	}
}

func TestLoginHistoryEntry_Items(t *testing.T) {
	entry := &LoginHistoryEntry{
		XPID:     evr.EvrId{PlatformCode: evr.OVR, AccountId: 12345},
		ClientIP: "192.168.1.1",
		LoginData: &evr.LoginProfile{
			HMDSerialNumber: "ABC123XYZ",
			SystemInfo: evr.SystemInfo{
				HeadsetType:        "Oculus Rift S",
				NetworkType:        "WiFi",
				VideoCard:          "NVIDIA RTX 3080",
				CPUModel:           "Intel i7-9700K",
				NumPhysicalCores:   8,
				NumLogicalCores:    8,
				MemoryTotal:        16777216000,
				DedicatedGPUMemory: 10737418240,
			},
		},
	}

	got := entry.Items()
	
	// Should have 4 items: ClientIP, HMDSerialNumber, XPID token, SystemProfile
	if len(got) != 4 {
		t.Errorf("LoginHistoryEntry.Items() length = %v, want 4", len(got))
	}

	// First three should match Patterns()
	patterns := entry.Patterns()
	for i := 0; i < 3; i++ {
		if got[i] != patterns[i] {
			t.Errorf("LoginHistoryEntry.Items()[%d] = %v, want %v", i, got[i], patterns[i])
		}
	}

	// Fourth should be system profile
	systemProfile := entry.SystemProfile()
	if got[3] != systemProfile {
		t.Errorf("LoginHistoryEntry.Items()[3] = %v, want system profile", got[3])
	}
}

func TestLoginHistory_IsAuthorizedIP(t *testing.T) {
	tests := []struct {
		name    string
		history *LoginHistory
		ip      string
		want    bool
	}{
		{
			name: "returns true for authorized IP",
			history: &LoginHistory{
				AuthorizedIPs: map[string]time.Time{
					"192.168.1.1": time.Now(),
				},
			},
			ip:   "192.168.1.1",
			want: true,
		},
		{
			name: "returns false for unauthorized IP",
			history: &LoginHistory{
				AuthorizedIPs: map[string]time.Time{
					"192.168.1.1": time.Now(),
				},
			},
			ip:   "192.168.1.2",
			want: false,
		},
		{
			name:    "returns false when AuthorizedIPs is nil",
			history: &LoginHistory{},
			ip:      "192.168.1.1",
			want:    false,
		},
		{
			name: "returns false when AuthorizedIPs is empty",
			history: &LoginHistory{
				AuthorizedIPs: map[string]time.Time{},
			},
			ip:   "192.168.1.1",
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.history.IsAuthorizedIP(tt.ip)
			if got != tt.want {
				t.Errorf("LoginHistory.IsAuthorizedIP() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestLoginHistory_AuthorizeIP(t *testing.T) {
	tests := []struct {
		name         string
		history      *LoginHistory
		ip           string
		wantIsNew    bool
		wantHasEntry bool
	}{
		{
			name:         "authorizes new IP",
			history:      &LoginHistory{},
			ip:           "192.168.1.1",
			wantIsNew:    true,
			wantHasEntry: true,
		},
		{
			name: "re-authorizes existing IP",
			history: &LoginHistory{
				AuthorizedIPs: map[string]time.Time{
					"192.168.1.1": time.Now().Add(-time.Hour),
				},
			},
			ip:           "192.168.1.1",
			wantIsNew:    false,
			wantHasEntry: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotIsNew := tt.history.AuthorizeIP(tt.ip)
			if gotIsNew != tt.wantIsNew {
				t.Errorf("LoginHistory.AuthorizeIP() = %v, want %v", gotIsNew, tt.wantIsNew)
			}

			if tt.wantHasEntry {
				if tt.history.AuthorizedIPs == nil {
					t.Error("LoginHistory.AuthorizeIP() should initialize AuthorizedIPs")
				}
				if _, exists := tt.history.AuthorizedIPs[tt.ip]; !exists {
					t.Error("LoginHistory.AuthorizeIP() should add IP to AuthorizedIPs")
				}
			}
		})
	}
}

func TestLoginHistory_AddPendingAuthorizationIP(t *testing.T) {
	history := &LoginHistory{}
	xpid := evr.EvrId{PlatformCode: evr.OVR, AccountId: 12345}
	clientIP := "192.168.1.1"
	loginData := &evr.LoginProfile{
		DisplayName: "TestUser",
	}

	entry := history.AddPendingAuthorizationIP(xpid, clientIP, loginData)

	// Check return value
	if entry == nil {
		t.Fatal("AddPendingAuthorizationIP should return an entry")
	}
	if entry.XPID != xpid {
		t.Errorf("entry.XPID = %v, want %v", entry.XPID, xpid)
	}
	if entry.ClientIP != clientIP {
		t.Errorf("entry.ClientIP = %v, want %v", entry.ClientIP, clientIP)
	}
	if entry.LoginData != loginData {
		t.Errorf("entry.LoginData = %v, want %v", entry.LoginData, loginData)
	}

	// Check state changes
	if history.PendingAuthorizations == nil {
		t.Error("AddPendingAuthorizationIP should initialize PendingAuthorizations")
	}
	if storedEntry, exists := history.PendingAuthorizations[clientIP]; !exists {
		t.Error("AddPendingAuthorizationIP should store entry in PendingAuthorizations")
	} else if storedEntry != entry {
		t.Error("AddPendingAuthorizationIP should store the same entry reference")
	}
}

func TestLoginHistory_GetPendingAuthorizationIP(t *testing.T) {
	entry := &LoginHistoryEntry{
		ClientIP: "192.168.1.1",
		XPID:     evr.EvrId{PlatformCode: evr.OVR, AccountId: 12345},
	}

	tests := []struct {
		name    string
		history *LoginHistory
		ip      string
		want    *LoginHistoryEntry
	}{
		{
			name: "returns existing pending authorization",
			history: &LoginHistory{
				PendingAuthorizations: map[string]*LoginHistoryEntry{
					"192.168.1.1": entry,
				},
			},
			ip:   "192.168.1.1",
			want: entry,
		},
		{
			name: "returns nil for non-existent IP",
			history: &LoginHistory{
				PendingAuthorizations: map[string]*LoginHistoryEntry{
					"192.168.1.1": entry,
				},
			},
			ip:   "192.168.1.2",
			want: nil,
		},
		{
			name:    "returns nil when PendingAuthorizations is nil",
			history: &LoginHistory{},
			ip:      "192.168.1.1",
			want:    nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.history.GetPendingAuthorizationIP(tt.ip)
			if got != tt.want {
				t.Errorf("LoginHistory.GetPendingAuthorizationIP() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestLoginHistory_RemovePendingAuthorizationIP(t *testing.T) {
	entry := &LoginHistoryEntry{
		ClientIP: "192.168.1.1",
		XPID:     evr.EvrId{PlatformCode: evr.OVR, AccountId: 12345},
	}

	tests := []struct {
		name    string
		history *LoginHistory
		ip      string
		want    *LoginHistoryEntry
	}{
		{
			name: "removes and returns existing pending authorization",
			history: &LoginHistory{
				PendingAuthorizations: map[string]*LoginHistoryEntry{
					"192.168.1.1": entry,
				},
			},
			ip:   "192.168.1.1",
			want: entry,
		},
		{
			name: "returns nil for non-existent IP",
			history: &LoginHistory{
				PendingAuthorizations: map[string]*LoginHistoryEntry{
					"192.168.1.1": entry,
				},
			},
			ip:   "192.168.1.2",
			want: nil,
		},
		{
			name:    "returns nil when PendingAuthorizations is nil",
			history: &LoginHistory{},
			ip:      "192.168.1.1",
			want:    nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Make a copy to check removal
			originalMap := make(map[string]*LoginHistoryEntry)
			if tt.history.PendingAuthorizations != nil {
				for k, v := range tt.history.PendingAuthorizations {
					originalMap[k] = v
				}
			}

			got := tt.history.RemovePendingAuthorizationIP(tt.ip)
			if got != tt.want {
				t.Errorf("LoginHistory.RemovePendingAuthorizationIP() = %v, want %v", got, tt.want)
			}

			// Check that entry was actually removed
			if tt.want != nil && tt.history.PendingAuthorizations != nil {
				if _, exists := tt.history.PendingAuthorizations[tt.ip]; exists {
					t.Error("RemovePendingAuthorizationIP should remove entry from map")
				}
			}
		})
	}
}

func TestLoginHistory_AuthorizeIPWithCode(t *testing.T) {
	// Create entry with known timestamp for predictable code
	createdAt := time.Date(2023, 1, 1, 12, 0, 0, 123456789, time.UTC)
	entry := &LoginHistoryEntry{
		CreatedAt: createdAt,
		ClientIP:  "192.168.1.1",
		XPID:      evr.EvrId{PlatformCode: evr.OVR, AccountId: 12345},
	}
	expectedCode := "89" // 123456789 % 100 = 89

	tests := []struct {
		name    string
		history *LoginHistory
		ip      string
		code    string
		wantErr bool
	}{
		{
			name: "authorizes with correct code",
			history: &LoginHistory{
				PendingAuthorizations: map[string]*LoginHistoryEntry{
					"192.168.1.1": entry,
				},
			},
			ip:      "192.168.1.1",
			code:    expectedCode,
			wantErr: false,
		},
		{
			name: "fails with incorrect code",
			history: &LoginHistory{
				PendingAuthorizations: map[string]*LoginHistoryEntry{
					"192.168.1.1": entry,
				},
			},
			ip:      "192.168.1.1",
			code:    "99",
			wantErr: true,
		},
		{
			name: "fails with no pending authorization",
			history: &LoginHistory{
				PendingAuthorizations: map[string]*LoginHistoryEntry{},
			},
			ip:      "192.168.1.1",
			code:    expectedCode,
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.history.AuthorizeIPWithCode(tt.ip, tt.code)
			if (err != nil) != tt.wantErr {
				t.Errorf("LoginHistory.AuthorizeIPWithCode() error = %v, wantErr %v", err, tt.wantErr)
			}

			if !tt.wantErr {
				// Should be authorized now
				if !tt.history.IsAuthorizedIP(tt.ip) {
					t.Error("IP should be authorized after successful AuthorizeIPWithCode")
				}
				// Should no longer be pending
				if tt.history.GetPendingAuthorizationIP(tt.ip) != nil {
					t.Error("IP should not be pending after successful AuthorizeIPWithCode")
				}
			}
		})
	}
}

func TestLoginHistory_LastSeen(t *testing.T) {
	now := time.Now()
	entry1 := &LoginHistoryEntry{UpdatedAt: now.Add(-time.Hour)}
	entry2 := &LoginHistoryEntry{UpdatedAt: now.Add(-time.Minute)}
	entry3 := &LoginHistoryEntry{UpdatedAt: now.Add(-time.Second)}

	tests := []struct {
		name    string
		history *LoginHistory
		want    time.Time
	}{
		{
			name: "returns most recent UpdatedAt",
			history: &LoginHistory{
				History: map[string]*LoginHistoryEntry{
					"key1": entry1,
					"key2": entry2,
					"key3": entry3,
				},
			},
			want: entry3.UpdatedAt,
		},
		{
			name: "returns zero time for empty history",
			history: &LoginHistory{
				History: map[string]*LoginHistoryEntry{},
			},
			want: time.Time{},
		},
		{
			name:    "returns zero time for nil history",
			history: &LoginHistory{},
			want:    time.Time{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.history.LastSeen()
			if !got.Equal(tt.want) {
				t.Errorf("LoginHistory.LastSeen() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestLoginHistory_GetXPI(t *testing.T) {
	now := time.Now()
	xpid := evr.EvrId{PlatformCode: evr.OVR, AccountId: 12345}

	tests := []struct {
		name      string
		history   *LoginHistory
		xpid      evr.EvrId
		wantTime  time.Time
		wantFound bool
	}{
		{
			name: "returns time for existing XPI",
			history: &LoginHistory{
				XPIs: map[string]time.Time{
					xpid.String(): now,
				},
			},
			xpid:      xpid,
			wantTime:  now,
			wantFound: true,
		},
		{
			name: "returns false for non-existent XPI",
			history: &LoginHistory{
				XPIs: map[string]time.Time{},
			},
			xpid:      xpid,
			wantTime:  time.Time{},
			wantFound: false,
		},
		{
			name:      "returns false when XPIs is nil",
			history:   &LoginHistory{},
			xpid:      xpid,
			wantTime:  time.Time{},
			wantFound: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotTime, gotFound := tt.history.GetXPI(tt.xpid)
			if !gotTime.Equal(tt.wantTime) {
				t.Errorf("LoginHistory.GetXPI() time = %v, want %v", gotTime, tt.wantTime)
			}
			if gotFound != tt.wantFound {
				t.Errorf("LoginHistory.GetXPI() found = %v, want %v", gotFound, tt.wantFound)
			}
		})
	}
}

func TestLoginHistory_SearchPatterns(t *testing.T) {
	entry1 := &LoginHistoryEntry{
		XPID:     evr.EvrId{PlatformCode: evr.OVR, AccountId: 12345},
		ClientIP: "192.168.1.1",
		LoginData: &evr.LoginProfile{
			HMDSerialNumber: "ABC123",
		},
	}
	entry2 := &LoginHistoryEntry{
		XPID:     evr.EvrId{PlatformCode: evr.STM, AccountId: 67890},
		ClientIP: "192.168.1.2",
		LoginData: &evr.LoginProfile{
			HMDSerialNumber: "XYZ789",
		},
	}

	tests := []struct {
		name    string
		history *LoginHistory
		want    []string
	}{
		{
			name: "returns unique patterns from history",
			history: &LoginHistory{
				History: map[string]*LoginHistoryEntry{
					"key1": entry1,
					"key2": entry2,
				},
			},
			want: []string{"192.168.1.1", "ABC123", "OVR-12345", "192.168.1.2", "XYZ789", "STM-67890"},
		},
		{
			name: "handles duplicate patterns",
			history: &LoginHistory{
				History: map[string]*LoginHistoryEntry{
					"key1": entry1,
					"key2": entry1, // Same entry, should deduplicate
				},
			},
			want: []string{"192.168.1.1", "ABC123", "OVR-12345"},
		},
		{
			name: "returns empty for empty history",
			history: &LoginHistory{
				History: map[string]*LoginHistoryEntry{},
			},
			want: nil, // SearchPatterns returns nil for empty results, not empty slice
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.history.SearchPatterns()
			
			// Sort both slices for comparison since order may vary
			slices.Sort(got)
			slices.Sort(tt.want)
			
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("LoginHistory.SearchPatterns() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestLoginHistory_AlternateIDs(t *testing.T) {
	tests := []struct {
		name            string
		history         *LoginHistory
		wantFirstDegree []string
		wantSecondDegree []string
	}{
		{
			name: "returns sorted alternate IDs",
			history: &LoginHistory{
				AlternateMatches: map[string][]*AlternateSearchMatch{
					"user1": {{OtherUserID: "user1"}},
					"user3": {{OtherUserID: "user3"}},
				},
				SecondDegreeAlternates: []string{"user2", "user4", "user1"}, // user1 should be filtered out
			},
			wantFirstDegree:  []string{"user1", "user3"},
			wantSecondDegree: []string{"user2", "user4"},
		},
		{
			name: "returns empty for no alternates",
			history: &LoginHistory{
				AlternateMatches:       map[string][]*AlternateSearchMatch{},
				SecondDegreeAlternates: []string{},
			},
			wantFirstDegree:  nil,
			wantSecondDegree: nil,
		},
		{
			name:             "returns nil for nil maps",
			history:          &LoginHistory{},
			wantFirstDegree:  nil,
			wantSecondDegree: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotFirst, gotSecond := tt.history.AlternateIDs()
			if !reflect.DeepEqual(gotFirst, tt.wantFirstDegree) {
				t.Errorf("LoginHistory.AlternateIDs() first degree = %v, want %v", gotFirst, tt.wantFirstDegree)
			}
			if !reflect.DeepEqual(gotSecond, tt.wantSecondDegree) {
				t.Errorf("LoginHistory.AlternateIDs() second degree = %v, want %v", gotSecond, tt.wantSecondDegree)
			}
		})
	}
}

func TestLoginHistory_Update(t *testing.T) {
	xpid := evr.EvrId{PlatformCode: evr.OVR, AccountId: 12345}
	ip := "192.168.1.1"
	loginData := &evr.LoginProfile{
		DisplayName:     "TestUser",
		HMDSerialNumber: "ABC123",
	}

	tests := []struct {
		name               string
		history            *LoginHistory
		xpid               evr.EvrId
		ip                 string
		loginData          *evr.LoginProfile
		isAuthenticated    bool
		wantIsNew          bool
		wantAllowed        bool
		wantHistoryEntry   bool
		wantActiveEntry    bool
		wantAuthorizedIP   bool
	}{
		{
			name:             "new authenticated login",
			history:          &LoginHistory{},
			xpid:             xpid,
			ip:               ip,
			loginData:        loginData,
			isAuthenticated:  true,
			wantIsNew:        true,
			wantAllowed:      true,
			wantHistoryEntry: true,
			wantActiveEntry:  true,
			wantAuthorizedIP: true,
		},
		{
			name:             "new unauthenticated login",
			history:          &LoginHistory{},
			xpid:             xpid,
			ip:               ip,
			loginData:        loginData,
			isAuthenticated:  false,
			wantIsNew:        false,
			wantAllowed:      false,
			wantHistoryEntry: true,
			wantActiveEntry:  false,
			wantAuthorizedIP: false,
		},
		{
			name: "login from authorized IP",
			history: &LoginHistory{
				AuthorizedIPs: map[string]time.Time{
					ip: time.Now(),
				},
			},
			xpid:             xpid,
			ip:               ip,
			loginData:        loginData,
			isAuthenticated:  false,
			wantIsNew:        false,
			wantAllowed:      true,
			wantHistoryEntry: true,
			wantActiveEntry:  false,
			wantAuthorizedIP: true,
		},
		{
			name: "login from denied IP",
			history: &LoginHistory{
				DeniedClientAddresses: []string{ip},
			},
			xpid:             xpid,
			ip:               ip,
			loginData:        loginData,
			isAuthenticated:  true, // Even if authenticated, denied IP should not be allowed
			wantIsNew:        true,
			wantAllowed:      false,
			wantHistoryEntry: true,
			wantActiveEntry:  true,
			wantAuthorizedIP: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotIsNew, gotAllowed := tt.history.Update(tt.xpid, tt.ip, tt.loginData, tt.isAuthenticated)
			
			if gotIsNew != tt.wantIsNew {
				t.Errorf("LoginHistory.Update() isNew = %v, want %v", gotIsNew, tt.wantIsNew)
			}
			if gotAllowed != tt.wantAllowed {
				t.Errorf("LoginHistory.Update() allowed = %v, want %v", gotAllowed, tt.wantAllowed)
			}

			// Check history entry was created
			if tt.wantHistoryEntry {
				if tt.history.History == nil {
					t.Error("History should be initialized")
				}
				key := loginHistoryEntryKey(tt.xpid, tt.ip)
				if _, exists := tt.history.History[key]; !exists {
					t.Error("History entry should be created")
				}
			}

			// Check active entry was created
			if tt.wantActiveEntry {
				if tt.history.Active == nil {
					t.Error("Active should be initialized")
				}
				key := loginHistoryEntryKey(tt.xpid, tt.ip)
				if _, exists := tt.history.Active[key]; !exists {
					t.Error("Active entry should be created")
				}
			} else if tt.history.Active != nil {
				key := loginHistoryEntryKey(tt.xpid, tt.ip)
				if _, exists := tt.history.Active[key]; exists {
					t.Error("Active entry should not be created for unauthenticated login")
				}
			}

			// Check IP authorization
			if tt.wantAuthorizedIP {
				if !tt.history.IsAuthorizedIP(tt.ip) {
					t.Error("IP should be authorized")
				}
			}
		})
	}
}

func TestLoginHistory_MarshalJSON_SizeLimit(t *testing.T) {
	history := &LoginHistory{
		userID: "test-user",
	}

	// Create a large history that exceeds 5MB
	history.History = make(map[string]*LoginHistoryEntry)
	for i := 0; i < 10000; i++ {
		key := fmt.Sprintf("key-%d", i)
		entry := &LoginHistoryEntry{
			CreatedAt: time.Now(),
			UpdatedAt: time.Now(),
			XPID:      evr.EvrId{PlatformCode: evr.OVR, AccountId: uint64(i)},
			ClientIP:  fmt.Sprintf("192.168.1.%d", i%255+1),
			LoginData: &evr.LoginProfile{
				DisplayName:     fmt.Sprintf("User-%d", i),
				HMDSerialNumber: fmt.Sprintf("HMD-%d", i),
				SystemInfo: evr.SystemInfo{
					HeadsetType:        "Test Headset",
					NetworkType:        "WiFi",
					VideoCard:          "Test GPU",
					CPUModel:           "Test CPU",
					NumPhysicalCores:   8,
					NumLogicalCores:    16,
					MemoryTotal:        16777216000,
					DedicatedGPUMemory: 10737418240,
				},
			},
		}
		history.History[key] = entry
	}

	// Marshal to JSON
	data, err := history.MarshalJSON()
	if err != nil {
		t.Fatalf("MarshalJSON() error = %v", err)
	}

	// Check size is under 5MB
	maxSize := 5 * 1024 * 1024
	if len(data) >= maxSize {
		t.Errorf("MarshalJSON() size = %d bytes, should be < %d bytes", len(data), maxSize)
	}

	// Should be valid JSON
	var result map[string]interface{}
	if err := json.Unmarshal(data, &result); err != nil {
		t.Errorf("MarshalJSON() produced invalid JSON: %v", err)
	}
}

func TestLoginHistory_MarshalJSON_ErrorWithoutUserID(t *testing.T) {
	history := &LoginHistory{} // No userID set

	_, err := history.MarshalJSON()
	if err == nil {
		t.Error("MarshalJSON() should return error when userID is empty")
	}
	if !strings.Contains(err.Error(), "missing user ID") {
		t.Errorf("MarshalJSON() error = %v, should contain 'missing user ID'", err)
	}
}

func TestLoginHistory_NotifyGroup(t *testing.T) {
	now := time.Now()
	threshold := now.Add(-time.Hour)

	tests := []struct {
		name      string
		history   *LoginHistory
		groupID   string
		threshold time.Time
		want      bool
	}{
		{
			name: "notifies group for new alternates",
			history: &LoginHistory{
				AlternateMatches: map[string][]*AlternateSearchMatch{
					"user1": {{OtherUserID: "user1"}},
					"user2": {{OtherUserID: "user2"}},
				},
			},
			groupID:   "group1",
			threshold: threshold,
			want:      true,
		},
		{
			name: "does not notify if already notified within threshold",
			history: &LoginHistory{
				AlternateMatches: map[string][]*AlternateSearchMatch{
					"user1": {{OtherUserID: "user1"}},
				},
				GroupNotifications: map[string]map[string]time.Time{
					"group1": {
						"user1": now.Add(-30 * time.Minute), // Within threshold
					},
				},
			},
			groupID:   "group1",
			threshold: threshold,
			want:      false,
		},
		{
			name: "notifies if last notification was before threshold",
			history: &LoginHistory{
				AlternateMatches: map[string][]*AlternateSearchMatch{
					"user1": {{OtherUserID: "user1"}},
				},
				GroupNotifications: map[string]map[string]time.Time{
					"group1": {
						"user1": now.Add(-2 * time.Hour), // Before threshold
					},
				},
			},
			groupID:   "group1",
			threshold: threshold,
			want:      true,
		},
		{
			name: "returns false for no alternates",
			history: &LoginHistory{
				AlternateMatches:       map[string][]*AlternateSearchMatch{},
				SecondDegreeAlternates: []string{},
			},
			groupID:   "group1",
			threshold: threshold,
			want:      false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.history.NotifyGroup(tt.groupID, tt.threshold)
			if got != tt.want {
				t.Errorf("LoginHistory.NotifyGroup() = %v, want %v", got, tt.want)
			}

			if tt.want {
				// Check that GroupNotifications was updated
				if tt.history.GroupNotifications == nil {
					t.Error("GroupNotifications should be initialized")
				}
				if _, exists := tt.history.GroupNotifications[tt.groupID]; !exists {
					t.Error("Group should be added to GroupNotifications")
				}
			}
		})
	}
}
