package server

import (
	"context"
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
	MatchLabels           []*MatchLabel                   `json:"match_labels"`
	DefaultLobbyGroup     string                          `json:"active_lobby_group,omitempty"`
	ClientAddresses       []string                        `json:"addresses,omitempty"`
	GhostedPlayers        []string                        `json:"ghosted_discord_ids,omitempty"`
	LastMatchmakingError  error                           `json:"last_matchmaking_error,omitempty"`
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
		MatchLabels:           make([]*MatchLabel, 0),
	}

	// Get the user's ID
	member, err := s.GuildMember(i.GuildID, discordID)
	if err != nil || member == nil || member.User == nil {
		return fmt.Errorf("failed to get guild member: %w", err)
	}

	userIDStr, err := GetUserIDByDiscordID(ctx, d.db, discordID)
	if err != nil {
		userIDStr, _, _, err = d.nk.AuthenticateCustom(ctx, discordID, member.User.Username, true)
		if err != nil {
			return fmt.Errorf("failed to authenticate (or create) user %s: %w", discordID, err)
		}
	}
	userID := uuid.FromStringOrNil(userIDStr)

	// Basic account details
	whoami.NakamaID = userID

	account, err := nk.AccountGetId(ctx, whoami.NakamaID.String())
	if err != nil {
		return fmt.Errorf("failed to get account by ID: %w", err)
	}
	md, err := GetAccountMetadata(ctx, nk, userID.String())
	if err != nil {
		return fmt.Errorf("failed to get account metadata: %w", err)
	}

	whoami.DefaultLobbyGroup = md.GetActiveGroupID().String()

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

	history, err := DisplayNameHistoryLoad(ctx, nk, userID.String())
	if err != nil {
		return fmt.Errorf("failed to load display name history: %w", err)
	}

	pastDisplayNames := make(map[string]time.Time)
	for _, items := range history.Histories {
		for _, item := range items {
			if e, ok := pastDisplayNames[item.DisplayName]; !ok || e.After(item.UpdateTime) {
				pastDisplayNames[item.DisplayName] = item.UpdateTime
			}
		}
	}

	whoami.DisplayNames = make([]string, 0, len(pastDisplayNames))
	for dn := range pastDisplayNames {
		whoami.DisplayNames = append(whoami.DisplayNames, EscapeDiscordMarkdown(dn))
	}

	slices.SortStableFunc(whoami.DisplayNames, func(a, b string) int {
		return int(pastDisplayNames[a].Unix() - pastDisplayNames[b].Unix())
	})

	// Get the MatchIDs for the user from it's presence
	presences, err := d.nk.StreamUserList(StreamModeService, userID.String(), "", StreamLabelMatchService, true, true)
	if err != nil {
		return err
	}

	whoami.MatchLabels = make([]*MatchLabel, 0, len(presences))
	for _, p := range presences {
		if p.GetStatus() != "" {
			mid := MatchIDFromStringOrNil(p.GetStatus())
			if mid.IsNil() {
				continue
			}
			label, err := MatchLabelByID(ctx, nk, mid)
			if err != nil {
				logger.Warn("failed to get match label", "error", err)
				continue
			}

			whoami.MatchLabels = append(whoami.MatchLabels, label)
		}
	}

	// If the player is online, Get the most recent matchmaking error for the player.
	if len(presences) > 0 {
		// Get the most recent matchmaking error for the player
		if session := d.pipeline.sessionRegistry.Get(uuid.FromStringOrNil(presences[0].GetSessionId())); session != nil {
			params, ok := LoadParams(session.Context())
			if ok {
				whoami.LastMatchmakingError = params.LastMatchmakingError.Load()
			}
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
		whoami.DefaultLobbyGroup = ""
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
		whoami.MatchLabels = nil
	}

	fields := []*discordgo.MessageEmbedField{
		{Name: "Nakama ID", Value: whoami.NakamaID.String(), Inline: true},
		{Name: "Username", Value: whoami.Username, Inline: true},
		{Name: "Discord ID", Value: whoami.DiscordID, Inline: true},
		{Name: "Created", Value: fmt.Sprintf("<t:%d:R>", whoami.CreateTime.Unix()), Inline: false},
		{Name: "Password Set", Value: func() string {
			if whoami.HasPassword {
				return "Yes"
			}
			return ""
		}(), Inline: true},
		{Name: "Online", Value: func() string {
			if len(presences) > 0 {
				return "Yes"
			}
			return "No"
		}(), Inline: false},
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

			groupIDs := make([]string, 0, len(whoami.GuildGroupMemberships))
			for gid := range whoami.GuildGroupMemberships {
				groupIDs = append(groupIDs, gid)
			}
			sort.Strings(groupIDs)

			for _, gid := range groupIDs {
				m := whoami.GuildGroupMemberships[gid]

				group, ok := guildGroups[gid]
				if !ok {
					continue
				}

				groupStr := group.Name()
				roles := make([]string, 0)
				if m.IsMember {
					roles = append(roles, "member")
				}
				if m.IsModerator {
					roles = append(roles, "moderator")
				}
				if m.IsServerHost {
					roles = append(roles, "server-host")
				}
				if m.IsAllocator {
					roles = append(roles, "allocator")
				}
				if m.IsAPIAccess {
					roles = append(roles, "api-access")
				}
				if m.IsSuspended {
					roles = append(roles, "suspended")
				}
				if m.IsVPNBypass {
					roles = append(roles, "vpn-bypass")
				}
				if len(roles) > 0 {
					groupStr += fmt.Sprintf(" (%s)", strings.Join(roles, ", "))
				}
				output = append(output, groupStr)
			}
			return output
		}(), "\n"), Inline: false},
		{Name: "Match List", Value: strings.Join(lo.Map(whoami.MatchLabels, func(l *MatchLabel, index int) string {
			link := fmt.Sprintf("`%s`: https://echo.taxi/spark://c/%s", l.Mode.String(), strings.ToUpper(l.ID.UUID.String()))
			players := make([]string, 0, len(l.Players))
			for _, p := range l.Players {
				players = append(players, fmt.Sprintf("<@%s>", p.DiscordID))
			}
			return fmt.Sprintf("%s - %s\n%s", l.Mode.String(), link, strings.Join(players, ", "))
		}), "\n"), Inline: false},
	}

	fields = append(fields, &discordgo.MessageEmbedField{
		Name: "Default Matchmaking Guild",
		Value: func() string {
			if whoami.DefaultLobbyGroup != "" {
				if group, ok := guildGroups[whoami.DefaultLobbyGroup]; ok {
					return group.Name()
				}
				return whoami.DefaultLobbyGroup
			}
			return ""
		}(),
		Inline: false,
	})

	if whoami.LastMatchmakingError != nil {
		fields = append(fields, &discordgo.MessageEmbedField{
			Name:   "Last Matchmaking Error",
			Value:  whoami.LastMatchmakingError.Error(),
			Inline: false,
		})
	}
	// Remove any blank fields
	fields = lo.Filter(fields, func(f *discordgo.MessageEmbedField, _ int) bool {
		return f.Value != ""
	})

	// Send the response
	return s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
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
}
