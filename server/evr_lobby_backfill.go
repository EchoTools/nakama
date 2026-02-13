package server

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/gofrs/uuid/v5"
	"github.com/heroiclabs/nakama-common/runtime"
	"github.com/heroiclabs/nakama/v3/server/evr"
	"go.uber.org/zap"
)

// Backfill scoring constants
const (
	// BackfillBaseScore is the starting score for backfill matching
	BackfillBaseScore = 100.0
	// BackfillRTTExceedsPenaltyMultiplier is applied when RTT exceeds max allowed
	BackfillRTTExceedsPenaltyMultiplier = 0.5
	// BackfillNoRTTPenalty is applied when no RTT data is available
	BackfillNoRTTPenalty = 50.0
	// BackfillMatchAgeThreshold is when match age starts affecting score
	BackfillMatchAgeThreshold = 2 * time.Minute
	// BackfillMatchAgePenaltyPerMinute is the penalty per minute of match age
	BackfillMatchAgePenaltyPerMinute = 2.0
	// BackfillMinAcceptableScore is the minimum score for a backfill to be accepted
	BackfillMinAcceptableScore = 0.0
	// BackfillProcessTimeout is the maximum time for processing backfill
	BackfillProcessTimeout = 30 * time.Second
	// BackfillTeamBalanceBonus is the bonus for assignments that improve team balance
	BackfillTeamBalanceBonus = 10.0
	// BackfillTeamImbalancePenalty is the penalty for assignments that worsen team balance
	BackfillTeamImbalancePenalty = 5.0
	// BackfillRatingScoreWeight is the weight for rating-based scoring
	BackfillRatingScoreWeight = 30.0
	// BackfillRTTScoreWeight is the weight for RTT-based scoring
	BackfillRTTScoreWeight = 20.0
	// BackfillPopulationBonus is the bonus per player in the match
	BackfillPopulationBonus = 2.0
)

// BackfillCandidate represents a ticket that needs to be backfilled into an existing match
type BackfillCandidate struct {
	Ticket         string
	Entries        []*MatchmakerEntry
	GroupID        uuid.UUID
	Mode           evr.Symbol
	Rating         float64
	SubmissionTime time.Time
	RTTs           map[string]int // map[externalIP]rtt
	MaxRTT         int
	TeamAlignment  int // Team assignment from matchmaker ticket
	Intervals      int
}

// BackfillMatch represents a match that can accept backfill candidates
type BackfillMatch struct {
	Label      *MatchLabel
	OpenSlots  map[int]int // map[teamIndex]openSlots
	TeamCounts map[int]int // map[teamIndex]playerCount - tracks pending backfills
	RTTs       map[string]int
}

// BackfillResult represents the result of a backfill attempt
type BackfillResult struct {
	Candidate     *BackfillCandidate
	Match         *BackfillMatch
	Team          int
	Score         float64
	PlayerUserIDs []string // Track user IDs of players successfully backfilled
}

// backfillContext holds pre-computed values for a backfill processing cycle
// This avoids repeatedly calling ServiceSettings() and other expensive lookups
type backfillContext struct {
	settings                SkillBasedMatchmakingConfig
	now                     time.Time
	reducingPrecisionFactor float64
}

// preparedBackfillMatch holds pre-computed match metadata for scoring
type preparedBackfillMatch struct {
	*BackfillMatch
	externalIP string
	matchAge   time.Duration
}

// preparedBackfillCandidate holds pre-computed candidate data including resolved sessions
type preparedBackfillCandidate struct {
	*BackfillCandidate
	sessions  []Session
	partySize int
}

// prepareMatches pre-computes match metadata that doesn't change during processing
func (b *PostMatchmakerBackfill) prepareMatches(matches []*BackfillMatch, bctx *backfillContext) []*preparedBackfillMatch {
	prepared := make([]*preparedBackfillMatch, len(matches))
	for i, m := range matches {
		prepared[i] = &preparedBackfillMatch{
			BackfillMatch: m,
			externalIP:    m.Label.GameServer.Endpoint.GetExternalIP(),
			matchAge:      bctx.now.Sub(m.Label.StartTime),
		}
	}
	return prepared
}

// prepareCandidates pre-resolves sessions for all candidates upfront
func (b *PostMatchmakerBackfill) prepareCandidates(candidates []*BackfillCandidate) []*preparedBackfillCandidate {
	prepared := make([]*preparedBackfillCandidate, 0, len(candidates))
	for _, c := range candidates {
		sessions := make([]Session, 0, len(c.Entries))
		for _, entry := range c.Entries {
			sessionID := uuid.FromStringOrNil(entry.Presence.GetSessionId())
			if session := b.sessionRegistry.Get(sessionID); session != nil {
				sessions = append(sessions, session)
			}
		}
		// Skip candidates with no valid sessions
		if len(sessions) == 0 {
			continue
		}
		prepared = append(prepared, &preparedBackfillCandidate{
			BackfillCandidate: c,
			sessions:          sessions,
			partySize:         len(c.Entries),
		})
	}
	return prepared
}

