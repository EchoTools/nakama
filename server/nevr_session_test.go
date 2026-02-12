// Copyright 2018 The Nakama Authors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

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
