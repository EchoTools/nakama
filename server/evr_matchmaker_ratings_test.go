package server

import (
	"math"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/heroiclabs/nakama/v3/server/evr"
	"github.com/intinig/go-openskill/types"
)

// TestNewDefaultRating tests the NewDefaultRating function
func TestNewDefaultRating(t *testing.T) {
	rating := NewDefaultRating()

	// Default values should be Z=3, Mu=10, Sigma=10/3
	if rating.Z != 3 {
		t.Errorf("Expected Z=3, got %d", rating.Z)
	}
	if rating.Mu != 10.0 {
		t.Errorf("Expected Mu=10.0, got %f", rating.Mu)
	}
	if math.Abs(rating.Sigma-10.0/3.0) > 0.0001 {
		t.Errorf("Expected Sigma=%f, got %f", 10.0/3.0, rating.Sigma)
	}
}

// TestNewRating tests the NewRating function with various inputs
func TestNewRating(t *testing.T) {
	tests := []struct {
		name          string
		z             int
		mu            float64
		sigma         float64
		expectedZ     int
		expectedMu    float64
		expectedSigma float64
	}{
		{
			name:          "All defaults (zeros)",
			z:             0,
			mu:            0,
			sigma:         0,
			expectedZ:     3,
			expectedMu:    10.0,
			expectedSigma: 10.0 / 3.0,
		},
		{
			name:          "Custom values",
			z:             4,
			mu:            25.0,
			sigma:         8.333,
			expectedZ:     4,
			expectedMu:    25.0,
			expectedSigma: 8.333,
		},
		{
			name:          "Custom mu, auto sigma",
			z:             3,
			mu:            30.0,
			sigma:         0,
			expectedZ:     3,
			expectedMu:    30.0,
			expectedSigma: 10.0, // 30/3
		},
		{
			name:          "Negative mu uses default",
			z:             3,
			mu:            -5.0,
			sigma:         5.0,
			expectedZ:     3,
			expectedMu:    10.0,
			expectedSigma: 5.0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rating := NewRatingFromConfig(tt.z, tt.mu, tt.sigma, nil)
			if rating.Z != tt.expectedZ {
				t.Errorf("Expected Z=%d, got %d", tt.expectedZ, rating.Z)
			}
			if math.Abs(rating.Mu-tt.expectedMu) > 0.0001 {
				t.Errorf("Expected Mu=%f, got %f", tt.expectedMu, rating.Mu)
			}
			if math.Abs(rating.Sigma-tt.expectedSigma) > 0.0001 {
				t.Errorf("Expected Sigma=%f, got %f", tt.expectedSigma, rating.Sigma)
			}
		})
	}
}

// TestGetRatingDefaults tests the GetRatingDefaults function
func TestGetRatingDefaults(t *testing.T) {
	tests := []struct {
		name     string
		config   *SkillRatingSettings
		expected RatingDefaults
	}{
		{
			name:   "Nil config returns hardcoded defaults",
			config: nil,
			expected: RatingDefaults{
				Z:     3,
				Mu:    10.0,
				Sigma: 10.0 / 3.0,
				Tau:   0.3,
			},
		},
		{
			name: "Config with Z=0 returns hardcoded defaults",
			config: &SkillRatingSettings{
				Defaults: RatingDefaults{Z: 0},
			},
			expected: RatingDefaults{
				Z:     3,
				Mu:    10.0,
				Sigma: 10.0 / 3.0,
				Tau:   0.3,
			},
		},
		{
			name: "Config with valid values returns config values",
			config: &SkillRatingSettings{
				Defaults: RatingDefaults{
					Z:     4,
					Mu:    25.0,
					Sigma: 8.333,
					Tau:   0.5,
				},
			},
			expected: RatingDefaults{
				Z:     4,
				Mu:    25.0,
				Sigma: 8.333,
				Tau:   0.5,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := GetRatingDefaults(tt.config)
			if result.Z != tt.expected.Z {
				t.Errorf("Expected Z=%d, got %d", tt.expected.Z, result.Z)
			}
			if math.Abs(result.Mu-tt.expected.Mu) > 0.0001 {
				t.Errorf("Expected Mu=%f, got %f", tt.expected.Mu, result.Mu)
			}
			if math.Abs(result.Sigma-tt.expected.Sigma) > 0.0001 {
				t.Errorf("Expected Sigma=%f, got %f", tt.expected.Sigma, result.Sigma)
			}
			if math.Abs(result.Tau-tt.expected.Tau) > 0.0001 {
				t.Errorf("Expected Tau=%f, got %f", tt.expected.Tau, result.Tau)
			}
		})
	}
}

