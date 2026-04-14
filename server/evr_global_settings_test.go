package server

import (
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
