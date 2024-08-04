package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/bwmarrin/discordgo"
	"github.com/gofrs/uuid/v5"
	"github.com/heroiclabs/nakama-common/runtime"
	"github.com/heroiclabs/nakama/v3/server/evr"
	"github.com/samber/lo"
	"go.uber.org/zap"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func (d *DiscordAppBot) handleInteractionCreate(logger runtime.Logger, s *discordgo.Session, i *discordgo.InteractionCreate, commandName string, commandFn DiscordCommandHandlerFn) error {
	ctx := d.ctx
	db := d.db

	user, member := getScopedUserMember(i)

	if user == nil {
		return fmt.Errorf("user is nil")
	}
	// Check if the interaction is a command
	if i.Type != discordgo.InteractionApplicationCommand {
		// Handle the command
		logger.WithField("type", i.Type).Warn("Unhandled interaction type")
		return nil
	}

	var userID string
	var err error

	switch commandName {
	case "whoami", "link-headset":

		// Authenticate/create an account.
		userID, err = GetUserIDByDiscordID(ctx, d.db, user.ID)
		if err != nil {
			userID, _, _, err = d.nk.AuthenticateCustom(ctx, user.ID, user.Username, true)
			if err != nil {
				return fmt.Errorf("failed to authenticate (or create) user %s: %w", user.ID, err)
			}
		}

	case "trigger-cv", "kick-player":

		userID, err = GetUserIDByDiscordID(ctx, d.db, user.ID)
		if err != nil {
			return fmt.Errorf("a headsets must be linked to this Discord account to use slash commands")
		}
		if isGlobalModerator, err := CheckSystemGroupMembership(ctx, db, userID, GroupGlobalModerators); err != nil {
			return errors.New("failed to check global moderator status")
		} else if !isGlobalModerator {
			return simpleInteractionResponse(s, i, "You must be a global moderator to use this command.")

		}

	default:
		userID, err = GetUserIDByDiscordID(ctx, d.db, user.ID)
		if err != nil {
			return fmt.Errorf("a headsets must be linked to this Discord account to use slash commands")
		}

	}

	groupID, err := GetGroupIDByGuildID(ctx, d.db, i.GuildID)
	if err != nil {
		logger.Error("Failed to get guild ID", zap.Error(err))
	}
	return commandFn(logger, s, i, user, member, userID, groupID)
}

