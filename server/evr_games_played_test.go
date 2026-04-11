package server

import (
	"testing"

	"github.com/heroiclabs/nakama-common/runtime"
)

// gamesPlayedMockEntry implements runtime.MatchmakerEntry with configurable properties.
type gamesPlayedMockEntry struct {
	properties map[string]interface{}
}

func (m *gamesPlayedMockEntry) GetTicket() string                      { return "" }
func (m *gamesPlayedMockEntry) GetPresence() runtime.Presence          { return nil }
func (m *gamesPlayedMockEntry) GetPartyId() string                     { return "" }
func (m *gamesPlayedMockEntry) GetCreateTime() int64                   { return 0 }
func (m *gamesPlayedMockEntry) GetProperties() map[string]interface{} { return m.properties }

func TestIsNewPlayer(t *testing.T) {
	tests := []struct {
		name      string
		props     map[string]interface{}
		threshold int
		want      bool
	}{
		{
			name:      "zero games is new player",
			props:     map[string]interface{}{"games_played": float64(0)},
			threshold: 50,
			want:      true,
		},
		{
			name:      "below threshold is new player",
			props:     map[string]interface{}{"games_played": float64(49)},
			threshold: 50,
			want:      true,
		},
		{
			name:      "at threshold is not new player",
			props:     map[string]interface{}{"games_played": float64(50)},
			threshold: 50,
			want:      false,
		},
		{
			name:      "above threshold is not new player",
			props:     map[string]interface{}{"games_played": float64(200)},
			threshold: 50,
			want:      false,
		},
		{
			name:      "missing property treated as new player",
			props:     map[string]interface{}{},
			threshold: 50,
			want:      true,
		},
		{
			name:      "nil properties treated as new player",
			props:     nil,
			threshold: 50,
			want:      true,
		},
		{
			name:      "threshold of 1 with zero games",
			props:     map[string]interface{}{"games_played": float64(0)},
			threshold: 1,
			want:      true,
		},
		{
			name:      "threshold of 1 with one game",
			props:     map[string]interface{}{"games_played": float64(1)},
			threshold: 1,
			want:      false,
		},
		{
			name:      "threshold of zero means nobody is new",
			props:     map[string]interface{}{"games_played": float64(0)},
			threshold: 0,
			want:      false,
		},
		{
			name:      "non-numeric property treated as new player",
			props:     map[string]interface{}{"games_played": "not_a_number"},
			threshold: 50,
			want:      true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			entry := &gamesPlayedMockEntry{properties: tt.props}
			got := IsNewPlayer(entry, tt.threshold)
			if got != tt.want {
				t.Errorf("IsNewPlayer() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestNewPlayerMaxGamesZeroPreserved(t *testing.T) {
	data := &ServiceSettingsData{}
	FixDefaultServiceSettings(nil, data)

	if data.Matchmaking.NewPlayerMaxGames != 0 {
		t.Errorf("expected NewPlayerMaxGames zero (disabled) to be preserved, got %d", data.Matchmaking.NewPlayerMaxGames)
	}
}

func TestNewPlayerMaxGamesPreserved(t *testing.T) {
	data := &ServiceSettingsData{}
	data.Matchmaking.NewPlayerMaxGames = 100
	FixDefaultServiceSettings(nil, data)

	if data.Matchmaking.NewPlayerMaxGames != 100 {
		t.Errorf("expected NewPlayerMaxGames to be preserved as 100, got %d", data.Matchmaking.NewPlayerMaxGames)
	}
}

func TestNewPlayerMaxGamesNegativeClamped(t *testing.T) {
	data := &ServiceSettingsData{}
	data.Matchmaking.NewPlayerMaxGames = -5
	FixDefaultServiceSettings(nil, data)

	if data.Matchmaking.NewPlayerMaxGames != 0 {
		t.Errorf("expected negative NewPlayerMaxGames to clamp to 0, got %d", data.Matchmaking.NewPlayerMaxGames)
	}
}


func TestGamesPlayedOnLobbySessionParameters(t *testing.T) {
	params := &LobbySessionParameters{
		GamesPlayed: 42,
	}
	if params.GamesPlayed != 42 {
		t.Errorf("GamesPlayed = %d, want 42", params.GamesPlayed)
	}
}