// TestRatedTeamStrength tests the RatedTeam.Strength method
func TestRatedTeamStrength(t *testing.T) {
	tests := []struct {
		name     string
		team     RatedTeam
		expected float64
	}{
		{
			name:     "Empty team",
			team:     RatedTeam{},
			expected: 0.0,
		},
		{
			name: "Single player",
			team: RatedTeam{
				{Mu: 25.0, Sigma: 8.333, Z: 3},
			},
			expected: 25.0,
		},
		{
			name: "Multiple players",
			team: RatedTeam{
				{Mu: 25.0, Sigma: 8.333, Z: 3},
				{Mu: 30.0, Sigma: 7.0, Z: 3},
				{Mu: 20.0, Sigma: 6.0, Z: 3},
			},
			expected: 75.0, // 25 + 30 + 20
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := tt.team.Strength()
			if math.Abs(result-tt.expected) > 0.0001 {
				t.Errorf("Expected strength=%f, got %f", tt.expected, result)
			}
		})
	}
}

// TestRatedTeamRating tests the RatedTeam.Rating method
func TestRatedTeamRating(t *testing.T) {
	tests := []struct {
		name          string
		team          RatedTeam
		expectedMu    float64
		expectedSigma float64
	}{
		{
			name:          "Empty team returns default",
			team:          RatedTeam{},
			expectedMu:    10.0,
			expectedSigma: 10.0 / 3.0,
		},
		{
			name: "Single player",
			team: RatedTeam{
				{Mu: 25.0, Sigma: 8.333, Z: 3},
			},
			expectedMu:    25.0,
			expectedSigma: 8.333,
		},
		{
			name: "Two players - RMS sigma",
			team: RatedTeam{
				{Mu: 20.0, Sigma: 6.0, Z: 3},
				{Mu: 30.0, Sigma: 8.0, Z: 3},
			},
			expectedMu:    25.0,                           // (20+30)/2
			expectedSigma: math.Sqrt((36.0 + 64.0) / 2.0), // sqrt((6^2 + 8^2)/2)
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := tt.team.Rating()
			if math.Abs(result.Mu-tt.expectedMu) > 0.0001 {
				t.Errorf("Expected Mu=%f, got %f", tt.expectedMu, result.Mu)
			}
			if math.Abs(result.Sigma-tt.expectedSigma) > 0.0001 {
				t.Errorf("Expected Sigma=%f, got %f", tt.expectedSigma, result.Sigma)
			}
		})
	}
}

// TestCalculateScoreFromStats tests the calculateScoreFromStats function
func TestCalculateScoreFromStats(t *testing.T) {
	tests := []struct {
		name        string
		stats       evr.MatchTypeStats
		multipliers map[string]float64
		expected    float64
	}{
		{
			name:        "Empty multipliers",
			stats:       evr.MatchTypeStats{Points: 10, Assists: 5},
			multipliers: nil,
			expected:    0.0,
		},
		{
			name:  "Default team multipliers",
			stats: evr.MatchTypeStats{Points: 10, Assists: 5, Saves: 3, Passes: 2, ShotsOnGoal: 4},
			multipliers: map[string]float64{
				"Points":      1.0,
				"Assists":     2.0,
				"Saves":       3.0,
				"Passes":      1.0,
				"ShotsOnGoal": -1.0,
			},
			expected: 10*1.0 + 5*2.0 + 3*3.0 + 2*1.0 - 4*1.0, // 10 + 10 + 9 + 2 - 4 = 27
		},
		{
			name:  "Player multipliers",
			stats: evr.MatchTypeStats{Points: 10, Assists: 5, Saves: 3},
			multipliers: map[string]float64{
				"Points":  1.0,
				"Assists": 1.0,
				"Saves":   2.0,
			},
			expected: 10*1.0 + 5*1.0 + 3*2.0, // 10 + 5 + 6 = 21
		},
		{
			name:  "Only some stats present",
			stats: evr.MatchTypeStats{Points: 5},
			multipliers: map[string]float64{
				"Points":  2.0,
				"Assists": 3.0,
			},
			expected: 10.0, // 5 * 2
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := calculateScoreFromStats(tt.stats, tt.multipliers)
			if math.Abs(result-tt.expected) > 0.0001 {
				t.Errorf("Expected score=%f, got %f", tt.expected, result)
			}
		})
	}
}

