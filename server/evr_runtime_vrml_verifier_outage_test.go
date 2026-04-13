package server

import (
	"testing"
)

// TestVRMLScanQueue_OutageModeGatesHTTPRequests verifies that the outage mode
// check is evaluated dynamically (not just at Start time), so that enabling
// outage mode while the worker goroutine is already running prevents further
// VRML HTTP requests.
func TestVRMLScanQueue_OutageModeGatesHTTPRequests(t *testing.T) {
	// Save and restore original settings.
	original := ServiceSettings()
	defer func() {
		if original != nil {
			ServiceSettingsUpdate(original)
		} else {
			serviceSettings.Store(nil)
		}
	}()

	// Simulate: outage mode was OFF when the server started (goroutine launched).
	ServiceSettingsUpdate(&ServiceSettingsData{VRMLOutageMode: false})

	// Now outage mode gets enabled (e.g. admin flips the setting).
	ServiceSettingsUpdate(&ServiceSettingsData{VRMLOutageMode: true})

	// The worker loop should check VRMLOutageModeEnabled() on each iteration.
	// shouldSkipVRMLWork returns true when outage mode is enabled.
	if !shouldSkipVRMLWork() {
		t.Error("shouldSkipVRMLWork() returned false during outage mode; " +
			"the worker loop would still make HTTP requests to VRML")
	}

	// Disable outage mode again — worker should resume.
	ServiceSettingsUpdate(&ServiceSettingsData{VRMLOutageMode: false})
	if shouldSkipVRMLWork() {
		t.Error("shouldSkipVRMLWork() returned true when outage mode is disabled; " +
			"the worker loop would not process VRML requests")
	}
}
