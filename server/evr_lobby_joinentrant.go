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

var (
	ErrFailedToTrackSessionID     = errors.New("failed to track session ID")
	ErrFailedToGetEntrantMetadata = errors.New("failed to get entrant metadata")
	ErrNoPresences                = errors.New("no presences")
	ErrServerSessionNotFound      = errors.New("server session not found")
	LobbyErrMatchNotFound         = NewLobbyError(ServerDoesNotExist, "match not found")
	LobbyErrMatchLabelEmpty       = NewLobbyError(ServerDoesNotExist, "match label empty")
	LobbyErrDuplicateEvrID        = NewLobbyError(BadRequest, "duplicate evr ID")
	LobbyErrMatchClosed           = NewLobbyError(ServerIsLocked, "match closed")
	LobbyErrJoinNotAllowed        = NewLobbyError(ServerIsFull, "join not allowed")
	ErrFailedToTrackEntrantStream = errors.New("failed to track entrant stream")
)

func (p *EvrPipeline) LobbyJoinEntrants(logger *zap.Logger, label *MatchLabel, presences ...*EvrMatchPresence) error {
	if len(presences) == 0 {
		return ErrNoPresences
	}

	session := p.nk.sessionRegistry.Get(presences[0].SessionID)
	if session == nil {
		return ErrSessionNotFound
	}

	serverSession := p.nk.sessionRegistry.Get(label.GameServer.SessionID)
	if serverSession == nil {
		return ErrServerSessionNotFound
	}

	return LobbyJoinEntrants(logger, p.nk.matchRegistry, p.nk.tracker, session, serverSession, label, presences...)
}
func LobbyJoinEntrants(logger *zap.Logger, matchRegistry MatchRegistry, tracker Tracker, session Session, serverSession Session, label *MatchLabel, entrants ...*EvrMatchPresence) error {
	if session == nil || serverSession == nil {
		return ErrSessionNotFound
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
	// Define these errors at the package level or where appropriate

	switch {
	case !found:
		err = LobbyErrMatchNotFound
	case labelStr == "":
		err = LobbyErrMatchLabelEmpty
	case reason == ErrJoinRejectDuplicateEvrID.Error():
		// Assuming ErrJoinRejectDuplicateEvrID is defined elsewhere and its Error() method returns the specific string
		err = LobbyErrDuplicateEvrID
	case reason == ErrJoinRejectReasonMatchClosed.Error():
		// Assuming ErrJoinRejectReasonMatchClosed is defined elsewhere and its Error() method returns the specific string
		err = LobbyErrMatchClosed
	case !allowed:
		// Wrap the base error with the specific reason provided by JoinAttempt
		err = fmt.Errorf("%w: %s", LobbyErrJoinNotAllowed, reason)
	}

	if err != nil {
		logger.Warn("failed to join match", zap.Error(err))
		return fmt.Errorf("failed to join match: %w", err)
	}

	entrantStream := PresenceStream{Mode: StreamModeEntrant, Subject: e.EntrantID(label.ID), Label: e.Node}

	if isNew {

		// The match handler will return an updated entrant presence.
		e = &EvrMatchPresence{}
		if err := json.Unmarshal([]byte(reason), e); err != nil {
			return fmt.Errorf("failed to unmarshal match presence: %w", err)
		}

		// Update the presence stream for the entrant.
		entrantMeta := PresenceMeta{Format: SessionFormatEVR, Username: e.Username, Status: e.String(), Hidden: false}

		if success := tracker.Update(sessionCtx, e.SessionID, entrantStream, e.UserID, entrantMeta); !success {
			return ErrFailedToTrackEntrantStream
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

	// Check for an active suspension in the enforcement journal.
	if recordsByGuild, err := EnforcementSuspensionSearch(ctx, p.nk, groupID, []string{userID}, true); err != nil {
		logger.Warn("Unable to read enforcement records", zap.Error(err))
	} else if len(recordsByGuild) > 0 {
		var latestRecord *GuildEnforcementRecord

		for _, byUserID := range recordsByGuild {
			for _, records := range byUserID {
				for _, r := range records.Records {
					if latestRecord == nil || r.SuspensionExpiry.After(latestRecord.SuspensionExpiry) && time.Now().Before(r.SuspensionExpiry) {
						latestRecord = r
					}
				}
			}
		}

		if latestRecord != nil {
			// User has an active suspension
			metricsTags["error"] = "suspended_user"
			if _, err := p.appBot.LogAuditMessage(ctx, groupID, fmt.Sprintf("Rejected suspended user <@!%s> (%s) (%s) (expires <t:%d:R>)", lobbyParams.DiscordID, lobbyParams.DisplayName, latestRecord.SuspensionNotice, latestRecord.SuspensionExpiry.Unix()), false); err != nil {
				p.logger.Warn("Failed to send audit message", zap.String("channel_id", gg.AuditChannelID), zap.Error(err))
			}
			const maxMessageLength = 60
			message := latestRecord.SuspensionNotice
			expires := fmt.Sprintf(" [exp: %s]", FormatDuration(time.Until(latestRecord.SuspensionExpiry)))

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

			fields := []*discordgo.MessageEmbedField{
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

	displayName := params.profile.GetGroupDisplayNameOrDefault(groupID)
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
