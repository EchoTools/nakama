package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/gofrs/uuid/v5"
	"github.com/heroiclabs/nakama-common/api"
	"github.com/heroiclabs/nakama-common/runtime"
	"github.com/heroiclabs/nakama/v3/server/evr"
	"github.com/intinig/go-openskill/types"
	"go.uber.org/zap"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

var LobbyTestCounter = 0

var ErrCreateLock = errors.New("failed to acquire create lock")
var MatchmakingTimeout = 5 * time.Minute

//var MatchmakingTimeout = 30 * time.Second

// lobbyJoinSessionRequest is a request to join a specific existing session.
func (p *EvrPipeline) lobbyFind(ctx context.Context, logger *zap.Logger, session *sessionWS, params *LobbySessionParameters) error {

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	// Do authorization checks related to the guild.
	if err := p.authorizeGuildGroupSession(ctx, session, params.GroupID.String()); err != nil {
		return fmt.Errorf("failed to authorize guild group session: %w", err)
	}

	switch params.Mode {
	case evr.ModeArenaPublic:
	case evr.ModeSocialPublic, evr.ModeCombatPublic:

	default:
		return NewLobbyError(BadRequest, "invalid mode")
	}

	// This stream tracks the user's matchmaking status.
	// This stream is untracked when the user cancels matchmaking.
	groupStream, err := JoinMatchmakingStream(logger, session, params)
	if err != nil {
		return errors.Join(NewLobbyError(InternalError, "failed to join matchmaking stream"), err)
	}

	// The lobby group is the party that the user is currently in.
	lobbyGroup, err := JoinLobbyGroup(session, params.PartyGroupName, params.PartyID)
	if err != nil {
		return errors.Join(NewLobbyError(InternalError, "failed to join lobby group"), err)
	}

	// Only try to join the party leader if this player is currently in a match (i.e not joining from the main menu)
	if !params.CurrentMatchID.IsNil() {

		if lobbyGroup.GetLeader().SessionId != session.id.String() {
			partyStream := lobbyGroup.PresenceStream()
			logger.Debug("User is member of party", zap.String("leader", lobbyGroup.GetLeader().GetUsername()))
			// This is a party member, wait for the party leader to join a match, or cancel matchmaking.
			for {
				select {
				case <-ctx.Done():
					return nil
				case <-time.After(5 * time.Second):
				}
				leader := lobbyGroup.GetLeader()
				// Check if the leader has changed to this player.
				if leader == nil || leader.SessionId == session.id.String() {
					return NewLobbyError(BadRequest, "party leader changed")
				}
				// Check if the party leader has joined a match.
				presence := session.pipeline.tracker.GetLocalBySessionIDStreamUserID(uuid.FromStringOrNil(leader.SessionId), partyStream, uuid.FromStringOrNil(leader.UserId))
				if presence == nil {
					return NewLobbyError(BadRequest, "party leader left the party")
				}

				// Check if the party leader is in a match.
				leaderMatchID := MatchIDFromStringOrNil(presence.GetStatus())
				if leaderMatchID.IsNil() {
					continue
				}

				// Wait 3 seconds, then check if this player is in the match as well (i.e. the matchmaker sent them to a match)
				select {
				case <-ctx.Done():
					return nil
				case <-time.After(3 * time.Second):
				}

				presence = session.pipeline.tracker.GetLocalBySessionIDStreamUserID(session.id, partyStream, session.userID)
				if presence == nil {
					return NewLobbyError(BadRequest, "party leader left the party")
				}

				memberMatchID := MatchIDFromStringOrNil(presence.GetStatus())
				if memberMatchID.IsNil() {
					continue
				}

				if memberMatchID == leaderMatchID {
					// The leader is in a match, and this player is in the same match.
					continue
				} else {
					// If the leader is in a different public lobby, try to join it.
					label, err := MatchLabelByID(ctx, p.runtimeModule, leaderMatchID)
					if err != nil {
						return errors.Join(NewLobbyError(InternalError, "failed to get match by session id"), err)
					} else if label == nil {
						continue
					}

					if !label.Open || label.PlayerCount >= label.PlayerLimit {
						// The leader's match is full.
						continue
					}

					switch label.Mode {

					case evr.ModeSocialPublic, evr.ModeCombatPublic, evr.ModeArenaPublic:
						// Join the leader's match.
						logger.Debug("Joining leader's lobby", zap.String("mid", leaderMatchID.String()))
						params.CurrentMatchID = leaderMatchID
						if err := p.lobbyJoin(ctx, logger, session, params); err != nil {
							if LobbyErrorIs(err, ServerIsFull) || LobbyErrorIs(err, ServerIsLocked) {
								<-time.After(5 * time.Second)
								continue
							}
							return errors.Join(NewLobbyError(InternalError, "failed to join leader's social lobby"), err)
						}
						return nil
					}
				}
				// The leader is in a match, but this player is not.
				return NewLobbyError(ServerIsLocked, "party leader is in a match")
			}

		}

		logger.Debug("User is leader of party")
		params.PartySize = len(lobbyGroup.List())

		// Wait for the party
		delay := 10 * time.Second
		if params.Mode == evr.ModeSocialPublic {
			delay = 1 * time.Second
		}
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(delay):
		}

		// Remove any players not matchmaking.
		for _, member := range lobbyGroup.List() {
			if member.Presence.GetSessionId() == session.id.String() {
				continue
			}

			sessionID := uuid.FromStringOrNil(member.Presence.GetSessionId())
			userID := uuid.FromStringOrNil(member.Presence.GetUserId())
			if session.tracker.GetLocalBySessionIDStreamUserID(sessionID, groupStream, userID) == nil {
				// Kick the player from the party.
				logger.Debug("Kicking player from party, because they are not matchmaking.", zap.String("uid", member.Presence.GetUserId()))
				session.tracker.UntrackLocalByModes(sessionID, map[uint8]struct{}{StreamModeParty: {}}, PresenceStream{})
			}
		}
	}
	// Check latency to active game servers.
	go func() {
		// Give a delay to ensure the client is ready to receive the ping response.
		select {
		case <-ctx.Done():
			return
		case <-time.After(1 * time.Second):
		}

		activeEndpoints := make([]evr.Endpoint, 0, 100)
		p.broadcasterRegistrationBySession.Range(func(_ string, b *MatchBroadcaster) bool {
			activeEndpoints = append(activeEndpoints, b.Endpoint)
			return true
		})

		if err := PingGameServers(ctx, logger, session, p.db, activeEndpoints); err != nil {
			logger.Warn("Failed to ping game servers", zap.Error(err))
		}
	}()

	if params.Mode == evr.ModeArenaPublic || params.Mode == evr.ModeCombatPublic {
		// Matchmake a new lobby session
		logger.Debug("matchmaking", zap.Any("members", lobbyGroup.List()))

		if err := p.lobbyMatchMake(ctx, logger, session, params, lobbyGroup); err != nil {
			return errors.Join(NewLobbyError(InternalError, "failed to matchmake"), err)
		}
	}

	timeout := time.After(MatchmakingTimeout)

	// Maintain a simple cache of ratings to avoid repeated session lookups.
	ratingCache := make(map[string]types.Rating)

	initialDelay := 1 * time.Second
	backfillInterval := 6 * time.Second

	initialTimer := time.NewTimer(initialDelay)

	for {
		var err error
		select {
		case <-ctx.Done():
			if ctx.Err() != nil && ctx.Err() != context.Canceled {
				return errors.Join(NewLobbyError(BadRequest, "context error"), ctx.Err())
			}
			return nil
		case <-timeout:
			return NewLobbyError(Timeout, "matchmaking timeout")
		case <-initialTimer.C:
			logger.Debug("initial timer")
		case <-time.After(backfillInterval):
		}

		if params.DisableArenaBackfill && params.Mode == evr.ModeArenaPublic {
			continue
		}

		sessionIDs := []uuid.UUID{session.id}
		// Prepare all of the presences
		presences := lobbyGroup.List()

		ratedTeam := make(RatedTeam, 0, len(presences))

		for _, presence := range presences {

			rating, ok := ratingCache[presence.Presence.GetUserId()]
			if !ok {
				rating, err := GetRatinByUserID(ctx, p.db, presence.Presence.GetUserId())
				if err != nil || rating.Mu == 0 || rating.Sigma == 0 || rating.Z == 0 {
					rating = NewDefaultRating()
				}
				ratingCache[presence.Presence.GetUserId()] = rating
			}
			ratedTeam = append(ratedTeam, rating)

			if presence.Presence.GetSessionId() == session.id.String() {
				continue
			}

			sessionIDs = append(sessionIDs, uuid.FromStringOrNil(presence.Presence.GetSessionId()))
		}
		teamRating := ratedTeam.Rating()

		entrants, err := EntrantPresencesFromSessionIDs(logger, p.sessionRegistry, params.PartyID, params.GroupID, &teamRating, params.Role, sessionIDs...)
		if err != nil {
			return NewLobbyError(InternalError, "failed to create entrant presences")
		}

		label, err := p.lobbyQueue.GetUnfilledMatch(ctx, params)
		if err == ErrNoUnfilledMatches {

			if params.Mode == evr.ModeSocialPublic {

				err := p.NewSocialLobby(ctx, logger, session, params)
				if err == ErrCreateLock {
					<-time.After(5 * time.Second)
					continue
				}
				return err
			}
			continue
		} else if err != nil {
			return errors.Join(NewLobbyError(InternalError, "failed to get unfilled match"), err)
		}

		logger := logger.With(zap.String("mid", label.ID.UUID.String()))

		logger.Debug("Joining backfill match.")
		p.metrics.CustomCounter("lobby_join_backfill", params.MetricsTags(), int64(params.PartySize))

		label, serverSession, err := p.LobbySessionGet(ctx, logger, label.ID)
		if err != nil {
			logger.Debug("Failed to get match session", zap.Error(err))
			continue
		}

		for _, e := range entrants {
			go func(e *EvrMatchPresence) {
				session := p.sessionRegistry.Get(e.SessionID)
				if session == nil {
					logger.Debug("Session not found", zap.String("sid", e.SessionID.String()))
					return
				}
				if err := p.LobbyJoinEntrant(logger, serverSession, label, UnassignedRole, e); err != nil {
					// Send the error to the client
					if err := SendEVRMessages(session, LobbySessionFailureFromError(label.Mode, label.GetGroupID(), err)); err != nil {
						logger.Debug("Failed to send error message", zap.Error(err))
					}
				}
			}(e)
		}

		logger.Debug("Joined match")
		return nil
	}
	return nil
}

