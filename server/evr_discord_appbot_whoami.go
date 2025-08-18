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
	evr "github.com/echotools/nakama/v3/protocol"
	"github.com/gofrs/uuid/v5"
	"github.com/heroiclabs/nakama-common/api"
	"github.com/heroiclabs/nakama-common/runtime"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type UserProfileRequestOptions struct {
	IncludeSuspensionsEmbed      bool
	IncludePastSuspensions       bool
	IncludeCurrentMatchesEmbed   bool
	IncludeVRMLHistoryEmbed      bool
	IncludePastDisplayNamesEmbed bool
	IncludeAlternatesEmbed       bool

	IncludeDiscordDisplayName      bool
	IncludeSuspensionAuditorNotes  bool
	IncludeInactiveSuspensions     bool
	ErrorIfAccountDisabled         bool
	IncludePartyGroupName          bool
	IncludeDefaultMatchmakingGuild bool
	IncludeLinkedDevices           bool
	StripIPAddresses               bool
	IncludeRecentLogins            bool
	IncludePasswordSetState        bool
	IncludeGuildRoles              bool
	IncludeAllGuilds               bool
	ShowLoginsSince                time.Time
	SendFileOnError                bool // If true, send a file with the error message instead of an ephemeral message
}

const (
	WhoAmISecondaryColor = 0x00CC00 // Green
	WhoAmIBaseColor      = 0xFFA500 // Orange
	WhoAmISystemColor    = 0x800080 // Purple
)

type WhoAmI struct {
	GroupID string

	loginHistory        *LoginHistory
	profile             *EVRProfile
	guildGroups         map[string]*GuildGroup
	matchmakingSettings *MatchmakingSettings
	displayNameHistory  *DisplayNameHistory
	journal             *GuildEnforcementJournal
	activeSuspensions   ActiveGuildEnforcements
	potentialAlternates map[string]*api.Account
	opts                UserProfileRequestOptions
}

func NewWhoAmI(ctx context.Context, logger runtime.Logger, nk runtime.NakamaModule, guildGroupRegistry *GuildGroupRegistry, cache *DiscordIntegrator, profile *EVRProfile, loginHistory *LoginHistory, matchmakingSettings *MatchmakingSettings, guildGroups map[string]*GuildGroup, displayNameHistory *DisplayNameHistory, journal *GuildEnforcementJournal, potentialAlternates map[string]*api.Account, activeSuspensions ActiveGuildEnforcements, opts UserProfileRequestOptions, groupID string) *WhoAmI {

	return &WhoAmI{
		GroupID: groupID,

		loginHistory:        loginHistory,
		profile:             profile,
		guildGroups:         guildGroups,
		matchmakingSettings: matchmakingSettings,
		displayNameHistory:  displayNameHistory,
		journal:             journal,
		activeSuspensions:   activeSuspensions,
		potentialAlternates: potentialAlternates,
		opts:                opts,
	}
}

