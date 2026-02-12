package server

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestAdminPlayerRenameRequest_Validation(t *testing.T) {
	// Test that the request structure is correctly defined
	request := AdminPlayerRenameRequest{
		TargetUserID:   "00000000-0000-0000-0000-000000000001",
		NewDisplayName: "NewPlayerName",
		ModeratorNotes: "Updated display name per user request",
	}

	// Verify fields are accessible
	if request.TargetUserID != "00000000-0000-0000-0000-000000000001" {
		t.Errorf("Expected TargetUserID to be '00000000-0000-0000-0000-000000000001', got '%s'", request.TargetUserID)
	}
	if request.NewDisplayName != "NewPlayerName" {
		t.Errorf("Expected NewDisplayName to be 'NewPlayerName', got '%s'", request.NewDisplayName)
	}
	if request.ModeratorNotes != "Updated display name per user request" {
		t.Errorf("Expected ModeratorNotes to be 'Updated display name per user request', got '%s'", request.ModeratorNotes)
	}
}

func TestAdminPlayerRenameResponse_JSON(t *testing.T) {
	response := AdminPlayerRenameResponse{
		Success: true,
		OldName: "OldPlayerName",
		NewName: "NewPlayerName",
	}

	// Marshal to JSON
	jsonBytes, err := json.Marshal(response)
	if err != nil {
		t.Fatalf("Failed to marshal response: %v", err)
	}

	jsonStr := string(jsonBytes)
	if jsonStr == "" {
		t.Error("AdminPlayerRenameResponse JSON is empty")
	}

	// Verify it contains expected fields
	expectedFields := []string{
		`"success":true`,
		`"old_name":"OldPlayerName"`,
		`"new_name":"NewPlayerName"`,
	}

	for _, field := range expectedFields {
		if !strings.Contains(jsonStr, field) {
			t.Errorf("AdminPlayerRenameResponse JSON missing expected field: %s\nGot: %s", field, jsonStr)
		}
	}

	// Unmarshal and verify
	var decoded AdminPlayerRenameResponse
	if err := json.Unmarshal(jsonBytes, &decoded); err != nil {
		t.Fatalf("Failed to unmarshal response: %v", err)
	}

	if decoded.Success != response.Success {
		t.Errorf("Expected Success=%v, got %v", response.Success, decoded.Success)
	}
	if decoded.OldName != response.OldName {
		t.Errorf("Expected OldName=%s, got %s", response.OldName, decoded.OldName)
	}
	if decoded.NewName != response.NewName {
		t.Errorf("Expected NewName=%s, got %s", response.NewName, decoded.NewName)
	}
}

func TestAdminPlayerRenameRequest_JSON(t *testing.T) {
	tests := []struct {
		name     string
		jsonStr  string
		expected AdminPlayerRenameRequest
		wantErr  bool
	}{
		{
			name:    "valid request with all fields",
			jsonStr: `{"target_user_id":"00000000-0000-0000-0000-000000000001","new_display_name":"TestName","moderator_notes":"Test notes"}`,
			expected: AdminPlayerRenameRequest{
				TargetUserID:   "00000000-0000-0000-0000-000000000001",
				NewDisplayName: "TestName",
				ModeratorNotes: "Test notes",
			},
			wantErr: false,
		},
		{
			name:    "valid request without moderator notes",
			jsonStr: `{"target_user_id":"00000000-0000-0000-0000-000000000002","new_display_name":"AnotherName"}`,
			expected: AdminPlayerRenameRequest{
				TargetUserID:   "00000000-0000-0000-0000-000000000002",
				NewDisplayName: "AnotherName",
				ModeratorNotes: "",
			},
			wantErr: false,
		},
		{
			name:     "invalid JSON",
			jsonStr:  `{invalid json}`,
			expected: AdminPlayerRenameRequest{},
			wantErr:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var request AdminPlayerRenameRequest
			err := json.Unmarshal([]byte(tt.jsonStr), &request)

			if (err != nil) != tt.wantErr {
				t.Errorf("Unmarshal error = %v, wantErr %v", err, tt.wantErr)
				return
			}

			if !tt.wantErr {
				if request.TargetUserID != tt.expected.TargetUserID {
					t.Errorf("TargetUserID = %v, want %v", request.TargetUserID, tt.expected.TargetUserID)
				}
				if request.NewDisplayName != tt.expected.NewDisplayName {
					t.Errorf("NewDisplayName = %v, want %v", request.NewDisplayName, tt.expected.NewDisplayName)
				}
				if request.ModeratorNotes != tt.expected.ModeratorNotes {
					t.Errorf("ModeratorNotes = %v, want %v", request.ModeratorNotes, tt.expected.ModeratorNotes)
				}
			}
		})
	}
}

func TestSanitizeDisplayName(t *testing.T) {
	// Test display name sanitization patterns used by the admin rename RPC
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "valid simple name",
			input:    "PlayerOne",
			expected: "PlayerOne",
		},
		{
			name:     "valid name with numbers",
			input:    "Player123",
			expected: "Player123",
		},
		{
			name:     "valid name with special chars",
			input:    "Player[VIP]",
			expected: "Player[VIP]",
		},
		{
			name:     "name with spaces",
			input:    "Player Name",
			expected: "Player Name",
		},
		{
			name:     "name with trailing spaces",
			input:    "  Player  ",
			expected: "Player",
		},
		{
			name:     "name at max length",
			input:    "12345678901234567890ABCD",
			expected: "12345678901234567890ABCD",
		},
		{
			name:     "name exceeding max length",
			input:    "12345678901234567890ABCDE",
			expected: "12345678901234567890ABCD",
		},
		{
			name:     "empty name",
			input:    "",
			expected: "",
		},
		{
			name:     "name with only numbers",
			input:    "123456",
			expected: "",
		},
		{
			name:     "name with emoji shortcode",
			input:    "Player:smile:",
			expected: "Player",
		},
		{
			name:     "name with unicode converted to ASCII",
			input:    "Plàyér",
			expected: "Player",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := sanitizeDisplayName(tt.input)
			if result != tt.expected {
				t.Errorf("sanitizeDisplayName(%q) = %q, want %q", tt.input, result, tt.expected)
			}
		})
	}
}