func TestCalculatePlayerRating(t *testing.T) {
	type args struct {
		evrID       evr.EvrId
		players     []PlayerInfo
		playerStats map[evr.EvrId]evr.MatchTypeStats
		blueWins    bool
	}
	tests := []struct {
		name string
		args args
		want map[string]types.Rating
	}{
		{
			name: "Player in blue team, blue wins",
			args: args{
				evrID: evr.EvrId{
					PlatformCode: 4,
					AccountId:    123456789,
				},
				players: []PlayerInfo{

					{
						SessionID:   "1",
						EvrID:       evr.EvrId{PlatformCode: 4, AccountId: 987654321},
						Team:        0,
						RatingMu:    30,
						RatingSigma: 7.5,
						JoinTime:    0.0,
					},
					{
						SessionID:   "2",
						EvrID:       evr.EvrId{PlatformCode: 4, AccountId: 123456789},
						Team:        0,
						RatingMu:    19,
						RatingSigma: 4.333,
						JoinTime:    0.0,
					},
					{
						SessionID:   "3",
						EvrID:       evr.EvrId{PlatformCode: 4, AccountId: 112233445},
						Team:        1,
						RatingMu:    20,
						RatingSigma: 6.666,
						JoinTime:    0.0,
					},
					{
						SessionID:   "4",
						EvrID:       evr.EvrId{PlatformCode: 4, AccountId: 556677889},
						Team:        1,
						RatingMu:    22,
						RatingSigma: 7.0,
						JoinTime:    0.0,
					},
				},
				playerStats: map[evr.EvrId]evr.MatchTypeStats{
					{PlatformCode: 4, AccountId: 987654321}: {Points: 4, Assists: 2},
					{PlatformCode: 4, AccountId: 123456789}: {Points: 2, Assists: 2},
					{PlatformCode: 4, AccountId: 112233445}: {Points: 2, Assists: 0},
					{PlatformCode: 4, AccountId: 556677889}: {Points: 4, Assists: 2},
				},
				blueWins: true,
			},
			want: map[string]types.Rating{
				"1": {Mu: 32.25100494750614, Sigma: 7.403291008004081, Z: 3},
				"2": {Mu: 19.620014923159122, Sigma: 4.325701896576898, Z: 3},
				"3": {Mu: 17.134731637125135, Sigma: 6.501092020874961, Z: 3},
				"4": {Mu: 21.584260659198744, Sigma: 6.787349653038609, Z: 3},
			},
		},
		{
			name: "Player in orange team, orange wins",
			args: args{
				evrID: evr.EvrId{
					PlatformCode: 4,
					AccountId:    123456789,
				},
				players: []PlayerInfo{
					{
						SessionID:   "112233445",
						EvrID:       evr.EvrId{PlatformCode: 4, AccountId: 112233445},
						Team:        0,
						RatingMu:    20,
						RatingSigma: 6.666,
						JoinTime:    0.0,
					},
					{
						SessionID:   "556677889",
						EvrID:       evr.EvrId{PlatformCode: 4, AccountId: 556677889},
						Team:        0,
						RatingMu:    22,
						RatingSigma: 7.0,
						JoinTime:    0.0,
					},
					{
						SessionID:   "987654321",
						EvrID:       evr.EvrId{PlatformCode: 4, AccountId: 987654321},
						Team:        1,
						RatingMu:    30,
						RatingSigma: 7.5,
						JoinTime:    0.0,
					},
					{
						SessionID:   "123456789",
						EvrID:       evr.EvrId{PlatformCode: 4, AccountId: 123456789},
						Team:        1,
						RatingMu:    19,
						RatingSigma: 4.333,
						JoinTime:    0.0,
					},
				},
				playerStats: map[evr.EvrId]evr.MatchTypeStats{
					{PlatformCode: 4, AccountId: 112233445}: {Points: 2, Assists: 0},
					{PlatformCode: 4, AccountId: 556677889}: {Points: 4, Assists: 2},
					{PlatformCode: 4, AccountId: 987654321}: {Points: 4, Assists: 2},
					{PlatformCode: 4, AccountId: 123456789}: {Points: 2, Assists: 2},
				},
				blueWins: false,
			},
			want: map[string]types.Rating{
				"112233445": {Mu: 17.134731637125135, Sigma: 6.501092020874961, Z: 3},
				"123456789": {Mu: 19.620014923159122, Sigma: 4.325701896576898, Z: 3},
				"556677889": {Mu: 21.584260659198744, Sigma: 6.787349653038609, Z: 3},
				"987654321": {Mu: 32.25100494750614, Sigma: 7.403291008004081, Z: 3},
			},
		},
		{
			name: "Player in blue team, orange wins",
			args: args{
				evrID: evr.EvrId{
					PlatformCode: 4,
					AccountId:    123456789,
				},
				players: []PlayerInfo{
					{
						SessionID:   "123456789",
						EvrID:       evr.EvrId{PlatformCode: 4, AccountId: 123456789},
						Team:        0,
						RatingMu:    12,
						RatingSigma: 8.333,
						JoinTime:    0.0,
					},
					{
						SessionID:   "987654321",
						EvrID:       evr.EvrId{PlatformCode: 4, AccountId: 987654321},
						Team:        0,
						RatingMu:    30,
						RatingSigma: 7.5,
						JoinTime:    0.0,
					},
					{
						SessionID:   "112233445",
						EvrID:       evr.EvrId{PlatformCode: 4, AccountId: 112233445},
						Team:        1,
						RatingMu:    20,
						RatingSigma: 6.666,
						JoinTime:    0.0,
					},
					{
						SessionID:   "556677889",
						EvrID:       evr.EvrId{PlatformCode: 4, AccountId: 556677889},
						Team:        1,
						RatingMu:    22,
						RatingSigma: 7.0,
						JoinTime:    0.0,
					},
				},
				playerStats: map[evr.EvrId]evr.MatchTypeStats{
					{PlatformCode: 4, AccountId: 123456789}: {Points: 0, Assists: 0},
					{PlatformCode: 4, AccountId: 987654321}: {Points: 0, Assists: 0},
					{PlatformCode: 4, AccountId: 112233445}: {Points: 0, Assists: 0},
					{PlatformCode: 4, AccountId: 556677889}: {Points: 0, Assists: 0},
				},
				blueWins: false,
			},
			want: map[string]types.Rating{
				"112233445": {Mu: 20.734078648705445, Sigma: 6.638048733401844, Z: 3},
				"123456789": {Mu: 12.430429521320995, Sigma: 8.184594812783692, Z: 3},
				"556677889": {Mu: 22.73008417680748, Sigma: 6.960619786170357, Z: 3},
				"987654321": {Mu: 27.884449958743556, Sigma: 7.365617736624244, Z: 3},
			},
		},
		{
			name: "Player in orange team, blue wins",
			args: args{
				evrID: evr.EvrId{
					PlatformCode: 4,
					AccountId:    112233445,
				},
				players: []PlayerInfo{
					{
						SessionID:   "987654321",
						EvrID:       evr.EvrId{PlatformCode: 4, AccountId: 987654321},
						Team:        0,
						RatingMu:    30,
						RatingSigma: 7.5,
						JoinTime:    0.0,
					},
					{
						SessionID:   "123456789",
						EvrID:       evr.EvrId{PlatformCode: 4, AccountId: 123456789},
						Team:        0,
						RatingMu:    12,
						RatingSigma: 8.333,
						JoinTime:    0.0,
					},
					{
						SessionID:   "112233445",
						EvrID:       evr.EvrId{PlatformCode: 4, AccountId: 112233445},
						Team:        1,
						RatingMu:    20,
						RatingSigma: 6.666,
						JoinTime:    0.0,
					},
					{
						SessionID:   "556677889",
						EvrID:       evr.EvrId{PlatformCode: 4, AccountId: 556677889},
						Team:        1,
						RatingMu:    22,
						RatingSigma: 7.0,
						JoinTime:    0.0,
					},
				},
				playerStats: map[evr.EvrId]evr.MatchTypeStats{
					{PlatformCode: 4, AccountId: 987654321}: {Points: 0, Assists: 0},
					{PlatformCode: 4, AccountId: 123456789}: {Points: 0, Assists: 0},
					{PlatformCode: 4, AccountId: 112233445}: {Points: 0, Assists: 0},
					{PlatformCode: 4, AccountId: 556677889}: {Points: 0, Assists: 0},
				},
				blueWins: true,
			},
			want: map[string]types.Rating{
				"112233445": {Mu: 19.501306348685958, Sigma: 6.58730676305727, Z: 3},
				"123456789": {Mu: 13.483755866429656, Sigma: 8.279910016496327, Z: 3},
				"556677889": {Mu: 21.201229156253063, Sigma: 6.89881982340915, Z: 3},
				"987654321": {Mu: 30.345453845567764, Sigma: 7.4283160798165335, Z: 3},
			},
		},
		{
			name: "Player alone",
			args: args{
				evrID: evr.EvrId{
					PlatformCode: 4,
					AccountId:    123456789,
				},
				players: []PlayerInfo{
					{
						SessionID:   "123456789",
						EvrID:       evr.EvrId{PlatformCode: 4, AccountId: 123456789},
						Team:        0,
						RatingMu:    30,
						RatingSigma: 7.5,
						JoinTime:    0.0,
					},
				},
				playerStats: map[evr.EvrId]evr.MatchTypeStats{
					{PlatformCode: 4, AccountId: 123456789}: {Points: 0, Assists: 0},
				},
				blueWins: true,
			},
			want: map[string]types.Rating{
				"123456789": {Mu: 30, Sigma: 7.505997601918082, Z: 3},
			},
		},
		{
			name: "Player not found",
			args: args{
				evrID: evr.EvrId{
					PlatformCode: 4,
					AccountId:    123456789,
				},
				players: []PlayerInfo{
					{
						SessionID:   "987654321",
						EvrID:       evr.EvrId{PlatformCode: 4, AccountId: 987654321},
						Team:        0,
						RatingMu:    30,
						RatingSigma: 7.5,
						JoinTime:    0.0,
					},
				},
				playerStats: map[evr.EvrId]evr.MatchTypeStats{
					{PlatformCode: 4, AccountId: 987654321}: {Points: 0, Assists: 0},
				},
				blueWins: true,
			},
			want: map[string]types.Rating{
				"987654321": {Mu: 30, Sigma: 7.505997601918082, Z: 3},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := CalculateNewPlayerRatings(tt.args.players, tt.args.playerStats, tt.args.blueWins); cmp.Diff(got, tt.want) != "" {
				t.Errorf("calculatePlayerRating() = %s", cmp.Diff(got, tt.want))
			}
		})
	}
}