// calculateBackfillScoreOptimized calculates score using pre-computed values
func (b *PostMatchmakerBackfill) calculateBackfillScoreOptimized(
	candidate *preparedBackfillCandidate,
	match *preparedBackfillMatch,
	team int,
	bctx *backfillContext,
) float64 {
	score := BackfillBaseScore
	rpf := bctx.reducingPrecisionFactor

	// RTT scoring using pre-computed external IP
	rtt, hasRTT := candidate.RTTs[match.externalIP]
	if !hasRTT {
		score -= BackfillNoRTTPenalty * (1.0 - rpf)
	} else if rtt > candidate.MaxRTT {
		penalty := float64(rtt-candidate.MaxRTT) * BackfillRTTExceedsPenaltyMultiplier * (1.0 - rpf)
		score -= penalty
	} else if candidate.MaxRTT > 0 {
		score += (float64(candidate.MaxRTT-rtt) / float64(candidate.MaxRTT)) * BackfillRTTScoreWeight
	}

	// Rating scoring using pre-loaded settings
	if bctx.settings.EnableSBMM && candidate.Mode == evr.ModeArenaPublic {
		ratingDelta := math.Abs(match.Label.RatingMu - candidate.Rating)
		ratingRange := bctx.settings.RatingRange
		if ratingRange == 0 {
			ratingRange = 2.0
		}

		const reducingPrecisionRatingWeightScale = 0.5
		if ratingDelta <= ratingRange || rpf >= reducingPrecisionRatingWeightScale {
			ratingScore := (1.0 - ratingDelta/ratingRange) * BackfillRatingScoreWeight
			if ratingScore < 0 {
				ratingScore = 0
			}
			score += ratingScore * (1.0 - rpf*reducingPrecisionRatingWeightScale)
		} else {
			score -= BackfillRatingScoreWeight * (1.0 - rpf)
		}
	}

	// Population bonus
	score += float64(match.Label.PlayerCount) * BackfillPopulationBonus

	// Team balance scoring
	if team != evr.TeamSocial {
		blueCount := match.TeamCounts[evr.TeamBlue]
		orangeCount := match.TeamCounts[evr.TeamOrange]
		teamImbalance := math.Abs(float64(blueCount - orangeCount))

		if team == evr.TeamBlue && blueCount < orangeCount {
			score += BackfillTeamBalanceBonus
		} else if team == evr.TeamOrange && orangeCount < blueCount {
			score += BackfillTeamBalanceBonus
		} else if teamImbalance > 1 {
			score -= BackfillTeamImbalancePenalty * (1.0 - rpf)
		}
	}

	// Match age penalty using pre-computed age
	if match.matchAge > BackfillMatchAgeThreshold {
		agePenalty := match.matchAge.Minutes() * BackfillMatchAgePenaltyPerMinute * (1.0 - rpf)
		score -= agePenalty
	}

	return score
}

// findBestBackfillMatchOptimized finds best match using pre-computed data
func (b *PostMatchmakerBackfill) findBestBackfillMatchOptimized(
	candidate *preparedBackfillCandidate,
	matches []*preparedBackfillMatch,
	bctx *backfillContext,
) *BackfillResult {
	var bestResult *BackfillResult
	partySize := candidate.partySize

	for _, match := range matches {
		possibleTeams := b.getPossibleTeams(candidate.BackfillCandidate, match.BackfillMatch, partySize)
		if len(possibleTeams) == 0 {
			continue
		}

		for _, team := range possibleTeams {
			score := b.calculateBackfillScoreOptimized(candidate, match, team, bctx)
			if bestResult == nil || score > bestResult.Score {
				bestResult = &BackfillResult{
					Candidate: candidate.BackfillCandidate,
					Match:     match.BackfillMatch,
					Team:      team,
					Score:     score,
				}
			}
		}
	}

	return bestResult
}

// getPossibleTeams determines which teams can accept a candidate
func (b *PostMatchmakerBackfill) getPossibleTeams(candidate *BackfillCandidate, match *BackfillMatch, partySize int) []int {
	if candidate.Mode == evr.ModeSocialPublic {
		if match.OpenSlots[evr.TeamSocial] >= partySize {
			return []int{evr.TeamSocial}
		}
		return nil
	}

	// For competitive modes
	if candidate.TeamAlignment != evr.TeamUnassigned {
		if match.OpenSlots[candidate.TeamAlignment] >= partySize {
			return []int{candidate.TeamAlignment}
		}
		return nil
	}

	// No team preference - prioritize smaller team for balance
	blueCount := match.TeamCounts[evr.TeamBlue]
	orangeCount := match.TeamCounts[evr.TeamOrange]
	blueOpen := match.OpenSlots[evr.TeamBlue] >= partySize
	orangeOpen := match.OpenSlots[evr.TeamOrange] >= partySize

	var teams []int
	if blueCount < orangeCount && blueOpen {
		teams = append(teams, evr.TeamBlue)
	} else if orangeCount < blueCount && orangeOpen {
		teams = append(teams, evr.TeamOrange)
	} else {
		// Equal or preferred team full - add both available
		if blueOpen {
			teams = append(teams, evr.TeamBlue)
		}
		if orangeOpen {
			teams = append(teams, evr.TeamOrange)
		}
	}

	return teams
}

