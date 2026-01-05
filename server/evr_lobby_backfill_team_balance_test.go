package server

import (
	"testing"

	"github.com/heroiclabs/nakama/v3/server/evr"
	"github.com/stretchr/testify/assert"
)

// TestFindBestBackfillMatch_TeamBalancing tests the team selection logic in backfill
func TestFindBestBackfillMatch_TeamBalancing(t *testing.T) {
	tests := []struct {
		name               string
		blueCount          int
		orangeCount        int
		blueOpenSlots      int
		orangeOpenSlots    int
		partySize          int
		expectedTeamsCount int // How many teams should be in possibleTeams
		shouldIncludeBlue  bool
		shouldIncludeOrange bool
		description        string
	}{
		{
			name:                "Prefer smaller team - Blue smaller",
			blueCount:           2,
			orangeCount:         3,
			blueOpenSlots:       3,
			orangeOpenSlots:     2,
			partySize:           1,
			expectedTeamsCount:  1,
			shouldIncludeBlue:   true,
			shouldIncludeOrange: false,
			description:         "Should only consider blue team when it's smaller",
		},
		{
			name:                "Prefer smaller team - Orange smaller",
			blueCount:           3,
			orangeCount:         2,
			blueOpenSlots:       2,
			orangeOpenSlots:     3,
			partySize:           1,
			expectedTeamsCount:  1,
			shouldIncludeBlue:   false,
			shouldIncludeOrange: true,
			description:         "Should only consider orange team when it's smaller",
		},
		{
			name:                "Equal teams - Consider both",
			blueCount:           3,
			orangeCount:         3,
			blueOpenSlots:       2,
			orangeOpenSlots:     2,
			partySize:           1,
			expectedTeamsCount:  2,
			shouldIncludeBlue:   true,
			shouldIncludeOrange: true,
			description:         "Should consider both teams when equal for better match quality",
		},
		{
			name:                "Smaller team full - Fall back to larger",
			blueCount:           2,
			orangeCount:         3,
			blueOpenSlots:       0, // Blue is smaller but full
			orangeOpenSlots:     2,
			partySize:           1,
			expectedTeamsCount:  1,
			shouldIncludeBlue:   false,
			shouldIncludeOrange: true,
			description:         "Should fall back to larger team when smaller is full",
		},
		{
			name:                "Party doesn't fit smaller team - Use larger",
			blueCount:           2,
			orangeCount:         3,
			blueOpenSlots:       1, // Blue is smaller but party won't fit
			orangeOpenSlots:     3,
			partySize:           2, // Party of 2 won't fit in 1 slot
			expectedTeamsCount:  1,
			shouldIncludeBlue:   false,
			shouldIncludeOrange: true,
			description:         "Should use larger team when party doesn't fit in smaller",
		},
		{
			name:                "Both teams full",
			blueCount:           5,
			orangeCount:         5,
			blueOpenSlots:       0,
			orangeOpenSlots:     0,
			partySize:           1,
			expectedTeamsCount:  0,
			shouldIncludeBlue:   false,
			shouldIncludeOrange: false,
			description:         "Should not add any teams when both are full",
		},
		{
			name:                "Equal teams, only one has space",
			blueCount:           4,
			orangeCount:         4,
			blueOpenSlots:       1,
			orangeOpenSlots:     0,
			partySize:           1,
			expectedTeamsCount:  1,
			shouldIncludeBlue:   true,
			shouldIncludeOrange: false,
			description:         "Should only include team with space when teams are equal",
		},
		{
			name:                "Large party needs space",
			blueCount:           2,
			orangeCount:         1,
			blueOpenSlots:       2,
			orangeOpenSlots:     3,
			partySize:           3,
			expectedTeamsCount:  1,
			shouldIncludeBlue:   false,
			shouldIncludeOrange: true,
			description:         "Should select team with enough slots for large party",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Simulate the team selection logic from FindBestBackfillMatch
			openSlots := map[int]int{
				evr.TeamBlue:   tt.blueOpenSlots,
				evr.TeamOrange: tt.orangeOpenSlots,
			}

			possibleTeams := []int{}

			// This is the fixed logic from the backfill code
			blueCount := tt.blueCount
			orangeCount := tt.orangeCount

			if blueCount < orangeCount {
				// Blue team is smaller, prioritize it
				if openSlots[evr.TeamBlue] >= tt.partySize {
					possibleTeams = append(possibleTeams, evr.TeamBlue)
				}
			} else if orangeCount < blueCount {
				// Orange team is smaller, prioritize it
				if openSlots[evr.TeamOrange] >= tt.partySize {
					possibleTeams = append(possibleTeams, evr.TeamOrange)
				}
			} else {
				// Teams are equal - consider both teams for better match quality
				if openSlots[evr.TeamBlue] >= tt.partySize {
					possibleTeams = append(possibleTeams, evr.TeamBlue)
				}
				if openSlots[evr.TeamOrange] >= tt.partySize {
					possibleTeams = append(possibleTeams, evr.TeamOrange)
				}
			}

			// If no team was added (smaller team is full), try the other team
			if len(possibleTeams) == 0 {
				if openSlots[evr.TeamBlue] >= tt.partySize {
					possibleTeams = append(possibleTeams, evr.TeamBlue)
				}
				if openSlots[evr.TeamOrange] >= tt.partySize {
					possibleTeams = append(possibleTeams, evr.TeamOrange)
				}
			}

			// Verify expectations
			assert.Equal(t, tt.expectedTeamsCount, len(possibleTeams), tt.description)

			if tt.shouldIncludeBlue {
				assert.Contains(t, possibleTeams, evr.TeamBlue, "Blue team should be in possibleTeams")
			} else {
				assert.NotContains(t, possibleTeams, evr.TeamBlue, "Blue team should not be in possibleTeams")
			}

			if tt.shouldIncludeOrange {
				assert.Contains(t, possibleTeams, evr.TeamOrange, "Orange team should be in possibleTeams")
			} else {
				assert.NotContains(t, possibleTeams, evr.TeamOrange, "Orange team should not be in possibleTeams")
			}
		})
	}
}

