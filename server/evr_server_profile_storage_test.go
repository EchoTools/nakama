package server

import (
	"encoding/json"
	"testing"

	"github.com/heroiclabs/nakama/v3/server/evr"
)

func TestStripUnlocksFromProfileJSON(t *testing.T) {
	tests := []struct {
		name      string
		input     string
		wantKey   bool   // should "unlocks" key exist in output?
		wantError bool
	}{
		{
			name:    "strips unlocks from valid profile",
			input:   `{"displayname":"test","unlocks":{"arena":{"item1":true,"item2":true}},"stats":{"wins":5}}`,
			wantKey: false,
		},
		{
			name:    "preserves other fields",
			input:   `{"displayname":"test","unlocks":{"a":{"b":true}},"loadout":{"hat":"cool"}}`,
			wantKey: false,
		},
		{
			name:    "no-op when unlocks absent",
			input:   `{"displayname":"test","stats":{"wins":5}}`,
			wantKey: false,
		},
		{
			name:      "returns error on invalid JSON",
			input:     `{not valid json`,
			wantError: true,
		},
		{
			name:      "returns error on empty input",
			input:     ``,
			wantError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := stripUnlocksFromProfileJSON(json.RawMessage(tt.input))

			if tt.wantError {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				return
			}

			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			// Verify result is valid JSON
			var m map[string]json.RawMessage
			if err := json.Unmarshal(result, &m); err != nil {
				t.Fatalf("result is not valid JSON: %v", err)
			}

			// Verify unlocks key is absent
			if _, ok := m["unlocks"]; ok {
				t.Error("unlocks key should not be present in output")
			}

			// Verify other keys are preserved
			if tt.name == "preserves other fields" {
				if _, ok := m["displayname"]; !ok {
					t.Error("displayname should be preserved")
				}
				if _, ok := m["loadout"]; !ok {
					t.Error("loadout should be preserved")
				}
			}
		})
	}
}

func TestStripUnlocksFromProfileJSON_PreservesNestedValues(t *testing.T) {
	input := `{"displayname":"player1","stats":{"arena":{"wins":10,"losses":5}},"loadout":{"emote":"wave"}}`
	result, err := stripUnlocksFromProfileJSON(json.RawMessage(input))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var m map[string]json.RawMessage
	if err := json.Unmarshal(result, &m); err != nil {
		t.Fatalf("result is not valid JSON: %v", err)
	}

	// Verify nested stats are preserved exactly
	var stats map[string]json.RawMessage
	if err := json.Unmarshal(m["stats"], &stats); err != nil {
		t.Fatalf("stats is not valid JSON: %v", err)
	}
	if _, ok := stats["arena"]; !ok {
		t.Error("nested stats.arena should be preserved")
	}
}

func TestServerProfileStorage_PublicProfilePopulated(t *testing.T) {
	input := `{"displayname":"test","xplatformid":"OVR-ORG-1234","unlocks":{"arena":{"item1":true}},"stats":{}}`

	storage := NewServerProfileStorageFromJSON("user1", evrIDForTest(), json.RawMessage(input))

	if len(storage.PublicProfile) == 0 {
		t.Fatal("PublicProfile should be populated")
	}

	var m map[string]json.RawMessage
	if err := json.Unmarshal(storage.PublicProfile, &m); err != nil {
		t.Fatalf("PublicProfile is not valid JSON: %v", err)
	}

	if _, ok := m["unlocks"]; ok {
		t.Error("PublicProfile should not contain unlocks")
	}
	if _, ok := m["displayname"]; !ok {
		t.Error("PublicProfile should contain displayname")
	}
}

func evrIDForTest() evr.EvrId {
	return evr.EvrId{PlatformCode: 4, AccountId: 1234}
}
