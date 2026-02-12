package server

import (
	"time"

	"github.com/heroiclabs/nakama-common/runtime"
)

// Ticket Reservation System
//
// This system addresses the "starving ticket" problem where long-waiting players
// lose viable opponents to greedy matching. It implements a two-pass assembly approach:
//
// Pass 1: Starving tickets (wait >= ReservationThresholdSecs) get unrestricted access
// Pass 2: Remaining matches skip reserved players (hard reservation)
//
// Key Features:
// - Tracks starving state across matchmaker cycles (in-memory)
// - Reserves up to MaxReservationRatio of pool for starving players
// - Safety valve at ReservationSafetyValveSecs releases reservations to prevent deadlock
// - Feature flag EnableTicketReservation (default: false)
//
// Example: Player waiting 3 minutes (starving) gets 3-4 opponents reserved.
// Fresh tickets can't consume those players, protecting them for the starving player.

// buildReservations scans candidates and builds reservation sets for starving tickets
// Returns (starvingSessionIDs, reservedSessionIDs) both as map[string]struct{}
func (m *SkillBasedMatchmaker) buildReservations(candidates [][]runtime.MatchmakerEntry, predictions []PredictedMatch, settings *GlobalMatchmakingSettings) (map[string]struct{}, map[string]struct{}) {
	m.reservationMu.Lock()
	defer m.reservationMu.Unlock()

	now := time.Now().UTC()
	nowUnix := now.Unix()

	// Default settings
	reservationThreshold := int64(90)
	safetyValveThreshold := int64(300)
	maxReservationRatio := 0.4

	if settings != nil {
		if settings.ReservationThresholdSecs > 0 {
			reservationThreshold = int64(settings.ReservationThresholdSecs)
		}
		if settings.ReservationSafetyValveSecs > 0 {
			safetyValveThreshold = int64(settings.ReservationSafetyValveSecs)
		}
		if settings.MaxReservationRatio > 0 && settings.MaxReservationRatio <= 1.0 {
			maxReservationRatio = settings.MaxReservationRatio
		}
	}

	// Build set of all active tickets in the pool
	activeTickets := make(map[string]struct{})
	for _, candidate := range candidates {
		if candidate == nil {
			continue
		}
		for _, entry := range candidate {
			activeTickets[entry.GetTicket()] = struct{}{}
		}
	}

	// Prune stale starving tickets that are no longer in the pool
	for ticket := range m.starvingTickets {
		if _, active := activeTickets[ticket]; !active {
			delete(m.starvingTickets, ticket)
		}
	}

	// Scan for starving tickets and update state
	starvingSessionIDs := make(map[string]struct{})
	for _, candidate := range candidates {
		if candidate == nil {
			continue
		}
		for _, entry := range candidate {
			props := entry.GetProperties()
			submissionTime, ok := props["submission_time"].(float64)
			if !ok || submissionTime <= 0 {
				continue
			}

			waitTime := nowUnix - int64(submissionTime)
			ticket := entry.GetTicket()

			// Check if already starving
			if st, exists := m.starvingTickets[ticket]; exists {
				// Check safety valve
				starvingDuration := now.Sub(st.FirstStarvedAt).Seconds()
				if starvingDuration >= float64(safetyValveThreshold) {
					// Safety valve fired - remove from starving map
					delete(m.starvingTickets, ticket)
					continue
				}
				// Still starving, add session to starving set
				starvingSessionIDs[entry.GetPresence().GetSessionId()] = struct{}{}
			} else if waitTime >= reservationThreshold {
				// New starving ticket
				m.starvingTickets[ticket] = &StarvingTicket{
					Ticket:         ticket,
					FirstStarvedAt: now,
				}
				starvingSessionIDs[entry.GetPresence().GetSessionId()] = struct{}{}
			}
		}
	}

	// Build reserved set from predictions that contain starving players
	reservedSessionIDs := make(map[string]struct{})
	totalUniquePlayers := make(map[string]struct{})

	// Count total unique players first
	for _, candidate := range candidates {
		if candidate == nil {
			continue
		}
		for _, entry := range candidate {
			totalUniquePlayers[entry.GetPresence().GetSessionId()] = struct{}{}
		}
	}

	maxReserved := int(float64(len(totalUniquePlayers)) * maxReservationRatio)

	// For each prediction, if it contains a starving player, reserve the OTHER players
	for _, pred := range predictions {
		if len(reservedSessionIDs) >= maxReserved {
			break
		}

		hasStarvingPlayer := false
		for _, entry := range pred.Candidate {
			if _, isStarving := starvingSessionIDs[entry.GetPresence().GetSessionId()]; isStarving {
				hasStarvingPlayer = true
				break
			}
		}

		if hasStarvingPlayer {
			// Reserve all non-starving players in this candidate
			for _, entry := range pred.Candidate {
				sessionID := entry.GetPresence().GetSessionId()
				if _, isStarving := starvingSessionIDs[sessionID]; !isStarving {
					if len(reservedSessionIDs) < maxReserved {
						reservedSessionIDs[sessionID] = struct{}{}
					}
				}
			}
		}
	}

	return starvingSessionIDs, reservedSessionIDs
}

