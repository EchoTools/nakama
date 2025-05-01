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
	"github.com/heroiclabs/nakama-common/runtime"
)

type WhoAmI struct{}

// basic account Detail
func (w *WhoAmI) createUserAccountDetailsEmbed(a *EVRProfile, loginHistory *LoginHistory, matchmakingSettings *MatchmakingSettings, displayNameHistory *DisplayNameHistory, guildGroups map[string]*GuildGroup, groupID string, showLoginsSince time.Time, stripIPAddresses bool, includePriviledged bool) *discordgo.MessageEmbed {

	var (
		currentDisplayNameByGroupID = displayNameHistory.LatestByGroupID()
		lastSeen                    string
		partyGroupName              string
		defaultMatchmakingGuildName string
		recentLogins                string
		linkedDevices               string
	)

	// If the player is online, set the last seen time to now.
	// If the player is offline, set the last seen time to the last login time.
	if a.IsOnline() {
		lastSeen = "Now"
	} else {
		lastSeen = fmt.Sprintf("<t:%d:R>", loginHistory.LastSeen().UTC().Unix())
	}

	// Build a list of the players active display names, by guild group
	activeDisplayNames := make([]string, 0, len(currentDisplayNameByGroupID))
	for gid, dn := range currentDisplayNameByGroupID {
		if gg, ok := guildGroups[gid]; ok {
			guildName := EscapeDiscordMarkdown(gg.Name())
			activeDisplayNames = append(activeDisplayNames, fmt.Sprintf("%s: `%s`", guildName, EscapeDiscordMarkdown(dn)))

		}
	}
	slices.Sort(activeDisplayNames)

	if includePriviledged {

		// Build a list of the players linked devices/XPIDs
		xpidStrs := make([]string, 0, len(a.LinkedXPIDs()))
		for _, xpid := range a.LinkedXPIDs() {
			if stripIPAddresses {
				xpidStrs = append(xpidStrs, xpid.String())
			}
		}
		linkedDevices = strings.Join(xpidStrs, "\n")

		if gg, ok := guildGroups[a.GetActiveGroupID().String()]; ok {
			defaultMatchmakingGuildName = EscapeDiscordMarkdown(gg.Name())
		}

		partyGroupName = "not set"
		if matchmakingSettings != nil && matchmakingSettings.LobbyGroupName != "" {
			partyGroupName = "`" + matchmakingSettings.LobbyGroupName + "`"
		}

		recentLogins = w.createRecentLoginsFieldValue(loginHistory, stripIPAddresses, showLoginsSince)
	}

	passwordState := ""
	if includePriviledged {
		if a.HasPasswordSet() {
			passwordState = "Yes"
		} else {
			passwordState = "No"
		}
	}

	embed := &discordgo.MessageEmbed{
		Description: fmt.Sprintf("<@%s>", a.DiscordID()),
		Color:       0x0000CC,
		Author: &discordgo.MessageEmbedAuthor{
			Name: "EchoVRCE Account",
		},
		Fields: []*discordgo.MessageEmbedField{
			{
				Name:   "Username",
				Value:  a.Username(),
				Inline: true,
			},
			{
				Name:   "Discord ID",
				Value:  a.DiscordID(),
				Inline: true,
			},
			{
				Name:   "Password Set",
				Value:  passwordState,
				Inline: true,
			},
			{
				Name:   "Created",
				Value:  fmt.Sprintf("<t:%d:R>", a.CreatedAt().UTC().Unix()),
				Inline: true,
			},
			{
				Name:   "Last Seen",
				Value:  lastSeen,
				Inline: true,
			},
			{
				Name:   "Public Matchmaking Guild",
				Value:  defaultMatchmakingGuildName,
				Inline: true,
			},
			{
				Name:   "Party Group",
				Value:  partyGroupName,
				Inline: true,
			},
			{
				Name:   "Linked Devices",
				Value:  linkedDevices,
				Inline: false,
			},
			{
				Name:   "Recent Logins",
				Value:  recentLogins,
				Inline: false,
			},
			{
				Name:   "Display Names",
				Value:  strings.Join(activeDisplayNames, "\n"),
				Inline: false,
			},
			{
				Name:   "Guild Memberships",
				Value:  w.createGuildMembershipsEmbedFieldValue(guildGroups, a.UserID(), includePriviledged),
				Inline: false,
			},
		},
		Footer: &discordgo.MessageEmbedFooter{
			Text: fmt.Sprintf("Nakama ID: %s", a.UserID()),
		},
	}

	if a.IsDisabled() {
		embed.Color = 0xFF0000
		embed.Fields = append(embed.Fields, &discordgo.MessageEmbedField{
			Name:   "Account Status",
			Value:  "Disabled",
			Inline: true,
		})
	}

	return embed
}

