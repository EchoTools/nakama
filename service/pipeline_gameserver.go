package service

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"maps"
	"net"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"fmt"

	"github.com/bwmarrin/discordgo"
	evr "github.com/echotools/nakama/v3/protocol"
	"github.com/echotools/nevr-common/gen/go/rtapi"
	"github.com/gofrs/uuid/v5"
	"github.com/heroiclabs/nakama-common/runtime"
	"github.com/heroiclabs/nakama/v3/server"
	"go.uber.org/zap"
)

const MINIMUM_NATIVE_SERVER_VERSION = "2.0.0"

var (
	ErrGameServerPresenceNotFound = errors.New("game server presence not found")
)

// sendDiscordError sends an error message to the user on discord
func sendDiscordError(e error, discordId string, logger *zap.Logger, bot *discordgo.Session) {
	// Message the user on discord

	if bot != nil && discordId != "" {
		channel, err := bot.UserChannelCreate(discordId)
		if err != nil {
			logger.Warn("Failed to create user channel", zap.Error(err))
			return
		}
		_, err = bot.ChannelMessageSend(channel.ID, fmt.Sprintf("Failed to register game server: %v", e))
		if err != nil {
			logger.Warn("Failed to send message to user", zap.Error(err))
			return
		}
	}
}

// errFailedRegistration sends a failure message to the broadcaster and closes the session
func errFailedRegistration(session *sessionEVR, logger *zap.Logger, err error, code evr.BroadcasterRegistrationFailureCode) error {
	logger = logger.WithOptions(zap.AddCallerSkip(1))
	logger.Warn("Failed to register game server", zap.Error(err))
	if err := session.SendEVR(Envelope{
		ServiceType: ServiceTypeServer,
		Messages:    []evr.Message{evr.NewBroadcasterRegistrationFailure(code)},
	}); err != nil {
		return fmt.Errorf("failed to send lobby registration failure: %w", err)
	}
	session.Close(err.Error(), runtime.PresenceReasonDisconnect)
	return fmt.Errorf("failed to register game server: %w", err)
}

// gameServerRegistrationParams holds all extracted and validated parameters
type gameServerRegistrationParams struct {
	loginSessionID uuid.UUID
	serverID       uint64
	regionHash     evr.Symbol
	internalIP     net.IP
	externalIP     net.IP
	externalPort   uint16
	versionLock    evr.Symbol
	isNative       bool
	version        string
	timeStepUsecs  uint32
	sessionParams  SessionParameters
	ipInfo         IPInfo
}

// extractAndValidateParams extracts and validates all parameters from the request and session
func extractAndValidateParams(request *rtapi.GameServerRegistrationMessage, s *sessionEVR) (*gameServerRegistrationParams, error) {
	if request == nil {
		return nil, fmt.Errorf("request is nil")
	}

	params := &gameServerRegistrationParams{
		loginSessionID: uuid.FromStringOrNil(request.LoginSessionId),
		serverID:       request.ServerId,
		regionHash:     evr.Symbol(request.Region),
		internalIP:     net.ParseIP(request.InternalIpAddress),
		externalIP:     net.ParseIP(s.ClientIP()),
		externalPort:   uint16(request.Port),
		versionLock:    evr.Symbol(request.VersionLock),
		version:        request.Version,
		timeStepUsecs:  request.TimeStepUsecs,
	}

	// Check if this is a native server
	params.isNative = params.loginSessionID != uuid.Nil

	// Validate native server version
	if params.isNative && params.version < MINIMUM_NATIVE_SERVER_VERSION {
		return nil, fmt.Errorf("game server version is below minimum required version: %s < %s", params.version, MINIMUM_NATIVE_SERVER_VERSION)
	}

	// Extract session parameters
	ctx := s.Context()
	sessionParams, ok := LoadParams(ctx)
	if !ok {
		return nil, fmt.Errorf("session parameters not provided")
	}
	params.sessionParams = sessionParams

	return params, nil
}

