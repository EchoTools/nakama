package service

import (
	"context"

	"github.com/gofrs/uuid/v5"
	"github.com/heroiclabs/nakama-common/runtime"
)

// TelemetryIntegration provides integration points for telemetry with existing EVR systems
type TelemetryIntegration struct {
	eventJournal      *EventJournal
	telemetryManager  *LobbyTelemetryManager
	matchSummaryStore *MatchSummaryStore
	logger            runtime.Logger
}

// NewTelemetryIntegration creates a new telemetry integration instance
func NewTelemetryIntegration(eventJournal *EventJournal, telemetryManager *LobbyTelemetryManager, matchSummaryStore *MatchSummaryStore, logger runtime.Logger) *TelemetryIntegration {
	return &TelemetryIntegration{
		eventJournal:      eventJournal,
		telemetryManager:  telemetryManager,
		matchSummaryStore: matchSummaryStore,
		logger:            logger,
	}
}

// HandleSNSTelemetryEvent processes SNSTelemetryEvent messages and journals them
func (ti *TelemetryIntegration) HandleSNSTelemetryEvent(ctx context.Context, sessionID, userID, lobbyID string, telemetryData map[string]interface{}) error {
	if ti.eventJournal == nil {
		return nil // Graceful degradation when journaling is not available
	}

	// Journal the telemetry event
	if err := ti.eventJournal.JournalTelemetry(ctx, userID, sessionID, lobbyID, telemetryData); err != nil {
		ti.logger.Error("Failed to journal telemetry event: %v", err)
		return err
	}

	// Broadcast to subscribed sessions if telemetry manager is available
	if ti.telemetryManager != nil {
		lobbyUUID, err := uuid.FromString(lobbyID)
		if err != nil {
			ti.logger.Error("Invalid lobby ID for telemetry broadcast: %v", err)
			return err
		}

		if err := ti.telemetryManager.BroadcastTelemetry(ctx, lobbyUUID, telemetryData); err != nil {
			ti.logger.Error("Failed to broadcast telemetry: %v", err)
			return err
		}
	}

	return nil
}

// HandleMatchEnd processes match end events and creates match summaries
// Added playerPings: map from playerID to slice of ping samples (float64)
func (ti *TelemetryIntegration) HandleMatchEnd(ctx context.Context, matchID string, players []string, duration int, label string, playerPings map[string][]float64) error {
	if ti.matchSummaryStore == nil {
		return nil // Graceful degradation when MongoDB is not available
	}

	// Calculate ping statistics from playerPings
	var minPing, maxPing, avgPing float64
	var pingCount int
	var pingSum float64
	minPing = -1
	maxPing = -1
	for _, pings := range playerPings {
		for _, ping := range pings {
			if minPing == -1 || ping < minPing {
				minPing = ping
			}
			if maxPing == -1 || ping > maxPing {
				maxPing = ping
			}
			pingSum += ping
			pingCount++
		}
	}
	if pingCount > 0 {
		avgPing = pingSum / float64(pingCount)
	} else {
		minPing = 0
		maxPing = 0
		avgPing = 0
	}

	// Extract final scores (example implementation)
	finalScores := make(map[string]int)
	for i, player := range players {
		finalScores[player] = 100 - (i * 10) // Example scoring
	}

	// Create per-round stats (example implementation)
	perRoundStats := make(map[string][]RoundStat)
	for i, player := range players {
		score := 100 - (i * 10)
		perRoundStats[player] = []RoundStat{
			{Round: 1, Score: score / 2},
			{Round: 2, Score: score / 2},
		}
	}

	// Create match summary
	summary := &MatchSummary{
		MatchID:           matchID,
		Players:           players,
		PerRoundStats:     perRoundStats,
		DurationSeconds:   duration,
		MinPing:           int(minPing),
		MaxPing:           int(maxPing),
		AvgPing:           avgPing,
		FinalRoundScores:  finalScores,
		EvrLobbyPresences: nil, // Would be populated with actual presence data
		MatchLabel:        label,
	}

	// Store in MongoDB
	if err := ti.matchSummaryStore.Store(ctx, summary); err != nil {
		ti.logger.Error("Failed to store match summary: %v", err)
		return err
	}

	ti.logger.Info("Match summary stored successfully: match_id=%s, players=%d", summary.MatchID, len(summary.Players))
	return nil
}

// HandlePlayerAction processes player action events
func (ti *TelemetryIntegration) HandlePlayerAction(ctx context.Context, userID, sessionID, matchID, action string, actionData map[string]interface{}) error {
	if ti.eventJournal == nil {
		return nil
	}

	return ti.eventJournal.JournalPlayerAction(ctx, userID, sessionID, matchID, action, actionData)
}

// HandlePurchase processes purchase events
func (ti *TelemetryIntegration) HandlePurchase(ctx context.Context, userID, sessionID string, purchaseData map[string]interface{}) error {
	if ti.eventJournal == nil {
		return nil
	}

	return ti.eventJournal.JournalPurchase(ctx, userID, sessionID, purchaseData)
}

// Helper function to calculate ping statistics - simplified for demo
func calculatePingStats() (min, max int, avg float64) {
	// Example implementation - in real usage this would process actual ping data
	return 10, 50, 25.5
}

// Example integration function that could be called from EVR match handling
func IntegrateTelemetryWithEVRMatch(nk runtime.NakamaModule, logger runtime.Logger, integration *TelemetryIntegration) {
	// This would be called during match events to integrate telemetry
	// For example, in the EVR match signal handler or data message handler

	logger.Info("Telemetry integration ready for EVR matches")

	// Example usage in a match handler:
	/*
		switch opcode {
		case OpCodeEVRPacketData:
			// Handle regular EVR packet data

			// Extract telemetry if it's a telemetry packet
			if isTelemetryPacket(data) {
				telemetryData := extractTelemetryData(data)
				integration.HandleSNSTelemetryEvent(ctx, sessionID, userID, lobbyID, telemetryData)
			}

		case OpCodeMatchGameStateUpdate:
			// Handle match state updates

			// If match is ending, create summary
			if isMatchEnding(state) {
				integration.HandleMatchEnd(ctx, state)
			}
		}
	*/
}