// basic account Detail
func (w *WhoAmI) createUserAccountDetailsEmbed() *discordgo.MessageEmbed {

	var (
		currentDisplayNameByGroupID = w.profile.DisplayNamesByGroupID()
		lastSeen                    string
		partyGroupName              string
		defaultMatchmakingGuildName string
		recentLogins                string
		linkedDevices               string
	)

	// If the player is online, set the last seen time to now.
	// If the player is offline, set the last seen time to the last login time.
	if w.profile.IsOnline() {
		lastSeen = "Now"
	} else {
		lastSeen = fmt.Sprintf("<t:%d:R>", w.loginHistory.LastSeen().UTC().Unix())
	}

	// Build a list of the players active display names, by guild group
	activeDisplayNames := make([]string, 0, len(currentDisplayNameByGroupID))
	for gid, dn := range currentDisplayNameByGroupID {
		if gg, ok := w.guildGroups[gid]; ok {
			guildName := EscapeDiscordMarkdown(gg.Name())
			// Replace `'s with `\'s`
			dn = strings.ReplaceAll(dn, "`", "\\`")
			activeDisplayNames = append(activeDisplayNames, fmt.Sprintf("%s: `%s`", guildName, dn))

		}
	}
	slices.Sort(activeDisplayNames)

	if w.opts.IncludeLinkedDevices {
		// Build a list of the players linked devices/XPIDs
		xpidStrs := make([]string, 0, len(w.profile.LinkedXPIDs()))
		for _, xpid := range w.profile.LinkedXPIDs() {
			if w.opts.StripIPAddresses {
				xpidStrs = append(xpidStrs, xpid.String())
			}
		}
		linkedDevices = strings.Join(xpidStrs, "\n")
	}

	if w.opts.IncludeDefaultMatchmakingGuild {
		if gg, ok := w.guildGroups[w.profile.GetActiveGroupID().String()]; ok {
			defaultMatchmakingGuildName = EscapeDiscordMarkdown(gg.Name())
		}
	}
	if w.opts.IncludePartyGroupName {
		partyGroupName = "not set"
		if w.matchmakingSettings != nil && w.matchmakingSettings.LobbyGroupName != "" {
			partyGroupName = "`" + w.matchmakingSettings.LobbyGroupName + "`"
		}
	}

	if w.opts.IncludeRecentLogins {
		recentLogins = w.createRecentLoginsFieldValue()
	}

	passwordState := ""
	if w.opts.IncludePasswordSetState {
		if w.profile.HasPasswordSet() {
			passwordState = "Yes"
		} else {
			passwordState = "No"
		}
	}

	embed := &discordgo.MessageEmbed{
		Description: fmt.Sprintf("<@%s>", w.profile.DiscordID()),
		Color:       WhoAmIBaseColor,
		Author: &discordgo.MessageEmbedAuthor{
			Name: "EchoVRCE Account",
		},
		Fields: []*discordgo.MessageEmbedField{
			{
				Name:   "Username",
				Value:  w.profile.Username(),
				Inline: true,
			},
			{
				Name:   "Discord ID",
				Value:  w.profile.DiscordID(),
				Inline: true,
			},
			{
				Name:   "Password Set",
				Value:  passwordState,
				Inline: true,
			},
			{
				Name:   "Created",
				Value:  fmt.Sprintf("<t:%d:R>", w.profile.CreatedAt().UTC().Unix()),
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
				Value:  w.createGuildMembershipsEmbedFieldValue(),
				Inline: false,
			},
		},
		Footer: &discordgo.MessageEmbedFooter{
			Text: fmt.Sprintf("Nakama ID: %s", w.profile.UserID()),
		},
	}

	if w.profile.IsDisabled() {
		embed.Color = 0xCC0000
		embed.Fields = append(embed.Fields, &discordgo.MessageEmbedField{
			Name:   "Account Status",
			Value:  "Disabled",
			Inline: true,
		})
	}

	return embed
}

