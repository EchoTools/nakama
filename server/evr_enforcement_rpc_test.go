package server

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/gofrs/uuid/v5"
	"github.com/heroiclabs/nakama-common/api"
	"github.com/heroiclabs/nakama-common/runtime"
)

// mockEnforcementNakamaModule is a minimal mock for testing the PlayerReportRPC
type mockEnforcementNakamaModule struct {
	runtime.NakamaModule
	storageObjects map[string][]*api.StorageObject
}

func newMockEnforcementNakamaModule() *mockEnforcementNakamaModule {
	return &mockEnforcementNakamaModule{
		storageObjects: make(map[string][]*api.StorageObject),
	}
}

func (m *mockEnforcementNakamaModule) UsersGetId(ctx context.Context, userIDs []string, facebookIDs []string) ([]*api.User, error) {
	// Return mock users for any valid UUID
	users := make([]*api.User, 0)
	for _, id := range userIDs {
		if _, err := uuid.FromString(id); err == nil {
			users = append(users, &api.User{
				Id: id,
			})
		}
	}
	return users, nil
}

func (m *mockEnforcementNakamaModule) GroupsGetId(ctx context.Context, groupIDs []string) ([]*api.Group, error) {
	// Return mock groups for any valid UUID
	groups := make([]*api.Group, 0)
	for _, id := range groupIDs {
		if _, err := uuid.FromString(id); err == nil {
			groups = append(groups, &api.Group{
				Id:       id,
				LangTag:  "guild",
				Metadata: "{}",
			})
		}
	}
	return groups, nil
}

func (m *mockEnforcementNakamaModule) StorageWrite(ctx context.Context, writes []*runtime.StorageWrite) ([]*api.StorageObjectAck, error) {
	acks := make([]*api.StorageObjectAck, len(writes))
	for i, write := range writes {
		key := write.UserID + ":" + write.Collection + ":" + write.Key
		obj := &api.StorageObject{
			Collection: write.Collection,
			Key:        write.Key,
			UserId:     write.UserID,
			Value:      write.Value,
		}
		m.storageObjects[key] = append(m.storageObjects[key], obj)
		acks[i] = &api.StorageObjectAck{
			Collection: write.Collection,
			Key:        write.Key,
			UserId:     write.UserID,
			Version:    "1",
		}
	}
	return acks, nil
}

func (m *mockEnforcementNakamaModule) StorageList(ctx context.Context, callerID, userID, collection string, limit int, cursor string) ([]*api.StorageObject, string, error) {
	objects := make([]*api.StorageObject, 0)
	for _, objs := range m.storageObjects {
		for i := range objs {
			if objs[i].Collection == collection && objs[i].UserId == userID {
				objects = append(objects, objs[i])
			}
		}
	}
	// Basic mock implementation of limit/cursor
	// This is not efficient or perfectly accurate but sufficient for unit tests

	if cursor != "" {
		// simplistic cursor implementation: skip until we find one we haven't seen?
		// or just ignore cursor for now since our tests don't strictly rely on it yet
		// except for the new loop logic we'll add
	}

	if limit > 0 && len(objects) > limit {
		// simplistic pagination
		return objects[:limit], "next_cursor", nil
	}

	return objects, "", nil
}

func (m *mockEnforcementNakamaModule) StorageRead(ctx context.Context, reads []*runtime.StorageRead) ([]*api.StorageObject, error) {
	objects := make([]*api.StorageObject, 0)
	for _, read := range reads {
		key := read.UserID + ":" + read.Collection + ":" + read.Key
		if objs, ok := m.storageObjects[key]; ok {
			objects = append(objects, objs...)
		}
	}
	return objects, nil
}

func TestPlayerReportRPC_Success(t *testing.T) {
	// Initialize ServiceSettings to avoid panic
	ServiceSettingsUpdate(&ServiceSettingsData{
		DiscordBotUserID: "discord-bot-user-id",
	})

	ctx := context.WithValue(context.Background(), runtime.RUNTIME_CTX_USER_ID, "reporter-user-id")
	nk := newMockEnforcementNakamaModule()

	reportedUserID := uuid.Must(uuid.NewV4()).String()
	groupID := uuid.Must(uuid.NewV4()).String()

	// Setup GuildGroupState in storage
	state := GuildGroupState{
		GroupID: groupID,
	}
	stateJSON, _ := json.Marshal(state)
	nk.storageObjects["discord-bot-user-id:"+StorageCollectionState+":"+groupID] = []*api.StorageObject{{
		Collection: StorageCollectionState,
		Key:        groupID,
		UserId:     "discord-bot-user-id",
		Value:      string(stateJSON),
	}}

	request := PlayerReportRequest{
		ReportedUserID: reportedUserID,
		GroupID:        groupID,
		Reason:         "cheating",
		Description:    "Player was using an aimbot",
		Evidence:       "https://example.com/video.mp4",
	}

	payload, err := json.Marshal(request)
	if err != nil {
		t.Fatalf("Failed to marshal request: %v", err)
	}

	response, err := PlayerReportRPC(ctx, nil, nil, nk, string(payload))
	if err != nil {
		t.Fatalf("Expected no error, got: %v", err)
	}

	var resp PlayerReportResponse
	if err := json.Unmarshal([]byte(response), &resp); err != nil {
		t.Fatalf("Failed to unmarshal response: %v", err)
	}

	if !resp.Success {
		t.Errorf("Expected success=true, got: %v", resp.Success)
	}

	if resp.ReportID == "" {
		t.Error("Expected non-empty report_id")
	}

	if resp.Message == "" {
		t.Error("Expected non-empty message")
	}
}

