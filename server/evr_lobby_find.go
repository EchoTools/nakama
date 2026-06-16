package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"sync"
	"time"

	"github.com/gofrs/uuid/v5"
	"github.com/heroiclabs/nakama-common/runtime"
	"github.com/heroiclabs/nakama/v3/server/evr"
	"go.uber.org/zap"
)

type TeamAlignments map[string]int // map[UserID]Role

var createLobbyMu = &sync.Mutex{}

var LobbyTestCounter = 0

var ErrCreateLock = errors.New("failed to acquire create lock")

func shouldFollowerFindOrCreateSocial(mode evr.Symbol) bool {
	return mode == evr.ModeSocialPublic || mode == evr.ModeSocialNPE
}

// lobbyJoinSessionRequest is a request to join a specific existing session.
func (p *EvrPipeline) lobbyFind(ctx context.Context, logger *zap.Logger, session *sessionWS, lobbyParams *LobbySessionParameters) error {

	startTime := time.Now()
	entrantSessionIDs := []uuid.UUID{session.id}

	var lobbyGroup *LobbyGroup
	var memberSessionIDs []uuid.UUID
	var isLeader bool
	var headingToSocial bool // cached from first isLeaderHeadingToSocial call; reused to avoid TOCTOU double-read

	// Resolve party state early if applicable
	if lobbyParams.PartyGroupName != "" && lobbyParams.PartyGroupName != "tablet" {
		var err error
		lobbyGroup, memberSessionIDs, isLeader, err = p.configureParty(ctx, logger, session, lobbyParams)
		if err != nil {
			return fmt.Errorf("failed to join party: %w", err)
		}

		if isLeader {
			// SMELL(concurrency/data_races, CRITICAL, high): deferred Untrack still races followers
			// reading matchmaking presence in pollFollowPartyLeader. H3 fix (cached headingToSocial)
			// eliminates the double-call TOCTOU at lines 61/111, but followers in the poll loop
			// can still observe the presence between the leader's match-join and this deferred removal.
			// TODO: requires integration testing before fix — full fix needs architectural change
			// (e.g. update matchmaking presence to include match ID before untracking, so followers
			// can observe the match ID and join directly).
			defer func() {
				mmStream := PresenceStream{
					Mode:    StreamModeMatchmaking,
					Subject: lobbyParams.GroupID,
				}
				p.nk.tracker.Untrack(session.id, mmStream, session.userID)
			}()
		}

		// Fast path: if the follower is already in the leader's match,
		// skip all heavyweight operations (authorization, matchmaking
		// stream, monitor goroutine, TryFollowPartyLeader). This
		// prevents repeated "Joined party group" / "Already in
		// leader's match" churn when the client re-sends
		// LobbyFindSessionRequest on its normal message cycle.
		if !isLeader && p.isFollowerAlreadyInLeaderMatch(logger, session, lobbyGroup) {
			logger.Debug("Follower already in leader's match, skipping follow path")
			return nil
		}

		// Synchronize mode if the leader is heading to Social.
		// Cache the result to avoid a TOCTOU double-read later (line 111).
		headingToSocial = !isLeader && p.isLeaderHeadingToSocial(ctx, logger, session, lobbyParams, lobbyGroup)
		if headingToSocial {
			logger.Info("Leader is heading to a social lobby, forcing social mode for follower")
			lobbyParams.Mode = evr.ModeSocialPublic
			lobbyParams.Level = evr.LevelUnspecified
		}
	}

	// Authorize the session
	if err := p.lobbyAuthorize(ctx, logger, session, lobbyParams); err != nil {
		return err
	}

	// Restrict matchmaking to public lobbies only
	switch lobbyParams.Mode {
	case evr.ModeArenaPublic, evr.ModeSocialPublic, evr.ModeCombatPublic, evr.ModeArenaPublicAI:
	default:
		return NewLobbyError(BadRequest, fmt.Sprintf("`%s` is an invalid mode for matchmaking.", lobbyParams.Mode.String()))
	}

	// Cancel matchmaking after the timeout.
	ctx, cancel := context.WithTimeoutCause(ctx, lobbyParams.MatchmakingTimeout, ErrMatchmakingTimeout)
	defer cancel()

	// Join the "matchmaking" status stream
	if err := JoinMatchmakingStream(logger, session, lobbyParams); err != nil {
		return fmt.Errorf("failed to join matchmaking stream: %w", err)
	}

	// Monitor the matchmaking status stream, canceling the context if the stream is closed.
	go p.monitorMatchmakingStream(ctx, logger, session, lobbyParams, cancel)

	if lobbyGroup != nil {
		if !isLeader {
			// Guard: if the follower is in an active Arena/Combat match,
			// do not process the party follow. Let them finish their match.
			// The party will pick them up when they return to social.
			// Fixes #460: player mid-match yanked back to social by party follow.
			if p.isFollowerInActiveMatch(ctx, logger, session) {
				logger.Info("Follower is in an active arena/combat match, skipping party follow")
				return nil
			}

			// Observer: non-leader entering holding pattern, waiting for leader's ticket.
			if lc := getMatchLifecycle(session); lc != nil {
				lc.Transition(StateHolding, "waiting for leader's ticket")
			}

			// Late arrival detection: if the party has an active matchmaking
			// ticket and this session is NOT on it, cancel the ticket so
			// the leader rebuilds with the full party. Per the behavioral
			// spec, tickets are immutable — cancel and rebuild is the
			// correct response. This only applies to Arena/Combat modes.
			// Social lobbies use find-or-create convergence and do not
			// need ticket cancellation.
			//
			// After cancellation the late arrival falls through to the
			// poll path. The leader's lobbyMatchMakeWithFallback will
			// submit a new ticket (via replaceTicket) that includes this
			// session. When matched, the match builder places everyone.
			if !shouldFollowerFindOrCreateSocial(lobbyParams.Mode) &&
				lobbyGroup.Size() > 1 &&
				!lobbyGroup.HasSessionOnTicket(session.id.String()) {
				p.cancelTicketForLateArrival(ctx, logger, session, lobbyParams, lobbyGroup)
			}

			// Gate: only enter the follow path when the leader is in a
			// *social* lobby. For Arena/Combat the correct path is the
			// one-ticket model — all party members on one matchmaking
			// ticket. If the follower missed the ticket the answer is
			// ticket cancellation + rebuild, not chasing the leader's
			// match. In the interim, redirect the follower to a social
			// lobby so they are not silently released to solo matchmaking.
			if p.isLeaderInArenaCombatMatch(ctx, logger, session, lobbyParams, lobbyGroup) {
				logger.Info("Leader is in arena/combat match, skipping follow path — returning to social lobby")
				if lc := getMatchLifecycle(session); lc != nil {
					lc.Transition(StateSocialReady, "leader in arena/combat — waiting in social for next round")
				}
				lobbyParams.Mode = evr.ModeSocialPublic
				lobbyParams.Level = evr.LevelUnspecified
				followerEntrants, err := PrepareEntrantPresences(ctx, logger, p.nk, p.nk.sessionRegistry, lobbyParams, session.id)
				if err != nil {
					return NewLobbyError(InternalError, fmt.Sprintf("failed to prepare follower entrant: %s", err))
				}
				return p.lobbyFindOrCreateSocial(ctx, logger, session, lobbyParams, lobbyGroup, followerEntrants...)
			}

			if p.TryFollowPartyLeader(ctx, logger, session, lobbyParams, lobbyGroup) {
				return nil
			}

			// TryFollowPartyLeader returned false. Check if the session became the leader
			// during the follow attempt (e.g. original leader left the party).
			leader := lobbyGroup.GetLeader()
			if leader != nil && leader.SessionId == session.id.String() {
				// We're now the leader — populate entrant list and fall through
				// to matchmaking as the leader.
				isLeader = true
				for _, sid := range memberSessionIDs {
					if sid == session.id {
						continue
					}
					entrantSessionIDs = append(entrantSessionIDs, sid)
				}
			} else if shouldFollowerFindOrCreateSocial(lobbyParams.Mode) || headingToSocial {
				// Social mode (or leader heading to social): skip the polling
				// loop entirely. Social lobbies use find-or-create with party
				// reservations, so the follower will naturally converge to the
				// leader's lobby. Polling for the leader to settle is
				// unnecessary and can silently timeout, leaving the client
				// stuck in infinite matchmaking.
				// headingToSocial is cached from the early call above — no second read.
				if !shouldFollowerFindOrCreateSocial(lobbyParams.Mode) {
					logger.Info("Leader heading to social lobby, forcing follower to social mode")
					lobbyParams.Mode = evr.ModeSocialPublic
				} else {
					logger.Info("Follower in social mode, finding social lobby independently (party reservations will converge)")
				}

				lobbyParams.Level = evr.LevelUnspecified
				followerEntrants, err := PrepareEntrantPresences(ctx, logger, p.nk, p.nk.sessionRegistry, lobbyParams, session.id)
				if err != nil {
					return NewLobbyError(InternalError, fmt.Sprintf("failed to prepare follower entrant: %s", err))
				}
				return p.lobbyFindOrCreateSocial(ctx, logger, session, lobbyParams, lobbyGroup, followerEntrants...)
			} else {
				// Still a non-leader in a non-social mode. Poll for the leader
				// to settle into a match that can be joined. This covers followers at
				// the main menu whose leader is in a closed/full match — they
				// should wait rather than immediately erroring out.
				if ctx.Err() != nil {
					return ctx.Err()
				}
				if p.pollFollowPartyLeader(ctx, logger, session, lobbyParams, lobbyGroup) {
					return nil
				}
				// Re-check leadership one more time after polling.
				leader = lobbyGroup.GetLeader()
				if leader != nil && leader.SessionId == session.id.String() {
					isLeader = true
					for _, sid := range memberSessionIDs {
						if sid == session.id {
							continue
						}
						entrantSessionIDs = append(entrantSessionIDs, sid)
					}
				} else {
					if ctx.Err() != nil {
						return ctx.Err()
					}
					// Non-social mode: release the follower to independent matchmaking.
					logger.Info("Follower cannot join leader's match, releasing to independent matchmaking",
						zap.String("mode", lobbyParams.Mode.String()))

					// Observer: released from follow path, regrouping.
					if lc := getMatchLifecycle(session); lc != nil {
						lc.Transition(StateSocialReady, "released from follow path, regrouping")
					}

					// For social modes, don't set party size to 1 — party members should converge
					// to the leader's lobby even if they are searching independently.
					if !shouldFollowerFindOrCreateSocial(lobbyParams.Mode) {
						lobbyParams.SetPartySize(1)
						// Released followers must queue as solo. Keeping lobbyGroup attached
						// causes addTicket to enforce leader-only party submission.
						lobbyGroup = nil
					}
					// Fall through to normal matchmaking below.
				}
			}
		} else {

			for _, memberSessionIDs := range memberSessionIDs {

				if memberSessionIDs == session.id {
					continue
				}

				entrantSessionIDs = append(entrantSessionIDs, memberSessionIDs)
			}
		}
	} else {
		lobbyParams.SetPartySize(1)
	}

	p.nk.metrics.CustomCounter("lobby_find_match", lobbyParams.MetricsTags(), int64(lobbyParams.GetPartySize()))
	logger.Info("Finding match", zap.String("mode", lobbyParams.Mode.String()), zap.Int("party_size", lobbyParams.GetPartySize()))

	// Construct the entrant presences for the party members.
	entrants, err := PrepareEntrantPresences(ctx, logger, p.nk, p.nk.sessionRegistry, lobbyParams, entrantSessionIDs...)
	if err != nil {
		return fmt.Errorf("failed to be party leader.: %w", err)
	}

	// For social lobbies with a party, create placeholder reservation presences
	// for online party members whose sessions were not found by PrepareEntrantPresences.
	// This ensures that when the leader joins a social lobby, slots are reserved
	// for followers who haven't started their own lobby find yet.
	entrants = appendPartyReservationPlaceholders(logger, entrants, lobbyGroup, lobbyParams, session.pipeline.node)

	lobbyParams.SetPartySize(len(entrants))

	defer func() {

		isLeader := true

		if lobbyGroup != nil {
			leader := lobbyGroup.GetLeader()
			if leader != nil && leader.SessionId != session.id.String() {
				isLeader = false
			}
		}
		// If this is the leader, or a solo player, send the metrics

		tags := lobbyParams.MetricsTags()
		tags["is_leader"] = strconv.FormatBool(isLeader)
		tags["party_size"] = strconv.Itoa(lobbyParams.GetPartySize())
		p.nk.metrics.CustomTimer("lobby_find_duration", tags, time.Since(startTime))

		logger.Debug("Lobby find complete", zap.String("group_id", lobbyParams.GroupID.String()), zap.Int("party_size", lobbyParams.GetPartySize()), zap.String("mode", lobbyParams.Mode.String()), zap.Int("role", lobbyParams.Role), zap.Bool("leader", isLeader), zap.Int("duration", int(time.Since(startTime).Seconds())))
	}()

	// Check latency to active game servers.
	if err := p.CheckServerPing(ctx, logger, session, lobbyParams.GroupID.String()); err != nil {
		return fmt.Errorf("failed to check server ping: %w", err)
	}

	if !lobbyParams.CurrentMatchID.IsNil() {
		// Sometimes the client doesn't respond to the ping request, so delay for a few seconds.
		<-time.After(3 * time.Second)
	}
	serviceSettings := ServiceSettings()
	if lobbyParams.Mode == evr.ModeArenaPublic && lobbyParams.EarlyQuitPenaltyLevel > 0 && serviceSettings.Matchmaking.EnableEarlyQuitPenalty {
		eqConfig := NewEarlyQuitPlayerState()
		if err := StorableRead(ctx, p.nk, lobbyParams.UserID.String(), eqConfig, true); err != nil {
			logger.Debug("Failed to load early quit config for logging", zap.Error(err))
		} else {
			penaltyTime := time.Unix(eqConfig.PenaltyTimestamp, 0)
			timeSinceLastQuit := time.Since(penaltyTime)
			lockoutDuration := GetLockoutDuration(lobbyParams.EarlyQuitPenaltyLevel)

			if timeSinceLastQuit < lockoutDuration {
				remainingTime := lockoutDuration - timeSinceLastQuit
				logger.Info("Player queueing with active early quit penalty (client-side enforcement expected)",
					zap.String("user_id", lobbyParams.UserID.String()),
					zap.Int("penalty_level", lobbyParams.EarlyQuitPenaltyLevel),
					zap.Duration("remaining", remainingTime))
			}
		}
	}

	// Novelty: vibinator's gravity — may redirect social-lobby echo_arena matchmakers.
	if action, label, err := vibinatorsGravityCheck(ctx, logger, p, session, lobbyParams, entrants); err != nil {
		logger.Warn("vibinatorsGravity: check failed, continuing normally", zap.Error(err))
	} else {
		switch action {
		case vibinatorsGravityJoinMatch:
			if err := p.LobbyJoinEntrants(logger, label, entrants...); err != nil {
				logger.Warn("vibinatorsGravity: join failed, continuing normally", zap.Error(err))
			} else {
				return nil
			}
		case vibinatorsGravityRedirectMode:
			lobbyParams.Mode = evr.ModeCombatPublic
			lobbyParams.Level = evr.LevelUnspecified
		}
	}

	// Social lobbies use a simple find-or-create approach
	if lobbyParams.Mode == evr.ModeSocialPublic {
		// If the leader is already in a social lobby that can't fit the
		// entire party (even accounting for reservations), abandon it and
		// find/create a new lobby with room for everyone.
		if !lobbyParams.CurrentMatchID.IsNil() && lobbyGroup != nil && lobbyGroup.Size() > 1 {
			currentLabel, err := MatchLabelByID(ctx, p.nk, lobbyParams.CurrentMatchID)
			if err == nil && currentLabel != nil && currentLabel.IsSocial() {
				openSlots := currentLabel.OpenPlayerSlots()
				// Count party members already in this match.
				membersInMatch := 0
				for _, member := range lobbyGroup.List() {
					if currentLabel.GetPlayerByUserID(member.Presence.GetUserId()) != nil {
						membersInMatch++
					}
				}
				needed := lobbyGroup.Size() - membersInMatch
				if openSlots < needed {
					logger.Info("Current social lobby cannot fit party, relocating",
						zap.String("current_mid", lobbyParams.CurrentMatchID.String()),
						zap.Int("open_slots", openSlots),
						zap.Int("party_size", lobbyGroup.Size()),
						zap.Int("members_in_match", membersInMatch),
						zap.Int("needed", needed))
					lobbyParams.CurrentMatchID = MatchID{}
				}
			}
		}
		return p.lobbyFindOrCreateSocial(ctx, logger, session, lobbyParams, lobbyGroup, entrants...)
	}

	// Arena and Combat lobbies use the matchmaker (backfill is handled by the matchmaker process)
	return p.lobbyMatchMakeWithFallback(ctx, logger, session, lobbyParams, lobbyGroup, entrants...)
}