// assembleMatchesWithReservations performs two-pass assembly with reservation awareness
func (m *SkillBasedMatchmaker) assembleMatchesWithReservations(predictions []PredictedMatch, starvingSessionIDs, reservedSessionIDs map[string]struct{}) [][]runtime.MatchmakerEntry {
	matches := make([][]runtime.MatchmakerEntry, 0, len(predictions))
	matchedPlayers := make(map[string]struct{})

	// Pass 1: Assemble matches that contain starving players (unrestricted access)
	for _, pred := range predictions {
		// Skip if this candidate has already-matched players
		if hasMatchedPlayer(pred.Candidate, matchedPlayers) {
			continue
		}

		// Only process if this contains a starving player
		if !containsStarvingPlayer(pred.Candidate, starvingSessionIDs) {
			continue
		}

		// Mark all players as matched
		markMatched(pred.Candidate, matchedPlayers)
		matches = append(matches, pred.Candidate)
	}

	// Remove matched starving tickets from the starving map
	m.reservationMu.Lock()
	for _, match := range matches {
		for _, entry := range match {
			ticket := entry.GetTicket()
			delete(m.starvingTickets, ticket)
		}
	}
	m.reservationMu.Unlock()

	// Pass 2: Assemble remaining matches (reservation-restricted)
	for _, pred := range predictions {
		// Skip if this candidate has already-matched players
		if hasMatchedPlayer(pred.Candidate, matchedPlayers) {
			continue
		}

		// Hard reservation check: skip if this would consume any reserved player
		if consumesReservedPlayer(pred.Candidate, reservedSessionIDs, matchedPlayers) {
			continue
		}

		// Mark all players as matched
		markMatched(pred.Candidate, matchedPlayers)
		matches = append(matches, pred.Candidate)
	}

	return matches
}

// containsStarvingPlayer checks if a candidate contains any starving player
func containsStarvingPlayer(candidate []runtime.MatchmakerEntry, starving map[string]struct{}) bool {
	for _, entry := range candidate {
		if _, ok := starving[entry.GetPresence().GetSessionId()]; ok {
			return true
		}
	}
	return false
}

// consumesReservedPlayer checks if a candidate would consume any reserved player (who isn't already matched)
func consumesReservedPlayer(candidate []runtime.MatchmakerEntry, reserved, alreadyMatched map[string]struct{}) bool {
	for _, entry := range candidate {
		sessionID := entry.GetPresence().GetSessionId()
		// Skip if already matched (not available anyway)
		if _, matched := alreadyMatched[sessionID]; matched {
			continue
		}
		// Check if this is a reserved player
		if _, isReserved := reserved[sessionID]; isReserved {
			return true
		}
	}
	return false
}

// hasMatchedPlayer checks if any player in the candidate has already been matched
func hasMatchedPlayer(candidate []runtime.MatchmakerEntry, matched map[string]struct{}) bool {
	for _, entry := range candidate {
		if _, ok := matched[entry.GetPresence().GetSessionId()]; ok {
			return true
		}
	}
	return false
}

// markMatched marks all players in a candidate as matched
func markMatched(candidate []runtime.MatchmakerEntry, matched map[string]struct{}) {
	for _, entry := range candidate {
		matched[entry.GetPresence().GetSessionId()] = struct{}{}
	}
}
