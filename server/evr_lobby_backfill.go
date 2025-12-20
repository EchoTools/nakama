package server

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/gofrs/uuid/v5"
	"github.com/heroiclabs/nakama-common/runtime"
	"github.com/heroiclabs/nakama/v3/server/evr"
	"go.uber.org/zap"
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
}

// BackfillMatch represents a match that can accept backfill candidates
type BackfillMatch struct {
	Label     *MatchLabel
	OpenSlots map[int]int // map[teamIndex]openSlots
	RTTs      map[string]int
}

// BackfillResult represents the result of a backfill attempt
type BackfillResult struct {
	Candidate *BackfillCandidate
	Match     *BackfillMatch
	Team      int
	Score     float64
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
		maxAgeSecs := ServiceSettings().Matchmaking.ArenaBackfillMaxAgeSecs
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
		if mode == evr.ModeSocialPublic {
			openSlots[evr.TeamSocial] = m.State.OpenPlayerSlots()
		} else {
			blueOpen, _ := m.State.OpenSlotsByRole(evr.TeamBlue)
			orangeOpen, _ := m.State.OpenSlotsByRole(evr.TeamOrange)
			openSlots[evr.TeamBlue] = blueOpen
			openSlots[evr.TeamOrange] = orangeOpen
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
			Label:     m.State,
			OpenSlots: openSlots,
		})
	}

	return result, nil
}

