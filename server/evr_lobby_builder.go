package server

import (
	"context"
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
)

const (
	// LobbyBuilder constants
	DefaultLatencyMapCapacity      = 100
	DefaultTeamMapCapacity         = 8
	MinEntrantsForMatch            = 2
	ReservationLifetimeSeconds     = 20
	ServerAllocationTimeoutSeconds = 60
	ServerAllocationRetrySeconds   = 5
	MatchListLimit                 = 200
	MatchListMinSize               = 1
	HighLatencyThresholdMs         = 100
	RTTRoundingInterval            = 20
	RTTRoundingOffset              = 10
	RTTSignificantDifferenceMs     = 50
	ServerRatingExcludeValue       = -999
	MatchAllocateLimit             = 100
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

	mapQueue          map[evr.Symbol][]evr.Symbol // map[mode][]level
	postMatchBackfill *PostMatchmakerBackfill
	skillBasedMM      *SkillBasedMatchmaker

	// Backfill coordination
	backfillStopCh  chan struct{} // Channel to stop periodic backfill
	backfillResetCh chan struct{} // Channel to reset the periodic backfill timer
	backfillMu      sync.Mutex
}

func NewLobbyBuilder(logger *zap.Logger, nk runtime.NakamaModule, sessionRegistry SessionRegistry, matchRegistry MatchRegistry, tracker Tracker, metrics Metrics) *LobbyBuilder {
	logger = logger.With(zap.String("module", "lobby_builder"))

	lb := &LobbyBuilder{
		logger: logger,
		nk:     nk,

		sessionRegistry: sessionRegistry,
		matchRegistry:   matchRegistry,
		tracker:         tracker,
		metrics:         metrics,

		mapQueue: make(map[evr.Symbol][]evr.Symbol),
	}

	// Create the post-matchmaker backfill handler
	lb.postMatchBackfill = NewPostMatchmakerBackfill(logger, nk, sessionRegistry, matchRegistry, tracker, metrics)

	return lb
}

// SetSkillBasedMatchmaker sets the skill-based matchmaker reference for accessing matchmaker results
func (b *LobbyBuilder) SetSkillBasedMatchmaker(sbmm *SkillBasedMatchmaker) {
	b.skillBasedMM = sbmm
	// Always start periodic backfill - it will check the setting on each run
	b.startPeriodicBackfill()
}

func (b *LobbyBuilder) handleMatchedEntries(entries [][]*MatchmakerEntry) {
	// build matches one at a time.
	for _, entrants := range entries {
		if _, err := b.buildMatch(b.logger, entrants); err != nil {
			b.logger.With(zap.Any("entries", entries)).Error("Failed to build match", zap.Error(err))
			return
		}
	}

	// After building matches, attempt post-matchmaker backfill if enabled
	// This runs immediately after matchmaker completes in addition to periodic backfill
	if ServiceSettings().Matchmaking.EnablePostMatchmakerBackfill && b.skillBasedMM != nil {
		// Reset the periodic timer since matchmaker just ran and will trigger backfill
		b.resetBackfillTimer()
		// Run backfill immediately after matchmaker (not a periodic run)
		go b.runPostMatchmakerBackfill(false)
	}
}

// startPeriodicBackfill starts a goroutine that periodically runs backfill
// This ensures backfill happens even when matchmaker doesn't have enough players to form matches
func (b *LobbyBuilder) startPeriodicBackfill() {
	// Check matchmaker first before creating channels to avoid leaking them
	matchmaker := globalMatchmaker.Load()
	if matchmaker == nil {
		b.logger.Warn("Global matchmaker not initialized, periodic backfill not started")
		return
	}

	b.backfillMu.Lock()
	// Stop any existing periodic backfill
	if b.backfillStopCh != nil {
		close(b.backfillStopCh)
	}
	b.backfillStopCh = make(chan struct{})
	b.backfillResetCh = make(chan struct{}, 1) // Buffered to avoid blocking
	stopCh := b.backfillStopCh
	resetCh := b.backfillResetCh
	b.backfillMu.Unlock()
	intervalSecs := matchmaker.config.GetMatchmaker().IntervalSec
	interval := time.Duration(intervalSecs) * time.Second

	b.logger.Info("Starting periodic backfill", zap.Duration("interval", interval))

	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()

		for {
			select {
			case <-stopCh:
				b.logger.Info("Stopping periodic backfill")
				return
			case <-resetCh:
				// Reset the ticker when backfill runs (from any trigger)
				ticker.Reset(interval)
			case <-ticker.C:
				// Check if backfill is enabled (allows runtime config changes)
				if !ServiceSettings().Matchmaking.EnablePostMatchmakerBackfill {
					continue
				}
				// Reset timer at the start so it doesn't pile up
				ticker.Reset(interval)
				// Run periodic backfill (isPeriodicRun=true filters to older tickets)
				b.runPostMatchmakerBackfill(true)
			}
		}
	}()
}

