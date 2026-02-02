package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/gofrs/uuid/v5"
	"github.com/heroiclabs/nakama-common/runtime"
)

const (
	StorageCollectionPlayerReports = "PlayerReports"
	StorageKeyReportPrefix         = "report_"
	MaxReportsPerHour              = 5
)

// PlayerReportRequest represents the request payload for the player/report RPC
type PlayerReportRequest struct {
	ReportedUserID string `json:"reported_user_id"` // UUID of the reported user (required)
	GroupID        string `json:"group_id"`         // UUID of the guild group context (required)
	Reason         string `json:"reason"`           // Reason for the report (required)
	Description    string `json:"description"`      // Description of the incident (required)
	Evidence       string `json:"evidence"`         // Optional URL or text evidence
}

// PlayerReportResponse represents the response from the player/report RPC
type PlayerReportResponse struct {
	Success  bool   `json:"success"`
	ReportID string `json:"report_id"`
	Message  string `json:"message"`
}

// PlayerReport represents a stored player report
type PlayerReport struct {
	ID             string    `json:"id"`
	ReporterUserID string    `json:"reporter_user_id"`
	ReportedUserID string    `json:"reported_user_id"`
	GroupID        string    `json:"group_id"`
	Reason         string    `json:"reason"`
	Description    string    `json:"description"`
	Evidence       string    `json:"evidence"`
	CreatedAt      time.Time `json:"created_at"`
	Status         string    `json:"status"` // pending, reviewed, resolved
}

// PlayerReportRPC handles player report submissions
func PlayerReportRPC(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, payload string) (string, error) {
	var request PlayerReportRequest
	if err := json.Unmarshal([]byte(payload), &request); err != nil {
		return "", runtime.NewError("Invalid request payload", StatusInvalidArgument)
	}

	// Get the caller's user ID from context
	userID, ok := ctx.Value(runtime.RUNTIME_CTX_USER_ID).(string)
	if !ok || userID == "" {
		return "", runtime.NewError("User ID not found in context", StatusUnauthenticated)
	}

	// Validate required fields
	if request.ReportedUserID == "" {
		return "", runtime.NewError("reported_user_id is required", StatusInvalidArgument)
	}

	if request.GroupID == "" {
		return "", runtime.NewError("group_id is required", StatusInvalidArgument)
	}

	if request.Reason == "" {
		return "", runtime.NewError("reason is required", StatusInvalidArgument)
	}

	if request.Description == "" {
		return "", runtime.NewError("description is required", StatusInvalidArgument)
	}

	// Validate UUIDs
	if _, err := uuid.FromString(request.ReportedUserID); err != nil {
		return "", runtime.NewError("reported_user_id must be a valid UUID", StatusInvalidArgument)
	}

	if _, err := uuid.FromString(request.GroupID); err != nil {
		return "", runtime.NewError("group_id must be a valid UUID", StatusInvalidArgument)
	}

	// Disallow self-reporting
	if request.ReportedUserID == userID {
		return "", runtime.NewError("You cannot report yourself", StatusInvalidArgument)
	}

	// Check rate limit - max 5 reports per hour per user
	if err := checkReportRateLimit(ctx, nk, userID); err != nil {
		return "", err
	}

	// Verify the reported user exists
	accounts, err := nk.UsersGetId(ctx, []string{request.ReportedUserID}, nil)
	if err != nil {
		return "", runtime.NewError("Failed to verify reported user", StatusInternalError)
	}
	if len(accounts) == 0 {
		return "", runtime.NewError("Reported user not found", StatusNotFound)
	}

	// Verify the group exists
	groups, err := nk.GroupsGetId(ctx, []string{request.GroupID})
	if err != nil {
		return "", runtime.NewError("Failed to verify group", StatusInternalError)
	}
	if len(groups) == 0 {
		return "", runtime.NewError("Group not found", StatusNotFound)
	}

	// Create the report
	reportID := uuid.Must(uuid.NewV4()).String()
	report := PlayerReport{
		ID:             reportID,
		ReporterUserID: userID,
		ReportedUserID: request.ReportedUserID,
		GroupID:        request.GroupID,
		Reason:         request.Reason,
		Description:    request.Description,
		Evidence:       request.Evidence,
		CreatedAt:      time.Now().UTC(),
		Status:         "pending",
	}

	// Marshal the report to JSON
	reportJSON, err := json.Marshal(report)
	if err != nil {
		return "", runtime.NewError("Failed to marshal report", StatusInternalError)
	}

	// Store the report in the reporter's storage
	writes := []*runtime.StorageWrite{
		{
			Collection:      StorageCollectionPlayerReports,
			Key:             StorageKeyReportPrefix + reportID,
			UserID:          userID,
			Value:           string(reportJSON),
			PermissionRead:  runtime.STORAGE_PERMISSION_NO_READ,  // Only server can read
			PermissionWrite: runtime.STORAGE_PERMISSION_NO_WRITE, // Only server can write
		},
	}

	if _, err := nk.StorageWrite(ctx, writes); err != nil {
		return "", runtime.NewError("Failed to store report", StatusInternalError)
	}

	// Create the response
	response := PlayerReportResponse{
		Success:  true,
		ReportID: reportID,
		Message:  "Report submitted successfully",
	}

	responseJSON, err := json.Marshal(response)
	if err != nil {
		return "", runtime.NewError("Failed to marshal response", StatusInternalError)
	}

	return string(responseJSON), nil
}

// checkReportRateLimit checks if the user has exceeded the rate limit of 5 reports per hour
func checkReportRateLimit(ctx context.Context, nk runtime.NakamaModule, userID string) error {
	// List reports by this user (limited to 100 most recent)
	// Note: This assumes users don't have more than 100 reports in their storage.
	// In production, consider using time-based key patterns or multiple queries with cursors
	// to handle users with very large numbers of reports.
	objects, _, err := nk.StorageList(ctx, SystemUserID, userID, StorageCollectionPlayerReports, 100, "")
	if err != nil {
		// If there's an error reading storage, allow the report (fail open for rate limiting)
		return nil
	}

	// Count reports in the last hour
	oneHourAgo := time.Now().UTC().Add(-1 * time.Hour)
	recentReportCount := 0

	for _, obj := range objects {
		var report PlayerReport
		if err := json.Unmarshal([]byte(obj.Value), &report); err != nil {
			// Skip invalid reports
			continue
		}

		if report.CreatedAt.After(oneHourAgo) {
			recentReportCount++
		}
	}

	if recentReportCount >= MaxReportsPerHour {
		return runtime.NewError(
			fmt.Sprintf("Rate limit exceeded. You can only submit %d reports per hour. Please try again later.", MaxReportsPerHour),
			StatusResourceExhausted,
		)
	}

	return nil
}
