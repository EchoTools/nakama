package evr

import (
	"encoding/json"
	"strings"
	"testing"
)

// The game client reads its early quit state (earlyquit|penaltyts and the rest) from the server profile.
// The game service sets it only on the copy sent to the player at login; the stored copy other players
// fetch leaves it nil, and then the key is absent.
func TestServerProfileCarriesEarlyQuitOnlyWhenSet(t *testing.T) {
	without, err := json.Marshal(&ServerProfile{})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(without), `"earlyquit"`) {
		t.Errorf("a stored server profile carries earlyquit: %s", without)
	}

	with, err := json.Marshal(&ServerProfile{EarlyQuitFeatures: &EarlyQuitFeatures{PenaltyTimestamp: 1790979701, NumEarlyQuits: 5, PenaltyLevel: 2}})
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]json.RawMessage
	if err := json.Unmarshal(with, &decoded); err != nil {
		t.Fatal(err)
	}
	var eq map[string]int64
	if err := json.Unmarshal(decoded["earlyquit"], &eq); err != nil {
		t.Fatalf("earlyquit: %v (%s)", err, with)
	}
	if eq["penaltyts"] != 1790979701 || eq["numearlyquits"] != 5 || eq["penaltylevel"] != 2 {
		t.Errorf("earlyquit = %v, want the client's keys penaltyts/numearlyquits/penaltylevel", eq)
	}
}
