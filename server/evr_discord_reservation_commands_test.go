package server

import (
	"testing"
)

func TestReserveCommand_WithClass(t *testing.T) {
	tests := []struct {
		name          string
		classValue    string
		expectedClass SessionClassification
	}{
		{"league classification", "league", ClassificationLeague},
		{"scrimmage classification", "scrimmage", ClassificationScrimmage},
		{"mixed classification", "mixed", ClassificationMixed},
		{"pickup classification", "pickup", ClassificationPickup},
		{"none classification", "none", ClassificationNone},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			classification := ParseSessionClassification(tt.classValue)

			if classification != tt.expectedClass {
				t.Errorf("ParseSessionClassification(%q) = %d, want %d", tt.classValue, classification, tt.expectedClass)
			}
		})
	}
}

func TestReserveCommand_DefaultsToNone(t *testing.T) {
	var classificationStr string

	classification := ParseSessionClassification(classificationStr)

	if classification != ClassificationNone {
		t.Errorf("ParseSessionClassification(empty) = %d, want %d (ClassificationNone)", classification, ClassificationNone)
	}
}

// TestReservationActivation_SparkLinkInDM tests that reservation activation DMs include spark link
func TestReservationActivation_SparkLinkInDM(t *testing.T) {
	tests := []struct {
		name    string
		matchID string
		wantURL string
	}{
		{
			name:    "spark link with match ID",
			matchID: "550e8400-e29b-41d4-a716-446655440000",
			wantURL: "https://echo.taxi/spark://c/550E8400-E29B-41D4-A716-446655440000",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dmContent := BuildReservationActivationDM(tt.matchID)
			if dmContent == "" {
				t.Fatal("BuildReservationActivationDM returned empty string")
			}
			if !contains(dmContent, tt.wantURL) {
				t.Errorf("BuildReservationActivationDM(%q) missing spark link %q\nGot: %s", tt.matchID, tt.wantURL, dmContent)
			}
		})
	}
}

// contains is a helper function to check if a string contains a substring
func contains(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