func (p *EvrPipeline) configureParty(ctx context.Context, logger *zap.Logger, session *sessionWS, lobbyParams *LobbySessionParameters) (*LobbyGroup, []uuid.UUID, bool, error) {

	// Join the party if a player has a party group id set.
	// The lobby group is the party that the user is currently in.
	lobbyGroup, isLeader, err := JoinPartyGroup(session, lobbyParams.PartyGroupName, lobbyParams.CurrentMatchID)
	if err != nil {
		if err == runtime.ErrPartyFull {
			return nil, nil, false, NewLobbyError(ServerIsFull, "party is full")
		}
		return nil, nil, false, fmt.Errorf("failed to join party group: %w", err)
	}
	// Populate PartyID from the registry-assigned party (random UUID, not derived from group name).
	lobbyParams.PartyID = lobbyGroup.ID()
	logger.Debug("Joined party group", zap.String("partyID", lobbyGroup.IDStr()), zap.String("partyGroupName", lobbyParams.PartyGroupName))

	// If this is the leader, then set the presence status to the current match ID.
	if isLeader {
		// Track the leader on the matchmaking stream early so followers know they are queueing for Arena.
		mmStream := PresenceStream{
			Mode:    StreamModeMatchmaking,
			Subject: lobbyParams.GroupID,
		}
		statusBytes, err := json.Marshal(lobbyParams)
		if err != nil {
			return nil, nil, false, fmt.Errorf("configureParty: marshal lobby params: %w", err)
		}
		presenceMeta := PresenceMeta{
			Format:   session.Format(),
			Username: session.Username(),
			Status:   string(statusBytes),
		}
		success, isNew := p.nk.tracker.Track(ctx, session.id, mmStream, session.userID, presenceMeta)
		if !success {
			logger.Warn("Failed to track leader on matchmaking stream early")
		} else if !isNew {
			logger.Debug("Leader re-tracked on matchmaking stream (was already tracked)",
				zap.String("sid", session.id.String()))
		}

		if !lobbyParams.CurrentMatchID.IsNil() && lobbyParams.Mode != evr.ModeSocialPublic {
			// Query the match we're leaving to find how many party members should be joining us.
			// Use the leader's party ID from the match presence (set at join time), not
			// lobbyParams.PartyID. The user may have changed their LobbyGroupName since
			// joining the match (via /party group), which changes lobbyParams.PartyID but
			// doesn't update the match presence. Comparing against the match presence
			// party ID ensures we count everyone who was in the same party when they
			// entered the match.
			expectedCount := 0
			if presences, err := GetMatchPresences(ctx, p.nk, lobbyParams.CurrentMatchID); err == nil {
				matchPartyID := lobbyParams.PartyID
				if leaderPresence, ok := presences[session.userID.String()]; ok && !leaderPresence.PartyID.IsNil() {
					matchPartyID = leaderPresence.PartyID
				}
				for _, mp := range presences {
					if mp.PartyID == matchPartyID && mp.UserID != session.userID {
						expectedCount++
					}
				}
			}
			if expectedCount > 0 {
				logger.Debug("Waiting for party members to start matchmaking", zap.Int("expected", expectedCount), zap.Int("current", lobbyGroup.Size()-1))
				deadline := time.After(30 * time.Second)
				ticker := time.NewTicker(500 * time.Millisecond)
				defer ticker.Stop()
			waitLoop:
				for lobbyGroup.Size()-1 < expectedCount {
					select {
					case <-ctx.Done():
						return nil, nil, false, ctx.Err()
					case <-deadline:
						logger.Warn("Timed out waiting for party members", zap.Int("expected", expectedCount), zap.Int("current", lobbyGroup.Size()-1))
						break waitLoop
					case <-ticker.C:
					}
				}
			}
		} else if lobbyParams.CurrentMatchID.IsNil() && lobbyGroup.Size() <= 1 && lobbyParams.Mode != evr.ModeSocialPublic {
			// Fresh-start matchmaking: the party handler may have just been created and
			// followers may not have called JoinPartyGroup yet. Wait briefly so the leader
			// does not submit a solo matchmaking ticket before the follower joins — which
			// would allow backfill to place the leader in a match with no room left for
			// the follower.
			logger.Debug("Waiting for party followers (fresh-start grace period)", zap.Duration("timeout", MatchmakingStartGracePeriod))
			graceTimer := time.NewTimer(MatchmakingStartGracePeriod)
			defer graceTimer.Stop()
			graceTicker := time.NewTicker(200 * time.Millisecond)
			defer graceTicker.Stop()
		graceWaitLoop:
			for lobbyGroup.Size() <= 1 {
				select {
				case <-ctx.Done():
					return nil, nil, false, ctx.Err()
				case <-graceTimer.C:
					logger.Debug("Grace period elapsed; no followers joined, proceeding solo")
					break graceWaitLoop
				case <-graceTicker.C:
				}
			}
			if lobbyGroup.Size() > 1 {
				logger.Debug("Party followers joined during grace period", zap.Int("size", lobbyGroup.Size()))
			}
		}
		memberUsernames := make([]string, 0, lobbyGroup.Size())

		for _, member := range lobbyGroup.List() {
			if member.Presence.GetSessionId() == session.id.String() {
				continue
			}
			memberUsernames = append(memberUsernames, member.Presence.GetUsername())
		}

		partySize := lobbyGroup.Size()
		logger.Debug("Party is ready", zap.String("leader", session.id.String()), zap.Int("size", partySize), zap.Strings("members", memberUsernames))

		// Observer: leader submitting matchmaking ticket.
		if lc := getMatchLifecycle(session); lc != nil {
			lc.TransitionTo(StateMatchmaking, "leader submitted ticket", WithIsLeader(true))
		}

		lobbyParams.SetPartySize(partySize)
	}

	memberSessionIDs := []uuid.UUID{session.id}
	// Add the party members to the sessionID slice
	for _, member := range lobbyGroup.List() {
		if member.Presence.GetSessionId() == session.id.String() {
			continue
		}
		// Observer: party member included on leader's ticket.
		if memberSession := p.nk.sessionRegistry.Get(uuid.FromStringOrNil(member.Presence.GetSessionId())); memberSession != nil {
			if ws, ok := memberSession.(*sessionWS); ok {
				if lc := getMatchLifecycle(ws); lc != nil {
					lc.Transition(StateMatchmaking, "included on leader's ticket")
				}
			}
		}
		memberSessionIDs = append(memberSessionIDs, uuid.FromStringOrNil(member.Presence.GetSessionId()))
	}

	return lobbyGroup, memberSessionIDs, isLeader, nil
}