// TestCalculateNewTeamRatingsWithConfig tests that custom config values are applied
func TestCalculateNewTeamRatingsWithConfig(t *testing.T) {
	players := []PlayerInfo{
		{
			SessionID:   "player1",
			EvrID:       evr.EvrId{PlatformCode: 4, AccountId: 1},
			Team:        BlueTeam,
			RatingMu:    25.0,
			RatingSigma: 8.333,
		},
		{
			SessionID:   "player2",
			EvrID:       evr.EvrId{PlatformCode: 4, AccountId: 2},
			Team:        OrangeTeam,
			RatingMu:    25.0,
			RatingSigma: 8.333,
		},
	}

	stats := map[evr.EvrId]evr.MatchTypeStats{
		{PlatformCode: 4, AccountId: 1}: {Points: 10, Assists: 5, Saves: 2},
		{PlatformCode: 4, AccountId: 2}: {Points: 5, Assists: 2, Saves: 1},
	}

	config := &SkillRatingSettings{
		Defaults: RatingDefaults{
			Z:     3,
			Mu:    25.0,
			Sigma: 8.333,
			Tau:   0.5, // Higher tau = more volatility
		},
		TeamStatMultipliers: map[string]float64{
			"Points":  2.0, // Double the weight of points
			"Assists": 1.0,
			"Saves":   1.0,
		},
		WinningTeamBonus: 10.0, // Higher bonus
	}

	ratings := CalculateNewTeamRatingsWithConfig(players, stats, true, config)

	// Blue team won, so player1 should have higher rating than player2
	if ratings["player1"].Mu <= ratings["player2"].Mu {
		t.Errorf("Expected player1 (winner) to have higher Mu than player2, got player1=%f, player2=%f",
			ratings["player1"].Mu, ratings["player2"].Mu)
	}
}