// executeBackfillResultOptimized executes backfill using pre-resolved sessions
func (b *PostMatchmakerBackfill) executeBackfillResultOptimized(
	ctx context.Context,
	logger *zap.Logger,
	result *BackfillResult,
	sessions []Session,
	groupID string,
) int {
	if result == nil || result.Match == nil || len(sessions) == 0 {
		return 0
	}

	// Get the server session
	serverSession := b.sessionRegistry.Get(result.Match.Label.GameServer.SessionID)
	if serverSession == nil {
		logger.Warn("Server session not found for backfill", zap.String("match_id", result.Match.Label.ID.String()))
		return 0
	}

	// Create entrant presences and join them concurrently
	type entrantData struct {
		session Session
		entrant *EvrMatchPresence
	}
	entrantsToJoin := make([]entrantData, 0, len(sessions))

	for i, session := range sessions {
		if i >= len(result.Candidate.Entries) {
			break
		}
		entry := result.Candidate.Entries[i]

		mu := entry.NumericProperties["rating_mu"]
		sigma := entry.NumericProperties["rating_sigma"]
		rating := NewRating(0, mu, sigma)

		entrant, err := EntrantPresenceFromSession(
			session,
			uuid.FromStringOrNil(entry.PartyId),
			result.Team,
			rating,
			groupID,
			0,
			"",
		)
		if err != nil {
			logger.Warn("Failed to create entrant presence for backfill",
				zap.Error(err),
				zap.String("session_id", session.ID().String()))
			continue
		}

		entrantsToJoin = append(entrantsToJoin, entrantData{session: session, entrant: entrant})
	}

	if len(entrantsToJoin) == 0 {
		return 0
	}

	var wg sync.WaitGroup
	var mu sync.Mutex
	successCount := 0
	successfulUserIDs := make([]string, 0, len(entrantsToJoin))

	for _, data := range entrantsToJoin {
		wg.Add(1)
		go func(sess Session, ent *EvrMatchPresence) {
			defer wg.Done()
			if err := LobbyJoinEntrants(logger, b.matchRegistry, b.tracker, sess, serverSession, result.Match.Label, ent); err != nil {
				logger.Warn("Failed to join entrant to backfill match",
					zap.Error(err),
					zap.String("match_id", result.Match.Label.ID.String()),
					zap.String("user_id", ent.GetUserId()))
			} else {
				mu.Lock()
				successCount++
				successfulUserIDs = append(successfulUserIDs, ent.GetUserId())
				mu.Unlock()
				b.metrics.CustomCounter("lobby_join_post_matchmaker_backfill", result.Match.Label.MetricsTags(), 1)
				logger.Info("Successfully backfilled player",
					zap.String("match_id", result.Match.Label.ID.String()),
					zap.String("user_id", ent.GetUserId()),
					zap.Int("team", result.Team),
					zap.Float64("score", result.Score))
			}
		}(data.session, data.entrant)
	}

	wg.Wait()

	// Store the successful user IDs in the result for summary logging
	result.PlayerUserIDs = successfulUserIDs

	return successCount
}

// PostMatchmakerBackfill handles backfilling players into existing matches after the matchmaker runs
type PostMatchmakerBackfill struct {
	logger          *zap.Logger
	nk              runtime.NakamaModule
	sessionRegistry SessionRegistry
	matchRegistry   MatchRegistry
	tracker         Tracker
	metrics         Metrics
}

// NewPostMatchmakerBackfill creates a new post-matchmaker backfill handler
func NewPostMatchmakerBackfill(logger *zap.Logger, nk runtime.NakamaModule, sessionRegistry SessionRegistry, matchRegistry MatchRegistry, tracker Tracker, metrics Metrics) *PostMatchmakerBackfill {
	return &PostMatchmakerBackfill{
		logger:          logger.With(zap.String("module", "post_matchmaker_backfill")),
		nk:              nk,
		sessionRegistry: sessionRegistry,
		matchRegistry:   matchRegistry,
		tracker:         tracker,
		metrics:         metrics,
	}
}

