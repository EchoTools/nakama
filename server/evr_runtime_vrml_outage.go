package server

import (
	"context"
	"errors"
	"strings"
)

const (
	vrmlOutageNoCacheMessage = "VRML services are currently out of service. We do not have cached verification data for your account yet. Please try again when VRML is back online."
)

// VRMLOutageModeEnabled returns true when VRML integrations should avoid live API calls.
// This is controlled by global service settings (Global/settings) via vrml_outage_mode.
func VRMLOutageModeEnabled() bool {
	return true
}

// IsVRMLOutageError identifies likely network/service outage failures from VRML calls.
func IsVRMLOutageError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return true
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "connection refused") ||
		strings.Contains(msg, "no such host") ||
		strings.Contains(msg, "i/o timeout") ||
		strings.Contains(msg, "tls handshake timeout") ||
		strings.Contains(msg, "service unavailable") ||
		strings.Contains(msg, "bad gateway") ||
		strings.Contains(msg, "gateway timeout") ||
		strings.Contains(msg, "status code: 5")
}