// validateSessionAndUser validates the session and user authentication
func (p *Pipeline) validateSessionAndUser(s *sessionEVR, params *gameServerRegistrationParams, logger *zap.Logger) error {
	if s.userID.IsNil() {
		return fmt.Errorf("game server is not authenticated")
	}

	s.tracker.TrackMulti(s.Context(), s.id, []*server.TrackerOp{
		// EVR packet data stream for the login session by Session ID and broadcaster ID
		{
			Stream: server.PresenceStream{Mode: StreamModeService, Subject: s.userID},
			Meta:   server.PresenceMeta{Format: s.Format(), Username: s.username.String(), Hidden: false},
		},
		// EVR packet data stream by session ID and broadcaster ID
		{
			Stream: server.PresenceStream{Mode: StreamModeService, Subject: s.id},
			Meta:   server.PresenceMeta{Format: s.Format(), Username: s.username.String(), Hidden: false},
		},
	}, s.userID)

	return nil
}

// getHostingGroupIDs filters guild groups to determine which ones this server can host for
func getHostingGroupIDs(userID string, guildGroups map[string]*GuildGroup, sessionParams SessionParameters) ([]uuid.UUID, error) {
	if len(guildGroups) == 0 {
		return nil, fmt.Errorf("user is not in any guild groups")
	}

	// Include all guilds by default, or if "any" is in the list
	includeAll := len(sessionParams.serverGuilds) == 0 || slices.Contains(sessionParams.serverGuilds, "any")

	// Filter the guild groups to host for
	hostingGroupIDs := make([]uuid.UUID, 0)
	for _, gg := range guildGroups {
		// Must be a server host and not suspended
		if !gg.IsServerHost(userID) || gg.IsSuspended(userID, nil) {
			continue
		}

		// Include the group if it is in the list of server guilds, includes "any" or is empty
		if includeAll || slices.Contains(sessionParams.serverGuilds, gg.GuildID) {
			hostingGroupIDs = append(hostingGroupIDs, gg.ID())
		}
	}

	return hostingGroupIDs, nil
}

// buildRegionCodes constructs the region codes for server discovery
func buildRegionCodes(regionHash evr.Symbol, sessionParams SessionParameters, ipInfo IPInfo) []string {
	regionCodes := make([]string, 0, len(sessionParams.serverRegions))

	if len(sessionParams.serverRegions) == 0 {
		regionCodes = append(regionCodes, "default")
	}

	// Add the server regions specified in the config.json
	regionCodes = append(regionCodes, sessionParams.serverRegions...)

	switch regionHash {
	// If the region is unspecified, use the default region, otherwise use the region hash passed on the command line
	case evr.UnspecifiedRegion, 0:
		regionCodes = append(regionCodes, "default")
	default:
		regionCodes = append(regionCodes, regionHash.String())
	}

	// Limit to first 10 regions
	if len(regionCodes) > 10 {
		regionCodes = regionCodes[:10]
	}

	// Truncate all region codes to 32 characters
	for i, r := range regionCodes {
		slices.Sort(regionCodes)
		if len(r) > 32 {
			regionCodes[i] = r[:32]
		}
	}

	// Add IP-based region codes if available
	if slices.Contains(regionCodes, "default") {
		regionCodes = append(regionCodes,
			LocationToRegionCode(ipInfo.CountryCode(), ipInfo.Region(), ipInfo.City()),
			LocationToRegionCode(ipInfo.CountryCode(), ipInfo.Region(), ""),
			LocationToRegionCode(ipInfo.CountryCode(), "", ""),
		)
	}

	return regionCodes
}

