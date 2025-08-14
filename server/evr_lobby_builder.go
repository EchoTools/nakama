package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"math"
	"math/rand"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/gofrs/uuid/v5"
	"github.com/heroiclabs/nakama-common/api"
	"github.com/heroiclabs/nakama-common/runtime"
	"github.com/heroiclabs/nakama/v3/server/evr"
	"go.uber.org/zap"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Builds the match after the matchmaker has created it
type LobbyBuilder struct {
	sync.Mutex
	logger *zap.Logger
	nk     runtime.NakamaModule

	sessionRegistry SessionRegistry
	matchRegistry   MatchRegistry
	tracker         Tracker
	metrics         Metrics

	mapQueue map[evr.Symbol][]evr.Symbol // map[mode][]level
}

func NewLobbyBuilder(logger *zap.Logger, nk runtime.NakamaModule, sessionRegistry SessionRegistry, matchRegistry MatchRegistry, tracker Tracker, metrics Metrics) *LobbyBuilder {
	logger = logger.With(zap.String("module", "lobby_builder"))

	return &LobbyBuilder{
		logger: logger,
		nk:     nk,

		sessionRegistry: sessionRegistry,
		matchRegistry:   matchRegistry,
		tracker:         tracker,
		metrics:         metrics,

		mapQueue: make(map[evr.Symbol][]evr.Symbol),
	}
}

func (b *LobbyBuilder) handleMatchedEntries(entries [][]*MatchmakerEntry) {
	// build matches one at a time.
	for _, entrants := range entries {
		if _, err := b.buildMatch(b.logger, entrants); err != nil {
			b.logger.With(zap.Any("entries", entries)).Error("Failed to build match", zap.Error(err))
			return
		}
	}
}

func (b *LobbyBuilder) extractLatenciesFromEntrants(entrants []*MatchmakerEntry) (map[string][][]float64, map[string]map[string]float64) {
	latenciesByTeamByExtIP := make(map[string][][]float64, 100)
	latenciesByPlayerByExtIP := make(map[string]map[string]float64, 100)

	for _, e := range entrants {

		// loop over the number props and get the latencies
		for k, v := range e.GetProperties() {
			if extIP, ok := strings.CutPrefix(k, RTTPropertyPrefix); ok {
				latenciesByTeamByExtIP[extIP] = append(latenciesByTeamByExtIP[extIP], []float64{v.(float64)})

				if _, ok := latenciesByPlayerByExtIP[extIP]; !ok {
					latenciesByPlayerByExtIP[extIP] = make(map[string]float64, 8)
				}

				latenciesByPlayerByExtIP[extIP][e.Presence.UserId] = v.(float64)
			}
		}
	}

	return latenciesByTeamByExtIP, latenciesByPlayerByExtIP
}