func (d *DiscordAppBot) handleProfileRequest(ctx context.Context, logger runtime.Logger, nk runtime.NakamaModule, s *discordgo.Session, discordRegistry DiscordRegistry, i *discordgo.InteractionCreate, discordID string, username string, guildID string, includePrivate bool) error {
	whoami := &WhoAmI{
		DiscordID:             discordID,
		EVRIDLogins:           make(map[string]time.Time),
		DisplayNames:          make([]string, 0),
		ClientAddresses:       make([]string, 0),
		DeviceLinks:           make([]string, 0),
		GuildGroupMemberships: make([]GuildGroupMembership, 0),
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

	if includePrivate {
		// Do some profile checks and cleanups

		// Synchronize the user's guilds with nakama groups
		err = d.discordRegistry.UpdateGuildGroup(ctx, logger, userID, i.GuildID)
		if err != nil {
			return fmt.Errorf("error updating guild groups: %v", err)
		}
	}

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

	var groupIDs []uuid.UUID
	if guildID != "" {
		groupID, err := GetGroupIDByGuildID(ctx, d.db, guildID)
		if err != nil {
			return fmt.Errorf("guild group not found")
		}
		if _, err := UpdateDisplayNameByGroupID(ctx, logger, d.db, nk, d.discordRegistry, userID.String(), groupID); err != nil {
			return err
		}

		groupIDs = []uuid.UUID{uuid.FromStringOrNil(groupID)}
	}

	whoami.GuildGroupMemberships, err = d.discordRegistry.GetGuildGroupMemberships(ctx, userID, groupIDs)
	if err != nil {
		return err
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
	displayNameObjs, err := GetDisplayNameRecords(ctx, logger, nk, userID.String())
	if err != nil {
		return err
	}
	// Sort displayNames by age
	sort.SliceStable(displayNameObjs, func(i, j int) bool {
		return displayNameObjs[i].GetUpdateTime().AsTime().After(displayNameObjs[j].GetUpdateTime().AsTime())
	})

	whoami.DisplayNames = make([]string, 0, len(displayNameObjs))
	for _, dn := range displayNameObjs {
		whoami.DisplayNames = append(whoami.DisplayNames, dn.Key)
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
			whoami.MatchIDs = append(whoami.MatchIDs, mid.UUID().String())
		}
	}

	// Get the suspensions
	dr := d.discordRegistry.(*LocalDiscordRegistry)
	suspensions, err := dr.GetAllSuspensions(ctx, userID)
	if err != nil {
		return err
	}

	suspensionLines := make([]string, 0, len(suspensions))
	for _, suspension := range suspensions {
		suspensionLines = append(suspensionLines, fmt.Sprintf("%s: %s", suspension.GuildName, suspension.RoleName))
	}
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
		whoami.MatchIDs = nil
	}
	fields := []*discordgo.MessageEmbedField{
		{Name: "Nakama ID", Value: whoami.NakamaID.String(), Inline: true},
		{Name: "Create Time", Value: fmt.Sprintf("<t:%d:R>", whoami.CreateTime.Unix()), Inline: false},
		{Name: "Password Protected", Value: func() string {
			if whoami.HasPassword {
				return "Yes"
			}
			return ""
		}(), Inline: true},
		{Name: "Discord ID", Value: whoami.DiscordID, Inline: false},
		{Name: "Username", Value: whoami.Username, Inline: true},
		{Name: "Display Names", Value: strings.Join(whoami.DisplayNames, "\n"), Inline: false},
		{Name: "Linked Devices", Value: strings.Join(whoami.DeviceLinks, "\n"), Inline: false},
		{Name: "Logins", Value: func() string {
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
		{Name: "Guild Memberships", Value: strings.Join(lo.Map(whoami.GuildGroupMemberships, func(m GuildGroupMembership, index int) string {
			s := m.GuildGroup.Name()
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
			if m.canAllocateNonDefault {
				roles = append(roles, "allocator")
			}
			if len(roles) > 0 {
				s += fmt.Sprintf(" (%s)", strings.Join(roles, ", "))
			}
			return s
		}), "\n"), Inline: false},
		{Name: "Suspensions", Value: strings.Join(suspensionLines, "\n"), Inline: false},
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

func (d *DiscordAppBot) handlePrepareMatch(ctx context.Context, logger runtime.Logger, discordID, guildID string, region, mode, level evr.Symbol, startTime time.Time) (*MatchLabel, error) {
	userID, err := GetUserIDByDiscordID(ctx, d.db, discordID)
	if err != nil {
		return nil, status.Errorf(codes.NotFound, "user not found: %s", discordID)
	}
	qparts := []string{
		"+label.lobby_type:unassigned",
	}

	// Find a parking match to prepare
	minSize := 1
	maxSize := 1

	groupID, err := GetGroupIDByGuildID(ctx, d.db, guildID)
	if err != nil {
		return nil, status.Errorf(codes.NotFound, "guild not found: %s", guildID)
	}

	qparts = append(qparts, "+label.broadcaster.group_id:%s", groupID)

	// Get a list of the groups that this user has allocate access to
	memberships, err := d.discordRegistry.GetGuildGroupMemberships(ctx, uuid.FromStringOrNil(userID), nil)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to get guild group memberships: %v", err)
	}
	requirePublic := true
	for _, membership := range memberships {
		if membership.GuildGroup.ID() == uuid.FromStringOrNil(groupID) && membership.canAllocateNonDefault {
			requirePublic = false
			break
		}
	}

	if requirePublic {
		qparts = append(qparts, "+label.broadcaster.regions:%s", evr.DefaultRegion.String())
	}
	if region != evr.DefaultRegion {
		qparts = append(qparts, "+label.broadcaster.regions:%s", region.String())
	}
	query := strings.Join(qparts, " ")

	matches, err := d.nk.MatchList(ctx, 100, true, "", &minSize, &maxSize, query)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to list matches: %v", err)
	}

	if len(matches) == 0 {
		return nil, status.Error(codes.NotFound, "no matches found")
	}

	// Pick a random result
	match := matches[rand.Intn(len(matches))]
	matchID := MatchIDFromStringOrNil(match.GetMatchId())
	gid := uuid.FromStringOrNil(groupID)
	// Prepare the session for the match.
	state := &MatchLabel{}
	state.SpawnedBy = userID
	state.StartTime = startTime.UTC()
	state.MaxSize = MatchMaxSize
	state.Mode = mode
	state.Open = true
	state.GroupID = &gid
	if !level.IsNil() {
		state.Level = level
	}
	nk_ := d.nk.(*RuntimeGoNakamaModule)
	response, err := SignalMatch(ctx, nk_.matchRegistry, matchID, SignalPrepareSession, state)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to signal match: %v", err)
	}

	label := MatchLabel{}
	if err := json.Unmarshal([]byte(response), &label); err != nil {
		return nil, status.Errorf(codes.Internal, "failed to unmarshal match label: %v", err)
	}

	return &label, nil

}