func (p *EvrPipeline) monitorMatchmakingStream(ctx context.Context, logger *zap.Logger, session *sessionWS, lobbyParams *LobbySessionParameters, cancelFn context.CancelFunc) {

	// Monitor the stream and cancel the context (and matchmaking) if the stream is closed.
	// This stream tracks the user's matchmaking status.
	// This stream is untracked when the user cancels matchmaking.
	//
	// IMPORTANT: This function does NOT call LeaveMatchmakingStream on exit.
	// The matchmaking stream cleanup is handled by:
	// - LobbyJoinEntrants (when player joins a match)
	// - lobbyPendingSessionCancel (when player explicitly cancels)
	// - JoinMatchmakingStream (when player re-queues, it cleans up old streams)

	stream := lobbyParams.MatchmakingStream()
	const checkInterval = 1 * time.Second
	const gracePeriod = 1 * time.Second

	for {
		select {
		case <-ctx.Done():
			// Context was canceled (timeout, player joined match, or external cancel)
			// Do NOT clean up the matchmaking stream here - let the appropriate handler do it
			return
		case <-time.After(checkInterval):
		}

		// Check if the matchmaking stream has been closed (i.e., the user has canceled matchmaking)
		if session.tracker.GetLocalBySessionIDStreamUserID(session.id, stream, session.userID) == nil {
			// Wait grace period before canceling to handle race condition where player re-queues
			select {
			case <-ctx.Done():
				return
			case <-time.After(gracePeriod):
			}

			// Re-check after grace period - the presence might have been re-added if player re-queued
			if session.tracker.GetLocalBySessionIDStreamUserID(session.id, stream, session.userID) == nil {
				logger.Debug("Matchmaking stream closed, canceling matchmaking")
				cancelFn()
				return
			}
			// Player re-queued during grace period, continue monitoring
			logger.Debug("Player re-queued during grace period, continuing to monitor")
		}
	}
}

func (p *EvrPipeline) newLobby(ctx context.Context, logger *zap.Logger, lobbyParams *LobbySessionParameters, entrants ...*EvrMatchPresence) (*MatchLabel, error) {
	if !createLobbyMu.TryLock() {
		return nil, ErrFailedToAcquireLock
	}
	defer createLobbyMu.Unlock()

	metricsTags := map[string]string{
		"version_lock": lobbyParams.VersionLock.String(),
		"group_id":     lobbyParams.GroupID.String(),
		"mode":         lobbyParams.Mode.String(),
	}

	p.nk.metrics.CustomCounter("lobby_new", metricsTags, 1)

	settings := &MatchSettings{
		Mode:                lobbyParams.Mode,
		Level:               lobbyParams.Level,
		SpawnedBy:           lobbyParams.UserID.String(),
		GroupID:             lobbyParams.GroupID,
		StartTime:           time.Now().UTC(),
		Reservations:        entrants,
		ReservationLifetime: 30 * time.Second,
	}

	var latestRTTs map[string]int
	if lobbyParams.latencyHistory != nil {
		if lh := lobbyParams.latencyHistory.Load(); lh != nil {
			latestRTTs = lh.LatestRTTs()
		}
	}

	userBL := loadUserBlacklist(ctx, p.nk, lobbyParams.UserID.String())

	label, err := LobbyGameServerAllocate(ctx, NewRuntimeGoLogger(logger), p.nk, []string{lobbyParams.GroupID.String()}, latestRTTs, settings, []string{lobbyParams.RegionCode}, true, false, ServiceSettings().Matchmaking.QueryAddons.Create, userBL.IPs())
	if err != nil {
		// Check if this is a region fallback error - for pipeline, auto-select closest
		var regionErr ErrMatchmakingNoServersInRegion
		if errors.As(err, &regionErr) && regionErr.FallbackInfo != nil {
			logger.Info("Auto-selecting closest server for lobby creation (no servers in requested region)",
				zap.String("requested_region", lobbyParams.RegionCode),
				zap.String("selected_region", regionErr.FallbackInfo.ClosestRegion),
				zap.Int("latency_ms", regionErr.FallbackInfo.ClosestLatencyMs))

			// Allocate without region requirement to get the closest server
			label, err = LobbyGameServerAllocate(ctx, NewRuntimeGoLogger(logger), p.nk, []string{lobbyParams.GroupID.String()}, latestRTTs, settings, nil, true, false, ServiceSettings().Matchmaking.QueryAddons.Create, userBL.IPs())
		}

		if err != nil {
			logger.Warn("Failed to allocate game server", zap.Error(err), zap.Any("settings", settings))
			return nil, err
		}
	}

	// SignalPrepareSession (invoked inside LobbyGameServerAllocate) mutates the
	// match label but only enqueues it in the registry's pendingUpdates map.
	// The Bluge index is not refreshed until the next LabelUpdateIntervalMs
	// tick — up to 1s by default. Without an eager flush, a concurrent
	// lobbyFindOrCreateSocial caller racing on the same second would see zero
	// results and spin up its own duplicate lobby. Force an immediate flush
	// so the new lobby is searchable before we return.
	flushMatchRegistryLabelUpdates(p.nk)

	return label, nil
}