// ExtractUnmatchedCandidates extracts candidates that weren't matched from the matchmaker output
func (b *PostMatchmakerBackfill) ExtractUnmatchedCandidates(candidates [][]runtime.MatchmakerEntry, madeMatches [][]runtime.MatchmakerEntry) []*BackfillCandidate {
	// Build a set of matched session IDs
	matchedSessions := make(map[string]struct{})
	for _, match := range madeMatches {
		for _, entry := range match {
			matchedSessions[entry.GetPresence().GetSessionId()] = struct{}{}
		}
	}

	// Group unmatched entries by ticket
	ticketEntries := make(map[string][]*MatchmakerEntry)
	for _, candidate := range candidates {
		for _, entry := range candidate {
			if _, matched := matchedSessions[entry.GetPresence().GetSessionId()]; !matched {
				ticket := entry.GetTicket()
				me := &MatchmakerEntry{
					Ticket:     ticket,
					Presence:   entry.GetPresence().(*MatchmakerPresence),
					PartyId:    entry.GetPartyId(),
					Properties: entry.GetProperties(),
				}
				// Extract string and numeric properties
				me.StringProperties = make(map[string]string)
				me.NumericProperties = make(map[string]float64)
				for k, v := range entry.GetProperties() {
					switch val := v.(type) {
					case string:
						me.StringProperties[k] = val
					case float64:
						me.NumericProperties[k] = val
					}
				}
				ticketEntries[ticket] = append(ticketEntries[ticket], me)
			}
		}
	}

	// Convert to BackfillCandidates
	result := make([]*BackfillCandidate, 0, len(ticketEntries))
	for ticket, entries := range ticketEntries {
		if len(entries) == 0 {
			continue
		}

		firstEntry := entries[0]
		props := firstEntry.Properties

		groupIDStr, _ := props["group_id"].(string)
		groupID := uuid.FromStringOrNil(groupIDStr)

		modeStr, _ := props["game_mode"].(string)
		mode := evr.ToSymbol(modeStr)

		ratingMu, _ := props["rating_mu"].(float64)
		maxRTT, _ := props["max_rtt"].(float64)

		submissionTimeStr, _ := props["submission_time"].(string)
		submissionTime, _ := time.Parse(time.RFC3339, submissionTimeStr)

		// Extract RTTs from properties
		rtts := make(map[string]int)
		for k, v := range props {
			if strings.HasPrefix(k, RTTPropertyPrefix) {
				ip := strings.TrimPrefix(k, RTTPropertyPrefix)
				if rtt, ok := v.(float64); ok {
					rtts[ip] = int(rtt)
				}
			}
		}

		candidate := &BackfillCandidate{
			Ticket:         ticket,
			Entries:        entries,
			GroupID:        groupID,
			Mode:           mode,
			Rating:         ratingMu,
			SubmissionTime: submissionTime,
			RTTs:           rtts,
			MaxRTT:         int(maxRTT),
			TeamAlignment:  evr.TeamUnassigned,
		}

		result = append(result, candidate)
	}

	return result
}

// ExtractCandidatesFromMatchmaker extracts BackfillCandidates directly from matchmaker extracts
// Only the oldest ticket per player/party is kept to avoid duplicate processing
func (b *PostMatchmakerBackfill) ExtractCandidatesFromMatchmaker(extracts []*MatchmakerExtract) []*BackfillCandidate {
	// First pass: build all candidates
	allCandidates := make([]*BackfillCandidate, 0, len(extracts))

	for _, extract := range extracts {
		if len(extract.Presences) == 0 {
			continue
		}

		// Build MatchmakerEntry list from presences
		entries := make([]*MatchmakerEntry, 0, len(extract.Presences))
		for _, presence := range extract.Presences {
			me := &MatchmakerEntry{
				Ticket:            extract.Ticket,
				Presence:          presence,
				PartyId:           extract.PartyId,
				StringProperties:  extract.StringProperties,
				NumericProperties: extract.NumericProperties,
			}
			// Build Properties map from string and numeric properties
			me.Properties = make(map[string]any)
			for k, v := range extract.StringProperties {
				me.Properties[k] = v
			}
			for k, v := range extract.NumericProperties {
				me.Properties[k] = v
			}
			entries = append(entries, me)
		}

		// Extract candidate properties
		groupIDStr := extract.StringProperties["group_id"]
		groupID := uuid.FromStringOrNil(groupIDStr)

		modeStr := extract.StringProperties["game_mode"]
		mode := evr.ToSymbol(modeStr)

		ratingMu := extract.NumericProperties["rating_mu"]
		maxRTT := extract.NumericProperties["max_rtt"]
		intervals := extract.Intervals
		// Use CreatedAt from the extract (in milliseconds)
		submissionTime := time.UnixMilli(extract.CreatedAt)

		// Extract RTTs from numeric properties
		rtts := make(map[string]int)
		for k, v := range extract.NumericProperties {
			if strings.HasPrefix(k, RTTPropertyPrefix) {
				ip := strings.TrimPrefix(k, RTTPropertyPrefix)
				rtts[ip] = int(v)
			}
		}

		candidate := &BackfillCandidate{
			Ticket:         extract.Ticket,
			Entries:        entries,
			GroupID:        groupID,
			Mode:           mode,
			Rating:         ratingMu,
			SubmissionTime: submissionTime,
			RTTs:           rtts,
			MaxRTT:         int(maxRTT),
			TeamAlignment:  evr.TeamUnassigned,
			Intervals:      intervals,
		}

		allCandidates = append(allCandidates, candidate)
	}

	// Second pass: deduplicate by keeping only the oldest ticket per player/party
	// Use session ID as the key for solo players, party ID for parties
	oldestByKey := make(map[string]*BackfillCandidate)

	for _, candidate := range allCandidates {
		// Determine the deduplication key
		var key string
		if len(candidate.Entries) > 0 && candidate.Entries[0].PartyId != "" {
			// Party ticket - use party ID as key
			key = "party:" + candidate.Entries[0].PartyId
		} else if len(candidate.Entries) > 0 {
			// Solo player - use session ID as key
			key = "session:" + candidate.Entries[0].Presence.SessionId
		} else {
			continue
		}

		// Keep only the oldest ticket for this player/party
		if existing, ok := oldestByKey[key]; !ok || candidate.SubmissionTime.Before(existing.SubmissionTime) {
			oldestByKey[key] = candidate
		}
	}

	// Convert map back to slice
	result := make([]*BackfillCandidate, 0, len(oldestByKey))
	for _, candidate := range oldestByKey {
		result = append(result, candidate)
	}

	return result
}