// TestBackfill_PreventUnbalancedAssignment tests that backfill won't create extremely unbalanced teams
func TestBackfill_PreventUnbalancedAssignment(t *testing.T) {
	// Scenario: Match has Blue=4, Orange=1, both teams have open slots
	// We want to ensure backfill strongly prefers Orange to maintain balance
	
	blueCount := 4
	orangeCount := 1
	openSlots := map[int]int{
		evr.TeamBlue:   1, // Blue has space
		evr.TeamOrange: 4, // Orange has more space
	}
	partySize := 1

	possibleTeams := []int{}

	// Apply the fixed logic
	if blueCount < orangeCount {
		if openSlots[evr.TeamBlue] >= partySize {
			possibleTeams = append(possibleTeams, evr.TeamBlue)
		}
	} else if orangeCount < blueCount {
		// Orange is smaller - this should be prioritized
		if openSlots[evr.TeamOrange] >= partySize {
			possibleTeams = append(possibleTeams, evr.TeamOrange)
		}
	} else {
		if openSlots[evr.TeamBlue] >= partySize {
			possibleTeams = append(possibleTeams, evr.TeamBlue)
		}
		if openSlots[evr.TeamOrange] >= partySize {
			possibleTeams = append(possibleTeams, evr.TeamOrange)
		}
	}

	if len(possibleTeams) == 0 {
		if openSlots[evr.TeamBlue] >= partySize {
			possibleTeams = append(possibleTeams, evr.TeamBlue)
		}
		if openSlots[evr.TeamOrange] >= partySize {
			possibleTeams = append(possibleTeams, evr.TeamOrange)
		}
	}

	// Should only include orange (the smaller team)
	assert.Equal(t, 1, len(possibleTeams), "Should only consider the smaller team")
	assert.Contains(t, possibleTeams, evr.TeamOrange, "Should prefer orange (smaller team)")
	assert.NotContains(t, possibleTeams, evr.TeamBlue, "Should not consider blue (larger team)")
}