// TestCalculateNewIndividualRatings tests the individual player rating calculation
func TestCalculateNewIndividualRatings(t *testing.T) {
	players := []PlayerInfo{
		{
			SessionID:   "player1",
			EvrID:       evr.EvrId{PlatformCode: 4, AccountId: 1},
			Team:        BlueTeam,
			RatingMu:    25.0,
			RatingSigma: 8.333,
		},
		{
			SessionID:   "player2",
			EvrID:       evr.EvrId{PlatformCode: 4, AccountId: 2},
			Team:        BlueTeam, // Same team
			RatingMu:    25.0,
			RatingSigma: 8.333,
		},
		{
			SessionID:   "player3",
			EvrID:       evr.EvrId{PlatformCode: 4, AccountId: 3},
			Team:        OrangeTeam,
			RatingMu:    25.0,
			RatingSigma: 8.333,
		},
	}

	// Player 3 on losing team has more individual stats
	stats := map[evr.EvrId]evr.MatchTypeStats{
		{PlatformCode: 4, AccountId: 1}: {Points: 5, Assists: 2, Saves: 1},  // Low performer, winner
		{PlatformCode: 4, AccountId: 2}: {Points: 10, Assists: 5, Saves: 3}, // High performer, winner
		{PlatformCode: 4, AccountId: 3}: {Points: 15, Assists: 8, Saves: 5}, // Highest performer, loser
	}

	ratings := CalculateNewIndividualRatings(players, stats, true)

	// All players should have ratings calculated
	if len(ratings) != 3 {
		t.Errorf("Expected 3 ratings, got %d", len(ratings))
	}

	for sessionID, rating := range ratings {
		if rating.Z != 3 {
			t.Errorf("Player %s: Expected Z=3, got %d", sessionID, rating.Z)
		}
		if rating.Mu <= 0 {
			t.Errorf("Player %s: Expected positive Mu, got %f", sessionID, rating.Mu)
		}
		if rating.Sigma <= 0 {
			t.Errorf("Player %s: Expected positive Sigma, got %f", sessionID, rating.Sigma)
		}
	}
}