func (w *WhoAmI) createRecentLoginsFieldValue() string {

	loginsByXPID := make(map[evr.EvrId]time.Time, 0)

	// Only include the latest login for each XPID
	for _, e := range w.loginHistory.History {
		if e.UpdatedAt.Before(w.opts.ShowLoginsSince) {
			continue
		}
		if ts, ok := loginsByXPID[e.XPID]; !ok || ts.Before(e.UpdatedAt) {
			loginsByXPID[e.XPID] = e.UpdatedAt
		}
	}

	lines := make([]string, 0, len(w.loginHistory.History))
	for xpid, ts := range loginsByXPID {
		lines = append(lines, fmt.Sprintf("<t:%d:R> - `%s`", ts.UTC().Unix(), xpid.String()))
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
			players = append(players, fmt.Sprintf("<@!%s>", p.DiscordID))
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

func (WhoAmI) createPastDisplayNameEmbed(history *DisplayNameHistory, groupID string) *discordgo.MessageEmbed {
	if history == nil || len(history.Histories) == 0 {
		return nil
	}

	displayNameMap := make(map[string]time.Time)

	for gid, items := range history.Histories {
		if groupID != "" && gid != groupID {
			continue
		}
		for dn, ts := range items {
			dn = fmt.Sprintf("`%s`", dn)
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
		Color:  WhoAmIBaseColor,
		Fields: []*discordgo.MessageEmbedField{{Name: "Display Names", Value: strings.Join(displayNames, "\n"), Inline: false}},
	}
}

func (w *WhoAmI) createGuildMembershipsEmbedFieldValue() string {
	if len(w.guildGroups) == 0 {
		return ""
	}

	guildGroups := make([]*GuildGroup, 0, len(w.guildGroups))
	for _, group := range w.guildGroups {
		guildGroups = append(guildGroups, group)
	}

	sort.SliceStable(guildGroups, func(i, j int) bool {
		return guildGroups[i].Name() < guildGroups[j].Name()
	})

	lines := make([]string, 0, len(guildGroups))

	for _, group := range guildGroups {

		groupStr := group.Name()
		userIDStr := w.profile.UserID()
		if w.opts.IncludeGuildRoles {
			// Add the roles
			activeRoleMap := map[string]bool{
				"owner":       group.IsOwner(userIDStr),
				"matchmaking": group.IsAllowedMatchmaking(userIDStr),
				"auditor":     group.IsAuditor(userIDStr),
				"enforcer":    group.IsEnforcer(userIDStr),
				"server-host": group.IsServerHost(userIDStr),
				"allocator":   group.IsAllocator(userIDStr),
				"api-access":  group.IsAPIAccess(userIDStr),
				"suspended":   group.IsSuspended(userIDStr, nil),
				"vpn-bypass":  group.IsVPNBypass(userIDStr),
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

func (w *WhoAmI) createSuspensionsEmbed() *discordgo.MessageEmbed {
	groupIDs := make([]string, 0, len(w.guildGroups))
	for _, g := range w.guildGroups {
		groupIDs = append(groupIDs, g.Group.Id)
	}

	voids := w.journal.GroupVoids(groupIDs...)
	for {
		// Loop until the embed size is less than 1024 bytes
		fields := make([]*discordgo.MessageEmbedField, 0, len(w.journal.RecordsByGroupID))

		for groupID, records := range w.journal.RecordsByGroupID {
			if !w.opts.IncludeAllGuilds && !slices.Contains(groupIDs, groupID) {
				continue
			}

			gName := groupID
			if gg, ok := w.guildGroups[groupID]; ok {
				gName = EscapeDiscordMarkdown(gg.Name())
			}
			if field := createSuspensionDetailsEmbedField(gName, records, voids, w.opts.IncludeInactiveSuspensions, w.opts.IncludeSuspensionAuditorNotes); field != nil {
				if field.Value != "" {
					fields = append(fields, field)
				}
			}
		}

		if len(fields) == 0 {
			return nil
		}

		// Sort the fields by group name
		sort.SliceStable(fields, func(i, j int) bool {
			return fields[i].Name < fields[j].Name
		})

		embed := &discordgo.MessageEmbed{
			Title:  "Suspensions",
			Color:  WhoAmISecondaryColor,
			Fields: fields,
		}
		// If any of the suspensions are active, set the color to red

		if len(w.journal.ActiveSuspensions()) > 0 {
			embed.Color = 0xCC0000 // Red if there are active suspensions
		}

		return embed
	}
}

func (*WhoAmI) createVRMLHistoryEmbed(s *VRMLPlayerSummary) *discordgo.MessageEmbed {
	if s == nil || s.User == nil {
		return nil
	}

	embed := &discordgo.MessageEmbed{
		Title:  "VRML History",
		Color:  WhoAmISecondaryColor,
		Fields: make([]*discordgo.MessageEmbedField, 0, 4),
	}

	if s.Player == nil {
		// If the player page isn't found, display the user page
		embed.Fields = append(embed.Fields, &discordgo.MessageEmbedField{
			Name:   "User Page",
			Value:  fmt.Sprintf("[%s](https://vrmasterleague.com/Users/%s)", s.User.UserName, s.User.ID),
			Inline: false,
		}, &discordgo.MessageEmbedField{
			Name:   "Match Counts",
			Value:  "N/A - Player Page Not Found",
			Inline: false,
		})
	} else {
		embed.Fields = append(embed.Fields, &discordgo.MessageEmbedField{
			Name:   "Player Page",
			Value:  fmt.Sprintf("[%s](https://vrmasterleague.com/EchoArena/Players/%s)", s.Player.User.UserName, s.Player.ThisGame.PlayerID),
			Inline: false,
		})

		matchCountsBySeason := make(map[string]int, 0)

		for sID, teams := range s.MatchCountsBySeasonByTeam {
			for _, n := range teams {
				seasonName, ok := vrmlSeasonDescriptionMap[sID]
				if !ok {
					seasonName = string(sID)
				} else {
					sNumber, _ := strings.CutPrefix(seasonName, "Season ")
					seasonName = "S" + sNumber
				}

				matchCountsBySeason[seasonName] += n
			}
		}
		if len(matchCountsBySeason) == 0 {
			embed.Fields = append(embed.Fields, &discordgo.MessageEmbedField{
				Name:   "Match Counts",
				Value:  "No matches found",
				Inline: false,
			})
		} else {

			// Create a list of the match counts (i.e. season: count)
			lines := make([]string, 0, len(matchCountsBySeason))
			for season, count := range matchCountsBySeason {
				lines = append(lines, fmt.Sprintf("%s: %d", season, count))
			}
			slices.Sort(lines)

			embed.Fields = append(embed.Fields, &discordgo.MessageEmbedField{
				Name:   "Match Counts",
				Value:  strings.Join(lines, "\n"),
				Inline: true,
			})
		}
	}

	return embed
}

func (w *WhoAmI) createAlternatesEmbed() *discordgo.MessageEmbed {
	var (
		alternatesEmbed *discordgo.MessageEmbed
		hasSuspendedAlt bool
	)

	// Check for any suspensions on alternate accounts

	thisGroupSuspensions := w.activeSuspensions[w.GroupID]
	potentialAlternates := make([]string, 0, len(w.loginHistory.AlternateMatches))

	for altUserID, matches := range w.loginHistory.AlternateMatches {
		altAccount, ok := w.potentialAlternates[altUserID]
		if !ok {
			continue // Skip if the alternate account is not found
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
		suspendedText := ""
		var altsRecord GuildEnforcementRecord
		for _, r := range thisGroupSuspensions {
			if r.UserID == altUserID {
				// Collect the worst, or most widely effective suspension.
				if altsRecord.Expiry.Before(r.Expiry) || (!altsRecord.SuspensionExcludesPrivateLobbies() && r.SuspensionExcludesPrivateLobbies()) {
					altsRecord = r
				}
			}
		}

		if !altsRecord.IsExpired() {
			if altsRecord.SuspensionExcludesPrivateLobbies() {
				suspendedText = fmt.Sprintf(" (suspended from private lobbies until <t:%d:R>)", altsRecord.Expiry.UTC().Unix())
			} else {
				suspendedText = fmt.Sprintf(" (suspended until <t:%d:R>)", altsRecord.Expiry.UTC().Unix())
			}
			hasSuspendedAlt = true
		}
		s := fmt.Sprintf("<@%s> [%s] %s <t:%d:R>%s\n", altAccount.CustomId, altAccount.User.Username, state, altAccount.User.UpdateTime.AsTime().UTC().Unix(), suspendedText)

		for _, item := range items {
			s += fmt.Sprintf("-  `%s`\n", item)
		}

		potentialAlternates = append(potentialAlternates, s)
	}

	if len(potentialAlternates) > 0 {

		alternatesEmbed = &discordgo.MessageEmbed{
			Title:  "Suspected Alternate Accounts",
			Color:  WhoAmISystemColor,
			Fields: []*discordgo.MessageEmbedField{{Name: "Account / Match Items", Value: strings.Join(potentialAlternates, "\n"), Inline: false}},
		}

		if w.loginHistory.IgnoreDisabledAlternates {
			alternatesEmbed.Footer = &discordgo.MessageEmbedFooter{
				Text: "Note: Suspended alternates do not carry-over for this player.",
			}
		}
		if hasSuspendedAlt {
			alternatesEmbed.Color = 0xCC0000 // Red if there are suspended alternates
		}
	}

	return alternatesEmbed
}

func (w *WhoAmI) collectedBlockedFriends(ctx context.Context, nk runtime.NakamaModule) ([]*api.User, error) {

	var (
		err             error
		blockedUsers    = make([]*api.User, 0)
		blockedState    = int(api.Friend_BLOCKED)
		blockedStatePtr = &blockedState
		cursor          = ""
		friends         []*api.Friend
	)
	// Get the friends list
	for {
		friends, cursor, err = nk.FriendsList(ctx, w.profile.ID(), 100, blockedStatePtr, cursor)
		if err != nil {
			return nil, err
		}
		users := make([]*api.User, 0, len(friends))
		for _, f := range friends {
			if f.GetState().Value == int32(blockedState) {
				blockedUsers = append(users, f.GetUser())
			}
		}

		if len(friends) == 0 || cursor == "" {
			break
		}
	}

	return blockedUsers, nil
}

func (d *DiscordAppBot) handleProfileRequest(ctx context.Context, logger runtime.Logger, nk runtime.NakamaModule, s *discordgo.Session, i *discordgo.InteractionCreate, target *discordgo.User, opts UserProfileRequestOptions) error {

	var (
		err error

		callerID             = d.cache.DiscordIDToUserID(i.Member.User.ID)
		targetID             = d.cache.DiscordIDToUserID(target.ID)
		profile              *EVRProfile
		matchmakingSettings  *MatchmakingSettings
		loginHistory         *LoginHistory
		displayNameHistory   *DisplayNameHistory
		embeds               = make([]*discordgo.MessageEmbed, 0, 4)
		groupID              = d.cache.GuildIDToGroupID(i.GuildID)
		matchmakingEmbed     *discordgo.MessageEmbed
		vrmlEmbed            *discordgo.MessageEmbed
		suspensionsEmbed     *discordgo.MessageEmbed
		pastDisplayNameEmbed *discordgo.MessageEmbed
		alternatesEmbed      *discordgo.MessageEmbed
	)

	if callerID == "" || targetID == "" {
		return fmt.Errorf("invalid user ID")
	}

	if a, err := nk.AccountGetId(ctx, targetID); err != nil {
		return fmt.Errorf("failed to get account by ID: %w", err)
	} else if a.GetDisableTime() != nil && opts.ErrorIfAccountDisabled {
		return fmt.Errorf("account is disabled")
	} else if profile, err = BuildEVRProfileFromAccount(a); err != nil {
		return fmt.Errorf("failed to get account by ID: %w", err)
	}

	loginHistory = NewLoginHistory(targetID)
	if err := StorableRead(ctx, nk, targetID, loginHistory, true); err != nil {
		return fmt.Errorf("error getting device history: %w", err)
	}

	if displayNameHistory, err = DisplayNameHistoryLoad(ctx, nk, targetID); err != nil {
		return fmt.Errorf("failed to load display name history: %w", err)
	}
	potentialAlternates := make(map[string]*api.Account, len(loginHistory.AlternateMatches))
	for altUserID := range loginHistory.AlternateMatches {
		altAccount, err := nk.AccountGetId(ctx, altUserID)
		if err != nil {
			logger.WithFields(map[string]any{
				"error":       err,
				"user_id":     altUserID,
				"alt_user_id": altUserID,
			}).Warn("failed to get alternate account by ID")
			continue // Skip if the alternate account cannot be found
		}
		potentialAlternates[altUserID] = altAccount
	}

	firstIDs, _ := loginHistory.AlternateIDs()
	activeSuspensions, err := CheckEnforcementSuspensions(ctx, nk, d.guildGroupRegistry, profile.ID(), firstIDs)
	if err != nil {
		return fmt.Errorf("failed to check enforcement suspensions: %w", err)
	}

	guildGroups, err := GuildUserGroupsList(ctx, nk, d.guildGroupRegistry, targetID)
	if err != nil {
		return fmt.Errorf("error getting guild groups: %w", err)
	}

	journals, err := EnforcementJournalsLoad(ctx, nk, []string{targetID})
	if err != nil {
		return fmt.Errorf("failed to load enforcement journals: %w", err)
	}

	journal := NewGuildEnforcementJournal(profile.ID())
	if len(journals) > 0 && journals[targetID] != nil {
		journal = journals[targetID]
	}

	settings, err := LoadMatchmakingSettings(ctx, nk, targetID)
	if err != nil {
		return fmt.Errorf("failed to load matchmaking settings: %w", err)
	}
	matchmakingSettings = &settings

	// Make sure that all guilds with enforcement records are included in the guildGroups
	for groupID := range journal.RecordsByGroupID {
		if _, ok := guildGroups[groupID]; !ok {
			// If the guild group is not in the guildGroups, add it
			if gg := d.guildGroupRegistry.Get(groupID); gg != nil {
				guildGroups[groupID] = gg
			} else {
				logger.WithFields(map[string]any{
					"error":    err,
					"group_id": groupID,
				}).Warn("failed to get guild group for enforcement record")
			}
		}
	}

	if !opts.IncludeAllGuilds {
		for gid, g := range guildGroups {
			if g.GuildID != i.GuildID {
				delete(guildGroups, gid)
			}
		}
	}

	w := NewWhoAmI(ctx, logger, nk, d.guildGroupRegistry, d.cache, profile, loginHistory, matchmakingSettings, guildGroups, displayNameHistory, journal, potentialAlternates, activeSuspensions, opts, groupID)

	if w.opts.IncludePastDisplayNamesEmbed {
		pastDisplayNameEmbed = w.createPastDisplayNameEmbed(displayNameHistory, groupID)
	}

	if w.opts.IncludeVRMLHistoryEmbed {
		vrmlSummary := &VRMLPlayerSummary{}
		// Get VRML Summary
		if err := StorableRead(ctx, nk, targetID, vrmlSummary, false); err != nil {
			if status.Code(err) != codes.NotFound {
				logger.WithField("error", err).Warn("failed to get VRML summary")
			}
		} else {
			vrmlEmbed = w.createVRMLHistoryEmbed(vrmlSummary)
		}
	}

	if w.opts.IncludeSuspensionsEmbed {
		suspensionsEmbed = w.createSuspensionsEmbed()
	}

	if w.opts.IncludeCurrentMatchesEmbed {
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

		matchmakingEmbed = w.createMatchmakingEmbed(profile, guildGroups, matchmakingSettings, matchLabels, lastMatchmakingError)
	}

	if w.opts.IncludeAlternatesEmbed {
		// Create the alternates embed
		alternatesEmbed = w.createAlternatesEmbed()
	}

	accountDetailsEmbed := w.createUserAccountDetailsEmbed()

	// Combine the embeds into a message
	embeds = append(embeds,
		accountDetailsEmbed,
		alternatesEmbed,
		suspensionsEmbed,
		pastDisplayNameEmbed,
		vrmlEmbed,
		matchmakingEmbed,
	)

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
	if err := s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseChannelMessageWithSource,
		Data: &discordgo.InteractionResponseData{
			Flags:  discordgo.MessageFlagsEphemeral,
			Embeds: embeds,
		},
	}); err != nil {

		if opts.SendFileOnError {
			// Try and send a message with a file attachment of the embeds if the interaction response fails
			logger.Error("failed to respond to interaction", "error", err)
			embedData, err := json.MarshalIndent(embeds, "", "  ")
			if err != nil {
				return fmt.Errorf("failed to marshal embeds: %w", err)
			}

			if err := s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
				Type: discordgo.InteractionResponseChannelMessageWithSource,
				Data: &discordgo.InteractionResponseData{
					Content: "The profile is too large to display in an embed. Here is the raw data.",
					Flags:   discordgo.MessageFlagsEphemeral,
					Files: []*discordgo.File{
						{
							Name:        targetID + "_lookup.json",
							ContentType: "application/json",
							Reader:      strings.NewReader(string(embedData)),
						},
					},
				},
			}); err != nil {
				return fmt.Errorf("failed to send message with embeds: %w", err)
			}

		} else {
			return fmt.Errorf("failed to respond to interaction: %w", err)
		}
	}

	return nil
}
