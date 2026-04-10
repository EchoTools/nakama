package server

import (
	"math"
	"sort"
	"time"

	"github.com/heroiclabs/nakama-common/runtime"
)

const defaultMatchmakerIntervalSecs = 30.0

// accumulateForStarving identifies starving tickets, reserves best-fit players for them
// across cycles, and promotes full accumulation pools as pre-formed candidates.
// Returns (pre-formed candidates to inject, remaining entries for normal grouping).
func (m *SkillBasedMatchmaker) accumulateForStarving(logger runtime.Logger, entries []runtime.MatchmakerEntry, settings *GlobalMatchmakingSettings) ([][]runtime.MatchmakerEntry, []runtime.MatchmakerEntry) {
	if settings == nil || !settings.EnableAccumulation {
		return nil, entries
	}

	m.accumulationMu.Lock()
	defer m.accumulationMu.Unlock()

	now := time.Now().UTC()
	nowUnix := now.Unix()

	threshold := int64(settings.AccumulationThresholdSecs)
	if threshold <= 0 {
		threshold = 90
	}
	maxAge := int64(settings.AccumulationMaxAgeSecs)
	if maxAge <= 0 {
		maxAge = 300
	}
	initialRadius := settings.AccumulationInitialRadius
	if initialRadius <= 0 {
		initialRadius = 5.0
	}
	expansionPerCycle := settings.AccumulationRadiusExpansionPerCycle
	if expansionPerCycle <= 0 {
		expansionPerCycle = 1.0
	}
	maxRadius := settings.AccumulationMaxRadius
	if maxRadius <= 0 {
		maxRadius = 40.0
	}

	// Index entries by session ID and group by ticket
	sessionIndex := make(map[string]runtime.MatchmakerEntry, len(entries))
	ticketEntries := make(map[string][]runtime.MatchmakerEntry)
	for _, e := range entries {
		sessionIndex[e.GetPresence().GetSessionId()] = e
		ticketEntries[e.GetTicket()] = append(ticketEntries[e.GetTicket()], e)
	}

	activeTickets := make(map[string]struct{}, len(ticketEntries))
	for ticket := range ticketEntries {
		activeTickets[ticket] = struct{}{}
	}

	// Step 1: Prune stale state
	expiredTickets := make(map[string]struct{}) // tickets expired by safety valve (don't re-create)
	for ticket, pool := range m.accumulationPools {
		if _, active := activeTickets[ticket]; !active {
			delete(m.accumulationPools, ticket)
			continue
		}
		if now.Sub(pool.CreatedAt).Seconds() >= float64(maxAge) {
			logger.WithFields(map[string]interface{}{
				"ticket":  ticket,
				"age_sec": now.Sub(pool.CreatedAt).Seconds(),
			}).Debug("Accumulation pool expired (safety valve)")
			delete(m.accumulationPools, ticket)
			expiredTickets[ticket] = struct{}{}
			continue
		}
		// Prune reserved players who left the queue
		alive := pool.ReservedPlayers[:0]
		for _, sid := range pool.ReservedPlayers {
			if _, exists := sessionIndex[sid]; exists {
				alive = append(alive, sid)
			}
		}
		pool.ReservedPlayers = alive
	}

	// Step 2: Identify new starving tickets
	for ticket, ticketEnts := range ticketEntries {
		if _, exists := m.accumulationPools[ticket]; exists {
			continue
		}
		if _, expired := expiredTickets[ticket]; expired {
			continue // safety valve fired this cycle — don't re-create immediately
		}
		props := ticketEnts[0].GetProperties()
		timestamp, ok := props["timestamp"].(float64)
		if !ok || timestamp <= 0 {
			continue
		}
		if nowUnix-int64(timestamp) < threshold {
			continue
		}

		totalMu := 0.0
		sessionIDs := make([]string, 0, len(ticketEnts))
		for _, e := range ticketEnts {
			mu, _ := e.GetProperties()["rating_mu"].(float64)
			totalMu += mu
			sessionIDs = append(sessionIDs, e.GetPresence().GetSessionId())
		}

		targetSize := 8
		if v, ok := ticketEnts[0].GetProperties()["max_team_size"].(float64); ok && int(v) > 0 {
			targetSize = int(v) * 2
		}

		m.accumulationPools[ticket] = &AccumulationPool{
			StarvingTicket:  ticket,
			StarvingEntries: sessionIDs,
			CenterMu:        totalMu / float64(len(ticketEnts)),
			SkillRadius:     initialRadius,
			CreatedAt:       now,
			TargetSize:      targetSize,
		}

		logger.WithFields(map[string]interface{}{
			"ticket":     ticket,
			"center_mu":  totalMu / float64(len(ticketEnts)),
			"party_size": len(ticketEnts),
			"wait_secs":  nowUnix - int64(timestamp),
		}).Debug("Ticket entered accumulation")
	}

	// Step 3: Widen skill radius for existing pools
	for _, pool := range m.accumulationPools {
		ageSecs := now.Sub(pool.CreatedAt).Seconds()
		cycles := ageSecs / defaultMatchmakerIntervalSecs
		pool.SkillRadius = math.Min(initialRadius+cycles*expansionPerCycle, maxRadius)
	}

	// Step 4: Reserve best-fit players (oldest pools get priority)
	type poolEntry struct {
		ticket string
		pool   *AccumulationPool
	}
	sortedPools := make([]poolEntry, 0, len(m.accumulationPools))
	for ticket, pool := range m.accumulationPools {
		sortedPools = append(sortedPools, poolEntry{ticket, pool})
	}
	sort.Slice(sortedPools, func(i, j int) bool {
		return sortedPools[i].pool.CreatedAt.Before(sortedPools[j].pool.CreatedAt)
	})

	globalReserved := make(map[string]struct{})
	for _, pe := range sortedPools {
		for _, sid := range pe.pool.StarvingEntries {
			globalReserved[sid] = struct{}{}
		}
		for _, sid := range pe.pool.ReservedPlayers {
			globalReserved[sid] = struct{}{}
		}
	}

	for _, pe := range sortedPools {
		pool := pe.pool
		needed := pool.TargetSize - len(pool.StarvingEntries) - len(pool.ReservedPlayers)
		if needed <= 0 {
			continue
		}

		type candidate struct {
			sessionID string
			distance  float64
		}
		var candidates []candidate
		for sid, e := range sessionIndex {
			if _, reserved := globalReserved[sid]; reserved {
				continue
			}
			mu, _ := e.GetProperties()["rating_mu"].(float64)
			dist := math.Abs(mu - pool.CenterMu)
			if dist <= pool.SkillRadius {
				candidates = append(candidates, candidate{sid, dist})
			}
		}
		sort.Slice(candidates, func(i, j int) bool {
			return candidates[i].distance < candidates[j].distance
		})

		for i := 0; i < len(candidates) && i < needed; i++ {
			pool.ReservedPlayers = append(pool.ReservedPlayers, candidates[i].sessionID)
			globalReserved[candidates[i].sessionID] = struct{}{}
		}
	}

	// Step 5: Promote full pools as pre-formed candidates
	var preFormed [][]runtime.MatchmakerEntry
	for ticket, pool := range m.accumulationPools {
		if len(pool.StarvingEntries)+len(pool.ReservedPlayers) < pool.TargetSize {
			continue
		}

		candidate := make([]runtime.MatchmakerEntry, 0, pool.TargetSize)
		for _, sid := range pool.StarvingEntries {
			if e, ok := sessionIndex[sid]; ok {
				candidate = append(candidate, e)
			}
		}
		for _, sid := range pool.ReservedPlayers {
			if e, ok := sessionIndex[sid]; ok {
				candidate = append(candidate, e)
			}
		}

		if len(candidate) >= pool.TargetSize {
			candidate = candidate[:pool.TargetSize]
			preFormed = append(preFormed, candidate)
			delete(m.accumulationPools, ticket)

			logger.WithFields(map[string]interface{}{
				"ticket":       ticket,
				"center_mu":    pool.CenterMu,
				"skill_radius": pool.SkillRadius,
				"players":      len(candidate),
			}).Info("Accumulation pool promoted to candidate")
		}
	}

	// Step 6: Build remaining entries (exclude reserved players)
	remaining := make([]runtime.MatchmakerEntry, 0, len(entries)-len(globalReserved))
	for _, e := range entries {
		if _, reserved := globalReserved[e.GetPresence().GetSessionId()]; !reserved {
			remaining = append(remaining, e)
		}
	}

	return preFormed, remaining
}

// countAccumulatedPlayers returns the total number of reserved players across all pools.
func countAccumulatedPlayers(m *SkillBasedMatchmaker) int {
	total := 0
	for _, pool := range m.accumulationPools {
		total += len(pool.ReservedPlayers)
	}
	return total
}
