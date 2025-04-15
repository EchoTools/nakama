package server

import (
	"context"
	"errors"
	"fmt"

	"github.com/bwmarrin/discordgo"
	"github.com/heroiclabs/nakama-common/runtime"
)

var ErrNotInAMatch = errors.New("You are not in a match")

func (d *DiscordAppBot) ModPanelMessageEmbed(ctx context.Context, logger runtime.Logger, nk runtime.NakamaModule, discordID string) ([]discordgo.MessageComponent, error) {

	userID := d.cache.DiscordIDToUserID(discordID)

	// Get the list of players in the match
	presences, err := nk.StreamUserList(StreamModeService, userID, "", StreamLabelMatchService, false, true)
	if err != nil {
		return nil, fmt.Errorf("failed to get stream presences: %w", err)
	}

	if len(presences) == 0 {
		return nil, ErrNotInAMatch
	}

	// Get the match label
	label, err := MatchLabelByID(ctx, d.nk, MatchIDFromStringOrNil(presences[0].GetStatus()))
	if err != nil {
		return nil, fmt.Errorf("failed to get match label: %w", err)
	} else if label == nil {
		return nil, errors.New("failed to get match label")
	}

	options := make([]discordgo.SelectMenuOption, 0)

	for _, p := range label.Players {
		emoji := "⚫"
		switch p.Team {
		case BlueTeam:
			emoji = "🔵"
		case OrangeTeam:
			emoji = "🟠"
		case SocialLobbyParticipant:
			emoji = "🟣"
		}

		options = append(options, discordgo.SelectMenuOption{
			Label:       p.DisplayName,
			Value:       p.UserID,
			Description: fmt.Sprintf("%s / %s", p.Username, p.EvrID.String()),
			Emoji:       &discordgo.ComponentEmoji{Name: emoji},
		})
	}

	return []discordgo.MessageComponent{
		discordgo.ActionsRow{
			Components: []discordgo.MessageComponent{
				discordgo.SelectMenu{
					CustomID:    "select",
					Placeholder: "<select a player to kick>",
					Options:     options,
				},
				discordgo.Button{
					Label:    "Refresh",
					Style:    discordgo.PrimaryButton,
					CustomID: "refresh",
					Emoji:    &discordgo.ComponentEmoji{Name: "🔄"},
				},
			},
		},
		discordgo.ActionsRow{
			Components: []discordgo.MessageComponent{
				discordgo.SelectMenu{
					CustomID:    "trigger_cv",
					Placeholder: "Send user through Community Values?",
					Options: []discordgo.SelectMenuOption{
						{
							Label: "Yes",
							Value: "yes",
						},
						{
							Label: "No",
							Value: "no",
						},
					},
				},
			},
		},
		discordgo.ActionsRow{
			Components: []discordgo.MessageComponent{
				discordgo.SelectMenu{
					CustomID:    "reason",
					Placeholder: "<select a reason>",
					Options: []discordgo.SelectMenuOption{
						{
							Label: "Toxicity",
							Value: "toxicity",
						},
						{
							Label: "Poor Sportsmanship",
							Value: "poor_sportsmanship",
						},
						{
							Label: "Other (see below)",
							Value: "custom_reason",
						},
					},
				},
			},
		},
		discordgo.ActionsRow{
			Components: []discordgo.MessageComponent{
				discordgo.TextInput{
					CustomID:    "custom_reason_input",
					Label:       "Custom Reason",
					Style:       discordgo.TextInputParagraph,
					Placeholder: "Enter custom reason here...",
					Required:    false,
				},
			},
		},
		discordgo.ActionsRow{
			Components: []discordgo.MessageComponent{
				discordgo.Button{
					Label:    "Kick Player",
					Style:    discordgo.DangerButton,
					CustomID: "kick_player",
				},
			},
		},
	}, nil

}