func (WhoAmI) createRecentLoginsFieldValue(loginHistory *LoginHistory, stripIPAddresses bool, since time.Time) string {
	if loginHistory == nil || len(loginHistory.History) == 0 {
		return ""
	}
	// Include the recent logins
	lines := make([]string, 0, len(loginHistory.History))
	for _, e := range loginHistory.History {
		if e.UpdatedAt.Before(since) {
			continue
		}

		lines = append(lines, fmt.Sprintf("<t:%d:R> - `%s`", e.UpdatedAt.UTC().Unix(), e.XPID.String()))
	}
	slices.Sort(lines)
	slices.Reverse(lines)

	return strings.Join(lines, "\n")
}

func (WhoAmI) createMatchmakingEmbed(account *EVRProfile, guildGroups map[string]*GuildGroup, matchmakingSettings *MatchmakingSettings, labels []*MatchLabel, lastMatchmakingError error) *discordgo.MessageEmbed {
	if matchmakingSettings == nil {
		return nil
	}

	matchList := make([]string, 0, len(labels))

	for _, l := range labels {
		guildName := ""
		if gg, ok := guildGroups[l.GetGroupID().String()]; ok {
			guildName = EscapeDiscordMarkdown(gg.Name())
		}

		link := fmt.Sprintf("https://echo.taxi/spark://c/%s", strings.ToUpper(l.ID.UUID.String()))
		players := make([]string, 0, len(l.Players))
		for _, p := range l.Players {
			players = append(players, fmt.Sprintf("<@%s>", p.DiscordID))
		}
		line := fmt.Sprintf("%s (%s)- %s\n%s", guildName, l.Mode.String(), link, strings.Join(players, ", "))
		matchList = append(matchList, line)
	}

	if len(matchList) == 0 && lastMatchmakingError == nil {
		return nil
	}
	lastMatchmakingErrorStr := ""
	if lastMatchmakingError != nil {
		lastMatchmakingErrorStr = lastMatchmakingError.Error()
	}

	embed := &discordgo.MessageEmbed{
		Title: "Matchmaking",
		Fields: []*discordgo.MessageEmbedField{

			{
				Name:   "Match List",
				Value:  strings.Join(matchList, "\n"),
				Inline: false,
			},
			{
				Name:   "Last Matchmaking Error",
				Value:  lastMatchmakingErrorStr,
				Inline: true,
			},
		},
	}

	return embed
}

func (WhoAmI) createPastDisplayNameEmbed(history *DisplayNameHistory, groupID string, activeOnly bool) *discordgo.MessageEmbed {
	if history == nil || len(history.Histories) == 0 {
		return nil
	}

	displayNameMap := make(map[string]time.Time)

	for gid, items := range history.Histories {
		if groupID != "" && gid != groupID {
			continue
		}
		for dn, ts := range items {
			dn = EscapeDiscordMarkdown(dn)
			if e, ok := displayNameMap[dn]; !ok || e.After(ts) {
				displayNameMap[dn] = e
			}
		}
	}

	displayNames := make([]string, 0, len(displayNameMap))
	for dn := range displayNameMap {
		displayNames = append(displayNames, dn)
	}

	slices.SortStableFunc(displayNames, func(a, b string) int {
		return int(displayNameMap[a].Unix() - displayNameMap[b].Unix())
	})

	if len(displayNames) > 10 {
		displayNames = displayNames[len(displayNames)-10:]
	}

	if len(displayNames) == 0 {
		return nil
	}

	return &discordgo.MessageEmbed{
		Title:  "Past Display Names",
		Fields: []*discordgo.MessageEmbedField{{Name: "Display Names", Value: strings.Join(displayNames, "\n"), Inline: false}},
	}
}