// flushMatchRegistryLabelUpdates forces the match registry's pending label
// updates to be written to the Bluge index synchronously, bypassing the
// LabelUpdateIntervalMs ticker. No-op if the registry is not a
// *LocalMatchRegistry.
func flushMatchRegistryLabelUpdates(nk runtime.NakamaModule) {
	rgo, ok := nk.(*RuntimeGoNakamaModule)
	if !ok || rgo == nil {
		return
	}
	lmr, ok := rgo.matchRegistry.(*LocalMatchRegistry)
	if !ok || lmr == nil {
		return
	}
	lmr.FlushPendingLabelUpdates()
}

// filterBlacklistedSocialMatches drops any candidate social match hosted on a
// server whose external IP is in blacklistedIPs. An empty blacklist is a no-op
// and returns the input slice unchanged. Filtering happens AFTER the GroupID-scoped
// match query — it only ever narrows results, never widens them across guilds.
func filterBlacklistedSocialMatches(matches []*MatchLabelMeta, blacklistedIPs map[string]struct{}) []*MatchLabelMeta {
	if len(blacklistedIPs) == 0 {
		return matches
	}
	filtered := matches[:0]
	for _, m := range matches {
		if m.State.GameServer != nil {
			if _, blocked := blacklistedIPs[m.State.GameServer.Endpoint.GetExternalIP()]; blocked {
				continue
			}
		}
		filtered = append(filtered, m)
	}
	return filtered
}

func (p *EvrPipeline) lobbyFindOrCreateSocial(ctx context.Context, logger *zap.Logger, session Session, lobbyParams *LobbySessionParameters, lobbyGroup *LobbyGroup, entrants ...*EvrMatchPresence) error {
	// Fast path: if the player is already in the social lobby we intend to
	// send them to, treat as no-op. The guard is target-aware — it only
	// short-circuits when the player's current social lobby equals the
	// intended target (the party leader's lobby in a follow, or the
	// player's own CurrentMatchID otherwise). A same-guild move to a
	// *different* social lobby, and a forced relocation (cleared
	// CurrentMatchID), are NOT no-ops. (#462)
	if currentMatchID := p.currentSocialLobbyForSession(ctx, logger, session, lobbyParams, lobbyGroup); !currentMatchID.IsNil() {
		logger.Debug("Player already in the intended social lobby, treating as no-op",
			zap.String("mid", currentMatchID.String()))
		return nil
	}

	// Load the user's server blacklist once before the retry loop
	blacklistedIPs := loadUserBlacklist(ctx, p.nk, session.UserID().String()).IPSet()

	// First attempt runs immediately — no pre-wait. The old 1s pre-query wait
	// was a workaround for the Bluge label-flush lag; newLobby now flushes
	// synchronously, so the first query sees fresh state. Subsequent attempts
	// back off exponentially only on specific retryable errors (server full,
	// failed-to-acquire-lock) — a successful find/create returns immediately.
	interval := 1 * time.Second
	const maxInterval = 8 * time.Second
	const maxAttempts = 30

	for attempt := 0; attempt < maxAttempts; attempt++ {
		// Skip the pre-wait on the first attempt so concurrent joiners converge
		// on the first existing lobby instead of racing to create their own.
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return fmt.Errorf("context canceled: %w", ctx.Err())
			case <-time.After(interval):
			}
		} else {
			select {
			case <-ctx.Done():
				return fmt.Errorf("context canceled: %w", ctx.Err())
			default:
			}
		}

		// List all social matches that are open and have available slots.
		// minSize=0: freshly-allocated social lobbies have zero tracked
		// presences until the first player joins; excluding them caused every
		// concurrent finder to spawn its own solo lobby instead of converging.
		query := lobbyParams.BackfillSearchQuery(false, false)
		matches, err := ListMatchStates(ctx, p.nk, query, 0)
		if err != nil {
			return fmt.Errorf("failed to list matches: %w", err)
		}

		// Filter out any matches hosted on servers the user has blacklisted.
		matches = filterBlacklistedSocialMatches(matches, blacklistedIPs)

		logger.Info("Social lobby search",
			zap.Int("attempt", attempt),
			zap.Int("candidates", len(matches)),
			zap.String("query", query),
		)

		partySize := lobbyParams.GetPartySize()
		if partySize == 0 {
			logger.Warn("party size is 0")
			lobbyParams.SetPartySize(1)
			partySize = 1
		}

		// Set the team for social lobbies
		team := evr.TeamSocial
		for _, e := range entrants {
			e.RoleAlignment = team
		}

		// Pre-warm latency data for all candidate server endpoints in parallel so
		// that validatePreJoinPing can read from cache rather than issuing a
		// separate ping round-trip per lobby.
		{
			endpoints := make([]evr.Endpoint, 0, len(matches))
			seen := make(map[string]struct{})
			for _, m := range matches {
				if m.State.GameServer != nil && m.State.GameServer.Endpoint.IsValid() {
					ip := m.State.GameServer.Endpoint.GetExternalIP()
					if _, ok := seen[ip]; !ok {
						seen[ip] = struct{}{}
						endpoints = append(endpoints, m.State.GameServer.Endpoint)
					}
				}
			}
			p.prewarmEntrantPings(ctx, logger, entrants, endpoints)
		}

		// Priority 1: If we're in a party, try to find the leader's specific lobby first.
		if lobbyParams.PartyGroupName != "" && lobbyParams.PartyGroupName != "tablet" {
			// We can use JoinPartyGroup here as it just retrieves/joins the group without side effects if already joined
			ws, ok := session.(*sessionWS)
			if ok {
				lobbyGroup, _, err := JoinPartyGroup(ws, lobbyParams.PartyGroupName, lobbyParams.CurrentMatchID)
				if err == nil && lobbyGroup != nil {
					leader := lobbyGroup.GetLeader()
					if leader != nil && leader.SessionId != session.ID().String() {
						leaderSessionID := uuid.FromStringOrNil(leader.SessionId)
						leaderUserID := uuid.FromStringOrNil(leader.UserId)

						// Look up the leader's current match via tracker.
						stream := PresenceStream{
							Mode:    StreamModeService,
							Subject: leaderSessionID,
							Label:   StreamLabelMatchService,
						}
						presence := p.nk.tracker.GetLocalBySessionIDStreamUserID(leaderSessionID, stream, leaderUserID)
						if presence != nil {
							leaderMatchID := MatchIDFromStringOrNil(presence.GetStatus())
							if !leaderMatchID.IsNil() {
								// Find this specific match in our search results
								var leaderMatch *MatchLabelMeta
								for _, m := range matches {
									// MatchLabelMeta doesn't have ID, but can compare its State.ID if available
									if m.State.ID.UUID == leaderMatchID.UUID {
										leaderMatch = m
										break
									}
								}

								// If not in search results (maybe query too restrictive?), fetch label directly
								if leaderMatch == nil {
									if label, err := MatchLabelByID(ctx, p.nk, leaderMatchID); err != nil {
										logger.Debug("Failed to fetch leader's match label for priority join",
											zap.Error(err), zap.String("mid", leaderMatchID.String()))
									} else if label != nil {
										leaderMatch = &MatchLabelMeta{State: label}
									}
								}

								if leaderMatch != nil && leaderMatch.State.IsSocial() {
									logger.Info("Priority join: Found party leader's social lobby", zap.String("mid", leaderMatchID.String()))
									if err := p.LobbyJoinEntrants(logger, leaderMatch.State, entrants...); err == nil {
										return nil
									} else {
										logger.Warn("Failed priority join to leader's lobby", zap.Error(err))
									}
								}
							}
						}
					} else {
						// Leader logic: Check if any followers are in a social lobby.
						for _, member := range lobbyGroup.List() {
							if member.Presence.GetSessionId() == session.ID().String() {
								continue // Skip self
							}
							memberSessionID := uuid.FromStringOrNil(member.Presence.GetSessionId())
							memberUserID := uuid.FromStringOrNil(member.Presence.GetUserId())

							// Look up the member's current match via tracker.
							stream := PresenceStream{
								Mode:    StreamModeService,
								Subject: memberSessionID,
								Label:   StreamLabelMatchService,
							}
							presence := p.nk.tracker.GetLocalBySessionIDStreamUserID(memberSessionID, stream, memberUserID)
							if presence != nil {
								memberMatchID := MatchIDFromStringOrNil(presence.GetStatus())
								if !memberMatchID.IsNil() {
									// Find this specific match in our search results
									var followerMatch *MatchLabelMeta
									for _, m := range matches {
										if m.State.ID.UUID == memberMatchID.UUID {
											followerMatch = m
											break
										}
									}

									// If not in search results, fetch label directly
									if followerMatch == nil {
										if label, err := MatchLabelByID(ctx, p.nk, memberMatchID); err != nil {
											logger.Debug("Failed to fetch member's match label for priority join",
												zap.Error(err), zap.String("mid", memberMatchID.String()))
										} else if label != nil {
											followerMatch = &MatchLabelMeta{State: label}
										}
									}

									if followerMatch != nil && followerMatch.State.IsSocial() {
										logger.Info("Priority join: Leader joining follower's social lobby", zap.String("mid", memberMatchID.String()))
										if err := p.LobbyJoinEntrants(logger, followerMatch.State, entrants...); err == nil {
											return nil
										} else {
											logger.Warn("Failed priority join to follower's lobby", zap.Error(err))
										}
									}
								}
							}
						}
					}
				}
			}
		}

		// Priority 2: Try to join any existing social lobby from search results
		for _, labelMeta := range matches {
			select {
			case <-ctx.Done():
				return ctx.Err()
			default:
			}

			l := labelMeta.State

			if n, err := l.OpenSlotsByRole(team); err != nil {
				logger.Warn("Failed to get open slots by role", zap.Error(err))
				continue
			} else if n < len(entrants) {
				continue
			}

			logger := logger.With(zap.String("mid", l.ID.UUID.String()))
			logger.Debug("Joining social lobby.")
			p.nk.metrics.CustomCounter("lobby_join_backfill", lobbyParams.MetricsTags(), int64(lobbyParams.GetPartySize()))

			if err := p.LobbyJoinEntrants(logger, l, entrants...); err != nil {
				if LobbyErrorCode(err) == ServerIsFull {
					logger.Debug("Server is full, ignoring.")
					continue
				}
				if errors.Is(err, ErrPreJoinPingFailed) {
					logger.Debug("Pre-join ping failed, skipping server.", zap.String("endpoint", l.GameServer.Endpoint.String()))
					continue
				}
				return fmt.Errorf("failed to join existing social lobby: %w", err)
			}
			return nil
		}

		// No suitable social lobby found, create a new one
		logger.Info("Creating new social lobby", zap.Int("attempt", attempt), zap.Int("candidates_tried", len(matches)))
		label, err := p.newLobby(ctx, logger, lobbyParams, entrants...)
		if err != nil {
			// If the error is a lock error, back off and try again.
			if err == ErrFailedToAcquireLock {
				if interval < maxInterval {
					interval = min(interval*2, maxInterval)
				}
				continue
			}

			return NewLobbyErrorf(ServerFindFailed, "failed to create social lobby: %w", err)
		}

		// newLobby already flushed the label index synchronously, so the
		// just-prepared lobby is searchable and the game server presence has
		// been tracked. Proceed straight to join without the historical 1s
		// sleep.
		if err := p.LobbyJoinEntrants(logger, label, entrants...); err != nil {
			if LobbyErrorCode(err) == ServerIsFull {
				logger.Debug("Server is full, ignoring.")
				if interval < maxInterval {
					interval = min(interval*2, maxInterval)
				}
				continue
			}
			return fmt.Errorf("failed to join auto-created social lobby: %w", err)
		}
		return nil
	}
	return NewLobbyErrorf(ServerFindFailed, "exceeded maximum social lobby find attempts")
}

