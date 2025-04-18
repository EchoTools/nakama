package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/bwmarrin/discordgo"
	"github.com/heroiclabs/nakama-common/api"
	"github.com/heroiclabs/nakama/v3/server/evr"
	"go.uber.org/zap"
)

func (p *EvrPipeline) LobbyJoinEntrants(logger *zap.Logger, label *MatchLabel, presences ...*EvrMatchPresence) error {
	if len(presences) == 0 {
		return errors.New("no presences")
	}

	session := p.nk.sessionRegistry.Get(presences[0].SessionID)
	if session == nil {
		return errors.New("session not found")
	}

	serverSession := p.nk.sessionRegistry.Get(label.GameServer.SessionID)
	if serverSession == nil {
		return errors.New("server session not found")
	}

	return LobbyJoinEntrants(logger, p.nk.matchRegistry, p.nk.tracker, session, serverSession, label, presences...)
}
func LobbyJoinEntrants(logger *zap.Logger, matchRegistry MatchRegistry, tracker Tracker, session Session, serverSession Session, label *MatchLabel, entrants ...*EvrMatchPresence) error {
	if session == nil || serverSession == nil {
		return errors.New("session is nil")
	}

	for _, e := range entrants {
		for _, feature := range label.RequiredFeatures {
			if !slices.Contains(e.SupportedFeatures, feature) {
				logger.With(zap.String("uid", e.UserID.String()), zap.String("sid", e.SessionID.String())).Warn("Player does not support required feature", zap.String("feature", feature), zap.String("mid", label.ID.UUID.String()), zap.String("uid", e.UserID.String()))
				return NewLobbyErrorf(MissingEntitlement, "player does not support required feature: %s", feature)
			}
		}
	}

	// Additional entrants are considered reservations
	metadata := EntrantMetadata{
		Presence:     entrants[0],
		Reservations: entrants[1:],
	}.ToMatchMetadata()

	e := entrants[0]

	sessionCtx := session.Context()

	var err error
	var found, allowed, isNew bool
	var reason string
	var labelStr string

	// Trigger MatchJoinAttempt
	found, allowed, isNew, reason, labelStr, _ = matchRegistry.JoinAttempt(sessionCtx, label.ID.UUID, label.ID.Node, e.UserID, e.SessionID, e.Username, e.SessionExpiry, nil, e.ClientIP, e.ClientPort, label.ID.Node, metadata)
	switch {
	case !found:
		err = NewLobbyErrorf(ServerDoesNotExist, "join attempt failed: match not found")

	case labelStr == "":
		err = NewLobbyErrorf(ServerDoesNotExist, "join attempt failed: match label empty")

	case reason == ErrJoinRejectDuplicateEvrID.Error():
		err = NewLobbyErrorf(BadRequest, "join attempt failed: duplicate evr ID")

	case reason == ErrJoinRejectReasonMatchClosed.Error():
		err = NewLobbyErrorf(ServerIsLocked, "join attempt failed: match closed")

	case !allowed:
		err = NewLobbyErrorf(ServerIsFull, "join attempt failed: not allowed: %s", reason)
	}

	if err != nil {
		logger.Warn("failed to join match", zap.Error(err))
		return fmt.Errorf("failed to join match: %w", err)
	}

	entrantStream := PresenceStream{Mode: StreamModeEntrant, Subject: e.EntrantID(label.ID), Label: e.Node}

	if isNew {

		e = &EvrMatchPresence{}
		if err := json.Unmarshal([]byte(reason), e); err != nil {
			err = fmt.Errorf("failed to unmarshal match presence: %w", err)
			return err
		}

		// Update the presence stream for the entrant.
		entrantMeta := PresenceMeta{Format: SessionFormatEVR, Username: e.Username, Status: e.String(), Hidden: false}

		success := tracker.Update(sessionCtx, e.SessionID, entrantStream, e.UserID, entrantMeta)
		if !success {
			return errors.New("failed to track session ID")
		}

	} else {

		// Use the existing entrant metadata.
		entrantMeta := tracker.GetLocalBySessionIDStreamUserID(e.SessionID, entrantStream, e.UserID)
		if entrantMeta == nil {
			return errors.New("failed to get entrant metadata")
		}
		if err := json.Unmarshal([]byte(entrantMeta.Status), e); err != nil {
			return fmt.Errorf("failed to unmarshal entrant metadata: %w", err)
		}
	}

	<-time.After(1 * time.Second)

	matchIDStr := label.ID.String()

	guildGroupStream := PresenceStream{Mode: StreamModeGuildGroup, Subject: label.GetGroupID(), Label: label.Mode.String()}

	ops := []*TrackerOp{
		{
			guildGroupStream,
			PresenceMeta{Format: SessionFormatEVR, Username: e.Username, Status: matchIDStr, Hidden: false},
		},
		{
			PresenceStream{Mode: StreamModeService, Subject: e.SessionID, Label: StreamLabelMatchService},
			PresenceMeta{Format: SessionFormatEVR, Username: e.Username, Status: matchIDStr, Hidden: false},
		},
		{
			PresenceStream{Mode: StreamModeService, Subject: e.LoginSessionID, Label: StreamLabelMatchService},
			PresenceMeta{Format: SessionFormatEVR, Username: e.Username, Status: matchIDStr, Hidden: false},
		},
		{
			PresenceStream{Mode: StreamModeService, Subject: e.UserID, Label: StreamLabelMatchService},
			PresenceMeta{Format: SessionFormatEVR, Username: e.Username, Status: matchIDStr, Hidden: false},
		},
		{
			PresenceStream{Mode: StreamModeService, Subject: e.EvrID.UUID(), Label: StreamLabelMatchService},
			PresenceMeta{Format: SessionFormatEVR, Username: e.Username, Status: matchIDStr, Hidden: false},
		},
	}

	// Update the statuses. This is looked up by the pipeline when the game server sends the new entrant message.
	for _, op := range ops {
		if ok := tracker.Update(sessionCtx, e.SessionID, op.Stream, e.UserID, op.Meta); !ok {
			return errors.New("failed to track session ID")
		}
	}

	// Leave any other lobby group stream.
	tracker.UntrackLocalByModes(session.ID(), map[uint8]struct{}{StreamModeMatchmaking: {}, StreamModeGuildGroup: {}}, guildGroupStream)

	connectionSettings := label.GetEntrantConnectMessage(e.RoleAlignment, e.IsPCVR, e.DisableEncryption, e.DisableMAC)

	// Send the lobby session success message to the game server.
	if err := SendEVRMessages(serverSession, false, connectionSettings); err != nil {
		logger.Error("failed to send lobby session success to game server", zap.Error(err))

		return errors.New("failed to send lobby session success to game server")
	}

	// Send the lobby session success message to the game client.
	<-time.After(250 * time.Millisecond)

	err = SendEVRMessages(session, false, connectionSettings)
	if err != nil {
		logger.Error("failed to send lobby session success to game client", zap.Error(err))
		return errors.New("failed to send lobby session success to game client")
	}

	logger.Info("Joined entrant.", zap.String("mid", label.ID.UUID.String()), zap.String("uid", e.UserID.String()), zap.String("sid", e.SessionID.String()), zap.Int("role", e.RoleAlignment))
	return nil
}

