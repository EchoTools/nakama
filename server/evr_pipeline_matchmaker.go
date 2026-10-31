package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/gofrs/uuid/v5"
	"github.com/heroiclabs/nakama-common/rtapi"
	"github.com/heroiclabs/nakama/v3/server/evr"
	"go.uber.org/zap"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const (
	FriendStateFriends = iota
	FriendInvitationSent
	FriendInvitationReceived
	FriendStateBlocked
)

// lobbyMatchmakerStatusRequest is a message requesting the status of the matchmaker.
func (p *EvrPipeline) lobbyMatchmakerStatusRequest(ctx context.Context, logger *zap.Logger, session *sessionWS, in evr.Message) error {
	_ = in.(*evr.LobbyMatchmakerStatusRequest)

	// This cannot have an unrequire message, otherwise the client will hang forever with "MATCHMAKING"
	err := session.SendEvr(evr.NewLobbyMatchmakerStatusResponse())
	if err != nil {
		return fmt.Errorf("LobbyMatchmakerStatus: %w", err)
	}
	return nil
}

func (p *EvrPipeline) lobbySessionRequest(ctx context.Context, logger *zap.Logger, session *sessionWS, in evr.Message) error {
	request, ok := in.(evr.LobbySessionRequest)
	if !ok {
		return fmt.Errorf("expected evr.LobbySessionRequest, got %T", in)
	}

	go func() {

		lobbyParams, err := NewLobbyParametersFromRequest(ctx, logger, p.nk, session, in.(evr.LobbySessionRequest))
		if err != nil {
			logger.Error("Failed to create lobby parameters", zap.Error(err))
			if err := session.SendEvr(LobbySessionFailureFromError(request.GetMode(), request.GetGroupID(), err)); err != nil {
				logger.Error("Failed to send lobby session failure message", zap.Error(err))
			}
			return
		}

		ctx, cancel := context.WithCancel(ctx)
		defer cancel()

		ctx = context.WithValue(ctx, ctxLobbyParametersKey{}, lobbyParams)

		if err := p.handleLobbySessionRequest(ctx, logger, session, request, lobbyParams); err != nil {
			p.answerLobbySessionRequestError(ctx, logger, session, in, request, lobbyParams, err)
			return
		}
	}()
	return nil
}

// answerLobbySessionRequestError answers a lobby session request that failed: the client is sent a
// LobbySessionFailure for err, then the failure is posted to the guild's error channel. It is the one
// place a failed request is answered. The post goes to Discord, which rate-limits, so it runs after
// the client's answer and off this goroutine (postLobbyErrorAuditAsync).
func (p *EvrPipeline) answerLobbySessionRequestError(ctx context.Context, logger *zap.Logger, session *sessionWS, in evr.Message, request evr.LobbySessionRequest, lobbyParams *LobbySessionParameters, err error) {
	if lobbyParams.Verbose {
		if sendErr := session.Send(&rtapi.Envelope{
			Message: &rtapi.Envelope_Error{
				Error: &rtapi.Error{
					Code:    int32(codes.Internal),
					Message: err.Error(),
				},
			},
		}, true); sendErr != nil {
			logger.Warn("Failed to send verbose lobby session error", zap.Error(sendErr))
		}
	}

	if ctx.Err() == context.Canceled || errors.Is(err, context.Canceled) {
		logger.Debug("Lobby session request was canceled (context.Canceled)")
		return
	}
	logger.Debug("Failed to process lobby session request", zap.Error(err))

	params, ok := LoadParams(ctx)
	if !ok {
		logger.Error("Failed to load params from context")
	} else {
		params.lastMatchmakingError.Store(err)
	}

	if err := session.SendEvr(LobbySessionFailureFromError(request.GetMode(), request.GetGroupID(), err)); err != nil {
		logger.Error("Failed to send lobby session failure message", zap.Error(err))
	}

	p.postLobbyErrorAuditAsync(ctx, logger, session.ID().String(), lobbyParams.GroupID.String(), fmt.Sprintf("```fix\n%s\n\n%T failed:\n %v\n```", session.Username(), in, err), err)
}

