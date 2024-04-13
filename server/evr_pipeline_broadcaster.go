package server

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"fmt"

	"github.com/gofrs/uuid/v5"
	"github.com/heroiclabs/nakama-common/rtapi"
	"github.com/heroiclabs/nakama-common/runtime"
	"github.com/heroiclabs/nakama/v3/server/evr"
	"github.com/samber/lo"
	"go.uber.org/zap"
)

// sendDiscordError sends an error message to the user on discord
func sendDiscordError(e error, discordId string, logger *zap.Logger, discordRegistry DiscordRegistry) {
	// Message the user on discord
	bot := discordRegistry.GetBot()
	if bot != nil && discordId != "" {
		channel, err := bot.UserChannelCreate(discordId)
		if err != nil {
			logger.Warn("Failed to create user channel", zap.Error(err))
		}
		_, err = bot.ChannelMessageSend(channel.ID, fmt.Sprintf("Failed to register game server: %v", e))
		if err != nil {
			logger.Warn("Failed to send message to user", zap.Error(err))
		}
	}
}

// errFailedRegistration sends a failure message to the broadcaster and closes the session
func errFailedRegistration(session *sessionWS, err error, code evr.BroadcasterRegistrationFailureCode) error {
	if err := session.SendEvr([]evr.Message{
		evr.NewBroadcasterRegistrationFailure(code),
	}); err != nil {
		return fmt.Errorf("failed to send lobby registration failure: %v", err)
	}

	session.Close(err.Error(), runtime.PresenceReasonDisconnect)
	return fmt.Errorf("failed to register game server: %v", err)
}

// broadcasterSessionEnded is called when the broadcaster has ended the session.
func (p *EvrPipeline) broadcasterSessionEnded(ctx context.Context, logger *zap.Logger, session *sessionWS, in evr.Message) error {
	// The broadcaster has ended the session.
	// shutdown the match. A new parking match will be created in response to the match leave message.

	config, found := p.broadcasterRegistrationBySession.Load(session.ID().String())
	if !found {
		return fmt.Errorf("broadcaster session not found")
	}

	matchId, found := p.matchBySessionID.Load(session.ID().String())
	if !found {
		return fmt.Errorf("match not found")
	}

	// Leave the old match
	leavemsg := &rtapi.Envelope{
		Message: &rtapi.Envelope_MatchLeave{
			MatchLeave: &rtapi.MatchLeave{
				MatchId: matchId,
			},
		},
	}

	if ok := session.pipeline.ProcessRequest(logger, session, leavemsg); !ok {
		return fmt.Errorf("failed process leave request")
	}

	err := p.newParkingMatch(logger, session, config)
	if err != nil {
		return fmt.Errorf("failed to create new parking match: %v", err)
	}

	return nil
}

