package server

import (
	"context"
	"testing"

	"github.com/bwmarrin/discordgo"
)

func TestHandleMessageReactionAdd(t *testing.T) {
	ctx := context.Background()
	s := &discordgo.Session{}
	reaction := &discordgo.MessageReactionAdd{
		MessageReaction: &discordgo.MessageReaction{
			GuildID:   "guildID",
			UserID:    "userID",
			Emoji:     discordgo.Emoji{Name: "🚕"},
			ChannelID: "channelID",
			MessageID: "messageID",
		},
	}
	logger := NewRuntimeGoLogger(logger)
	nk := &RuntimeGoNakamaModule{}
	hailRegistry := &echoTaxiHailRegistry{}

	handleMessageReactionAdd(ctx, s, reaction, nk, logger, hailRegistry)

	// Validate that the reaction resulted in a new entry in the hail registry
	userId, found := hailRegistry.userIdByDiscordId.Load(reaction.UserID)
	if !found || userId == "" {
		t.Errorf("Expected a new entry in the hail registry for the user")
	}

	// Validate that the reaction resulted in a new entry in the hail registry
	matchId, found := hailRegistry.matchIdByUserId.Load(userId)
	if !found || matchId == "" {
		t.Errorf("Expected a new entry in the hail registry for the match")
	}

}