func (p *EvrPipeline) CheckServerPing(ctx context.Context, logger *zap.Logger, session *sessionWS, groupID string) error {

	params, ok := LoadParams(session.Context())
	if !ok {
		return fmt.Errorf("failed to load lobby session parameters")
	}

	latencyHistory := params.latencyHistory.Load()

	// Phase 1 fallback: if ping discovery has already warmed the cache for a
	// sufficient fraction of servers, skip the blocking ping request. The
	// matchmaker will use the cached latencies directly.
	discoveryCutoff := time.Now().Add(-5 * time.Minute) // same window as preJoinPingMaxAge

	// Build set of IPs this player has failed to connect to.
	var unreachableIPs map[string]struct{}
	if params.unreachableServers != nil {
		if u := params.unreachableServers.Load(); u != nil {
			unreachableIPs = u.UnreachableIPs()
		}
	}

	presences, err := p.nk.StreamUserList(StreamModeGameServer, groupID, "", "", false, true)
	if err != nil {
		return fmt.Errorf("Error listing game servers: %w", err)
	}

	// Include any global game servers
	globalPresences, err := p.nk.StreamUserList(StreamModeGameServer, uuid.Nil.String(), "", "", false, true)
	if err != nil {
		return fmt.Errorf("Error listing global game servers: %w", err)
	}
	presences = append(presences, globalPresences...)

	endpointMap := make(map[string]evr.Endpoint, len(presences))
	hostIPs := make([]string, 0, len(presences))
	for _, presence := range presences {
		gPresence := &GameServerPresence{}
		if err := json.Unmarshal([]byte(presence.GetStatus()), gPresence); err != nil {
			logger.Warn("Failed to unmarshal game server presence", zap.Error(err))
			continue
		}
		if !gPresence.Endpoint.IsValid() {
			logger.Warn("Game server has invalid endpoint, skipping", zap.String("presence", presence.GetStatus()))
			continue
		}
		extIP := gPresence.Endpoint.GetExternalIP()
		// Skip servers this player cannot reach.
		if _, blocked := unreachableIPs[extIP]; blocked {
			continue
		}
		hostIPs = append(hostIPs, extIP)
		if _, ok := endpointMap[extIP]; ok {
			continue
		}
		endpointMap[extIP] = gPresence.Endpoint
	}

	// Count how many candidate servers already have fresh latency data from
	// login-time ping discovery.
	cachedCount := 0
	for _, ip := range hostIPs {
		if latencyHistory.HasRecentEntry(ip, discoveryCutoff) {
			cachedCount++
		}
	}

	// If all candidate servers have fresh data, skip the blocking ping request.
	// The matchmaker reads directly from the warm latency history.
	if cachedCount == len(hostIPs) && len(hostIPs) > 0 {
		logger.Debug("CheckServerPing: all candidates have fresh latency data, skipping ping request",
			zap.Int("cached", cachedCount), zap.Int("total", len(hostIPs)))
		return nil
	}

	logger.Debug("CheckServerPing: cache miss, sending ping request",
		zap.Int("cached", cachedCount), zap.Int("total", len(hostIPs)))

	sortPingCandidatesByLatencyHistory(hostIPs, latencyHistory)

	candidates := make([]evr.Endpoint, 0, len(hostIPs))

	for _, ip := range hostIPs {
		candidates = append(candidates, endpointMap[ip])
		if len(candidates) >= 16 {
			break
		}
	}

	if err := SendEVRMessages(session, true, evr.NewLobbyPingRequest(275, candidates)); err != nil {
		return fmt.Errorf("failed to send ping request: %w", err)
	}

	return nil
}

func (p *EvrPipeline) isLeaderHeadingToSocial(ctx context.Context, logger *zap.Logger, session *sessionWS, lobbyParams *LobbySessionParameters, lobbyGroup *LobbyGroup) bool {
	leader := lobbyGroup.GetLeader()
	if leader == nil || leader.SessionId == session.id.String() {
		return false
	}

	leaderSessionID := uuid.FromStringOrNil(leader.SessionId)
	leaderUserID := uuid.FromStringOrNil(leader.UserId)

	// 1. Check if the leader is matchmaking.
	// Matchmaking intent takes precedence over their current location.
	mmStream := PresenceStream{
		Mode:    StreamModeMatchmaking,
		Subject: lobbyParams.GroupID,
	}
	if presence := session.pipeline.tracker.GetLocalBySessionIDStreamUserID(leaderSessionID, mmStream, leaderUserID); presence != nil {
		var leaderParams LobbySessionParameters
		if err := json.Unmarshal([]byte(presence.GetStatus()), &leaderParams); err != nil {
			logger.Debug("Failed to unmarshal leader matchmaking status, skipping intent check",
				zap.Error(err), zap.String("leader_sid", leaderSessionID.String()))
			// Cannot determine intent from malformed status — fall through to match check.
		} else if shouldFollowerFindOrCreateSocial(leaderParams.Mode) {
			return true
		} else {
			// Leader is matchmaking for a non-social mode (e.g. Arena).
			// They are heading to a match, not staying in Social.
			return false
		}
	}

	// 2. Check if the leader is already in a social lobby.
	// If they are not matchmaking and are in a social lobby, then they are staying there.
	matchStream := PresenceStream{
		Mode:    StreamModeService,
		Subject: leaderSessionID,
		Label:   StreamLabelMatchService,
	}
	if presence := session.pipeline.tracker.GetLocalBySessionIDStreamUserID(leaderSessionID, matchStream, leaderUserID); presence != nil {
		if matchID := MatchIDFromStringOrNil(presence.GetStatus()); !matchID.IsNil() {
			if label, err := MatchLabelByID(ctx, p.nk, matchID); err == nil && label != nil {
				if shouldFollowerFindOrCreateSocial(label.Mode) {
					return true
				}
			}
		}
	}

	return false
}

// isFollowerInActiveMatch reports whether the follower (session) is currently
// in an Arena or Combat match (public or private). When this returns true,
// the party follow system must not process the LobbyFindSessionRequest —
// doing so would yank the player out of their active match.
//
// Returns false when the session has no match presence, the match label
// cannot be resolved, or the match is a social lobby.
//
// Fixes #460: player mid-match was pulled back to social when party leader
// hit matchmaking.
func (p *EvrPipeline) isFollowerInActiveMatch(ctx context.Context, logger *zap.Logger, session *sessionWS) bool {
	matchStream := PresenceStream{
		Mode:    StreamModeService,
		Subject: session.id,
		Label:   StreamLabelMatchService,
	}
	presence := session.pipeline.tracker.GetLocalBySessionIDStreamUserID(session.id, matchStream, session.userID)
	if presence == nil {
		return false
	}

	followerMatchID := MatchIDFromStringOrNil(presence.GetStatus())
	if followerMatchID.IsNil() {
		return false
	}

	label, err := MatchLabelByID(ctx, p.nk, followerMatchID)
	if err != nil || label == nil {
		return false
	}

	return label.IsArena() || label.IsCombat()
}