// SortGameServerIPs sorts the game server IPs by latency, returning a slice of external IP addresses
func (b *LobbyBuilder) rankEndpointsByAverageLatency(entrants []*MatchmakerEntry) (map[string]int, map[string]map[string]float64) {

	latenciesByTeamByExtIP, latenciesByPlayerByExtIP := b.extractLatenciesFromEntrants(entrants)

	meanRTTByExtIP := make(map[string]int, len(latenciesByTeamByExtIP))

	for extIP, latenciesByTeam := range latenciesByTeamByExtIP {
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

	return meanRTTByExtIP, latenciesByPlayerByExtIP
}

// SortGameServerIPs sorts the game server IPs by latency, returning a slice of external IP addresses
func (b *LobbyBuilder) rankEndpointsByServerScore(entrants []*MatchmakerEntry) []string {

	latenciesByTeamByExtIP, _ := b.extractLatenciesFromEntrants(entrants)

	scoresByExtIP := make(map[string]float64, len(latenciesByTeamByExtIP))

	for extIP, latenciesByTeam := range latenciesByTeamByExtIP {
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

func (b *LobbyBuilder) buildMatch(logger *zap.Logger, entrants []*MatchmakerEntry) (matchID *MatchID, err error) {
	// Build matches one at a time.

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	logger = logger.With(zap.Int("entrants", len(entrants)))

	logger.Debug("Building match", zap.Any("entrants", entrants))

	if len(entrants) < 2 {
		return nil, fmt.Errorf("not enough entrants to build a match")
	}

	groupID, err := b.groupIDFromEntrants(entrants)

	// Divide the entrants into two equal-sized teams
	teamSize := len(entrants) / 2
	teams := [2][]*MatchmakerEntry{}
	for i, e := range entrants {
		teams[i/teamSize] = append(teams[i/teamSize], e)
	}

	// Split the entrants into teams, half and half

	entrantPresences := make([]*EvrMatchPresence, 0, len(entrants))
	sessions := make([]Session, 0, len(entrants))
	for teamIndex, players := range teams {

		// Assign each player in the team to the match
		for _, entry := range players {

			session := b.sessionRegistry.Get(uuid.FromStringOrNil(entry.Presence.GetSessionId()))
			if session == nil {
				logger.Warn("Failed to get session from session registry", zap.String("sid", entry.Presence.GetSessionId()))
				continue
			}

			mu := entry.NumericProperties["rating_mu"]
			sigma := entry.NumericProperties["rating_sigma"]
			rating := NewRating(0, mu, sigma)
			percentile, ok := entry.NumericProperties["rank_percentile"]
			if !ok {
				percentile = 0.0
			}

			query, ok := entry.StringProperties["query"]
			if !ok {
				query = ""
			}

			sessions = append(sessions, session)

			if entrant, err := EntrantPresenceFromSession(session, MatchIDFromStringOrNil(entry.GetPartyId()).UUID, teamIndex, rating, percentile, groupID.String(), 0, query); err != nil {
				logger.Error("Failed to create entrant presence", zap.String("sid", session.ID().String()), zap.Error(err))
				continue
			} else {
				entrantPresences = append(entrantPresences, entrant)
			}
		}
	}

	// gameServers := b.rankEndpointsByServerScore(entrants)
	meanRTTByExtIP, latenciesByPlayerByExtIP := b.rankEndpointsByAverageLatency(entrants)

	modestr, ok := entrants[0].StringProperties["game_mode"]
	if !ok {
		return nil, fmt.Errorf("missing mode property")
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
	queryAddon := ServiceSettings().Matchmaking.QueryAddons.LobbyBuilder
	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-timeout:
			return nil, ErrMatchmakingNoAvailableServers
		default:
		}
		label, err = LobbyGameServerAllocate(ctx, NewRuntimeGoLogger(logger), b.nk, []string{groupID.String()}, meanRTTByExtIP, settings, nil, true, false, queryAddon)
		if err != nil || label == nil {
			logger.Error("Failed to allocate game server.", zap.Error(err))
			<-time.After(5 * time.Second)
			continue
		}
		break
	}

	// Update the entrant ping to the game server
	for _, p := range entrantPresences {
		p.PingMillis = int(latenciesByPlayerByExtIP[label.GameServer.Endpoint.ExternalIP.String()][p.GetUserId()])
	}

	serverSession := b.sessionRegistry.Get(label.GameServer.SessionID)
	if serverSession == nil {
		return nil, fmt.Errorf("failed to get server session")
	}

	successful := make([]*EvrMatchPresence, 0, len(entrants))
	errored := make([]*EvrMatchPresence, 0, len(entrants))
	wg := &sync.WaitGroup{}
	wg.Add(len(entrantPresences))

	erroredCh := make(chan *EvrMatchPresence, len(entrantPresences))
	succeededCh := make(chan *EvrMatchPresence, len(entrantPresences))

	for i, p := range entrantPresences {
		go func(session Session, p *EvrMatchPresence) {
			defer wg.Done()
			if p == nil {
				return
			}

			if err := LobbyJoinEntrants(logger, b.matchRegistry, b.tracker, session, serverSession, label, p); err != nil {
				logger.Error("Failed to join entrant to match", zap.String("mid", label.ID.UUID.String()), zap.String("uid", p.GetUserId()), zap.Error(err))
				erroredCh <- p
				return
			}

			succeededCh <- p

		}(sessions[i], p)
	}

	wg.Wait()

	close(erroredCh)
	close(succeededCh)

	for p := range erroredCh {
		errored = append(errored, p)
	}

	for p := range succeededCh {
		successful = append(successful, p)
	}

	tags := map[string]string{
		"mode":    label.Mode.String(),
		"level":   label.Level.String(),
		"groupID": label.GetGroupID().String(),
	}
	b.metrics.CustomCounter("lobby_join_match_made", tags, int64(len(successful)))
	b.metrics.CustomCounter("lobby_error_match_made", tags, int64(len(errored)))

	logger.Info("Match built.", zap.String("mid", label.ID.UUID.String()), zap.Any("teams", teams), zap.Any("successful", successful), zap.Any("errored", errored), zap.Any("game_server", label.GameServer))
	return &label.ID, nil
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
		k := label.GameServer.Endpoint.ExternalIP.String()
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

func rttByPlayerByExtIP(ctx context.Context, logger *zap.Logger, db *sql.DB, nk runtime.NakamaModule, groupID string) (map[string]map[string]int, error) {
	qparts := []string{
		fmt.Sprintf("+label.broadcaster.group_ids:/(%s)/", Query.QuoteStringValue(groupID)),
	}

	query := strings.Join(qparts, " ")

	pubLabels, err := LobbyGameServerList(ctx, nk, query)
	if err != nil {
		return nil, err
	}

	rttByPlayerByExtIP := make(map[string]map[string]int)

	totalPlayers := 0
	for _, label := range pubLabels {
		for _, p := range label.Players {
			history := NewLatencyHistory()
			if err := StorableRead(ctx, nk, p.UserID, history, true); err != nil && status.Code(err) != codes.NotFound {
				logger.Warn("Failed to load latency history", zap.Error(err))
				continue
			}

			rtts := history.LatestRTTs()
			if len(rtts) > 0 {
				totalPlayers++
			}

			for extIP, rtt := range rtts {
				if _, ok := rttByPlayerByExtIP[p.UserID]; !ok {
					rttByPlayerByExtIP[p.UserID] = make(map[string]int)
				}
				rttByPlayerByExtIP[p.UserID][extIP] = rtt
			}
		}
	}

	return rttByPlayerByExtIP, nil
}

// Wrapper for the matchRegistry.ListMatches function.
func LobbyList(ctx context.Context, nk runtime.NakamaModule, limit int, minSize int, maxSize int, query string) ([]*api.Match, error) {
	return nk.MatchList(ctx, limit, true, "", &minSize, &maxSize, query)
}

func LobbyGameServerList(ctx context.Context, nk runtime.NakamaModule, query string) ([]*MatchLabel, error) {
	limit := 200
	minSize, maxSize := 1, MatchLobbyMaxSize // the game server counts as one.
	matches, err := LobbyList(ctx, nk, limit, minSize, maxSize, query)
	if err != nil {
		return nil, fmt.Errorf("failed to list matches: %w", err)
	}

	if len(matches) == 0 {
		return nil, ErrMatchmakingNoAvailableServers
	}

	labels := make([]*MatchLabel, 0, len(matches))
	for _, match := range matches {
		label := &MatchLabel{}
		if err := json.Unmarshal([]byte(match.GetLabel().GetValue()), label); err != nil {
			return nil, fmt.Errorf("failed to unmarshal match label: %w", err)
		}
		labels = append(labels, label)
	}
	return labels, nil
}

func LobbyGameServerAllocate(ctx context.Context, logger runtime.Logger, nk runtime.NakamaModule, groupIDs []string, rttsByExternalIP map[string]int, settings *MatchSettings, regions []string, requireDefaultRegion bool, requireRegion bool, queryAddon string) (*MatchLabel, error) {

	if len(groupIDs) == 0 {
		return nil, fmt.Errorf("no group IDs provided")
	}
	// Load the server ratings storage object
	globalSettings := ServiceSettings().Matchmaking

	qparts := []string{
		fmt.Sprintf("+label.broadcaster.group_ids:%s", Query.CreateMatchPattern(groupIDs)),
		queryAddon,
	}

	if requireDefaultRegion {
		qparts = append(qparts, "+label.broadcaster.region_codes:/(default)/")
	}

	if len(regions) > 0 {
		prefix := ""
		if requireRegion {
			prefix = "+"
		}

		qparts = append(qparts, fmt.Sprintf("%slabel.broadcaster.region_codes:%s", prefix, Query.CreateMatchPattern(regions)))
	}

	query := strings.Join(qparts, " ")

	// Create a set of regions for fast lookup
	regionSet := make(map[string]struct{}, len(regions))
	for _, region := range regions {
		regionSet[region] = struct{}{}
	}

	// Get the list of matches
	matches, err := nk.MatchList(ctx, 100, true, "", nil, nil, query)
	if err != nil {
		return nil, fmt.Errorf("failed to find matches: %w", err)
	}

	if len(matches) == 0 {
		return nil, ErrMatchmakingNoAvailableServers
	}

	// Create a slice containing the match labels
	var (
		availableServers    = make([]*MatchLabel, 0, len(matches))
		activeCountByHostID = make(map[string]int, len(matches))
	)
	for _, match := range matches {
		label := &MatchLabel{}
		if err := json.Unmarshal([]byte(match.GetLabel().GetValue()), label); err != nil {
			return nil, fmt.Errorf("failed to unmarshal match label: %w", err)
		}
		if label.GameServer == nil {
			continue
		}

		if label.LobbyType == UnassignedLobby {
			availableServers = append(availableServers, label)
		} else {
			activeCountByHostID[label.GameServer.Endpoint.GetHostID()]++
		}
	}

	if len(availableServers) == 0 {
		return nil, ErrMatchmakingNoAvailableServers
	}

	indexes := make([]labelIndex, len(availableServers))
	for i, label := range availableServers {
		extIP := label.GameServer.Endpoint.ExternalIP.String()
		hostID := label.GameServer.Endpoint.GetHostID()
		regionMatch := false
		for _, region := range label.GameServer.RegionCodes {
			if region == "default" {
				continue
			}
			if _, ok := regionSet[region]; ok {
				regionMatch = true
			}
		}

		rating, ok := globalSettings.ServerRatings.ByExternalIP[extIP]
		if !ok {
			if rating, ok = globalSettings.ServerRatings.ByOperatorUsername[label.GameServer.Username]; !ok {
				rating = 0
			}
		}

		// Skip servers with a rating of -999
		if rating == -999 {
			continue
		}

		indexes[i] = labelIndex{
			Label:             label,
			RTT:               (rttsByExternalIP[extIP] + 10) / 20 * 20,
			IsReachable:       rttsByExternalIP[extIP] != 0,
			Rating:            rating,
			IsPriorityForMode: slices.Contains(label.GameServer.DesignatedModes, settings.Mode),
			ActiveCount:       activeCountByHostID[hostID],
			IsRegionMatch:     regionMatch,
			IsHighLatency:     rttsByExternalIP[extIP] > 100,
		}
	}

	sortLabelIndexes(indexes)

	// Find the first available game server
	var label *MatchLabel
	for _, index := range indexes {
		if index.Label.LobbyType != UnassignedLobby {
			continue
		}

		label, err = LobbyPrepareSession(ctx, nk, index.Label.ID, settings)
		if err != nil {
			logger.WithFields(map[string]any{
				"mid": index.Label.ID.UUID.String(),
				"err": err,
			}).Warn("Failed to prepare session")
			continue
		}

		return label, nil
	}

	return nil, ErrMatchmakingNoAvailableServers
}

type labelIndex struct {
	Label             *MatchLabel
	RTT               int
	IsReachable       bool
	Rating            float64
	IsHighLatency     bool
	IsPriorityForMode bool
	ActiveCount       int
	IsRegionMatch     bool
}

func sortLabelIndexes(labels []labelIndex) {
	// Sort the labels
	slices.SortStableFunc(labels, func(a, b labelIndex) int {

		// Sort by whether the server is reachable or not
		if a.IsReachable && !b.IsReachable {
			return -1
		}

		if !a.IsReachable && b.IsReachable {
			return 1
		}

		// Sort by whether the server is in the region
		if a.IsRegionMatch && !b.IsRegionMatch {
			return -1
		}
		if !a.IsRegionMatch && b.IsRegionMatch {
			return 1
		}

		// Sort by whether the server high latency
		if !a.IsHighLatency && b.IsHighLatency {
			return -1
		} else if a.IsHighLatency && !b.IsHighLatency {
			return 1
		}

		// Sort by the server rating
		if a.Rating > b.Rating {
			return -1
		}

		if a.Rating < b.Rating {
			return 1
		}

		// Sort by whether the server is a priority server
		if a.IsPriorityForMode && !b.IsPriorityForMode {
			return -1
		}
		if !a.IsPriorityForMode && b.IsPriorityForMode {
			return 1
		}

		// If there is a large difference in RTT, sort by RTT
		if math.Abs(float64(a.RTT-b.RTT)) > 50 {
			if a.RTT < b.RTT {
				return -1
			}

			if a.RTT > b.RTT {
				return 1
			}
		}

		// Sort by the number of active game servers
		if a.ActiveCount < b.ActiveCount {
			return -1
		}

		if a.ActiveCount > b.ActiveCount {
			return 1
		}

		return 0
	})
}