func (p *EvrPipeline) lobbyAuthorize(ctx context.Context, logger *zap.Logger, session Session, lobbyParams *LobbySessionParameters) error {
	groupID := lobbyParams.GroupID.String()
	metricsTags := map[string]string{
		"group_id": groupID,
	}

	defer func() {
		p.nk.MetricsCounterAdd("lobby_authorization", metricsTags, 1)
	}()

	userID := session.UserID().String()

	params, ok := LoadParams(ctx)
	if !ok {
		return errors.New("failed to get session parameters")
	}
	var err error

	isMember := false

	gg, ok := params.guildGroups[groupID]
	if ok {
		// If there is no member role, the user is considered a member if they are in the discord guild.
		isMember = gg.RoleMap.Member == "" || gg.IsMember(userID)
	} else {
		gg = p.guildGroupRegistry.Get(groupID)
		if gg == nil {
			return fmt.Errorf("failed to get guild group: %s", groupID)
		}
	}

	// User is not a member of the group.
	if gg.MembersOnlyMatchmaking && !isMember {

		metricsTags["error"] = "not_member"

		if _, err := p.appBot.LogAuditMessage(ctx, groupID, fmt.Sprintf("Rejected non-member <@%s>", userID), true); err != nil {
			p.logger.Warn("Failed to send audit message", zap.String("channel_id", gg.AuditChannelID), zap.Error(err))
		}

		return NewLobbyError(KickedFromLobbyGroup, "user does not have member role")
	}

	// User is suspended from the group.
	if gg.IsSuspended(userID, &params.xpID) {

		metricsTags["error"] = "suspended_user"

		if _, err := p.appBot.LogAuditMessage(ctx, groupID, fmt.Sprintf("Rejected suspended user <@%s>", userID), true); err != nil {
			p.logger.Warn("Failed to send audit message", zap.String("channel_id", gg.AuditChannelID), zap.Error(err))
		}

		return ErrSuspended
	}
	if recordsByGuild, err := EnforcementSuspensionSearch(ctx, p.nk, groupID, []string{userID}, false, false); err != nil {
		logger.Warn("Unable to read enforcement records", zap.Error(err))
	} else if len(recordsByGuild) > 0 {
		var latestRecord *GuildEnforcementRecord

		for _, byUserID := range recordsByGuild {
			for _, records := range byUserID {
				for _, r := range records.Records {
					if latestRecord == nil || r.CreatedAt.After(latestRecord.CreatedAt) && time.Now().Before(r.SuspensionExpiry) {
						latestRecord = r
					}
				}
			}
		}

		if latestRecord != nil {
			// User has an active suspension
			metricsTags["error"] = "suspended_user"
			if _, err := p.appBot.LogAuditMessage(ctx, groupID, fmt.Sprintf("Rejected suspended user <@%s> (%s) (expires <t:%d:R>)", userID, latestRecord.SuspensionNotice, latestRecord.SuspensionExpiry.Unix()), true); err != nil {
				p.logger.Warn("Failed to send audit message", zap.String("channel_id", gg.AuditChannelID), zap.Error(err))
			}
			const maxMessageLength = 60
			message := latestRecord.SuspensionNotice
			expires := fmt.Sprintf(" [exp: %s]", formatDuration(time.Until(latestRecord.SuspensionExpiry), false))

			if len(message)+len(expires) > maxMessageLength {
				message = message[:maxMessageLength-len(expires)-3] + "..."
			}
			message = message + expires
			return NewLobbyError(KickedFromLobbyGroup, message)
		}
	}

	if gg.IsLimitedAccess(userID) {

		switch lobbyParams.Mode {
		case evr.ModeArenaPublic, evr.ModeCombatPublic, evr.ModeSocialPublic:
			metricsTags["error"] = "limited_access_user"

			if _, err := p.appBot.LogAuditMessage(ctx, groupID, fmt.Sprintf("Rejected limited access user <@%s>", userID), true); err != nil {
				p.logger.Warn("Failed to send audit message", zap.String("channel_id", gg.AuditChannelID), zap.Error(err))
			}

			return NewLobbyError(KickedFromLobbyGroup, "user does not have access social lobbies or matchmaking.")
		}
	}

	if gg.MinimumAccountAgeDays > 0 && !gg.IsAccountAgeBypass(userID) {
		// Check the account creation date.
		discordID, err := GetDiscordIDByUserID(ctx, p.db, userID)
		if err != nil {
			return fmt.Errorf("failed to get discord ID by user ID: %w", err)
		}

		t, err := discordgo.SnowflakeTimestamp(discordID)
		if err != nil {
			return fmt.Errorf("failed to get discord snowflake timestamp: %w", err)
		}

		if t.After(time.Now().AddDate(0, 0, -gg.MinimumAccountAgeDays)) {

			accountAge := time.Since(t).Hours() / 24

			metricsTags["error"] = "account_age"

			if _, err := p.appBot.dg.ChannelMessageSend(gg.AuditChannelID, fmt.Sprintf("Rejected user <@%s> because of account age (%d days).", discordID, int(accountAge))); err != nil {
				p.logger.Warn("Failed to send audit message", zap.String("channel_id", gg.AuditChannelID), zap.Error(err))
			}

			return NewLobbyErrorf(KickedFromLobbyGroup, "account is too new to join this guild")
		}
	}

	if gg.BlockVPNUsers && params.isVPN && !gg.IsVPNBypass(userID) && params.ipInfo != nil {
		metricsTags["error"] = "vpn_user"

		if params.ipInfo.FraudScore() >= gg.FraudScoreThreshold {

			var fields []*discordgo.MessageEmbedField

			fields = []*discordgo.MessageEmbedField{
				{
					Name:   "Player",
					Value:  fmt.Sprintf("<@%s>", lobbyParams.DiscordID),
					Inline: false,
				},
				{
					Name:   "IP Address",
					Value:  session.ClientIP(),
					Inline: true,
				},
				{
					Name:   "Data Provider",
					Value:  params.ipInfo.DataProvider(),
					Inline: true,
				},
				{
					Name:   "Score",
					Value:  fmt.Sprintf("%d", params.ipInfo.FraudScore()),
					Inline: true,
				},
				{
					Name:   "ISP",
					Value:  params.ipInfo.ISP(),
					Inline: true,
				},
				{
					Name:   "Organization",
					Value:  params.ipInfo.Organization(),
					Inline: true,
				},
				{
					Name:   "ASN",
					Value:  fmt.Sprintf("%d", params.ipInfo.ASN()),
					Inline: true,
				},
				{
					Name:   "City",
					Value:  params.ipInfo.City(),
					Inline: true,
				},
				{
					Name:   "Region",
					Value:  params.ipInfo.Region(),
					Inline: true,
				},
				{
					Name:   "Country",
					Value:  params.ipInfo.CountryCode(),
					Inline: true,
				},
			}

			embed := &discordgo.MessageEmbed{
				Title:  "VPN User Rejected",
				Fields: fields,
				Color:  0xff0000, // Red color
			}
			message := &discordgo.MessageSend{
				Embeds: []*discordgo.MessageEmbed{embed},
			}
			if _, err := p.appBot.dg.ChannelMessageSendComplex(gg.AuditChannelID, message); err != nil {
				p.logger.Warn("Failed to send audit message", zap.String("channel_id", gg.AuditChannelID), zap.Error(err))
			}

			guildName := "This guild"
			if gg := params.guildGroups[groupID]; gg != nil {
				guildName = gg.Group.Name
			}

			return NewLobbyError(KickedFromLobbyGroup, fmt.Sprintf("%s does not allow VPN users.", guildName))
		}
	}

	if len(gg.AllowedFeatures) > 0 {
		allowedFeatures := gg.AllowedFeatures
		for _, feature := range params.supportedFeatures {
			if !slices.Contains(allowedFeatures, feature) {
				return NewLobbyError(KickedFromLobbyGroup, "This guild does not allow clients with `feature DLLs`.")
			}
		}
	}

	displayName := params.accountMetadata.GetGroupDisplayNameOrDefault(groupID)
	p.nk.Event(ctx, &api.Event{
		Name: EventLobbySessionAuthorized,
		Properties: map[string]string{
			"session_id":   session.ID().String(),
			"group_id":     groupID,
			"user_id":      userID,
			"discord_id":   params.DiscordID(),
			"display_name": displayName,
		},
		External: true, // used to denote if the event was generated from the client
	})

	// Generate a profile for this group
	profile, err := UserServerProfileFromParameters(ctx, logger, p.db, p.nk, params, groupID, []evr.Symbol{lobbyParams.Mode}, lobbyParams.Mode)
	if err != nil {
		return fmt.Errorf("failed to create user server profile: %w", err)
	}

	if gg.IsEnforcer(userID) && params.isGoldNameTag.Load() && profile.DeveloperFeatures == nil {
		// Give the user a gold name if they are enabled as a moderator in the guild, and want it.
		profile.DeveloperFeatures = &evr.DeveloperFeatures{}
	}

	if _, err := p.profileCache.Store(session.ID(), *profile); err != nil {
		return fmt.Errorf("failed to cache profile: %w", err)
	}

	session.Logger().Info("Authorized access to lobby session", zap.String("gid", groupID), zap.String("display_name", displayName))

	return nil
}