func lobbyBackfillQuery(p *LobbySessionParameters) (string, error) {

	qparts := []string{
		"+label.open:T",
		fmt.Sprintf("+label.mode:%s", p.Mode.String()),
		fmt.Sprintf("+label.group_id:/(%s)/", Query.Escape(p.GroupID.String())),
		p.BackfillQueryAddon,
	}

	if !p.CurrentMatchID.IsNil() {
		qparts = append(qparts, fmt.Sprintf("-label.id:/(%s)/", Query.Escape(p.CurrentMatchID.String())))
	}

	playerLimit := MatchLobbyMaxSize
	if size, ok := LobbySizeByMode[p.Mode]; ok {
		playerLimit = size
	}

	qparts = append(qparts, fmt.Sprintf("+label.player_count:<=%d", playerLimit-p.PartySize))

	return strings.Join(qparts, " "), nil
}

// Wrapper for the matchRegistry.ListMatches function.
func listMatches(ctx context.Context, nk runtime.NakamaModule, limit int, minSize int, maxSize int, query string) ([]*api.Match, error) {
	return nk.MatchList(ctx, limit, true, "", &minSize, &maxSize, query)
}

func listUnfilledLobbies(ctx context.Context, nk runtime.NakamaModule, partySize int, mode evr.Symbol, query string) ([]*MatchLabel, error) {
	var err error

	var labels []*MatchLabel

	minSize := 0
	limit := 100
	lobbySize := SocialLobbyMaxSize
	if l, ok := LobbySizeByMode[mode]; ok {
		lobbySize = l
	}

	maxSize := lobbySize - partySize

	// Search for possible matches
	matches, err := listMatches(ctx, nk, limit, minSize+1, maxSize+1, query)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "Failed to find matches: %v", err)
	}

	// Create a label slice of the matches
	labels = make([]*MatchLabel, 0, len(matches))
	for _, match := range matches {
		label := &MatchLabel{}
		if err := json.Unmarshal([]byte(match.GetLabel().GetValue()), label); err != nil {
			continue
		}
		labels = append(labels, label)
	}

	return labels, nil
}

