package server

import (
	"sync"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/heroiclabs/nakama/v3/server/evr"
	"github.com/stretchr/testify/assert"
)

// TestServerExcluded covers the predicate that drops a game server from
// selection when its IP or username is in any exclude list — including the
// per-allocation blacklist union built from all matched players.
func TestServerExcluded(t *testing.T) {
	t.Parallel()

	global := []string{"banned-host"}
	blacklistUnion := []string{"10.0.0.5", "10.0.0.6"}

	cases := []struct {
		name     string
		extIP    string
		username string
		want     bool
	}{
		{"clean server selectable", "10.0.0.1", "good-host", false},
		{"server blacklisted by a matched player excluded", "10.0.0.5", "good-host", true},
		{"second blacklisted ip excluded", "10.0.0.6", "good-host", true},
		{"globally excluded username excluded", "10.0.0.1", "banned-host", true},
		{"empty exclude lists select everything", "10.0.0.1", "good-host", false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := serverExcluded(tc.extIP, tc.username, global, blacklistUnion)
			if got != tc.want {
				t.Fatalf("serverExcluded(%q,%q) = %v, want %v", tc.extIP, tc.username, got, tc.want)
			}
		})
	}
}

func TestSortGameServerIPs(t *testing.T) {
	tests := []struct {
		name     string
		entrants []*MatchmakerEntry
		expected []string
	}{
		{
			// rankEndpointsByServerScore reads the "any" Properties map and keys
			// prefixed with RTTPropertyPrefix ("rtt_"); it returns the stripped
			// external IPs ordered by server score. VRMLServerScore compares two
			// teams, so a scored endpoint needs at least two entrants.
			name: "Single endpoint",
			entrants: []*MatchmakerEntry{
				{
					Presence:   &MatchmakerPresence{UserId: "u1"},
					Properties: map[string]any{"rtt_ip1": 100.0},
				},
				{
					Presence:   &MatchmakerPresence{UserId: "u2"},
					Properties: map[string]any{"rtt_ip1": 100.0},
				},
			},
			expected: []string{"ip1"},
		},
		{
			name: "Multiple endpoints with different latencies",
			entrants: []*MatchmakerEntry{
				{
					Presence:   &MatchmakerPresence{UserId: "u1"},
					Properties: map[string]any{"rtt_ip1": 100.0, "rtt_ip2": 200.0},
				},
				{
					Presence:   &MatchmakerPresence{UserId: "u2"},
					Properties: map[string]any{"rtt_ip1": 150.0, "rtt_ip2": 250.0},
				},
			},
			// Endpoints are returned ordered by ascending VRML server score; this
			// is the function's deterministic ranking for the two endpoints.
			expected: []string{"ip2", "ip1"},
		},
		{
			name: "No latencies",
			entrants: []*MatchmakerEntry{
				{
					Presence:   &MatchmakerPresence{UserId: "u1"},
					Properties: map[string]any{},
				},
				{
					Presence:   &MatchmakerPresence{UserId: "u2"},
					Properties: map[string]any{},
				},
			},
			expected: []string{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			lb := &LobbyBuilder{}
			result := lb.rankEndpointsByServerScore(tt.entrants)
			assert.Equal(t, tt.expected, result)
		})
	}
}
func TestGroupByTicket(t *testing.T) {
	tests := []struct {
		name     string
		entrants []*MatchmakerEntry
		expected [][]*MatchmakerEntry
	}{
		{
			name: "Single entrant with one ticket",
			entrants: []*MatchmakerEntry{
				{
					Ticket: "ticket1",
				},
			},
			expected: [][]*MatchmakerEntry{
				{
					{
						Ticket: "ticket1",
					},
				},
			},
		},
		{
			name: "Multiple entrants with different tickets",
			entrants: []*MatchmakerEntry{
				{
					Ticket: "ticket1",
				},
				{
					Ticket: "ticket2",
				},
			},
			expected: [][]*MatchmakerEntry{
				{
					{
						Ticket: "ticket1",
					},
				},
				{
					{
						Ticket: "ticket2",
					},
				},
			},
		},
		{
			name: "Multiple entrants with same ticket",
			entrants: []*MatchmakerEntry{
				{
					Ticket: "ticket1",
				},
				{
					Ticket: "ticket1",
				},
			},
			expected: [][]*MatchmakerEntry{
				{
					{
						Ticket: "ticket1",
					},
					{
						Ticket: "ticket1",
					},
				},
			},
		},
		{
			name: "No tickets",
			entrants: []*MatchmakerEntry{
				{
					Ticket: "",
				},
			},
			expected: [][]*MatchmakerEntry{
				{
					{
						Ticket: "",
					},
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			lb := &LobbyBuilder{}
			result := lb.groupByTicket(tt.entrants)
			assert.Equal(t, tt.expected, result)
		})
	}
}
func TestSortLabelIndexes(t *testing.T) {
	tests := []struct {
		name     string
		labels   []labelIndex
		expected []labelIndex
	}{
		{
			name: "Sort by regionMatches",
			labels: []labelIndex{
				{IsRegionMatch: false},
				{IsRegionMatch: true},
			},
			expected: []labelIndex{
				{IsRegionMatch: true},
				{IsRegionMatch: false},
			},
		},
		{
			name: "Sort by rtt",
			labels: []labelIndex{
				{RTT: 100},
				{RTT: 30},
			},
			expected: []labelIndex{
				{RTT: 30},
				{RTT: 100},
			},
		},
		{
			name: "Sort by rating",
			labels: []labelIndex{
				{Rating: 50},
				{Rating: 100},
				{Rating: 25},
				{Rating: 75},
			},
			expected: []labelIndex{
				{Rating: 100},
				{Rating: 75},
				{Rating: 50},
				{Rating: 25},
			},
		},
		{
			name: "Sort by isPriorityForMode",
			labels: []labelIndex{
				{IsPriorityForMode: false},
				{IsPriorityForMode: true},
			},
			expected: []labelIndex{
				{IsPriorityForMode: true},
				{IsPriorityForMode: false},
			},
		},
		{
			name: "Sort by isHighLatency",
			labels: []labelIndex{
				{IsHighLatency: false},
				{IsHighLatency: true},
				{IsHighLatency: false},
				{IsHighLatency: true},
				{IsHighLatency: false},
			},
			expected: []labelIndex{
				{IsHighLatency: false},
				{IsHighLatency: false},
				{IsHighLatency: false},
				{IsHighLatency: true},
				{IsHighLatency: true},
			},
		},
		{
			name: "Sort by isReachable",
			labels: []labelIndex{
				{IsReachable: false},
				{IsReachable: true},
			},
			expected: []labelIndex{
				{IsReachable: true},
				{IsReachable: false},
			},
		},
		{
			name: "Sort by activeCount",
			labels: []labelIndex{
				{ActiveCount: 2},
				{ActiveCount: 1},
			},
			expected: []labelIndex{
				{ActiveCount: 1},
				{ActiveCount: 2},
			},
		},
		{
			name: "Sort by activeCount and rtt",
			labels: []labelIndex{
				{IsRegionMatch: true, RTT: 50, IsReachable: true, ActiveCount: 2},
				{IsRegionMatch: true, RTT: 40, IsReachable: true, ActiveCount: 3},
				{IsRegionMatch: true, RTT: 55, IsReachable: true, ActiveCount: 2},
				{IsRegionMatch: true, RTT: 70, IsReachable: true, ActiveCount: 1},
			},
			expected: []labelIndex{
				{IsRegionMatch: true, RTT: 70, IsReachable: true, ActiveCount: 1},
				{IsRegionMatch: true, RTT: 50, IsReachable: true, ActiveCount: 2},
				{IsRegionMatch: true, RTT: 55, IsReachable: true, ActiveCount: 2},
				{IsRegionMatch: true, RTT: 40, IsReachable: true, ActiveCount: 3},
			},
		},
		{
			name: "Complex sorting",
			labels: []labelIndex{
				{IsRegionMatch: true, RTT: 100, IsPriorityForMode: false, IsReachable: false, ActiveCount: 2},
				{IsRegionMatch: true, RTT: 50, IsPriorityForMode: true, IsReachable: true, ActiveCount: 1},
				{IsRegionMatch: true, RTT: 50, IsPriorityForMode: true, IsReachable: true, ActiveCount: 2},
				{IsRegionMatch: true, RTT: 100, IsPriorityForMode: false, IsReachable: false, ActiveCount: 1},
			},
			expected: []labelIndex{
				{IsRegionMatch: true, RTT: 50, IsPriorityForMode: true, IsReachable: true, ActiveCount: 1},
				{IsRegionMatch: true, RTT: 50, IsPriorityForMode: true, IsReachable: true, ActiveCount: 2},
				{IsRegionMatch: true, RTT: 100, IsPriorityForMode: false, IsReachable: false, ActiveCount: 1},
				{IsRegionMatch: true, RTT: 100, IsPriorityForMode: false, IsReachable: false, ActiveCount: 2},
			},
		},
		{
			name: "RTT difference less than 30",
			labels: []labelIndex{
				{RTT: 60},
				{RTT: 50},
			},
			expected: []labelIndex{
				{RTT: 60},
				{RTT: 50},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			labelsCopy := make([]labelIndex, len(tt.labels))
			copy(labelsCopy, tt.labels)

			sortLabelIndexes(labelsCopy)
			if diff := cmp.Diff(labelsCopy, tt.expected); diff != "" {
				t.Errorf("sortLabelIndexes() mismatch (-got +want):\n%s", diff)
			}
		})
	}
}

// TestSelectNextMapCombatRandomizes verifies that selectNextMap returns a valid
// combat level on the first call (i.e. the queue is populated rather than
// short-circuited to LevelUnspecified).
//
// Bug: selectNextMap returns evr.LevelUnspecified on the first call for any mode
// because the early-exit at line 648 triggers when mapQueue[mode] has not yet
// been written (key absent → ok=false), before the queue-refill block at line 654
// can run. The queue is never seeded, so every call returns LevelUnspecified.
func TestSelectNextMapCombatRandomizes(t *testing.T) {
	lb := &LobbyBuilder{
		mapQueue: make(map[evr.Symbol][]evr.Symbol),
	}

	validLevels := evr.LevelsByMode[evr.ModeCombatPublic]
	if len(validLevels) == 0 {
		t.Fatal("LevelsByMode[ModeCombatPublic] is empty — test precondition violated")
	}

	// First call: queue is empty (key not present). Should still return a valid level.
	level := lb.selectNextMap(evr.ModeCombatPublic)
	assert.NotEqual(t, evr.LevelUnspecified, level,
		"selectNextMap returned LevelUnspecified on first call; queue was never seeded")
	assert.Contains(t, validLevels, level,
		"selectNextMap returned a level not in LevelsByMode[ModeCombatPublic]")

	// Call enough times to exercise the full queue rotation. With 4 combat maps
	// the queue refills every 4 calls; run 20 iterations to cover multiple cycles.
	seen := map[evr.Symbol]struct{}{level: {}}
	for i := 1; i < 20; i++ {
		got := lb.selectNextMap(evr.ModeCombatPublic)
		assert.NotEqual(t, evr.LevelUnspecified, got,
			"selectNextMap returned LevelUnspecified on call %d", i+1)
		assert.Contains(t, validLevels, got,
			"selectNextMap returned an invalid level on call %d: %v", i+1, got)
		seen[got] = struct{}{}
	}

	// Regression: verify the function actually rotates through distinct levels.
	// With 4 combat maps and 20 calls, at least 2 distinct levels must appear.
	assert.Greater(t, len(seen), 1,
		"selectNextMap returned the same level on all 20 calls; queue rotation is broken")
}

// TestSelectNextMapConcurrentNoRace exercises selectNextMap from multiple
// goroutines simultaneously, which is exactly what happens in production:
// handleMatchedEntries (via the fire-and-forget `go m.matchedEntriesFn` at
// matchmaker.go:598) and runPostMatchmakerBackfill (via `go
// b.runPostMatchmakerBackfill` at evr_lobby_builder.go:105, plus the periodic
// backfill goroutine) both call buildMatch -> selectNextMap on the SAME
// LobbyBuilder.mapQueue map with no synchronization.
//
// Without locking inside selectNextMap this test fails under `go test -race`
// with a concurrent map read/write data race (and can panic with "concurrent
// map writes"). After adding b.mapQueueMu around the map access it passes.
func TestSelectNextMapConcurrentNoRace(t *testing.T) {
	lb := &LobbyBuilder{
		mapQueue: make(map[evr.Symbol][]evr.Symbol),
	}

	modes := []evr.Symbol{evr.ModeCombatPublic, evr.ModeArenaPublic}

	const goroutines = 16
	const iterations = 200

	var wg sync.WaitGroup
	wg.Add(goroutines)
	for g := 0; g < goroutines; g++ {
		go func(g int) {
			defer wg.Done()
			mode := modes[g%len(modes)]
			for i := 0; i < iterations; i++ {
				level := lb.selectNextMap(mode)
				_ = level
			}
		}(g)
	}
	wg.Wait()
}

// TestRankEndpointsByAverageLatency_SymmetricRTT_UsesMean verifies that when all
// entrants have RTT values within the MaxServerRTT threshold, the mean RTT is used
// as the score for each endpoint.
func TestRankEndpointsByAverageLatency_SymmetricRTT_UsesMean(t *testing.T) {
	// Setup: Save original settings and restore after test
	originalSettings := ServiceSettings()
	defer ServiceSettingsUpdate(originalSettings)

	// Configure MaxServerRTT threshold
	newSettings := &ServiceSettingsData{
		Matchmaking: GlobalMatchmakingSettings{
			MaxServerRTT: 180,
		},
	}
	ServiceSettingsUpdate(newSettings)

	// Create test data: 2 entrants, both with ~50ms RTT to server
	entrants := []*MatchmakerEntry{
		{
			Ticket: "ticket1",
			Presence: &MatchmakerPresence{
				UserId: "user1",
			},
			Properties: map[string]any{
				"rtt_1.2.3.4": 50.0,
			},
		},
		{
			Ticket: "ticket1",
			Presence: &MatchmakerPresence{
				UserId: "user2",
			},
			Properties: map[string]any{
				"rtt_1.2.3.4": 50.0,
			},
		},
	}

	lb := &LobbyBuilder{}
	meanRTTByExtIP, _ := lb.rankEndpointsByAverageLatency(entrants)

	// Assert: result should be mean (50), not max
	assert.Equal(t, 50, meanRTTByExtIP["1.2.3.4"],
		"Expected mean RTT of 50ms for symmetric latencies")
}

// TestRankEndpointsByAverageLatency_AsymmetricRTT_OverThreshold_UsesMax verifies that
// when any entrant exceeds MaxServerRTT, the max RTT is used as the score instead of mean.
// This prevents selecting servers where one player would fail pre-join validation.
func TestRankEndpointsByAverageLatency_AsymmetricRTT_OverThreshold_UsesMax(t *testing.T) {
	// Setup: Save original settings and restore after test
	originalSettings := ServiceSettings()
	defer ServiceSettingsUpdate(originalSettings)

	// Configure MaxServerRTT threshold to 180ms
	newSettings := &ServiceSettingsData{
		Matchmaking: GlobalMatchmakingSettings{
			MaxServerRTT: 180,
		},
	}
	ServiceSettingsUpdate(newSettings)

	// Create test data: 2 entrants with asymmetric RTT [50ms, 250ms]
	// Mean would be 150ms (looks acceptable) but max is 250ms (exceeds 180ms threshold)
	entrants := []*MatchmakerEntry{
		{
			Ticket: "ticket1",
			Presence: &MatchmakerPresence{
				UserId: "user1",
			},
			Properties: map[string]any{
				"rtt_1.2.3.4": 50.0,
			},
		},
		{
			Ticket: "ticket1",
			Presence: &MatchmakerPresence{
				UserId: "user2",
			},
			Properties: map[string]any{
				"rtt_1.2.3.4": 250.0,
			},
		},
	}

	lb := &LobbyBuilder{}
	meanRTTByExtIP, _ := lb.rankEndpointsByAverageLatency(entrants)

	// Assert: result should be max (250), not mean (150)
	// This ensures the server is penalized for the outlier latency
	assert.Equal(t, 250, meanRTTByExtIP["1.2.3.4"],
		"Expected max RTT of 250ms when any entrant exceeds threshold; mean-based selection would hide outlier")
}

// TestRankEndpointsByAverageLatency_AsymmetricRTT_UnderThreshold_UsesMean verifies that
// when all entrants are below MaxServerRTT threshold, the mean is used even if there is asymmetry.
func TestRankEndpointsByAverageLatency_AsymmetricRTT_UnderThreshold_UsesMean(t *testing.T) {
	// Setup: Save original settings and restore after test
	originalSettings := ServiceSettings()
	defer ServiceSettingsUpdate(originalSettings)

	// Configure MaxServerRTT threshold to 180ms
	newSettings := &ServiceSettingsData{
		Matchmaking: GlobalMatchmakingSettings{
			MaxServerRTT: 180,
		},
	}
	ServiceSettingsUpdate(newSettings)

	// Create test data: 2 entrants with asymmetric RTT [50ms, 150ms]
	// Mean is 100ms, max is 150ms, both under 180ms threshold
	entrants := []*MatchmakerEntry{
		{
			Ticket: "ticket1",
			Presence: &MatchmakerPresence{
				UserId: "user1",
			},
			Properties: map[string]any{
				"rtt_1.2.3.4": 50.0,
			},
		},
		{
			Ticket: "ticket1",
			Presence: &MatchmakerPresence{
				UserId: "user2",
			},
			Properties: map[string]any{
				"rtt_1.2.3.4": 150.0,
			},
		},
	}

	lb := &LobbyBuilder{}
	meanRTTByExtIP, _ := lb.rankEndpointsByAverageLatency(entrants)

	// Assert: result should be mean (100), not max (150)
	// All entrants are within acceptable threshold, so mean provides better scoring
	assert.Equal(t, 100, meanRTTByExtIP["1.2.3.4"],
		"Expected mean RTT of 100ms when all entrants are below threshold")
}

// TestRankEndpointsByAverageLatency_MultipleServers_MixedThresholds verifies the fix works
// correctly across multiple servers with mixed threshold behaviors.
func TestRankEndpointsByAverageLatency_MultipleServers_MixedThresholds(t *testing.T) {
	// Setup: Save original settings and restore after test
	originalSettings := ServiceSettings()
	defer ServiceSettingsUpdate(originalSettings)

	// Configure MaxServerRTT threshold to 180ms
	newSettings := &ServiceSettingsData{
		Matchmaking: GlobalMatchmakingSettings{
			MaxServerRTT: 180,
		},
	}
	ServiceSettingsUpdate(newSettings)

	// Create test data: 2 entrants, 2 servers
	// Server A (1.1.1.1): entrant1=40ms, entrant2=60ms → both under, use mean=50
	// Server B (2.2.2.2): entrant1=50ms, entrant2=300ms → one over, use max=300
	entrants := []*MatchmakerEntry{
		{
			Ticket: "ticket1",
			Presence: &MatchmakerPresence{
				UserId: "user1",
			},
			Properties: map[string]any{
				"rtt_1.1.1.1": 40.0,
				"rtt_2.2.2.2": 50.0,
			},
		},
		{
			Ticket: "ticket1",
			Presence: &MatchmakerPresence{
				UserId: "user2",
			},
			Properties: map[string]any{
				"rtt_1.1.1.1": 60.0,
				"rtt_2.2.2.2": 300.0,
			},
		},
	}

	lb := &LobbyBuilder{}
	meanRTTByExtIP, _ := lb.rankEndpointsByAverageLatency(entrants)

	// Assert server A uses mean (both under threshold)
	assert.Equal(t, 50, meanRTTByExtIP["1.1.1.1"],
		"Expected mean RTT of 50ms for server A (both entrants under threshold)")

	// Assert server B uses max (one over threshold)
	assert.Equal(t, 300, meanRTTByExtIP["2.2.2.2"],
		"Expected max RTT of 300ms for server B (one entrant exceeds threshold)")
}
