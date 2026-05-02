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

// lobbyJoinSessionRequest is a request to join a specific existing session.
func (p *EvrPipeline) lobbyFind(ctx context.Context, logger *zap.Logger, session *sessionWS, lobbyParams *LobbySessionParameters) error {

	startTime := time.Now()

	// Do authorization checks related to the guild.
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

	entrantSessionIDs := []uuid.UUID{session.id}

	var lobbyGroup *LobbyGroup

	if lobbyParams.PartyGroupName != "" && lobbyParams.PartyGroupName != "tablet" {
		var err error
		var isLeader bool
		var memberSessionIDs []uuid.UUID
		lobbyGroup, memberSessionIDs, isLeader, err = p.configureParty(ctx, logger, session, lobbyParams)
		if err != nil {
			return fmt.Errorf("failed to join party: %w", err)
		}

		if !isLeader {
			// If the leader is heading to a social lobby, force the follower into social mode.
			// This prevents the follower from getting stuck in arena matchmaking when the
			// leader has moved on to a social lobby.
			if p.isLeaderHeadingToSocial(ctx, logger, session, lobbyParams, lobbyGroup) {
				logger.Info("Leader is heading to a social lobby, forcing social mode for follower")
				lobbyParams.Mode = evr.ModeSocialPublic
				lobbyParams.Level = evr.LevelUnspecified
			}

			if p.TryFollowPartyLeader(ctx, logger, session, lobbyParams, lobbyGroup) {
				return nil
			}

			// TryFollowPartyLeader returned false. Check if we became the leader
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
			} else if lobbyParams.Mode == evr.ModeSocialPublic || lobbyParams.Mode == evr.ModeSocialNPE {
				// Social mode: skip the polling loop entirely. Social lobbies
				// use find-or-create with party reservations, so the follower
				// will naturally converge to the leader's lobby. Polling for
				// the leader to settle is unnecessary and can silently timeout,
				// leaving the client stuck in infinite matchmaking.
				logger.Info("Follower in social mode, finding social lobby independently (party reservations will converge)")
				lobbyParams.Level = evr.LevelUnspecified
				followerEntrants, err := PrepareEntrantPresences(ctx, logger, p.nk, p.nk.sessionRegistry, lobbyParams, session.id)
				if err != nil {
					return fmt.Errorf("failed to prepare follower entrant: %w", err)
				}
				return p.lobbyFindOrCreateSocial(ctx, logger, session, lobbyParams, followerEntrants...)
			} else {
				// Still a non-leader in a non-social mode. Poll for the leader
				// to settle into a match we can join. This covers followers at
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
					lobbyParams.SetPartySize(1)
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
		return p.lobbyFindOrCreateSocial(ctx, logger, session, lobbyParams, entrants...)
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

		lobbyParams.SetPartySize(partySize)
	}

	memberSessionIDs := []uuid.UUID{session.id}
	// Add the party members to the sessionID slice
	for _, member := range lobbyGroup.List() {
		if member.Presence.GetSessionId() == session.id.String() {
			continue
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

	label, err := LobbyGameServerAllocate(ctx, NewRuntimeGoLogger(logger), p.nk, []string{lobbyParams.GroupID.String()}, latestRTTs, settings, []string{lobbyParams.RegionCode}, true, false, ServiceSettings().Matchmaking.QueryAddons.Create)
	if err != nil {
		// Check if this is a region fallback error - for pipeline, auto-select closest
		var regionErr ErrMatchmakingNoServersInRegion
		if errors.As(err, &regionErr) && regionErr.FallbackInfo != nil {
			logger.Info("Auto-selecting closest server for lobby creation (no servers in requested region)",
				zap.String("requested_region", lobbyParams.RegionCode),
				zap.String("selected_region", regionErr.FallbackInfo.ClosestRegion),
				zap.Int("latency_ms", regionErr.FallbackInfo.ClosestLatencyMs))

			// Allocate without region requirement to get the closest server
			label, err = LobbyGameServerAllocate(ctx, NewRuntimeGoLogger(logger), p.nk, []string{lobbyParams.GroupID.String()}, latestRTTs, settings, nil, true, false, ServiceSettings().Matchmaking.QueryAddons.Create)
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

func (p *EvrPipeline) lobbyFindOrCreateSocial(ctx context.Context, logger *zap.Logger, _ Session, lobbyParams *LobbySessionParameters, entrants ...*EvrMatchPresence) error {
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

		// Try to join an existing social lobby
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

	// 1. Check if the leader is already in a social lobby.
	matchStream := PresenceStream{
		Mode:    StreamModeService,
		Subject: leaderSessionID,
		Label:   StreamLabelMatchService,
	}
	if presence := session.pipeline.tracker.GetLocalBySessionIDStreamUserID(leaderSessionID, matchStream, leaderUserID); presence != nil {
		if matchID := MatchIDFromStringOrNil(presence.GetStatus()); !matchID.IsNil() {
			if label, err := MatchLabelByID(ctx, p.nk, matchID); err == nil && label != nil {
				if label.Mode == evr.ModeSocialPublic || label.Mode == evr.ModeSocialNPE {
					return true
				}
			}
		}
	}

	// 2. Check if the leader is matchmaking for a social lobby.
	mmStream := PresenceStream{
		Mode:    StreamModeMatchmaking,
		Subject: lobbyParams.GroupID,
	}
	if presence := session.pipeline.tracker.GetLocalBySessionIDStreamUserID(leaderSessionID, mmStream, leaderUserID); presence != nil {
		var leaderParams LobbySessionParameters
		if err := json.Unmarshal([]byte(presence.GetStatus()), &leaderParams); err == nil {
			if leaderParams.Mode == evr.ModeSocialPublic || leaderParams.Mode == evr.ModeSocialNPE {
				return true
			}
		}
	}

	return false
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
				// Follower is at main menu; fall through to normal find.
				return false
			}
			// Follower is in a lobby; poll and retry.
			return p.pollFollowPartyLeader(ctx, logger, session, params, lobbyGroup)
		}
	}

	switch label.Mode {
	case evr.ModeSocialPrivate, evr.ModeSocialPublic, evr.ModeCombatPublic, evr.ModeArenaPublic:
	default:
		logger.Debug("Leader is in a non-joinable mode", zap.String("mode", label.Mode.String()))
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

	return true
}

// pollFollowPartyLeader is the blocking polling loop used when the follower
// is already in a lobby and can safely wait for the leader to settle into a match.
func (p *EvrPipeline) pollFollowPartyLeader(ctx context.Context, logger *zap.Logger, session *sessionWS, params *LobbySessionParameters, lobbyGroup *LobbyGroup) bool {

	logger.Debug("Polling to follow party leader (follower is in a lobby)")

	// isFollowerInLeaderMatch checks if the follower was placed into the
	// leader's match (e.g., by the matchmaker). This is used as a final
	// check before returning false due to context cancellation — the
	// matchmaker may have successfully placed both players even though
	// the matchmaking monitor canceled our context.
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

		// Guard against stale service streams: if the leader's match is the
		// same match the follower was in when they started lobby find, this is
		// not a new placement — it's leftover data from the previous lobby.
		// Without this check, a matchmaking timeout produces a false positive
		// (both players still point to the old social lobby), causing
		// pollFollowPartyLeader to return true even though no new match was
		// found. The followers get stuck in transition indefinitely.
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
		if MatchIDFromStringOrNil(memberPresence.GetStatus()) != leaderMatchID {
			return false
		}

		// Verify the follower actually appears in the match's player list,
		// not just in the tracker stream. The tracker can converge before
		// the client completes the join (lobbyJoin), causing a false positive.
		label, err := MatchLabelByID(ctx, p.nk, leaderMatchID)
		if err != nil || label == nil {
			return false
		}
		return label.GetPlayerByUserID(session.userID.String()) != nil
	}

	// Track consecutive cycles where the leader's match is non-joinable.
	// Non-social matches (arena/combat) don't open up mid-game, so polling
	// forever is pointless. After a few cycles, give up so the follower can
	// be redirected to a social lobby.
	const maxNonJoinableCycles = 1
	nonJoinableCycles := 0

	for {
		select {
		case <-ctx.Done():
			// Before giving up, check if the matchmaker placed us into the
			// leader's match. The matchmaking monitor may have canceled our
			// context (by detecting our matchmaking stream was removed), but
			// that removal happened precisely because we were placed into
			// a match. Without this check, the follower returns false and
			// the client retries, creating a persistent matchmaking loop.
			if isFollowerInLeaderMatch() {
				logger.Debug("Context canceled but follower is in leader's match (placed by matchmaker)")
				return true
			}
			return false
		case <-time.After(3 * time.Second):
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

		// Check if the leader is still matchmaking.
		if pr := session.pipeline.tracker.GetLocalBySessionIDStreamUserID(leaderSessionID, params.MatchmakingStream(), leaderUserID); pr != nil {
			continue
		}

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

		// Wait for the leader to settle, then check if we ended up in the same match.
		select {
		case <-ctx.Done():
			// Same check as above — the matchmaker may have placed us during
			// the settle wait.
			if isFollowerInLeaderMatch() {
				logger.Debug("Context canceled during settle but follower is in leader's match")
				return true
			}
			return false
		case <-time.After(3 * time.Second):
		}

		memberStream := PresenceStream{
			Mode:    StreamModeService,
			Subject: session.id,
			Label:   StreamLabelMatchService,
		}
		memberMatchID := MatchID{}
		if memberPresence := session.pipeline.tracker.GetLocalBySessionIDStreamUserID(session.id, memberStream, session.userID); memberPresence != nil {
			memberMatchID = MatchIDFromStringOrNil(memberPresence.GetStatus())
		}

		if memberMatchID == leaderMatchID {
			// Verify the follower actually appears in the match's player list.
			// Stream convergence alone is not sufficient — the client may not
			// have completed the join yet.
			if pollLabel, pollErr := MatchLabelByID(ctx, p.nk, leaderMatchID); pollErr == nil && pollLabel != nil &&
				pollLabel.GetPlayerByUserID(session.userID.String()) != nil {
				logger.Debug("Already in leader's match during poll")
				return true
			}
		}

		label, err := MatchLabelByID(ctx, p.nk, leaderMatchID)
		if err != nil {
			// The leader's old match may have been terminated while they
			// transition to a new lobby. Keep polling instead of giving up
			// — the service stream will update once the leader settles.
			logger.Debug("Leader's match label unavailable during poll, retrying", zap.Error(err))
			continue
		}
		if label == nil {
			continue
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
			// Social lobbies may open up as players leave, so keep polling.
			// Non-social matches (arena/combat) don't open mid-game — stop
			// polling after a few cycles so the follower can be sent to a
			// social lobby instead of waiting forever.
			if !label.IsSocial() {
				nonJoinableCycles++
				if nonJoinableCycles >= maxNonJoinableCycles {
					logger.Debug("Leader's non-social match is persistently non-joinable, releasing follower",
						zap.String("mid", leaderMatchID.String()),
						zap.String("mode", label.Mode.String()),
						zap.Bool("open", label.Open),
						zap.Int("open_slots", label.OpenPlayerSlots()),
						zap.Int("required_slots", requiredSlots))
					return false
				}
			}
			continue
		}

		switch label.Mode {
		case evr.ModeSocialPrivate, evr.ModeSocialPublic, evr.ModeCombatPublic, evr.ModeArenaPublic:
			logger.Debug("Joining leader's lobby during poll", zap.String("mid", leaderMatchID.String()))
			if err := p.lobbyJoin(ctx, logger, session, params, leaderMatchID); err != nil {
				code := LobbyErrorCode(err)
				if code == ServerIsFull || code == ServerIsLocked {
					<-time.After(5 * time.Second)
					continue
				}
				logger.Warn("Failed to join leader's lobby during poll", zap.Error(err))
				return false
			}
			return true
		default:
			logger.Debug("Leader is in a non-joinable mode during poll", zap.String("mode", label.Mode.String()))
			return false
		}
	}
}