// currentSocialLobbyForSession returns the match ID of the social lobby that
// the player is currently in, but ONLY when that lobby is the one we intend to
// send them to. Returns a nil MatchID otherwise, so the normal
// find-or-create flow proceeds.
//
// The guard is TARGET-AWARE (#462). It is not enough for the player's current
// lobby to be a social lobby of the same guild; it must be the *intended
// target*:
//
//   - In a party-follow context (PartyGroupName set, follower is not the
//     leader, and the leader is resolvable to a current match), the target is
//     the leader's match. This lets a player in social lobby X of guild G be
//     correctly moved to a DIFFERENT social lobby Y of the same guild G
//     (GAP 1) instead of being short-circuited on a mere group-ID match.
//   - Otherwise the target is lobbyParams.CurrentMatchID. A cleared
//     CurrentMatchID (the relocate path at lobbyFind nils it to force a move
//     to a larger lobby) yields a nil target, so the requested relocation is
//     NOT short-circuited (GAP 2).
//
// Guild isolation is preserved: a current lobby in a different guild never
// matches the search group and is rejected before the target comparison.
//
// Used as a fast-path guard in lobbyFindOrCreateSocial to avoid rejoining a
// lobby the player is already in — the party follow path can direct a player
// to a social lobby they never left.
func (p *EvrPipeline) currentSocialLobbyForSession(ctx context.Context, logger *zap.Logger, session Session, lobbyParams *LobbySessionParameters, lobbyGroup *LobbyGroup) MatchID {
	matchStream := PresenceStream{
		Mode:    StreamModeService,
		Subject: session.ID(),
		Label:   StreamLabelMatchService,
	}

	ws, ok := session.(*sessionWS)
	if !ok {
		return MatchID{}
	}

	presence := ws.pipeline.tracker.GetLocalBySessionIDStreamUserID(session.ID(), matchStream, session.UserID())
	if presence == nil {
		return MatchID{}
	}

	currentMatchID := MatchIDFromStringOrNil(presence.GetStatus())
	if currentMatchID.IsNil() {
		return MatchID{}
	}

	label, err := MatchLabelByID(ctx, p.nk, currentMatchID)
	if err != nil || label == nil {
		return MatchID{}
	}

	if !label.IsSocial() {
		return MatchID{}
	}

	// Guild isolation: a current lobby in a different guild is never the
	// target of a search scoped to lobbyParams.GroupID.
	if label.GetGroupID() != lobbyParams.GroupID {
		return MatchID{}
	}

	// Resolve the intended target match. Only no-op when the player's current
	// social lobby IS that target.
	target := p.intendedSocialTargetMatchID(ws, lobbyParams, lobbyGroup)
	if target.IsNil() {
		// No concrete target (forced relocation cleared CurrentMatchID, or no
		// follow leader to align with): do not short-circuit the requested move.
		logger.Debug("Social lobby guard: no intended target, not treating as no-op",
			zap.String("current_mid", currentMatchID.String()))
		return MatchID{}
	}

	if target != currentMatchID {
		// Player is in a different social lobby of the same guild than the one
		// we intend to send them to (GAP 1): this is a real move, not a no-op.
		logger.Debug("Social lobby guard: current lobby differs from intended target, not a no-op",
			zap.String("current_mid", currentMatchID.String()),
			zap.String("target_mid", target.String()))
		return MatchID{}
	}

	return currentMatchID
}

// intendedSocialTargetMatchID resolves the social lobby that the caller intends
// to place this session into. In a party-follow context (PartyGroupName set,
// session is not the leader, leader resolvable to a current match) the target
// is the leader's match, resolved via the tracker. Otherwise the target is the
// session's own lobbyParams.CurrentMatchID, which is nil when a relocation was
// requested. Returns a nil MatchID when no concrete target can be resolved.
func (p *EvrPipeline) intendedSocialTargetMatchID(session *sessionWS, lobbyParams *LobbySessionParameters, lobbyGroup *LobbyGroup) MatchID {
	if lobbyGroup != nil && lobbyParams.PartyGroupName != "" && lobbyParams.PartyGroupName != "tablet" {
		leader := lobbyGroup.GetLeader()
		if leader != nil && leader.SessionId != session.ID().String() {
			leaderSessionID := uuid.FromStringOrNil(leader.SessionId)
			leaderUserID := uuid.FromStringOrNil(leader.UserId)

			leaderStream := PresenceStream{
				Mode:    StreamModeService,
				Subject: leaderSessionID,
				Label:   StreamLabelMatchService,
			}
			leaderPresence := session.pipeline.tracker.GetLocalBySessionIDStreamUserID(leaderSessionID, leaderStream, leaderUserID)
			if leaderPresence != nil {
				if leaderMatchID := MatchIDFromStringOrNil(leaderPresence.GetStatus()); !leaderMatchID.IsNil() {
					return leaderMatchID
				}
			}
		}
	}

	return lobbyParams.CurrentMatchID
}

// isFollowerAlreadyInLeaderMatch checks whether the follower is already in
// the same match as the party leader. This is a lightweight tracker-only
// check (no match registry calls) used as a fast path at the top of
// lobbyFind to avoid redundant configureParty / authorization / matchmaking
// stream / TryFollowPartyLeader work when the client re-sends
// LobbyFindSessionRequest on its normal message cycle.
//
// Returns false when the leader cannot be found, either player is not in a
// match, or their match IDs differ.
func (p *EvrPipeline) isFollowerAlreadyInLeaderMatch(logger *zap.Logger, session *sessionWS, lobbyGroup *LobbyGroup) bool {
	leader := lobbyGroup.GetLeader()
	if leader == nil || leader.SessionId == session.id.String() {
		return false
	}

	leaderSessionID := uuid.FromStringOrNil(leader.SessionId)
	leaderUserID := uuid.FromStringOrNil(leader.UserId)

	leaderStream := PresenceStream{
		Mode:    StreamModeService,
		Subject: leaderSessionID,
		Label:   StreamLabelMatchService,
	}
	leaderPresence := session.pipeline.tracker.GetLocalBySessionIDStreamUserID(leaderSessionID, leaderStream, leaderUserID)
	if leaderPresence == nil {
		return false
	}
	leaderMatchID := MatchIDFromStringOrNil(leaderPresence.GetStatus())
	if leaderMatchID.IsNil() {
		return false
	}

	followerStream := PresenceStream{
		Mode:    StreamModeService,
		Subject: session.id,
		Label:   StreamLabelMatchService,
	}
	followerPresence := session.pipeline.tracker.GetLocalBySessionIDStreamUserID(session.id, followerStream, session.userID)
	if followerPresence == nil {
		return false
	}
	followerMatchID := MatchIDFromStringOrNil(followerPresence.GetStatus())

	return followerMatchID == leaderMatchID
}

// isLeaderInArenaCombatMatch reports whether the party leader is currently
// in an Arena or Combat match (public or private). The follower should NOT
// enter the follow path when this returns true — the correct path is the
// one-ticket model where all party members are placed via a single
// matchmaking ticket.
//
// Returns false when the leader cannot be found, is not in a match, is
// actively matchmaking, or is in a social lobby.
func (p *EvrPipeline) isLeaderInArenaCombatMatch(ctx context.Context, logger *zap.Logger, session *sessionWS, params *LobbySessionParameters, lobbyGroup *LobbyGroup) bool {
	leader := lobbyGroup.GetLeader()
	if leader == nil || leader.SessionId == session.id.String() {
		return false
	}

	leaderSessionID := uuid.FromStringOrNil(leader.SessionId)
	leaderUserID := uuid.FromStringOrNil(leader.UserId)

	// If the leader is actively matchmaking, they have not settled into
	// a match yet. Do not gate here — let the normal flow handle it.
	mmStream := PresenceStream{
		Mode:    StreamModeMatchmaking,
		Subject: params.GroupID,
	}
	if session.pipeline.tracker.GetLocalBySessionIDStreamUserID(leaderSessionID, mmStream, leaderUserID) != nil {
		return false
	}

	// Look up the leader's current match.
	matchStream := PresenceStream{
		Mode:    StreamModeService,
		Subject: leaderSessionID,
		Label:   StreamLabelMatchService,
	}
	presence := session.pipeline.tracker.GetLocalBySessionIDStreamUserID(leaderSessionID, matchStream, leaderUserID)
	if presence == nil {
		return false
	}

	leaderMatchID := MatchIDFromStringOrNil(presence.GetStatus())
	if leaderMatchID.IsNil() {
		return false
	}

	label, err := MatchLabelByID(ctx, p.nk, leaderMatchID)
	if err != nil || label == nil {
		return false
	}

	// Arena or Combat (public or private) — follow path is wrong here.
	return label.IsArena() || label.IsCombat()
}

