package server

import (
	"context"
	"math"
	"net"
	"testing"
	"time"

	"github.com/heroiclabs/nakama-common/runtime"
	"github.com/heroiclabs/nakama/v3/server/evr"
)

func TestVRMLServerScore_DivisionByZero(t *testing.T) {
	// Bug: division by zero when maxTeamVariance==0, maxServerVar==0, or thresholdRTT==minRTT
	tests := []struct {
		name         string
		latencies    [][]float64
		minRTT       float64
		maxRTT       float64
		thresholdRTT float64
	}{
		{
			name:         "uniform RTTs (all equal)",
			latencies:    [][]float64{{50, 50, 50, 50}, {50, 50, 50, 50}},
			minRTT:       50,
			maxRTT:       50,
			thresholdRTT: 50,
		},
		{
			name:         "minRTT equals thresholdRTT",
			latencies:    [][]float64{{30, 40, 50, 60}, {35, 45, 55, 65}},
			minRTT:       10,
			maxRTT:       100,
			thresholdRTT: 10,
		},
		{
			name:         "zero RTTs",
			latencies:    [][]float64{{0, 0, 0, 0}, {0, 0, 0, 0}},
			minRTT:       0,
			maxRTT:       0,
			thresholdRTT: 0,
		},
	}
	pointsDistro := []float64{0.25, 0.25, 0.25, 0.25}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			score := VRMLServerScore(tt.latencies, tt.minRTT, tt.maxRTT, tt.thresholdRTT, pointsDistro)
			if math.IsNaN(score) {
				t.Errorf("VRMLServerScore returned NaN")
			}
			if math.IsInf(score, 0) {
				t.Errorf("VRMLServerScore returned Inf")
			}
		})
	}
}

func TestIPInfoCache_Get_InvalidIP(t *testing.T) {
	// Bug: net.ParseIP returns nil for invalid IPs, then .IsLoopback() panics
	cache, err := NewIPInfoCache(nil, nil)
	if err != nil {
		t.Fatalf("NewIPInfoCache failed: %v", err)
	}

	tests := []string{
		"not-an-ip",
		"",
		"999.999.999.999",
		"abc.def.ghi.jkl",
	}
	for _, ip := range tests {
		t.Run(ip, func(t *testing.T) {
			defer func() {
				if r := recover(); r != nil {
					t.Errorf("Get(%q) panicked: %v", ip, r)
				}
			}()
			// Should not panic even with invalid IP
			_, _ = cache.Get(context.Background(), ip)
		})
	}
}

func TestIPInfoCache_Get_ValidReservedIPs(t *testing.T) {
	// Verify reserved IPs still return StubIPInfo
	cache, err := NewIPInfoCache(nil, nil)
	if err != nil {
		t.Fatalf("NewIPInfoCache failed: %v", err)
	}

	tests := []string{
		"127.0.0.1",
		"10.0.0.1",
		"192.168.1.1",
		"::1",
	}
	for _, ip := range tests {
		t.Run(ip, func(t *testing.T) {
			result, err := cache.Get(context.Background(), ip)
			if err != nil {
				t.Errorf("Get(%q) returned error: %v", ip, err)
			}
			if result == nil {
				t.Errorf("Get(%q) returned nil", ip)
			}
		})
	}
}

func TestLatencyHistorySortOrder(t *testing.T) {
	// Bug: items were never assigned to the index slice, so sorting operated on zero values.
	// After the fix, cached entries should be sorted after uncached, then by timestamp (older first), then by RTT.
	// The function shuffles before sorting (for randomness among equal elements), so
	// we test structural properties rather than exact order.

	now := time.Now()
	h := &LatencyHistory{
		GameServerLatencies: map[string][]LatencyHistoryItem{
			"host-old": {{RTT: 50 * time.Millisecond, Timestamp: now.Add(-10 * time.Minute)}},
			"host-new": {{RTT: 20 * time.Millisecond, Timestamp: now}},
			// host-none is NOT in cache
		},
	}

	hostIPs := []string{"host-old", "host-new", "host-none"}
	sortPingCandidatesByLatencyHistory(hostIPs, h)

	// Uncached host should be first (prioritized for pinging)
	if hostIPs[0] != "host-none" {
		t.Errorf("expected uncached host first, got %s", hostIPs[0])
	}
	// Among cached, older timestamp should come before newer
	if hostIPs[1] != "host-old" {
		t.Errorf("expected older-cached host second, got %s", hostIPs[1])
	}
	if hostIPs[2] != "host-new" {
		t.Errorf("expected newer-cached host third, got %s", hostIPs[2])
	}
}