// GetBackfillMatches retrieves matches that can accept backfill candidates
func (b *PostMatchmakerBackfill) GetBackfillMatches(ctx context.Context, groupID uuid.UUID, mode evr.Symbol) ([]*BackfillMatch, error) {
	// Build query for open matches in the same group with the same mode
	qparts := []string{
		"+label.open:T",
		fmt.Sprintf("+label.mode:%s", mode.String()),
		fmt.Sprintf("+label.group_id:%s", Query.QuoteStringValue(groupID.String())),
	}

	// For arena matches, exclude matches that are too old
	if mode == evr.ModeArenaPublic {
		maxAgeSecs := 270 // Default 4.5 minutes
		if config := EVRMatchmakerConfigGet(); config != nil {
			maxAgeSecs = config.Backfill.ArenaBackfillMaxAgeSecs
		}
		if maxAgeSecs > 0 {
			startTime := time.Now().UTC().Add(-time.Duration(maxAgeSecs) * time.Second).Format(time.RFC3339Nano)
			qparts = append(qparts, fmt.Sprintf(`-label.start_time:<"%s"`, startTime))
		}
	}

	query := strings.Join(qparts, " ")

	matches, err := ListMatchStates(ctx, b.nk, query)
	if err != nil {
		return nil, fmt.Errorf("failed to list matches: %w", err)
	}

	result := make([]*BackfillMatch, 0, len(matches))
	for _, m := range matches {
		if m.State == nil || !m.State.Open {
			continue
		}

		openSlots := make(map[int]int)
		teamCounts := make(map[int]int)
		if mode == evr.ModeSocialPublic {
			openSlots[evr.TeamSocial] = m.State.OpenPlayerSlots()
			teamCounts[evr.TeamSocial] = m.State.RoleCount(evr.TeamSocial)
		} else {
			blueOpen, _ := m.State.OpenSlotsByRole(evr.TeamBlue)
			orangeOpen, _ := m.State.OpenSlotsByRole(evr.TeamOrange)
			openSlots[evr.TeamBlue] = blueOpen
			openSlots[evr.TeamOrange] = orangeOpen
			teamCounts[evr.TeamBlue] = m.State.RoleCount(evr.TeamBlue)
			teamCounts[evr.TeamOrange] = m.State.RoleCount(evr.TeamOrange)
		}

		// Skip matches with no open slots
		hasOpenSlots := false
		for _, slots := range openSlots {
			if slots > 0 {
				hasOpenSlots = true
				break
			}
		}
		if !hasOpenSlots {
			continue
		}

		result = append(result, &BackfillMatch{
			Label:      m.State,
			OpenSlots:  openSlots,
			TeamCounts: teamCounts,
		})
	}

	return result, nil
}

