package server

import (
	"testing"

	"github.com/echotools/vrmlgo/v5"
)

func TestPlayerSummary(t *testing.T) {

	verifier := VRMLVerifier{}
	vg := vrmlgo.New("")

	summary, err := verifier.playerSummary(vg, "P-Dh8NZNe1cpUc4Wr2HXZw2")
	if err != nil {
		t.Fatalf("Error: %v", err)
	}

	if summary.Player == nil {
		t.Errorf("Player is nil")
	}

	if summary.User == nil {
		t.Errorf("User is nil")
	}

	if summary.Teams == nil {
		t.Errorf("Teams is nil")
	}

	if summary.MatchCountsBySeasonByTeam == nil {
		t.Errorf("MatchCountsBySeasonByTeam is nil")
	}

	t.Errorf("Summary: %+v\n", summary.MatchCountsBySeasonByTeam)
}