const (
	// lobbyErrorAuditMaxInFlight bounds concurrent posts from one session, so
	// one retrying client cannot occupy every audit slot for every other client.
	lobbyErrorAuditMaxInFlight = 8
	// lobbyErrorAuditTimeout bounds one post. The request's context ends when the request is answered,
	// so a post runs on a detached context of its own.
	lobbyErrorAuditTimeout = 30 * time.Second
)

var lobbyErrorAuditSessions = struct {
	sync.Mutex
	slots map[string]chan struct{}
}{slots: make(map[string]chan struct{})}

// postLobbyErrorAuditAsync posts message to the guild's error channel without waiting for it.
// requestErr is the failure being posted, for the warn when the post errors or is skipped.
func (p *EvrPipeline) postLobbyErrorAuditAsync(ctx context.Context, logger *zap.Logger, sessionID, groupID, message string, requestErr error) {
	lobbyErrorAuditSessions.Lock()
	slots := lobbyErrorAuditSessions.slots[sessionID]
	if slots == nil {
		slots = make(chan struct{}, lobbyErrorAuditMaxInFlight)
		lobbyErrorAuditSessions.slots[sessionID] = slots
	}
	select {
	case slots <- struct{}{}:
		lobbyErrorAuditSessions.Unlock()
	default:
		lobbyErrorAuditSessions.Unlock()
		logger.Warn("Lobby error audit post skipped: too many posts in flight",
			zap.Int("in_flight_limit", lobbyErrorAuditMaxInFlight), zap.String("group_id", groupID), zap.Error(requestErr))
		return
	}
	postCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), lobbyErrorAuditTimeout)
	go func() {
		defer func() {
			lobbyErrorAuditSessions.Lock()
			<-slots
			if len(slots) == 0 {
				delete(lobbyErrorAuditSessions.slots, sessionID)
			}
			lobbyErrorAuditSessions.Unlock()
		}()
		defer cancel()
		if err := p.postLobbyErrorAudit(postCtx, groupID, message); err != nil {
			logger.Warn("Failed to log audit message", zap.Error(err), zap.String("group_id", groupID), zap.NamedError("request_error", requestErr))
		}
	}()
}

// postLobbyErrorAudit posts message to the guild's error channel.
func (p *EvrPipeline) postLobbyErrorAudit(ctx context.Context, groupID, message string) error {
	if p.lobbyErrorAudit != nil {
		return p.lobbyErrorAudit(ctx, groupID, message)
	}
	_, err := p.appBot.LogUserErrorMessage(ctx, groupID, message, false)
	return err
}