// CalculateBackfillScore calculates the quality score for matching a candidate to a match
// Higher scores indicate better matches
// reducingPrecisionFactor is 0.0 (strict) to 1.0 (fully relaxed)
func (b *PostMatchmakerBackfill) CalculateBackfillScore(candidate *BackfillCandidate, match *BackfillMatch, team int, reducingPrecisionFactor float64) float64 {
	var settings SkillBasedMatchmakingConfig
	if config := EVRMatchmakerConfigGet(); config != nil {
		settings = config.SBMM
	}
	score := BackfillBaseScore

	// RTT scoring - lower RTT is better
	extIP := match.Label.GameServer.Endpoint.GetExternalIP()
	rtt, hasRTT := candidate.RTTs[extIP]
	if !hasRTT {
		// No RTT data means we can't assess latency - penalize unless precision is relaxed
		score -= BackfillNoRTTPenalty * (1.0 - reducingPrecisionFactor)
	} else if rtt > candidate.MaxRTT {
		// RTT exceeds max - penalize unless precision is relaxed
		penalty := float64(rtt-candidate.MaxRTT) * BackfillRTTExceedsPenaltyMultiplier * (1.0 - reducingPrecisionFactor)
		score -= penalty
	} else {
		// Good RTT - add points based on how low it is
		score += (float64(candidate.MaxRTT-rtt) / float64(candidate.MaxRTT)) * BackfillRTTScoreWeight
	}

	// Rating scoring - closer ratings are better for SBMM
	if settings.EnableSBMM && candidate.Mode == evr.ModeArenaPublic {
		ratingDelta := math.Abs(match.Label.RatingMu - candidate.Rating)
		ratingRange := settings.RatingRange
		if ratingRange == 0 {
			ratingRange = 2.0 // Default range
		}

		// When reducing precision, only partially dampen rating-based scoring to keep SBMM influence.
		const reducingPrecisionRatingWeightScale = 0.5

		if ratingDelta <= ratingRange || reducingPrecisionFactor >= reducingPrecisionRatingWeightScale {
			// Within rating range or precision is relaxed
			ratingScore := (1.0 - ratingDelta/ratingRange) * BackfillRatingScoreWeight
			if ratingScore < 0 {
				ratingScore = 0
			}
			score += ratingScore * (1.0 - reducingPrecisionFactor*reducingPrecisionRatingWeightScale)
		} else {
			// Outside rating range - penalize
			score -= BackfillRatingScoreWeight * (1.0 - reducingPrecisionFactor)
		}
	}

	// Prefer matches with more players (more active/likely to be good)
	populationBonus := float64(match.Label.PlayerCount) * BackfillPopulationBonus
	score += populationBonus

	// Prefer matches where the team needs more players (balance)
	// Use tracked team counts to account for pending backfills
	if team != evr.TeamSocial {
		blueCount := match.TeamCounts[evr.TeamBlue]
		orangeCount := match.TeamCounts[evr.TeamOrange]
		teamImbalance := math.Abs(float64(blueCount - orangeCount))

		// If this team assignment would help balance, add points
		if team == evr.TeamBlue && blueCount < orangeCount {
			score += BackfillTeamBalanceBonus
		} else if team == evr.TeamOrange && orangeCount < blueCount {
			score += BackfillTeamBalanceBonus
		} else if teamImbalance > 1 {
			// Would make imbalance worse - penalize
			score -= BackfillTeamImbalancePenalty * (1.0 - reducingPrecisionFactor)
		}
	}

	// Older matches get slight penalty to prefer fresher games
	matchAge := time.Since(match.Label.StartTime)
	if matchAge > BackfillMatchAgeThreshold {
		agePenalty := float64(matchAge.Minutes()) * BackfillMatchAgePenaltyPerMinute * (1.0 - reducingPrecisionFactor)
		score -= agePenalty
	}

	return score
}

