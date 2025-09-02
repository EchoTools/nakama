package service

import (
	"context"
	"crypto/sha1"
	"fmt"
	"maps"
	"net"
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"strings"

	evr "github.com/echotools/nakama/v3/protocol"
	"github.com/heroiclabs/nakama/v3/server"

	"github.com/gofrs/uuid/v5"
	"github.com/gorilla/websocket"
	"go.uber.org/zap"
	"google.golang.org/protobuf/encoding/protojson"
)

var (
	hmdOverridePattern = regexp.MustCompile(`^[-a-zA-Z0-9_]+$`)
	featurePattern     = regexp.MustCompile(`^[a-z0-9_]+$`)
	discordIDPattern   = regexp.MustCompile(`^[0-9]+$`)
	regionPattern      = regexp.MustCompile(`^[-A-Za-z0-9_]+$`)
	guildPattern       = regexp.MustCompile(`^[0-9]+$`)
	tagsPattern        = regexp.MustCompile(`^[-.A-Za-z0-9_:]+$`)
)

func NewSocketWSEVRAcceptor(logger *zap.Logger, config server.Config, sessionRegistry server.SessionRegistry, sessionCache server.SessionCache, statusRegistry server.StatusRegistry, matchmaker server.Matchmaker, tracker server.Tracker, metrics server.Metrics, runtime *server.Runtime, protojsonMarshaler *protojson.MarshalOptions, protojsonUnmarshaler *protojson.UnmarshalOptions, nkpipeline *server.Pipeline, pipeline *EvrPipeline) func(http.ResponseWriter, *http.Request) {
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
		// Extract the client IP address and port.
		clientIP, clientPort := extractClientAddressFromRequest(logger, r)

		// Upgrade to WebSocket.
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			// http.Error is invoked automatically from within the Upgrade function.
			logger.Debug("Could not upgrade to WebSocket", zap.Error(err))
			return
		}

		// Mark the start of the session.
		metrics.CountWebsocketOpened(1)
		defer metrics.CountWebsocketClosed(1)

		// Add the URL parameters to the context
		urlParams := make(map[string][]string, 0)
		maps.Copy(urlParams, r.URL.Query())

		vars := make(map[string]string, 0)
		// Parse the geo precision value
		vars["geo_precision"] = "8"
		if s := parseUserQueryFunc(r, "geo_precision", 2, nil); s != "" {
			v, err := strconv.Atoi(s)
			if err != nil {
				logger.Warn("Failed to parse geo precision", zap.Error(err), zap.String("geo_precision", parseUserQueryFunc(r, "geo_precision", 2, nil)))
			} else {
				if v < 0 {
					v = 0
				}
				if v > 12 {
					v = 12
				}
				vars["geo_precision"] = strconv.Itoa(v)
			}
		}

		// user_display_name_override overrides the in-game name of the user
		vars["user_display_name_override"] = parseUserQueryFunc(r, "ign", 20, nil)
		// auth_discord_id is the Discord ID of the user
		vars["auth_discord_id"] = parseUserQueryFunc(r, "discordid", 20, discordIDPattern)
		if v := parseUserQueryFunc(r, "discord_id", 20, discordIDPattern); v != "" {
			vars["auth_discord_id"] = v
		}
		// auth_password is the password of the user's nakama account
		vars["auth_password"] = parseUserQueryFunc(r, "password", 32, nil)
		// disable_encryption disables encryption for game server connections
		vars["disable_encryption"] = parseUserQueryFunc(r, "disable_encryption", 5, nil)
		// disable_mac disables message authentication codes for game server connections
		vars["disable_mac"] = parseUserQueryFunc(r, "disable_mac", 5, nil)
		// is_vpn indicates if the client is detected as using VPN
		vars["is_vpn"] = strconv.FormatBool(pipeline.ipInfoCache.IsVPN(clientIP))
		// features is a comma-delimited list of features supported by the client
		vars["features"] = strings.Join(parseUserQueryCommaDelimited(r, "features", 32, featurePattern), ",")
		// requires is a comma-delimited list of features required by the client (client only)
		vars["requires"] = strings.Join(parseUserQueryCommaDelimited(r, "requires", 32, featurePattern), ",")
		// server_addr specifies the incoming server address (ip or host) and port. (server only)
		vars["server_addr"] = parseUserQueryFunc(r, "server_addr", 64, nil)
		// tags is a comma-delimited list of tags associated with the game server (server only)
		vars["tags"] = strings.Join(parseUserQueryCommaDelimited(r, "tags", 32, tagsPattern), ",")
		// guilds is a comma-delimited list of guilds associated with the game server (server only)
		vars["guilds"] = strings.Join(parseUserQueryCommaDelimited(r, "guilds", 32, guildPattern), ",")
		// regions is a comma-delimited list of region pools to use for matchmaking (server only)
		vars["regions"] = strings.Join(parseUserQueryCommaDelimited(r, "regions", 32, regionPattern), ",")
		// verbose enables verbose logging for the connection
		vars["verbose"] = parseUserQueryFunc(r, "verbose", 5, nil)
		// debug enables debug mode for the connection
		vars["debug"] = parseUserQueryFunc(r, "debug", 5, nil)

		service := NewServiceWS(logger, clientIP, clientPort, conn, config, metrics)

		defer service.Close()

		incomingCh := service.Start()
		var in evr.Message
		var payload []byte
	InitLoop:
		for {
			var messages []evr.Message
			select {
			case <-service.ctx.Done():
				// The client has not sent any messages, so we can assume that the connection is closed.
				return
			case payload = <-incomingCh:
			}
			// Parse the first message to do the initial authentication.
			if messages, err = evr.ParsePacket(payload); err != nil {
				logger.Warn("Failed to parse incoming message", zap.Error(err), zap.Binary("data", payload))
				return
			} else if len(messages) == 0 {
				logger.Warn("Received empty message", zap.Binary("data", payload))
				return
			}
			for _, in = range messages {
				logger.Debug("Received incoming message", zap.String("type", fmt.Sprintf("%T", in)), zap.Any("message", in))
				// Handle unauthenticated (service-level messages)
				// Config service
				switch msg := in.(type) {
				case *evr.ConfigRequest:
					if err := pipeline.configRequest(service.Context(), logger, service, in); err != nil {
						logger.Warn("Failed to process request", zap.Error(err))
						return
					}
				case evr.LoginIdentifier:
					sessionID := msg.GetLoginSessionID()
					if !sessionID.IsNil() {
						// If the client has specified a login session ID, check if the session exists.
						if session, ok := sessionRegistry.Get(sessionID).(*sessionEVR); !ok || session == nil {
							// The session does not exist, so create a new session.
							break InitLoop
						} else if err = session.AddService(MessageServiceType(in), service); err != nil {
							logger.Warn("Failed to add service to session", zap.Error(err), zap.String("sid", sessionID.String()))
							return
						} else {
							// The session exists, so add the service to the session.
							logger.Debug("Session already exists, adding service", zap.String("sid", sessionID.String()), zap.String("service_type", fmt.Sprintf("%T", in)))
							// Parse the initial packets
							session.incomingCh <- payload
							// The session exists, wait until it ends, then return.
							<-session.ctx.Done()
							return
						}
					}
				default:
					// The session does not exist, so create a new session.
					if _, ok := in.(*evr.LoginRequest); !ok {
						logger.Warn("Received unexpected message while expecting login", zap.String("request_type", fmt.Sprintf("%T", msg)))
						return
					}
					break InitLoop
				}
			}
		}

		var (
			ctx                      = service.Context()
			msg                      = in.(*evr.LoginRequest)
			userID                   string
			username                 string
			isWebsocketAuthenticated bool
			profile                  *EVRProfile
		)

		authLogger := logger.With(zap.String("xp_id", msg.XPID.String()), zap.String("client_ip", clientIP), zap.String("client_port", clientPort))
		if len(urlParams) > 0 {
			authLogger = authLogger.With(zap.Any("url_params", urlParams), zap.Any("vars", vars))
		}

		sendFailure := func(err error) {
			service.SendEVR(Envelope{
				ServiceType: ServiceTypeLogin,
				Messages: []evr.Message{
					evr.NewLoginFailure(msg.XPID, formatLoginErrorMessage(msg.XPID, profile.DiscordID(), err)),
				},
				State: RequireStateUnrequired,
			})
		}

		// Validate the payload of the login message
		err = validateLoginPayload(ctx, authLogger, msg)
		if err != nil {
			sendFailure(err)
			authLogger.Warn("Login payload validation failed", zap.Error(err), zap.String("xp_id", msg.XPID.String()))
			return
		}

		if authDiscordID := vars["auth_discord_id"]; authDiscordID != "" {
			// Authenticate with Discord ID and password, linking the device if necessary.
			authPassword := vars["auth_password"]
			if authPassword == "" {
				sendFailure(newLoginError("missing_password", "missing password for Discord ID authentication"))
				authLogger.Warn("Missing password for Discord ID authentication")
				return
			}
			userID, _, _, err = AuthenticateDiscordPassword(ctx, logger, pipeline.db, config, statusRegistry, msg.XPID, authDiscordID, authPassword)
			if err != nil {
				authLogger.Debug("Discord ID authentication failed", zap.Error(err))
				sendFailure(err)
				return
			}
			isWebsocketAuthenticated = true
		} else {
			// Authenticate the XPID to get the linked user ID.
			deviceUserID, _, err := AuthenticateXPID(ctx, logger, pipeline.db, statusRegistry, msg.XPID)
			switch err {
			case ErrDeviceNotLinked:
				// Special case for unlinked devices.
				code, err := IssueLinkTicket(ctx, pipeline.nk, msg.XPID, clientIP, msg.Payload)
				if err != nil {
					authLogger.Warn("Failed to issue link ticket", zap.Error(err))
				}
				errMessage := (&DeviceNotLinkedError{
					Code:         code.Code,
					Instructions: ServiceSettings().LinkInstructions,
				}).Error()
				service.SendEVR(Envelope{
					ServiceType: ServiceTypeLogin,
					Messages: []evr.Message{
						evr.NewLoginFailure(msg.XPID, errMessage),
					},
					State: RequireStateUnrequired,
				})
				authLogger.Info("Device not linked", zap.String("link_code", code.Code))
				return
			case nil:
				// Authenticated successfully.
				userID = deviceUserID
			default:
				// Some other error occurred.
				sendFailure(err)
				authLogger.Warn("XPID authentication failed", zap.Error(err))
				return
			}
		}
		authLogger = authLogger.With(zap.String("uid", userID))
		// Load the user profile.
		profile, err = EVRProfileLoad(ctx, pipeline.nk, userID)
		if err != nil {
			sendFailure(err)
			authLogger.Warn("Failed to load user profile", zap.Error(err), zap.String("user_id", userID))
			return
		}
		username = profile.Username()
		authLogger = authLogger.With(zap.String("username", username))

		authLogger.Info("User authenticated")

		// Get the IPQS Data
		var ipInfo IPInfo
		if pipeline.ipInfoCache != nil {
			if ipInfo, err = pipeline.ipInfoCache.Get(ctx, clientIP); err != nil {
				logger.Debug("Failed to get IPQS details", zap.Error(err))
			}
		}

		var (
			disableLoginMessage = ServiceSettings().DisableLoginMessage
			reportURL           = ServiceSettings().ReportURL
		)
		if err = pipeline.authorizeSession(ctx, authLogger, msg.XPID, &msg.Payload, profile, ipInfo, clientIP, disableLoginMessage, reportURL, isWebsocketAuthenticated); err != nil {
			sendFailure(err)
			authLogger.Info("Authorization failed", zap.Error(err))
			return
		}

		sessionID := uuid.Must(sessionIdGen.NewV1())

		userUUID := uuid.FromStringOrNil(userID)
		session := NewSessionWSEVR(logger, config, sessionID, userUUID, username, msg.XPID, &msg.Payload, vars, clientIP, clientPort, ipInfo, protojsonMarshaler, protojsonUnmarshaler, service, sessionRegistry, statusRegistry, matchmaker, tracker, metrics, runtime, pipeline)
		session.AddService(ServiceTypeLogin, service)
		// Add to the session registry.
		sessionRegistry.Add(server.Session(session))

		// Register initial status tracking and presence(s) for this session.
		statusRegistry.Follow(sessionID, map[uuid.UUID]struct{}{session.userID: {}})

		// Both notification and status presence.

		tracker.TrackMulti(session.Context(), session.id, []*server.TrackerOp{
			// EVR packet data stream for the login session by user ID, and service ID, with EVR ID
			{
				Stream: server.PresenceStream{Mode: StreamModeService, Subject: session.userID, Label: StreamLabelLoginService},
				Meta:   server.PresenceMeta{Format: session.Format(), Username: session.Username(), Hidden: false},
			},
			// EVR packet data stream for the login session by session ID and service ID, with EVR ID
			{
				Stream: server.PresenceStream{Mode: StreamModeService, Subject: session.id, Label: StreamLabelLoginService},
				Meta:   server.PresenceMeta{Format: session.Format(), Username: session.Username(), Hidden: false},
			},
			// Notification presence.
			{
				Stream: server.PresenceStream{Mode: server.StreamModeNotifications, Subject: session.userID},
				Meta:   server.PresenceMeta{Format: session.Format(), Username: session.Username(), Hidden: false},
			},

			// Status presence.
			{
				Stream: server.PresenceStream{Mode: server.StreamModeStatus, Subject: session.userID},
				Meta:   server.PresenceMeta{Format: session.Format(), Username: session.Username(), Status: ""},
			},
		}, session.userID)
		session.incomingCh <- payload

		// Allow the server to begin processing incoming messages from this session.
		session.Consume()

	}
}