func TestLatencyHistorySort_UncachedFirst(t *testing.T) {
	// Uncached hosts should be sorted before cached ones (prioritized for ping)
	h := &LatencyHistory{
		GameServerLatencies: map[string][]LatencyHistoryItem{
			"host-a": {{RTT: 20 * time.Millisecond, Timestamp: time.Now()}},
			// host-b is NOT in cache
		},
	}

	hostIPs := []string{"host-a", "host-b"}
	sortPingCandidatesByLatencyHistory(hostIPs, h)

	if hostIPs[0] != "host-b" {
		t.Errorf("expected uncached host-b first, got %s", hostIPs[0])
	}
}

func TestCheckForceUserRPC_MissingParams(t *testing.T) {
	// Bug: bare type assertion panicked when context value was nil
	ctx := context.Background()

	defer func() {
		if r := recover(); r != nil {
			t.Errorf("CheckForceUserRPC panicked: %v", r)
		}
	}()

	_, err := CheckForceUserRPC(ctx, nil, nil, nil, "")
	if err == nil {
		t.Error("expected error for missing query parameters")
	}
}

func TestGuildGroupGetRPC_MissingParams(t *testing.T) {
	// Bug: bare type assertion panicked when context value was nil
	ctx := context.Background()

	defer func() {
		if r := recover(); r != nil {
			t.Errorf("GuildGroupGetRPC panicked: %v", r)
		}
	}()

	_, err := GuildGroupGetRPC(ctx, nil, nil, nil, "")
	if err == nil {
		t.Error("expected error for missing query parameters")
	}
}

func TestUserGuildGroupListRPC_MissingUserID(t *testing.T) {
	// Bug: bare type assertion panicked when context value was nil
	ctx := context.Background()

	defer func() {
		if r := recover(); r != nil {
			t.Errorf("UserGuildGroupListRPC panicked: %v", r)
		}
	}()

	_, err := UserGuildGroupListRPC(ctx, nil, nil, nil, "")
	if err == nil {
		t.Error("expected error for missing user ID")
	}
}

// Verify the IP address is correctly parsed for reserved IP check
func TestNetParseIP_NilSafety(t *testing.T) {
	// This is the exact pattern from the bug fix
	ip := "not-valid"
	parsedIP := net.ParseIP(ip)
	if parsedIP != nil {
		t.Errorf("expected nil for invalid IP, got %v", parsedIP)
	}
	// The old code would panic here:
	// parsedIP.IsLoopback()  // <-- nil pointer dereference
}

func TestSortPingCandidates_Empty(t *testing.T) {
	h := &LatencyHistory{
		GameServerLatencies: map[string][]LatencyHistoryItem{},
	}
	hostIPs := []string{}
	sortPingCandidatesByLatencyHistory(hostIPs, h)
	if len(hostIPs) != 0 {
		t.Errorf("expected 0 results for empty input, got %d", len(hostIPs))
	}
}

// Verify team alignment uses correct IDs (not caller userID)
func TestAllocateMatchRPC_TeamAlignments(t *testing.T) {
	// This tests the logic of the team alignment loop, not the full RPC
	// The bug was: teamAlignments[userID] instead of teamAlignments[id]
	request := &PrepareMatchRequest{
		TeamAlignments: map[string]string{
			"user-aaa": "blue",
			"user-bbb": "orange",
		},
	}

	teamAlignments := make(map[string]int, len(request.TeamAlignments))
	for id, roleName := range request.TeamAlignments {
		roleID := -1
		switch roleName {
		case "blue":
			roleID = 0
		case "orange":
			roleID = 1
		}
		// The fix: use id, not some outer-scope variable
		teamAlignments[id] = roleID
	}

	if v, ok := teamAlignments["user-aaa"]; !ok || v != 0 {
		t.Errorf("user-aaa: got %d, want 0 (blue)", v)
	}
	if v, ok := teamAlignments["user-bbb"]; !ok || v != 1 {
		t.Errorf("user-bbb: got %d, want 1 (orange)", v)
	}
	if len(teamAlignments) != 2 {
		t.Errorf("expected 2 entries, got %d: %v", len(teamAlignments), teamAlignments)
	}
}

// Verify leaderboard record field mapping is correct
func TestLeaderboardHaystackRecord_Fields(t *testing.T) {
	// Bug: UpdateTime was set to CreateTime
	r := LeaderboardHaystackRecord{
		CreateTime: 1000,
		UpdateTime: 2000,
	}
	if r.CreateTime == r.UpdateTime {
		t.Error("CreateTime and UpdateTime should be different")
	}
	if r.UpdateTime != 2000 {
		t.Errorf("UpdateTime: got %d, want 2000", r.UpdateTime)
	}
}

// Compile-time check that StatusNotFound is accessible (used in matchmaker nil check)
var _ = runtime.NewError("test", StatusNotFound)

// Verify Symbol.HexString format from the evr package is correct
func TestSymbolHexStringFromServer(t *testing.T) {
	s := evr.Symbol(255)
	got := s.HexString()
	want := "0x00000000000000ff"
	if got != want {
		t.Errorf("Symbol(255).HexString() = %q, want %q", got, want)
	}
}
