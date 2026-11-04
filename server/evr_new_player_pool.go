package server

import (
	"slices"
	"time"
)

// newPlayerPoolDivision is the stored division key for the new-player pool
// ("Students" to players). The key stays "green": the matchmaker reads the
// literal, and the display name is a label only.
const newPlayerPoolDivision = "green"

// applyNewPlayerPool places an account in or out of the new-player pool by
// editing the stored division lists, and reports whether it changed them.
//
// An account younger than maxAgeDays is put in the pool. Any other account
// loses it, unless it is a moderator who holds it (protected), and is excluded
// from it unless it is a moderator. haveCreateTime is false when the account
// has no creation time; such an account is treated as old.
func applyNewPlayerPool(settings *MatchmakingSettings, createTime time.Time, haveCreateTime bool, now time.Time, maxAgeDays int, isModerator bool) (updated bool) {
	hasGreenInDivisions := slices.Contains(settings.Divisions, newPlayerPoolDivision)
	hasGreenInExcluded := slices.Contains(settings.ExcludedDivisions, newPlayerPoolDivision)

	// Moderators with green in their divisions are protected from automatic removal.
	isProtectedModerator := isModerator && hasGreenInDivisions

	if haveCreateTime && now.Sub(createTime) < time.Duration(maxAgeDays)*24*time.Hour {
		if !hasGreenInDivisions {
			settings.Divisions = append(settings.Divisions, newPlayerPoolDivision)
			updated = true
		}
		if hasGreenInExcluded {
			updated = true
			settings.ExcludedDivisions, _ = RemoveFromStringSlice(settings.ExcludedDivisions, newPlayerPoolDivision)
		}
		return updated
	}

	// Old accounts lose green UNLESS they're protected moderators.
	if hasGreenInDivisions && !isProtectedModerator {
		updated = true
		settings.Divisions, _ = RemoveFromStringSlice(settings.Divisions, newPlayerPoolDivision)
	}
	// Only exclude if not already excluded, not a moderator (moderators manage
	// their own divisions), and green is not in divisions (removed or never had it).
	if !hasGreenInExcluded && !isModerator && !slices.Contains(settings.Divisions, newPlayerPoolDivision) {
		updated = true
		settings.ExcludedDivisions = append(settings.ExcludedDivisions, newPlayerPoolDivision)
	}
	return updated
}