func CompactedFrequencySort[T comparable](s []T, desc bool) []T {
	s = s[:]
	// Create a map of the frequency of each item
	frequency := make(map[T]int, len(s))
	for _, item := range s {
		frequency[item]++
	}
	// Sort the items by frequency
	slices.SortStableFunc(s, func(a, b T) int {
		return frequency[a] - frequency[b]
	})
	if desc {
		slices.Reverse(s)
	}
	return slices.Compact(s)
}

// Backfill returns a match that the player can backfill
func (p *EvrPipeline) GetBackfillCandidates(ctx context.Context, logger *zap.Logger, userID uuid.UUID, params *LobbySessionParameters, query string) ([]*MatchLabel, error) {
	labels, err := listUnfilledLobbies(ctx, p.runtimeModule, params.PartySize, params.Mode, query)
	if err != nil || len(labels) == 0 {
		return nil, err
	}
	available := make([]*MatchLabel, 0, len(labels))
	for _, label := range labels {
		// If the match is a public match, and it has started, and it has been less than 15 seconds since it started, skip it.
		if label.Mode == evr.ModeArenaPublic || label.Mode == evr.ModeCombatPublic {
			if label.Started() && time.Since(label.StartTime) < MadeMatchBackfillDelay {
				continue
			}
		}
		if label.PlayerCount < label.PlayerLimit {
			available = append(available, label)
		}
	}
	if len(available) == 0 {
		return available, nil
	}
	labels = available

	labelRTTs := params.latencyHistory.LabelsByAverageRTT(labels)

	var cmpFn func(i, j time.Duration, o, p int) bool
	switch params.Mode {
	case evr.ModeArenaPublic:
		cmpFn = RTTweightedPopulationCmp
	default:
		cmpFn = PopulationCmp
	}

	sort.SliceStable(labelRTTs, func(i, j int) bool {
		return cmpFn(labelRTTs[i].AsDuration(), labelRTTs[j].AsDuration(), labels[i].PlayerCount, labels[j].PlayerCount)
	})

	return labels, nil
}

