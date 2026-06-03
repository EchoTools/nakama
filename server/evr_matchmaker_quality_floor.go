package server

import (
	"math"

	"github.com/heroiclabs/nakama/v3/server/evr"
)

// isCombatCandidate reports whether a predicted match is for the Combat mode
// (echo_combat / evr.ModeCombatPublic). Combat must NEVER be skill-gated at
// match creation (issue #479): SBMM is only allowed to sort players onto teams,
// not to decide whether a combat match forms. The mode is carried per-entry in
// the "game_mode" property, mirroring how groupEntriesSequentially and
// predictCandidateOutcomes read it.
func isCombatCandidate(prediction PredictedMatch) bool {
	if len(prediction.Candidate) == 0 {
		return false
	}
	modeStr, _ := prediction.Candidate[0].GetProperties()["game_mode"].(string)
	return modeStr == evr.ModeCombatPublic.String()
}

// computeQualityFloor calculates the effective quality floor for a given wait time.
// The floor decays linearly from QualityFloorInitial toward QualityFloorMinimum
// as the longest-waiting ticket ages.
func computeQualityFloor(settings *GlobalMatchmakingSettings, maxWaitSeconds float64) float64 {
	decayed := settings.QualityFloorInitial - (maxWaitSeconds * settings.QualityFloorDecayPerSecond)
	return math.Max(settings.QualityFloorMinimum, decayed)
}

// passesQualityFloor returns true if the prediction's draw probability
// meets or exceeds the floor threshold.
func passesQualityFloor(prediction PredictedMatch, floor float64) bool {
	return float64(prediction.DrawProb) >= floor
}

// filterByQualityFloor removes predictions whose draw probability falls below
// the dynamic quality floor. The floor is computed per-prediction based on the
// oldest ticket timestamp in that candidate. Returns the filtered slice.
//
// Predictions that contain a starving ticket (oldest wait time >=
// ReservationThresholdSecs) are exempt from the quality floor so that the
// reservation system can always rescue long-waiting players, regardless of
// draw probability.
//
// When settings are nil or EnableQualityFloor is false, all predictions pass through.
func filterByQualityFloor(predictions []PredictedMatch, settings *GlobalMatchmakingSettings, nowUnix int64) []PredictedMatch {
	if settings == nil || !settings.EnableQualityFloor {
		return predictions
	}

	// Determine the starving threshold used by the reservation system.
	// Default matches buildReservations (90s).
	starvingThreshold := int64(90)
	if settings.ReservationThresholdSecs > 0 {
		starvingThreshold = int64(settings.ReservationThresholdSecs)
	}

	filtered := make([]PredictedMatch, 0, len(predictions))
	for _, p := range predictions {
		// Combat (echo_combat) is never skill-gated at match creation (issue
		// #479). SBMM may only sort players onto teams for combat, so combat
		// candidates always pass the quality floor regardless of DrawProb. This
		// keeps combat match formation count/availability-only. Arena and other
		// modes are unaffected.
		if isCombatCandidate(p) {
			filtered = append(filtered, p)
			continue
		}

		maxWaitSeconds := float64(nowUnix - p.OldestTicketTimestamp)
		if maxWaitSeconds < 0 {
			maxWaitSeconds = 0
		}

		// Exempt starving candidates from the quality floor so the reservation
		// system can always form a match for long-waiting players.
		if nowUnix-p.OldestTicketTimestamp >= starvingThreshold {
			filtered = append(filtered, p)
			continue
		}

		floor := computeQualityFloor(settings, maxWaitSeconds)
		if passesQualityFloor(p, floor) {
			filtered = append(filtered, p)
		}
	}
	return filtered
}
