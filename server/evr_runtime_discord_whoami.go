package server

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/bwmarrin/discordgo"
	"github.com/gofrs/uuid/v5"
	"github.com/heroiclabs/nakama-common/api"
	"github.com/heroiclabs/nakama-common/runtime"
	"github.com/samber/lo"
)

type WhoAmI struct {
	NakamaID              uuid.UUID                       `json:"nakama_id"`
	Username              string                          `json:"username"`
	DiscordID             string                          `json:"discord_id"`
	CreateTime            time.Time                       `json:"create_time,omitempty"`
	DisplayNames          []string                        `json:"display_names"`
	DeviceLinks           []string                        `json:"device_links,omitempty"`
	HasPassword           bool                            `json:"has_password"`
	EVRIDLogins           map[string]time.Time            `json:"evr_id_logins"`
	GuildGroupMemberships map[string]GuildGroupMembership `json:"guild_memberships"`
	VRMLSeasons           []string                        `json:"vrml_seasons"`
	MatchIDs              []string                        `json:"match_ids"`

	ClientAddresses []string `json:"addresses,omitempty"`
	GhostedPlayers  []string `json:"ghosted_discord_ids,omitempty"`
}

type EvrIdLogins struct {
	EvrId         string `json:"evr_id"`
	LastLoginTime string `json:"login_time"`
	DisplayName   string `json:"display_name,omitempty"`
}