// FindBestBackfillMatch finds the best match for a candidate
func (b *PostMatchmakerBackfill) FindBestBackfillMatch(candidate *BackfillCandidate, matches []*BackfillMatch, reducingPrecisionFactor float64) (*BackfillResult, error) {
	partySize := len(candidate.Entries)
	var bestResult *BackfillResult

	for _, match := range matches {
		// Determine which teams can accept this party
		possibleTeams := []int{}

		if candidate.Mode == evr.ModeSocialPublic {
			if match.OpenSlots[evr.TeamSocial] >= partySize {
				possibleTeams = append(possibleTeams, evr.TeamSocial)
			}
		} else {
			// For competitive modes, try to use ticket's team alignment if set
			if candidate.TeamAlignment != evr.TeamUnassigned {
				if match.OpenSlots[candidate.TeamAlignment] >= partySize {
					possibleTeams = append(possibleTeams, candidate.TeamAlignment)
				}
			} else {
				// No team preference - try both teams, prefer the smaller one for balance
				// Use tracked team counts to account for pending backfills
				blueCount := match.TeamCounts[evr.TeamBlue]
				orangeCount := match.TeamCounts[evr.TeamOrange]

				// Determine which team(s) to consider based on current balance
				if blueCount < orangeCount {
					// Blue team is smaller, prioritize it
					if match.OpenSlots[evr.TeamBlue] >= partySize {
						possibleTeams = append(possibleTeams, evr.TeamBlue)
					}
				} else if orangeCount < blueCount {
					// Orange team is smaller, prioritize it
					if match.OpenSlots[evr.TeamOrange] >= partySize {
						possibleTeams = append(possibleTeams, evr.TeamOrange)
					}
				} else {
					// Teams are equal - consider both teams for better match quality
					if match.OpenSlots[evr.TeamBlue] >= partySize {
						possibleTeams = append(possibleTeams, evr.TeamBlue)
					}
					if match.OpenSlots[evr.TeamOrange] >= partySize {
						possibleTeams = append(possibleTeams, evr.TeamOrange)
					}
				}

				// If no team was added (smaller team is full), try the other team
				if len(possibleTeams) == 0 {
					if match.OpenSlots[evr.TeamBlue] >= partySize {
						possibleTeams = append(possibleTeams, evr.TeamBlue)
					}
					if match.OpenSlots[evr.TeamOrange] >= partySize {
						possibleTeams = append(possibleTeams, evr.TeamOrange)
					}
				}
			}
		}

		// Score each possible team assignment
		for _, team := range possibleTeams {
			score := b.CalculateBackfillScore(candidate, match, team, reducingPrecisionFactor)

			if bestResult == nil || score > bestResult.Score {
				bestResult = &BackfillResult{
					Candidate: candidate,
					Match:     match,
					Team:      team,
					Score:     score,
				}
			}
		}
	}

	return bestResult, nil
}

// ProcessAndExecuteBackfill processes all unmatched candidates and attempts to backfill them,
// executing each backfill immediately after finding a match. This ensures slot tracking remains
// accurate by only decrementing slots when joins actually succeed.
//
// Optimized to pre-compute settings, match metadata, and resolve sessions upfront to avoid
// redundant lookups in inner loops.
func (b *PostMatchmakerBackfill) ProcessAndExecuteBackfill(ctx context.Context, logger *zap.Logger, candidates []*BackfillCandidate, reducingPrecisionFactor float64) ([]*BackfillResult, error) {
	if len(candidates) == 0 {
		return nil, nil
	}

	logger.Info("Processing backfill candidates",
		zap.Int("candidate_count", len(candidates)),
		zap.Float64("reducing_precision_factor", reducingPrecisionFactor))

	// Pre-compute context once for all scoring calculations
	bctx := &backfillContext{
		now:                     time.Now(),
		reducingPrecisionFactor: reducingPrecisionFactor,
	}
	if config := EVRMatchmakerConfigGet(); config != nil {
		bctx.settings = config.SBMM
	}

	// Sort candidates by submission time (oldest first - they've waited longest)
	sort.SliceStable(candidates, func(i, j int) bool {
		return candidates[i].SubmissionTime.Before(candidates[j].SubmissionTime)
	})

	// Pre-resolve sessions for all candidates upfront (batch operation)
	preparedCandidates := b.prepareCandidates(candidates)
	if len(preparedCandidates) == 0 {
		logger.Debug("No valid candidates after session resolution")
		return nil, nil
	}

	// Group candidates by groupID and mode
	type groupKey struct {
		GroupID uuid.UUID
		Mode    evr.Symbol
	}
	candidatesByGroup := make(map[groupKey][]*preparedBackfillCandidate)
	for _, c := range preparedCandidates {
		key := groupKey{GroupID: c.GroupID, Mode: c.Mode}
		candidatesByGroup[key] = append(candidatesByGroup[key], c)
	}

	results := make([]*BackfillResult, 0)

	for key, groupCandidates := range candidatesByGroup {
		// Check for context cancellation before processing each group
		select {
		case <-ctx.Done():
			b.logger.Debug("Backfill cancelled", zap.Error(ctx.Err()))
			return results, ctx.Err()
		default:
		}

		// Get available matches for this group/mode
		rawMatches, err := b.GetBackfillMatches(ctx, key.GroupID, key.Mode)
		if err != nil {
			b.logger.Warn("Failed to get backfill matches", zap.Error(err), zap.String("group_id", key.GroupID.String()), zap.String("mode", key.Mode.String()))
			continue
		}

		if len(rawMatches) == 0 {
			continue
		}

		// Pre-compute match metadata (external IPs, ages) once per group
		preparedMatches := b.prepareMatches(rawMatches, bctx)
		groupIDStr := key.GroupID.String()

		// Process and execute each candidate immediately
		for _, candidate := range groupCandidates {
			// Check for context cancellation before processing each candidate
			select {
			case <-ctx.Done():
				b.logger.Debug("Backfill cancelled during candidate processing", zap.Error(ctx.Err()))
				return results, ctx.Err()
			default:
			}

			// Find best match using pre-computed data
			result := b.findBestBackfillMatchOptimized(candidate, preparedMatches, bctx)
			if result == nil || result.Score <= BackfillMinAcceptableScore {
				continue
			}

			// Execute backfill immediately using pre-resolved sessions
			successCount := b.executeBackfillResultOptimized(ctx, logger, result, candidate.sessions, groupIDStr)
			if successCount > 0 {
				results = append(results, result)
				// Update tracked slots and team counts for successfully joined entrants
				result.Match.OpenSlots[result.Team] -= successCount
				result.Match.TeamCounts[result.Team] += successCount

				// Log structured summary after each backfill operation
				b.logBackfillSummary(logger, result)
			}
		}
	}

	return results, nil
}

