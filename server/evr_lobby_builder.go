package server

import (
	"context"
	"fmt"
	"math/rand"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/gofrs/uuid/v5"
	"github.com/heroiclabs/nakama-common/runtime"
	"github.com/heroiclabs/nakama/v3/server/evr"
	"github.com/intinig/go-openskill/rating"
	"github.com/intinig/go-openskill/types"
	"go.uber.org/zap"
)

// Builds the match after the matchmaker has created it
type LobbyBuilder struct {
	sync.Mutex
	logger *zap.Logger
	nk     runtime.NakamaModule

	sessionRegistry SessionRegistry
	matchRegistry   MatchRegistry
	tracker         Tracker
	profileRegistry *ProfileRegistry
	metrics         Metrics

	mapQueue map[evr.Symbol][]evr.Symbol // map[mode][]level
}

func NewLobbyBuilder(logger *zap.Logger, nk runtime.NakamaModule, sessionRegistry SessionRegistry, matchRegistry MatchRegistry, tracker Tracker, metrics Metrics, profileRegistry *ProfileRegistry) *LobbyBuilder {
	logger = logger.With(zap.String("module", "lobby_builder"))

	return &LobbyBuilder{
		logger: logger,
		nk:     nk,

		sessionRegistry: sessionRegistry,
		matchRegistry:   matchRegistry,
		tracker:         tracker,
		metrics:         metrics,
		profileRegistry: profileRegistry,

		mapQueue: make(map[evr.Symbol][]evr.Symbol),
	}
}

func (b *LobbyBuilder) handleMatchedEntries(entries [][]*MatchmakerEntry) {
	// build matches one at a time.
	for _, entrants := range entries {
		if err := b.buildMatch(b.logger, entrants); err != nil {
			b.logger.With(zap.Any("entries", entries)).Error("Failed to build match", zap.Error(err))
			return
		}
	}
}

func (b *LobbyBuilder) extractLatenciesFromEntrants(entrants []*MatchmakerEntry) map[string][][]float64 {
	latenciesByTeamByExtIP := make(map[string][][]float64, 100)

	for _, e := range entrants {

		// loop over the number props and get the latencies
		for k, v := range e.GetProperties() {
			if extIP, ok := strings.CutPrefix(k, RTTPropertyPrefix); ok {
				latenciesByTeamByExtIP[extIP] = append(latenciesByTeamByExtIP[extIP], []float64{v.(float64)})
			}
		}
	}

	return latenciesByTeamByExtIP
}

// SortGameServerIPs sorts the game server IPs by latency, returning a slice of external IP addresses
func (b *LobbyBuilder) rankEndpointsByAverageLatency(entrants []*MatchmakerEntry) map[string]int {

	latencies := b.extractLatenciesFromEntrants(entrants)

	meanRTTByExtIP := make(map[string]int, len(latencies))

	for extIP, latenciesByTeam := range latencies {
		// Calculate the mean RTT across the lobby
		var sum float64
		for _, teamLatencies := range latenciesByTeam {
			for _, latency := range teamLatencies {
				sum += latency
			}
		}
		meanRTT := sum / float64(len(entrants))

		meanRTTByExtIP[extIP] = int(meanRTT)
	}

	return meanRTTByExtIP
}

// SortGameServerIPs sorts the game server IPs by latency, returning a slice of external IP addresses
func (b *LobbyBuilder) rankEndpointsByServerScore(entrants []*MatchmakerEntry) []string {

	latencies := b.extractLatenciesFromEntrants(entrants)

	scoresByExtIP := make(map[string]float64, len(latencies))

	for extIP, latenciesByTeam := range latencies {
		score := VRMLServerScore(latenciesByTeam, ServerScoreDefaultMinRTT, ServerScoreDefaultMaxRTT, ServerScoreDefaultThreshold, ServerScorePointsDistribution)
		scoresByExtIP[extIP] = score
	}

	// Sort the scored endpoints
	extIPs := make([]string, 0, len(scoresByExtIP))
	for k := range scoresByExtIP {
		extIPs = append(extIPs, k)
	}

	sort.SliceStable(extIPs, func(i, j int) bool {
		return scoresByExtIP[extIPs[i]] < scoresByExtIP[extIPs[j]]
	})

	return extIPs
}

func (b *LobbyBuilder) groupByTicket(entrants []*MatchmakerEntry) [][]*MatchmakerEntry {
	partyMap := make(map[string][]*MatchmakerEntry, 8)
	for _, e := range entrants {
		t := e.GetTicket()
		partyMap[t] = append(partyMap[t], e)
	}

	parties := make([][]*MatchmakerEntry, 0, len(partyMap))
	for _, p := range partyMap {
		parties = append(parties, p)
	}
	return parties
}

