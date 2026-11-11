package server

import (
	"strings"
	"testing"

	"github.com/gofrs/uuid/v5"
	"github.com/heroiclabs/nakama/v3/server/evr"
)

// TestAllocateEmbedShowsWhatAPersonNeeds: /allocate answers with an embed
// (not the label JSON) carrying mode, region, level and the match link.
func TestAllocateEmbedShowsWhatAPersonNeeds(t *testing.T) {
	id := uuid.Must(uuid.FromString("6b2e2b8e-0000-4000-8000-000000000000"))
	label := &MatchLabel{
		ID:    MatchID{UUID: id, Node: "n"},
		Mode:  evr.ModeArenaPrivate,
		Level: evr.LevelArena,
		GameServer: &GameServerPresence{
			Region:      "us-east",
			RegionCodes: []string{"us-east"},
		},
	}
	e := allocateEmbed(label, "us-east")
	if e == nil || e.Title == "" {
		t.Fatalf("no embed title: %+v", e)
	}
	got := map[string]string{}
	for _, f := range e.Fields {
		got[f.Name] = f.Value
	}
	for _, name := range []string{"Mode", "Region", "Level", "Match ID", "Spark Link"} {
		if got[name] == "" {
			t.Errorf("field %q missing or empty; fields=%v", name, got)
		}
	}
	if !strings.Contains(got["Spark Link"], "https://echo.taxi/spark://c/6B2E2B8E-0000-4000-8000-000000000000") {
		t.Errorf("spark link = %q", got["Spark Link"])
	}
	if !strings.Contains(got["Match ID"], id.String()) {
		t.Errorf("match id = %q", got["Match ID"])
	}
	for name, v := range got {
		if strings.Contains(v, "```json") || len(v) > 1024 {
			t.Errorf("field %q is raw JSON or over 1024: %q", name, v)
		}
	}
}