// resolveExternalAddress handles external IP and port resolution including DNS lookup
func resolveExternalAddress(params *gameServerRegistrationParams) (net.IP, uint16, error) {
	externalIP := params.externalIP
	externalPort := params.externalPort

	// If the external IP address is specified in the config.json, use it instead of the system's external IP
	if params.sessionParams.externalServerAddr != "" {
		parts := strings.Split(params.sessionParams.externalServerAddr, ":")
		if len(parts) != 2 {
			return nil, 0, fmt.Errorf("invalid external IP address: %s. it must be `ip:port`", params.sessionParams.externalServerAddr)
		}

		// If the external IP address is not a valid IP address, look it up as a hostname
		if externalIP = net.ParseIP(parts[0]); externalIP == nil {
			if ips, err := net.LookupIP(parts[0]); err != nil {
				return nil, 0, fmt.Errorf("invalid address `%s`: %w", parts[0], err)
			} else {
				externalIP = ips[0]
			}
		}

		// Parse the external port
		if p, err := strconv.Atoi(parts[1]); err != nil {
			return nil, 0, fmt.Errorf("invalid external port: %s", parts[1])
		} else {
			externalPort = uint16(p)
		}
	}

	return externalIP, externalPort, nil
}

// checkBroadcasterAlive validates connectivity to the game server with retries
func (p *Pipeline) checkBroadcasterAlive(config *GameServerPresence, retries int) (bool, error) {
	// Give the server some time to start up
	time.Sleep(2 * time.Second)

	var rtt time.Duration
	var err error

	for i := 0; i < retries; i++ {
		// TODO: This shouldn't be locked to internal IP
		rtt, err = BroadcasterHealthcheck(p.internalIP, config.Endpoint.ExternalIP, int(config.Endpoint.Port), 500*time.Millisecond)
		if err != nil {
			// Try the internal IP
			rtt, err = BroadcasterHealthcheck(p.internalIP, config.Endpoint.InternalIP, int(config.Endpoint.Port), 500*time.Millisecond)
			if err != nil {
				time.Sleep(500 * time.Millisecond)
				continue
			}
		}
		if rtt >= 0 {
			return true, nil
		}
	}

	return false, fmt.Errorf("broadcaster could not be reached after %d retries: %w", retries, err)
}

// handleRegistrationError handles error logging, Discord notifications, and error responses
func (p *Pipeline) handleRegistrationError(params *gameServerRegistrationParams, session *sessionEVR, logger *zap.Logger, err error, code evr.BroadcasterRegistrationFailureCode) error {
	if params != nil {
		errorMessage := fmt.Sprintf("Broadcaster (Server ID: %d) registration failed. Error: %v", params.serverID, err)
		go sendDiscordError(errors.New(errorMessage), params.sessionParams.DiscordID(), logger, p.discordCache.dg)
	}
	return errFailedRegistration(session, logger, err, code)
}

// updateLoggerContext enhances the logger with consistent context information
func updateLoggerContext(logger *zap.Logger, params *gameServerRegistrationParams, hostingGroupIDs []uuid.UUID, regionCodes []string, externalIP net.IP, externalPort uint16) *zap.Logger {
	if params == nil {
		return logger
	}

	return logger.With(
		zap.String("uid", params.sessionParams.UserID()),
		zap.String("discord_id", params.sessionParams.DiscordID()),
		zap.Any("group_ids", hostingGroupIDs),
		zap.Strings("tags", params.sessionParams.serverTags),
		zap.Strings("regions", regionCodes),
		zap.String("internal_ip", params.internalIP.String()),
		zap.String("external_ip", externalIP.String()),
		zap.Uint16("port", externalPort),
	)
}

