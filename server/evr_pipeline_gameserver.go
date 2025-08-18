package server

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"io"
	"maps"
	"net"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"fmt"

	"github.com/bwmarrin/discordgo"
	evr "github.com/echotools/nakama/v3/protocol"
	"github.com/echotools/nevr-common/v3/rtapi"
	"github.com/gofrs/uuid/v5"
	"github.com/heroiclabs/nakama-common/runtime"
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
func errFailedRegistration(session *sessionWS, logger *zap.Logger, err error, code evr.BroadcasterRegistrationFailureCode) error {
	logger.Warn("Failed to register game server", zap.Error(err))
	if err := session.SendEvrUnrequire(evr.NewBroadcasterRegistrationFailure(code)); err != nil {
		return fmt.Errorf("failed to send lobby registration failure: %w", err)
	}

	session.Close(err.Error(), runtime.PresenceReasonDisconnect)
	return fmt.Errorf("failed to register game server: %w", err)
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
func (p *EvrPipeline) gameserverRegistrationRequest(logger *zap.Logger, session *sessionWS, in *rtapi.Envelope) error {
	request := in.GetGameServerRegistration()
	var (
		loginSessionID = uuid.FromStringOrNil(request.LoginSessionId)
		serverID       = request.ServerId
		regionHash     = evr.Symbol(request.Region)
		internalIP     = net.ParseIP(request.InternalIpAddress)
		externalIP     = net.ParseIP(session.ClientIP())
		externalPort   = uint16(request.Port)
		versionLock    = evr.Symbol(request.VersionLock)
	)

	isNative := false
	if uuid.FromStringOrNil(request.LoginSessionId) != uuid.Nil {
		isNative = true
		logger.Info("Game server registration request", zap.String("login_session_id", loginSessionID.String()), zap.Uint64("server_id", serverID), zap.String("internal_ip", internalIP.String()), zap.Uint16("port", externalPort), zap.String("region", regionHash.String()), zap.String("version_lock", versionLock.String()), zap.Uint32("time_step_usecs", request.TimeStepUsecs), zap.String("version", request.Version))
		if request.Version < MINIMUM_NATIVE_SERVER_VERSION {
			logger.Warn("Game server version is below minimum required version", zap.String("version", request.Version), zap.String("minimum_version", MINIMUM_NATIVE_SERVER_VERSION))
			return errFailedRegistration(session, logger, fmt.Errorf("game server version is below minimum required version: %s < %s", request.Version, MINIMUM_NATIVE_SERVER_VERSION), evr.BroadcasterRegistration_Unknown)
		}

		// Get the login session ID
		loginSession := p.nk.sessionRegistry.Get(loginSessionID)
		if loginSession == nil {
			return errFailedRegistration(session, logger, errors.New("failed to get login session"), evr.BroadcasterRegistration_Unknown)
		}

		if err := Secondary(session, loginSession.(*sessionWS), true, false); err != nil {
			return errFailedRegistration(session, logger, err, evr.BroadcasterRegistration_Unknown)
		}
	}

	if session.userID.IsNil() {
		return errFailedRegistration(session, logger, errors.New("game server is not authenticated."), evr.BroadcasterRegistration_Unknown)
	}

	ctx := session.Context()
	params, ok := LoadParams(ctx)
	if !ok {
		return fmt.Errorf("session parameters not provided")
	}

	userIDStr := session.userID.String()

	if err := BroadcasterSession(session, session.userID, "broadcaster:"+session.Username(), serverID); err != nil {
		return fmt.Errorf("failed to create broadcaster session: %w", err)
	}

	logger = logger.With(zap.String("uid", userIDStr), zap.String("discord_id", params.DiscordID()))

	// Get a list of the user's guild memberships and set to the largest one
	guildGroups, err := GuildUserGroupsList(ctx, p.nk, p.guildGroupRegistry, userIDStr)
	if err != nil {
		return fmt.Errorf("failed to get guild groups: %w", err)
	}
	if len(guildGroups) == 0 {
		return fmt.Errorf("user is not in any guild groups")
	}

	params.guildGroups = guildGroups
	StoreParams(ctx, &params)

	// By default, use the client's IP address as the external IP address

	// If the external IP address is specified in the config.json, use it instead of the system's external IP
	if params.externalServerAddr != "" {
		parts := strings.Split(params.externalServerAddr, ":")
		if len(parts) != 2 {
			return errFailedRegistration(session, logger, fmt.Errorf("invalid external IP address: %s. it must be `ip:port`", params.externalServerAddr), evr.BroadcasterRegistration_Unknown)
		}

		// If the external IP address is not a valid IP address, look it up as a hostname
		if externalIP = net.ParseIP(parts[0]); externalIP == nil {
			if ips, err := net.LookupIP(parts[0]); err != nil {
				return errFailedRegistration(session, logger, fmt.Errorf("invalid address `%s`: %w", parts[0], err), evr.BroadcasterRegistration_Unknown)
			} else {
				externalIP = ips[0]
			}
		}

		// Parse the external port
		if p, err := strconv.Atoi(parts[1]); err != nil {
			return errFailedRegistration(session, logger, fmt.Errorf("invalid external port: %s", parts[1]), evr.BroadcasterRegistration_Unknown)
		} else {
			externalPort = uint16(p)
		}
	}

	if isPrivateIP(externalIP) {
		externalIP = p.externalIP
		logger.Warn("Game server is on a private IP, using this systems external IP", zap.String("private_ip", internalIP.String()), zap.String("external_ip", externalIP.String()), zap.String("port", fmt.Sprintf("%d", externalPort)))
	}

	// Include all guilds by default, or if "any" is in the list
	includeAll := false
	if len(params.serverGuilds) == 0 || slices.Contains(params.serverGuilds, "any") {
		includeAll = true
	}

	// Filter the guild groups to host for
	hostingGroupIDs := make([]uuid.UUID, 0)
	for _, gg := range guildGroups {

		// Must be a server host and not suspended
		if !gg.IsServerHost(userIDStr) || gg.IsSuspended(userIDStr, nil) {
			continue
		}

		// Include the group if it is in the list of server guilds, includes "any" or is empty
		if includeAll || slices.Contains(params.serverGuilds, gg.GuildID) {
			hostingGroupIDs = append(hostingGroupIDs, gg.ID())
		}
	}

	// Configure the region codes to use for finding the game server
	regionCodes := make([]string, 0, len(params.serverRegions))

	if len(params.serverRegions) == 0 {
		regionCodes = append(regionCodes, "default")
	}

	// Add the server regions specified in the config.json
	for _, r := range params.serverRegions {
		regionCodes = append(regionCodes, r)
	}

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

	// Truncate all region codes to 16 characters
	for i, r := range regionCodes {
		if len(r) > 32 {
			regionCodes[i] = r[:32]
		}
	}

	logger = logger.With(zap.Any("group_ids", hostingGroupIDs), zap.Strings("tags", params.serverTags), zap.Strings("regions", regionCodes))

	// Add the server id, as displayed by the game server once registered (i.e. "0x5A700FE2D34D5B6D")
	regionCodes = append(regionCodes, fmt.Sprintf("0x%x", serverID))

	var ipInfo IPInfo
	if p.ipInfoCache != nil {
		ipInfo, err = p.ipInfoCache.Get(ctx, externalIP.String())
		if err != nil {
			logger.Warn("Failed to get IPQS data", zap.Error(err))
		}
		if slices.Contains(regionCodes, "default") {
			regionCodes = append(regionCodes,
				LocationToRegionCode(ipInfo.CountryCode(), ipInfo.Region(), ipInfo.City()),
				LocationToRegionCode(ipInfo.CountryCode(), ipInfo.Region(), ""),
				LocationToRegionCode(ipInfo.CountryCode(), "", ""),
			)
		}
	}

	// Create the broadcaster config
	config := NewGameServerPresence(session.UserID(), session.id, serverID, internalIP, externalIP, externalPort, hostingGroupIDs, regionCodes, versionLock, params.serverTags, params.supportedFeatures, request.TimeStepUsecs, ipInfo, params.geoHashPrecision, isNative, request.Version)

	logger = logger.With(zap.String("internal_ip", internalIP.String()), zap.String("external_ip", externalIP.String()), zap.Uint16("port", externalPort))

	// Give the server some time to start up
	time.Sleep(2 * time.Second)

	// Validate connectivity to the game server.
	isAlive := false

	// Check if the broadcaster is available
	retries := 5
	var rtt time.Duration
	for i := 0; i < retries; i++ {
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
			isAlive = true
			break
		}
	}
	if !isAlive {
		// If the broadcaster is not available, send an error message to the user on discord
		logger.Error("Broadcaster could not be reached", zap.Error(err))
		errorMessage := fmt.Sprintf("Broadcaster (Endpoint ID: %s, Server ID: %d) could not be reached. Error: %v", config.Endpoint.ExternalAddress(), config.ServerID, err)
		go sendDiscordError(errors.New(errorMessage), params.DiscordID(), logger, p.discordCache.dg)
		return errFailedRegistration(session, logger, errors.New(errorMessage), evr.BroadcasterRegistration_Failure)
	}

	status := config.GetStatus()

	// Join the monitoring stream
	if err := p.nk.StreamUserUpdate(StreamModeGameServer, session.ID().String(), "", "", config.GetUserId(), config.GetSessionId(), false, false, status); err != nil {
		logger.Warn("Failed to update game server stream", zap.Error(err))
		return errFailedRegistration(session, logger, err, evr.BroadcasterRegistration_Failure)
	}

	// Join the game server to the game server pool streams
	for _, gID := range config.GroupIDs {
		if err := p.nk.StreamUserUpdate(StreamModeGameServer, gID.String(), "", "", config.GetUserId(), config.GetSessionId(), false, false, status); err != nil {
			logger.Warn("Failed to update game server stream", zap.Error(err))
			return errFailedRegistration(session, logger, err, evr.BroadcasterRegistration_Failure)
		}
	}

	// Monitor the game server and create new parking matches as needed.
	go func() {
		// Create the initial parking match for the game server.
		if _, err = newGameServerParkingMatch(NewRuntimeGoLogger(logger), p.nk, config); err != nil {
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
				rtts, err := BroadcasterRTTcheck(p.internalIP, config.Endpoint.ExternalIP, int(config.Endpoint.Port), 5, 500*time.Millisecond)
				if err != nil || len(rtts) == 0 {
					logger.Warn("Game server is not responding", zap.Error(err), zap.String("endpoint", config.Endpoint.String()))
					// Send the discord error
					errorMessage := fmt.Sprintf("Game server (Endpoint ID: %s, Server ID: %d) is not responding. Error: %v", config.Endpoint.ExternalAddress(), config.ServerID, err)
					go sendDiscordError(errors.New(errorMessage), params.DiscordID(), logger, p.discordCache.dg)
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
					if _, err = newGameServerParkingMatch(NewRuntimeGoLogger(logger), p.nk, config); err != nil {
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

	if ServiceSettings().EnableContinuousGameserverHealthCheck {
		go HealthCheckStart(ctx, logger, p.nk, session, p.internalIP, config.Endpoint.ExternalIP, int(config.Endpoint.Port), 500*time.Millisecond)
	}

	// Send the registration success message
	return session.SendEvrUnrequire(evr.NewBroadcasterRegistrationSuccess(config.ServerID, config.Endpoint.ExternalIP))
}

func newGameServerParkingMatch(logger runtime.Logger, nk runtime.NakamaModule, p runtime.Presence) (*MatchID, error) {
	ctx := context.Background()
	params := map[string]any{
		"gameserver": p.GetStatus(),
	}
	// Create the match
	matchIDStr, err := nk.MatchCreate(ctx, EVRLobbySessionMatchModule, params)
	if err != nil {
		return nil, fmt.Errorf("failed to create parking match: %w", err)
	}
	// Update the game server stream with the match ID.
	if err := nk.StreamUserUpdate(StreamModeGameServer, p.GetSessionId(), "", StreamLabelMatchService, p.GetUserId(), p.GetSessionId(), false, false, matchIDStr); err != nil {
		return nil, fmt.Errorf("failed to update game server stream: %w", err)
	}
	matchID := MatchIDFromStringOrNil(matchIDStr)
	// Trigger the MatchJoin event if the match was created successfully.
	if _nk, ok := nk.(*RuntimeGoNakamaModule); ok {
		if session := _nk.sessionRegistry.Get(uuid.FromStringOrNil(p.GetSessionId())); session != nil {
			found, allowed, _, reason, _, _ := _nk.matchRegistry.JoinAttempt(session.Context(), matchID.UUID, matchID.Node, session.UserID(), session.ID(), session.Username(), session.Expiry(), session.Vars(), session.ClientIP(), session.ClientPort(), p.GetNodeId(), nil)
			if !found {
				return nil, fmt.Errorf("match not found: %s", matchID.String())
			}
			if !allowed {
				return nil, fmt.Errorf("join not allowed: %s", reason)
			}

			// Trigger the MatchJoin event.
			stream := PresenceStream{Mode: StreamModeMatchAuthoritative, Subject: matchID.UUID, Label: matchID.Node}
			m := PresenceMeta{
				Username: session.Username(),
				Format:   session.Format(),
			}
			if success, _ := _nk.tracker.Track(session.Context(), session.ID(), stream, session.UserID(), m); success {
				// Kick the user from any other matches they may be part of.
				// WARNING This cannot be used during transition. It will kick the player from their current match.
				//p.tracker.UntrackLocalByModes(session.ID(), matchStreamModes, stream)
			}
		}
	}
	logger.WithField("mid", matchIDStr).Info("New parking match")
	return &matchID, nil
}

func HealthCheckStart(ctx context.Context, logger *zap.Logger, nk runtime.NakamaModule, session Session, localIP net.IP, remoteIP net.IP, port int, timeout time.Duration) {
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

func BroadcasterPortScan(lIP net.IP, rIP net.IP, startPort, endPort int, timeout time.Duration) (map[int]time.Duration, []error) {

	// Prepare slices to store results
	rtts := make(map[int]time.Duration, endPort-startPort+1)
	errs := make([]error, endPort-startPort+1)

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

func BroadcasterRTTcheck(lIP net.IP, rIP net.IP, port, count int, timeout time.Duration) (rtts []time.Duration, err error) {
	// Create a slice to store round trip times (rtts)
	rtts = make([]time.Duration, count)
	// Set a timeout duration

	doneCh := make(chan struct{})

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

func DetermineLocalIPAddress() (net.IP, error) {
	conn, err := net.Dial("udp", "8.8.8.8:80")
	if err != nil {
		return nil, err
	}
	return conn.LocalAddr().(*net.UDPAddr).IP, nil
}

func DetermineExternalIPAddress() (net.IP, error) {
	response, err := http.Get("https://api.ipify.org?format=text")
	if err != nil {
		return nil, err
	}

	defer response.Body.Close()

	data, _ := io.ReadAll(response.Body)
	addr := net.ParseIP(string(data))
	if addr == nil {
		return nil, errors.New("failed to parse IP address")
	}

	return addr, nil
}

func isPrivateIP(ip net.IP) bool {
	privateRanges := []string{
		"127.0.0.0/8",
		"10.0.0.0/8",
		"172.16.0.0/12",
		"192.168.0.0/16",
	}
	for _, cidr := range privateRanges {
		_, subnet, _ := net.ParseCIDR(cidr)
		if subnet.Contains(ip) {
			return true
		}
	}
	return false
}

func GameServerBySessionID(nk runtime.NakamaModule, sessionID uuid.UUID) (MatchID, runtime.Presence, error) {
	// Get the game server presence. The MatchID is stored in the status field of the presence.
	presences, err := nk.StreamUserList(StreamModeGameServer, sessionID.String(), "", StreamLabelMatchService, false, true)
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