// lobbyPingResponse is a message responding to a ping request.
func (p *EvrPipeline) lobbyPingResponse(ctx context.Context, logger *zap.Logger, session *sessionWS, in evr.Message) error {
	receivedAt := time.Now()
	response, ok := in.(*evr.LobbyPingResponse)
	if !ok {
		return fmt.Errorf("expected *evr.LobbyPingResponse, got %T", in)
	}

	var (
		now            = time.Now().UTC()
		expiry         = now.Add(-14 * 24 * time.Hour)
		latencyHistory *LatencyHistory
		limit          = 25
	)

	params, ok := LoadParams(ctx)
	if !ok {
		return fmt.Errorf("failed to load params from context")
	}
	latencyHistory = params.latencyHistory.Load()

	// Build an allowlist of IPs from active game server presences so that
	// clients cannot inject arbitrary IPs into their latency history and
	// inflate rtt_* matchmaker properties.
	// Query both guild-specific streams and the global stream, matching the
	// same logic used in sendPingRequest to build the candidate list.
	knownIPs := make(map[string]struct{})
	addPresencesFunc := func(subject string) {
		presences, err := p.nk.StreamUserList(StreamModeGameServer, subject, "", "", false, true)
		if err != nil {
			logger.Warn("failed to list game server presences for ping validation", zap.String("subject", subject), zap.Error(err))
			return
		}
		for _, presence := range presences {
			gp := &GameServerPresence{}
			if err := json.Unmarshal([]byte(presence.GetStatus()), gp); err != nil {
				continue
			}
			if ip := gp.Endpoint.GetExternalIP(); ip != "" {
				knownIPs[ip] = struct{}{}
			}
			if ip := gp.Endpoint.InternalIP; ip != nil {
				knownIPs[ip.String()] = struct{}{}
			}
		}
	}

	// Include guild-specific game servers
	for groupID := range params.guildGroups {
		addPresencesFunc(groupID)
	}
	// Include global game servers
	addPresencesFunc(uuid.Nil.String())

	// applyPingResults re-applies this session's just-received ping samples onto
	// the (possibly refreshed) latencyHistory. It is run once now and re-run on
	// each version-conflict retry inside LatencyHistory.writeWithRetry, which
	// re-reads the concurrent winner's object before each retry, so a user's
	// concurrent sessions merge their samples losslessly instead of one
	// clobbering the other. The Add call keeps limit/expiry pruning inside the
	// closure so the merged result stays bounded.
	applyPingResults := func() error {
		for _, result := range response.Results {
			ip := result.ExternalIP
			if result.ExternalIP.IsUnspecified() {
				ip = result.InternalIP
			}

			if knownIPs != nil {
				if _, ok := knownIPs[ip.String()]; !ok {
					logger.Debug("dropping ping result for unknown game server IP", zap.String("ip", ip.String()))
					continue
				}
			}

			latencyHistory.Add(ip, int(result.PingMilliseconds), limit, expiry)
		}
		return nil
	}

	// Apply this session's samples once before the first write attempt.
	if err := applyPingResults(); err != nil {
		return status.Errorf(codes.Internal, "failed to apply ping results: %v", err)
	}

	if err := latencyHistory.writeWithRetry(ctx, p.nk, session.UserID().String(), applyPingResults); err != nil {
		return status.Errorf(codes.Internal, "failed to write latency history: %v", err)
	}

	// Signal any pre-join ping waiter for this session.
	if notifyPreJoinPingWaiter(session.ID()) {
		logger.Info("Pre-join ping response delivered",
			zap.String("uid", session.UserID().String()),
			zap.String("sid", session.ID().String()),
			zap.Int64("handler_ms", time.Since(receivedAt).Milliseconds()),
			zap.Int("results", len(response.Results)),
		)
	}

	return nil
}

func SendEVRMessages(session Session, unrequire bool, messages ...evr.Message) error {
	if session == nil {
		return fmt.Errorf("session is nil")
	}

	logger := session.Logger()
	isDebug := logger.Core().Enabled(zap.DebugLevel)
	if isDebug {
		msgnames := make([]string, 0, len(messages))
		for _, msg := range messages {
			msgnames = append(msgnames, fmt.Sprintf("%T", msg))
		}
		logger.Debug("Sending messages.", zap.Any("message", msgnames))
	}
	for _, message := range messages {
		if message == nil {
			continue
		}

		payload, err := evr.Marshal(message)
		if err != nil {
			return fmt.Errorf("could not marshal message: %w", err)
		}

		if err := session.SendBytes(payload, true); err != nil {
			return err
		}
	}
	if unrequire {
		if err := session.SendBytes(unrequireBytes, true); err != nil {
			return err
		}
	}

	return nil
}

func LeavePartyStream(s *sessionWS) {
	s.tracker.UntrackLocalByModes(s.id, map[uint8]struct{}{StreamModeParty: {}}, PresenceStream{})
}