// startGameServerMonitoring launches the background monitoring goroutine for the game server
func (p *Pipeline) startGameServerMonitoring(session *sessionEVR, config *GameServerPresence, params *gameServerRegistrationParams, logger *zap.Logger) {
	go func() {
		// Create the initial parking match for the game server.
		if _, err := p.newGameServerParkingMatch(server.NewRuntimeGoLogger(logger), p.nk, config); err != nil {
			errFailedRegistration(session, logger, err, evr.BroadcasterRegistration_Failure)
			session.Close("Game server match lookup error", runtime.PresenceReasonDisconnect)
			return
		}
		// Start monitoring the game server presence.
		for {
			select {
			case <-session.Context().Done():
				logger.Info("Game server session closed, stopping monitoring")
				return
			case <-time.After(5 * time.Second):
				// Check if the game server is still alive
				rtts, err := BroadcasterRTTcheck(config.Endpoint.ExternalIP, int(config.Endpoint.Port), 5, 500*time.Millisecond)
				if err != nil || len(rtts) == 0 {
					logger.Warn("Game server is not responding", zap.Error(err), zap.String("endpoint", config.Endpoint.String()))
					// Send the discord error
					errorMessage := fmt.Sprintf("Game server (Endpoint ID: %s, Server ID: %d) is not responding. Error: %v", config.Endpoint.ExternalAddress(), config.ServerID, err)
					go sendDiscordError(errors.New(errorMessage), params.sessionParams.DiscordID(), logger, p.discordCache.dg)
				}
			}
			// If the game server is alive, check if it is still in a match
			if matchID, _, err := GameServerBySessionID(p.nk, session.ID()); err != nil {
				if errors.Is(err, ErrGameServerPresenceNotFound) {
					logger.Debug("Game server presence not found")
					session.Close("Game server presence not found", runtime.PresenceReasonDisconnect)
					return
				}
			} else {
				// Check if the match is still active
				if match, err := p.nk.MatchGet(session.Context(), matchID.String()); err != nil {
					// Close the session if the match lookup failed
					logger.Warn("Game server match lookup error.", zap.Error(err), zap.String("match_id", matchID.String()), zap.String("session_id", session.ID().String()))
					session.Close("Game server match lookup error", runtime.PresenceReasonDisconnect)
				} else if match == nil {
					logger.Debug("Game server match not found, creating a new parking match")
					// Create a new parking match
					if _, err = p.newGameServerParkingMatch(server.NewRuntimeGoLogger(logger), p.nk, config); err != nil {
						session.Close("Game server match lookup error", runtime.PresenceReasonDisconnect)
						return
					}
				} else {
					// If the match is still active, continue monitoring
					continue
				}
			}
		}
	}()
}