// cancelTicketForLateArrival checks whether the party's leader has an active
// matchmaking ticket that does not include this session. If so, the ticket is
// cancelled so the leader can rebuild with the full party.
//
// After cancellation the caller should fall through to the normal non-leader
// path. The leader's lobbyMatchMakeWithFallback will submit a new ticket (via
// replaceTicket) that includes this session because it is already a party
// member. When matched, the match builder places everyone.
//
// This only applies to Arena/Combat modes. Social lobbies converge via
// find-or-create and do not use immutable matchmaking tickets.
func (p *EvrPipeline) cancelTicketForLateArrival(_ context.Context, logger *zap.Logger, session *sessionWS, lobbyParams *LobbySessionParameters, lobbyGroup *LobbyGroup) {
	leader := lobbyGroup.GetLeader()
	if leader == nil || leader.SessionId == session.id.String() {
		return
	}

	leaderSessionID := uuid.FromStringOrNil(leader.SessionId)
	leaderUserID := uuid.FromStringOrNil(leader.UserId)

	// The leader tracks on the matchmaking stream when actively queueing.
	// If they are NOT on the stream, there is no ticket to cancel.
	mmStream := PresenceStream{
		Mode:    StreamModeMatchmaking,
		Subject: lobbyParams.GroupID,
	}
	if session.pipeline.tracker.GetLocalBySessionIDStreamUserID(leaderSessionID, mmStream, leaderUserID) == nil {
		return
	}

	// Leader is matchmaking. Cancel all tickets for the party so the
	// leader can rebuild with the late arrival included.
	logger.Info("Cancelling matchmaking ticket for late party arrival",
		zap.String("late_session", session.id.String()),
		zap.String("leader_session", leader.SessionId),
		zap.String("party_id", lobbyGroup.IDStr()),
		zap.Int("party_size", lobbyGroup.Size()))

	// Remove party-scoped tickets first.
	if err := lobbyGroup.MatchmakerRemoveAll(); err != nil {
		logger.Warn("Failed to cancel party matchmaking tickets for late arrival",
			zap.Error(err))
	}

	// Also remove any solo ticket the leader submitted before the late
	// arrival joined. When the leader initially had a party of 1, addTicket
	// takes the solo path and creates a ticket with an empty party ID.
	// MatchmakerRemoveAll only removes tickets keyed by the party ID, so
	// the solo ticket survives. RemoveSessionAll catches it.
	if err := lobbyGroup.MatchmakerRemoveSessionAll(leader.SessionId); err != nil {
		logger.Warn("Failed to cancel leader session tickets for late arrival",
			zap.Error(err))
	}

	// Signal the leader's matchmaking loop to rebuild the ticket
	// immediately instead of waiting for the fallback timer.
	lobbyGroup.SignalTicketRebuild()

	// Observer: ticket cancelled due to late arrival.
	if lc := getMatchLifecycle(session); lc != nil {
		lc.Transition(StateHolding, "ticket cancelled for late arrival, rebuilding")
	}
}

func PrepareEntrantPresences(ctx context.Context, logger *zap.Logger, nk runtime.NakamaModule, sessionRegistry SessionRegistry, lobbyParams *LobbySessionParameters, sessionIDs ...uuid.UUID) ([]*EvrMatchPresence, error) {

	entrantPresences := make([]*EvrMatchPresence, 0, len(sessionIDs))
	for _, sessionID := range sessionIDs {
		session := sessionRegistry.Get(sessionID)
		if session == nil {
			logger.Warn("Session not found", zap.String("sid", sessionID.String()))
			continue
		}
		mmMode := lobbyParams.Mode
		if mmMode == evr.ModeSocialPublic {
			mmMode = evr.ModeArenaPublic
		}

		rating, err := MatchmakingRatingLoad(ctx, nk, session.UserID().String(), lobbyParams.GroupID.String(), mmMode)
		if err != nil {
			logger.Warn("Failed to load rating", zap.String("sid", sessionID.String()), zap.Error(err))
			rating = NewDefaultRating()
		}

		presence, err := EntrantPresenceFromSession(session, lobbyParams.PartyID, lobbyParams.Role, rating, lobbyParams.GroupID.String(), 0, "")
		if err != nil {
			logger.Warn("Failed to create entrant presence", zap.String("session_id", session.ID().String()), zap.Error(err))
			continue
		}

		entrantPresences = append(entrantPresences, presence)
	}

	if len(entrantPresences) == 0 {
		return nil, fmt.Errorf("no entrants found")
	}

	return entrantPresences, nil
}

// appendPartyReservationPlaceholders adds minimal EvrMatchPresence entries for
// online party members who are not already in the entrants slice. This is used
// for social lobbies so that LobbyJoinEntrants creates slot reservations for
// party followers who haven't started their own lobby find yet.
// Returns the (possibly extended) entrants slice unchanged if the conditions
// are not met (non-social mode, no party, solo player).
func appendPartyReservationPlaceholders(logger *zap.Logger, entrants []*EvrMatchPresence, lobbyGroup *LobbyGroup, lobbyParams *LobbySessionParameters, node string) []*EvrMatchPresence {
	if lobbyParams.Mode != evr.ModeSocialPublic || lobbyGroup == nil || lobbyGroup.Size() <= 1 {
		return entrants
	}

	entrantSet := make(map[uuid.UUID]struct{}, len(entrants))
	for _, e := range entrants {
		entrantSet[e.SessionID] = struct{}{}
	}

	for _, member := range lobbyGroup.List() {
		memberSID := uuid.FromStringOrNil(member.Presence.GetSessionId())
		if _, exists := entrantSet[memberSID]; exists {
			continue
		}
		placeholder := &EvrMatchPresence{
			SessionID:     memberSID,
			UserID:        uuid.FromStringOrNil(member.Presence.GetUserId()),
			Username:      member.Presence.GetUsername(),
			PartyID:       lobbyParams.PartyID,
			RoleAlignment: evr.TeamSocial,
			Node:          node,
		}
		entrants = append(entrants, placeholder)
		logger.Debug("Added party reservation placeholder",
			zap.String("uid", member.Presence.GetUserId()),
			zap.String("sid", member.Presence.GetSessionId()))
	}

	return entrants
}

// TryFollowPartyLeader attempts to join the party leader's current match.
// Returns true if the follower successfully joined the leader's match.
// Returns false if the leader is not in a match or the join failed and the
// follower should fall through to normal lobby find/create.
func (p *EvrPipeline) TryFollowPartyLeader(ctx context.Context, logger *zap.Logger, session *sessionWS, params *LobbySessionParameters, lobbyGroup *LobbyGroup) bool {

	leader := lobbyGroup.GetLeader()
	if leader == nil {
		logger.Warn("Party leader not found, falling through to normal find")
		return false
	}

	logger.Debug("User is member of party, attempting to follow leader", zap.String("leader", leader.GetUsername()))

	// Check if the leader has changed to this player.
	if leader.SessionId == session.id.String() {
		logger.Debug("This player is now the leader, falling through")
		return false
	}

	leaderSessionID := uuid.FromStringOrNil(leader.SessionId)
	leaderUserID := uuid.FromStringOrNil(leader.UserId)

	// If the leader is currently matchmaking, don't try to follow their
	// old match — wait for matchmaking to complete. Without this check,
	// the follower joins the leader's stale match (e.g. social lobby),
	// which untracks their matchmaking stream and gets them kicked from
	// the party ticket. This is the primary cause of the "rubber-banding"
	// bug in parties of 3+.
	if pr := session.pipeline.tracker.GetLocalBySessionIDStreamUserID(leaderSessionID, params.MatchmakingStream(), leaderUserID); pr != nil {
		logger.Debug("Leader is currently matchmaking, falling through")
		return false
	}

	// Look up the leader's current match via tracker.
	stream := PresenceStream{
		Mode:    StreamModeService,
		Subject: leaderSessionID,
		Label:   StreamLabelMatchService,
	}
	presence := session.pipeline.tracker.GetLocalBySessionIDStreamUserID(leaderSessionID, stream, leaderUserID)
	if presence == nil {
		logger.Debug("Leader is not in a match, falling through to normal find")
		return false
	}

	leaderMatchID := MatchIDFromStringOrNil(presence.GetStatus())
	if leaderMatchID.IsNil() {
		logger.Debug("Leader has no match ID, falling through to normal find")
		return false
	}

	// Check if we're already in the leader's match.
	memberStream := PresenceStream{
		Mode:    StreamModeService,
		Subject: session.id,
		Label:   StreamLabelMatchService,
	}
	if memberPresence := session.pipeline.tracker.GetLocalBySessionIDStreamUserID(session.id, memberStream, session.userID); memberPresence != nil {
		memberMatchID := MatchIDFromStringOrNil(memberPresence.GetStatus())
		if memberMatchID == leaderMatchID {
			logger.Debug("Already in leader's match")
			return true
		}
	}

	// Validate the leader's match is joinable.
	label, err := MatchLabelByID(ctx, p.nk, leaderMatchID)
	if err != nil {
		logger.Warn("Failed to get leader's match label", zap.Error(err))
		return false
	}
	if label == nil {
		logger.Debug("Leader's match not found")
		return false
	}

	partySize := lobbyGroup.Size()
	if partySize < 1 {
		partySize = 1
	}

	// Count how many party members are already in the match.
	// The required slots should only be for the members NOT in the match.
	countInMatch := 0
	for _, member := range lobbyGroup.List() {
		if label.GetPlayerByUserID(member.Presence.GetUserId()) != nil {
			countInMatch++
		}
	}
	requiredSlots := partySize - countInMatch

	if !label.Open || label.OpenPlayerSlots() < requiredSlots {
		// For social lobbies, the leader may have created a reservation for
		// this follower. Reservations are counted in the label's Size (making
		// it appear full from outside) but the match handler will accept
		// reserved players via LoadAndDeleteReservation. Attempt the join
		// instead of immediately giving up.
		if label.IsSocial() {
			logger.Debug("Leader's social lobby appears full, but attempting join (may have reservation)",
				zap.Int("open_slots", label.OpenPlayerSlots()),
				zap.Int("required_slots", requiredSlots))
		} else {
			logger.Debug("Leader's match is full or closed",
				zap.Bool("open", label.Open),
				zap.Int("open_slots", label.OpenPlayerSlots()),
				zap.Int("party_size", partySize),
				zap.Int("required_slots", requiredSlots))

			if params.CurrentMatchID.IsNil() {
				// Intentional side-effect: mutate params.Mode so the caller (lobbyFind)
				// sends this follower to a social lobby rather than leaving them at the
				// main menu. The false return signals "don't treat this as a successful
				// follow" — lobbyFind will see the updated mode and call lobbyFindOrCreateSocial.
				logger.Info("Leader's match is full, forcing follower to Social mode")
				params.Mode = evr.ModeSocialPublic
				params.Level = evr.LevelUnspecified
				return false
			}
			// Follower is in a lobby; poll and retry.
			return p.pollFollowPartyLeader(ctx, logger, session, params, lobbyGroup)
		}
	}

	// Defense-in-depth: the follow path is only for social lobby convergence.
	// Arena/Combat uses the one-ticket model. The gate in lobbyFind should
	// have prevented reaching here for non-social modes, but guard anyway.
	if !label.IsSocial() {
		logger.Info("Leader is in a non-social match, follow path not applicable",
			zap.String("leader_match_mode", label.Mode.String()))
		return false
	}
	// ModeSocialPrivate is excluded: private lobbies require explicit invitation.
	// Party follow must not bypass that gate.
	if label.Mode != evr.ModeSocialPublic {
		logger.Debug("Leader is in a non-joinable mode for party follow",
			zap.String("mode", label.Mode.String()))
		return false
	}

	// Try to join the leader's match.
	logger.Debug("Joining leader's lobby", zap.String("mid", leaderMatchID.String()))
	if err := p.lobbyJoin(ctx, logger, session, params, leaderMatchID); err != nil {
		code := LobbyErrorCode(err)
		logger.Debug("Failed to join leader's lobby", zap.Error(err), zap.Int("code", int(code)))

		if params.CurrentMatchID.IsNil() {
			// Follower is at main menu; fall through to normal find.
			return false
		}
		// Follower is in a lobby; poll and retry.
		return p.pollFollowPartyLeader(ctx, logger, session, params, lobbyGroup)
	}

	// Observer: follower successfully followed leader to match.
	if lc := getMatchLifecycle(session); lc != nil {
		lc.TransitionTo(StateInMatch, "followed leader to match", WithMatchID(leaderMatchID.String()))
	}

	return true
}

