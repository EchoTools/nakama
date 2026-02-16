package server

import (
	"testing"
)

func TestAllocateCommand_WithClass(t *testing.T) {
	tests := []struct {
		name               string
		classValue         string
		wantClassification SessionClassification
	}{
		{"none", "none", ClassificationNone},
		{"pickup", "pickup", ClassificationPickup},
		{"mixed", "mixed", ClassificationMixed},
		{"scrimmage", "scrimmage", ClassificationScrimmage},
		{"league", "league", ClassificationLeague},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			classification := ParseSessionClassification(tt.classValue)
			if classification != tt.wantClassification {
				t.Errorf("ParseSessionClassification(%q) = %v, want %v", tt.classValue, classification, tt.wantClassification)
			}
		})
	}
}

func TestAllocateCommand_DefaultsToNone(t *testing.T) {
	classification := ParseSessionClassification("")
	if classification != ClassificationNone {
		t.Errorf("ParseSessionClassification(\"\") = %v, want %v", classification, ClassificationNone)
	}

	classification = ParseSessionClassification("invalid")
	if classification != ClassificationNone {
		t.Errorf("ParseSessionClassification(\"invalid\") = %v, want %v", classification, ClassificationNone)
	}
}
