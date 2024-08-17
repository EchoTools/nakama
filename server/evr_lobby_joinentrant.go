package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"slices"
	"time"

	"github.com/gofrs/uuid/v5"
	"github.com/heroiclabs/nakama/v3/server/evr"
	"go.uber.org/zap"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func LobbyJoinEntrants(ctx context.Context, logger *zap.Logger, matchRegistry MatchRegistry, sessionRegistry SessionRegistry, tracker Tracker, profileRegistry *ProfileRegistry, matchID MatchID, role int, entrants []*EvrMatchPresence) error {
	logger = logger.With(zap.String("mid", matchID.UUID.String()), zap.Int("role", role))
	match, _, err := matchRegistry.GetMatch(ctx, matchID.String())
	if err != nil || match == nil {
		return status.Error(codes.NotFound, "Match not found")
	}

	label := MatchLabel{}
	if err := json.Unmarshal([]byte(match.GetLabel().GetValue()), &label); err != nil {
		return fmt.Errorf("failed to unmarshal match label: %w", err)
	}

	// Ensure this player is authorized to join this lobby/match.
	session := sessionRegistry.Get(entrants[0].SessionID)
	if session == nil {
		return status.Error(codes.Internal, "Session not found")
	}
	s := session.(*sessionWS)

	if err := authorizeUserForGuildGroup(ctx, s.pipeline.db, s.userID.String(), label.GetGroupID().String()); err != nil {
		return err
	}

	// The lobbysessionsuccess message is sent to both the game server and the game client.
	serverSession := sessionRegistry.Get(uuid.FromStringOrNil(label.Broadcaster.SessionID))
	if serverSession == nil {
		logger.Error("game server session not found", zap.String("sessionID", label.Broadcaster.SessionID))
		return status.Error(codes.Internal, "game server session not found")
	}

	var labelStr string

	// The final messages happen in a goroutine so this can wait for all of them to complete.
	errorCh := make(chan error, len(entrants))

	presences := make([]*EvrMatchPresence, 0, len(entrants))

	for _, e := range entrants {
		logger := logger.With(zap.String("uid", e.UserID.String()), zap.String("sid", e.SessionID.String()))

		metadata := EntrantMetadata{Presence: *e}.MarshalMap()

		var err error
		var found, allowed, isNew bool
		var reason string

		// Trigger MatchJoinAttempt
		found, allowed, isNew, reason, labelStr, _ = matchRegistry.JoinAttempt(ctx, matchID.UUID, matchID.Node, e.UserID, e.SessionID, e.Username, e.SessionExpiry, nil, e.ClientIP, e.ClientPort, matchID.Node, metadata)
		if !found || labelStr == "" {
			err = status.Error(codes.NotFound, "Match not found")
		} else if !allowed {
			err = status.Error(codes.PermissionDenied, reason)
		} else if !isNew {
			err = status.Error(codes.AlreadyExists, "Already joined match")
		}

		if err != nil {
			logger.Warn("Failed to join match", zap.Error(err))
			continue
		}

		e = &EvrMatchPresence{}
		if err := json.Unmarshal([]byte(reason), &e); err != nil {
			return fmt.Errorf("failed to unmarshal attempt response: %w", err)
		}

		matchIDStr := matchID.String()
		ops := []*TrackerOp{
			{
				PresenceStream{Mode: StreamModeEntrant, Subject: e.EntrantID(matchID), Label: matchID.Node},
				PresenceMeta{Format: SessionFormatEvr, Username: e.Username, Status: e.String(), Hidden: true},
			},
			{
				PresenceStream{Mode: StreamModeService, Subject: e.SessionID, Label: StreamLabelMatchService},
				PresenceMeta{Format: SessionFormatEvr, Username: e.Username, Status: matchIDStr, Hidden: true},
			},
			{
				PresenceStream{Mode: StreamModeService, Subject: e.LoginSessionID, Label: StreamLabelMatchService},
				PresenceMeta{Format: SessionFormatEvr, Username: e.Username, Status: matchIDStr, Hidden: true},
			},
			{
				PresenceStream{Mode: StreamModeService, Subject: e.UserID, Label: StreamLabelMatchService},
				PresenceMeta{Format: SessionFormatEvr, Username: e.Username, Status: matchIDStr, Hidden: true},
			},
			{
				PresenceStream{Mode: StreamModeService, Subject: e.EvrID.UUID(), Label: StreamLabelMatchService},
				PresenceMeta{Format: SessionFormatEvr, Username: e.Username, Status: matchIDStr, Hidden: true},
			},
		}

		// Update the statuses. This is looked up by the pipeline when the game server sends the new entrant message.
		for _, op := range ops {
			if ok := tracker.Update(ctx, e.SessionID, op.Stream, e.UserID, op.Meta); !ok {
				return fmt.Errorf("failed to track session ID: %s", e.SessionID)
			}
		}

		// Get the client's session
		session := sessionRegistry.Get(e.SessionID)
		if session == nil {
			logger.Warn("Session not found", zap.String("sid", e.SessionID.String()))
			continue
		}
		sessionCtx := session.Context()

		// Prepare the cached profile for the lobby.
		displayName, ok := sessionCtx.Value(ctxDisplayNameOverrideKey{}).(string)
		if !ok {
			metadata, ok := sessionCtx.Value(ctxAccountMetadataKey{}).(AccountMetadata)
			if !ok {
				return status.Error(codes.Internal, "Failed to get account metadata from session context")
			}
			displayName = metadata.GetGroupDisplayNameOrDefault(label.GroupID.String())
		}
		evrID, ok := sessionCtx.Value(ctxEvrIDKey{}).(evr.EvrId)
		if !ok {
			return status.Error(codes.Internal, "Failed to get evrID from session context")
		}
		if err := profileRegistry.SetLobbyProfile(ctx, session.UserID(), evrID, displayName); err != nil {
			logger.Warn("Failed to set lobby profile", zap.Error(err))
			continue
		}

		connectionSettings := label.GetEntrantConnectMessage(role, e.IsPCVR)
		if err := SendEVRMessages(serverSession, connectionSettings); err != nil {
			logger.Error("Failed to send lobby session success to game server", zap.Error(err))
		}

		LeaveMatchmakingStream(logger, session.(*sessionWS))
		// Send the lobby session success message to the game client.
		go func(session Session, msg *evr.LobbySessionSuccessv5) {
			<-time.After(250 * time.Millisecond)
			errorCh <- SendEVRMessages(session, connectionSettings)
		}(session, connectionSettings)

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
				logger.Warn("Failed to send lobby session success to game client", zap.Any("presence", presence), zap.Error(err))
				failed = append(failed, presence)
			} else {
				success = append(success, presence)
			}
		}
	}

	logger.Info("Lobby join completed.", zap.Any("presences", presences), zap.Any("success", success), zap.Any("failed", failed), zap.Error(err))
	return nil
}

