package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/bwmarrin/discordgo"
	"github.com/gofrs/uuid/v5"
	"github.com/intinig/go-openskill/types"
	"go.uber.org/zap"
)

func NewEvrMatchPresenceFromSession(session Session, rating *types.Rating, groupID string, partyID uuid.UUID, role int) (*EvrMatchPresence, error) {
	sessionCtx := session.Context()
	params, ok := LoadParams(sessionCtx)
	if !ok {
		return nil, NewLobbyError(InternalError, "failed to get session parameters")
	}

	displayName := params.AccountMetadata.GetGroupDisplayNameOrDefault(groupID)

	r := types.Rating{}
	if rating != nil {
		r = *rating
	}

	return &EvrMatchPresence{

		Node:           params.Node,
		UserID:         session.UserID(),
		SessionID:      session.ID(),
		LoginSessionID: params.LoginSession.ID(),
		Username:       session.Username(),
		DisplayName:    displayName,
		EvrID:          params.EvrID,
		PartyID:        partyID,
		RoleAlignment:  role,
		DiscordID:      params.DiscordID,
		ClientIP:       session.ClientIP(),
		ClientPort:     session.ClientPort(),
		IsPCVR:         params.IsPCVR,
		Rating:         r,
	}, nil
}

func (p *EvrPipeline) LobbySessionGet(ctx context.Context, logger *zap.Logger, matchID MatchID) (*MatchLabel, Session, error) {
	return LobbySessionGet(ctx, logger, p.matchRegistry, p.tracker, p.profileRegistry, p.sessionRegistry, matchID)
}

func LobbySessionGet(ctx context.Context, logger *zap.Logger, matchRegistry MatchRegistry, tracker Tracker, profileRegistry *ProfileRegistry, sessionRegistry SessionRegistry, matchID MatchID) (*MatchLabel, Session, error) {

	match, _, err := matchRegistry.GetMatch(ctx, matchID.String())
	if err != nil {
		return nil, nil, fmt.Errorf("failed to get match: %w", err)
	}

	label := &MatchLabel{}
	if err := json.Unmarshal([]byte(match.GetLabel().GetValue()), label); err != nil {
		return nil, nil, fmt.Errorf("failed to unmarshal match label: %w", err)
	}

	serverSession := sessionRegistry.Get(uuid.FromStringOrNil(label.Broadcaster.SessionID))
	if serverSession == nil {
		return nil, nil, fmt.Errorf("failed to get server session")
	}

	return label, serverSession, nil
}

func (p *EvrPipeline) LobbyJoinEntrant(logger *zap.Logger, serverSession Session, label *MatchLabel, role int, presence *EvrMatchPresence) error {
	session := p.sessionRegistry.Get(presence.SessionID)
	if session == nil {
		return NewLobbyError(InternalError, "session is nil")
	}
	return LobbyJoinEntrant(logger, p.matchRegistry, p.tracker, session, serverSession, label, presence, role)
}