// resetBackfillTimer resets the periodic backfill timer
// Call this after backfill runs to avoid running again too soon
func (b *LobbyBuilder) resetBackfillTimer() {
	b.backfillMu.Lock()
	defer b.backfillMu.Unlock()
	if b.backfillResetCh != nil {
		select {
		case b.backfillResetCh <- struct{}{}:
		default:
			// Channel full, reset already pending
		}
	}
}

// runPostMatchmakerBackfill runs the post-matchmaker backfill process
// isPeriodicRun indicates this is a periodic run (not triggered by matchmaker completion)
// When isPeriodicRun is true, we filter to only process tickets that have been waiting
// for at least BackfillMinTicketAgeSecs to avoid poaching fresh tickets before matchmaker runs
func (b *LobbyBuilder) runPostMatchmakerBackfill(isPeriodicRun bool) {
	ctx, cancel := context.WithTimeout(context.Background(), BackfillProcessTimeout)
	defer cancel()

	logger := b.logger.With(
		zap.String("operation", "post_matchmaker_backfill"),
		zap.Bool("is_periodic_run", isPeriodicRun),
	)

	// If matchmaker is currently processing, skip this backfill run to avoid poaching
	if b.skillBasedMM != nil && b.skillBasedMM.IsProcessing() {
		logger.Debug("Matchmaker is currently processing, skipping backfill")
		return
	}

	// Get current tickets from the global matchmaker
	matchmaker := globalMatchmaker.Load()
	if matchmaker == nil {
		logger.Debug("Global matchmaker not initialized")
		return
	}

	extracts := matchmaker.Extract()
	if len(extracts) == 0 {
		return
	}

	logger.Debug("Got matchmaker tickets", zap.Int("count", len(extracts)))

	// Convert extracts to BackfillCandidates
	candidates := b.postMatchBackfill.ExtractCandidatesFromMatchmaker(extracts)
	if len(candidates) == 0 {
		logger.Debug("No valid candidates from matchmaker tickets")
		return
	}

	// For periodic runs, filter to only tickets that have been waiting at least 1 matchmaker interval
	// This prevents backfill from "poaching" tickets before matchmaker has a chance to process them
	if isPeriodicRun {
		// Minimum age is 1 matchmaker interval (use time-based fallback if matchmaker hasn't run yet)
		minAge := time.Duration(matchmaker.config.GetMatchmaker().IntervalSec) * time.Second

		filteredCandidates := make([]*BackfillCandidate, 0, len(candidates))
		for _, c := range candidates {
			// Accept candidate if either:
			// 1. Matchmaker has seen it at least once (Intervals >= 1), OR
			// 2. It's been waiting long enough (time-based fallback for when matchmaker never completes)
			ticketAge := time.Since(c.SubmissionTime)
			if c.Intervals >= 1 || ticketAge >= minAge {
				filteredCandidates = append(filteredCandidates, c)
			}
		}

		logger.Debug("Filtered candidates for periodic backfill",
			zap.Int("original_count", len(candidates)),
			zap.Int("filtered_count", len(filteredCandidates)),
			zap.Duration("min_age", minAge),
		)

		candidates = filteredCandidates

		if len(candidates) == 0 {
			logger.Debug("No candidates old enough for periodic backfill")
			return
		}
	}

	logger.Info("Running post-matchmaker backfill",
		zap.Int("candidates", len(candidates)))

	// Calculate reducing precision factor based on oldest ticket
	settings := ServiceSettings().Matchmaking
	reducingPrecisionFactor := 0.0

	if settings.ReducingPrecisionIntervalSecs > 0 {
		oldestSubmission := time.Now()
		for _, c := range candidates {
			if c.SubmissionTime.Before(oldestSubmission) {
				oldestSubmission = c.SubmissionTime
			}
		}

		waitTime := time.Since(oldestSubmission)
		interval := time.Duration(settings.ReducingPrecisionIntervalSecs) * time.Second
		maxCycles := float64(settings.ReducingPrecisionMaxCycles)

		if maxCycles > 0 && interval > 0 {
			cycles := float64(waitTime) / float64(interval)
			reducingPrecisionFactor = min(cycles/maxCycles, 1.0)
		}
	}

	logger.Debug("Reducing precision factor calculated",
		zap.Float64("factor", reducingPrecisionFactor))

	// Process and execute backfill (combined to ensure accurate slot tracking)
	results, err := b.postMatchBackfill.ProcessAndExecuteBackfill(ctx, logger, candidates, reducingPrecisionFactor)
	if err != nil {
		if ctx.Err() != nil {
			logger.Warn("Post-matchmaker backfill timed out or cancelled", zap.Error(err), zap.Int("partial_results", len(results)))
		} else {
			logger.Error("Failed to process post-matchmaker backfill", zap.Error(err))
		}
		// Still report partial results if any succeeded before timeout
		if len(results) > 0 {
			logger.Info("Partially backfilled players before timeout/error", zap.Int("count", len(results)))
		}
		return
	}

	if len(results) == 0 {
		logger.Debug("No backfill matches found")
		return
	}

	logger.Info("Successfully backfilled players", zap.Int("count", len(results)))
}