func authorizeUserForGuildGroup(ctx context.Context, db *sql.DB, userID string, groupID string) error {
	groupMetadata, err := GetGuildGroupMetadata(ctx, db, groupID)
	if err != nil {
		return status.Errorf(codes.Internal, "failed to get group metadata: %v", err)
	}

	if slices.Contains(groupMetadata.SuspendedUserIDs, userID) {
		return status.Errorf(codes.PermissionDenied, "user is suspended from the guild")
	}

	if groupMetadata.MinimumAccountAgeDays > 0 && !slices.Contains(groupMetadata.AccountAgeBypassUserIDs, userID) {
		// Check the account creation date.
		discordID, err := GetDiscordIDByUserID(ctx, db, userID)
		if err != nil {
			return status.Errorf(codes.Internal, "failed to get discord ID by user ID: %v", err)
		}

		if SnowflakeToTime(discordID).After(time.Now().AddDate(0, 0, -groupMetadata.MinimumAccountAgeDays)) {
			return status.Error(codes.PermissionDenied, "Account is too new to join this guild's sessions")
		}
	}
	if groupMetadata.MembersOnlyMatchmaking {
		groupIDs, err := GetGuildGroupIDsByUser(ctx, db, userID)
		if err != nil {
			return status.Errorf(codes.Internal, "failed to get group ID by guild ID: %v", err)
		}

		found := false
		for _, id := range groupIDs {
			if id == groupID {
				found = true
				break
			}
		}
		if !found {
			return status.Error(codes.PermissionDenied, "user is not a member of the members-only guild")
		}
	}
	return nil
}
