package server

import (
	"testing"

	"github.com/gofrs/uuid/v5"
)

func TestSessionNEVRFormat(t *testing.T) {
	// Create a minimal session with nil dependencies (we're just testing the format)
	session := &sessionNEVR{
		id:     uuid.Must(uuid.NewV4()),
		userID: uuid.Must(uuid.NewV4()),
	}

	if session.Format() != SessionFormatNEVR {
		t.Errorf("Expected SessionFormatNEVR, got %v", session.Format())
	}
}

func TestSessionNEVRAccessors(t *testing.T) {
	sessionID := uuid.Must(uuid.NewV4())
	userID := uuid.Must(uuid.NewV4())
	clientIP := "192.168.1.1"
	clientPort := "12345"
	lang := "en"
	vars := map[string]string{"key": "value"}
	expiry := int64(1234567890)

	session := &sessionNEVR{
		id:         sessionID,
		userID:     userID,
		clientIP:   clientIP,
		clientPort: clientPort,
		lang:       lang,
		vars:       vars,
		expiry:     expiry,
	}

	if session.ID() != sessionID {
		t.Errorf("Expected session ID %v, got %v", sessionID, session.ID())
	}

	if session.UserID() != userID {
		t.Errorf("Expected user ID %v, got %v", userID, session.UserID())
	}

	if session.ClientIP() != clientIP {
		t.Errorf("Expected client IP %s, got %s", clientIP, session.ClientIP())
	}

	if session.ClientPort() != clientPort {
		t.Errorf("Expected client port %s, got %s", clientPort, session.ClientPort())
	}

	if session.Lang() != lang {
		t.Errorf("Expected lang %s, got %s", lang, session.Lang())
	}

	if session.Expiry() != expiry {
		t.Errorf("Expected expiry %d, got %d", expiry, session.Expiry())
	}

	sessionVars := session.Vars()
	if sessionVars["key"] != "value" {
		t.Errorf("Expected vars[key] = value, got %s", sessionVars["key"])
	}
}
