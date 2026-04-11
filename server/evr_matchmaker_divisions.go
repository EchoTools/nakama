package server

import (
	"slices"

	"github.com/heroiclabs/nakama-common/runtime"
)

type Division int

const (
	DivisionGreen Division = iota
	DivisionBronze
	DivisionSilver
	DivisionGold
	DivisionPlatinum
	DivisionDiamond
	DivisionMaster
)

func (d Division) String() string {
	return [...]string{"green", "bronze", "silver", "gold", "platinum", "diamond", "master"}[d]
}

func (d Division) MarshalText() ([]byte, error) {
	return []byte(d.String()), nil
}

func (d *Division) UnmarshalText(text []byte) error {
	*d = DivisionFromName(string(text))
	return nil
}

func DivisionFromName(name string) Division {
	switch name {
	case "green":
		return DivisionGreen
	case "bronze":
		return DivisionBronze
	case "silver":
		return DivisionSilver
	case "gold":
		return DivisionGold
	case "platinum":
		return DivisionPlatinum
	case "diamond":
		return DivisionDiamond
	case "master":
		return DivisionMaster
	default:
		return DivisionGreen
	}
}

// AllDivisionNames returns a list of all valid division names
func AllDivisionNames() []string {
	return []string{"green", "bronze", "silver", "gold", "platinum", "diamond", "master"}
}

// RemoveFromStringSlice removes all occurrences of a value from a string slice
// Returns the modified slice and a boolean indicating if any removals occurred
func RemoveFromStringSlice(slice []string, value string) ([]string, bool) {
	removed := false
	for i := 0; i < len(slice); i++ {
		if slice[i] == value {
			slice = slices.Delete(slice, i, i+1)
			i--
			removed = true
		}
	}
	return slice, removed
}

// AssignDivision determines which skill division a player belongs to based on
// their mu rating and games played. New players (gamesPlayed < newPlayerThreshold)
// are always placed in the lowest division regardless of mu.
//
// boundaries defines the mu thresholds between divisions. For example,
// boundaries=[15, 25, 35] with names=["Bronze", "Silver", "Gold", "Diamond"]
// means: mu < 15 -> Bronze, 15 <= mu < 25 -> Silver, etc.
//
// len(names) must equal len(boundaries) + 1.
func AssignDivision(mu float64, gamesPlayed int, newPlayerThreshold int, boundaries []float64, names []string) string {
	if len(names) == 0 {
		return ""
	}

	// New players always go to the lowest division.
	if gamesPlayed < newPlayerThreshold {
		return names[0]
	}

	// Walk the boundaries to find the right bracket.
	for i, boundary := range boundaries {
		if mu < boundary {
			return names[i]
		}
	}

	// mu >= all boundaries -> highest division.
	return names[len(names)-1]
}

// HighestDivision returns the highest-ranked division from the given list,
// ordered by position in the names slice. Used for party division assignment:
// a party plays at the highest member's division.
func HighestDivision(divisions []string, names []string) string {
	if len(divisions) == 0 || len(names) == 0 {
		return ""
	}

	// Build a rank lookup from names (higher index = higher rank).
	rank := make(map[string]int, len(names))
	for i, name := range names {
		rank[name] = i
	}

	best := divisions[0]
	bestRank := rank[best]
	for _, d := range divisions[1:] {
		if r, ok := rank[d]; ok && r > bestRank {
			best = d
			bestRank = r
		}
	}
	return best
}

// FilterEntriesByDivision groups matchmaker entries by their "division"
// string property. Entries without a division property are grouped under "".
func FilterEntriesByDivision(entries []runtime.MatchmakerEntry) map[string][]runtime.MatchmakerEntry {
	result := make(map[string][]runtime.MatchmakerEntry)
	for _, entry := range entries {
		div, _ := entry.GetProperties()["division"].(string)
		result[div] = append(result[div], entry)
	}
	return result
}