// CalculateBackfillScore calculates the quality score for matching a candidate to a match
// Higher scores indicate better matches
// reducingPrecisionFactor is 0.0 (strict) to 1.0 (fully relaxed)
func (b *PostMatchmakerBackfill) CalculateBackfillScore(candidate *BackfillCandidate, match *BackfillMatch, team int, reducingPrecisionFactor float64) float64 {
	settings := ServiceSettings().Matchmaking
	var score float64 = 100.0 // Start with a base score

	// RTT scoring - lower RTT is better
	extIP := match.Label.GameServer.Endpoint.GetExternalIP()
	rtt, hasRTT := candidate.RTTs[extIP]
	if !hasRTT {
		// No RTT data means we can't assess latency - penalize unless precision is relaxed
		score -= 50.0 * (1.0 - reducingPrecisionFactor)
	} else if rtt > candidate.MaxRTT {
		// RTT exceeds max - penalize unless precision is relaxed
		penalty := float64(rtt-candidate.MaxRTT) * 0.5 * (1.0 - reducingPrecisionFactor)
		score -= penalty
	} else {
		// Good RTT - add points based on how low it is
		score += (float64(candidate.MaxRTT-rtt) / float64(candidate.MaxRTT)) * 20.0
	}

	// Rating scoring - closer ratings are better for SBMM
	if settings.EnableSBMM && candidate.Mode == evr.ModeArenaPublic {
		ratingDelta := math.Abs(match.Label.RatingMu - candidate.Rating)
		ratingRange := settings.RatingRange
		if ratingRange == 0 {
			ratingRange = 2.0 // Default range
		}

		if ratingDelta <= ratingRange || reducingPrecisionFactor >= 0.5 {
			// Within rating range or precision is relaxed
			ratingScore := (1.0 - ratingDelta/ratingRange) * 30.0
			if ratingScore < 0 {
				ratingScore = 0
			}
			score += ratingScore * (1.0 - reducingPrecisionFactor*0.5)
		} else {
			// Outside rating range - penalize
			score -= 30.0 * (1.0 - reducingPrecisionFactor)
		}
	}

	// Prefer matches with more players (more active/likely to be good)
	populationBonus := float64(match.Label.PlayerCount) * 2.0
	score += populationBonus

	// Prefer matches where the team needs more players (balance)
	if team != evr.TeamSocial {
		blueCount := match.Label.RoleCount(evr.TeamBlue)
		orangeCount := match.Label.RoleCount(evr.TeamOrange)
		teamImbalance := math.Abs(float64(blueCount - orangeCount))

		// If this team assignment would help balance, add points
		if team == evr.TeamBlue && blueCount < orangeCount {
			score += 10.0
		} else if team == evr.TeamOrange && orangeCount < blueCount {
			score += 10.0
		} else if teamImbalance > 1 {
			// Would make imbalance worse - penalize
			score -= 5.0 * (1.0 - reducingPrecisionFactor)
		}
	}

	// Older matches get slight penalty to prefer fresher games
	matchAge := time.Since(match.Label.StartTime)
	if matchAge > 2*time.Minute {
		agePenalty := float64(matchAge.Minutes()) * 2.0 * (1.0 - reducingPrecisionFactor)
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
				blueCount := match.Label.RoleCount(evr.TeamBlue)
				orangeCount := match.Label.RoleCount(evr.TeamOrange)

				if blueCount <= orangeCount && match.OpenSlots[evr.TeamBlue] >= partySize {
					possibleTeams = append(possibleTeams, evr.TeamBlue)
				}
				if orangeCount < blueCount && match.OpenSlots[evr.TeamOrange] >= partySize {
					possibleTeams = append(possibleTeams, evr.TeamOrange)
				}
				// If neither team is smaller or both can fit, try both
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

// ProcessBackfill processes all unmatched candidates and attempts to backfill them
func (b *PostMatchmakerBackfill) ProcessBackfill(ctx context.Context, candidates []*BackfillCandidate, reducingPrecisionFactor float64) ([]*BackfillResult, error) {
	if len(candidates) == 0 {
		return nil, nil
	}

	// Sort candidates by submission time (oldest first - they've waited longest)
	sort.SliceStable(candidates, func(i, j int) bool {
		return candidates[i].SubmissionTime.Before(candidates[j].SubmissionTime)
	})

	// Group candidates by groupID and mode
	type groupKey struct {
		GroupID uuid.UUID
		Mode    evr.Symbol
	}
	candidatesByGroup := make(map[groupKey][]*BackfillCandidate)
	for _, c := range candidates {
		key := groupKey{GroupID: c.GroupID, Mode: c.Mode}
		candidatesByGroup[key] = append(candidatesByGroup[key], c)
	}

	results := make([]*BackfillResult, 0)

	for key, groupCandidates := range candidatesByGroup {
		// Get available matches for this group/mode
		matches, err := b.GetBackfillMatches(ctx, key.GroupID, key.Mode)
		if err != nil {
			b.logger.Warn("Failed to get backfill matches", zap.Error(err), zap.String("group_id", key.GroupID.String()), zap.String("mode", key.Mode.String()))
			continue
		}

		// Process each candidate
		for _, candidate := range groupCandidates {
			result, err := b.FindBestBackfillMatch(candidate, matches, reducingPrecisionFactor)
			if err != nil {
				b.logger.Warn("Failed to find backfill match", zap.Error(err), zap.String("ticket", candidate.Ticket))
				continue
			}

			if result != nil && result.Score > 0 {
				results = append(results, result)

				// Update match open slots to reflect this assignment
				partySize := len(candidate.Entries)
				result.Match.OpenSlots[result.Team] -= partySize
			}
		}
	}

	return results, nil
}

// ExecuteBackfill executes the backfill results by joining players to matches
func (b *PostMatchmakerBackfill) ExecuteBackfill(ctx context.Context, logger *zap.Logger, results []*BackfillResult) error {
	for _, result := range results {
		if result == nil || result.Match == nil || result.Candidate == nil {
			continue
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
			continue
		}

		// Get the server session
		serverSession := b.sessionRegistry.Get(result.Match.Label.GameServer.SessionID)
		if serverSession == nil {
			logger.Warn("Server session not found for backfill", zap.String("match_id", result.Match.Label.ID.String()))
			continue
		}

		// Join entrants to the match
		for i, entrant := range entrants {
			if err := LobbyJoinEntrants(logger, b.matchRegistry, b.tracker, sessions[i], serverSession, result.Match.Label, entrant); err != nil {
				logger.Warn("Failed to join entrant to backfill match", zap.Error(err), zap.String("match_id", result.Match.Label.ID.String()), zap.String("user_id", entrant.GetUserId()))
			} else {
				b.metrics.CustomCounter("lobby_join_post_matchmaker_backfill", result.Match.Label.MetricsTags(), 1)
				logger.Info("Successfully backfilled player",
					zap.String("match_id", result.Match.Label.ID.String()),
					zap.String("user_id", entrant.GetUserId()),
					zap.Int("team", result.Team),
					zap.Float64("score", result.Score))
			}
		}
	}

	return nil
}
