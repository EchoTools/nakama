package server

import (
	"context"
	"fmt"

	"github.com/heroiclabs/nakama/v3/server/evr"
)

func (p *EvrPipeline) updatePlayerStats(ctx context.Context, userID, groupID, displayName string, update evr.ServerProfileUpdate, mode evr.Symbol) error {
	var stats evr.Statistics

	// Select the correct statistics based on the mode
	switch mode {
	case evr.ModeArenaPublic:
		if update.Statistics.Arena == nil {
			return fmt.Errorf("missing arena statistics")
		}
		stats = update.Statistics.Arena
	case evr.ModeCombatPublic:
		if update.Statistics.Combat == nil {
			return fmt.Errorf("missing combat statistics")
		}
		stats = update.Statistics.Combat
	default:
		return fmt.Errorf("unknown mode: %s", mode)
	}

	// Get the players existing statistics
	prevPlayerStats, _, err := PlayerStatisticsGetID(ctx, p.db, p.nk, userID, groupID, []evr.Symbol{mode}, mode)
	if err != nil {
		return fmt.Errorf("failed to get player statistics: %w", err)
	}
	g := evr.StatisticsGroup{
		Mode:          mode,
		ResetSchedule: evr.ResetScheduleAllTime,
	}

	// Use defaults if the player has no existing statistics
	prevStats, ok := prevPlayerStats[g]
	if !ok {
		prevStats = evr.NewServerProfile().Statistics[g]
	}

	entries, err := StatisticsToEntries(userID, displayName, groupID, mode, prevStats, stats)
	if err != nil {
		return fmt.Errorf("failed to convert statistics to entries: %w", err)
	}

	return p.statisticsQueue.Add(entries)
}