func TestPlayerReportRPC_MissingFields(t *testing.T) {
	ctx := context.WithValue(context.Background(), runtime.RUNTIME_CTX_USER_ID, "reporter-user-id")
	nk := newMockEnforcementNakamaModule()

	tests := []struct {
		name    string
		request PlayerReportRequest
		errMsg  string
	}{
		{
			name: "missing reported_user_id",
			request: PlayerReportRequest{
				GroupID:     uuid.Must(uuid.NewV4()).String(),
				Reason:      "cheating",
				Description: "test",
			},
			errMsg: "reported_user_id is required",
		},
		{
			name: "missing group_id",
			request: PlayerReportRequest{
				ReportedUserID: uuid.Must(uuid.NewV4()).String(),
				Reason:         "cheating",
				Description:    "test",
			},
			errMsg: "group_id is required",
		},
		{
			name: "missing reason",
			request: PlayerReportRequest{
				ReportedUserID: uuid.Must(uuid.NewV4()).String(),
				GroupID:        uuid.Must(uuid.NewV4()).String(),
				Description:    "test",
			},
			errMsg: "reason is required",
		},
		{
			name: "missing description",
			request: PlayerReportRequest{
				ReportedUserID: uuid.Must(uuid.NewV4()).String(),
				GroupID:        uuid.Must(uuid.NewV4()).String(),
				Reason:         "cheating",
			},
			errMsg: "description is required",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			payload, _ := json.Marshal(tt.request)
			_, err := PlayerReportRPC(ctx, nil, nil, nk, string(payload))
			if err == nil {
				t.Error("Expected an error, got nil")
			} else if tt.errMsg != "" && err.Error() != tt.errMsg {
				t.Errorf("Expected error message %q, got %q", tt.errMsg, err.Error())
			}
		})
	}
}

func TestPlayerReportRPC_SelfReport(t *testing.T) {
	userID := uuid.Must(uuid.NewV4()).String()
	ctx := context.WithValue(context.Background(), runtime.RUNTIME_CTX_USER_ID, userID)
	nk := newMockEnforcementNakamaModule()

	request := PlayerReportRequest{
		ReportedUserID: userID, // Same as reporter
		GroupID:        uuid.Must(uuid.NewV4()).String(),
		Reason:         "cheating",
		Description:    "test",
	}

	payload, _ := json.Marshal(request)
	_, err := PlayerReportRPC(ctx, nil, nil, nk, string(payload))
	if err == nil {
		t.Error("Expected error for self-reporting, got nil")
	}
}

func TestPlayerReportRPC_RateLimit(t *testing.T) {
	// Initialize ServiceSettings to avoid panic
	ServiceSettingsUpdate(&ServiceSettingsData{
		DiscordBotUserID: "discord-bot-user-id",
	})

	ctx := context.WithValue(context.Background(), runtime.RUNTIME_CTX_USER_ID, "reporter-user-id")
	nk := newMockEnforcementNakamaModule()

	reportedUserID := uuid.Must(uuid.NewV4()).String()
	groupID := uuid.Must(uuid.NewV4()).String()

	// Setup GuildGroupState in storage
	state := GuildGroupState{
		GroupID: groupID,
	}
	stateJSON, _ := json.Marshal(state)
	nk.storageObjects["discord-bot-user-id:"+StorageCollectionState+":"+groupID] = []*api.StorageObject{{
		Collection: StorageCollectionState,
		Key:        groupID,
		UserId:     "discord-bot-user-id",
		Value:      string(stateJSON),
	}}

	// Submit 5 reports (the limit)
	for i := 0; i < 5; i++ {
		request := PlayerReportRequest{
			ReportedUserID: reportedUserID,
			GroupID:        groupID,
			Reason:         "cheating",
			Description:    fmt.Sprintf("test report %d", i),
		}

		payload, _ := json.Marshal(request)
		_, err := PlayerReportRPC(ctx, nil, nil, nk, string(payload))
		if err != nil {
			t.Fatalf("Report %d failed: %v", i+1, err)
		}
	}

	// The 6th report should be rate limited
	request := PlayerReportRequest{
		ReportedUserID: reportedUserID,
		GroupID:        groupID,
		Reason:         "cheating",
		Description:    "test report 6",
	}

	payload, _ := json.Marshal(request)
	_, err := PlayerReportRPC(ctx, nil, nil, nk, string(payload))
	if err == nil {
		t.Error("Expected rate limit error on 6th report, got nil")
	}
}