func (b *LobbyBuilder) buildMatch(logger *zap.Logger, entrants []*MatchmakerEntry) (err error) {
	// Build matches one at a time.

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	logger = logger.With(zap.Int("entrants", len(entrants)))

	logger.Debug("Building match", zap.Any("entrants", entrants))

	if len(entrants) < 2 {
		return fmt.Errorf("not enough entrants to build a match")
	}

	sbmm := NewSkillBasedMatchmaker()

	groupID, err := b.groupIDFromEntrants(entrants)

	matchmakerEntrants := make([]runtime.MatchmakerEntry, 0, len(entrants))
	for _, e := range entrants {
		matchmakerEntrants = append(matchmakerEntrants, e)
	}

	ratedMatch := sbmm.balanceByTicket(matchmakerEntrants)

	entrantPresences := make([]*EvrMatchPresence, 0, len(entrants))
	sessions := make([]Session, 0, len(entrants))
	for teamIndex, players := range ratedMatch {

		// Assign each player in the team to the match
		for _, entry := range players {

			session := b.sessionRegistry.Get(uuid.FromStringOrNil(entry.Entry.Presence.GetSessionId()))
			if session == nil {
				logger.Warn("Failed to get session from session registry", zap.String("sid", entry.Entry.Presence.GetSessionId()))
				continue
			}

			sessionParams, ok := LoadParams(session.Context())
			if !ok {
				logger.Warn("Failed to load session params", zap.String("sid", entry.Entry.Presence.GetSessionId()))
				continue
			}

			mu := entry.Entry.NumericProperties["rating_mu"]
			sigma := entry.Entry.NumericProperties["rating_sigma"]
			rating := rating.NewWithOptions(&types.OpenSkillOptions{
				Mu:    &mu,
				Sigma: &sigma,
			})

			percentile, ok := entry.Entry.NumericProperties["rank_percentile"]
			if !ok {
				percentile = 0.0
			}

			query, ok := entry.Entry.StringProperties["query"]
			if !ok {
				query = ""
			}

			sessions = append(sessions, session)
			entrantPresences = append(entrantPresences, &EvrMatchPresence{
				Node:           sessionParams.Node,
				UserID:         session.UserID(),
				SessionID:      session.ID(),
				LoginSessionID: sessionParams.LoginSession.Load().id,
				Username:       session.Username(),
				DisplayName:    sessionParams.AccountMetadata.GetGroupDisplayNameOrDefault(groupID.String()),
				EvrID:          sessionParams.EvrID,
				PartyID:        MatchIDFromStringOrNil(entry.Entry.GetPartyId()).UUID,
				RoleAlignment:  teamIndex,
				DiscordID:      sessionParams.DiscordID,
				ClientIP:       session.ClientIP(),
				ClientPort:     session.ClientPort(),
				IsPCVR:         sessionParams.IsPCVR.Load(),
				Rating:         rating,
				RankPercentile: percentile,
				Query:          query,
			})

		}
	}

	// gameServers := b.rankEndpointsByServerScore(entrants)
	gameServers := b.rankEndpointsByAverageLatency(entrants)

	modestr, ok := entrants[0].StringProperties["game_mode"]
	if !ok {
		return fmt.Errorf("missing mode property")
	}

	mode := evr.ToSymbol(modestr)

	settings := &MatchSettings{
		Mode:                mode,
		Level:               b.selectNextMap(mode),
		SpawnedBy:           SystemUserID,
		GroupID:             groupID,
		Reservations:        entrantPresences,
		ReservationLifetime: 20 * time.Second,
		StartTime:           time.Now().UTC(),
	}

	var label *MatchLabel
	timeout := time.After(60 * time.Second)
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timeout:
			return ErrMatchmakingNoAvailableServers
		default:
		}

		label, err = AllocateGameServer(ctx, NewRuntimeGoLogger(logger), b.nk, groupID.String(), gameServers, settings, nil, true, false)
		if err != nil || label == nil {
			logger.Error("Failed to allocate game server.", zap.Error(err))
			<-time.After(5 * time.Second)
			continue
		}
		break
	}

	serverSession := b.sessionRegistry.Get(uuid.FromStringOrNil(label.Broadcaster.SessionID))
	if serverSession == nil {
		return fmt.Errorf("failed to get server session")
	}

	successful := make([]*EvrMatchPresence, 0, len(entrants))
	errored := make([]*EvrMatchPresence, 0, len(entrants))
	wg := &sync.WaitGroup{}
	wg.Add(len(entrantPresences))

	mu := sync.Mutex{}

	for i, p := range entrantPresences {
		go func(session Session, p *EvrMatchPresence) {
			defer wg.Done()
			if p == nil {
				return
			}

			if err := LobbyJoinEntrants(logger, b.matchRegistry, b.tracker, session, serverSession, label, p); err != nil {
				logger.Error("Failed to join entrant to match", zap.String("mid", label.ID.UUID.String()), zap.String("uid", p.GetUserId()), zap.Error(err))
				mu.Lock()
				errored = append(errored, p)
				mu.Unlock()
				return
			}

			mu.Lock()
			successful = append(successful, p)
			mu.Unlock()
		}(sessions[i], p)
	}

	wg.Wait()
	tags := map[string]string{
		"mode":    label.Mode.String(),
		"level":   label.Level.String(),
		"groupID": label.GetGroupID().String(),
	}
	b.metrics.CustomCounter("lobby_join_match_made", tags, int64(len(successful)))
	b.metrics.CustomCounter("lobby_error_match_made", tags, int64(len(errored)))

	logger.Info("Match built.", zap.String("mid", label.ID.UUID.String()), zap.Any("teams", ratedMatch), zap.Any("successful", successful), zap.Any("errored", errored))
	return nil
}