// LobbyPendingSessionCancel is a message from the server to the client, indicating that the user wishes to cancel matchmaking.
// When a party member cancels, ALL party members' matchmaking is cancelled and any active ticket is removed.
// Cancel does NOT remove members from the party (BAC-022).
func (p *EvrPipeline) lobbyPendingSessionCancel(ctx context.Context, logger *zap.Logger, session *sessionWS, in evr.Message) error {
	// Always leave the caller's matchmaking stream first.
	if err := LeaveMatchmakingStream(logger, session); err != nil {
		logger.Warn("Failed to leave matchmaking stream", zap.Error(err))
	}

	// If the player is in a party, cancel matchmaking for ALL members.
	// Per spec: "Any member cancels matchmaking -> cancel ALL members'
	// matchmaking. Remove the ticket. No partial tickets."
	params, ok := LoadParams(session.Context())
	if !ok || params.currentPartyID == uuid.Nil {
		return nil // Solo player -- already cancelled above.
	}

	// Remove any active matchmaking tickets for the party.
	partyID := params.currentPartyID
	if ph, ok := p.nk.partyRegistry.Get(partyID); ok {
		lobbyGroup := &LobbyGroup{ph: ph}
		if err := lobbyGroup.MatchmakerRemoveAll(); err != nil {
			logger.Warn("Failed to remove party matchmaking tickets on cancel",
				zap.String("party_id", partyID.String()),
				zap.Error(err))
		}
	}

	// Close all party members' matchmaking streams so their
	// monitorMatchmakingStream goroutines cancel their lobbyFind contexts.
	partyStream := PresenceStream{Mode: StreamModeParty, Subject: partyID, Label: p.node}
	partyPresences := p.nk.tracker.ListByStream(partyStream, true, true)
	for _, pp := range partyPresences {
		if pp.ID.SessionID == session.id {
			continue // Already cancelled above.
		}
		memberSession := p.nk.sessionRegistry.Get(pp.ID.SessionID)
		if memberSession == nil {
			continue
		}
		ws, ok := memberSession.(*sessionWS)
		if !ok {
			continue
		}
		if err := LeaveMatchmakingStream(logger, ws); err != nil {
			logger.Warn("Failed to cancel party member's matchmaking stream",
				zap.String("member_sid", pp.ID.SessionID.String()),
				zap.Error(err))
		}
	}

	logger.Debug("Cancelled matchmaking for entire party",
		zap.String("party_id", partyID.String()),
		zap.Int("members_cancelled", len(partyPresences)))

	return nil
}

// lobbyPlayerSessionsRequest is called when a client requests the player sessions for a list of XP IDs.
// Player Sessions are random UUIDs generated when each player joins the match.
func (p *EvrPipeline) lobbyPlayerSessionsRequest(ctx context.Context, logger *zap.Logger, session *sessionWS, in evr.Message) error {
	message, ok := in.(*evr.LobbyPlayerSessionsRequest)
	if !ok {
		return fmt.Errorf("expected *evr.LobbyPlayerSessionsRequest, got %T", in)
	}

	matchID, err := NewMatchID(message.LobbyID, p.node)
	if err != nil {
		return fmt.Errorf("failed to create match ID: %w", err)
	}

	// Get all presences in the match
	presenceMap, err := GetMatchPresences(ctx, p.nk, matchID)
	if err != nil {
		return fmt.Errorf("failed to get match presences: %w", err)
	}

	// Find the requesting player's presence by their EvrID
	var presence *EvrMatchPresence
	for _, mp := range presenceMap {
		if mp.EvrID.Equals(message.EvrId) {
			presence = mp
			break
		}
	}
	if presence == nil {
		return fmt.Errorf("requesting player not found in match: %s", message.EvrId.String())
	}

	// Look up entrant IDs for the requested player EvrIDs
	entrantIDs := make([]uuid.UUID, len(message.PlayerEvrIDs))
	for i, evrID := range message.PlayerEvrIDs {
		for _, mp := range presenceMap {
			if mp.EvrID.Equals(evrID) {
				entrantIDs[i] = mp.EntrantID
				break
			}
		}
		// If not found, leave as nil UUID
	}

	entrant := evr.NewLobbyEntrant(message.EvrId, message.LobbyID, presence.EntrantID, entrantIDs, int16(presence.RoleAlignment))

	return session.SendEvr(entrant.Version3())
}
