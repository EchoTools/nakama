package server

import (
	"strings"
	"testing"

	"github.com/bwmarrin/discordgo"
)

// Server hosts read the "Server Issue Report" embed to find the reporter.
// Discord's client often fails to resolve a bare user mention, so for months
// hosts have seen `<@1049325326234304522>` and no name. The mention stays -- it
// still resolves when Discord cooperates -- and the username goes beside it as
// plain text.

func TestServerIssueReportEmbed_NamesTheReporterInPlainText(t *testing.T) {
	reporter := &discordgo.User{ID: "1049325326234304522", Username: "sarcasm_personified"}

	embed := (&DiscordAppBot{}).createServerIssueReportEmbed(reporter, ServerIssueTypeLag, "", nil)

	if !strings.Contains(embed.Description, "<@1049325326234304522>") {
		t.Errorf("description dropped the mention, which still resolves when Discord cooperates: %q", embed.Description)
	}
	// Compare against the escaped form: `_` is markdown, so the description
	// carries `sarcasm\_personified`, which Discord renders as the username.
	if want := EscapeDiscordMarkdown(reporter.Username); !strings.Contains(embed.Description, want) {
		t.Errorf("description does not name the reporter in plain text (want %q), so a host sees only a number when the mention does not resolve: %q", want, embed.Description)
	}
}

func TestServerIssueReportEmbed_EscapesMarkdownInUsername(t *testing.T) {
	reporter := &discordgo.User{ID: "1", Username: "__bold__"}

	embed := (&DiscordAppBot{}).createServerIssueReportEmbed(reporter, ServerIssueTypeLag, "", nil)

	if strings.Contains(embed.Description, "__bold__") {
		t.Errorf("a player-controlled username reached the embed unescaped and would render as markdown: %q", embed.Description)
	}
}

// Additive only: nothing a host might be reading by position moves.
func TestServerIssueReportEmbed_FieldOrderUnchanged(t *testing.T) {
	reporter := &discordgo.User{ID: "1", Username: "u"}
	data := &WhereAmIData{
		ServerHostIP: "203.0.113.1", ServerHostPort: 6792, RegionCode: "us-east",
		GuildName: "G", MatchMode: "Arena", EchoTaxiLink: "https://echo.taxi/x",
		OperatorDiscord: "9", Players: []PlayerInfo{{DiscordID: "1", DisplayName: "P"}},
	}

	embed := (&DiscordAppBot{}).createServerIssueReportEmbed(reporter, ServerIssueTypeLag, "details", data)

	want := []string{"Issue Type", "Details", "Server Host", "Region", "Guild", "Match Mode", "Spark Link", "Server Operator", "Players (1)"}
	if len(embed.Fields) != len(want) {
		t.Fatalf("field count = %d, want %d", len(embed.Fields), len(want))
	}
	for i, name := range want {
		if embed.Fields[i].Name != name {
			t.Errorf("field %d = %q, want %q", i, embed.Fields[i].Name, name)
		}
	}
}
