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

	userID := session.UserID().String()

	params, ok := LoadParams(ctx)
	if !ok {
		return errors.New("failed to get session parameters")
	}

	gg := p.guildGroupRegistry.Get(groupID)
	if gg == nil {
		return fmt.Errorf("failed to get guild group: %s", groupID)
	}

	// Use the new centralized authorizer
	authorizer := NewDefaultLobbyAuthorizer(p)
	authCtx := &LobbyAuthorizationContext{
		Session:     session,
		LobbyParams: lobbyParams,
		GuildGroup:  gg,
		UserID:      userID,
		GroupID:     groupID,
		Params:      params,
	}

	result := authorizer.Authorize(ctx, logger, authCtx)
	if !result.Authorized {
		metricsTags["error"] = result.ErrorCode
		
		// Log audit message
		auditMessage := fmt.Sprintf("Rejected lobby join by %s <@%s>[%s] to `%s`: %s", 
			EscapeDiscordMarkdown(lobbyParams.DisplayName), 
			lobbyParams.DiscordID, 
			session.Username(), 
			lobbyParams.Mode, 
			result.AuditMessage)
		
		if _, err := p.appBot.LogAuditMessage(ctx, groupID, auditMessage, true); err != nil {
			p.logger.Warn("Failed to send audit message", zap.String("channel_id", groupID), zap.Error(err))
		}
		
		return NewLobbyError(KickedFromLobbyGroup, result.ErrorMessage)
	}

	// Store the generated profile
	if result.Profile != nil {
		if _, err := p.profileCache.Store(session.ID(), *result.Profile); err != nil {
			return fmt.Errorf("failed to cache profile: %w", err)
		}
	}

	// Log successful authorization
	displayName := params.profile.GetGroupIGN(groupID)
	session.Logger().Info("Authorized access to lobby session", zap.String("gid", groupID), zap.String("display_name", displayName))

	// Emit authorization event
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

	metricsTags["error"] = "nil"
	return nil
}