// pollFollowPartyLeader polls for the party leader to join a match.
func (p *EvrPipeline) pollFollowPartyLeader(ctx context.Context, logger *zap.Logger, session *sessionWS, params *LobbySessionParameters, lobbyGroup *LobbyGroup) bool {
	logger.Debug("Polling to follow party leader")

	// isFollowerInLeaderMatch checks if the follower was placed into the
	// leader's match (e.g., by the matchmaker).
	isFollowerInLeaderMatch := func() bool {
		leader := lobbyGroup.GetLeader()
		if leader == nil || leader.SessionId == session.id.String() {
			return false
		}
		leaderSessionID := uuid.FromStringOrNil(leader.SessionId)
		leaderUserID := uuid.FromStringOrNil(leader.UserId)

		leaderStream := PresenceStream{
			Mode:    StreamModeService,
			Subject: leaderSessionID,
			Label:   StreamLabelMatchService,
		}
		leaderPresence := session.pipeline.tracker.GetLocalBySessionIDStreamUserID(leaderSessionID, leaderStream, leaderUserID)
		if leaderPresence == nil {
			return false
		}
		leaderMatchID := MatchIDFromStringOrNil(leaderPresence.GetStatus())
		if leaderMatchID.IsNil() {
			return false
		}

		if !params.CurrentMatchID.IsNil() && leaderMatchID == params.CurrentMatchID {
			return false
		}

		memberStream := PresenceStream{
			Mode:    StreamModeService,
			Subject: session.id,
			Label:   StreamLabelMatchService,
		}
		memberPresence := session.pipeline.tracker.GetLocalBySessionIDStreamUserID(session.id, memberStream, session.userID)
		if memberPresence == nil {
			return false
		}
		followerMatchID := MatchIDFromStringOrNil(memberPresence.GetStatus())
		if followerMatchID != leaderMatchID {
			return false
		}

		// MatchLabelByID is authoritative when available. Fall back to
		// tracker-based convergence only when the registry is unreachable
		// (nil NK) or the match is genuinely not found. When the context
		// is canceled the label lookup fails spuriously — return false to
		// avoid false-positive convergence that causes party splits.
		if p.nk != nil {
			label, err := MatchLabelByID(ctx, p.nk, leaderMatchID)
			if err == nil && label != nil {
				return label.GetPlayerByUserID(session.userID.String()) != nil
			}
			if ctx.Err() != nil {
				return false
			}
		}
		return true
	}

	// Early convergence: the matchmaker may have placed both players into
	// the same match before the poll loop started. Check before any wait.
	if isFollowerInLeaderMatch() {
		logger.Debug("Follower already in leader's match, poll returning success")
		return true
	}

	const maxNonJoinableCycles = 3
	nonJoinableCycles := 0

	for {
		select {
		case <-ctx.Done():
			if isFollowerInLeaderMatch() {
				logger.Debug("Context canceled but follower is in leader's match (placed by matchmaker)")
				return true
			}
			return false
		case <-time.After(3 * time.Second):
		}

		// Re-check convergence after the poll interval. The matchmaker may
		// have placed the follower during the wait.
		if isFollowerInLeaderMatch() {
			logger.Debug("Follower is in leader's match after poll interval, returning success")
			return true
		}

		leader := lobbyGroup.GetLeader()
		if leader == nil {
			logger.Warn("Party leader not found during poll")
			return false
		}

		leaderUserID := uuid.FromStringOrNil(leader.UserId)

		if leader.SessionId == session.id.String() {
			logger.Debug("This player became the leader during poll")
			return false
		}

		leaderSessionID := uuid.FromStringOrNil(leader.SessionId)

		stream := PresenceStream{
			Mode:    StreamModeService,
			Subject: leaderSessionID,
			Label:   StreamLabelMatchService,
		}

		presence := session.pipeline.tracker.GetLocalBySessionIDStreamUserID(leaderSessionID, stream, leaderUserID)
		if presence == nil {
			logger.Debug("Leader left match during poll")
			return false
		}

		leaderMatchID := MatchIDFromStringOrNil(presence.GetStatus())
		if leaderMatchID.IsNil() {
			continue
		}

		// Wait for the leader to settle into the match before attempting to join.
		select {
		case <-ctx.Done():
			if isFollowerInLeaderMatch() {
				logger.Debug("Context canceled during settle but follower is in leader's match")
				return true
			}
			return false
		case <-time.After(3 * time.Second):
		}

		// Re-check convergence after the settle wait.
		if isFollowerInLeaderMatch() {
			logger.Debug("Follower is in leader's match after settle wait, returning success")
			return true
		}

		// Nil-safe: skip label validation when NK is unavailable (tests,
		// transient startup). The early convergence checks above have already
		// handled the common case — this path is for joining a match the
		// follower is not yet in.
		if p.nk == nil {
			logger.Debug("Match registry unavailable (nil NK), continuing poll without label validation")
			continue
		}

		label, err := MatchLabelByID(ctx, p.nk, leaderMatchID)
		if err != nil || label == nil {
			logger.Debug("Leader's match label unavailable during poll, retrying", zap.Error(err))
			continue
		}

		partySize := lobbyGroup.Size()
		if partySize < 1 {
			partySize = 1
		}

		countInMatch := 0
		for _, member := range lobbyGroup.List() {
			if label.GetPlayerByUserID(member.Presence.GetUserId()) != nil {
				countInMatch++
			}
		}
		requiredSlots := partySize - countInMatch

		if !label.Open || label.OpenPlayerSlots() < requiredSlots {
			if !label.IsSocial() {
				nonJoinableCycles++
				if nonJoinableCycles >= maxNonJoinableCycles {
					return false
				}
			}
			continue
		}

		// For social modes, skip polling and return false so the follower
		// is released to independent lobby finding.
		if shouldFollowerFindOrCreateSocial(params.Mode) {
			return false
		}

		// Defense-in-depth: follow path is only for social lobby convergence.
		// Arena/Combat uses the one-ticket model. The gate in lobbyFind
		// should have prevented reaching here for non-social modes.
		if label.Mode != evr.ModeSocialPublic {
			logger.Info("Leader is in a non-social match during poll, follow path not applicable",
				zap.String("leader_match_mode", label.Mode.String()))
			return false
		}

		logger.Debug("Joining leader's social lobby during poll", zap.String("mid", leaderMatchID.String()))
		if err := p.lobbyJoin(ctx, logger, session, params, leaderMatchID); err != nil {
			code := LobbyErrorCode(err)
			if code == ServerIsFull || code == ServerIsLocked {
				<-time.After(5 * time.Second)
				continue
			}
			logger.Warn("Failed to join leader's lobby during poll", zap.Error(err))
			return false
		}
		// Observer: follower joined leader's social lobby during poll.
		if lc := getMatchLifecycle(session); lc != nil {
			lc.TransitionTo(StateInMatch, "followed leader to social lobby via poll", WithMatchID(leaderMatchID.String()))
		}
		return true
	}
}
