package server

import (
	"testing"

	"github.com/heroiclabs/nakama-common/runtime"
)

// toxicSepMockEntry implements runtime.MatchmakerEntry for toxic separation tests.
type toxicSepMockEntry struct {
	ticket     string
	properties map[string]interface{}
	presence   runtime.Presence
}

func (m *toxicSepMockEntry) GetTicket() string                      { return m.ticket }
func (m *toxicSepMockEntry) GetPresence() runtime.Presence          { return m.presence }
func (m *toxicSepMockEntry) GetPartyId() string                     { return "" }
func (m *toxicSepMockEntry) GetCreateTime() int64                   { return 0 }
func (m *toxicSepMockEntry) GetProperties() map[string]interface{} { return m.properties }

// toxicSepMockPresence implements runtime.Presence for toxic separation tests.
type toxicSepMockPresence struct {
	sessionID string
}

func (p *toxicSepMockPresence) GetUserId() string    { return "" }
func (p *toxicSepMockPresence) GetSessionId() string { return p.sessionID }
func (p *toxicSepMockPresence) GetNodeId() string    { return "" }
func (p *toxicSepMockPresence) GetHidden() bool      { return false }
func (p *toxicSepMockPresence) GetPersistence() bool { return false }
func (p *toxicSepMockPresence) GetUsername() string   { return "" }
func (p *toxicSepMockPresence) GetStatus() string     { return "" }
func (p *toxicSepMockPresence) GetReason() runtime.PresenceReason {
	return runtime.PresenceReason(0)
}

func newToxicSepEntry(sessionID string, gamesPlayed float64, hasSuspensionHistory string) runtime.MatchmakerEntry {
	return &toxicSepMockEntry{
		ticket: "ticket-" + sessionID,
		properties: map[string]interface{}{
			"games_played":           gamesPlayed,
			"has_suspension_history": hasSuspensionHistory,
		},
		presence: &toxicSepMockPresence{sessionID: sessionID},
	}
}

func TestCandidateContainsToxicNewPlayerMix(t *testing.T) {
	threshold := 50

	tests := []struct {
		name       string
		candidate  []runtime.MatchmakerEntry
		wantToxic  bool
	}{
		{
			name: "new player + toxic player is rejected",
			candidate: []runtime.MatchmakerEntry{
				newToxicSepEntry("new1", 10, "false"),   // new player
				newToxicSepEntry("toxic1", 200, "true"), // toxic veteran
			},
			wantToxic: true,
		},
		{
			name: "new player + clean player is accepted",
			candidate: []runtime.MatchmakerEntry{
				newToxicSepEntry("new1", 10, "false"),    // new player
				newToxicSepEntry("clean1", 200, "false"), // clean veteran
			},
			wantToxic: false,
		},
		{
			name: "veteran + toxic player is accepted (no new player shield needed)",
			candidate: []runtime.MatchmakerEntry{
				newToxicSepEntry("vet1", 100, "false"),  // veteran
				newToxicSepEntry("toxic1", 200, "true"), // toxic veteran
			},
			wantToxic: false,
		},
		{
			name: "new player + enforcer with suspensions is accepted (enforcer exempt)",
			candidate: []runtime.MatchmakerEntry{
				newToxicSepEntry("new1", 10, "false"),    // new player
				newToxicSepEntry("enforcer1", 200, "false"), // enforcer — exempt, so has_suspension_history="false"
			},
			wantToxic: false,
		},
		{
			name: "all new players no toxic",
			candidate: []runtime.MatchmakerEntry{
				newToxicSepEntry("new1", 5, "false"),
				newToxicSepEntry("new2", 3, "false"),
			},
			wantToxic: false,
		},
		{
			name: "all toxic veterans — no new players",
			candidate: []runtime.MatchmakerEntry{
				newToxicSepEntry("toxic1", 200, "true"),
				newToxicSepEntry("toxic2", 300, "true"),
			},
			wantToxic: false,
		},
		{
			name: "mixed candidate with multiple new and one toxic",
			candidate: []runtime.MatchmakerEntry{
				newToxicSepEntry("new1", 5, "false"),
				newToxicSepEntry("new2", 10, "false"),
				newToxicSepEntry("vet1", 100, "false"),
				newToxicSepEntry("toxic1", 200, "true"),
			},
			wantToxic: true,
		},
		{
			name: "missing has_suspension_history property treated as clean",
			candidate: []runtime.MatchmakerEntry{
				newToxicSepEntry("new1", 10, "false"),
				&toxicSepMockEntry{
					ticket: "ticket-nohistory",
					properties: map[string]interface{}{
						"games_played": float64(200),
						// no has_suspension_history
					},
					presence: &toxicSepMockPresence{sessionID: "nohistory"},
				},
			},
			wantToxic: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := CandidateContainsToxicNewPlayerMix(tt.candidate, threshold)
			if got != tt.wantToxic {
				t.Errorf("CandidateContainsToxicNewPlayerMix() = %v, want %v", got, tt.wantToxic)
			}
		})
	}
}

