package server

import (
	"testing"

	"github.com/bwmarrin/discordgo"
)

// TestReserveAdd_ClassOptionIsOptional verifies --class parameter exists, is optional, and has correct choices
func TestReserveAdd_ClassOptionIsOptional(t *testing.T) {
	cmd := GetReserveCommandDefinition()
	if cmd == nil {
		t.Fatal("GetReserveCommandDefinition returned nil")
	}

	// Find the "add" subcommand
	var addSubcommand *discordgo.ApplicationCommandOption
	for _, opt := range cmd.Options {
		if opt.Type == discordgo.ApplicationCommandOptionSubCommand && opt.Name == "add" {
			addSubcommand = opt
			break
		}
	}

	if addSubcommand == nil {
		t.Fatal("add subcommand not found")
	}

	// Find the "class" option within "add" subcommand
	var classOption *discordgo.ApplicationCommandOption
	for _, opt := range addSubcommand.Options {
		if opt.Name == "class" {
			classOption = opt
			break
		}
	}

	if classOption == nil {
		t.Error("class option not found in /reserve add command")
		return
	}

	if classOption.Required {
		t.Error("class option should be optional (Required: false)")
	}

	if classOption.Type != 3 {
		t.Errorf("class option type should be String (3), got %d", classOption.Type)
	}

	if len(classOption.Choices) != 5 {
		t.Errorf("class option should have 5 choices, got %d", len(classOption.Choices))
	}

	expectedChoices := map[string]string{
		"League":    "league",
		"Scrimmage": "scrimmage",
		"Mixed":     "mixed",
		"Pickup":    "pickup",
		"None":      "none",
	}

	for _, choice := range classOption.Choices {
		if expectedChoices[choice.Name] != choice.Value {
			t.Errorf("unexpected choice: Name=%s, Value=%s", choice.Name, choice.Value)
		}
	}
}

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