// parseUserQueryFunc extracts and sanitizes a single string URL parameter from the request.
func parseUserQueryFunc(request *http.Request, key string, maxLength int, pattern *regexp.Regexp) string {
	v := request.URL.Query().Get(key)
	if v != "" {
		if len(v) > maxLength {
			v = v[:maxLength]
		}
		if pattern != nil && !pattern.MatchString(v) {
			return ""
		}
	}
	return v
}

// parseUserQueryCommaDelimited extracts and sanitizes a comma-delimited list of string URL parameters from the request.
func parseUserQueryCommaDelimited(request *http.Request, key string, maxLength int, pattern *regexp.Regexp) []string {
	// Add the items list to the urlparam, sanitizing it
	items := make([]string, 0)
	if v := request.URL.Query().Get(key); v != "" {
		s := strings.Split(v, ",")
		for _, f := range s {
			if f == "" {
				continue
			}
			if len(f) > maxLength {
				f = f[:maxLength]
			}
			if pattern != nil && !pattern.MatchString(f) {
				continue
			}
			items = append(items, f)
		}
		if len(items) == 0 {
			return nil
		}
	}
	slices.Sort(items)
	return items
}

// NodeToHash generates a 6-byte hash from the node name.
func NodeToHash(node string) [6]byte {
	hash := sha1.Sum([]byte(node))
	var hashArr [6]byte
	copy(hashArr[:], hash[:6])
	return hashArr
}

// HashFromId generates a 6-byte hash from the last 6 bytes of a UUID.
func HashFromId(id uuid.UUID) [6]byte {
	var idArr [6]byte
	copy(idArr[:], id[10:])
	return idArr
}

func validateLoginPayload(ctx context.Context, logger *zap.Logger, request *evr.LoginRequest) error {
	// Validate the XPID
	if !request.XPID.IsValid() {
		return newLoginError("invalid_xpid", "invalid XPID: %s", request.XPID.String())
	}
	// Validate the HMD Serial Number
	if sn := request.Payload.HMDSerialNumber; strings.Contains(sn, ":") {
		return newLoginError("invalid_sn", "Invalid HMD Serial Number: %s", sn)
	}
	// Check the build number
	if request.Payload.BuildNumber != 0 && !slices.Contains(evr.KnownBuilds, request.Payload.BuildNumber) {
		logger.Warn("Unknown build version", zap.Int64("build", int64(request.Payload.BuildNumber)))
	}
	return nil
}
