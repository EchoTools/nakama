package server

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/gofrs/uuid/v5"
	"github.com/heroiclabs/nakama/v3/server/evr"
	"go.uber.org/zap"
)

const (
	// preJoinPingMaxAge is how recent a latency entry must be to skip re-pinging.
	preJoinPingMaxAge = 5 * time.Minute
	// preJoinPingTimeout is the maximum time to wait for a ping response.
	preJoinPingTimeout = 5 * time.Second
	// preJoinPingRTTMax is the RTT ceiling sent in the ping request.
	preJoinPingRTTMax = 250
)

// preJoinPingWaiters tracks pending pre-join ping validations.
// Key: session ID; value: channel that receives the ping response.
// The lobbyPingResponse handler closes the channel after updating latency history.
var preJoinPingWaiters = MapOf[uuid.UUID, chan struct{}]{}

// registerPreJoinPingWaiter registers a channel that will be signaled when the
// given session sends a LobbyPingResponse.
func registerPreJoinPingWaiter(sessionID uuid.UUID) chan struct{} {
	ch := make(chan struct{}, 1)
	preJoinPingWaiters.Store(sessionID, ch)
	return ch
}

// notifyPreJoinPingWaiter signals the waiter for the given session, if any.
// Called from lobbyPingResponse after updating the latency history.
func notifyPreJoinPingWaiter(sessionID uuid.UUID) {
	if ch, ok := preJoinPingWaiters.LoadAndDelete(sessionID); ok {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
}

// cleanupPreJoinPingWaiter removes a waiter registration without signaling.
func cleanupPreJoinPingWaiter(sessionID uuid.UUID) {
	preJoinPingWaiters.Delete(sessionID)
}

// preJoinPingResult captures the outcome of a single party member's ping validation.
type preJoinPingResult struct {
	UserID    uuid.UUID
	SessionID uuid.UUID
	Username  string
	RTT       int  // milliseconds; 0 means no data / timeout
	Cached    bool // true if the entry was already cached (no ping needed)
	Err       error
}

// validatePreJoinPing checks that all entrants have recent good latency data for
// the target game server endpoint. If any member lacks a recent entry, a ping
// request is sent and the function waits for the response.
//
// Returns nil if all entrants pass, or an error describing which entrants failed.
// On failure the caller is responsible for erroring the entire party.
func (p *EvrPipeline) validatePreJoinPing(
	ctx context.Context,
	logger *zap.Logger,
	label *MatchLabel,
	entrants []*EvrMatchPresence,
) error {
	settings := ServiceSettings()
	if settings == nil || !settings.Matchmaking.RequiresPreMatchPing() {
		return nil
	}

	endpoint := label.GameServer.Endpoint
	if !endpoint.IsValid() {
		logger.Warn("Game server endpoint invalid, skipping pre-join ping")
		return nil
	}

	extIP := endpoint.GetExternalIP()
	maxRTT := settings.Matchmaking.MaxServerRTT
	if maxRTT <= 0 {
		maxRTT = 180
	}

	cutoff := time.Now().Add(-preJoinPingMaxAge)

	type memberCheck struct {
		presence *EvrMatchPresence
		session  Session
		history  *LatencyHistory
	}

	// Gather sessions and latency histories for all entrants.
	checks := make([]memberCheck, 0, len(entrants))
	for _, ent := range entrants {
		sess := p.nk.sessionRegistry.Get(ent.SessionID)
		if sess == nil {
			// Session gone; skip (will fail at join attempt anyway).
			continue
		}

		params, ok := LoadParams(sess.Context())
		if !ok {
			continue
		}

		lh := params.latencyHistory.Load()
		if lh == nil {
			lh = NewLatencyHistory()
		}

		checks = append(checks, memberCheck{
			presence: ent,
			session:  sess,
			history:  lh,
		})
	}

	if len(checks) == 0 {
		return nil
	}

	// Phase 1: identify which members need a ping.
	type needsPing struct {
		memberCheck
		waiter chan struct{}
	}

	var pending []needsPing

	for _, mc := range checks {
		entry, found := mc.history.LatestEntry(extIP)
		if found && entry.Timestamp.After(cutoff) && int(entry.RTT.Milliseconds()) <= maxRTT {
			// Recent good entry exists; skip.
			continue
		}
		pending = append(pending, needsPing{memberCheck: mc})
	}

	if len(pending) == 0 {
		// All members have recent good latency data.
		return nil
	}

	// Phase 2: send ping requests and register waiters.
	for i := range pending {
		np := &pending[i]
		np.waiter = registerPreJoinPingWaiter(np.presence.SessionID)
		if err := SendEVRMessages(np.session, true, evr.NewLobbyPingRequest(preJoinPingRTTMax, []evr.Endpoint{endpoint})); err != nil {
			cleanupPreJoinPingWaiter(np.presence.SessionID)
			np.waiter = nil
			logger.Warn("Failed to send pre-join ping request",
				zap.String("uid", np.presence.UserID.String()),
				zap.String("sid", np.presence.SessionID.String()),
				zap.Error(err))
		}
	}

	// Phase 3: wait for responses concurrently.
	results := make([]preJoinPingResult, len(pending))
	var wg sync.WaitGroup

	for i := range pending {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			np := &pending[idx]
			result := &results[idx]
			result.UserID = np.presence.UserID
			result.SessionID = np.presence.SessionID
			result.Username = np.presence.Username

			if np.waiter == nil {
				result.Err = fmt.Errorf("failed to send ping request")
				return
			}

			defer cleanupPreJoinPingWaiter(np.presence.SessionID)

			timer := time.NewTimer(preJoinPingTimeout)
			defer timer.Stop()

			select {
			case <-np.waiter:
				// Ping response received; check the updated latency history.
				entry, found := np.history.LatestEntry(extIP)
				if !found {
					result.Err = fmt.Errorf("no latency data after ping response")
					return
				}
				result.RTT = int(entry.RTT.Milliseconds())
				if result.RTT > maxRTT {
					result.Err = fmt.Errorf("RTT %dms exceeds max %dms", result.RTT, maxRTT)
				}
			case <-timer.C:
				result.Err = fmt.Errorf("ping response timed out after %s", preJoinPingTimeout)
			case <-ctx.Done():
				result.Err = fmt.Errorf("context canceled: %w", ctx.Err())
			}
		}(i)
	}

	wg.Wait()

	// Phase 4: check results — if ANY member failed, error the entire party.
	var failures []preJoinPingResult
	for _, r := range results {
		if r.Err != nil {
			failures = append(failures, r)
		}
	}

	if len(failures) == 0 {
		return nil
	}

	// Build detailed error info for logging.
	groupID := ""
	if label.GroupID != nil {
		groupID = label.GroupID.String()
	}

	failureDetails := make([]string, 0, len(failures))
	for _, f := range failures {
		failureDetails = append(failureDetails, fmt.Sprintf("%s (uid=%s, sid=%s): %v", f.Username, f.UserID, f.SessionID, f.Err))
	}

	allEntrantDetails := make([]string, 0, len(entrants))
	for _, ent := range entrants {
		allEntrantDetails = append(allEntrantDetails, fmt.Sprintf("uid=%s sid=%s user=%s", ent.UserID, ent.SessionID, ent.Username))
	}

	logger.Warn("Pre-join ping validation failed",
		zap.String("mid", label.ID.UUID.String()),
		zap.String("group_id", groupID),
		zap.String("endpoint", endpoint.String()),
		zap.Int("party_size", len(entrants)),
		zap.Int("failures", len(failures)),
		zap.Strings("failure_details", failureDetails),
	)

	// Audit log at guild level.
	auditMsg := fmt.Sprintf("Pre-join ping validation failed for match `%s` (endpoint: `%s`):\n**Failed members:** %s\n**All party members:** %s\n**Game server:** %s",
		label.ID.UUID.String(),
		endpoint.ExternalAddress(),
		strings.Join(failureDetails, "; "),
		strings.Join(allEntrantDetails, "; "),
		label.GameServer.Username,
	)
	if _, err := p.appBot.LogAuditMessage(ctx, groupID, auditMsg, true); err != nil {
		logger.Warn("Failed to send pre-join ping audit message", zap.Error(err))
	}

	// User error log with full details.
	errorMsg := fmt.Sprintf("```fix\nPre-join ping validation failed\n\nMatch: %s\nEndpoint: %s\nGame Server: %s\n\nFailed members:\n  %s\n\nAll party members:\n  %s\n```",
		label.ID.UUID.String(),
		endpoint.ExternalAddress(),
		label.GameServer.Username,
		strings.Join(failureDetails, "\n  "),
		strings.Join(allEntrantDetails, "\n  "),
	)
	if _, err := p.appBot.LogUserErrorMessage(ctx, groupID, errorMsg, false); err != nil {
		logger.Warn("Failed to send pre-join ping error message", zap.Error(err))
	}

	return NewLobbyErrorf(InternalError, "pre-join ping validation failed: %d of %d party members could not reach server %s",
		len(failures), len(entrants), endpoint.ExternalAddress())
}
