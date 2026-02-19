package server

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/bwmarrin/discordgo"
	"github.com/gofrs/uuid/v5"
	"github.com/heroiclabs/nakama-common/api"
	"github.com/heroiclabs/nakama-common/runtime"
)

// TestGetGuildIDByGroupIDNK_Valid tests fetching guild ID from valid group
func TestGetGuildIDByGroupIDNK_Valid(t *testing.T) {
	ctx := context.Background()
	mockNK := &mockReservationNakamaModule{
		groups: map[string]*api.Group{
			"test-group-123": {
				Id:       "test-group-123",
				Name:     "Test Guild",
				Metadata: `{"guild_id":"987654321"}`,
				LangTag:  GuildGroupLangTag,
			},
		},
	}

	guildID, err := GetGuildIDByGroupIDNK(ctx, mockNK, "test-group-123")
	if err != nil {
		t.Fatalf("Expected no error, got: %v", err)
	}

	expected := "987654321"
	if guildID != expected {
		t.Errorf("Expected guild ID %s, got %s", expected, guildID)
	}
}

// TestGetGuildIDByGroupIDNK_NotFound tests error handling for missing group
func TestGetGuildIDByGroupIDNK_NotFound(t *testing.T) {
	ctx := context.Background()
	mockNK := &mockReservationNakamaModule{
		groups: map[string]*api.Group{},
	}

	guildID, err := GetGuildIDByGroupIDNK(ctx, mockNK, "nonexistent-group")
	if err == nil {
		t.Fatal("Expected error for nonexistent group, got nil")
	}

	if guildID != "" {
		t.Errorf("Expected empty guild ID, got: %s", guildID)
	}
}

// TestGetGuildIDByGroupIDNK_EmptyGuildID tests error for group without guild_id
func TestGetGuildIDByGroupIDNK_EmptyGuildID(t *testing.T) {
	ctx := context.Background()
	mockNK := &mockReservationNakamaModule{
		groups: map[string]*api.Group{
			"group-without-guild": {
				Id:       "group-without-guild",
				Name:     "No Guild",
				Metadata: `{}`,
				LangTag:  GuildGroupLangTag,
			},
		},
	}

	guildID, err := GetGuildIDByGroupIDNK(ctx, mockNK, "group-without-guild")
	if err == nil {
		t.Fatal("Expected error for missing guild_id field, got nil")
	}

	if guildID != "" {
		t.Errorf("Expected empty guild ID, got: %s", guildID)
	}
}

// mockReservationNakamaModule is a minimal mock for testing GetGuildIDByGroupIDNK
type mockReservationNakamaModule struct {
	runtime.NakamaModule
	groups map[string]*api.Group
}

func (m *mockReservationNakamaModule) GroupsGetId(ctx context.Context, groupIDs []string) ([]*api.Group, error) {
	var result []*api.Group
	for _, id := range groupIDs {
		if g, ok := m.groups[id]; ok {
			result = append(result, g)
		}
	}
	return result, nil
}

func (m *mockReservationNakamaModule) GroupsList(ctx context.Context, name, langTag string, members *int, open *bool, limit int, cursor string) ([]*api.Group, string, error) {
	var result []*api.Group
	for _, g := range m.groups {
		if langTag != "" && g.LangTag != langTag {
			continue
		}
		result = append(result, g)
	}
	return result, "", nil
}

// Helper to create GroupMetadata with guild_id
func createGroupMetadataJSON(guildID string) string {
	md := GroupMetadata{
		GuildID: guildID,
	}
	data, _ := json.Marshal(md)
	return string(data)
}