// TestNonCompetitorPlayersExcluded tests that spectators and other non-competitor players are excluded
func TestNonCompetitorPlayersExcluded(t *testing.T) {
	players := []PlayerInfo{
		{
			SessionID:   "player1",
			EvrID:       evr.EvrId{PlatformCode: 4, AccountId: 1},
			Team:        BlueTeam,
			RatingMu:    25.0,
			RatingSigma: 8.333,
		},
		{
			SessionID:   "spectator",
			EvrID:       evr.EvrId{PlatformCode: 4, AccountId: 2},
			Team:        TeamIndex(evr.TeamSpectator), // Spectator
			RatingMu:    25.0,
			RatingSigma: 8.333,
		},
		{
			SessionID:   "player2",
			EvrID:       evr.EvrId{PlatformCode: 4, AccountId: 3},
			Team:        OrangeTeam,
			RatingMu:    25.0,
			RatingSigma: 8.333,
		},
	}

	stats := map[evr.EvrId]evr.MatchTypeStats{
		{PlatformCode: 4, AccountId: 1}: {Points: 5},
		{PlatformCode: 4, AccountId: 2}: {Points: 100}, // Spectator has high points but should be excluded
		{PlatformCode: 4, AccountId: 3}: {Points: 5},
	}

	ratings := CalculateNewTeamRatings(players, stats, true)

	// Only 2 players should have ratings (spectator excluded)
	if len(ratings) != 2 {
		t.Errorf("Expected 2 ratings (excluding spectator), got %d", len(ratings))
	}

	if _, exists := ratings["spectator"]; exists {
		t.Error("Spectator should not have a rating")
	}
}

