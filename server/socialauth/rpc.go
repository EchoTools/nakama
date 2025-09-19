package socialauth

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/heroiclabs/nakama-common/runtime"
)

// RpcCreateApplication creates a new application but requires GitHub account linking
func RpcCreateApplication(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, payload string) (string, error) {
	userID, ok := ctx.Value(runtime.RUNTIME_CTX_USER_ID).(string)
	if !ok {
		return "", runtime.NewError("No user ID found", 3) // UNAUTHENTICATED
	}

	// Check if user has GitHub account linked
	_, err := LoadGitHubProfile(ctx, nk, userID)
	if err != nil {
		return "", runtime.NewError("GitHub account linking required to create applications. Please link your GitHub account first.", 9) // FAILED_PRECONDITION
	}

	// Parse the payload to get application details
	var req struct {
		Name        string `json:"name"`
		Description string `json:"description"`
	}
	if err := json.Unmarshal([]byte(payload), &req); err != nil {
		return "", runtime.NewError("Invalid request payload", 3) // INVALID_ARGUMENT
	}

	// Validate required fields
	if req.Name == "" {
		return "", runtime.NewError("Application name is required", 3) // INVALID_ARGUMENT
	}

	// Create application record
	applicationID := generateApplicationID()
	app := map[string]interface{}{
		"id":          applicationID,
		"name":        req.Name,
		"description": req.Description,
		"owner_id":    userID,
		"created_at":  fmt.Sprintf("%d", time.Now().Unix()),
	}

	// Store application
	appJSON, _ := json.Marshal(app)
	_, err = nk.StorageWrite(ctx, []*runtime.StorageWrite{
		{
			Collection:      "applications",
			Key:             applicationID,
			UserID:          userID,
			Value:           string(appJSON),
			PermissionRead:  runtime.STORAGE_PERMISSION_OWNER_READ,
			PermissionWrite: runtime.STORAGE_PERMISSION_OWNER_WRITE,
		},
	})
	if err != nil {
		return "", runtime.NewError("Failed to create application", 13) // INTERNAL
	}

	return string(appJSON), nil
}

// RpcListApplications lists applications for the authenticated user
func RpcListApplications(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, payload string) (string, error) {
	userID, ok := ctx.Value(runtime.RUNTIME_CTX_USER_ID).(string)
	if !ok {
		return "", runtime.NewError("No user ID found", 3) // UNAUTHENTICATED
	}

	// List user's applications
	objects, _, err := nk.StorageList(ctx, userID, "applications", "", 100, "")
	if err != nil {
		return "", runtime.NewError("Failed to list applications", 13) // INTERNAL
	}

	applications := make([]map[string]interface{}, 0, len(objects))
	for _, obj := range objects {
		var app map[string]interface{}
		if err := json.Unmarshal([]byte(obj.Value), &app); err == nil {
			applications = append(applications, app)
		}
	}

	result := map[string]interface{}{
		"applications": applications,
	}

	resultJSON, _ := json.Marshal(result)
	return string(resultJSON), nil
}

// RpcGetGitHubStatus returns GitHub account linking status
func RpcGetGitHubStatus(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, payload string) (string, error) {
	userID, ok := ctx.Value(runtime.RUNTIME_CTX_USER_ID).(string)
	if !ok {
		return "", runtime.NewError("No user ID found", 3) // UNAUTHENTICATED
	}

	// Check if user has GitHub account linked
	profile, err := LoadGitHubProfile(ctx, nk, userID)

	result := map[string]any{
		"linked": err == nil,
	}

	if err == nil {
		result["github_username"] = profile.Login
		result["github_id"] = profile.ID
		result["avatar_url"] = profile.AvatarURL
		result["name"] = profile.Name
	}

	resultJSON, _ := json.Marshal(result)
	return string(resultJSON), nil
}

// generateApplicationID generates a unique application ID
func generateApplicationID() string {
	// Simple implementation - in production you might want something more sophisticated
	uuid := make([]byte, 16)
	for i := range uuid {
		uuid[i] = byte(i % 256)
	}
	return fmt.Sprintf("app_%x", uuid)
}