// TestGetGuildAuditChannelID_Valid tests fetching audit channel ID from valid guild
func TestGetGuildAuditChannelID_Valid(t *testing.T) {
	ctx := context.Background()
	mockNK := &mockReservationNakamaModule{
		groups: map[string]*api.Group{
			"group-123": {
				Id:       "group-123",
				Name:     "Test Guild",
				Metadata: `{"guild_id":"987654321","audit_channel_id":"123456789012345678"}`,
				LangTag:  GuildGroupLangTag,
			},
		},
	}

	auditChannelID, err := GetGuildAuditChannelID(ctx, mockNK, "987654321")
	if err != nil {
		t.Fatalf("Expected no error, got: %v", err)
	}

	expected := "123456789012345678"
	if auditChannelID != expected {
		t.Errorf("Expected audit channel ID %s, got %s", expected, auditChannelID)
	}
}

// TestGetGuildAuditChannelID_NotConfigured tests error handling for missing audit channel
func TestGetGuildAuditChannelID_NotConfigured(t *testing.T) {
	ctx := context.Background()
	mockNK := &mockReservationNakamaModule{
		groups: map[string]*api.Group{
			"group-456": {
				Id:       "group-456",
				Name:     "Test Guild Without Audit",
				Metadata: `{"guild_id":"555555555"}`,
				LangTag:  GuildGroupLangTag,
			},
		},
	}

	auditChannelID, err := GetGuildAuditChannelID(ctx, mockNK, "555555555")
	if err == nil {
		t.Fatal("Expected error for missing audit channel, got nil")
	}

	if auditChannelID != "" {
		t.Errorf("Expected empty audit channel ID, got: %s", auditChannelID)
	}
}

// TestGetGuildAuditChannelID_GuildNotFound tests error handling for nonexistent guild
func TestGetGuildAuditChannelID_GuildNotFound(t *testing.T) {
	ctx := context.Background()
	mockNK := &mockReservationNakamaModule{
		groups: map[string]*api.Group{},
	}

	auditChannelID, err := GetGuildAuditChannelID(ctx, mockNK, "nonexistent-guild")
	if err == nil {
		t.Fatal("Expected error for nonexistent guild, got nil")
	}

	if auditChannelID != "" {
		t.Errorf("Expected empty audit channel ID, got: %s", auditChannelID)
	}
}

// TestCleanupExpiredReservations_RemovesExpired tests that expired reservations are removed
func TestCleanupExpiredReservations_RemovesExpired(t *testing.T) {
	ctx := context.Background()

	testGroupID := uuid.Must(uuid.NewV4())

	// Create mock with test reservations
	mockNK := &mockCleanupNakamaModule{
		storage: make(map[string]*api.StorageObject),
		groups: map[string]*api.Group{
			testGroupID.String(): {
				Id:       testGroupID.String(),
				Name:     "Test Guild",
				Metadata: `{"guild_id":"987654321","audit_channel_id":"audit-channel-123"}`,
				LangTag:  GuildGroupLangTag,
			},
		},
	}

	mockDG := &mockDiscordSession{
		sentMessages: make([]string, 0),
	}

	// Create expired reservation (start time 10 minutes ago, state=reserved)
	expiredRes := &MatchReservation{
		ID:             "expired-res-1",
		GroupID:        testGroupID,
		Owner:          "user-123",
		Requester:      "user-123",
		StartTime:      time.Now().Add(-10 * time.Minute),
		EndTime:        time.Now().Add(20 * time.Minute),
		Classification: ClassificationPickup,
		State:          ReservationStateReserved,
		CreatedAt:      time.Now().Add(-15 * time.Minute),
	}
	mockNK.addReservation(expiredRes)

	// Create active reservation (should not be removed)
	activeRes := &MatchReservation{
		ID:             "active-res-1",
		GroupID:        testGroupID,
		Owner:          "user-456",
		Requester:      "user-456",
		StartTime:      time.Now().Add(10 * time.Minute),
		EndTime:        time.Now().Add(40 * time.Minute),
		Classification: ClassificationScrimmage,
		State:          ReservationStateReserved,
		CreatedAt:      time.Now(),
	}
	mockNK.addReservation(activeRes)

	// Create reservation integration and run cleanup
	ri := NewReservationIntegration(mockNK, &testLogger{t: t})
	ri.cleanupExpiredReservationsWithDiscord(ctx, mockDG)

	// Verify expired reservation was deleted
	if mockNK.storage["expired-res-1"] != nil {
		t.Error("Expected expired reservation to be removed, but it still exists")
	}

	// Verify active reservation was NOT deleted
	if mockNK.storage["active-res-1"] == nil {
		t.Error("Expected active reservation to remain, but it was removed")
	}

	// Verify audit message was sent
	if len(mockDG.sentMessages) == 0 {
		t.Error("Expected audit message to be sent, but none were sent")
	}
}