// broadcasterRegistrationRequest is called when the broadcaster has sent a registration request.
func (p *EvrPipeline) broadcasterRegistrationRequest(ctx context.Context, logger *zap.Logger, session *sessionWS, in evr.Message) error {
	request := in.(*evr.BroadcasterRegistrationRequest)
	discordId := ""

	// server connections are authenticated by discord ID and password.
	// Get the discordId and password from the context
	// Get the tags and guilds from the url params
	discordId, password, tags, guildIds, err := extractAuthenticationDetailsFromContext(ctx)
	if err != nil {
		return errFailedRegistration(session, err, evr.BroadcasterRegistration_Failure)
	}

	// Authenticate the broadcaster
	userId, _, err := p.authenticateBroadcaster(ctx, logger, session, discordId, password, guildIds, tags)
	if err != nil {
		return errFailedRegistration(session, err, evr.BroadcasterRegistration_Failure)
	}

	// Set the external address in the request (to use for the registration cache).
	externalIP := net.ParseIP(session.ClientIP())
	if isPrivateIP(externalIP) {
		logger.Warn("Broadcaster is on a private IP, using this systems external IP", zap.String("privateIP", externalIP.String()), zap.String("externalIP", p.externalIP.String()))
		externalIP = p.externalIP
	}

	// Create the broadcaster config
	config := broadcasterConfig(userId, session.id.String(), request.ServerId, request.InternalIP, externalIP, request.Port, request.Region, request.VersionLock, tags)

	// Get the hosted channels
	channels, err := p.getBroadcasterHostInfo(ctx, logger, session, userId, discordId, guildIds)
	if err != nil {
		return errFailedRegistration(session, err, evr.BroadcasterRegistration_Failure)
	}
	config.Channels = channels

	// Validate connectivity to the broadcaster.
	// Wait 2 seconds, then check

	time.Sleep(2 * time.Second)

	alive := false

	// Check if the broadcaster is available
	retries := 5
	var rtt time.Duration
	for i := 0; i < retries; i++ {
		rtt, err = BroadcasterHealthcheck(p.localIP, config.Endpoint.ExternalIP, int(config.Endpoint.Port), 500*time.Millisecond)
		if err != nil {
			logger.Warn("Failed to healthcheck broadcaster", zap.Error(err))
			time.Sleep(500 * time.Millisecond)
			continue
		}
		if rtt >= 0 {
			alive = true
			break
		}
	}
	if !alive {
		// If the broadcaster is not available, send an error message to the user on discord
		errorMessage := fmt.Sprintf("Broadcaster (Endpoint ID: %s, Server ID: %d) could not be reached. Error: %v", config.Endpoint.ID(), config.ServerID, err)
		go sendDiscordError(errors.New(errorMessage), discordId, logger, p.discordRegistry)
		return errFailedRegistration(session, errors.New(errorMessage), evr.BroadcasterRegistration_Failure)
	}

	p.broadcasterRegistrationBySession.Store(session.ID().String(), config)
	p.matchmakingRegistry.broadcasters.Store(config.Endpoint.ID(), config.Endpoint)
	// Create a new parking match
	if err := p.newParkingMatch(logger, session, config); err != nil {
		return errFailedRegistration(session, err, evr.BroadcasterRegistration_Failure)
	}
	// Send the registration success message
	if err := session.SendEvr([]evr.Message{
		evr.NewBroadcasterRegistrationSuccess(config.ServerID, config.Endpoint.ExternalIP),
		evr.NewSTcpConnectionUnrequireEvent(),
	}); err != nil {
		return errFailedRegistration(session, fmt.Errorf("failed to send lobby registration failure: %v", err), evr.BroadcasterRegistration_Failure)
	}

	go func() {
		// Every 10 seconds, check that this broadcaster is a member of a match
		// If not, disconnect the broadcaster
		ticker := time.NewTicker(10 * time.Second)
		defer ticker.Stop()

		for {
			select {
			case <-session.Context().Done():
				return
			case <-ticker.C:
				if _, found := p.matchBySessionID.Load(session.ID().String()); !found {
					logger.Warn("Broadcaster is not in a match, disconnecting")
					session.Close("", runtime.PresenceReasonDisconnect)
					return
				}
			}
		}

	}()

	return nil
}

func extractAuthenticationDetailsFromContext(ctx context.Context) (discordId, password string, tags []string, guildIds []string, err error) {
	var ok bool

	// Get the discord id from the context
	discordId, ok = ctx.Value(ctxDiscordIdKey{}).(string)
	if !ok || discordId == "" {
		return "", "", nil, nil, fmt.Errorf("no url params provided")
	}

	// Get the password from the context
	password, ok = ctx.Value(ctxPasswordKey{}).(string)
	if !ok || password == "" {
		return "", "", nil, nil, fmt.Errorf("no url params provided")
	}

	// Get the url params
	params, ok := ctx.Value(ctxUrlParamsKey{}).(map[string][]string)
	if !ok {
		return "", "", nil, nil, fmt.Errorf("no url params provided")
	}

	// Get the tags from the url params
	tags = make([]string, 0)
	if tagsets, ok := params["tags"]; ok {
		for _, tagstr := range tagsets {
			tags = append(tags, strings.Split(tagstr, ",")...)
		}
	}

	// Get the guilds that the broadcaster wants to host for
	guildIds = make([]string, 0)
	if guildparams, ok := params["guilds"]; ok {
		for _, guildstr := range guildparams {
			for _, guildId := range strings.Split(guildstr, ",") {
				s := strings.Trim(guildId, " ")
				if s != "" {
					guildIds = append(guildIds, s)
				}
			}
		}
	}

	// If the list of guilds is empty or contains "any", use the user's groups
	if lo.Contains(guildIds, "any") {
		guildIds = make([]string, 0)
	}

	return discordId, password, tags, guildIds, nil
}