func LobbyJoinEntrant(logger *zap.Logger, matchRegistry MatchRegistry, tracker Tracker, session Session, serverSession Session, label *MatchLabel, e *EvrMatchPresence, role int) error {
	if session == nil || serverSession == nil {
		return NewLobbyError(InternalError, "session is nil")
	}

	logger = logger.With(zap.String("uid", e.UserID.String()), zap.String("sid", e.SessionID.String()))

	for _, feature := range label.RequiredFeatures {
		if !slices.Contains(e.SupportedFeatures, feature) {
			logger.Warn("Player does not support required feature", zap.String("feature", feature), zap.String("mid", label.ID.UUID.String()), zap.String("uid", e.UserID.String()))
			return NewLobbyErrorf(MissingEntitlement, "player does not support required feature: %s", feature)
		}
	}

	sessionCtx := session.Context()
	metadata := EntrantMetadata{Presence: *e}.MarshalMap()

	var err error
	var found, allowed, isNew bool
	var reason string
	var labelStr string
	// Trigger MatchJoinAttempt
	found, allowed, isNew, reason, labelStr, _ = matchRegistry.JoinAttempt(sessionCtx, label.ID.UUID, label.ID.Node, e.UserID, e.SessionID, e.Username, e.SessionExpiry, nil, e.ClientIP, e.ClientPort, label.ID.Node, metadata)
	if !found {
		err = NewLobbyErrorf(ServerDoesNotExist, "join attempt failed: match not found")
	} else if labelStr == "" {
		err = NewLobbyErrorf(ServerDoesNotExist, "join attempt failed: match label not found")
	} else if !allowed {
		err = NewLobbyErrorf(ServerIsFull, "join attempt failed: not allowed: %s", reason)
	} else if !isNew {
		logger.Warn("Player is already in the match. ignoring.", zap.String("mid", label.ID.UUID.String()), zap.String("uid", e.UserID.String()))
		return nil
	}

	if err != nil {
		logger.Warn("failed to join match", zap.Error(err))
		return fmt.Errorf("failed to join match: %w", err)
	}

	e = &EvrMatchPresence{}
	if err := json.Unmarshal([]byte(reason), &e); err != nil {
		err = errors.Join(NewLobbyErrorf(InternalError, "failed to unmarshal match presence"), err)
		return err
	}

	// Leave any other lobby group stream.
	lobbyGroupStream := PresenceStream{Mode: StreamModeLobbyGroup, Subject: label.GetGroupID()}

	untrackModes := map[uint8]struct{}{
		StreamModeLobbyGroup:  {},
		StreamModeEntrant:     {},
		StreamModeMatchmaking: {},
	}

	tracker.UntrackLocalByModes(session.ID(), untrackModes, lobbyGroupStream)

	matchIDStr := label.ID.String()

	ops := []*TrackerOp{
		{
			PresenceStream{Mode: StreamModeLobbyGroup, Subject: label.GetGroupID()},
			PresenceMeta{Format: SessionFormatEVR, Username: e.Username, Status: matchIDStr, Hidden: false},
		},
		{
			PresenceStream{Mode: StreamModeEntrant, Subject: e.EntrantID(label.ID), Label: label.ID.Node},
			PresenceMeta{Format: SessionFormatEVR, Username: e.Username, Status: e.String(), Hidden: true},
		},
		{
			PresenceStream{Mode: StreamModeService, Subject: e.SessionID, Label: StreamLabelMatchService},
			PresenceMeta{Format: SessionFormatEVR, Username: e.Username, Status: matchIDStr, Hidden: true},
		},
		{
			PresenceStream{Mode: StreamModeService, Subject: e.LoginSessionID, Label: StreamLabelMatchService},
			PresenceMeta{Format: SessionFormatEVR, Username: e.Username, Status: matchIDStr, Hidden: true},
		},
		{
			PresenceStream{Mode: StreamModeService, Subject: e.UserID, Label: StreamLabelMatchService},
			PresenceMeta{Format: SessionFormatEVR, Username: e.Username, Status: matchIDStr, Hidden: true},
		},
		{
			PresenceStream{Mode: StreamModeService, Subject: e.EvrID.UUID(), Label: StreamLabelMatchService},
			PresenceMeta{Format: SessionFormatEVR, Username: e.Username, Status: matchIDStr, Hidden: true},
		},
	}
	// Update the statuses. This is looked up by the pipeline when the game server sends the new entrant message.
	for _, op := range ops {
		if ok := tracker.Update(sessionCtx, e.SessionID, op.Stream, e.UserID, op.Meta); !ok {
			return NewLobbyError(InternalError, "failed to track session ID")
		}
	}

	connectionSettings := label.GetEntrantConnectMessage(role, e.IsPCVR, e.DisableEncryption, e.DisableMAC)
	if err := SendEVRMessages(serverSession, connectionSettings); err != nil {
		logger.Error("failed to send lobby session success to game server", zap.Error(err))

		return NewLobbyError(InternalError, "failed to send lobby session success to game server")
	}

	// Send the lobby session success message to the game client.
	<-time.After(250 * time.Millisecond)

	err = SendEVRMessages(session, connectionSettings)
	if err != nil {
		logger.Error("failed to send lobby session success to game client", zap.Error(err))
		return NewLobbyError(InternalError, "failed to send lobby session success to game client")
	}

	if err := LeaveMatchmakingStream(logger, session.(*sessionWS)); err != nil {
		logger.Error("failed to leave matchmaking stream", zap.Error(err))
	}

	logger.Info("Joined entrant.", zap.String("mid", label.ID.UUID.String()), zap.String("uid", e.UserID.String()), zap.String("sid", e.SessionID.String()))
	return nil
}

