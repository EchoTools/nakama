package server

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestServiceSettings_NeverReturnsNil verifies that ServiceSettings() returns
// a non-nil *ServiceSettingsData even before any initialization has occurred.
// This guards against nil-pointer panics in callers that read settings fields.
func TestServiceSettings_NeverReturnsNil(t *testing.T) {
	// Store nil explicitly to simulate pre-initialization state.
	ServiceSettingsUpdate(nil)

	got := ServiceSettings()
	require.NotNil(t, got, "ServiceSettings() must never return nil")

	// The returned struct should be a zero-value (empty) ServiceSettingsData.
	require.Equal(t, "", got.LinkInstructions, "expected empty LinkInstructions on zero-value settings")
	require.Equal(t, "", got.DisableLoginMessage, "expected empty DisableLoginMessage on zero-value settings")
}

// TestServiceSettings_ReturnsStoredValue verifies that after storing a real
// settings value, ServiceSettings() returns the stored data, not the fallback.
func TestServiceSettings_ReturnsStoredValue(t *testing.T) {
	want := &ServiceSettingsData{
		LinkInstructions: "test-instructions",
	}
	ServiceSettingsUpdate(want)
	defer ServiceSettingsUpdate(nil) // restore nil for other tests

	got := ServiceSettings()
	require.NotNil(t, got)
	require.Equal(t, "test-instructions", got.LinkInstructions)
}

// TestPruneSettingsWireKeysAreLoadBearing pins the JSON keys of PruneSettings
// to the object already stored in production.
//
// These are not cosmetic struct tags. ServiceSettingsLoad reads the settings
// object out of storage and, on the first load of a process, writes it straight
// back (evr_global_settings.go:281-294) using json.Marshal of the whole struct
// with no omitempty. Renaming a key therefore does two things on the next
// restart, not one: the stored value stops being read (the field falls back to
// its zero value), and the write-back then re-serializes the struct WITHOUT the
// old key, erasing what the operator had configured. There is no later
// opportunity to notice and migrate -- the evidence is gone.
//
// `leave_orphan_groups` really does drive DeleteOrphanedGroups, and the name
// mismatch (leave vs delete) is deliberate history, documented on the field.
// This test is what turns that comment into a tripwire.
func TestPruneSettingsWireKeysAreLoadBearing(t *testing.T) {
	// Exactly what a deployment that turned deletes on has stored today.
	const stored = `{"prune_settings":{"leave_orphan_guilds":true,"leave_orphan_groups":true,"safety_limit":5}}`

	var data ServiceSettingsData
	require.NoError(t, json.Unmarshal([]byte(stored), &data))

	require.True(t, data.PruneSettings.DeleteOrphanedGroups,
		"stored key leave_orphan_groups no longer drives DeleteOrphanedGroups; a rename silently disarms a configured delete")
	require.True(t, data.PruneSettings.LeaveOrphanedGuilds)
	require.Equal(t, 5, data.PruneSettings.SafetyLimit)

	// The write-back on next boot must not drop the operator's setting.
	out, err := json.Marshal(&data)
	require.NoError(t, err)

	var round map[string]any
	require.NoError(t, json.Unmarshal(out, &round))
	prune, ok := round["prune_settings"].(map[string]any)
	require.True(t, ok, "prune_settings key changed; stored settings would be orphaned")

	require.Equal(t, true, prune["leave_orphan_groups"],
		"the re-serialized settings object lost leave_orphan_groups=true; ServiceSettingsLoad writes this back over the stored object, so the operator's configuration would be destroyed, not merely ignored")

	// ReportOnly is new in this PR, so it must serialize under the key the
	// documentation tells operators to set.
	_, hasReportOnly := prune["report_only"]
	require.True(t, hasReportOnly, "report_only missing from the serialized prune settings")
}