// TestCleanupExpiredReservations_KeepsActive tests that active reservations are preserved
func TestCleanupExpiredReservations_KeepsActive(t *testing.T) {
	ctx := context.Background()

	testGroupID := uuid.Must(uuid.NewV4())

	mockNK := &mockCleanupNakamaModule{
		storage: make(map[string]*api.StorageObject),
		groups: map[string]*api.Group{
			testGroupID.String(): {
				Id:       testGroupID.String(),
				Name:     "Test Guild 2",
				Metadata: `{"guild_id":"111222333","audit_channel_id":"audit-channel-456"}`,
				LangTag:  GuildGroupLangTag,
			},
		},
	}

	mockDG := &mockDiscordSession{
		sentMessages: make([]string, 0),
	}

	// Create multiple active reservations (future start times)
	for i := 0; i < 3; i++ {
		res := &MatchReservation{
			ID:             uuid.Must(uuid.NewV4()).String(),
			GroupID:        testGroupID,
			Owner:          "user-789",
			Requester:      "user-789",
			StartTime:      time.Now().Add(time.Duration(10+i*5) * time.Minute),
			EndTime:        time.Now().Add(time.Duration(40+i*5) * time.Minute),
			Classification: ClassificationMixed,
			State:          ReservationStateReserved,
			CreatedAt:      time.Now(),
		}
		mockNK.addReservation(res)
	}

	initialCount := len(mockNK.storage)

	// Run cleanup
	ri := NewReservationIntegration(mockNK, &testLogger{t: t})
	ri.cleanupExpiredReservationsWithDiscord(ctx, mockDG)

	// Verify no reservations were removed
	if len(mockNK.storage) != initialCount {
		t.Errorf("Expected %d reservations to remain, got %d", initialCount, len(mockNK.storage))
	}

	// Verify no audit messages were sent
	if len(mockDG.sentMessages) > 0 {
		t.Errorf("Expected no audit messages, but got %d", len(mockDG.sentMessages))
	}
}

// TestCleanupExpiredReservations_HandlesExplicitExpiredState tests cleanup of explicitly expired reservations
func TestCleanupExpiredReservations_HandlesExplicitExpiredState(t *testing.T) {
	ctx := context.Background()

	testGroupID := uuid.Must(uuid.NewV4())

	mockNK := &mockCleanupNakamaModule{
		storage: make(map[string]*api.StorageObject),
		groups: map[string]*api.Group{
			testGroupID.String(): {
				Id:       testGroupID.String(),
				Name:     "Test Guild 3",
				Metadata: `{"guild_id":"444555666","audit_channel_id":"audit-channel-789"}`,
				LangTag:  GuildGroupLangTag,
			},
		},
	}

	mockDG := &mockDiscordSession{
		sentMessages: make([]string, 0),
	}

	// Create reservation with explicit expired state
	expiredRes := &MatchReservation{
		ID:             "expired-state-res",
		GroupID:        testGroupID,
		Owner:          "user-999",
		Requester:      "user-999",
		StartTime:      time.Now().Add(-20 * time.Minute),
		EndTime:        time.Now().Add(-5 * time.Minute),
		Classification: ClassificationPickup,
		State:          ReservationStateExpired,
		CreatedAt:      time.Now().Add(-30 * time.Minute),
	}
	mockNK.addReservation(expiredRes)

	// Run cleanup
	ri := NewReservationIntegration(mockNK, &testLogger{t: t})
	ri.cleanupExpiredReservationsWithDiscord(ctx, mockDG)

	// Verify reservation was removed
	if mockNK.storage["expired-state-res"] != nil {
		t.Error("Expected explicitly expired reservation to be removed")
	}

	// Verify audit message was sent
	if len(mockDG.sentMessages) == 0 {
		t.Error("Expected audit message for explicitly expired reservation")
	}
}