// gameserverRegistrationRequest handles the game server registration request from the game server.
//
// This function utilizes various URL parameters that are parsed during WebSocket session initialization.
// For comprehensive documentation of all URL parameters, see: docs/game-server-url-parameters.md
//
// Key URL parameters used in this function:
//   - discordid/discord_id: Discord user ID for authentication
//   - password: Authentication password
//   - serveraddr: External server address (IP:port format)
//   - tags: Comma-delimited server tags
//   - guilds: Comma-delimited Discord guild IDs
//   - regions: Comma-delimited geographic regions
//   - features: Comma-delimited supported features
//   - geo_precision: Geohash precision (0-12)
//   - debug: Enable debug mode
//   - verbose: Enable verbose logging
func (p *Pipeline) gameserverRegistrationRequest(logger *zap.Logger, s *sessionEVR, in *rtapi.Envelope) error {
	request := in.GetGameServerRegistration()

	// Extract and validate all parameters
	params, err := extractAndValidateParams(request, s)
	if err != nil {
		return p.handleRegistrationError(params, s, logger, err, evr.BroadcasterRegistration_Unknown)
	}

	// Log the registration request for native servers
	if params.isNative {
		logger.Info("Game server registration request",
			zap.String("login_session_id", params.loginSessionID.String()),
			zap.Uint64("server_id", params.serverID),
			zap.String("internal_ip", params.internalIP.String()),
			zap.Uint16("port", params.externalPort),
			zap.String("region", params.regionHash.String()),
			zap.String("version_lock", params.versionLock.String()),
			zap.Uint32("time_step_usecs", params.timeStepUsecs),
			zap.String("version", params.version))
	}

	// Validate session and user authentication
	if err := p.validateSessionAndUser(s, params, logger); err != nil {
		return p.handleRegistrationError(params, s, logger, err, evr.BroadcasterRegistration_Unknown)
	}

	// Get user guild groups
	guildGroups, err := GuildUserGroupsList(s.Context(), p.nk, p.guildGroupRegistry, s.userID.String())
	if err != nil {
		return p.handleRegistrationError(params, s, logger, fmt.Errorf("failed to get guild groups: %w", err), evr.BroadcasterRegistration_Unknown)
	}

	// Update session parameters with guild groups
	params.sessionParams.guildGroups = guildGroups
	StoreParams(s.Context(), &params.sessionParams)

	// Determine which guild groups this server can host for
	hostingGroupIDs, err := getHostingGroupIDs(s.userID.String(), guildGroups, params.sessionParams)
	if err != nil {
		return p.handleRegistrationError(params, s, logger, err, evr.BroadcasterRegistration_Unknown)
	}

	// Resolve external IP and port
	externalIP, externalPort, err := resolveExternalAddress(params)
	if err != nil {
		return p.handleRegistrationError(params, s, logger, err, evr.BroadcasterRegistration_Unknown)
	}

	// Get IP information for region codes
	if p.ipInfoCache != nil {
		params.ipInfo, err = p.ipInfoCache.Get(s.Context(), externalIP.String())
		if err != nil {
			logger.Warn("Failed to get IPQS data", zap.Error(err))
		}
	}

	// Build region codes for server discovery
	regionCodes := buildRegionCodes(params.regionHash, params.sessionParams, params.ipInfo)

	// Add the server ID as a region code for direct connection
	regionCodes = append(regionCodes, fmt.Sprintf("0x%x", params.serverID))

	// Update logger with comprehensive context
	logger = updateLoggerContext(logger, params, hostingGroupIDs, regionCodes, externalIP, externalPort)

	// Create the game server presence configuration
	config := NewGameServerPresence(
		s.UserID(),
		s.id,
		params.serverID,
		params.internalIP,
		externalIP,
		externalPort,
		hostingGroupIDs,
		regionCodes,
		params.versionLock,
		params.sessionParams.serverTags,
		params.sessionParams.supportedFeatures,
		params.timeStepUsecs,
		params.ipInfo,
		params.sessionParams.geoHashPrecision,
		params.isNative,
		params.version)

	// Check if the broadcaster is alive and reachable
	isAlive, err := p.checkBroadcasterAlive(config, 5)
	if !isAlive {
		logger.Error("Broadcaster could not be reached", zap.Error(err))
		errorMessage := fmt.Sprintf("Broadcaster (Endpoint ID: %s, Server ID: %d) could not be reached. Error: %v", config.Endpoint.ExternalAddress(), config.ServerID, err)
		go sendDiscordError(errors.New(errorMessage), params.sessionParams.DiscordID(), logger, p.discordCache.dg)
		return p.handleRegistrationError(params, s, logger, errors.New(errorMessage), evr.BroadcasterRegistration_Failure)
	}

	status := config.GetStatus()

	// Join the monitoring stream
	if err := p.nk.StreamUserUpdate(StreamModeGameServer, s.ID().String(), "", "", config.GetUserId(), config.GetSessionId(), false, false, status); err != nil {
		logger.Warn("Failed to update game server stream", zap.Error(err))
		return p.handleRegistrationError(params, s, logger, err, evr.BroadcasterRegistration_Failure)
	}

	// Join the game server to the game server pool streams
	for _, gID := range config.GroupIDs {
		if err := p.nk.StreamUserUpdate(StreamModeGameServer, gID.String(), "", "", config.GetUserId(), config.GetSessionId(), false, false, status); err != nil {
			logger.Warn("Failed to update game server stream", zap.Error(err))
			return p.handleRegistrationError(params, s, logger, err, evr.BroadcasterRegistration_Failure)
		}
	}

	// Start background monitoring
	p.startGameServerMonitoring(s, config, params, logger)

	// Start continuous health checking if enabled
	if ServiceSettings().EnableContinuousGameserverHealthCheck {
		go HealthCheckStart(s.Context(), logger, p.nk, s, p.internalIP, config.Endpoint.ExternalIP, int(config.Endpoint.Port), 500*time.Millisecond)
	}

	// Send the registration success message
	return s.SendEVR(Envelope{
		ServiceType: ServiceTypeServer,
		Messages:    []evr.Message{evr.NewBroadcasterRegistrationSuccess(config.ServerID, config.Endpoint.ExternalIP)},
		State:       RequireStateUnrequired,
	})
}