func (b *LobbyBuilder) extractLatenciesFromEntrants(entrants []*MatchmakerEntry) (map[string][][]float64, map[string]map[string]float64) {
	latenciesByTeamByExtIP := make(map[string][][]float64, DefaultLatencyMapCapacity)
	latenciesByPlayerByExtIP := make(map[string]map[string]float64, DefaultLatencyMapCapacity)

	for _, e := range entrants {

		// loop over the number props and get the latencies
		for k, v := range e.GetProperties() {
			if extIP, ok := strings.CutPrefix(k, RTTPropertyPrefix); ok {
				latenciesByTeamByExtIP[extIP] = append(latenciesByTeamByExtIP[extIP], []float64{v.(float64)})

				if _, ok := latenciesByPlayerByExtIP[extIP]; !ok {
					latenciesByPlayerByExtIP[extIP] = make(map[string]float64, DefaultTeamMapCapacity)
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
	partyMap := make(map[string][]*MatchmakerEntry, DefaultTeamMapCapacity)
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

	if len(entrants) < MinEntrantsForMatch {
		return nil, fmt.Errorf("not enough entrants to build a match")
	}

	groupID, err := b.groupIDFromEntrants(entrants)
	if err != nil {
		return nil, fmt.Errorf("failed to determine group ID from entrants: %w", err)
	}

	// Divide the entrants into two equal-sized teams
	// The matchmaker returns balanced teams in the candidate array:
	// - First half (indices 0 : len(entrants)/2) = Team 0 (Blue)
	// - Second half (indices len(entrants)/2 : len(entrants)) = Team 1 (Orange)
	// We must preserve this assignment, not re-split arbitrarily.
	teamSize := len(entrants) / 2
	if teamSize*2 != len(entrants) {
		return nil, fmt.Errorf("entrants count must be even for team splitting, got %d", len(entrants))
	}

	teams := [2][]*MatchmakerEntry{
		entrants[:teamSize], // Blue team (first half)
		entrants[teamSize:], // Orange team (second half)
	}

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

			query, ok := entry.StringProperties["query"]
			if !ok {
				query = ""
			}

			sessions = append(sessions, session)

			if entrant, err := EntrantPresenceFromSession(session, MatchIDFromStringOrNil(entry.GetPartyId()).UUID, teamIndex, rating, groupID.String(), 0, query); err != nil {
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
		ReservationLifetime: ReservationLifetimeSeconds * time.Second,
		StartTime:           time.Now().UTC(),
	}

	var label *MatchLabel
	timeout := time.After(ServerAllocationTimeoutSeconds * time.Second)
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
			<-time.After(ServerAllocationRetrySeconds * time.Second)
			continue
		}
		break
	}

	// Update the entrant ping to the game server
	endpointID := EncodeEndpointID(label.GameServer.Endpoint.ExternalIP.String())
	for _, p := range entrantPresences {
		if endpointLatencies, ok := latenciesByPlayerByExtIP[endpointID]; ok && endpointLatencies != nil {
			if latency, ok := endpointLatencies[p.GetUserId()]; ok {
				p.PingMillis = int(latency)
			}
		}
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

// Wrapper for the matchRegistry.ListMatches function.
func LobbyList(ctx context.Context, nk runtime.NakamaModule, limit int, minSize int, maxSize int, query string) ([]*api.Match, error) {
	return nk.MatchList(ctx, limit, true, "", &minSize, &maxSize, query)
}

func LobbyGameServerList(ctx context.Context, nk runtime.NakamaModule, query string) ([]*MatchLabel, error) {
	limit := MatchListLimit
	minSize, maxSize := MatchListMinSize, MatchLobbyMaxSize // the game server counts as one.
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

func LobbyGameServerAllocate(ctx context.Context, logger runtime.Logger, nk runtime.NakamaModule, groupIDs []string, rttsByEndpoint map[string]int, settings *MatchSettings, regions []string, requireDefaultRegion bool, requireRegion bool, queryAddon string) (*MatchLabel, error) {

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
	matches, err := nk.MatchList(ctx, MatchAllocateLimit, true, "", nil, nil, query)
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

	indexes := make([]labelIndex, 0, len(availableServers))
	for _, label := range availableServers {
		extIP := label.GameServer.Endpoint.ExternalIP.String()
		hostID := label.GameServer.Endpoint.GetHostID()

		if slices.Contains(globalSettings.ServerSelection.ExcludeList, label.GameServer.Username) || slices.Contains(globalSettings.ServerSelection.ExcludeList, extIP) {
			continue
		}

		regionMatch := false
		for _, region := range label.GameServer.RegionCodes {
			if region == RegionDefault {
				continue
			}
			if _, ok := regionSet[region]; ok {
				regionMatch = true
			}
		}

		rating, ok := globalSettings.ServerSelection.Ratings[extIP]
		if !ok {
			if rating, ok = globalSettings.ServerSelection.Ratings[label.GameServer.Username]; !ok {
				rating = 0
			}
		}

		// Skip servers with a rating of ServerRatingExcludeValue
		if rating == ServerRatingExcludeValue {
			continue
		}

		// Look up RTT using both raw IP and encoded endpoint ID for compatibility
		// (matchmaking properties use encoded IDs, direct client data uses raw IPs)
		endpointID := EncodeEndpointID(extIP)
		rtt := rttsByEndpoint[endpointID]
		if rtt == 0 {
			rtt = rttsByEndpoint[extIP]
		}
		if delta, ok := globalSettings.ServerSelection.RTTDelta[label.GameServer.Username]; ok {
			rtt += delta
		}
		if delta, ok := globalSettings.ServerSelection.RTTDelta[extIP]; ok {
			rtt += delta
		}

		isReachable := rttsByEndpoint[endpointID] != 0 || rttsByEndpoint[extIP] != 0
		isHighLatency := rtt > HighLatencyThresholdMs

		indexes = append(indexes, labelIndex{
			Label:             label,
			RTT:               (rtt + RTTRoundingOffset) / RTTRoundingInterval * RTTRoundingInterval,
			IsReachable:       isReachable,
			Rating:            rating,
			IsPriorityForMode: slices.Contains(label.GameServer.DesignatedModes, settings.Mode),
			ActiveCount:       activeCountByHostID[hostID],
			IsRegionMatch:     regionMatch,
			IsHighLatency:     isHighLatency,
		})
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
		if math.Abs(float64(a.RTT-b.RTT)) > RTTSignificantDifferenceMs {
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