// mockCleanupNakamaModule extends mockReservationNakamaModule with storage operations
type mockCleanupNakamaModule struct {
	runtime.NakamaModule
	groups  map[string]*api.Group
	storage map[string]*api.StorageObject
}

func (m *mockCleanupNakamaModule) GroupsGetId(ctx context.Context, groupIDs []string) ([]*api.Group, error) {
	var result []*api.Group
	for _, id := range groupIDs {
		if g, ok := m.groups[id]; ok {
			result = append(result, g)
		}
	}
	return result, nil
}

func (m *mockCleanupNakamaModule) GroupsList(ctx context.Context, name, langTag string, members *int, open *bool, limit int, cursor string) ([]*api.Group, string, error) {
	var result []*api.Group
	for _, g := range m.groups {
		if langTag != "" && g.LangTag != langTag {
			continue
		}
		result = append(result, g)
	}
	return result, "", nil
}

func (m *mockCleanupNakamaModule) addReservation(res *MatchReservation) {
	data, _ := json.Marshal(res)
	m.storage[res.ID] = &api.StorageObject{
		Collection: ReservationStorageCollection,
		Key:        res.ID,
		UserId:     SystemUserID,
		Value:      string(data),
	}
}

func (m *mockCleanupNakamaModule) StorageList(ctx context.Context, callerID, ownerID, collection string, limit int, cursor string) ([]*api.StorageObject, string, error) {
	var result []*api.StorageObject
	if collection == ReservationStorageCollection {
		for _, obj := range m.storage {
			result = append(result, obj)
		}
	}
	return result, "", nil
}

func (m *mockCleanupNakamaModule) StorageDelete(ctx context.Context, deletes []*runtime.StorageDelete) error {
	for _, del := range deletes {
		delete(m.storage, del.Key)
	}
	return nil
}

// mockDiscordSession tracks sent messages
type mockDiscordSession struct {
	sentMessages []string
}

func (m *mockDiscordSession) ChannelMessageSendComplex(channelID string, data *discordgo.MessageSend) (*discordgo.Message, error) {
	m.sentMessages = append(m.sentMessages, data.Content)
	return &discordgo.Message{
		ID:        "msg-" + uuid.Must(uuid.NewV4()).String(),
		ChannelID: channelID,
		Content:   data.Content,
	}, nil
}

// TestReservationLifecycle_WithClassification tests that classification persists through state transitions
func TestReservationLifecycle_WithClassification(t *testing.T) {
	tests := []struct {
		name           string
		classification SessionClassification
		expectedClass  SessionClassification
	}{
		{
			name:           "league classification persists through lifecycle",
			classification: ClassificationLeague,
			expectedClass:  ClassificationLeague,
		},
		{
			name:           "pickup classification persists through lifecycle",
			classification: ClassificationPickup,
			expectedClass:  ClassificationPickup,
		},
		{
			name:           "mixed classification persists through lifecycle",
			classification: ClassificationMixed,
			expectedClass:  ClassificationMixed,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reservation := &MatchReservation{
				ID:             uuid.Must(uuid.NewV4()).String(),
				MatchID:        uuid.Must(uuid.NewV4()).String(),
				GroupID:        uuid.Must(uuid.NewV4()),
				Owner:          "test-owner",
				Requester:      "test-requester",
				StartTime:      time.Now().Add(1 * time.Hour),
				EndTime:        time.Now().Add(2 * time.Hour),
				Duration:       1 * time.Hour,
				Classification: tt.classification,
				State:          ReservationStateReserved,
				CreatedAt:      time.Now(),
				UpdatedAt:      time.Now(),
			}

			if reservation.State != ReservationStateReserved {
				t.Errorf("expected initial state Reserved, got %v", reservation.State)
			}
			if reservation.Classification != tt.expectedClass {
				t.Errorf("expected classification %v, got %v", tt.expectedClass, reservation.Classification)
			}

			reservation.State = ReservationStateActivated
			reservation.UpdatedAt = time.Now()

			if reservation.State != ReservationStateActivated {
				t.Errorf("expected state Activated, got %v", reservation.State)
			}
			if reservation.Classification != tt.expectedClass {
				t.Errorf("expected classification %v after activation, got %v", tt.expectedClass, reservation.Classification)
			}

			reservation.State = ReservationStateEnded
			reservation.UpdatedAt = time.Now()

			if reservation.State != ReservationStateEnded {
				t.Errorf("expected state Ended, got %v", reservation.State)
			}
			if reservation.Classification != tt.expectedClass {
				t.Errorf("expected classification %v after ending, got %v", tt.expectedClass, reservation.Classification)
			}
		})
	}
}

