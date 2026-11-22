package server

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/bwmarrin/discordgo"
	"github.com/heroiclabs/nakama-common/api"
	"google.golang.org/protobuf/types/known/wrapperspb"
)

func embedsJSON(t *testing.T, embeds ...*discordgo.MessageEmbed) string {
	t.Helper()
	data, err := json.Marshal(embeds)
	if err != nil {
		t.Fatalf("marshal embeds: %v", err)
	}
	return string(data)
}

func requireEmbedHidesModerator(t *testing.T, rendered string) {
	t.Helper()
	if strings.Contains(rendered, modTestModeratorTag) {
		t.Errorf("embed lists a moderator: %s", rendered)
	}
	for _, name := range []string{"BluePlayer", "OrangePlayer"} {
		if !strings.Contains(rendered, name) {
			t.Errorf("embed is missing player %s: %s", name, rendered)
		}
	}
}

func newModeratorEmbedFixture(t *testing.T) *modFixture {
	t.Helper()
	f := newModFixture(t)
	f.label.GroupID = nil
	f.label.SpawnedBy = ""
	f.label.GameServer.RegionCodes = []string{"us-east"}
	return f
}

func TestCreateServerEmbed_HidesModerators(t *testing.T) {
	f := newModeratorEmbedFixture(t)
	d := &DiscordAppBot{}

	embed := d.createServerEmbed(f.label)

	requireEmbedHidesModerator(t, embedsJSON(t, embed))
	if got := f.label.Players[4].DisplayName; !strings.Contains(got, modTestModeratorTag) {
		t.Fatalf("the published label was modified: row 4 is %q", got)
	}
}

func TestCreateSessionEmbed_HidesModerators(t *testing.T) {
	f := newModeratorEmbedFixture(t)
	manager := &SessionsChannelManager{ctx: context.Background(), logger: &testLogger{t: t}}

	embeds, _ := manager.createSessionEmbed(f.label, nil)

	requireEmbedHidesModerator(t, embedsJSON(t, embeds...))
	want := fmt.Sprintf("%d/%d", len(f.label.Players)-2, MaxPlayersArenaCombatPublic)
	if rendered := embedsJSON(t, embeds...); !strings.Contains(rendered, want) {
		t.Errorf("session size is not %s: %s", want, rendered)
	}
}

func labelAsMatch(t *testing.T, label *MatchLabel, size int32) *api.Match {
	t.Helper()
	data, err := json.Marshal(label)
	if err != nil {
		t.Fatalf("marshal label: %v", err)
	}
	return &api.Match{MatchId: label.ID.String(), Size: size, Label: wrapperspb.String(string(data))}
}

func TestRegionStatusEmbed_HidesModerators(t *testing.T) {
	f := newModeratorEmbedFixture(t)
	d := &DiscordAppBot{}

	embed, err := d.regionStatusEmbed(context.Background(), &testLogger{t: t}, "us-east", []*api.Match{labelAsMatch(t, f.label, int32(f.label.Size)+1)})
	if err != nil {
		t.Fatalf("regionStatusEmbed: %v", err)
	}

	rendered := embedsJSON(t, embed)
	if strings.Contains(rendered, modTestModeratorTag) {
		t.Errorf("region status lists a moderator: %s", rendered)
	}
	if !strings.Contains(rendered, "BluePlayer") {
		t.Errorf("region status is missing a player: %s", rendered)
	}
}

func TestBotStatusCounts_LeavesOutModerators(t *testing.T) {
	f := newModeratorEmbedFixture(t)

	// The match reports its presences plus the game server's.
	players, matches := botStatusCounts(&testLogger{t: t}, []*api.Match{labelAsMatch(t, f.label, int32(f.label.Size)+1)})

	if matches != 1 {
		t.Errorf("matches = %d, want 1", matches)
	}
	if want := f.label.Size - 1; players != want {
		t.Errorf("players = %d, want %d", players, want)
	}
}

func TestBotStatusCounts_CountsUnreadableLabelBySize(t *testing.T) {
	bad := &api.Match{Size: 3, Label: wrapperspb.String("not json")}

	players, matches := botStatusCounts(&testLogger{t: t}, []*api.Match{bad})

	if players != 2 || matches != 1 {
		t.Errorf("unreadable match counted as players=%d matches=%d, want 2 and 1", players, matches)
	}
}
