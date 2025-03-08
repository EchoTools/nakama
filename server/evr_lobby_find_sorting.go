package server

import (
	"math"
	"slices"
	"time"

	"github.com/heroiclabs/nakama/v3/server/evr"
)

type backfillSortItem struct {
	match               *MatchLabelMeta
	rtt                 int
	rankPercentileDelta float64
	openSlots           int
	withinRankRange     bool
	withinRTTRange      bool
}

func (p *EvrPipeline) sortBackfillOptions(filteredMatches []*MatchLabelMeta, lobbyParams *LobbySessionParameters) []*MatchLabelMeta {

	var (
		partySize      = lobbyParams.GetPartySize()
		rtts           = lobbyParams.latencyHistory.LatestRTTs()
		rankPercentile = 0.5
		items          = make([]backfillSortItem, 0, len(filteredMatches))
	)

	if lobbyParams.Mode == evr.ModeArenaPublic || lobbyParams.Mode == evr.ModeCombatPublic {
		rankPercentile = lobbyParams.RankPercentile.Load()
	}

	for _, m := range filteredMatches {
		rtt := rtts[m.State.GameServer.Endpoint.GetExternalIP()]
		if rtt == 0 {
			rtt = 999
		}

		rankDelta := math.Abs(m.State.RankPercentile - rankPercentile)
		item := backfillSortItem{
			match:               m,
			rtt:                 rtt,
			rankPercentileDelta: rankDelta,
			openSlots:           m.State.OpenPlayerSlots(),
			withinRankRange:     rankDelta <= lobbyParams.RankPercentileMaxDelta,
			withinRTTRange:      rtt <= lobbyParams.MaxServerRTT,
		}

		// Skip matches that are full
		if item.openSlots < partySize {
			continue
		}

		// Skip matches with no RTT or RTT above the max allowed
		if !item.withinRTTRange {
			continue
		}

		// Skip matches that are too new
		if lobbyParams.Mode != evr.ModeSocialPublic && time.Since(m.State.CreatedAt) < 10*time.Second {
			continue
		}

		items = append(items, item)
	}

	// Sort the matches by open slots and then by latency
	slices.SortStableFunc(items, func(a, b backfillSortItem) int {
		// Rank by RTT
		if a.withinRTTRange && !b.withinRTTRange {
			return -1
		}
		if !a.withinRTTRange && b.withinRTTRange {
			return 1
		}

		// Sort social lobbies by least open slots (largest population)
		if lobbyParams.Mode == evr.ModeSocialPublic {
			return a.openSlots - b.openSlots
		}

		// If the rank delta is within the acceptable range, sort by population
		if a.withinRankRange && !b.withinRankRange {
			return -1
		}
		if !a.withinRankRange && b.withinRankRange {
			return 1
		}

		// If the rtt's are below 100 and within 30ms of each other, sort by rank percentile
		if a.rtt < 100 && b.rtt < 100 && math.Abs(float64(a.rtt-b.rtt)) < 30 {
			return int((a.rankPercentileDelta - b.rankPercentileDelta) * 100)
		}

		// Sort by RTT
		return a.rtt - b.rtt
	})

	// Return the sorted matches
	sortedMatches := make([]*MatchLabelMeta, 0, len(items))
	for _, item := range items {
		sortedMatches = append(sortedMatches, item.match)
	}
	return sortedMatches
}
