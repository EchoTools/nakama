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
// When settings are nil or EnableQualityFloor is false, all predictions pass through.
func filterByQualityFloor(predictions []PredictedMatch, settings *GlobalMatchmakingSettings, nowUnix int64) []PredictedMatch {
	if settings == nil || !settings.EnableQualityFloor {
		return predictions
	}

	filtered := make([]PredictedMatch, 0, len(predictions))
	for _, p := range predictions {
		maxWaitSeconds := float64(nowUnix - p.OldestTicketTimestamp)
		if maxWaitSeconds < 0 {
			maxWaitSeconds = 0
		}
		floor := computeQualityFloor(settings, maxWaitSeconds)
		if passesQualityFloor(p, floor) {
			filtered = append(filtered, p)
		}
	}
	return filtered
}
