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
	"go.uber.org/atomic"
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

func NewSocketWSEVRAcceptor(logger *zap.Logger, config server.Config, sessionRegistry server.SessionRegistry, sessionCache server.SessionCache, statusRegistry server.StatusRegistry, matchmakerRef *atomic.Pointer[server.LocalMatchmaker], tracker server.Tracker, metrics server.Metrics, protojsonMarshaler *protojson.MarshalOptions, protojsonUnmarshaler *protojson.UnmarshalOptions, pipeline *Pipeline) func(http.ResponseWriter, *http.Request) {
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

		// Check if the system is temporarily unavailable.
		if msg := ServiceSettings().DisableLoginMessage; msg != "" {
			// Send 403 Forbidden and close the connection.
			conn.WriteMessage(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.ClosePolicyViolation, "System is Temporarily Unavailable:\n"+msg))
			conn.Close()
		}

		// Add the URL parameters to the context
		urlParams := make(map[string][]string, 0)
		maps.Copy(urlParams, r.URL.Query())

		vars := make(map[string]string, 0)
		// Parse the geo precision value
		geoPrecision := 8
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
				geoPrecision = v
			}
		}

		// user_display_name_override overrides the in-game name of the user
		vars["user_display_name_override"] = parseUserQueryFunc(r, "ign", 20, nil)

		params := SessionParameters{
			node: config.GetName(),

			externalServerAddr:   parseUserQueryFunc(r, "server_addr", 64, nil),
			geoHashPrecision:     geoPrecision,
			isVPN:                pipeline.ipInfoCache.IsVPN(clientIP),
			supportedFeatures:    parseUserQueryCommaDelimited(r, "features", 32, featurePattern),
			requiredFeatures:     parseUserQueryCommaDelimited(r, "requires", 32, featurePattern),
			serverTags:           parseUserQueryCommaDelimited(r, "tags", 32, tagsPattern),
			serverGuilds:         parseUserQueryCommaDelimited(r, "guilds", 32, guildPattern),
			serverRegions:        parseUserQueryCommaDelimited(r, "regions", 32, regionPattern),
			relayOutgoing:        parseUserQueryFunc(r, "verbose", 5, nil) == "true",
			enableAllRemoteLogs:  parseUserQueryFunc(r, "debug", 5, nil) == "true",
			lastMatchmakingError: atomic.NewError(nil),
			guildGroups:          make(map[string]*GuildGroup),
			isIGPOpen:            atomic.NewBool(false),
			earlyQuitConfig:      atomic.NewPointer[EarlyQuitConfig](nil),
			isGoldNameTag:        atomic.NewBool(false),
			latencyHistory:       atomic.NewPointer[LatencyHistory](nil),
		}

		// remove any empty values from vars
		for k, v := range vars {
			if v == "" {
				delete(vars, k)
			}
		}
		authLogger := logger.With(zap.String("client_ip", clientIP), zap.String("client_port", clientPort))
		if len(urlParams) > 0 {
			authLogger = authLogger.With(zap.Any("url_params", urlParams), zap.Any("vars", vars))
		}

		var (
			service         = NewServiceWS(logger, clientIP, clientPort, conn, config, metrics)
			incomingCh      = service.Start()
			in              evr.Message
			payload         []byte
			failureResponse func(err error)
		)

		defer service.Close()

	InitLoop:
		for {
			var messages []evr.Message
			select {
			case <-service.ctx.Done():
				// The client has not sent any messages, so assume the connection is closed.
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

				case *evr.ReconcileIAP:
					// Return a stub response for now
					if err := service.SendEVR(Envelope{
						ServiceType: ServiceTypeIAP,
						Messages:    []evr.Message{evr.NewReconcileIAPResult(msg.XPID)},
					}); err != nil {
						logger.Warn("Failed to process request", zap.Error(err))
						return
					}

				case evr.LoginIdentifier:
					// Associate with an existing session.
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
				case *evr.GameServerRegistrationRequest:

					failureResponse = func(err error) {
						if err := service.SendEVR(Envelope{
							ServiceType: ServiceTypeServer,
							Messages:    []evr.Message{evr.NewBroadcasterRegistrationFailure(evr.BroadcasterRegistration_Failure)},
							State:       RequireStateUnrequired,
						}); err != nil {
							logger.Warn("Failed to send lobby registration failure", zap.Error(err))
						}
					}
					// Handle legacy game server registration requests (that do not associate themselves with a login connection)
					authDiscordID, authPassword := extractDiscordUserCredentials(r)
					if authDiscordID != "" {
						if authPassword == "" {
							// Password is required for Discord authentication.
							failureResponse(ErrMissingPassword)
							return
						}
						_, username, err := GetUserIDByDiscordID(service.Context(), pipeline.db, authDiscordID)
						if err != nil {
							// Some other error occurred while trying to get the user ID.
							failureResponse(err)
							return
						}

						// Authenticate the user with password.
						userID, err := server.AuthenticateUsername(service.Context(), logger, pipeline.db, username, authPassword)
						if err != nil {
							logger.Warn("Username/password authentication failed.", zap.Error(err), zap.String("discord_id", authDiscordID))
							failureResponse(err)
							return
						}
						params.profile, err = EVRProfileLoad(service.Context(), pipeline.nk, userID)
						if err != nil {
							logger.Warn("Failed to load user profile", zap.Error(err), zap.String("user_id", userID))
							failureResponse(err)
							return
						}
					}
					if params.profile == nil {
						// Must be authenticated to register a game server.
						failureResponse(ErrAuthenticateFailed)
						return
					}
					// The session does not exist, so create a new session.
					break InitLoop
				case *evr.LoginRequest:

					params.xpID = msg.XPID
					params.loginPayload = &msg.Payload
					ctx := service.Context()
					// Handle login requests
					if err = validateLoginPayload(service.Context(), logger, msg); err != nil {
						service.SendEVR(Envelope{
							ServiceType: ServiceTypeLogin,
							Messages: []evr.Message{
								evr.NewLoginFailure(msg.XPID, formatLoginErrorMessage(msg.XPID, "", err)),
							},
							State: RequireStateUnrequired,
						})
						logger.Warn("Login payload validation failed", zap.Error(err), zap.String("xp_id", msg.XPID.String()))
						return
					}

					authLogger = authLogger.With(zap.String("xp_id", msg.XPID.String()))
					failureResponse = func(err error) {
						service.SendEVR(Envelope{
							ServiceType: ServiceTypeLogin,
							Messages: []evr.Message{
								evr.NewLoginFailure(msg.XPID, formatLoginErrorMessage(msg.XPID, "", err)),
							},
							State: RequireStateUnrequired,
						})
					}
					authDiscordID, authPassword := extractDiscordUserCredentials(r)

					params.profile, err = pipeline.authenticateSession(ctx, logger, pipeline.db, config, statusRegistry, msg.XPID, clientIP, &msg.Payload, authDiscordID, authPassword)
					switch err {
					case nil:
						// Authenticated successfully.
						SendEvent(ctx, pipeline.nk, &EventUserAuthenticated{
							UserID:                   params.profile.ID(),
							XPID:                     msg.XPID,
							ClientIP:                 clientIP,
							LoginPayload:             &msg.Payload,
							IsWebSocketAuthenticated: params.profile.HasPasswordSet(),
						})
						break InitLoop
					case ErrDeviceNotLinked:
						if params.profile != nil && params.profile.HasPasswordSet() {
							// Device is not linked to an account. Link it to the one that was authenticated via Discord ID and password.
							if err := server.LinkDevice(ctx, logger, pipeline.db, uuid.FromStringOrNil(params.profile.ID()), msg.XPID.String()); err != nil {
								failureResponse(err)
								authLogger.Warn("Failed to link device to account", zap.Error(err))
								return
							}
						}

						// Special case for unlinked devices.
						ticket, err := IssueLinkTicket(service.Context(), pipeline.nk, msg.XPID, clientIP, msg.Payload)
						if err != nil {
							authLogger.Warn("Failed to issue link ticket", zap.Error(err))
						}
						errMessage := (&DeviceNotLinkedError{
							Code:         ticket.Code,
							Instructions: ServiceSettings().LinkInstructions,
						}).Error()
						service.SendEVR(Envelope{
							ServiceType: ServiceTypeLogin,
							Messages: []evr.Message{
								evr.NewLoginFailure(msg.XPID, errMessage),
							},
							State: RequireStateUnrequired,
						})
						authLogger.Info("Device not linked", zap.String("link_code", ticket.Code))
						return
					default:
						// Some other error occurred.
						failureResponse(err)
						authLogger.Warn("XPID authentication failed", zap.Error(err))
						return
					}

				default:
					// The session does not exist, so create a new session.
					logger.Warn("Received unexpected message while expecting login", zap.String("request_type", fmt.Sprintf("%T", msg)))
					return

				}
			}
		}

		var (
			ctx    = service.Context()
			userID = params.profile.ID()
		)

		authLogger = authLogger.With(zap.String("uid", userID))
		authLogger = authLogger.With(zap.String("username", params.profile.Username()))

		authLogger.Info("User authenticated")

		// Get the IPQS Data

		if pipeline.ipInfoCache != nil {
			if params.ipInfo, err = pipeline.ipInfoCache.Get(ctx, clientIP); err != nil {
				logger.Debug("Failed to get IPQS details", zap.Error(err))
			}
		}

		sessionID := uuid.Must(sessionIdGen.NewV1())

		if err := pipeline.initializeSessionParameters(ctx, authLogger, userID, &params); err != nil {
			failureResponse(err)
			authLogger.Warn("Failed to initialize session", zap.Error(err))
			return
		}

		matchmaker := matchmakerRef.Load()
		if matchmaker == nil {
			authLogger.Warn("Matchmaker not available")
			failureResponse(ErrSystemTemporarilyUnavailable)
		}

		userUUID := uuid.FromStringOrNil(userID)
		session := NewSessionWSEVR(logger, config, sessionID, userUUID, params, vars, clientIP, clientPort, protojsonMarshaler, protojsonUnmarshaler, service, sessionRegistry, statusRegistry, matchmaker, tracker, metrics, pipeline)
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

func extractDiscordUserCredentials(r *http.Request) (string, string) {
	// Authenticate the websocket connection out-of-band, before creating a session.
	authDiscordID := parseUserQueryFunc(r, "discordid", 20, discordIDPattern)
	if v := parseUserQueryFunc(r, "discord_id", 20, discordIDPattern); v != "" {
		authDiscordID = v
	}
	// auth_password is the password of the user's nakama account
	authPassword := parseUserQueryFunc(r, "password", 32, nil)
	return authDiscordID, authPassword
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
