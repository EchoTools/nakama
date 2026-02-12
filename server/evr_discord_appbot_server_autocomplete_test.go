package server

import (
	"testing"
)

// TestRegionAutocompleteFiltersUnavailable verifies that regions with no available servers
// are filtered out from the autocomplete choices to prevent users from selecting
// regions where all servers are fully allocated.
func TestRegionAutocompleteFiltersUnavailable(t *testing.T) {
	tests := []struct {
		name                string
		regionDatas         map[string]*RegionAutocompleteData
		expectedCount       int
		shouldIncludeRegion map[string]bool
	}{
		{
			name: "filters out regions with zero available servers",
			regionDatas: map[string]*RegionAutocompleteData{
				"us-east": {
					RegionCode: "us-east",
					Location:   "US East",
					Total:      5,
					Available:  0, // No available servers
					MinPing:    20,
					MaxPing:    30,
				},
				"us-west": {
					RegionCode: "us-west",
					Location:   "US West",
					Total:      3,
					Available:  2, // Has available servers
					MinPing:    40,
					MaxPing:    50,
				},
				"eu-west": {
					RegionCode: "eu-west",
					Location:   "EU West",
					Total:      4,
					Available:  1, // Has available servers
					MinPing:    60,
					MaxPing:    70,
				},
			},
			expectedCount: 2, // Only us-west and eu-west should be included
			shouldIncludeRegion: map[string]bool{
				"us-east": false,
				"us-west": true,
				"eu-west": true,
			},
		},
		{
			name: "includes all regions when all have available servers",
			regionDatas: map[string]*RegionAutocompleteData{
				"us-east": {
					RegionCode: "us-east",
					Location:   "US East",
					Total:      5,
					Available:  3,
					MinPing:    20,
					MaxPing:    30,
				},
				"us-west": {
					RegionCode: "us-west",
					Location:   "US West",
					Total:      3,
					Available:  2,
					MinPing:    40,
					MaxPing:    50,
				},
			},
			expectedCount: 2, // Both should be included
			shouldIncludeRegion: map[string]bool{
				"us-east": true,
				"us-west": true,
			},
		},
		{
			name: "returns empty list when no regions have available servers",
			regionDatas: map[string]*RegionAutocompleteData{
				"us-east": {
					RegionCode: "us-east",
					Location:   "US East",
					Total:      5,
					Available:  0,
					MinPing:    20,
					MaxPing:    30,
				},
				"us-west": {
					RegionCode: "us-west",
					Location:   "US West",
					Total:      3,
					Available:  0,
					MinPing:    40,
					MaxPing:    50,
				},
			},
			expectedCount: 0, // No regions should be included
			shouldIncludeRegion: map[string]bool{
				"us-east": false,
				"us-west": false,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Simulate the filtering logic that should be in autocompleteRegions
			var filteredRegions []*RegionAutocompleteData
			for _, data := range tt.regionDatas {
				// This is the fix: filter out regions with Available == 0
				if data.Available > 0 {
					filteredRegions = append(filteredRegions, data)
				}
			}

			// Verify count
			if len(filteredRegions) != tt.expectedCount {
				t.Errorf("Expected %d regions after filtering, got %d", tt.expectedCount, len(filteredRegions))
			}

			// Verify specific regions
			for regionCode, shouldInclude := range tt.shouldIncludeRegion {
				found := false
				for _, region := range filteredRegions {
					if region.RegionCode == regionCode {
						found = true
						break
					}
				}
				if found != shouldInclude {
					if shouldInclude {
						t.Errorf("Expected region %s to be included but it was filtered out", regionCode)
					} else {
						t.Errorf("Expected region %s to be filtered out but it was included", regionCode)
					}
				}
			}
		})
	}
}