func (p *Pipeline) newGameServerParkingMatch(logger runtime.Logger, nk runtime.NakamaModule, presence runtime.Presence) (*MatchID, error) {
	ctx := context.Background()
	params := map[string]any{
		"gameserver": presence.GetStatus(),
	}
	// Create the match
	matchIDStr, err := nk.MatchCreate(ctx, EVRLobbySessionMatchModule, params)
	if err != nil {
		return nil, fmt.Errorf("failed to create parking match: %w", err)
	}
	// Update the game server stream with the match ID.
	if err := nk.StreamUserUpdate(StreamModeGameServer, presence.GetSessionId(), "", "", presence.GetUserId(), presence.GetSessionId(), false, false, matchIDStr); err != nil {
		return nil, fmt.Errorf("failed to update game server stream: %w", err)
	}
	matchID := MatchIDFromStringOrNil(matchIDStr)
	// Trigger the MatchJoin event if the match was created successfully.
	if session := p.sessionRegistry.Get(uuid.FromStringOrNil(presence.GetSessionId())); session != nil {
		found, allowed, _, reason, _, _ := p.matchRegistry.JoinAttempt(session.Context(), matchID.UUID, matchID.Node, session.UserID(), session.ID(), session.Username(), session.Expiry(), session.Vars(), session.ClientIP(), session.ClientPort(), presence.GetNodeId(), nil)
		if !found {
			return nil, fmt.Errorf("match not found: %s", matchID.String())
		}
		if !allowed {
			return nil, fmt.Errorf("join not allowed: %s", reason)
		}

		// Trigger the MatchJoin event.
		stream := server.PresenceStream{Mode: server.StreamModeMatchAuthoritative, Subject: matchID.UUID, Label: matchID.Node}
		m := server.PresenceMeta{
			Username: session.Username(),
			Format:   session.Format(),
		}
		if success, _ := p.tracker.Track(session.Context(), session.ID(), stream, session.UserID(), m); !success {
			return nil, fmt.Errorf("failed to track user in match: %s", matchIDStr)
		}
	}

	logger.WithField("mid", matchIDStr).Info("New parking match")
	return &matchID, nil
}
func HealthCheckStart(ctx context.Context, logger *zap.Logger, nk runtime.NakamaModule, session server.Session, localIP net.IP, remoteIP net.IP, port int, timeout time.Duration) {
	const (
		pingRequestSymbol               uint64 = 0x997279DE065A03B0
		RawPingAcknowledgeMessageSymbol uint64 = 0x4F7AE556E0B77891
	)

	var (
		sessionID  = session.ID()
		localAddr  = &net.UDPAddr{IP: net.ParseIP("0.0.0.0"), Port: 0}
		remoteAddr = &net.UDPAddr{IP: remoteIP, Port: int(port)}

		baseMetricsTags = map[string]string{
			"mode":              "unassigned",
			"operator_id":       session.UserID().String(),
			"operator_username": strings.TrimPrefix("broadcaster:", session.Username()),
			"external_ip":       remoteAddr.String(),
		}
	)

	logger = logger.With(zap.String("external_ip", remoteAddr.String()))

	// Establish a UDP connection to the specified address
	conn, err := net.DialUDP("udp", localAddr, remoteAddr)
	if err != nil {
		logger.Error("could not establish connection", zap.Error(err))
		return
	}
	defer conn.Close() // Ensure the connection is closed when the function ends

	var (

		// Construct the raw ping request message
		request       = make([]byte, 16)
		response      = make([]byte, 16)
		pingAckPrefix = make([]byte, 8)
	)
	binary.LittleEndian.PutUint64(request[:8], pingRequestSymbol) // Add the ping request symbol
	binary.LittleEndian.PutUint64(pingAckPrefix, RawPingAcknowledgeMessageSymbol)

	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()

	for {
		// Generate a random 8-byte number for the ping request
		if _, err := rand.Read(request[8:]); err != nil {
			logger.Error("could not generate random number for ping request", zap.Error(err))
			return
		}

		// Clone the metrics tags
		tags := maps.Clone(baseMetricsTags)

		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			// Get the current match ID
			matchID, _, err := GameServerBySessionID(nk, sessionID)
			if err != nil {
				logger.Warn("Failed to get game server presence by session ID", zap.Error(err))
				continue
			} else if matchID.IsNil() || errors.Is(err, ErrGameServerPresenceNotFound) {
				continue
			}

			// Get the match label
			if l, err := MatchLabelByID(ctx, nk, matchID); err != nil {
				logger.Warn("Failed to get match label by ID", zap.Error(err))
			} else if l != nil {
				if l.LobbyType != UnassignedLobby {
					// match is active. add metadata.
					maps.Copy(tags, map[string]string{
						"mode":     l.Mode.String(),
						"level":    l.Level.String(),
						"type":     l.LobbyType.String(),
						"group_id": l.GetGroupID().String(),
					})
					ticker.Reset(500 * time.Millisecond)
				} else {
					ticker.Reset(2 * time.Second)
				}
			}
		}
		// Start the timer
		start := time.Now()

		// Set a deadline for the connection to prevent hanging indefinitely
		if err := conn.SetDeadline(start.Add(timeout)); err != nil {
			logger.Error("could not set deadline for connection to %v", zap.Error(err))
			return
		}

		// Send the ping request to the broadcaster
		if _, err := conn.Write(request); err != nil {
			logger.Error("could not send ping request to %v", zap.Error(err))
			return
		}

		// Read the response from the broadcaster
		if _, err := conn.Read(response); err != nil {
			if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
				logger.Warn("ping request timed out", zap.String("remote_addr", remoteAddr.String()), zap.Int("timeout_ms", int(timeout.Milliseconds())))
			}
			logger.Error("could not read ping response from %v", zap.Error(err))
			return
		}

		// Calculate the round trip time
		rtt := time.Since(start)

		// Check if the response's number matches the sent number, indicating a successful ping
		if ok := bytes.Equal(response, append(pingAckPrefix, request[8:]...)); !ok {
			logger.Error("received unexpected response.",
				zap.String("remote_addr", remoteAddr.String()),
				zap.String("request", fmt.Sprintf("%x", request)),
				zap.String("response", fmt.Sprintf("%x", response)))
			return
		}

		nk.MetricsTimerRecord("gameserver_rtt_duration", tags, rtt)
	}
}