func (d *DiscordAppBot) handleProfileRequest(ctx context.Context, logger runtime.Logger, nk runtime.NakamaModule, s *discordgo.Session, i *discordgo.InteractionCreate, discordID string, username string, guildID string, includePrivate bool, includeDetail bool) error {
	whoami := &WhoAmI{
		DiscordID:             discordID,
		EVRIDLogins:           make(map[string]time.Time),
		DisplayNames:          make([]string, 0),
		ClientAddresses:       make([]string, 0),
		DeviceLinks:           make([]string, 0),
		GuildGroupMemberships: make(map[string]GuildGroupMembership, 0),
		MatchIDs:              make([]string, 0),
	}
	// Get the user's ID

	userIDStr, err := GetUserIDByDiscordID(ctx, d.db, discordID)
	if err != nil {
		userIDStr, _, _, err = d.nk.AuthenticateCustom(ctx, discordID, i.Member.User.Username, true)
		if err != nil {
			return fmt.Errorf("failed to authenticate (or create) user %s: %w", discordID, err)
		}
	}
	userID := uuid.FromStringOrNil(userIDStr)

	i.Member.GuildID = i.GuildID

	// Basic account details
	whoami.NakamaID = userID

	account, err := nk.AccountGetId(ctx, whoami.NakamaID.String())
	if err != nil {
		return err
	}

	whoami.Username = account.GetUser().GetUsername()

	if account.GetUser().GetCreateTime() != nil {
		whoami.CreateTime = account.GetUser().GetCreateTime().AsTime().UTC()
	}

	// Get the device links from the account
	whoami.DeviceLinks = make([]string, 0, len(account.GetDevices()))
	for _, device := range account.GetDevices() {
		whoami.DeviceLinks = append(whoami.DeviceLinks, fmt.Sprintf("`%s`", device.GetId()))
	}

	whoami.HasPassword = account.GetEmail() != ""

	memberships, err := GetGuildGroupMemberships(ctx, d.nk, userID.String())
	if err != nil {
		return err
	}

	for gid, m := range memberships {
		if guildID != "" && gid != guildID {
			continue
		}
		whoami.GuildGroupMemberships[gid] = m
	}

	evrIDRecords, err := GetEVRRecords(ctx, logger, nk, userID.String())
	if err != nil {
		return err
	}

	whoami.EVRIDLogins = make(map[string]time.Time, len(evrIDRecords))

	for evrID, record := range evrIDRecords {
		whoami.EVRIDLogins[evrID.String()] = record.UpdateTime.UTC()
	}

	// Get the past displayNames
	displayNameObjs, err := GetDisplayNameRecords(ctx, nk, userID.String())
	if err != nil {
		return err
	}
	// Sort displayNames by age
	sort.SliceStable(displayNameObjs, func(i, j int) bool {
		return displayNameObjs[i].GetUpdateTime().AsTime().After(displayNameObjs[j].GetUpdateTime().AsTime())
	})

	whoami.DisplayNames = make([]string, 0, len(displayNameObjs))
	for _, dn := range displayNameObjs {
		m := map[string]string{}
		if err := json.Unmarshal([]byte(dn.GetValue()), &m); err != nil {
			return err
		}
		whoami.DisplayNames = append(whoami.DisplayNames, m["displayName"])
	}

	// Get the MatchIDs for the user from it's presence
	presences, err := d.nk.StreamUserList(StreamModeService, userID.String(), "", StreamLabelMatchService, true, true)
	if err != nil {
		return err
	}

	whoami.MatchIDs = make([]string, 0, len(presences))
	for _, p := range presences {
		if p.GetStatus() != "" {
			mid := MatchIDFromStringOrNil(p.GetStatus())
			if mid.IsNil() {
				continue
			}
			whoami.MatchIDs = append(whoami.MatchIDs, mid.UUID.String())
		}
	}

	friends, err := ListPlayerFriends(ctx, RuntimeLoggerToZapLogger(logger), d.db, d.statusRegistry, userID)
	if err != nil {
		return err
	}

	guildGroups := d.cache.guildGroupCache.GuildGroups()

	ghostedDiscordIDs := make([]string, 0)
	for _, f := range friends {
		if api.Friend_State(f.GetState().Value) == api.Friend_BLOCKED {
			discordID := d.cache.UserIDToDiscordID(f.GetUser().GetId())
			ghostedDiscordIDs = append(ghostedDiscordIDs, discordID)
		}
	}
	whoami.GhostedPlayers = ghostedDiscordIDs

	if !includePrivate {
		whoami.HasPassword = false
		whoami.ClientAddresses = nil
		whoami.DeviceLinks = nil
		if len(whoami.EVRIDLogins) > 0 {
			// Set the timestamp to zero
			for k := range whoami.EVRIDLogins {
				whoami.EVRIDLogins[k] = time.Time{}
			}
		}

		if guildID == "" {
			whoami.GuildGroupMemberships = nil
		}
		whoami.GhostedPlayers = nil
		whoami.MatchIDs = nil
	}

	fields := []*discordgo.MessageEmbedField{
		{Name: "Nakama ID", Value: whoami.NakamaID.String(), Inline: false},
		{Name: "Online", Value: func() string {
			if len(presences) > 0 {
				return "Yes"
			}
			return "No"
		}(), Inline: true},
		{Name: "Create Time", Value: fmt.Sprintf("<t:%d:R>", whoami.CreateTime.Unix()), Inline: false},
		{Name: "Username", Value: whoami.Username, Inline: true},
		{Name: "Discord ID", Value: whoami.DiscordID, Inline: true},
		{Name: "Password Set", Value: func() string {
			if whoami.HasPassword {
				return "Yes"
			}
			return ""
		}(), Inline: true},
		{Name: "Linked Devices", Value: strings.Join(whoami.DeviceLinks, "\n"), Inline: false},
		{Name: "Display Names", Value: strings.Join(whoami.DisplayNames, "\n"), Inline: false},
		{Name: "Recent Logins", Value: func() string {
			lines := lo.MapToSlice(whoami.EVRIDLogins, func(k string, v time.Time) string {
				if v.IsZero() {
					// Don't use the timestamp
					return k
				} else {
					return fmt.Sprintf("<t:%d:R> - %s", v.Unix(), k)
				}
			})
			slices.Sort(lines)
			slices.Reverse(lines)
			return strings.Join(lines, "\n")
		}(), Inline: false},
		{Name: "Guild Memberships", Value: strings.Join(func() []string {
			output := make([]string, 0, len(whoami.GuildGroupMemberships))
			for gid, m := range whoami.GuildGroupMemberships {
				s := guildGroups[gid].Name()
				roles := make([]string, 0)
				if m.isMember {
					roles = append(roles, "member")
				}
				if m.isModerator {
					roles = append(roles, "moderator")
				}
				if m.isServerHost {
					roles = append(roles, "server-host")
				}
				if m.isAllocator {
					roles = append(roles, "allocator")
				}
				if len(roles) > 0 {
					s += fmt.Sprintf(" (%s)", strings.Join(roles, ", "))
				}
				output = append(output, s)
			}
			return output
		}(), "\n"), Inline: false},
		{Name: "Current Match(es)", Value: strings.Join(lo.Map(whoami.MatchIDs, func(m string, index int) string {
			return fmt.Sprintf("https://echo.taxi/spark://c/%s", strings.ToUpper(m))
		}), "\n"), Inline: false},
	}

	// Remove any blank fields
	fields = lo.Filter(fields, func(f *discordgo.MessageEmbedField, _ int) bool {
		return f.Value != ""
	})

	// Send the response
	s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseChannelMessageWithSource,
		Data: &discordgo.InteractionResponseData{
			Flags: discordgo.MessageFlagsEphemeral,
			Embeds: []*discordgo.MessageEmbed{
				{
					Title:  "EchoVRCE Account",
					Color:  0xCCCCCC,
					Fields: fields,
				},
			},
		},
	})
	return nil
}