func (p *EvrPipeline) authenticateBroadcaster(ctx context.Context, logger *zap.Logger, session *sessionWS, discordId, password string, guildIds []string, tags []string) (string, string, error) {
	// Get the user id from the discord id
	uid, err := p.discordRegistry.GetUserIdByDiscordId(ctx, discordId, false)
	if err != nil {
		return "", "", fmt.Errorf("failed to find user for Discord ID: %v", err)
	}
	userId := uid.String()
	// Authenticate the user
	userId, username, _, err := AuthenticateEmail(ctx, logger, session.pipeline.db, userId+"@"+p.placeholderEmail, password, "", false)
	if err != nil {
		return "", "", fmt.Errorf("password authentication failure")
	}
	p.logger.Info("Authenticated broadcaster", zap.String("operator_userID", userId), zap.String("operator_username", username))

	// The broadcaster is authenticated, set the userID as the broadcasterID and create a broadcaster session
	// Broadcasters are not linked to the login session, they have a generic username and only use the serverdb path.
	err = session.BroadcasterSession(userId, "broadcaster:"+username)
	if err != nil {
		return "", "", fmt.Errorf("failed to create broadcaster session: %v", err)
	}

	return userId, username, nil
}

func broadcasterConfig(userId, sessionId string, serverId uint64, internalIP, externalIP net.IP, port uint16, region evr.Symbol, versionLock uint64, tags []string) *MatchBroadcaster {

	config := &MatchBroadcaster{
		SessionID:  sessionId,
		OperatorID: userId,
		ServerID:   serverId,
		Endpoint: evr.Endpoint{
			InternalIP: internalIP,
			ExternalIP: externalIP,
			Port:       port,
		},
		Region:      region,
		VersionLock: versionLock,
		Channels:    make([]uuid.UUID, 0),

		Tags: make([]string, 0),
	}

	if len(tags) > 0 {
		config.Tags = tags
	}
	return config
}

func (p *EvrPipeline) getBroadcasterHostInfo(ctx context.Context, logger *zap.Logger, session *sessionWS, userId, discordId string, guildIds []string) (channels []uuid.UUID, err error) {

	// If the list of guilds is empty, get the user's guild groups
	if len(guildIds) == 0 {

		// Get the user's guild groups
		groups, err := p.discordRegistry.GetGuildGroups(ctx, uuid.FromStringOrNil(userId))
		if err != nil {
			return nil, fmt.Errorf("failed to get user's guild groups: %v", err)
		}

		// Create a slice of user's guild group IDs
		for _, g := range groups {
			guildId, ok := p.discordRegistry.Get(g.GetId())
			if !ok {
				logger.Warn("Guild not found", zap.String("groupId", g.GetId()))
				continue
			}

			guildIds = append(guildIds, guildId)
		}
	}

	allowed := make([]string, 0)
	// Validate the group memberships
	for _, guildId := range guildIds {
		if guildId == "" {
			continue
		}

		// Get the guild member
		member, err := p.discordRegistry.GetGuildMember(ctx, guildId, discordId)
		if err != nil {
			logger.Warn("User not a member of the guild", zap.String("guildId", guildId))
			continue
		}

		// Get the group id for the guild
		groupId, found := p.discordRegistry.Get(guildId)
		if !found {
			logger.Warn("Guild not found", zap.String("guildId", guildId))
			continue
		}

		// Get the guild's metadata
		md, err := p.discordRegistry.GetGuildGroupMetadata(ctx, groupId)
		if err != nil {
			logger.Warn("Failed to get guild group metadata", zap.String("groupId", groupId), zap.Error(err))
			continue
		}

		// If the broadcaster role is blank, add it to the channels
		if md.BroadcasterHostRole == "" {
			allowed = append(allowed, guildId)
			continue
		}

		// Verify the user has the broadcaster role
		if !lo.Contains(member.Roles, md.BroadcasterHostRole) {
			logger.Warn("User does not have the broadcaster role, allowing anyway", zap.String("guildId", guildId))
			continue
		}

		// Add the channel to the list of hosting channels
		allowed = append(allowed, guildId)
	}

	// Get the groupId for each guildId
	groupIds := make([]uuid.UUID, 0)
	for _, guildId := range allowed {
		groupId, found := p.discordRegistry.Get(guildId)
		if !found {
			logger.Warn("Guild not found", zap.String("guildId", guildId))
			continue
		}
		groupIds = append(groupIds, uuid.FromStringOrNil(groupId))
	}

	return groupIds, nil
}

