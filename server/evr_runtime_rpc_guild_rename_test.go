package server

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestGuildPlayerRenameRequest_JSON(t *testing.T) {
	tests := []struct {
		name     string
		jsonStr  string
		expected GuildPlayerRenameRequest
		wantErr  bool
	}{
		{
			name:    "valid request with all fields",
			jsonStr: `{"target_user_id":"00000000-0000-0000-0000-000000000001","group_id":"00000000-0000-0000-0000-000000000002","new_display_name":"TestName","is_locked":true}`,
			expected: GuildPlayerRenameRequest{
				TargetUserID:   "00000000-0000-0000-0000-000000000001",
				GroupID:        "00000000-0000-0000-0000-000000000002",
				NewDisplayName: "TestName",
				IsLocked:       true,
			},
			wantErr: false,
		},
		{
			name:    "valid request without lock",
			jsonStr: `{"target_user_id":"00000000-0000-0000-0000-000000000001","group_id":"00000000-0000-0000-0000-000000000002","new_display_name":"TestName"}`,
			expected: GuildPlayerRenameRequest{
				TargetUserID:   "00000000-0000-0000-0000-000000000001",
				GroupID:        "00000000-0000-0000-0000-000000000002",
				NewDisplayName: "TestName",
				IsLocked:       false,
			},
			wantErr: false,
		},
		{
			name:     "invalid JSON",
			jsonStr:  `{invalid json}`,
			expected: GuildPlayerRenameRequest{},
			wantErr:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var request GuildPlayerRenameRequest
			err := json.Unmarshal([]byte(tt.jsonStr), &request)

			if (err != nil) != tt.wantErr {
				t.Errorf("Unmarshal error = %v, wantErr %v", err, tt.wantErr)
				return
			}

			if !tt.wantErr {
				if request.TargetUserID != tt.expected.TargetUserID {
					t.Errorf("TargetUserID = %v, want %v", request.TargetUserID, tt.expected.TargetUserID)
				}
				if request.GroupID != tt.expected.GroupID {
					t.Errorf("GroupID = %v, want %v", request.GroupID, tt.expected.GroupID)
				}
				if request.NewDisplayName != tt.expected.NewDisplayName {
					t.Errorf("NewDisplayName = %v, want %v", request.NewDisplayName, tt.expected.NewDisplayName)
				}
				if request.IsLocked != tt.expected.IsLocked {
					t.Errorf("IsLocked = %v, want %v", request.IsLocked, tt.expected.IsLocked)
				}
			}
		})
	}
}

func TestGuildPlayerRenameResponse_JSON(t *testing.T) {
	response := GuildPlayerRenameResponse{
		Success:  true,
		OldName:  "OldName",
		NewName:  "NewName",
		IsLocked: true,
	}

	jsonBytes, err := json.Marshal(response)
	if err != nil {
		t.Fatalf("Failed to marshal response: %v", err)
	}

	jsonStr := string(jsonBytes)
	expectedFields := []string{
		`"success":true`,
		`"old_name":"OldName"`,
		`"new_name":"NewName"`,
		`"is_locked":true`,
	}

	for _, field := range expectedFields {
		if !strings.Contains(jsonStr, field) {
			t.Errorf("JSON missing expected field: %s\nGot: %s", field, jsonStr)
		}
	}

	var decoded GuildPlayerRenameResponse
	if err := json.Unmarshal(jsonBytes, &decoded); err != nil {
		t.Fatalf("Failed to unmarshal response: %v", err)
	}
	if decoded.Success != response.Success {
		t.Errorf("Success = %v, want %v", decoded.Success, response.Success)
	}
	if decoded.IsLocked != response.IsLocked {
		t.Errorf("IsLocked = %v, want %v", decoded.IsLocked, response.IsLocked)
	}
}

func TestGuildPlayerIGNResponse_JSON(t *testing.T) {
	response := GuildPlayerIGNResponse{
		GroupIGNs: map[string]GroupInGameName{
			"group-1": {
				GroupID:     "group-1",
				DisplayName: "PlayerOne",
				IsOverride:  true,
				IsLocked:    false,
			},
			"group-2": {
				GroupID:     "group-2",
				DisplayName: "PlayerTwo",
				IsOverride:  false,
				IsLocked:    false,
			},
		},
	}

	jsonBytes, err := json.Marshal(response)
	if err != nil {
		t.Fatalf("Failed to marshal response: %v", err)
	}

	var decoded GuildPlayerIGNResponse
	if err := json.Unmarshal(jsonBytes, &decoded); err != nil {
		t.Fatalf("Failed to unmarshal response: %v", err)
	}

	if len(decoded.GroupIGNs) != 2 {
		t.Fatalf("Expected 2 group IGNs, got %d", len(decoded.GroupIGNs))
	}

	g1, ok := decoded.GroupIGNs["group-1"]
	if !ok {
		t.Fatal("Missing group-1 in decoded response")
	}
	if g1.DisplayName != "PlayerOne" {
		t.Errorf("group-1 DisplayName = %q, want %q", g1.DisplayName, "PlayerOne")
	}
	if !g1.IsOverride {
		t.Error("group-1 IsOverride should be true")
	}
	if g1.IsLocked {
		t.Error("group-1 IsLocked should be false")
	}
}
