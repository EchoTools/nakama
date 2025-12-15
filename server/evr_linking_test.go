package server

import (
	"context"
	"errors"
	"testing"

	"github.com/bwmarrin/discordgo"
	"github.com/heroiclabs/nakama/v3/server/evr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGenerateLinkTicket(t *testing.T) {
	evrID, _ := evr.ParseEvrId("OVR-ORG-12345")

	linkTickets := make(map[string]*LinkTicket)

	loginData := &evr.LoginProfile{
		// Populate with necessary fields
	}

	ticket := generateLinkTicket(linkTickets, *evrID, "127.0.0.1", loginData)

	assert.NotNil(t, ticket, "Expected a non-nil link ticket")
	assert.Equal(t, loginData, ticket.LoginProfile, "Expected LoginRequest to match")
	assert.Contains(t, linkTickets, ticket.Code, "Expected linkTickets to contain the generated code")
}

func TestGenerateLinkTicketWithExistingToken(t *testing.T) {

	evrID, _ := evr.ParseEvrId("OVR-ORG-12345")
	linkTickets := map[string]*LinkTicket{
		"existing-code": {
			Code:         "existing-code",
			XPID:         *evrID,
			ClientIP:     "127.0.0.1",
			LoginProfile: &evr.LoginProfile{},
		},
	}

	loginData := &evr.LoginProfile{}

	ticket := generateLinkTicket(linkTickets, *evrID, "127.0.0.1", loginData)

	assert.NotNil(t, ticket, "Expected a non-nil link ticket")

	assert.Equal(t, loginData, ticket.LoginProfile, "Expected LoginRequest to match")
	assert.Contains(t, linkTickets, ticket.Code, "Expected linkTickets to contain the generated code")
	assert.NotEqual(t, "existing-code", ticket.Code, "Expected a new code to be generated")
}

type mockSession struct {
	guildMemberFunc func(guildID, userID string) (*discordgo.Member, error)
}

func (m *mockSession) GuildMember(guildID, userID string) (*discordgo.Member, error) {
	if m.guildMemberFunc != nil {
		return m.guildMemberFunc(guildID, userID)
	}
	return nil, errors.New("not implemented")
}

func TestHandleLinkHeadset_NilMember(t *testing.T) {
	bot := &DiscordAppBot{}
	ctx := context.Background()
	logger := &NoopLogger{}
	s := &mockSession{
		guildMemberFunc: func(guildID, userID string) (*discordgo.Member, error) {
			return nil, errors.New("member not found")
		},
	}

	i := &discordgo.InteractionCreate{
		GuildID: "guild1",
		Member:  &discordgo.Member{User: &discordgo.User{ID: "user1"}},
		// Simulate command options
		ApplicationCommandData: func() discordgo.ApplicationCommandInteractionData {
			return discordgo.ApplicationCommandInteractionData{
				Options: []*discordgo.ApplicationCommandInteractionDataOption{{
					Type:  3, // string
					Value: "ABCD",
				}},
			}
		},
	}

	err := bot.handleLinkHeadset(ctx, logger, s, i, i.Member.User, nil, "user1", "group1")
	require.Error(t, err)
}