func (b *LobbyBuilder) groupIDFromEntrants(entrants []*MatchmakerEntry) (uuid.UUID, error) {

	var groupID string
	for _, e := range entrants {
		g, ok := e.StringProperties["group_id"]
		if !ok {
			return uuid.Nil, fmt.Errorf("entrant has no group_id")
		}
		if groupID == "" {
			groupID = g
			continue
		}
		if groupID != g {
			return uuid.Nil, fmt.Errorf("multiple group_ids found")
		}
	}
	return uuid.FromStringOrNil(groupID), nil
}

func (b *LobbyBuilder) distributeParties(parties [][]*MatchmakerEntry) [][]*MatchmakerEntry {
	// Distribute the players from each party on the two teams.
	// Try to keep the parties together, but the teams must be balanced.
	// The algorithm is greedy and may not always produce the best result.
	// Each team must be 4 players or less
	teams := [][]*MatchmakerEntry{{}, {}}

	// Sort the parties by size, single players last
	sort.SliceStable(parties, func(i, j int) bool {
		if len(parties[i]) == 1 {
			return false
		}
		return len(parties[i]) < len(parties[j])
	})

	// Distribute the parties to the teams
	for _, party := range parties {
		// Find the team with the least players
		team := 0
		for i, t := range teams {
			if len(t) < len(teams[team]) {
				team = i
			}
		}
		teams[team] = append(teams[team], party...)
	}
	// sort the teams by size
	sort.SliceStable(teams, func(i, j int) bool {
		return len(teams[i]) > len(teams[j])
	})

	for i, player := range teams[0] {
		// If the team is more than two players larger than the other team, distribute the players evenly
		if len(teams[0]) > len(teams[1])+1 {
			// Move a player from teams[0] to teams[1]
			teams[1] = append(teams[1], player)
			teams[0] = append(teams[0][:i], teams[0][i+1:]...)
		}
	}

	return teams
}

// Count the number of active matches by external IP
func countByExtIP(labels []*MatchLabel) map[string]int {
	countByExtIP := make(map[string]int, len(labels))
	for _, label := range labels {
		k := label.Broadcaster.Endpoint.ExternalIP.String()
		countByExtIP[k]++
	}
	return countByExtIP
}

func (b *LobbyBuilder) selectNextMap(mode evr.Symbol) evr.Symbol {
	queue := b.mapQueue[mode]

	if levels, ok := b.mapQueue[mode]; !ok || len(levels) == 0 {
		return evr.LevelUnspecified
	} else if len(evr.LevelsByMode[mode]) == 1 {
		return evr.LevelsByMode[mode][0]
	}

	if len(queue) <= 1 {
		// Fill the queue with the available levels and shuffle.
		queue = append(queue, evr.LevelsByMode[mode]...)

		rand.Shuffle(len(queue), func(i, j int) {
			// leave the first (next) level in place
			if i == 0 || j == 0 {
				return
			}
			queue[i], queue[j] = queue[j], queue[i]
		})

		// If the first two levels are the same, move the first level to the end of the queue.
		if queue[0] == queue[1] {
			queue = append(queue[1:], queue[0])
		}
	}

	// Pop the first level from the queue
	b.mapQueue[mode] = queue[1:]

	return queue[0]
}

// CompactedFrequencySort sorts a slice of items by frequency and removes duplicates.
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
