package server

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGroupMetadata_TimeBlockDefaults(t *testing.T) {
	tests := []struct {
		name                 string
		metadata             *GroupMetadata
		expectedDefaultBlock int
		expectedMaintenance  int
	}{
		{
			name:                 "zero values return defaults",
			metadata:             &GroupMetadata{},
			expectedDefaultBlock: 50,
			expectedMaintenance:  10,
		},
		{
			name: "negative values return defaults",
			metadata: &GroupMetadata{
				DefaultBlockMinutes: -1,
				MaintenanceMinutes:  -5,
			},
			expectedDefaultBlock: 50,
			expectedMaintenance:  10,
		},
		{
			name: "custom values are preserved",
			metadata: &GroupMetadata{
				DefaultBlockMinutes: 45,
				MaintenanceMinutes:  15,
			},
			expectedDefaultBlock: 45,
			expectedMaintenance:  15,
		},
		{
			name: "only default block custom",
			metadata: &GroupMetadata{
				DefaultBlockMinutes: 60,
				MaintenanceMinutes:  0,
			},
			expectedDefaultBlock: 60,
			expectedMaintenance:  10,
		},
		{
			name: "only maintenance custom",
			metadata: &GroupMetadata{
				DefaultBlockMinutes: 0,
				MaintenanceMinutes:  20,
			},
			expectedDefaultBlock: 50,
			expectedMaintenance:  20,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expectedDefaultBlock, tt.metadata.GetDefaultBlockMinutes())
			assert.Equal(t, tt.expectedMaintenance, tt.metadata.GetMaintenanceMinutes())
		})
	}
}

func TestGroupMetadata_TimeBlockCustom(t *testing.T) {
	metadata := &GroupMetadata{
		GuildID:             "test-guild",
		DefaultBlockMinutes: 90,
		MaintenanceMinutes:  5,
	}

	assert.Equal(t, 90, metadata.GetDefaultBlockMinutes())
	assert.Equal(t, 5, metadata.GetMaintenanceMinutes())

	// Total block should be 95 minutes
	totalBlock := metadata.GetDefaultBlockMinutes() + metadata.GetMaintenanceMinutes()
	assert.Equal(t, 95, totalBlock)
}

func TestGroupMetadata_BackwardCompatJSON(t *testing.T) {
	tests := []struct {
		name                 string
		json                 string
		expectedDefaultBlock int
		expectedMaintenance  int
	}{
		{
			name:                 "old JSON without time block fields",
			json:                 `{"guild_id":"test-guild","owner_id":"test-owner"}`,
			expectedDefaultBlock: 50,
			expectedMaintenance:  10,
		},
		{
			name:                 "JSON with only default_block_minutes",
			json:                 `{"guild_id":"test-guild","default_block_minutes":60}`,
			expectedDefaultBlock: 60,
			expectedMaintenance:  10,
		},
		{
			name:                 "JSON with only maintenance_minutes",
			json:                 `{"guild_id":"test-guild","maintenance_minutes":15}`,
			expectedDefaultBlock: 50,
			expectedMaintenance:  15,
		},
		{
			name:                 "JSON with both time block fields",
			json:                 `{"guild_id":"test-guild","default_block_minutes":45,"maintenance_minutes":20}`,
			expectedDefaultBlock: 45,
			expectedMaintenance:  20,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var metadata GroupMetadata
			err := json.Unmarshal([]byte(tt.json), &metadata)
			require.NoError(t, err, "JSON unmarshaling should succeed")

			assert.Equal(t, tt.expectedDefaultBlock, metadata.GetDefaultBlockMinutes())
			assert.Equal(t, tt.expectedMaintenance, metadata.GetMaintenanceMinutes())
		})
	}
}

func TestGroupMetadata_JSONOmitEmpty(t *testing.T) {
	tests := []struct {
		name             string
		metadata         *GroupMetadata
		shouldContainKey map[string]bool
	}{
		{
			name: "zero values omitted from JSON",
			metadata: &GroupMetadata{
				GuildID:             "test-guild",
				DefaultBlockMinutes: 0,
				MaintenanceMinutes:  0,
			},
			shouldContainKey: map[string]bool{
				"guild_id":              true,
				"default_block_minutes": false,
				"maintenance_minutes":   false,
			},
		},
		{
			name: "custom values included in JSON",
			metadata: &GroupMetadata{
				GuildID:             "test-guild",
				DefaultBlockMinutes: 45,
				MaintenanceMinutes:  15,
			},
			shouldContainKey: map[string]bool{
				"guild_id":              true,
				"default_block_minutes": true,
				"maintenance_minutes":   true,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			data, err := json.Marshal(tt.metadata)
			require.NoError(t, err, "JSON marshaling should succeed")

			var result map[string]interface{}
			err = json.Unmarshal(data, &result)
			require.NoError(t, err, "JSON unmarshaling should succeed")

			for key, shouldContain := range tt.shouldContainKey {
				_, exists := result[key]
				if shouldContain {
					assert.True(t, exists, "JSON should contain key %s", key)
				} else {
					assert.False(t, exists, "JSON should not contain key %s", key)
				}
			}
		})
	}
}

func TestGroupMetadata_TimeBlockRoundtrip(t *testing.T) {
	original := &GroupMetadata{
		GuildID:             "test-guild",
		OwnerID:             "test-owner",
		DefaultBlockMinutes: 55,
		MaintenanceMinutes:  8,
	}

	// Marshal to JSON
	data, err := json.Marshal(original)
	require.NoError(t, err, "JSON marshaling should succeed")

	// Unmarshal back
	var restored GroupMetadata
	err = json.Unmarshal(data, &restored)
	require.NoError(t, err, "JSON unmarshaling should succeed")

	// Verify values match
	assert.Equal(t, original.GuildID, restored.GuildID)
	assert.Equal(t, original.OwnerID, restored.OwnerID)
	assert.Equal(t, original.DefaultBlockMinutes, restored.DefaultBlockMinutes)
	assert.Equal(t, original.MaintenanceMinutes, restored.MaintenanceMinutes)
	assert.Equal(t, original.GetDefaultBlockMinutes(), restored.GetDefaultBlockMinutes())
	assert.Equal(t, original.GetMaintenanceMinutes(), restored.GetMaintenanceMinutes())
}