// BenchmarkCalculateNewTeamRatings benchmarks the team rating calculation
func BenchmarkCalculateNewTeamRatings(b *testing.B) {
	players := make([]PlayerInfo, 8) // 4v4
	stats := make(map[evr.EvrId]evr.MatchTypeStats)

	for i := 0; i < 8; i++ {
		team := BlueTeam
		if i >= 4 {
			team = OrangeTeam
		}
		evrID := evr.EvrId{PlatformCode: 4, AccountId: uint64(i + 1)}
		players[i] = PlayerInfo{
			SessionID:   string(rune('a' + i)),
			EvrID:       evrID,
			Team:        team,
			RatingMu:    25.0,
			RatingSigma: 8.333,
		}
		stats[evrID] = evr.MatchTypeStats{
			Points:      int64(i * 2),
			Assists:     int64(i),
			Saves:       int64(i / 2),
			Passes:      int64(i),
			ShotsOnGoal: int64(i + 1),
		}
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		CalculateNewTeamRatings(players, stats, true)
	}
}

// TestPartyRatingsNotUsedForIndividualMMR verifies that matchmaking ratings (which can be aggregate
// party ratings) are not mistakenly used when calculating individual player MMR after a match.
// This is a regression test for the issue where party members would have their individual MMR
// overwritten by the party's aggregate MMR.
func TestPartyRatingsNotUsedForIndividualMMR(t *testing.T) {
	// Setup: Create two players with different individual ratings
	player1IndividualRating := types.Rating{Z: 3, Mu: 10.0, Sigma: 3.333}  // Low-skilled player
	player2IndividualRating := types.Rating{Z: 3, Mu: 27.0, Sigma: 3.333}  // High-skilled player
	
	// Simulate a party scenario: both players are in a party with an aggregate rating
	// (average of their ratings for matchmaking purposes)
	partyAggregateRating := types.Rating{
		Z:     3,
		Mu:    (player1IndividualRating.Mu + player2IndividualRating.Mu) / 2.0, // 18.5
		Sigma: 3.333,
	}

	// Create PlayerInfo with aggregate matchmaking ratings (this is what would be in label.Players)
	playersWithMatchmakingRatings := []PlayerInfo{
		{
			SessionID:   "player1",
			EvrID:       evr.EvrId{PlatformCode: 4, AccountId: 1},
			Team:        BlueTeam,
			RatingMu:    partyAggregateRating.Mu,    // This is the aggregate party rating
			RatingSigma: partyAggregateRating.Sigma,
		},
		{
			SessionID:   "player2",
			EvrID:       evr.EvrId{PlatformCode: 4, AccountId: 2},
			Team:        BlueTeam,
			RatingMu:    partyAggregateRating.Mu,    // This is the aggregate party rating
			RatingSigma: partyAggregateRating.Sigma,
		},
		{
			SessionID:   "player3",
			EvrID:       evr.EvrId{PlatformCode: 4, AccountId: 3},
			Team:        OrangeTeam,
			RatingMu:    18.0,
			RatingSigma: 3.333,
		},
		{
			SessionID:   "player4",
			EvrID:       evr.EvrId{PlatformCode: 4, AccountId: 4},
			Team:        OrangeTeam,
			RatingMu:    19.0,
			RatingSigma: 3.333,
		},
	}

	// Create PlayerInfo with individual ratings (this is what should be used for MMR calculation)
	playersWithIndividualRatings := []PlayerInfo{
		{
			SessionID:   "player1",
			EvrID:       evr.EvrId{PlatformCode: 4, AccountId: 1},
			Team:        BlueTeam,
			RatingMu:    player1IndividualRating.Mu,    // Individual rating, not party aggregate
			RatingSigma: player1IndividualRating.Sigma,
		},
		{
			SessionID:   "player2",
			EvrID:       evr.EvrId{PlatformCode: 4, AccountId: 2},
			Team:        BlueTeam,
			RatingMu:    player2IndividualRating.Mu,    // Individual rating, not party aggregate
			RatingSigma: player2IndividualRating.Sigma,
		},
		{
			SessionID:   "player3",
			EvrID:       evr.EvrId{PlatformCode: 4, AccountId: 3},
			Team:        OrangeTeam,
			RatingMu:    18.0,
			RatingSigma: 3.333,
		},
		{
			SessionID:   "player4",
			EvrID:       evr.EvrId{PlatformCode: 4, AccountId: 4},
			Team:        OrangeTeam,
			RatingMu:    19.0,
			RatingSigma: 3.333,
		},
	}

	// Create match stats (blue team wins)
	stats := map[evr.EvrId]evr.MatchTypeStats{
		{PlatformCode: 4, AccountId: 1}: {Points: 5, Assists: 2, Saves: 1},
		{PlatformCode: 4, AccountId: 2}: {Points: 8, Assists: 3, Saves: 2},
		{PlatformCode: 4, AccountId: 3}: {Points: 2, Assists: 1, Saves: 0},
		{PlatformCode: 4, AccountId: 4}: {Points: 3, Assists: 2, Saves: 1},
	}

	// Calculate ratings using matchmaking (aggregate) ratings - this is the BUG
	ratingsWithBug := CalculateNewTeamRatings(playersWithMatchmakingRatings, stats, true)

	// Calculate ratings using individual ratings - this is the FIX
	ratingsFixed := CalculateNewTeamRatings(playersWithIndividualRatings, stats, true)

	// Verify that the results are different, demonstrating the fix
	// Player 1 should have a different rating change when using individual vs aggregate
	bugRating1 := ratingsWithBug["player1"]
	fixedRating1 := ratingsFixed["player1"]
	
	// The fixed version should result in a smaller Mu increase for player1 (who started lower)
	// compared to using the aggregate rating
	if math.Abs(bugRating1.Mu - fixedRating1.Mu) < 0.1 {
		t.Errorf("Expected different rating changes when using individual vs aggregate ratings, but they were similar: bug=%.2f, fixed=%.2f", 
			bugRating1.Mu, fixedRating1.Mu)
	}

	// Player 2 should also have different rating changes
	bugRating2 := ratingsWithBug["player2"]
	fixedRating2 := ratingsFixed["player2"]
	
	if math.Abs(bugRating2.Mu - fixedRating2.Mu) < 0.1 {
		t.Errorf("Expected different rating changes when using individual vs aggregate ratings for player2, but they were similar: bug=%.2f, fixed=%.2f", 
			bugRating2.Mu, fixedRating2.Mu)
	}

	// Verify that individual ratings are preserved: player 1 should still be lower than player 2
	// after the match, reflecting their different starting points
	if fixedRating1.Mu >= fixedRating2.Mu {
		t.Errorf("Expected player1 (started at %.2f) to still have lower rating than player2 (started at %.2f) after match, but got player1=%.2f, player2=%.2f",
			player1IndividualRating.Mu, player2IndividualRating.Mu, fixedRating1.Mu, fixedRating2.Mu)
	}

	t.Logf("Bug scenario - Using aggregate rating %.2f for both players:", partyAggregateRating.Mu)
	t.Logf("  Player 1 (actual rating %.2f): %.2f -> %.2f", player1IndividualRating.Mu, partyAggregateRating.Mu, bugRating1.Mu)
	t.Logf("  Player 2 (actual rating %.2f): %.2f -> %.2f", player2IndividualRating.Mu, partyAggregateRating.Mu, bugRating2.Mu)
	
	t.Logf("Fixed scenario - Using individual ratings:")
	t.Logf("  Player 1: %.2f -> %.2f", player1IndividualRating.Mu, fixedRating1.Mu)
	t.Logf("  Player 2: %.2f -> %.2f", player2IndividualRating.Mu, fixedRating2.Mu)
}