func (p *EvrPipeline) authorizeGuildGroupSession(ctx context.Context, session Session, groupID string) error {
	userID := session.UserID().String()

	params, ok := LoadParams(ctx)
	if !ok {
		return NewLobbyError(InternalError, "failed to get session parameters")
	}

	guildGroups := p.guildGroupCache.GuildGroups()
	gg, ok := guildGroups[groupID]
	if !ok {
		return NewLobbyErrorf(InternalError, "failed to get guild group metadata for %s", groupID)
	}
	groupMetadata := gg.Metadata
	sendAuditMessage := groupMetadata.AuditChannelID != ""
	discordID := p.discordCache.UserIDToDiscordID(userID)

	membership, ok := params.Memberships[groupID]
	if !ok && groupMetadata.MembersOnlyMatchmaking {
		if sendAuditMessage {

			if _, err := p.appBot.dg.ChannelMessageSend(groupMetadata.AuditChannelID, fmt.Sprintf("Rejected non-member <@%s>", discordID)); err != nil {
				p.logger.Warn("Failed to send audit message", zap.String("channel_id", groupMetadata.AuditChannelID), zap.Error(err))
			}

		}

		return NewLobbyError(KickedFromLobbyGroup, "User is not a member of this guild")
	}

	if membership.IsSuspended {

		if sendAuditMessage {
			if _, err := p.appBot.dg.ChannelMessageSend(groupMetadata.AuditChannelID, fmt.Sprintf("Rejected suspended user <@%s>.", discordID)); err != nil {
				p.logger.Warn("Failed to send audit message", zap.String("channel_id", groupMetadata.AuditChannelID), zap.Error(err))
			}
		}

		return ErrSuspended
	}

	if groupMetadata.MinimumAccountAgeDays > 0 && groupMetadata.IsAccountAgeBypass(userID) {
		// Check the account creation date.
		discordID, err := GetDiscordIDByUserID(ctx, p.db, userID)
		if err != nil {
			return NewLobbyErrorf(InternalError, "failed to get discord ID by user ID: %v", err)
		}

		t, err := discordgo.SnowflakeTimestamp(discordID)
		if err != nil {
			return NewLobbyErrorf(InternalError, "failed to get discord snowflake timestamp: %v", err)
		}

		if t.After(time.Now().AddDate(0, 0, -groupMetadata.MinimumAccountAgeDays)) {

			if sendAuditMessage {

				accountAge := time.Since(t).Hours() / 24

				if _, err := p.appBot.dg.ChannelMessageSend(groupMetadata.AuditChannelID, fmt.Sprintf("Rejected user <@%s> because of account age (%d days).", discordID, int(accountAge))); err != nil {
					p.logger.Warn("Failed to send audit message", zap.String("channel_id", groupMetadata.AuditChannelID), zap.Error(err))
				}
			}

			return NewLobbyErrorf(KickedFromLobbyGroup, "account is too young to join this guild")
		}
	}

	if groupMetadata.BlockVPNUsers && params.IsVPN && !groupMetadata.IsVPNBypass(userID) {

		score := p.ipqsClient.Score(session.ClientIP())

		if score >= groupMetadata.FraudScoreThreshold {

			if sendAuditMessage {

				if _, err := p.appBot.dg.ChannelMessageSend(groupMetadata.AuditChannelID, fmt.Sprintf("Rejected VPN user <@%s> (score: %d) from %s", discordID, score, session.ClientIP())); err != nil {
					p.logger.Warn("Failed to send audit message", zap.String("channel_id", groupMetadata.AuditChannelID), zap.Error(err))
				}
			}

			return NewLobbyError(KickedFromLobbyGroup, "this guild does not allow VPN users")
		}
	}

	features := params.SupportedFeatures
	if ok && len(features) > 0 {
		allowedFeatures := groupMetadata.AllowedFeatures
		for _, feature := range features {
			if !slices.Contains(allowedFeatures, feature) {
				return NewLobbyError(KickedFromLobbyGroup, "This guild does not allow clients with `feature DLLs``.")
			}
		}
	}

	displayName := params.AccountMetadata.GetGroupDisplayNameOrDefault(groupID)

	if err := p.profileRegistry.SetLobbyProfile(ctx, uuid.FromStringOrNil(userID), params.EvrID, displayName); err != nil {
		return errors.Join(NewLobbyErrorf(InternalError, "failed to set lobby profile"), err)
	}

	session.Logger().Info("Authorized access to lobby session", zap.String("gid", groupID), zap.String("display_name", displayName))

	return nil
}
