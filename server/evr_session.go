package server

import (
	"context"
	"fmt"
	"net/http"
	"regexp"
	"slices"
	"strings"

	"github.com/gofrs/uuid/v5"
	"github.com/heroiclabs/nakama-common/runtime"
	"go.uber.org/zap"
)

// LobbySession is the matchmaker/lobby connection from the client
func LobbySession(s *sessionWS, sessionRegistry SessionRegistry, loginSessionID uuid.UUID) error {
	if loginSessionID == uuid.Nil {
		return fmt.Errorf("login session ID is nil")
	}

	// EVR uses multiple connections, that reference the login session, without using session IDs for any of the secondary connections.
	// The login session is the primary session, and the secondary sessions are validated against it.

	// If the session is not authenticated, use the login session to set the session information.
	if s.UserID() == uuid.Nil {

		// Obtain the login session.
		loginSession, ok := sessionRegistry.Get(loginSessionID).(*sessionWS)
		if !ok || loginSession == nil {
			return fmt.Errorf("login session not found: %v", loginSessionID)
		}

		// Use the context to avoid any locking issues.
		loginCtx := loginSession.Context()
		loginParams, ok := LoadParams(loginCtx)
		if !ok {
			return fmt.Errorf("login session parameters not found: %v", loginSessionID)
		}
		lobbyParams, ok := LoadParams(s.Context())
		if !ok {
			return fmt.Errorf("lobby session parameters not found: %v", s.id)
		}

		loginParams.lobbySession = s
		loginParams.requiredFeatures = lobbyParams.requiredFeatures

		// Store the login parameters as the lobby session's parameters.
		lobbyCtx := s.Context()
		StoreParams(lobbyCtx, &loginParams)

		// Create a derived context for this session.
		lobbyCtx = context.WithValue(lobbyCtx, ctxUserIDKey{}, loginSession.UserID())     // apiServer compatibility
		lobbyCtx = context.WithValue(lobbyCtx, ctxUsernameKey{}, loginSession.Username()) // apiServer compatibility

		// Set the session information
		s.Lock()
		s.ctx = lobbyCtx
		s.userID = loginSession.UserID()
		s.SetUsername(loginSession.Username())
		s.logger = s.logger.With(
			zap.String("login_sid", loginSessionID.String()),
			zap.String("uid", s.userID.String()),
			zap.String("evr_id", loginParams.xpID.Token()),
			zap.String("username", s.Username()))

		// cancel/disconnect this session if the login session is cancelled.
		go func() {
			<-loginCtx.Done()
			s.Close("echovr login session closed", runtime.PresenceReasonDisconnect)
		}()
		s.Unlock()
	}
	return nil
}

func SetSessionWSLogger(s Session, logger *zap.Logger) {
	if s == nil {
		return
	}
	if ws, ok := s.(*sessionWS); ok {
		ws.logger = logger
	} else {
		s.Logger().Error("SetSessionWSLogger called on non-websocket session")
	}
}

func BroadcasterSession(s *sessionWS, userID uuid.UUID, username string, serverID uint64) error {
	// Broadcaster's are "partial" sessions, and aren't directly associated with the user.
	// There's no information that directly links this connection to the login connection.

	// This is the first time the session has been validated.

	ctx := context.WithValue(s.Context(), ctxUserIDKey{}, userID) // apiServer compatibility
	ctx = context.WithValue(ctx, ctxUsernameKey{}, username)      // apiServer compatibility

	s.SetUsername(username)
	s.Lock()
	s.ctx = ctx
	s.userID = userID
	s.logger = s.logger.With(zap.String("operator_id", userID.String()), zap.String("server_id", fmt.Sprintf("%d", serverID)))
	s.Unlock()
	s.tracker.TrackMulti(ctx, s.id, []*TrackerOp{
		// EVR packet data stream for the login session by Session ID and broadcaster ID
		{
			Stream: PresenceStream{Mode: StreamModeService, Subject: s.userID, Label: StreamLabelGameServerService},
			Meta:   PresenceMeta{Format: s.format, Username: s.username.String(), Hidden: false},
		},
		// EVR packet data stream by session ID and broadcaster ID
		{
			Stream: PresenceStream{Mode: StreamModeService, Subject: s.id, Label: StreamLabelGameServerService},
			Meta:   PresenceMeta{Format: s.format, Username: s.username.String(), Hidden: false},
		},
	}, s.userID)

	return nil
}

func Secondary(s *sessionWS, loginSession *sessionWS, isLobby bool, isServer bool) error {
	// This is a secondary session, so it should inherit the login session's context.

	params, ok := LoadParams(loginSession.Context())
	if !ok {
		return fmt.Errorf("login session parameters not found: %v", loginSession.ID())
	}

	if isLobby {
		params.lobbySession = s
	}
	if isServer {
		params.serverSession = s
	}

	StoreParams(s.Context(), &params)

	// Replace the session context with a derived one that includes the login session ID and the EVR ID
	ctx := s.Context()
	s.Lock()
	ctx = context.WithValue(ctx, ctxUserIDKey{}, loginSession.userID)       // apiServer compatibility
	ctx = context.WithValue(ctx, ctxUsernameKey{}, loginSession.Username()) // apiServer compatibility
	s.ctx = ctx
	s.userID = loginSession.userID
	s.SetUsername(loginSession.Username())
	s.logger = s.logger.With(zap.String("loginsid", s.id.String()), zap.String("uid", s.userID.String()), zap.String("evrid", params.xpID.String()), zap.String("username", loginSession.Username()))
	s.Unlock()

	return nil
}

func parseUserQueryFunc(request *http.Request, key string, maxLength int, pattern *regexp.Regexp) string {
	v := request.URL.Query().Get(key)
	if v != "" {
		if len(v) > maxLength {
			v = v[:maxLength]
		}
		if pattern != nil && !pattern.MatchString(v) {
			return ""
		}
	}
	return v
}

func parseUserQueryCommaDelimited(request *http.Request, key string, maxLength int, pattern *regexp.Regexp) []string {
	// Add the items list to the urlparam, sanitizing it
	items := make([]string, 0)
	if v := request.URL.Query().Get(key); v != "" {
		s := strings.Split(v, ",")
		for _, f := range s {
			if f == "" {
				continue
			}
			if len(f) > maxLength {
				f = f[:maxLength]
			}
			if pattern != nil && !pattern.MatchString(f) {
				continue
			}
			items = append(items, f)
		}
		if len(items) == 0 {
			return nil
		}
	}
	slices.Sort(items)
	return items
}
