package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/bwmarrin/discordgo"
	"github.com/gofrs/uuid/v5"
	"github.com/heroiclabs/nakama/v3/server/evr"
	"go.uber.org/zap"
)

func (p *EvrPipeline) LobbyJoinEntrants(ctx context.Context, logger *zap.Logger, matchID MatchID, role int, entrants []*EvrMatchPresence) error {
	return LobbyJoinEntrants(ctx, logger, p.db, p.matchRegistry, p.sessionRegistry, p.tracker, p.profileRegistry, matchID, role, entrants)
}

func LobbyJoinEntrants(ctx context.Context, logger *zap.Logger, db *sql.DB, matchRegistry MatchRegistry, sessionRegistry SessionRegistry, tracker Tracker, profileRegistry *ProfileRegistry, matchID MatchID, role int, entrants []*EvrMatchPresence) error {

	match, _, err := matchRegistry.GetMatch(ctx, matchID.String())
	if err != nil {
		return errors.Join(NewLobbyErrorf(InternalError, "failed to get match"), err)
	} else if match == nil {
		logger.Warn("Match not found", zap.String("mid", matchID.UUID.String()))
		return ErrMatchNotFound
	}

	label := MatchLabel{}
	if err := json.Unmarshal([]byte(match.GetLabel().GetValue()), &label); err != nil {
		return errors.Join(NewLobbyError(InternalError, "failed to unmarshal match label"), err)
	}
	groupID := label.GetGroupID()
	groupIDStr := groupID.String()

	// Ensure this player is authorized to join this lobby/match.
	session := sessionRegistry.Get(entrants[0].SessionID)
	if session == nil {
		return NewLobbyError(InternalError, "session not found")
	}

	metadataCache, ok := ctx.Value(ctxGuildGroupMetadataCacheKey{}).(*MapOf[uuid.UUID, *GroupMetadata])
	if ok {
		_, ok := metadataCache.Load(groupID)
		if !ok {
			groupMetadata, err := GetGuildGroupMetadata(ctx, db, groupIDStr)
			if err != nil {
				return errors.Join(NewLobbyError(InternalError, "failed to get guild group metadata"), err)
			}
			metadataCache.Store(groupID, groupMetadata)
		}
	}

	// Ensure the player supports the required features.
	for _, e := range entrants {
		for _, feature := range label.RequiredFeatures {
			if !slices.Contains(e.SupportedFeatures, feature) {
				logger.Warn("Player does not support required feature", zap.String("feature", feature), zap.String("mid", matchID.UUID.String()), zap.String("uid", e.UserID.String()))
				return NewLobbyErrorf(MissingEntitlement, "player does not support required feature: %s", feature)
			}
		}
	}

	// The lobbysessionsuccess message is sent to both the game server and the game client.

	// The final messages happen in a goroutine so this can wait for all of them to complete.
	errorCh := make(chan error, len(entrants))
	presences := make([]*EvrMatchPresence, 0, len(entrants))
	logger = logger.With(zap.String("mid", matchID.UUID.String()), zap.Int("role", role))

	serverSession := sessionRegistry.Get(uuid.FromStringOrNil(label.Broadcaster.SessionID))
	if serverSession == nil {
		return NewLobbyError(InternalError, "game server session not found")
	}

	for _, e := range entrants {
		session := sessionRegistry.Get(e.SessionID)
		if session == nil {
			logger.Warn("failed to find session", zap.String("sid", e.GetSessionId()))
			continue
		}

		if err := LobbyJoinEntrant(logger, matchRegistry, tracker, session, serverSession, &label, e, matchID, role, errorCh); err != nil {
			if err := SendEVRMessages(session, LobbySessionFailureFromError(label.Mode, groupID, err)); err != nil {
				logger.Error("failed to send lobby session failure to game client", zap.Error(err))
			}
			continue
		}
		presences = append(presences, e)
	}

	success := make([]*EvrMatchPresence, 0, len(presences))
	failed := make([]*EvrMatchPresence, 0, len(presences))

	for _, presence := range presences {
		select {
		case <-time.After(4 * time.Second):
			logger.Warn("Timed out waiting for all lobby session successes to complete")
			err = fmt.Errorf("timed out waiting for all lobby session successes to complete")
		case err := <-errorCh:
			if err != nil {
				logger.Warn("failed to send lobby session success to game client", zap.Any("presence", presence), zap.Error(err))
				failed = append(failed, presence)
			} else {
				success = append(success, presence)
			}
		}
	}

	logger.Info("Lobby join completed.", zap.Any("presences", presences), zap.Any("success", success), zap.Any("failed", failed), zap.Error(err))
	return nil
}

