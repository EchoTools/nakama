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
	"net"
	"net/http"
	"strconv"
	"strings"

	"github.com/gofrs/uuid/v5"
	"github.com/gorilla/websocket"
	"go.uber.org/zap"
	"google.golang.org/protobuf/encoding/protojson"
)

func NewNEVRSocketWsAcceptor(logger *zap.Logger, config Config, sessionRegistry SessionRegistry, sessionCache SessionCache, statusRegistry StatusRegistry, matchmaker Matchmaker, tracker Tracker, metrics Metrics, runtime *Runtime, jsonpbMarshaler *protojson.MarshalOptions, jsonpbUnmarshaler *protojson.UnmarshalOptions, pipeline *Pipeline, storageIndex StorageIndex) func(http.ResponseWriter, *http.Request) {
	upgrader := &websocket.Upgrader{
		ReadBufferSize:  config.GetSocket().ReadBufferSizeBytes,
		WriteBufferSize: config.GetSocket().WriteBufferSizeBytes,
		CheckOrigin:     func(r *http.Request) bool { return true },
	}

	sessionIdGen := uuid.NewGenWithHWAF(func() (net.HardwareAddr, error) {
		hash := NodeToHash(config.GetName())
		return hash[:], nil
	})

	// This handler will be attached to the API Gateway server.
	return func(w http.ResponseWriter, r *http.Request) {
		// Check authentication.
		var token string
		if auth := r.Header["Authorization"]; len(auth) >= 1 {
			// Attempt header based authentication.
			const prefix = "Bearer "
			if !strings.HasPrefix(auth[0], prefix) {
				http.Error(w, "Missing or invalid token", 401)
				return
			}
			token = auth[0][len(prefix):]
		} else {
			// Attempt query parameter based authentication.
			token = r.URL.Query().Get("token")
		}
		if token == "" {
			http.Error(w, "Missing or invalid token", 401)
			return
		}

		var (
			ok       bool              = false
			userID   uuid.UUID         = uuid.Nil
			username string            = ""
			vars     map[string]string = nil
			expiry   int64             = 0
		)

		// Try server key authentication first (for game servers)
		if token == config.GetSocket().ServerKey {
			// Server key auth - allow connection with empty userID for game server connections
			ok = true
		} else {
			// JWT token authentication for regular clients
			userID, username, vars, expiry, _, _, ok = parseToken([]byte(config.GetSession().EncryptionKey), token)
			if !ok || !sessionCache.IsValidSession(userID, expiry, token) {
				http.Error(w, "Missing or invalid token", 401)
				return
			}
		}

		if !ok {
			http.Error(w, "Missing or invalid token", 401)
			return
		}

		// Extract lang query parameter. Use a default if empty or not present.
		lang := "en"
		if langParam := r.URL.Query().Get("lang"); langParam != "" {
			lang = langParam
		}

		// Upgrade to WebSocket.
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			// http.Error is invoked automatically from within the Upgrade function.
			logger.Debug("Could not upgrade to WebSocket", zap.Error(err))
			return
		}

		clientIP, clientPort := extractClientAddressFromRequest(logger, r)
		status, _ := strconv.ParseBool(r.URL.Query().Get("status"))
		sessionID := uuid.Must(sessionIdGen.NewV1())

		// Mark the start of the session.
		metrics.CountWebsocketOpened(1)

		// Wrap the connection for application handling.
		session := NewSessionNEVR(logger, config, sessionID, userID, username, vars, expiry, clientIP, clientPort, lang, conn, sessionRegistry, statusRegistry, matchmaker, tracker, metrics, pipeline, runtime, storageIndex)

		// Add to the session registry.
		sessionRegistry.Add(session)

		// Register initial status tracking and presence(s) for this session.
		if !userID.IsNil() {
			statusRegistry.Follow(sessionID, map[uuid.UUID]struct{}{userID: {}})

			if status {
				// Both notification and status presence.
				tracker.TrackMulti(session.Context(), sessionID, []*TrackerOp{
					{
						Stream: PresenceStream{Mode: StreamModeNotifications, Subject: userID},
						Meta:   PresenceMeta{Format: SessionFormatNEVR, Username: username, Hidden: true},
					},
					{
						Stream: PresenceStream{Mode: StreamModeStatus, Subject: userID},
						Meta:   PresenceMeta{Format: SessionFormatNEVR, Username: username, Status: ""},
					},
				}, userID)
			} else {
				// Only notification presence.
				tracker.Track(session.Context(), sessionID, PresenceStream{Mode: StreamModeNotifications, Subject: userID}, userID, PresenceMeta{Format: SessionFormatNEVR, Username: username, Hidden: true})
			}

			if config.GetSession().SingleSocket {
				// Kick any other sockets for this user.
				go sessionRegistry.SingleSession(session.Context(), tracker, userID, sessionID)
			}
		}

		// Allow the server to begin processing incoming messages from this session.
		session.Consume()

		// Mark the end of the session.
		metrics.CountWebsocketClosed(1)
	}
}