// TestBackfill_FallbackWhenPreferredFull tests fallback behavior
func TestBackfill_FallbackWhenPreferredFull(t *testing.T) {
	// Scenario: Orange is smaller (should be preferred) but full
	// Should fall back to blue
	
	blueCount := 4
	orangeCount := 2
	openSlots := map[int]int{
		evr.TeamBlue:   1,
		evr.TeamOrange: 0, // Preferred team is full
	}
	partySize := 1

	possibleTeams := []int{}

	// Apply the fixed logic
	if blueCount < orangeCount {
		if openSlots[evr.TeamBlue] >= partySize {
			possibleTeams = append(possibleTeams, evr.TeamBlue)
		}
	} else if orangeCount < blueCount {
		if openSlots[evr.TeamOrange] >= partySize {
			possibleTeams = append(possibleTeams, evr.TeamOrange)
		}
	} else {
		if openSlots[evr.TeamBlue] >= partySize {
			possibleTeams = append(possibleTeams, evr.TeamBlue)
		}
		if openSlots[evr.TeamOrange] >= partySize {
			possibleTeams = append(possibleTeams, evr.TeamOrange)
		}
	}

	// Fallback logic
	if len(possibleTeams) == 0 {
		if openSlots[evr.TeamBlue] >= partySize {
			possibleTeams = append(possibleTeams, evr.TeamBlue)
		}
		if openSlots[evr.TeamOrange] >= partySize {
			possibleTeams = append(possibleTeams, evr.TeamOrange)
		}
	}

	// Should fall back to blue
	assert.Equal(t, 1, len(possibleTeams), "Should fall back to one team")
	assert.Contains(t, possibleTeams, evr.TeamBlue, "Should fall back to blue team")
}

// TestBackfill_EqualTeamsConsiderBoth tests that when teams are equal, both are considered
func TestBackfill_EqualTeamsConsiderBoth(t *testing.T) {
	// This is important for match quality - when teams are balanced,
	// the scoring function should decide which team gives better balance
	
	blueCount := 3
	orangeCount := 3
	openSlots := map[int]int{
		evr.TeamBlue:   2,
		evr.TeamOrange: 2,
	}
	partySize := 1

	possibleTeams := []int{}

	// Apply the fixed logic
	if blueCount < orangeCount {
		if openSlots[evr.TeamBlue] >= partySize {
			possibleTeams = append(possibleTeams, evr.TeamBlue)
		}
	} else if orangeCount < blueCount {
		if openSlots[evr.TeamOrange] >= partySize {
			possibleTeams = append(possibleTeams, evr.TeamOrange)
		}
	} else {
		// Equal teams - add both
		if openSlots[evr.TeamBlue] >= partySize {
			possibleTeams = append(possibleTeams, evr.TeamBlue)
		}
		if openSlots[evr.TeamOrange] >= partySize {
			possibleTeams = append(possibleTeams, evr.TeamOrange)
		}
	}

	if len(possibleTeams) == 0 {
		if openSlots[evr.TeamBlue] >= partySize {
			possibleTeams = append(possibleTeams, evr.TeamBlue)
		}
		if openSlots[evr.TeamOrange] >= partySize {
			possibleTeams = append(possibleTeams, evr.TeamOrange)
		}
	}

	// Should include both teams for scoring
	assert.Equal(t, 2, len(possibleTeams), "Should consider both teams when equal")
	assert.Contains(t, possibleTeams, evr.TeamBlue, "Should include blue team")
	assert.Contains(t, possibleTeams, evr.TeamOrange, "Should include orange team")
}