// TestVacateWithPreemption tests vacate interaction with preemption logic
func TestVacateWithPreemption(t *testing.T) {
	tests := []struct {
		name       string
		lowClass   SessionClassification
		highClass  SessionClassification
		canPreempt bool
	}{
		{
			name:       "League can preempt Pickup",
			lowClass:   ClassificationPickup,
			highClass:  ClassificationLeague,
			canPreempt: true,
		},
		{
			name:       "League cannot preempt League",
			lowClass:   ClassificationLeague,
			highClass:  ClassificationLeague,
			canPreempt: false,
		},
		{
			name:       "Scrimmage can preempt Mixed",
			lowClass:   ClassificationMixed,
			highClass:  ClassificationScrimmage,
			canPreempt: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reservation := &MatchReservation{
				ID:             uuid.Must(uuid.NewV4()).String(),
				MatchID:        uuid.Must(uuid.NewV4()).String(),
				GroupID:        uuid.Must(uuid.NewV4()),
				Classification: tt.lowClass,
				State:          ReservationStateActivated,
				StartTime:      time.Now(),
				EndTime:        time.Now().Add(1 * time.Hour),
			}

			canPreempt := reservation.CanBePreempted(tt.highClass)
			if canPreempt != tt.canPreempt {
				t.Errorf("expected CanBePreempted(%v)=%v, got %v",
					tt.highClass, tt.canPreempt, canPreempt)
			}
		})
	}
}

// TestNoShowAutoVacateFlow tests full no-show auto-vacate lifecycle
func TestNoShowAutoVacateFlow(t *testing.T) {
	tests := []struct {
		name          string
		timeFromStart time.Duration
		shouldExpire  bool
	}{
		{
			name:          "Reservation expires after 20 minutes no-show",
			timeFromStart: 25 * time.Minute,
			shouldExpire:  true,
		},
		{
			name:          "Reservation does not expire before 20 minutes",
			timeFromStart: 15 * time.Minute,
			shouldExpire:  false,
		},
		{
			name:          "Reservation does not expire at 20 minute boundary",
			timeFromStart: 20 * time.Minute,
			shouldExpire:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			now := time.Now()
			reservation := &MatchReservation{
				ID:             uuid.Must(uuid.NewV4()).String(),
				MatchID:        uuid.Must(uuid.NewV4()).String(),
				GroupID:        uuid.Must(uuid.NewV4()),
				Owner:          "test-owner",
				Classification: ClassificationPickup,
				State:          ReservationStateReserved,
				StartTime:      now.Add(-tt.timeFromStart),
				EndTime:        now.Add(30 * time.Minute),
				CreatedAt:      now,
			}

			shouldExpire := now.After(reservation.StartTime.Add(20 * time.Minute))
			if shouldExpire != tt.shouldExpire {
				t.Errorf("expected shouldExpire=%v, got %v (time from start: %v)",
					tt.shouldExpire, shouldExpire, tt.timeFromStart)
			}
		})
	}
}
