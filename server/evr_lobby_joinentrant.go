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
			return ErrFailedToTrackSessionID
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
	<-time.After(150 * time.Millisecond)

	if err := SendEVRMessages(session, false, connectionSettings); err != nil {
		logger.Error("failed to send lobby session success to game client", zap.Error(err))
		return errors.New("failed to send lobby session success to game client")
	}

	logger.Info("Joined entrant.", zap.String("mid", label.ID.UUID.String()), zap.String("uid", e.UserID.String()), zap.String("sid", e.SessionID.String()), zap.Int("role", e.RoleAlignment))
	return nil
}

// lobbyAuthorize checks if the user is allowed to join the lobby based on various criteria such as guild membership, suspensions, and account age.
func (p *EvrPipeline) lobbyAuthorize(ctx context.Context, logger *zap.Logger, session Session, lobbyParams *LobbySessionParameters) error {
	groupID := lobbyParams.GroupID.String()
	metricsTags := map[string]string{
		"group_id": groupID,
	}

	defer func() {
		p.nk.MetricsCounterAdd("lobby_authorization", metricsTags, 1)
	}()

	logAuditMessage := func(message string) {
		if _, err := p.appBot.LogAuditMessage(ctx, groupID, message, true); err != nil {
			p.logger.Warn("Failed to send audit message", zap.String("channel_id", groupID), zap.Error(err))
		}
	}

	joinRejected := func(metricKey, reason, auditMessage string) error {
		metricsTags["error"] = metricKey
		auditMessage = fmt.Sprintf("Rejected lobby join by %s <@%s>[%s] to `%s`: %s", EscapeDiscordMarkdown(lobbyParams.DisplayName), lobbyParams.DiscordID, session.Username(), lobbyParams.Mode, auditMessage)
		if _, err := p.appBot.LogAuditMessage(ctx, groupID, auditMessage, true); err != nil {
			p.logger.Warn("Failed to send audit message", zap.String("channel_id", groupID), zap.Error(err))
		}
		return NewLobbyError(KickedFromLobbyGroup, reason)
	}

	userID := session.UserID().String()

	params, ok := LoadParams(ctx)
	if !ok {
		return errors.New("failed to get session parameters")
	}
	var err error

	gg := p.guildGroupRegistry.Get(groupID)
	if gg == nil {
		return fmt.Errorf("failed to get guild group: %s", groupID)
	}

	// Check if the user is a member of the (private) guild.
	if gg.IsPrivate() {
		if gg.RoleMap.Member != "" && !gg.IsMember(userID) {
			return joinRejected("not_member", "You are not a member of this private guild.", "not a member of private guild")
		} else {
			// If the member role is not set, the user must be a member of the guild.
			if guildMember, err := p.discordCache.GuildMember(gg.GuildID, lobbyParams.DiscordID); err != nil {
				if err != nil && !IsDiscordErrorCode(err, discordgo.ErrCodeUnknownMember) {
					p.logger.Warn("Failed to get guild member. failing open.", zap.String("guild_id", gg.GuildID), zap.Error(err))
				}
			} else if guildMember == nil || guildMember.Pending {
				return joinRejected("not_member", "You are not a member of this private guild.", "not a member of private guild")
			}
		}
	}

	// User is suspended from the group.
	if gg.IsSuspended(userID, &params.xpID) {
		// User is suspended from the group.
		return joinRejected("suspended_user", "You are suspended from this guild. (role-based)", "is suspended via role")
	}

	// TODO move this to the session initialization. it's static data.
	var (
		suspensionRecord GuildEnforcementRecord
		suspendedUserID  string
	)
	if recordsByUserID := params.activeSuspensionRecords[groupID]; len(recordsByUserID) > 0 {
		for recordUserID, r := range recordsByUserID {
			if r.IsExpired() || r.SuspensionExpiry.Before(suspensionRecord.SuspensionExpiry) {
				// Skip expired records.
				continue
			}
			if recordUserID != userID {
				// The suspension is for an alternate account.
				if params.ignoreDisabledAlternates {
					// User is excluded from suspension checks if they are ignoring disabled alternates.
					continue
				}
				if gg.RejectPlayersWithSuspendedAlternates {
					suspensionRecord = r
					suspendedUserID = userID
				} else {
					logAuditMessage(fmt.Sprintf("Allowed alternate account <@!%s> (%s) of suspended user <@!%s> (%s): `%s` (expires <t:%d:R>)", lobbyParams.DiscordID, lobbyParams.DisplayName, recordUserID, session.Username(), r.UserNoticeText, r.SuspensionExpiry.Unix()))
				}
			}
		}

		if suspendedUserID != "" {
			const maxMessageLength = 64
			var metricTag, auditLog string

			if suspendedUserID == userID {
				// User has an active suspension
				auditLog = "suspension record"
				metricTag = "suspended_user"
			} else {
				// This is an alternate account of a suspended user.
				auditLog = fmt.Sprintf("suspended alt (<@!%s>)", suspendedUserID)
				metricTag = "suspended_alt_user"
			}
			reason := suspensionRecord.UserNoticeText

			if suspensionRecord.IsLimitedAccess() {
				reason = "Privates only: " + reason
			}
			expires := fmt.Sprintf(" [exp: %s]", FormatDuration(time.Until(suspensionRecord.SuspensionExpiry)))
			if len(reason)+len(expires) > maxMessageLength {
				reason = reason[:maxMessageLength-len(expires)-3] + "..."
			}
			reason = reason + expires
			return joinRejected(metricTag, reason, auditLog)
		}
	}

	// Check if the user is allowed to join public lobbies.
	if slices.Contains(evr.PublicModes, lobbyParams.Mode) {
		if gg.IsLimitedAccess(userID) || suspensionRecord.IsLimitedAccess() {
			auditLog := fmt.Sprintf("Rejected limited access user <@%s> (%s) from public lobby (%s)", lobbyParams.DiscordID, lobbyParams.DisplayName, lobbyParams.Mode)
			reason := "You are not allowed to join public lobbies."
			return joinRejected("limited_access_user", reason, auditLog)
		}
	}

	if gg.MinimumAccountAgeDays > 0 && !gg.IsAccountAgeBypass(userID) {
		// Check the account creation date.
		t, err := discordgo.SnowflakeTimestamp(lobbyParams.DiscordID)
		if err != nil {
			return fmt.Errorf("failed to get discord snowflake timestamp: %w", err)
		}

		if t.After(time.Now().AddDate(0, 0, -gg.MinimumAccountAgeDays)) {
			accountAge := time.Since(t).Hours() / 24
			reason := fmt.Sprintf("Your account age (%d days) is too new (<%d days) to join this guild. ", int(accountAge), gg.MinimumAccountAgeDays)
			auditLog := fmt.Sprintf("account age (%d > %d days.", int(accountAge), gg.MinimumAccountAgeDays)
			return joinRejected("account_age", reason, auditLog)
		}
	}

	if gg.BlockVPNUsers && params.isVPN && !gg.IsVPNBypass(userID) && params.ipInfo != nil {
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
			reason := fmt.Sprintf("Disable VPN to join %s", guildName)
			auditLog := fmt.Sprintf("vpn probability score %d >= %d", params.ipInfo.FraudScore(), gg.FraudScoreThreshold)
			return joinRejected("vpn_user", reason, auditLog)
		}
	}

	if len(gg.AllowedFeatures) > 0 {
		allowedFeatures := gg.AllowedFeatures
		for _, feature := range params.supportedFeatures {
			if !slices.Contains(allowedFeatures, feature) {
				reason := fmt.Sprintf("You are not allowed to join this guild with the feature `%s` enabled.", feature)
				auditLog := fmt.Sprintf("feature `%s` not allowed in this guild", feature)
				return joinRejected("feature_not_allowed", reason, auditLog)
			}
		}
	}

	displayName := params.DisplayName(groupID)
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

	// Force the players name to match in-game
	if gg.DisplayNameForceNickToIGN {
		go func() {
			// Search for them in a match from this guild
			member, err := p.discordCache.GuildMember(gg.GuildID, params.DiscordID())
			if err != nil {
				logger.Warn("Failed to get guild member", zap.Error(err))
			} else if member != nil {
				if displayName != InGameName(member) {
					AuditLogSendGuild(p.discordCache.dg, gg, fmt.Sprintf("Setting display name for `%s` to match in-game name: `%s`", member.User.Username, displayName))
					// Force the display name to match the in-game name
					if err := p.discordCache.dg.GuildMemberNickname(gg.GuildID, member.User.ID, displayName); err != nil {
						logger.Warn("Failed to set display name", zap.Error(err))
					}
				}
			}
		}()
	}

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
