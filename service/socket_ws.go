package service

import (
	"crypto/sha1"
	"fmt"
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

		clientIP, clientPort := extractClientAddressFromRequest(logger, r)

		// Upgrade to WebSocket.
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			// http.Error is invoked automatically from within the Upgrade function.
			logger.Debug("Could not upgrade to WebSocket", zap.Error(err))
			return
		}

		// TODO Add a preliminary EVR consumer to handle the first "authenticating" messages from the client and then
		// hands off the authenticated session to the main consumer.

		// Add the URL parameters to the context
		urlParams := make(map[string][]string, 0)
		for k, v := range r.URL.Query() {
			urlParams[k] = v
		}

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

		vars["user_display_name_override"] = parseUserQueryFunc(r, "ign", 20, nil)
		vars["auth_discord_id"] = parseUserQueryFunc(r, "discordid", 20, discordIDPattern)
		if v := parseUserQueryFunc(r, "discord_id", 20, discordIDPattern); v != "" {
			vars["auth_discord_id"] = v
		}
		vars["auth_password"] = parseUserQueryFunc(r, "password", 32, nil)
		vars["disable_encryption"] = parseUserQueryFunc(r, "disable_encryption", 5, nil)
		vars["disable_mac"] = parseUserQueryFunc(r, "disable_mac", 5, nil)
		vars["server_addr"] = parseUserQueryFunc(r, "server_addr", 64, nil) // Specifies the incoming server address (ip or host) and port.
		vars["is_vpn"] = strconv.FormatBool(pipeline.ipInfoCache.IsVPN(clientIP))
		vars["features"] = strings.Join(parseUserQueryCommaDelimited(r, "features", 32, featurePattern), ",")
		vars["requires"] = strings.Join(parseUserQueryCommaDelimited(r, "requires", 32, featurePattern), ",")
		vars["tags"] = strings.Join(parseUserQueryCommaDelimited(r, "tags", 32, tagsPattern), ",")
		vars["guilds"] = strings.Join(parseUserQueryCommaDelimited(r, "guilds", 32, guildPattern), ",")
		vars["regions"] = strings.Join(parseUserQueryCommaDelimited(r, "regions", 32, regionPattern), ",")
		vars["verbose"] = parseUserQueryFunc(r, "verbose", 5, nil)
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
				logger.Debug("Parsed incoming message", zap.String("type", fmt.Sprintf("%T", in)), zap.Any("message", in))
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
		msg := in.(*evr.LoginRequest)

		profile, err := authSocket(service.ctx, logger, pipeline, vars, msg, clientIP)
		if err != nil {
			errMessage := formatLoginErrorMessage(msg.XPID, profile.DiscordID(), err)
			if data, err := evr.Marshal(evr.NewLoginFailure(msg.XPID, errMessage)); err != nil {
				logger.Warn("Failed to marshal login failure message", zap.Error(err))
			} else if err := service.SendBytes(data, true); err != nil {
				logger.Warn("Failed to send login failure message", zap.Error(err))
			}
			return
		}
		userID := uuid.FromStringOrNil(profile.UserID())
		username := profile.Username()

		// Get the IPQS Data
		ipInfo, err := pipeline.ipInfoCache.Get(service.ctx, clientIP)
		if err != nil {
			logger.Debug("Failed to get IPQS details", zap.Error(err))
		}

		sessionID := uuid.Must(sessionIdGen.NewV1())

		// Mark the start of the session.
		metrics.CountWebsocketOpened(1)
		defer metrics.CountWebsocketClosed(1)

		session := NewSessionWSEVR(logger, config, sessionID, userID, username, profile, msg.XPID, &msg.Payload, vars, clientIP, clientPort, ipInfo, protojsonMarshaler, protojsonUnmarshaler, service, sessionRegistry, statusRegistry, matchmaker, tracker, metrics, runtime, pipeline)
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
