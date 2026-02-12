package server

import (
	"testing"
)

// TestMatchEntrantJoinCountMetricLabelConsistency documents that the metric
// "match_entrant_join_count" must use the same labels across all call sites.
//
// The metric is used in two locations:
// 1. MatchJoinAttempt (line ~397): When a player attempts to join a match
// 2. MatchJoin (line ~515): When a player successfully joins a match
//
// Both must use these labels:
// - mode: Game mode (e.g., arena, social)
// - level: Map/level name
// - type: Lobby type (public, private, etc.)
// - role: Player's team/role alignment
// - group_id: Match group ID
// - backfill: Whether this is a backfill join (after match has started)
//
// Prometheus requires all usages of the same metric to have identical label sets.
// If labels differ, the metric registration will fail with an error like:
// "a previously registered descriptor... has different label names"
func TestMatchEntrantJoinCountMetricLabelConsistency(t *testing.T) {
	// This test exists as documentation only.
	// The actual validation happens at runtime when Prometheus registers the metrics.
	// If the labels are inconsistent, the server will log an error at startup:
	// "Error registering Prometheus metric"
	t.Log("Metric label consistency is validated at runtime by Prometheus")
}