func BroadcasterHealthcheck(localIP net.IP, remoteIP net.IP, port int, timeout time.Duration) (rtt time.Duration, err error) {
	const (
		pingRequestSymbol               uint64 = 0x997279DE065A03B0
		RawPingAcknowledgeMessageSymbol uint64 = 0x4F7AE556E0B77891
	)

	laddr := &net.UDPAddr{
		IP:   localIP,
		Port: 0,
	}

	raddr := &net.UDPAddr{
		IP:   remoteIP,
		Port: int(port),
	}
	// Establish a UDP connection to the specified address
	conn, err := net.DialUDP("udp", laddr, raddr)
	if err != nil {
		return 0, fmt.Errorf("could not establish connection to %v: %w", raddr, err)
	}
	defer conn.Close() // Ensure the connection is closed when the function ends

	// Set a deadline for the connection to prevent hanging indefinitely
	if err := conn.SetDeadline(time.Now().Add(timeout)); err != nil {
		return 0, fmt.Errorf("could not set deadline for connection to %v: %w", raddr, err)
	}

	// Generate a random 8-byte number for the ping request
	token := make([]byte, 8)
	if _, err := rand.Read(token); err != nil {
		return 0, fmt.Errorf("could not generate random number for ping request: %w", err)
	}

	// Construct the raw ping request message
	request := make([]byte, 16)
	response := make([]byte, 16)
	binary.LittleEndian.PutUint64(request[:8], pingRequestSymbol) // Add the ping request symbol
	copy(request[8:], token)                                      // Add the random number

	// Start the timer
	start := time.Now()

	// Send the ping request to the broadcaster
	if _, err := conn.Write(request); err != nil {
		return 0, fmt.Errorf("could not send ping request to %v: %w", raddr, err)
	}

	// Read the response from the broadcaster
	if _, err := conn.Read(response); err != nil {
		if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
			return -1, fmt.Errorf("ping request to %v timed out (>%dms)", raddr, timeout.Milliseconds())
		}
		return 0, fmt.Errorf("could not read ping response from %v: %w", raddr, err)
	}
	// Calculate the round trip time
	rtt = time.Since(start)

	// Check if the response's symbol matches the expected acknowledge symbol
	if binary.LittleEndian.Uint64(response[:8]) != RawPingAcknowledgeMessageSymbol {
		return 0, fmt.Errorf("received unexpected response from %v: %v", raddr, response)
	}

	// Check if the response's number matches the sent number, indicating a successful ping
	if ok := binary.LittleEndian.Uint64(response[8:]) == binary.LittleEndian.Uint64(token); !ok {
		return 0, fmt.Errorf("received unexpected response from %v: %v", raddr, response)
	}

	return rtt, nil
}

