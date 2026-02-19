package server

import (
	"encoding/json"
	"testing"

	"github.com/gofrs/uuid/v5"
)

func TestBreakAlternatesRPCRequest_JSONMarshaling(t *testing.T) {
	request := BreakAlternatesRPCRequest{
		UserID1: "550e8400-e29b-41d4-a716-446655440000",
		UserID2: "550e8400-e29b-41d4-a716-446655440001",
	}

	data, err := json.Marshal(request)
	if err != nil {
		t.Fatalf("Failed to marshal request: %v", err)
	}

	var unmarshaled BreakAlternatesRPCRequest
	if err := json.Unmarshal(data, &unmarshaled); err != nil {
		t.Fatalf("Failed to unmarshal request: %v", err)
	}

	if unmarshaled.UserID1 != request.UserID1 {
		t.Errorf("UserID1 mismatch: got %s, want %s", unmarshaled.UserID1, request.UserID1)
	}
	if unmarshaled.UserID2 != request.UserID2 {
		t.Errorf("UserID2 mismatch: got %s, want %s", unmarshaled.UserID2, request.UserID2)
	}
}

func TestBreakAlternatesRPCResponse_JSONMarshaling(t *testing.T) {
	response := BreakAlternatesRPCResponse{
		Success:        true,
		PrimaryUserID:  "550e8400-e29b-41d4-a716-446655440000",
		OtherUserID:    "550e8400-e29b-41d4-a716-446655440001",
		Message:        "Alternate account association successfully broken",
		OperatorUserID: "550e8400-e29b-41d4-a716-446655440002",
	}

	jsonStr := response.String()
	if jsonStr == "" {
		t.Fatal("Response.String() returned empty string")
	}

	var unmarshaled BreakAlternatesRPCResponse
	if err := json.Unmarshal([]byte(jsonStr), &unmarshaled); err != nil {
		t.Fatalf("Failed to unmarshal response: %v", err)
	}

	if unmarshaled.Success != response.Success {
		t.Errorf("Success mismatch: got %v, want %v", unmarshaled.Success, response.Success)
	}
	if unmarshaled.PrimaryUserID != response.PrimaryUserID {
		t.Errorf("PrimaryUserID mismatch: got %s, want %s", unmarshaled.PrimaryUserID, response.PrimaryUserID)
	}
	if unmarshaled.OtherUserID != response.OtherUserID {
		t.Errorf("OtherUserID mismatch: got %s, want %s", unmarshaled.OtherUserID, response.OtherUserID)
	}
	if unmarshaled.Message != response.Message {
		t.Errorf("Message mismatch: got %s, want %s", unmarshaled.Message, response.Message)
	}
	if unmarshaled.OperatorUserID != response.OperatorUserID {
		t.Errorf("OperatorUserID mismatch: got %s, want %s", unmarshaled.OperatorUserID, response.OperatorUserID)
	}
}

func TestBreakAlternatesRPCRequest_Validation(t *testing.T) {
	tests := []struct {
		name    string
		request BreakAlternatesRPCRequest
		valid   bool
	}{
		{
			name: "valid request",
			request: BreakAlternatesRPCRequest{
				UserID1: "550e8400-e29b-41d4-a716-446655440000",
				UserID2: "550e8400-e29b-41d4-a716-446655440001",
			},
			valid: true,
		},
		{
			name: "missing user_id_1",
			request: BreakAlternatesRPCRequest{
				UserID1: "",
				UserID2: "550e8400-e29b-41d4-a716-446655440001",
			},
			valid: false,
		},
		{
			name: "missing user_id_2",
			request: BreakAlternatesRPCRequest{
				UserID1: "550e8400-e29b-41d4-a716-446655440000",
				UserID2: "",
			},
			valid: false,
		},
		{
			name: "both missing",
			request: BreakAlternatesRPCRequest{
				UserID1: "",
				UserID2: "",
			},
			valid: false,
		},
		{
			name: "same user IDs",
			request: BreakAlternatesRPCRequest{
				UserID1: "550e8400-e29b-41d4-a716-446655440000",
				UserID2: "550e8400-e29b-41d4-a716-446655440000",
			},
			valid: false,
		},
		{
			name: "invalid UUID format in user_id_1",
			request: BreakAlternatesRPCRequest{
				UserID1: "not-a-valid-uuid",
				UserID2: "550e8400-e29b-41d4-a716-446655440001",
			},
			valid: false,
		},
		{
			name: "invalid UUID format in user_id_2",
			request: BreakAlternatesRPCRequest{
				UserID1: "550e8400-e29b-41d4-a716-446655440000",
				UserID2: "invalid-uuid-format",
			},
			valid: false,
		},
		{
			name: "both UUIDs invalid",
			request: BreakAlternatesRPCRequest{
				UserID1: "bad-uuid-1",
				UserID2: "bad-uuid-2",
			},
			valid: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Verify basic field presence
			isEmpty1 := tt.request.UserID1 == ""
			isEmpty2 := tt.request.UserID2 == ""
			areSame := tt.request.UserID1 == tt.request.UserID2

			// Validate UUID format
			_, err1 := uuid.FromString(tt.request.UserID1)
			_, err2 := uuid.FromString(tt.request.UserID2)
			validUUID1 := err1 == nil
			validUUID2 := err2 == nil

			if tt.valid {
				if isEmpty1 || isEmpty2 || areSame || !validUUID1 || !validUUID2 {
					t.Error("Expected valid request to have different non-empty valid UUIDs")
				}
			} else {
				if !isEmpty1 && !isEmpty2 && !areSame && validUUID1 && validUUID2 {
					t.Error("Expected invalid request to have missing, same, or invalid UUIDs")
				}
			}
		})
	}
}