// logBackfillSummary logs a structured summary of a completed backfill operation
func (b *PostMatchmakerBackfill) logBackfillSummary(logger *zap.Logger, result *BackfillResult) {
	if result == nil {
		return
	}

	// Build structured summary of the backfill operation
	fields := []zap.Field{
		zap.String("match_id", result.Match.Label.ID.String()),
		zap.Int("team", result.Team),
		zap.Float64("score", result.Score),
		zap.Int("player_count", len(result.PlayerUserIDs)),
		zap.Strings("player_user_ids", result.PlayerUserIDs),
		zap.String("mode", result.Match.Label.Mode.String()),
		zap.String("group_id", result.Match.Label.GetGroupID().String()),
	}

	// Add rating info if available
	if result.Match.Label.RatingMu > 0 {
		fields = append(fields, zap.Float64("match_rating", result.Match.Label.RatingMu))
	}
	if result.Candidate.Rating > 0 {
		fields = append(fields, zap.Float64("candidate_rating", result.Candidate.Rating))
	}

	logger.Info("Backfill operation completed", fields...)
}

// executeBackfillResult executes a single backfill result and returns the number of successfully joined entrants
func (b *PostMatchmakerBackfill) executeBackfillResult(ctx context.Context, logger *zap.Logger, result *BackfillResult) int {
	if result == nil || result.Match == nil || result.Candidate == nil {
		return 0
	}

	// Create entrant presences from the matchmaker entries
	entrants := make([]*EvrMatchPresence, 0, len(result.Candidate.Entries))
	sessions := make([]Session, 0, len(result.Candidate.Entries))

	for _, entry := range result.Candidate.Entries {
		sessionID := uuid.FromStringOrNil(entry.Presence.GetSessionId())
		session := b.sessionRegistry.Get(sessionID)
		if session == nil {
			logger.Warn("Session not found for backfill", zap.String("session_id", entry.Presence.GetSessionId()))
			continue
		}

		mu := entry.NumericProperties["rating_mu"]
		sigma := entry.NumericProperties["rating_sigma"]
		rating := NewRating(0, mu, sigma)

		entrant, err := EntrantPresenceFromSession(session, uuid.FromStringOrNil(entry.PartyId), result.Team, rating, result.Candidate.GroupID.String(), 0, "")
		if err != nil {
			logger.Warn("Failed to create entrant presence for backfill", zap.Error(err), zap.String("session_id", session.ID().String()))
			continue
		}

		entrants = append(entrants, entrant)
		sessions = append(sessions, session)
	}

	if len(entrants) == 0 {
		return 0
	}

	// Get the server session
	serverSession := b.sessionRegistry.Get(result.Match.Label.GameServer.SessionID)
	if serverSession == nil {
		logger.Warn("Server session not found for backfill", zap.String("match_id", result.Match.Label.ID.String()))
		return 0
	}

	// Join entrants to the match concurrently
	var wg sync.WaitGroup
	var mu sync.Mutex
	successCount := 0
	successfulUserIDs := make([]string, 0, len(entrants))

	for i, entrant := range entrants {
		wg.Add(1)
		go func(idx int, ent *EvrMatchPresence) {
			defer wg.Done()
			if err := LobbyJoinEntrants(logger, b.matchRegistry, b.tracker, sessions[idx], serverSession, result.Match.Label, ent); err != nil {
				logger.Warn("Failed to join entrant to backfill match", zap.Error(err), zap.String("match_id", result.Match.Label.ID.String()), zap.String("user_id", ent.GetUserId()))
			} else {
				mu.Lock()
				successCount++
				successfulUserIDs = append(successfulUserIDs, ent.GetUserId())
				mu.Unlock()
				b.metrics.CustomCounter("lobby_join_post_matchmaker_backfill", result.Match.Label.MetricsTags(), 1)
				logger.Info("Successfully backfilled player",
					zap.String("match_id", result.Match.Label.ID.String()),
					zap.String("user_id", ent.GetUserId()),
					zap.Int("team", result.Team),
					zap.Float64("score", result.Score))
			}
		}(i, entrant)
	}

	wg.Wait()

	// Store the successful user IDs in the result for summary logging
	result.PlayerUserIDs = successfulUserIDs

	return successCount
}