func formatDuration(d time.Duration, withSeconds bool) string {
	// Format the duration as a string in the format "1d1h2m3s"
	// if it's more than 1 day, it's "1d1h" (the hour is rounded up)
	// if it's more than 1 minute, it's "1h15m" (the minute is rounded up)
	// if it's more than 1 seconds, it's "30s" (the seconds are rounded up)

	isNegative := d < 0
	if isNegative {
		d = -d
	}
	// Calculate the number of days, hours, minutes, and seconds
	var (
		days    = int(d.Hours() / 24)
		hours   = int(d.Hours()) % 24
		minutes = int(d.Minutes()) % 60
		seconds = int(d.Seconds()) % 60
	)

	if !withSeconds && seconds > 0 {
		minutes++
	}

	var result string
	if days > 0 {
		result += fmt.Sprintf("%dd", days)
	}
	if days > 0 || hours > 0 {
		result += fmt.Sprintf("%dh", hours)
	}

	if days > 0 || hours > 0 || minutes > 0 {
		result += fmt.Sprintf("%dm", minutes)
	}
	if seconds > 0 && withSeconds {
		result += fmt.Sprintf("%ds", seconds)
	}

	if result == "" {
		result = "0s"
	}
	if isNegative {
		result = "-" + result
	}
	return result
}