func (*WhoAmI) createGuildMembershipsEmbedFieldValue(guildGroupMap map[string]*GuildGroup, userIDStr string, includePriviledged bool) string {
	if len(guildGroupMap) == 0 {
		return ""
	}

	guildGroups := make([]*GuildGroup, 0, len(guildGroupMap))
	for _, group := range guildGroupMap {
		guildGroups = append(guildGroups, group)
	}

	sort.SliceStable(guildGroups, func(i, j int) bool {
		return guildGroups[i].Name() < guildGroups[j].Name()
	})

	lines := make([]string, 0, len(guildGroups))

	for _, group := range guildGroups {

		groupStr := group.Name()

		if includePriviledged {
			// Add the roles
			activeRoleMap := map[string]bool{
				"owner":          group.IsOwner(userIDStr),
				"matchmaking":    group.IsAllowedMatchmaking(userIDStr),
				"auditor":        group.IsAuditor(userIDStr),
				"enforcer":       group.IsEnforcer(userIDStr),
				"server-host":    group.IsServerHost(userIDStr),
				"allocator":      group.IsAllocator(userIDStr),
				"api-access":     group.IsAPIAccess(userIDStr),
				"suspended":      group.IsSuspended(userIDStr, nil),
				"vpn-bypass":     group.IsVPNBypass(userIDStr),
				"limited-access": group.IsLimitedAccess(userIDStr),
			}

			roles := make([]string, 0, len(activeRoleMap))
			for role, ok := range activeRoleMap {
				if ok {
					roles = append(roles, role)
				}
			}

			slices.SortStableFunc(roles, func(a, b string) int {
				return int(slices.Index([]string{"owner", "enforcer", "auditor", "server-host", "allocator", "matchmaking", "api-access", "vpn-bypass", "limited-access", "suspended"}, a) - slices.Index([]string{"owner", "enforcer", "auditor", "server-host", "allocator", "api-access", "suspended", "vpn-bypass", "limited-access", "matchmaking"}, b))
			})

			if len(roles) > 0 {
				groupStr += fmt.Sprintf(" (%s)", strings.Join(roles, ", "))
			}
		}

		lines = append(lines, groupStr)
	}

	return strings.Join(lines, "\n")

}

func (*WhoAmI) createSuspensionsEmbed(enforcementRecordsByGroupName map[string][]*GuildEnforcementRecord) *discordgo.MessageEmbed {
	if enforcementRecordsByGroupName == nil {
		return nil
	}

	seenIDs := make(map[string]bool, 0)
	fields := make([]*discordgo.MessageEmbedField, 0, len(enforcementRecordsByGroupName))
	for gName, records := range enforcementRecordsByGroupName {

		// Don't duplicate records
		for i := 0; i < len(records); i++ {
			if seenIDs[records[i].ID] {
				records = append(records[:i], records[i+1:]...)
				i--
				continue
			}
			seenIDs[records[i].ID] = true
		}
		field := createSuspensionDetailsEmbedField(gName, records)
		if field == nil {
			continue
		}
		fields = append(fields, field)
	}

	if len(fields) == 0 {
		return nil
	}

	// Sort the fields by group name
	sort.SliceStable(fields, func(i, j int) bool {
		return fields[i].Name < fields[j].Name
	})

	return &discordgo.MessageEmbed{
		Title:  "Suspensions",
		Color:  0xFF0000,
		Fields: fields,
	}
}

