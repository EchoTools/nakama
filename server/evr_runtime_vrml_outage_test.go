package server

import (
	"context"
	"errors"
	"testing"
)

func TestVRMLOutageModeEnabled_ReadsFromServiceSettings(t *testing.T) {
	// Save and restore original settings.
	original := ServiceSettings()
	defer func() {
		if original != nil {
			ServiceSettingsUpdate(original)
		}
	}()

	// When outage mode is true in settings, function should return true.
	ServiceSettingsUpdate(&ServiceSettingsData{VRMLOutageMode: true})
	if !VRMLOutageModeEnabled() {
		t.Error("expected VRMLOutageModeEnabled() = true when setting is true")
	}

	// When outage mode is false in settings, function should return false.
	ServiceSettingsUpdate(&ServiceSettingsData{VRMLOutageMode: false})
	if VRMLOutageModeEnabled() {
		t.Error("expected VRMLOutageModeEnabled() = false when setting is false")
	}
}

func TestIsVRMLOutageError(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"deadline exceeded", context.DeadlineExceeded, true},
		{"canceled", context.Canceled, true},
		{"connection refused", errors.New("dial tcp: connection refused"), true},
		{"no such host", errors.New("no such host"), true},
		{"i/o timeout", errors.New("i/o timeout"), true},
		{"tls timeout", errors.New("TLS handshake timeout"), true},
		{"service unavailable", errors.New("service unavailable"), true},
		{"bad gateway", errors.New("bad gateway"), true},
		{"gateway timeout", errors.New("gateway timeout"), true},
		{"status code 500", errors.New("status code: 500"), true},
		{"normal error", errors.New("invalid user"), false},
		{"auth error", errors.New("unauthorized"), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsVRMLOutageError(tt.err); got != tt.want {
				t.Errorf("IsVRMLOutageError(%v) = %v, want %v", tt.err, got, tt.want)
			}
		})
	}
}
