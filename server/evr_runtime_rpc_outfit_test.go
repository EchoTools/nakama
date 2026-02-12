package server

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestOutfit_JSONMarshaling(t *testing.T) {
	outfit := Outfit{
		ID:      "test-id",
		Name:    "Test Outfit",
		Chassis: "chassis-123",
		Bracer:  "bracer-456",
		Booster: "booster-789",
		Decal:   "decal-012",
	}

	data, err := json.Marshal(outfit)
	if err != nil {
		t.Fatalf("Failed to marshal outfit: %v", err)
	}

	var unmarshaled Outfit
	if err := json.Unmarshal(data, &unmarshaled); err != nil {
		t.Fatalf("Failed to unmarshal outfit: %v", err)
	}

	if unmarshaled.ID != outfit.ID {
		t.Errorf("ID mismatch: got %s, want %s", unmarshaled.ID, outfit.ID)
	}
	if unmarshaled.Name != outfit.Name {
		t.Errorf("Name mismatch: got %s, want %s", unmarshaled.Name, outfit.Name)
	}
	if unmarshaled.Chassis != outfit.Chassis {
		t.Errorf("Chassis mismatch: got %s, want %s", unmarshaled.Chassis, outfit.Chassis)
	}
	if unmarshaled.Bracer != outfit.Bracer {
		t.Errorf("Bracer mismatch: got %s, want %s", unmarshaled.Bracer, outfit.Bracer)
	}
	if unmarshaled.Booster != outfit.Booster {
		t.Errorf("Booster mismatch: got %s, want %s", unmarshaled.Booster, outfit.Booster)
	}
	if unmarshaled.Decal != outfit.Decal {
		t.Errorf("Decal mismatch: got %s, want %s", unmarshaled.Decal, outfit.Decal)
	}
}

func TestSaveOutfitRequest_Validation(t *testing.T) {
	tests := []struct {
		name    string
		request SaveOutfitRequest
		valid   bool
	}{
		{
			name: "valid request with all fields",
			request: SaveOutfitRequest{
				Name:    "My Outfit",
				Chassis: "chassis-1",
				Bracer:  "bracer-1",
				Booster: "booster-1",
				Decal:   "decal-1",
			},
			valid: true,
		},
		{
			name: "valid request with minimal fields",
			request: SaveOutfitRequest{
				Name: "Simple Outfit",
			},
			valid: true,
		},
		{
			name: "invalid request with empty name",
			request: SaveOutfitRequest{
				Name:    "",
				Chassis: "chassis-1",
			},
			valid: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			data, err := json.Marshal(tt.request)
			if err != nil {
				t.Fatalf("Failed to marshal request: %v", err)
			}

			var unmarshaled SaveOutfitRequest
			if err := json.Unmarshal(data, &unmarshaled); err != nil {
				t.Fatalf("Failed to unmarshal request: %v", err)
			}

			// Verify name validation
			if tt.valid && unmarshaled.Name == "" {
				t.Error("Expected valid request to have non-empty name")
			}
			if !tt.valid && unmarshaled.Name != "" {
				t.Error("Expected invalid request to have empty name")
			}
		})
	}
}

func TestSaveOutfitResponse_JSONFormat(t *testing.T) {
	response := SaveOutfitResponse{
		Success: true,
		Outfit: Outfit{
			ID:      "test-id",
			Name:    "Test Outfit",
			Chassis: "chassis-1",
			Bracer:  "bracer-1",
			Booster: "booster-1",
			Decal:   "decal-1",
		},
		ID: "test-id",
	}

	data, err := json.Marshal(response)
	if err != nil {
		t.Fatalf("Failed to marshal response: %v", err)
	}

	jsonStr := string(data)
	expectedFields := []string{
		`"success":true`,
		`"id":"test-id"`,
		`"name":"Test Outfit"`,
		`"chassis":"chassis-1"`,
		`"bracer":"bracer-1"`,
		`"booster":"booster-1"`,
		`"decal":"decal-1"`,
	}

	for _, field := range expectedFields {
		if !strings.Contains(jsonStr, field) {
			t.Errorf("Response missing expected field: %s", field)
		}
	}
}

