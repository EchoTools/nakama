package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"

	"github.com/gofrs/uuid/v5"
	"github.com/heroiclabs/nakama-common/runtime"
)

// BreakAlternatesRPCRequest represents the request payload for breaking alternate account associations.
type BreakAlternatesRPCRequest struct {
	UserID1 string `json:"user_id_1"` // First user ID (UUID)
	UserID2 string `json:"user_id_2"` // Second user ID (UUID)
}

// BreakAlternatesRPCResponse represents the response payload for breaking alternate account associations.
type BreakAlternatesRPCResponse struct {
	Success        bool   `json:"success"`
	PrimaryUserID  string `json:"primary_user_id"`
	OtherUserID    string `json:"other_user_id"`
	Message        string `json:"message"`
	OperatorUserID string `json:"operator_user_id"`
}

func (r BreakAlternatesRPCResponse) String() string {
	data, _ := json.Marshal(r)
	return string(data)
}

// BreakAlternatesRPC handles breaking alternate account associations between two users.
// This RPC is restricted to Global Operators only (enforced by middleware).
func BreakAlternatesRPC(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, payload string) (string, error) {
	// Parse the request
	request := BreakAlternatesRPCRequest{}
	if err := json.Unmarshal([]byte(payload), &request); err != nil {
		return "", runtime.NewError(fmt.Sprintf("Error unmarshalling request: %s", err.Error()), StatusInvalidArgument)
	}

	// Get the caller's user ID from context (guaranteed to exist by middleware)
	callerUserID := ctx.Value(runtime.RUNTIME_CTX_USER_ID).(string)

	// Validate user IDs
	if request.UserID1 == "" || request.UserID2 == "" {
		return "", runtime.NewError("Both user_id_1 and user_id_2 are required", StatusInvalidArgument)
	}

	// Ensure UUIDs are valid
	if _, err := uuid.FromString(request.UserID1); err != nil {
		return "", runtime.NewError(fmt.Sprintf("Invalid user_id_1: %s", err.Error()), StatusInvalidArgument)
	}
	if _, err := uuid.FromString(request.UserID2); err != nil {
		return "", runtime.NewError(fmt.Sprintf("Invalid user_id_2: %s", err.Error()), StatusInvalidArgument)
	}

	// Ensure the two user IDs are different
	if request.UserID1 == request.UserID2 {
		return "", runtime.NewError("user_id_1 and user_id_2 must be different", StatusInvalidArgument)
	}

	// Load login histories for both users
	history1 := NewLoginHistory(request.UserID1)
	if err := StorableRead(ctx, nk, request.UserID1, history1, false); err != nil {
		return "", runtime.NewError(fmt.Sprintf("Failed to load login history for user_id_1: %s", err.Error()), StatusNotFound)
	}

	history2 := NewLoginHistory(request.UserID2)
	if err := StorableRead(ctx, nk, request.UserID2, history2, false); err != nil {
		return "", runtime.NewError(fmt.Sprintf("Failed to load login history for user_id_2: %s", err.Error()), StatusNotFound)
	}

	// Determine which account is the primary (most recent login)
	lastSeen1 := history1.LastSeen()
	lastSeen2 := history2.LastSeen()

	var primaryUserID, otherUserID string
	var primaryHistory, otherHistory *LoginHistory

	if lastSeen1.After(lastSeen2) {
		primaryUserID = request.UserID1
		otherUserID = request.UserID2
		primaryHistory = history1
		otherHistory = history2
	} else {
		primaryUserID = request.UserID2
		otherUserID = request.UserID1
		primaryHistory = history2
		otherHistory = history1
	}

	// Check if they are actually alternates
	_, found1 := primaryHistory.AlternateMatches[otherUserID]
	_, found2 := otherHistory.AlternateMatches[primaryUserID]

	if !found1 && !found2 {
		logger.WithFields(map[string]interface{}{
			"operator_user_id": callerUserID,
			"user_id_1":        request.UserID1,
			"user_id_2":        request.UserID2,
		}).Warn("No alternate association found between users")

		return BreakAlternatesRPCResponse{
			Success:        true,
			PrimaryUserID:  primaryUserID,
			OtherUserID:    otherUserID,
			Message:        "No alternate association found between the two accounts",
			OperatorUserID: callerUserID,
		}.String(), nil
	}

	// Remove each user from the other's AlternateMatches
	delete(primaryHistory.AlternateMatches, otherUserID)
	delete(otherHistory.AlternateMatches, primaryUserID)

	// Remove from second degree alternates if present
	for i := 0; i < len(primaryHistory.SecondDegreeAlternates); i++ {
		if primaryHistory.SecondDegreeAlternates[i] == otherUserID {
			primaryHistory.SecondDegreeAlternates = append(
				primaryHistory.SecondDegreeAlternates[:i],
				primaryHistory.SecondDegreeAlternates[i+1:]...,
			)
			i--
		}
	}
	for i := 0; i < len(otherHistory.SecondDegreeAlternates); i++ {
		if otherHistory.SecondDegreeAlternates[i] == primaryUserID {
			otherHistory.SecondDegreeAlternates = append(
				otherHistory.SecondDegreeAlternates[:i],
				otherHistory.SecondDegreeAlternates[i+1:]...,
			)
			i--
		}
	}

	// Save both login histories
	if err := StorableWrite(ctx, nk, primaryUserID, primaryHistory); err != nil {
		return "", runtime.NewError(fmt.Sprintf("Failed to save login history for primary user: %s", err.Error()), StatusInternalError)
	}

	if err := StorableWrite(ctx, nk, otherUserID, otherHistory); err != nil {
		return "", runtime.NewError(fmt.Sprintf("Failed to save login history for other user: %s", err.Error()), StatusInternalError)
	}

	logger.WithFields(map[string]interface{}{
		"operator_user_id": callerUserID,
		"primary_user_id":  primaryUserID,
		"other_user_id":    otherUserID,
	}).Info("Alternate account association broken")

	return BreakAlternatesRPCResponse{
		Success:        true,
		PrimaryUserID:  primaryUserID,
		OtherUserID:    otherUserID,
		Message:        "Alternate account association successfully broken",
		OperatorUserID: callerUserID,
	}.String(), nil
}
