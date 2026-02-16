package server

import (
	"context"
	"encoding/json"
	"testing"

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