func (d *DiscordAppBot) handleProfileRequest(ctx context.Context, logger runtime.Logger, nk runtime.NakamaModule, s *discordgo.Session, i *discordgo.InteractionCreate, target *discordgo.User, includePriviledged bool, includePrivate bool, includeGuildAuditor bool, includeSystem bool, includeSuspensions bool, includeInactiveSuspensions bool, includeCurrentMatches bool) error {

	var (
		callerID            = d.cache.DiscordIDToUserID(i.Member.User.ID)
		targetID            = d.cache.DiscordIDToUserID(target.ID)
		whoami              = &WhoAmI{}
		err                 error
		evrAccount          *EVRProfile
		matchmakingSettings *MatchmakingSettings
		loginHistory        *LoginHistory
		displayNameHistory  *DisplayNameHistory
		embeds              = make([]*discordgo.MessageEmbed, 0, 4)
		groupID             = d.cache.GuildIDToGroupID(i.GuildID)
		showLoginsSince     = time.Now().Add(time.Hour * 24 * -30) // 30 days
		stripIPAddresses    = !includePrivate
		matchmakingEmbed    *discordgo.MessageEmbed
	)

	if includeSystem {
		showLoginsSince = time.Time{}
	}

	if callerID == "" || targetID == "" {
		return fmt.Errorf("invalid user ID")
	}

	if a, err := nk.AccountGetId(ctx, targetID); err != nil {
		return fmt.Errorf("failed to get account by ID: %w", err)
	} else if a.GetDisableTime() != nil && !includePriviledged {
		return fmt.Errorf("account is disabled")
	} else if evrAccount, err = BuildEVRProfileFromAccount(a); err != nil {
		return fmt.Errorf("failed to get account by ID: %w", err)
	}

	loginHistory = NewLoginHistory(targetID)
	if err := StorageRead(ctx, nk, targetID, loginHistory, true); err != nil {
		return fmt.Errorf("error getting device history: %w", err)
	}

	if displayNameHistory, err = DisplayNameHistoryLoad(ctx, nk, targetID); err != nil {
		return fmt.Errorf("failed to load display name history: %w", err)
	}
	guildGroups, err := GuildUserGroupsList(ctx, nk, d.guildGroupRegistry, targetID)
	if err != nil {
		return fmt.Errorf("error getting guild groups: %w", err)
	}

	// Filter the guild groups based on the includePrivate flag
	if !includePrivate {
		for gid, g := range guildGroups {
			if g.GuildID != i.GuildID {
				delete(guildGroups, gid)
			}
		}
	}

	enforcementRecordsByGroupName := make(map[string][]*GuildEnforcementRecord, 0)

	if includeSuspensions {

		enforcementGroupIDs := make([]string, 0, len(guildGroups))
		for _, g := range guildGroups {
			enforcementGroupIDs = append(enforcementGroupIDs, g.GuildID)
			if len(g.SuspensionInheritanceGroupIDs) > 0 {
				enforcementGroupIDs = append(enforcementGroupIDs, g.SuspensionInheritanceGroupIDs...)
			}
		}

		if results, err := EnforcementJournalSearch(ctx, nk, enforcementGroupIDs, targetID); err != nil {
			logger.Warn("failed to get enforcement records", "error", err)
		} else {

			enforcementRecordsByID := make(map[string][]*GuildEnforcementRecord, 0)

			for _, journal := range results {
				for _, record := range journal.Records {
					if r, ok := enforcementRecordsByID[record.ID]; !ok {
						enforcementRecordsByID[record.ID] = []*GuildEnforcementRecord{record}
					} else {
						enforcementRecordsByID[record.ID] = append(r, record)
					}
				}
			}

			// Merge the records by ID
			enforcementRecords := make([]*GuildEnforcementRecord, 0, len(enforcementRecordsByID))
			for _, records := range enforcementRecordsByID {
				if len(records) == 0 {
					continue
				}
				record := records[0]
				for _, r := range records[1:] {
					record.Merge(r)
				}

				enforcementRecords = append(enforcementRecords, record)
			}

			if !includeInactiveSuspensions {
				for i := 0; i < len(enforcementRecords); i++ {
					r := enforcementRecords[i]
					if !r.IsActive() {
						enforcementRecords = append(enforcementRecords[:i], enforcementRecords[i+1:]...)
						i--
					}
				}
			}

			if !includeGuildAuditor {
				for _, journal := range results {
					// clear the notes
					for _, r := range journal.Records {
						r.EnforcerDiscordID = ""
						r.EnforcerUserID = ""
						r.AuditorNotes = ""
					}
				}
			}

			// Sort by updated time
			sort.SliceStable(enforcementRecords, func(i, j int) bool {
				return enforcementRecords[i].UpdatedAt.Before(enforcementRecords[j].UpdatedAt)
			})

			// Build the map
			for _, record := range enforcementRecords {
				gName := ""
				if gg, ok := guildGroups[record.GroupID]; ok {
					gName = EscapeDiscordMarkdown(gg.Name())
				} else {
					if gg := d.guildGroupRegistry.Get(record.GroupID); gg != nil {
						gName = EscapeDiscordMarkdown(gg.Name())
					} else {
						gName = record.GroupID
					}
				}

				// Add the records to the map
				if _, ok := enforcementRecordsByGroupName[gName]; !ok {
					enforcementRecordsByGroupName[gName] = []*GuildEnforcementRecord{}
				}
				enforcementRecordsByGroupName[gName] = append(enforcementRecordsByGroupName[gName], enforcementRecords...)
			}
		}
	}

	settings, err := LoadMatchmakingSettings(ctx, nk, targetID)
	if err != nil {
		return fmt.Errorf("failed to load matchmaking settings: %w", err)
	}
	matchmakingSettings = &settings

	if includeCurrentMatches {
		// Get the players current lobby sessions
		presences, err := d.nk.StreamUserList(StreamModeService, targetID, "", StreamLabelMatchService, false, true)
		if err != nil {
			return err
		}

		matchLabels := make([]*MatchLabel, 0, len(presences))
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

				matchLabels = append(matchLabels, label)
			}
		}

		var lastMatchmakingError error
		// If the player is online, Get the most recent matchmaking error for the player.
		if len(presences) > 0 {
			// Get the most recent matchmaking error for the player
			if session := d.pipeline.sessionRegistry.Get(uuid.FromStringOrNil(presences[0].GetSessionId())); session != nil {
				params, ok := LoadParams(session.Context())
				if ok {
					lastMatchmakingError = params.lastMatchmakingError.Load()
				}
			}
		}

		matchmakingEmbed = whoami.createMatchmakingEmbed(evrAccount, guildGroups, matchmakingSettings, matchLabels, lastMatchmakingError)
	}

	var potentialAlternates []string

	if includeSystem {
		potentialAlternates = make([]string, 0, len(loginHistory.AlternateMap))
		for altUserID, matches := range loginHistory.AlternateMap {
			altAccount, err := nk.AccountGetId(ctx, altUserID)
			if err != nil {
				return fmt.Errorf("failed to get account by ID: %w", err)
			}
			state := ""
			if altAccount.GetDisableTime() != nil {
				state = "disabled"
			}

			items := make([]string, 0, len(matches))
			for _, m := range matches {
				items = append(items, m.Items...)
			}
			slices.Sort(items)
			items = slices.Compact(items)

			s := fmt.Sprintf("<@%s> [%s] %s <t:%d:R>\n", altAccount.CustomId, altAccount.User.Username, state, altAccount.User.UpdateTime.AsTime().UTC().Unix())

			for _, item := range items {
				s += fmt.Sprintf("-  `%s`\n", item)
			}

			potentialAlternates = append(potentialAlternates, s)
		}
	}

	// Create the account details embed
	embeds = append(embeds,
		whoami.createUserAccountDetailsEmbed(evrAccount, loginHistory, matchmakingSettings, displayNameHistory, guildGroups, groupID, showLoginsSince, stripIPAddresses, includePriviledged),
		whoami.createSuspensionsEmbed(enforcementRecordsByGroupName),
		whoami.createPastDisplayNameEmbed(displayNameHistory, groupID, false),
		matchmakingEmbed,
	)

	if len(potentialAlternates) > 0 {
		embeds = append(embeds, &discordgo.MessageEmbed{
			Title:  "Potential Alternate Accounts",
			Color:  0xFF00FF,
			Fields: []*discordgo.MessageEmbedField{{Name: "Potential Alternates", Value: strings.Join(potentialAlternates, "\n"), Inline: false}},
		})
	}

	// Remove any nil or blank embeds/fields
	for i := 0; i < len(embeds); i++ {
		if embeds[i] == nil || embeds[i].Fields == nil {
			embeds = slices.Delete(embeds, i, i+1)
			i--
			continue
		}

		for j := 0; j < len(embeds[i].Fields); j++ {
			if embeds[i].Fields[j] == nil || embeds[i].Fields[j].Name == "" || embeds[i].Fields[j].Value == "" {
				embeds[i].Fields = slices.Delete(embeds[i].Fields, j, j+1)
				j--
				continue
			}
		}
	}

	// Send the response
	return s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseChannelMessageWithSource,
		Data: &discordgo.InteractionResponseData{
			Flags:  discordgo.MessageFlagsEphemeral,
			Embeds: embeds,
		},
	})
}

