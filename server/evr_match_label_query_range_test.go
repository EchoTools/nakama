package server

import (
	"encoding/json"
	"testing"
)

// A numeric string outside the field's width used to wrap silently
// (LobbyType "300" -> 44, TeamIndex "70000" -> 4464) instead of erroring.
func TestLobbyTypeAndTeamIndexRejectOutOfRangeNumbers(t *testing.T) {
	var l LobbyType
	if err := json.Unmarshal([]byte(`"300"`), &l); err == nil {
		t.Errorf("LobbyType \"300\" accepted as %d; want error", l)
	}
	if err := json.Unmarshal([]byte(`"-1"`), &l); err == nil {
		t.Errorf("LobbyType \"-1\" accepted as %d; want error", l)
	}
	if err := json.Unmarshal([]byte(`"2"`), &l); err != nil || l != 2 {
		t.Errorf("LobbyType \"2\" = %d, %v; want 2, nil", l, err)
	}

	var ti TeamIndex
	if err := ti.UnmarshalText([]byte("70000")); err == nil {
		t.Errorf("TeamIndex 70000 accepted as %d; want error", ti)
	}
	if err := ti.UnmarshalText([]byte("-1")); err != nil || ti != -1 {
		t.Errorf("TeamIndex -1 = %d, %v; want -1, nil", ti, err)
	}
}
