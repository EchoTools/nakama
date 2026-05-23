package server

import "math"

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