func TestListOutfitsResponse_EmptyList(t *testing.T) {
	response := ListOutfitsResponse{
		Outfits: []Outfit{},
	}

	data, err := json.Marshal(response)
	if err != nil {
		t.Fatalf("Failed to marshal response: %v", err)
	}

	var unmarshaled ListOutfitsResponse
	if err := json.Unmarshal(data, &unmarshaled); err != nil {
		t.Fatalf("Failed to unmarshal response: %v", err)
	}

	if unmarshaled.Outfits == nil {
		t.Error("Expected Outfits to be empty slice, not nil")
	}
	if len(unmarshaled.Outfits) != 0 {
		t.Errorf("Expected empty list, got %d outfits", len(unmarshaled.Outfits))
	}
}

func TestListOutfitsResponse_MultipleOutfits(t *testing.T) {
	response := ListOutfitsResponse{
		Outfits: []Outfit{
			{
				ID:      "outfit-1",
				Name:    "First Outfit",
				Chassis: "chassis-1",
			},
			{
				ID:      "outfit-2",
				Name:    "Second Outfit",
				Chassis: "chassis-2",
			},
		},
	}

	data, err := json.Marshal(response)
	if err != nil {
		t.Fatalf("Failed to marshal response: %v", err)
	}

	var unmarshaled ListOutfitsResponse
	if err := json.Unmarshal(data, &unmarshaled); err != nil {
		t.Fatalf("Failed to unmarshal response: %v", err)
	}

	if len(unmarshaled.Outfits) != 2 {
		t.Errorf("Expected 2 outfits, got %d", len(unmarshaled.Outfits))
	}

	if unmarshaled.Outfits[0].ID != "outfit-1" {
		t.Errorf("First outfit ID mismatch: got %s, want outfit-1", unmarshaled.Outfits[0].ID)
	}
	if unmarshaled.Outfits[1].ID != "outfit-2" {
		t.Errorf("Second outfit ID mismatch: got %s, want outfit-2", unmarshaled.Outfits[1].ID)
	}
}

func TestLoadOutfitRequest_Validation(t *testing.T) {
	tests := []struct {
		name      string
		request   LoadOutfitRequest
		expectErr bool
	}{
		{
			name: "valid request with outfit ID",
			request: LoadOutfitRequest{
				OutfitID: "test-outfit-id",
			},
			expectErr: false,
		},
		{
			name: "invalid request with empty outfit ID",
			request: LoadOutfitRequest{
				OutfitID: "",
			},
			expectErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.expectErr && tt.request.OutfitID != "" {
				t.Error("Expected empty outfit ID for error case")
			}
			if !tt.expectErr && tt.request.OutfitID == "" {
				t.Error("Expected non-empty outfit ID for valid case")
			}
		})
	}
}

func TestLoadOutfitResponse_JSONFormat(t *testing.T) {
	response := LoadOutfitResponse{
		Success: true,
		Outfit: Outfit{
			ID:      "test-id",
			Name:    "Loaded Outfit",
			Chassis: "chassis-1",
		},
	}

	data, err := json.Marshal(response)
	if err != nil {
		t.Fatalf("Failed to marshal response: %v", err)
	}

	jsonStr := string(data)
	expectedFields := []string{
		`"success":true`,
		`"id":"test-id"`,
		`"name":"Loaded Outfit"`,
	}

	for _, field := range expectedFields {
		if !strings.Contains(jsonStr, field) {
			t.Errorf("Response missing expected field: %s", field)
		}
	}
}

func TestDeleteOutfitRequest_Validation(t *testing.T) {
	request := DeleteOutfitRequest{
		OutfitID: "test-outfit-id",
	}

	if request.OutfitID != "test-outfit-id" {
		t.Errorf("Expected OutfitID to be 'test-outfit-id', got '%s'", request.OutfitID)
	}

	// Test empty ID
	emptyRequest := DeleteOutfitRequest{
		OutfitID: "",
	}

	if emptyRequest.OutfitID != "" {
		t.Error("Expected empty OutfitID")
	}
}

func TestDeleteOutfitResponse_JSONFormat(t *testing.T) {
	response := DeleteOutfitResponse{
		Success: true,
	}

	data, err := json.Marshal(response)
	if err != nil {
		t.Fatalf("Failed to marshal response: %v", err)
	}

	jsonStr := string(data)
	if !strings.Contains(jsonStr, `"success":true`) {
		t.Error("Response missing success field")
	}
}

func TestOutfitConstants(t *testing.T) {
	if OutfitCollection != "player_outfits" {
		t.Errorf("OutfitCollection mismatch: got %s, want player_outfits", OutfitCollection)
	}

	if MaxOutfitsPerUser != 10 {
		t.Errorf("MaxOutfitsPerUser mismatch: got %d, want 10", MaxOutfitsPerUser)
	}
}