/*
	presences, err := d.nk.StreamUserList(StreamModeService, userID.String(), "", StreamLabelMatchService, false, true)
	if err != nil {
		return err
	}

	if includePriviledged {
		whoami.DefaultLobbyGroup = evrAccount.GetActiveGroupID().String()
	}

	if includePriviledged {
		// Get the device links from the account
		whoami.DeviceLinks = make([]string, 0, len(account.GetDevices()))
		for _, device := range account.GetDevices() {
			whoami.DeviceLinks = append(whoami.DeviceLinks, fmt.Sprintf("`%s`", device.GetId()))
		}

		whoami.HasPassword = account.GetEmail() != ""
	}

	guildGroups, err := GuildUserGroupsList(ctx, nk, d.guildGroupRegistry, userID.String())
	if err != nil {
		return fmt.Errorf("error getting guild groups: %w", err)
	}

	for _, g := range guildGroups {
		if includePrivate || g.GuildID == i.GuildID {
			whoami.GuildGroups = append(whoami.GuildGroups, g)
		}
	}

	loginHistory := &LoginHistory{}
	if err := StorageRead(ctx, nk, userID.String(), loginHistory, true); err != nil {
		return fmt.Errorf("error getting device history: %w", err)
	}

	whoami.RecentLogins = make(map[string]time.Time, 0)

	if includePriviledged {
		for k, e := range loginHistory.History {
			if !includePrivate {
				// Remove the IP address
				k = e.XPID.String()
			}
			whoami.RecentLogins[k] = e.UpdatedAt.UTC()

		}
	}

	displayNameHistory, err := DisplayNameHistoryLoad(ctx, nk, userID.String())
	if err != nil {
		return fmt.Errorf("failed to load display name history: %w", err)
	}
	_ = displayNameHistory
	pastDisplayNames := make(map[string]time.Time)

	for groupID, items := range displayNameHistory.Histories {
		if !includePriviledged && groupID != i.GuildID {
			continue
		}
		for dn, ts := range items {
			if e, ok := pastDisplayNames[dn]; !ok || e.After(ts) {
				pastDisplayNames[dn] = ts
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

	if len(whoami.DisplayNames) > 10 {
		whoami.DisplayNames = whoami.DisplayNames[len(whoami.DisplayNames)-10:]
	}

	if !includePriviledged && len(whoami.DisplayNames) > 1 {
		// Only show the most recent display name
		whoami.DisplayNames = whoami.DisplayNames[len(whoami.DisplayNames)-1:]
	}

	if includePriviledged {

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
					whoami.LastMatchmakingError = params.lastMatchmakingError.Load()
				}
			}
		}
	}

	if includePriviledged {
		friends, err := ListPlayerFriends(ctx, RuntimeLoggerToZapLogger(logger), d.db, d.statusRegistry, userID)
		if err != nil {
			return err
		}

		ghostedDiscordIDs := make([]string, 0)
		for _, f := range friends {
			if api.Friend_State(f.GetState().Value) == api.Friend_BLOCKED {
				discordID := d.cache.UserIDToDiscordID(f.GetUser().GetId())
				ghostedDiscordIDs = append(ghostedDiscordIDs, discordID)
			}
		}
		whoami.GhostedPlayers = ghostedDiscordIDs
	}

	vrmlPlayerSummary := &VRMLPlayerSummary{}
	if includePriviledged {

		// Get VRML Summary
		if err := StorageRead(ctx, nk, userID.String(), vrmlPlayerSummary, false); err == nil {

			whoami.MatchCountsBySeason = make(map[string]int, 0)

			for sID, teams := range vrmlPlayerSummary.MatchCountsBySeasonByTeam {
				for _, n := range teams {
					seasonName, ok := vrmlSeasonDescriptionMap[sID]
					if !ok {
						seasonName = string(sID)
					}

					whoami.MatchCountsBySeason[seasonName] += n
				}
			}
		}

	}

	if includeSystem {
		for altUserID, matches := range loginHistory.AlternateMap {
			altAccount, err := nk.AccountGetId(ctx, altUserID)
			if err != nil {
				return fmt.Errorf("failed to get account by ID: %w", err)
			}
			state := ""
			if altAccount.GetDisableTime() != nil {
				state = "disabled"
			}

			items := make([]string, 0, len(matches))
			for _, m := range matches {
				items = append(items, m.Items...)
			}
			slices.Sort(items)
			items = slices.Compact(items)

			s := fmt.Sprintf("<@%s> [%s] %s <t:%d:R>\n", altAccount.CustomId, altAccount.User.Username, state, altAccount.User.UpdateTime.AsTime().UTC().Unix())

			for _, item := range items {
				s += fmt.Sprintf("-  `%s`\n", item)
			}

			whoami.PotentialAlternates = append(whoami.PotentialAlternates, s)
		}
	}

	accountFields := []*discordgo.MessageEmbedField{
		{Name: "Nakama ID", Value: whoami.NakamaID.String(), Inline: true},
		{Name: "Username", Value: whoami.Username, Inline: true},
		{Name: "Discord ID", Value: whoami.DiscordID, Inline: true},
		{Name: "Created", Value: fmt.Sprintf("<t:%d:R>", whoami.CreateTime.Unix()), Inline: true},
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
		}(), Inline: true},
		func() *discordgo.MessageEmbedField {
			if includePriviledged {
				matchmakingSettings, err := LoadMatchmakingSettings(ctx, nk, whoami.NakamaID.String())
				if err != nil {
					return nil
				}
				name := "not set"
				if matchmakingSettings.LobbyGroupName != "" {
					name = fmt.Sprintf("`%s`", matchmakingSettings.LobbyGroupName)
				}

				return &discordgo.MessageEmbedField{
					Name:   "Party Group",
					Value:  name,
					Inline: false,
				}
			}
			return nil
		}(),
		{Name: "Linked Devices", Value: strings.Join(whoami.DeviceLinks, "\n"), Inline: false},
		{Name: "Display Names", Value: strings.Join(whoami.DisplayNames, "\n"), Inline: false},
		{Name: "Recent Logins", Value: func() string {
			lines := lo.MapToSlice(whoami.RecentLogins, func(k string, v time.Time) string {
				if v.IsZero() {
					// Don't use the timestamp
					return k
				} else {
					return fmt.Sprintf("<t:%d:R> - %s", v.UTC().Unix(), k)
				}
			})
			slices.Sort(lines)
			slices.Reverse(lines)
			return strings.Join(lines, "\n")
		}(), Inline: false},
		{Name: "VRML Player", Value: func() string {
			if vrmlPlayerSummary == nil {
				return ""
			}
			if vrmlPlayerSummary.Player == nil {
				return ""
			}

			return fmt.Sprintf("[%s](https://vrmasterleague.com/EchoArena/Players/%s)", vrmlPlayerSummary.Player.User.UserName, vrmlPlayerSummary.Player.ThisGame.PlayerID)
		}(), Inline: true},
		{Name: "VRML Match Counts", Value: func() string {

			lines := make([]string, 0, len(whoami.MatchCountsBySeason))
			for season, count := range whoami.MatchCountsBySeason {
				lines = append(lines, fmt.Sprintf("%s: %d", season, count))
			}

			slices.Sort(lines)
			slices.Reverse(lines)
			return strings.Join(lines, "\n")
		}(), Inline: false},
		{Name: "Guild Memberships", Value: strings.Join(func() []string {
			output := make([]string, 0, len(whoami.GuildGroups))

			sort.SliceStable(whoami.GuildGroups, func(i, j int) bool {
				return whoami.GuildGroups[i].Name() < whoami.GuildGroups[j].Name()
			})

			for _, group := range whoami.GuildGroups {

				sameGuild := group.GuildID == i.GuildID
				groupStr := group.Name()

				if includePrivate || (includePriviledged && sameGuild) {

					// Add the roles
					activeRoleMap := map[string]bool{
						"matchmaking":    group.IsAllowedMatchmaking(userIDStr),
						"auditor":        group.IsAuditor(userIDStr),
						"enforcer":       group.IsEnforcer(userIDStr),
						"server-host":    group.IsServerHost(userIDStr),
						"allocator":      group.IsAllocator(userIDStr),
						"api-access":     group.IsAPIAccess(userIDStr),
						"suspended":      group.IsSuspended(userIDStr, nil),
						"vpn-bypass":     group.IsVPNBypass(userIDStr),
						"limited-access": group.IsLimitedAccess(userIDStr),
					}

					if g, err := dg.State.Guild(group.GuildID); err == nil {
						if whoami.DiscordID == g.OwnerID {
							activeRoleMap["owner"] = true
						}
					}

					roles := make([]string, 0, len(activeRoleMap))
					for role, ok := range activeRoleMap {
						if ok {
							roles = append(roles, role)
						}
					}

					slices.SortStableFunc(roles, func(a, b string) int {
						return int(slices.Index([]string{"owner", "enforcer", "auditor", "server-host", "allocator", "matchmaking", "api-access", "vpn-bypass", "limited-access", "suspended"}, a) - slices.Index([]string{"owner", "enforcer", "auditor", "server-host", "allocator", "api-access", "suspended", "vpn-bypass", "limited-access", "matchmaking"}, b))
					})

					if len(roles) > 0 {
						groupStr += fmt.Sprintf(" (%s)", strings.Join(roles, ", "))
					}
				}

				output = append(output, groupStr)
			}
			return output
		}(), "\n"), Inline: false},
		{Name: "Match List", Value: strings.Join(lo.Map(whoami.MatchLabels, func(l *MatchLabel, index int) string {
			link := fmt.Sprintf("https://echo.taxi/spark://c/%s", strings.ToUpper(l.ID.UUID.String()))
			players := make([]string, 0, len(l.Players))
			for _, p := range l.Players {
				players = append(players, fmt.Sprintf("<@%s>", p.DiscordID))
			}

			return fmt.Sprintf("%s (%s)- %s\n%s", d.cache.GuildGroupName(l.GetGroupID().String()), l.Mode.String(), link, strings.Join(players, ", "))
		}), "\n"), Inline: false},
	}

	if len(whoami.PotentialAlternates) > 0 {
		accountFields = append(accountFields, &discordgo.MessageEmbedField{
			Name:   "Potential Alternate Accounts",
			Value:  strings.Join(whoami.PotentialAlternates, "\n"),
			Inline: false,
		})
	}

	accountFields = append(accountFields, &discordgo.MessageEmbedField{
		Name: "Default Matchmaking Guild",
		Value: func() string {
			if whoami.DefaultLobbyGroup != "" {
				for _, group := range whoami.GuildGroups {
					if group.GuildID == whoami.DefaultLobbyGroup {
						return group.Name()
					}
				}
			}
			return ""
		}(),
		Inline: false,
	})

	if whoami.LastMatchmakingError != nil {
		accountFields = append(accountFields, &discordgo.MessageEmbedField{
			Name:   "Last Matchmaking Error",
			Value:  whoami.LastMatchmakingError.Error(),
			Inline: false,
		})
	}

	// Remove any blank fields, and truncate to 800 characters
	accountFields = lo.Filter(accountFields, func(f *discordgo.MessageEmbedField, _ int) bool {
		if f == nil || f.Name == "" || f.Value == "" {
			return false
		}

		if len(f.Value) > 800 {
			f.Value = f.Value[:800]
		}

		return true
	})

	embeds := []*discordgo.MessageEmbed{
		{
			Title:  "EchoVRCE Account",
			Color:  0xCCCCCC,
			Fields: accountFields,
		},
	}

	callerUserID := d.cache.DiscordIDToUserID(i.Member.User.ID)

	// Add Suspension Embed
	if includePriviledged || includeGuildAuditor || includeSystem {
		fields := make([]*discordgo.MessageEmbedField, 0, len(whoami.GuildGroups))
		if guildRecords, err := EnforcementSuspensionSearch(ctx, nk, "", []string{userID.String()}, false); err == nil {
			for groupID, byUserID := range guildRecords {
				gg, ok := guildGroups[groupID]
				if !ok {
					continue
				}
				if len(byUserID) == 0 {
					continue
				}

				includeNotes := false
				if includeSystem || gg.IsEnforcer(callerUserID) {
					includeNotes = true
				}

				for _, records := range byUserID {
					field := createSuspensionDetailsEmbedField(gg.Name(), records.Records, includeNotes)

					fields = append(fields, field)

				}

			}
		}

		if len(fields) > 0 {
			embeds = append(embeds, &discordgo.MessageEmbed{
				Title:  "Suspensions",
				Color:  0xFF0000,
				Fields: fields,
			})
		}
	}

	// Send the response
	return s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseChannelMessageWithSource,
		Data: &discordgo.InteractionResponseData{
			Flags:  discordgo.MessageFlagsEphemeral,
			Embeds: embeds,
		},
	})
}
*/