func (p *EvrPipeline) newParkingMatch(logger *zap.Logger, session *sessionWS, config *MatchBroadcaster) error {

	// Create the match state from the config
	_, params, _, err := NewEvrMatchState(config.Endpoint, config, session.id.String())
	if err != nil {
		return fmt.Errorf("failed to create match state: %v", err)
	}

	// Create the match
	matchId, err := p.matchRegistry.CreateMatch(context.Background(), p.runtime.matchCreateFunction, EvrMatchmakerModule, params)
	if err != nil {
		return fmt.Errorf("failed to create match: %v", err)
	}

	// (Attempt to) join the match
	joinmsg := &rtapi.Envelope{
		Message: &rtapi.Envelope_MatchJoin{
			MatchJoin: &rtapi.MatchJoin{
				Id:       &rtapi.MatchJoin_MatchId{MatchId: matchId},
				Metadata: map[string]string{},
			},
		},
	}

	// Process the join request
	if ok := session.pipeline.ProcessRequest(logger, session, joinmsg); !ok {
		return fmt.Errorf("failed process join request")
	}
	logger.Debug("New parking match", zap.String("matchId", matchId))

	p.matchBySessionID.Store(session.ID().String(), matchId)

	return nil
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
		return 0, fmt.Errorf("could not establish connection to %v: %v", raddr, err)
	}
	defer conn.Close() // Ensure the connection is closed when the function ends

	// Set a deadline for the connection to prevent hanging indefinitely
	if err := conn.SetDeadline(time.Now().Add(timeout)); err != nil {
		return 0, fmt.Errorf("could not set deadline for connection to %v: %v", raddr, err)
	}

	// Generate a random 8-byte number for the ping request
	token := make([]byte, 8)
	if _, err := rand.Read(token); err != nil {
		return 0, fmt.Errorf("could not generate random number for ping request: %v", err)
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
		return 0, fmt.Errorf("could not send ping request to %v: %v", raddr, err)
	}

	// Read the response from the broadcaster
	if _, err := conn.Read(response); err != nil {
		if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
			return -1, fmt.Errorf("ping request to %v timed out (>%dms)", raddr, timeout.Milliseconds())
		}
		return 0, fmt.Errorf("could not read ping response from %v: %v", raddr, err)
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

func BroadcasterRTTcheck(lIP net.IP, rIP net.IP, port, count int, interval, timeout time.Duration) (rtts []time.Duration, err error) {
	// Create a slice to store round trip times (rtts)
	rtts = make([]time.Duration, count)
	// Set a timeout duration

	// Create a WaitGroup to manage concurrency
	var wg sync.WaitGroup
	// Add a task to the WaitGroup
	wg.Add(1)

	// Start a goroutine
	go func() {
		// Ensure the task is marked done on return
		defer wg.Done()
		// Loop 5 times
		for i := 0; i < count; i++ {
			// Perform a health check on the broadcaster
			rtt, err := BroadcasterHealthcheck(lIP, rIP, port, timeout)
			if err != nil {
				// If there's an error, set the rtt to -1
				rtts[i] = -1
				continue
			}
			// Record the rtt
			rtts[i] = rtt
			// Sleep for the duration of the ping interval before the next iteration
			time.Sleep(interval)
		}
	}()

	wg.Wait()
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
