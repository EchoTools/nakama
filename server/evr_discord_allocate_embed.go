package server

import (
	"fmt"
	"strings"

	"github.com/bwmarrin/discordgo"
)

// allocateEmbed is the /allocate reply: the fields a person needs to find and
// join the match, in the same embed style as the match-created reply
// (evr_discord_appbot.go "create": Title, Color 0x9656ce, Mode/Region/Spark Link
// fields). It replaces dumping the label JSON, which could exceed Discord's
// 2000-rune content limit.
func allocateEmbed(label *MatchLabel, region string) *discordgo.MessageEmbed {
	matchID := label.ID.UUID.String()

	mode := label.Mode.String()
	level := label.Level.String()

	serverLocation := "Unknown"
	if label.GameServer != nil {
		serverLocation = label.GameServer.LocationRegionCode(true, true)
	}
	if region == "" {
		region = "default"
	}

	return &discordgo.MessageEmbed{
		Title: "Match Prepared",
		Color: 0x9656ce,
		Fields: []*discordgo.MessageEmbedField{
			{Name: "Mode", Value: mode, Inline: true},
			{Name: "Level", Value: level, Inline: true},
			{Name: "Region", Value: region, Inline: true},
			{Name: "Server", Value: serverLocation, Inline: false},
			{Name: "Match ID", Value: fmt.Sprintf("`%s`", matchID), Inline: false},
			{Name: "Spark Link", Value: fmt.Sprintf("[Spark Link](https://echo.taxi/spark://c/%s)", strings.ToUpper(matchID)), Inline: false},
		},
	}
}