func LobbyJoinEntrant(logger *zap.Logger, matchRegistry MatchRegistry, tracker Tracker, session Session, serverSession Session, label *MatchLabel, e *EvrMatchPresence, matchID MatchID, role int, errorCh chan error) error {
	logger = logger.With(zap.String("uid", e.UserID.String()), zap.String("sid", e.SessionID.String()))

	sessionCtx := session.Context()
	metadata := EntrantMetadata{Presence: *e}.MarshalMap()

	var err error
	var found, allowed, isNew bool
	var reason string
	var labelStr string
	// Trigger MatchJoinAttempt
	found, allowed, isNew, reason, labelStr, _ = matchRegistry.JoinAttempt(sessionCtx, matchID.UUID, matchID.Node, e.UserID, e.SessionID, e.Username, e.SessionExpiry, nil, e.ClientIP, e.ClientPort, matchID.Node, metadata)
	if !found {
		err = NewLobbyErrorf(ServerDoesNotExist, "join attempt failed: match not found")
	} else if labelStr == "" {
		err = NewLobbyErrorf(ServerDoesNotExist, "join attempt failed: match label not found")
	} else if !allowed {
		err = NewLobbyErrorf(ServerIsFull, "join attempt failed: %s", reason)
	} else if !isNew {
		err = NewLobbyError(ServerIsFull, "join attempt failed: User already in match")
	}

	if err != nil {
		logger.Warn("failed to join match", zap.Error(err))
		return err
	}

	e = &EvrMatchPresence{}
	if err := json.Unmarshal([]byte(reason), &e); err != nil {
		return errors.Join(NewLobbyErrorf(InternalError, "failed to unmarshal match presence"), err)
	}

	matchIDStr := matchID.String()
	ops := []*TrackerOp{
		{
			PresenceStream{Mode: StreamModeEntrant, Subject: e.EntrantID(matchID), Label: matchID.Node},
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

	connectionSettings := label.GetEntrantConnectMessage(role, e.IsPCVR)
	if err := SendEVRMessages(serverSession, connectionSettings); err != nil {
		logger.Error("failed to send lobby session success to game server", zap.Error(err))
		return NewLobbyError(InternalError, "failed to send lobby session success to game server")
	}

	if err := LeaveMatchmakingStream(logger, session.(*sessionWS)); err != nil {
		logger.Error("failed to leave matchmaking stream", zap.Error(err))
	}

	// Send the lobby session success message to the game client.
	go func(session Session, msg *evr.LobbySessionSuccessv5) {
		<-time.After(250 * time.Millisecond)
		errorCh <- SendEVRMessages(session, connectionSettings)
	}(session, connectionSettings)

	return nil
}

func (p *EvrPipeline) authorizeGuildGroupSession(ctx context.Context, userID string, groupID string) error {

	membership, err := GetGuildGroupMembership(ctx, p.runtimeModule, userID, groupID)
	if err != nil {
		return errors.Join(NewLobbyError(InternalError, "failed to get guild group membership"), err)
	}

	if membership.isSuspended {
		return ErrSuspended
	}

	groupMetadata := membership.GuildGroup.Metadata
	if groupMetadata.MinimumAccountAgeDays > 0 && !slices.Contains(groupMetadata.RoleCache[groupMetadata.Roles.AccountAgeBypass], userID) {
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
			return NewLobbyErrorf(KickedFromLobbyGroup, "account is too young to join this guild")
		}
	}
	if groupMetadata.MembersOnlyMatchmaking {
		memberships, ok := ctx.Value(ctxGuildGroupMembershipsKey{}).(GuildGroupMemberships)
		if !ok {
			return NewLobbyError(KickedFromLobbyGroup, "failed to get guild group memberships")
		}
		if !memberships.IsMember(groupID) {
			return NewLobbyError(KickedFromLobbyGroup, "User is not a member of this guild")
		}
	}

	evrID, ok := ctx.Value(ctxEvrIDKey{}).(evr.EvrId)
	if !ok {
		return NewLobbyError(InternalError, "failed to get evr ID from session context")
	}

	metadata, ok := ctx.Value(ctxAccountMetadataKey{}).(AccountMetadata)
	if !ok {
		return NewLobbyError(InternalError, "failed to get account metadata from session context")
	}
	displayName := metadata.GetGroupDisplayNameOrDefault(groupID)

	if err := p.profileRegistry.SetLobbyProfile(ctx, uuid.FromStringOrNil(userID), evrID, displayName); err != nil {
		return errors.Join(NewLobbyErrorf(InternalError, "failed to set lobby profile"), err)
	}

	return nil
}