func TestToxicSeparationEnabled(t *testing.T) {
	t.Run("default is true when nil", func(t *testing.T) {
		g := GlobalMatchmakingSettings{}
		if !g.ToxicSeparationEnabled() {
			t.Error("expected ToxicSeparationEnabled() to return true when EnableToxicSeparation is nil")
		}
	})
	t.Run("true when set to true", func(t *testing.T) {
		v := true
		g := GlobalMatchmakingSettings{EnableToxicSeparation: &v}
		if !g.ToxicSeparationEnabled() {
			t.Error("expected ToxicSeparationEnabled() to return true")
		}
	})
	t.Run("false when set to false", func(t *testing.T) {
		v := false
		g := GlobalMatchmakingSettings{EnableToxicSeparation: &v}
		if g.ToxicSeparationEnabled() {
			t.Error("expected ToxicSeparationEnabled() to return false")
		}
	})
}

func TestToxicSeparationDefaultSetting(t *testing.T) {
	data := &ServiceSettingsData{}
	FixDefaultServiceSettings(nil, data)

	if data.Matchmaking.EnableToxicSeparation == nil {
		t.Fatal("expected EnableToxicSeparation to be set after FixDefaultServiceSettings")
	}
	if !*data.Matchmaking.EnableToxicSeparation {
		t.Error("expected EnableToxicSeparation default to be true")
	}
}

func TestHasSuspensionHistory(t *testing.T) {
	tests := []struct {
		name  string
		props map[string]interface{}
		want  bool
	}{
		{
			name:  "true when set to true",
			props: map[string]interface{}{"has_suspension_history": "true"},
			want:  true,
		},
		{
			name:  "false when set to false",
			props: map[string]interface{}{"has_suspension_history": "false"},
			want:  false,
		},
		{
			name:  "false when missing",
			props: map[string]interface{}{},
			want:  false,
		},
		{
			name:  "false when nil properties",
			props: nil,
			want:  false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			entry := &toxicSepMockEntry{properties: tt.props}
			got := HasSuspensionHistory(entry)
			if got != tt.want {
				t.Errorf("HasSuspensionHistory() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestFilterToxicNewPlayerCandidates(t *testing.T) {
	threshold := 50

	t.Run("filters candidates with toxic-new mix", func(t *testing.T) {
		candidates := [][]runtime.MatchmakerEntry{
			{ // should be filtered: new + toxic
				newToxicSepEntry("new1", 10, "false"),
				newToxicSepEntry("toxic1", 200, "true"),
			},
			{ // should pass: no new players
				newToxicSepEntry("vet1", 100, "false"),
				newToxicSepEntry("toxic2", 200, "true"),
			},
			{ // should pass: new + clean
				newToxicSepEntry("new2", 5, "false"),
				newToxicSepEntry("clean1", 200, "false"),
			},
		}

		count := FilterToxicNewPlayerCandidates(candidates, threshold)
		if count != 1 {
			t.Errorf("expected 1 filtered candidate, got %d", count)
		}
		if candidates[0] != nil {
			t.Error("expected first candidate to be nil (filtered)")
		}
		if candidates[1] == nil {
			t.Error("expected second candidate to pass (no new players)")
		}
		if candidates[2] == nil {
			t.Error("expected third candidate to pass (new + clean)")
		}
	})

	t.Run("disabled setting passes all candidates", func(t *testing.T) {
		candidates := [][]runtime.MatchmakerEntry{
			{
				newToxicSepEntry("new1", 10, "false"),
				newToxicSepEntry("toxic1", 200, "true"),
			},
		}

		// threshold 0 means nobody is considered new, so no filtering
		count := FilterToxicNewPlayerCandidates(candidates, 0)
		if count != 0 {
			t.Errorf("expected 0 filtered candidates with threshold 0, got %d", count)
		}
		if candidates[0] == nil {
			t.Error("expected candidate to pass when threshold is 0")
		}
	})
}