func (p *EvrPipeline) NewSocialLobby(ctx context.Context, logger *zap.Logger, session *sessionWS, params *LobbySessionParameters) error {
	p.metrics.CustomCounter("lobby_create_social", params.MetricsTags(), 1)

	if !p.createLobbyMu.TryLock() {
		return ErrCreateLock
	}
	label, err := lobbyCreateSocial(ctx, logger, p.db, p.runtimeModule, session, p.matchRegistry, params)
	if err != nil {
		p.createLobbyMu.Unlock()
		return errors.Join(NewLobbyError(InternalError, "failed to create social lobby"), err)
	}

	createPresences, err := EntrantPresencesFromSessionIDs(logger, p.sessionRegistry, params.PartyID, params.GroupID, nil, params.Role, session.id)
	if err != nil {
		return errors.Join(NewLobbyError(InternalError, "failed to create entrant presences"), err)
	}

	logger.Debug("Joining newly created social lobby.")
	p.metrics.CustomCounter("lobby_join_created_social", params.MetricsTags(), 1)

	serverSession := p.sessionRegistry.Get(uuid.FromStringOrNil(label.Broadcaster.SessionID))
	if serverSession == nil {
		p.createLobbyMu.Unlock()
		return NewLobbyError(InternalError, "server session not found")
	}
	for _, e := range createPresences {
		go func(e *EvrMatchPresence) {
			if err := p.LobbyJoinEntrant(logger, serverSession, label, params.Role, e); err != nil {
				// Send the error to the client
				if err := SendEVRMessages(session, LobbySessionFailureFromError(label.Mode, label.GetGroupID(), err)); err != nil {
					logger.Debug("Failed to send error message", zap.Error(err))
				}
			}
		}(e)
	}
	p.createLobbyMu.Unlock()
	return nil
}