func TestPlayerReportRPC_InvalidUUID(t *testing.T) {
	ctx := context.WithValue(context.Background(), runtime.RUNTIME_CTX_USER_ID, "reporter-user-id")
	nk := newMockEnforcementNakamaModule()

	tests := []struct {
		name           string
		reportedUserID string
		groupID        string
	}{
		{
			name:           "invalid reported_user_id",
			reportedUserID: "not-a-uuid",
			groupID:        uuid.Must(uuid.NewV4()).String(),
		},
		{
			name:           "invalid group_id",
			reportedUserID: uuid.Must(uuid.NewV4()).String(),
			groupID:        "not-a-uuid",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			request := PlayerReportRequest{
				ReportedUserID: tt.reportedUserID,
				GroupID:        tt.groupID,
				Reason:         "cheating",
				Description:    "test",
			}

			payload, _ := json.Marshal(request)
			_, err := PlayerReportRPC(ctx, nil, nil, nk, string(payload))
			if err == nil {
				t.Error("Expected UUID validation error, got nil")
			}
		})
	}
}

func TestCheckReportRateLimit(t *testing.T) {
	ctx := context.Background()
	nk := newMockEnforcementNakamaModule()
	userID := "test-user"

	// No reports yet - should pass
	err := checkReportRateLimit(ctx, nk, userID)
	if err != nil {
		t.Errorf("Expected no error with no reports, got: %v", err)
	}

	// Add 4 recent reports (should pass)
	now := time.Now().UTC()
	for i := 0; i < 4; i++ {
		report := PlayerReport{
			ID:             uuid.Must(uuid.NewV4()).String(),
			ReporterUserID: userID,
			ReportedUserID: uuid.Must(uuid.NewV4()).String(),
			GroupID:        uuid.Must(uuid.NewV4()).String(),
			Reason:         "test",
			Description:    "test",
			CreatedAt:      now,
			Status:         "pending",
		}
		reportJSON, _ := json.Marshal(report)

		writes := []*runtime.StorageWrite{
			{
				Collection: StorageCollectionPlayerReports,
				Key:        StorageKeyReportPrefix + report.ID,
				UserID:     userID,
				Value:      string(reportJSON),
			},
		}
		_, _ = nk.StorageWrite(ctx, writes)
	}

	// Still under limit (4) - should pass
	err = checkReportRateLimit(ctx, nk, userID)
	if err != nil {
		t.Errorf("Expected no error with 4 reports, got: %v", err)
	}

	// Add an old report (2 hours ago) - should not count towards limit
	oldReport := PlayerReport{
		ID:             uuid.Must(uuid.NewV4()).String(),
		ReporterUserID: userID,
		ReportedUserID: uuid.Must(uuid.NewV4()).String(),
		GroupID:        uuid.Must(uuid.NewV4()).String(),
		Reason:         "test",
		Description:    "test",
		CreatedAt:      now.Add(-2 * time.Hour),
		Status:         "pending",
	}
	oldReportJSON, _ := json.Marshal(oldReport)
	writes := []*runtime.StorageWrite{
		{
			Collection: StorageCollectionPlayerReports,
			Key:        StorageKeyReportPrefix + oldReport.ID,
			UserID:     userID,
			Value:      string(oldReportJSON),
		},
	}
	_, _ = nk.StorageWrite(ctx, writes)

	// Still should pass (4 recent + 1 old = 5 total, but only 4 recent)
	err = checkReportRateLimit(ctx, nk, userID)
	if err != nil {
		t.Errorf("Expected no error with 4 recent + 1 old reports, got: %v", err)
	}

	// Add 5th recent report (now total 5 recent + 1 old)
	report := PlayerReport{
		ID:             uuid.Must(uuid.NewV4()).String(),
		ReporterUserID: userID,
		ReportedUserID: uuid.Must(uuid.NewV4()).String(),
		GroupID:        uuid.Must(uuid.NewV4()).String(),
		Reason:         "test",
		Description:    "test",
		CreatedAt:      now,
		Status:         "pending",
	}
	reportJSON, _ := json.Marshal(report)
	writes = []*runtime.StorageWrite{
		{
			Collection: StorageCollectionPlayerReports,
			Key:        StorageKeyReportPrefix + report.ID,
			UserID:     userID,
			Value:      string(reportJSON),
		},
	}
	_, _ = nk.StorageWrite(ctx, writes)

	// Now at limit (5 recent) - should be rate limited
	err = checkReportRateLimit(ctx, nk, userID)
	if err == nil {
		t.Error("Expected rate limit error with 5 recent reports, got nil")
	}
}