func BroadcasterPortScan(rIP net.IP, startPort, endPort int, timeout time.Duration) (map[int]time.Duration, []error) {

	// Prepare slices to store results
	rtts := make(map[int]time.Duration, endPort-startPort+1)
	errs := make([]error, endPort-startPort+1)
	lIP := net.ParseIP("0.0.0.0")

	// Use a WaitGroup to wait for all goroutines to finish
	var wg sync.WaitGroup
	var mu sync.Mutex // Mutex to avoid race condition
	for port := startPort; port <= endPort; port++ {
		wg.Add(1)
		go func(port int) {
			defer wg.Done()

			rtt, err := BroadcasterHealthcheck(lIP, rIP, port, timeout)
			mu.Lock()
			if err != nil {
				errs[port-startPort] = err
			} else {
				rtts[port] = rtt
			}
			mu.Unlock()
		}(port)
	}
	wg.Wait()

	return rtts, errs
}

func BroadcasterRTTcheck(rIP net.IP, port, count int, timeout time.Duration) (rtts []time.Duration, err error) {
	// Create a slice to store round trip times (rtts)
	rtts = make([]time.Duration, count)
	// Set a timeout duration

	// Create a channel to signal when the goroutine is done
	doneCh := make(chan struct{})

	lIP := net.ParseIP("0.0.0.0")

	// Start a goroutine
	go func() {
		// Ensure the task is marked done on return
		defer close(doneCh)
		// Loop 5 times
		for i := range count {
			// Perform a health check on the broadcaster
			rtt, err := BroadcasterHealthcheck(lIP, rIP, port, timeout)
			if err != nil {
				// If there's an error, set the rtt to -1
				rtts[i] = -1
				continue
			}
			// Record the rtt
			rtts[i] = rtt
			// Sleep for the duration of the latency before the next iteration
			<-time.After(min(500, rtt))
		}
	}()
	// Wait for the goroutine to finish
	select {
	case <-doneCh:
		// Goroutine finished successfully
	case <-time.After(10 * time.Second):
		// Timeout occurred, return an error
		return nil, fmt.Errorf("broadcaster RTT check timed out after %v", timeout)
	}
	return rtts, nil
}

func GameServerBySessionID(nk runtime.NakamaModule, sessionID uuid.UUID) (MatchID, runtime.Presence, error) {
	// Get the game server presence. The MatchID is stored in the status field of the presence.
	presences, err := nk.StreamUserList(StreamModeGameServer, sessionID.String(), "", "", false, true)
	if err != nil {
		return MatchID{}, nil, err
	}

	if len(presences) == 0 {
		return MatchID{}, nil, ErrGameServerPresenceNotFound
	}

	presence := presences[0]

	matchID, err := MatchIDFromString(presence.GetStatus())
	if err != nil {
		return MatchID{}, nil, err
	}
	return matchID, presence, nil
}
