package server

import (
	"testing"
)

// TestFilterAndSortRegionChoices verifies that regions with no available servers
// are filtered out from the autocomplete choices, that remaining regions are
// sorted by latency, and that non-privileged users cannot see regions with
// ping > MaxRegionPingMs or unknown ping.
func TestFilterAndSortRegionChoices(t *testing.T) {
	tests := []struct {
		name                string
		regionDatas         map[string]*RegionAutocompleteData
		privileged          bool
		expectedCount       int
		expectedOrder       []string // Region codes in expected order
		shouldIncludeRegion map[string]bool
	}{
		{
			name: "filters out regions with zero available servers",
			regionDatas: map[string]*RegionAutocompleteData{
				"us-east": {
					RegionCode: "us-east",
					Location:   "US East",
					Total:      5,
					Available:  0, // No available servers - should be filtered
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
				"eu-west": {
					RegionCode: "eu-west",
					Location:   "EU West",
					Total:      4,
					Available:  1,
					MinPing:    60,
					MaxPing:    70,
				},
			},
			privileged:    true,
			expectedCount: 2,
			expectedOrder: []string{"us-west", "eu-west"},
			shouldIncludeRegion: map[string]bool{
				"us-east": false,
				"us-west": true,
				"eu-west": true,
			},
		},
		{
			name: "privileged: sorts by min ping then max ping",
			regionDatas: map[string]*RegionAutocompleteData{
				"region-a": {
					RegionCode: "region-a",
					Location:   "Region A",
					Total:      3,
					Available:  1,
					MinPing:    50,
					MaxPing:    60,
				},
				"region-b": {
					RegionCode: "region-b",
					Location:   "Region B",
					Total:      3,
					Available:  1,
					MinPing:    30,
					MaxPing:    40,
				},
				"region-c": {
					RegionCode: "region-c",
					Location:   "Region C",
					Total:      3,
					Available:  1,
					MinPing:    30,
					MaxPing:    35,
				},
			},
			privileged:    true,
			expectedCount: 3,
			expectedOrder: []string{"region-c", "region-b", "region-a"},
			shouldIncludeRegion: map[string]bool{
				"region-a": true,
				"region-b": true,
				"region-c": true,
			},
		},
		{
			name: "privileged: regions with no ping data sort to end",
			regionDatas: map[string]*RegionAutocompleteData{
				"us-east": {
					RegionCode: "us-east",
					Location:   "US East",
					Total:      3,
					Available:  1,
					MinPing:    0,
					MaxPing:    0,
				},
				"us-west": {
					RegionCode: "us-west",
					Location:   "US West",
					Total:      3,
					Available:  2,
					MinPing:    40,
					MaxPing:    50,
				},
				"eu-west": {
					RegionCode: "eu-west",
					Location:   "EU West",
					Total:      4,
					Available:  1,
					MinPing:    60,
					MaxPing:    70,
				},
			},
			privileged:    true,
			expectedCount: 3,
			expectedOrder: []string{"us-west", "eu-west", "us-east"},
			shouldIncludeRegion: map[string]bool{
				"us-east": true,
				"us-west": true,
				"eu-west": true,
			},
		},
		{
			name: "privileged: high ping regions included",
			regionDatas: map[string]*RegionAutocompleteData{
				"us-west": {
					RegionCode: "us-west",
					Location:   "US West",
					Total:      3,
					Available:  2,
					MinPing:    40,
					MaxPing:    50,
				},
				"asia": {
					RegionCode: "asia",
					Location:   "Asia",
					Total:      3,
					Available:  1,
					MinPing:    150,
					MaxPing:    200,
				},
			},
			privileged:    true,
			expectedCount: 2,
			expectedOrder: []string{"us-west", "asia"},
			shouldIncludeRegion: map[string]bool{
				"us-west": true,
				"asia":    true,
			},
		},
		{
			name: "non-privileged: filters out high ping regions",
			regionDatas: map[string]*RegionAutocompleteData{
				"us-west": {
					RegionCode: "us-west",
					Location:   "US West",
					Total:      3,
					Available:  2,
					MinPing:    40,
					MaxPing:    50,
				},
				"eu-west": {
					RegionCode: "eu-west",
					Location:   "EU West",
					Total:      4,
					Available:  1,
					MinPing:    85,
					MaxPing:    90,
				},
				"asia": {
					RegionCode: "asia",
					Location:   "Asia",
					Total:      3,
					Available:  1,
					MinPing:    150,
					MaxPing:    200,
				},
			},
			privileged:    false,
			expectedCount: 2,
			expectedOrder: []string{"us-west", "eu-west"},
			shouldIncludeRegion: map[string]bool{
				"us-west": true,
				"eu-west": true,
				"asia":    false,
			},
		},
		{
			name: "non-privileged: filters out unknown ping regions",
			regionDatas: map[string]*RegionAutocompleteData{
				"us-west": {
					RegionCode: "us-west",
					Location:   "US West",
					Total:      3,
					Available:  2,
					MinPing:    40,
					MaxPing:    50,
				},
				"unknown": {
					RegionCode: "unknown",
					Location:   "Unknown",
					Total:      3,
					Available:  1,
					MinPing:    0,
					MaxPing:    0,
				},
			},
			privileged:    false,
			expectedCount: 1,
			expectedOrder: []string{"us-west"},
			shouldIncludeRegion: map[string]bool{
				"us-west": true,
				"unknown": false,
			},
		},
		{
			name: "non-privileged: exactly at threshold is included",
			regionDatas: map[string]*RegionAutocompleteData{
				"us-west": {
					RegionCode: "us-west",
					Location:   "US West",
					Total:      3,
					Available:  2,
					MinPing:    90,
					MaxPing:    95,
				},
			},
			privileged:    false,
			expectedCount: 1,
			expectedOrder: []string{"us-west"},
			shouldIncludeRegion: map[string]bool{
				"us-west": true,
			},
		},
		{
			name: "non-privileged: just over threshold is excluded",
			regionDatas: map[string]*RegionAutocompleteData{
				"us-west": {
					RegionCode: "us-west",
					Location:   "US West",
					Total:      3,
					Available:  2,
					MinPing:    91,
					MaxPing:    95,
				},
			},
			privileged:    false,
			expectedCount: 0,
			expectedOrder: []string{},
			shouldIncludeRegion: map[string]bool{
				"us-west": false,
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
			},
			privileged:    true,
			expectedCount: 0,
			expectedOrder: []string{},
			shouldIncludeRegion: map[string]bool{
				"us-east": false,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			choices := filterAndSortRegionChoices(tt.regionDatas, tt.privileged)

			if len(choices) != tt.expectedCount {
				t.Errorf("Expected %d regions after filtering, got %d", tt.expectedCount, len(choices))
			}

			if len(tt.expectedOrder) > 0 {
				for i, expectedRegionCode := range tt.expectedOrder {
					if i >= len(choices) {
						t.Errorf("Expected region %s at index %d, but only got %d choices", expectedRegionCode, i, len(choices))
						break
					}
					actualRegionCode := choices[i].Value.(string)
					if actualRegionCode != expectedRegionCode {
						t.Errorf("Expected region %s at index %d, got %s", expectedRegionCode, i, actualRegionCode)
					}
				}
			}

			for regionCode, shouldInclude := range tt.shouldIncludeRegion {
				found := false
				for _, choice := range choices {
					if choice.Value.(string) == regionCode {
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

			for _, choice := range choices {
				if choice.Name == "" {
					t.Error("Choice Name should not be empty")
				}
				if choice.Value == nil {
					t.Error("Choice Value should not be nil")
				}
				if _, ok := choice.Value.(string); !ok {
					t.Errorf("Choice Value should be string, got %T", choice.Value)
				}
			}
		})
	}
}

// TestRegionAutocompleteDataDescription tests the Description method formatting.
func TestRegionAutocompleteDataDescription(t *testing.T) {
	tests := []struct {
		name     string
		data     RegionAutocompleteData
		expected string
	}{
		{
			name: "same min and max ping",
			data: RegionAutocompleteData{
				RegionCode: "us-east",
				Location:   "US East",
				Total:      5,
				Available:  2,
				MinPing:    30,
				MaxPing:    30,
			},
			expected: "US East -- 30ms [3/5]",
		},
		{
			name: "different min and max ping",
			data: RegionAutocompleteData{
				RegionCode: "eu-west",
				Location:   "EU West",
				Total:      10,
				Available:  7,
				MinPing:    50,
				MaxPing:    80,
			},
			expected: "EU West -- 50ms-80ms [3/10]",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			actual := tt.data.Description()
			if actual != tt.expected {
				t.Errorf("Expected description %q, got %q", tt.expected, actual)
			}
		})
	}
}
