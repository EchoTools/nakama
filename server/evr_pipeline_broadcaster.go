package server

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"fmt"

	"github.com/gofrs/uuid/v5"
	"github.com/heroiclabs/nakama-common/api"
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
func errFailedRegistration(session *sessionWS, logger *zap.Logger, err error, code evr.BroadcasterRegistrationFailureCode) error {
	logger.Warn("Failed to register game server", zap.Error(err))
	if err := session.SendEvr(
		evr.NewBroadcasterRegistrationFailure(code),
	); err != nil {
		return fmt.Errorf("failed to send lobby registration failure: %v", err)
	}

	session.Close(err.Error(), runtime.PresenceReasonDisconnect)
	return fmt.Errorf("failed to register game server: %v", err)
}

// broadcasterRegistrationRequest is called when the broadcaster has sent a registration request.
func (p *EvrPipeline) broadcasterRegistrationRequest(ctx context.Context, logger *zap.Logger, session *sessionWS, in evr.Message) error {
	request := in.(*evr.BroadcasterRegistrationRequest)
	discordId := ""

	// server connections are authenticated by discord ID and password.
	// Get the discordId and password from the context
	// Get the tags and guilds from the url params
	discordId, password, tags, guildIds, regions, err := extractAuthenticationDetailsFromContext(ctx)
	if err != nil {
		return errFailedRegistration(session, logger, err, evr.BroadcasterRegistration_Unknown)
	}

	logger = logger.With(zap.String("discord_id", discordId), zap.Strings("guild_ids", guildIds), zap.Strings("tags", tags), zap.Strings("regions", lo.Map(regions, func(v evr.Symbol, _ int) string { return v.String() })))

	// Assume that the regions provided are the ONLY regions the broadcaster wants to host in
	if len(regions) == 0 {
		regions = append(regions, evr.DefaultRegion)
	}

	if request.Region != evr.DefaultRegion {
		regions = append(regions, request.Region)
	}

	// Authenticate the broadcaster
	userId, _, err := p.authenticateBroadcaster(ctx, logger, session, discordId, password, guildIds, tags, request.ServerId)
	if err != nil {
		return errFailedRegistration(session, logger, err, evr.BroadcasterRegistration_AccountDoesNotExist)
	}

	// Add the hash of the operator's ID as a region
	regions = append(regions, evr.ToSymbol(userId))

	// Add the server id as a region
	regions = append(regions, evr.ToSymbol(request.ServerId))

	// Remove any duplicates from the regions
	seenRegions := make(map[evr.Symbol]struct{})
	uniqueRegions := make([]evr.Symbol, 0)
	for _, r := range regions {
		if _, seen := seenRegions[r]; !seen {
			uniqueRegions = append(uniqueRegions, r)
			seenRegions[r] = struct{}{}
		}
	}
	regions = uniqueRegions

	logger = logger.With(zap.String("userId", userId))

	// Set the external address in the request (to use for the registration cache).
	externalIP := net.ParseIP(session.ClientIP())
	if isPrivateIP(externalIP) {
		logger.Warn("Broadcaster is on a private IP, using this systems external IP", zap.String("privateIP", externalIP.String()), zap.String("externalIP", p.externalIP.String()))
		externalIP = p.externalIP
	}

	logger = logger.With(zap.String("externalIP", externalIP.String()))

	features := ctx.Value(ctxFeaturesKey{}).([]string)

	// Create the broadcaster config
	config := broadcasterConfig(userId, session.id.String(), request.ServerId, request.InternalIP, externalIP, request.Port, regions, request.VersionLock, tags, features)

	// Get the hosted groupIDs
	groupIDs, err := p.getBroadcasterHostGroups(ctx, userId, guildIds)
	if err != nil {
		return errFailedRegistration(session, logger, err, evr.BroadcasterRegistration_Unknown)
	}

	// Add the operators userID to the group ids. this allows any host to spawn on a server they operate.
	config.GroupIDs = groupIDs

	logger = logger.With(zap.String("internalIP", request.InternalIP.String()), zap.String("externalIP", externalIP.String()), zap.Uint16("port", request.Port))
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
		errorMessage := fmt.Sprintf("Broadcaster (Endpoint ID: %s, Server ID: %d) could not be reached. Error: %v", config.Endpoint.ExternalAddress(), config.ServerID, err)
		go sendDiscordError(errors.New(errorMessage), discordId, logger, p.discordRegistry)
		return errFailedRegistration(session, logger, errors.New(errorMessage), evr.BroadcasterRegistration_Failure)
	}
	configJson, err := json.Marshal(config)
	if err != nil {
		return errFailedRegistration(session, logger, err, evr.BroadcasterRegistration_Failure)
	}

	if err := p.runtimeModule.StreamUserUpdate(StreamModeGameServer, session.ID().String(), "", StreamLabelGameServerService, userId, session.ID().String(), true, false, string(configJson)); err != nil {
		return errFailedRegistration(session, logger, err, evr.BroadcasterRegistration_Failure)
	}

	p.broadcasterRegistrationBySession.Store(session.ID().String(), config)
	go func() {
		<-session.Context().Done()
		p.broadcasterRegistrationBySession.Delete(session.ID().String())
	}()

	// Create a new parking match
	if err := p.newParkingMatch(logger, session, config); err != nil {
		return errFailedRegistration(session, logger, err, evr.BroadcasterRegistration_Failure)
	}
	// Send the registration success message
	if err := session.SendEvr(
		evr.NewBroadcasterRegistrationSuccess(config.ServerID, config.Endpoint.ExternalIP),
		evr.NewSTcpConnectionUnrequireEvent(),
	); err != nil {
		return errFailedRegistration(session, logger, fmt.Errorf("failed to send lobby registration failure: %v", err), evr.BroadcasterRegistration_Failure)
	}

	return nil
}

func extractAuthenticationDetailsFromContext(ctx context.Context) (discordId, password string, tags, guildIds []string, regions []evr.Symbol, err error) {
	var ok bool

	// Get the discord id from the context
	discordId, ok = ctx.Value(ctxAuthDiscordIDKey{}).(string)
	if !ok || discordId == "" {
		return "", "", nil, nil, nil, fmt.Errorf("`discordid` url param missing")
	}

	// Get the password from the context
	password, ok = ctx.Value(ctxAuthPasswordKey{}).(string)
	if !ok || password == "" {
		return "", "", nil, nil, nil, fmt.Errorf("`password` url param missing")
	}

	// Get the url params
	params, ok := ctx.Value(ctxUrlParamsKey{}).(map[string][]string)
	if !ok {
		return "", "", nil, nil, nil, fmt.Errorf("params not provided")
	}

	// Get the tags from the url params
	tags = make([]string, 0)
	if tagsets, ok := params["tags"]; ok {
		for _, tagstr := range tagsets {
			tags = append(tags, strings.Split(tagstr, ",")...)
		}
	}

	regions = make([]evr.Symbol, 0)
	if regionstr, ok := params["regions"]; ok {
		for _, regionstr := range regionstr {
			for _, region := range strings.Split(regionstr, ",") {
				s := strings.Trim(region, " ")
				if s != "" {
					regions = append(regions, evr.ToSymbol(s))
				}
			}
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

	return discordId, password, tags, guildIds, regions, nil
}

func (p *EvrPipeline) authenticateBroadcaster(ctx context.Context, logger *zap.Logger, session *sessionWS, discordId, password string, guildIds []string, tags []string, serverID uint64) (string, string, error) {
	// Get the user id from the discord id
	userID, err := GetUserIDByDiscordID(ctx, p.db, discordId)
	if err != nil {
		return "", "", fmt.Errorf("failed to find user for Discord ID: %v", err)
	}
	// Authenticate the user
	userID, username, _, err := AuthenticateEmail(ctx, logger, session.pipeline.db, userID+"@"+p.placeholderEmail, password, "", false)
	if err != nil {
		return "", "", fmt.Errorf("password authentication failure")
	}
	p.logger.Info("Authenticated broadcaster", zap.String("operator_userID", userID), zap.String("operator_username", username))

	// The broadcaster is authenticated, set the userID as the broadcasterID and create a broadcaster session
	// Broadcasters are not linked to the login session, they have a generic username and only use the serverdb path.
	err = session.BroadcasterSession(uuid.FromStringOrNil(userID), "broadcaster:"+username, serverID)
	if err != nil {
		return "", "", fmt.Errorf("failed to create broadcaster session: %v", err)
	}

	return userID, username, nil
}

func broadcasterConfig(userId, sessionId string, serverId uint64, internalIP, externalIP net.IP, port uint16, regions []evr.Symbol, versionLock uint64, tags, features []string) *MatchBroadcaster {

	config := &MatchBroadcaster{
		SessionID:  sessionId,
		OperatorID: userId,
		ServerID:   serverId,
		Endpoint: evr.Endpoint{
			InternalIP: internalIP,
			ExternalIP: externalIP,
			Port:       port,
		},
		Regions:     regions,
		VersionLock: versionLock,
		GroupIDs:    make([]uuid.UUID, 0),
		Features:    features,

		Tags: make([]string, 0),
	}

	if len(tags) > 0 {
		config.Tags = tags
	}
	return config
}

func (p *EvrPipeline) getUserGroups(ctx context.Context, userID uuid.UUID, minState api.GroupUserList_GroupUser_State) ([]*api.UserGroupList_UserGroup, error) {
	cursor := ""
	groups := make([]*api.UserGroupList_UserGroup, 0)
	for {
		usergroups, cursor, err := p.runtimeModule.UserGroupsList(ctx, userID.String(), 100, nil, cursor)
		if err != nil {
			return nil, fmt.Errorf("failed to get user groups: %v", err)
		}
		for _, g := range usergroups {
			if api.GroupUserList_GroupUser_State(g.State.GetValue()) <= minState {
				groups = append(groups, g)
			}
		}
		if cursor == "" {
			break
		}
	}
	return groups, nil
}

func (p *EvrPipeline) getBroadcasterHostGroups(ctx context.Context, userId string, guildIDs []string) (groupIDs []uuid.UUID, err error) {

	// Get the user's guild memberships
	memberships, err := p.discordRegistry.GetGuildGroupMemberships(ctx, uuid.FromStringOrNil(userId), nil)
	if err != nil {
		return nil, fmt.Errorf("failed to get user's guild groups: %v", err)
	}

	if len(memberships) == 0 {
		return nil, fmt.Errorf("user is not a member of any guilds")
	}
	if len(guildIDs) == 0 {
		// User all of the user's guilds
		for _, g := range memberships {
			guildIDs = append(guildIDs, g.GuildGroup.GuildID())
		}
	}

	desired := make([]GuildGroupMembership, 0, len(guildIDs))
	for _, guildID := range guildIDs {
		for _, m := range memberships {
			if m.GuildGroup.GuildID() == guildID {
				desired = append(desired, m)
				break
			}
		}
	}

	userGroups, err := p.getUserGroups(ctx, uuid.FromStringOrNil(userId), api.GroupUserList_GroupUser_MEMBER)
	if err != nil {
		return nil, fmt.Errorf("failed to get user groups: %v", err)
	}

	userGroupIDs := make([]string, len(userGroups))
	for i, g := range userGroups {
		userGroupIDs[i] = g.GetGroup().GetId()
	}

	groupIDs = make([]uuid.UUID, 0, len(desired))
	for _, m := range desired {
		if m.isServerHost {
			groupIDs = append(groupIDs, m.GuildGroup.ID())
		}
	}

	return groupIDs, nil
}

func (p *EvrPipeline) newParkingMatch(logger *zap.Logger, session *sessionWS, config *MatchBroadcaster) error {
	data, err := json.Marshal(config)
	if err != nil {
		return fmt.Errorf("failed to marshal game server config: %v", err)
	}

	params := map[string]interface{}{
		"gameserver": string(data),
	}

	// Create the match
	matchIDStr, err := p.matchRegistry.CreateMatch(context.Background(), p.runtime.matchCreateFunction, EvrMatchmakerModule, params)
	if err != nil {
		return fmt.Errorf("failed to create parking match: %v", err)
	}

	matchID := MatchIDFromStringOrNil(matchIDStr)

	if err := UpdateGameServerBySessionID(p.runtimeModule, session.userID, session.id, matchID); err != nil {
		return fmt.Errorf("failed to update game server by session ID: %v", err)
	}

	found, allowed, _, reason, _, _ := p.matchRegistry.JoinAttempt(session.Context(), matchID.UUID(), matchID.Node(), session.UserID(), session.ID(), session.Username(), session.Expiry(), session.Vars(), session.ClientIP(), session.ClientPort(), p.node, nil)
	if !found {
		return fmt.Errorf("match not found: %s", matchID.String())
	}
	if !allowed {
		return fmt.Errorf("join not allowed: %s", reason)
	}

	// Trigger the MatchJoin event.
	stream := PresenceStream{Mode: StreamModeMatchAuthoritative, Subject: matchID.UUID(), Label: matchID.Node()}
	m := PresenceMeta{
		Username: session.Username(),
		Format:   session.Format(),
	}
	if success, _ := p.tracker.Track(session.Context(), session.ID(), stream, session.UserID(), m); success {
		// Kick the user from any other matches they may be part of.
		// WARNING This cannot be used during transition. It will kick the player from their current match.
		//p.tracker.UntrackLocalByModes(session.ID(), matchStreamModes, stream)
	}

	logger.Debug("New parking match", zap.String("mid", matchIDStr))

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

func (p *EvrPipeline) broadcasterSessionStarted(_ context.Context, logger *zap.Logger, _ *sessionWS, in evr.Message) error {
	request := in.(*evr.BroadcasterSessionStarted)

	logger.Info("Game session started", zap.String("mid", request.LobbySessionID.String()))

	return nil
}

// broadcasterSessionEnded is called when the broadcaster has ended the session.
func (p *EvrPipeline) broadcasterSessionEnded(ctx context.Context, logger *zap.Logger, session *sessionWS, in evr.Message) error {
	matchID, presence, err := GameServerBySessionID(p.runtimeModule, session.ID())
	if err != nil {
		logger.Warn("Failed to get broadcaster's match by session ID", zap.Error(err))
	}

	label, err := MatchLabelByID(ctx, p.runtimeModule, matchID)
	if err != nil {
		logger.Warn("Failed to get match label", zap.Error(err))
	}

	// Get the ending rosters
	team1 := make([]string, 0)
	team2 := make([]string, 0)

	for _, p := range label.Players {
		if p.Team == BlueTeam {
			team1 = append(team1, p.UserID)
		} else if p.Team == OrangeTeam {
			team2 = append(team2, p.UserID)
		}
	}
	p.sbmm.AddMatch(matchID, [][]string{team1, team2})

	if err := p.runtimeModule.StreamUserLeave(StreamModeMatchAuthoritative, matchID.UUID().String(), "", p.node, presence.GetUserId(), presence.GetSessionId()); err != nil {
		logger.Warn("Failed to leave match stream", zap.Error(err))
	}

	go func() {
		select {
		case <-session.Context().Done():
			return
		case <-time.After(10 * time.Second):
		}

		// Get the broadcaster registration from the stream.
		presence, err := p.runtimeModule.StreamUserGet(StreamModeGameServer, session.ID().String(), "", StreamLabelGameServerService, session.UserID().String(), session.ID().String())
		if err != nil {
			logger.Error("Failed to get broadcaster presence", zap.Error(err))
			session.Close("Failed to get broadcaster presence", runtime.PresenceReasonUnknown)
		}
		config := &MatchBroadcaster{}
		if err := json.Unmarshal([]byte(presence.GetStatus()), config); err != nil {
			logger.Error("Failed to unmarshal broadcaster presence", zap.Error(err))
			session.Close("Failed to get broadcaster presence", runtime.PresenceReasonUnknown)
		}

		if err := p.newParkingMatch(logger, session, config); err != nil {
			logger.Error("Failed to create new parking match", zap.Error(err))
			session.Close("Failed to get broadcaster presence", runtime.PresenceReasonUnknown)
		}
	}()
	return nil
}

func (p *EvrPipeline) broadcasterPlayerAccept(ctx context.Context, logger *zap.Logger, session *sessionWS, in evr.Message) error {
	request := in.(*evr.GameServerJoinAttempt)

	matchID, _, err := GameServerBySessionID(p.runtimeModule, session.ID())
	if err != nil {
		logger.Warn("Failed to get broadcaster's match by session ID", zap.Error(err))
	}
	baseLogger := logger.With(zap.String("mid", matchID.String()))

	accepted := make([]uuid.UUID, 0, len(request.EntrantIDs))
	rejected := make([]uuid.UUID, 0)

	for _, entrantID := range request.EntrantIDs {
		logger = baseLogger.With(zap.String("entrant_id", entrantID.String()))

		presence, err := PresenceByEntrantID(p.runtimeModule, matchID, entrantID)
		if err != nil || presence == nil {
			logger.Warn("Failed to get player session by ID", zap.Error(err))
			rejected = append(rejected, entrantID)
			continue
		}

		logger = logger.With(zap.String("entrant_uid", presence.GetUserId()))

		s := p.sessionRegistry.Get(uuid.FromStringOrNil(presence.GetSessionId()))
		if s == nil {
			logger.Warn("Failed to get session by ID")
			rejected = append(rejected, entrantID)
			continue
		}

		ctx := s.Context()
		for _, subject := range []uuid.UUID{presence.SessionID, presence.UserID, presence.EvrID.UUID()} {
			session.tracker.Update(ctx, s.ID(), PresenceStream{Mode: StreamModeService, Subject: subject, Label: StreamLabelMatchService}, s.UserID(), PresenceMeta{Format: s.Format(), Hidden: true, Status: matchID.String()})
		}

		// Trigger the MatchJoin event.
		stream := PresenceStream{Mode: StreamModeMatchAuthoritative, Subject: matchID.UUID(), Label: matchID.Node()}
		m := PresenceMeta{
			Username: s.Username(),
			Format:   s.Format(),
			Status:   presence.GetStatus(),
		}

		if success, _ := p.tracker.Track(s.Context(), s.ID(), stream, s.UserID(), m); success {
			// Kick the user from any other matches they may be part of.
			// WARNING This cannot be used during transition. It will kick the player from their current match.
			//p.tracker.UntrackLocalByModes(session.ID(), matchStreamModes, stream)
		}

		accepted = append(accepted, entrantID)
	}

	// Only include the message if there are players to accept or reject.
	messages := []evr.Message{}
	if len(accepted) > 0 {
		messages = append(messages, evr.NewGameServerJoinAllowed(accepted...))
	}

	if len(rejected) > 0 {
		messages = append(messages, evr.NewBroadcasterPlayersRejected(evr.PlayerRejectionReasonBadRequest, rejected...))
	}

	return session.SendEvr(messages...)
}

// broadcasterPlayerRemoved is called when a player has been removed from the match.
func (p *EvrPipeline) broadcasterPlayerRemoved(ctx context.Context, logger *zap.Logger, session *sessionWS, in evr.Message) error {
	message := in.(*evr.BroadcasterPlayerRemoved)
	matchID, _, err := GameServerBySessionID(p.runtimeModule, session.ID())
	if err != nil {
		logger.Warn("Failed to get broadcaster's match by session ID", zap.Error(err))
	}

	presence, err := PresenceByEntrantID(p.runtimeModule, matchID, message.EntrantID)
	if err != nil {
		if err != ErrorEntrantNotFound {
			logger.Warn("Failed to get player session by ID", zap.Error(err))
		}
		return nil
	}
	if presence != nil {
		// Trigger MatchLeave.
		if err := p.runtimeModule.StreamUserLeave(StreamModeMatchAuthoritative, matchID.UUID().String(), "", matchID.Node(), presence.GetUserId(), presence.GetSessionId()); err != nil {
			logger.Warn("Failed to leave match stream", zap.Error(err))
		}

		if err := p.runtimeModule.StreamUserLeave(StreamModeEntrant, matchID.UUID().String(), message.EntrantID.String(), matchID.Node(), presence.GetUserId(), presence.GetSessionId()); err != nil {
			logger.Warn("Failed to leave entrant session stream", zap.Error(err))
		}
	}

	return nil
}

func (p *EvrPipeline) broadcasterPlayerSessionsLocked(ctx context.Context, logger *zap.Logger, session *sessionWS, in evr.Message) error {
	matchID, _, err := GameServerBySessionID(p.runtimeModule, session.ID())
	if err != nil {
		logger.Warn("Failed to get broadcaster's match by session ID", zap.Error(err))
	}
	signal := NewEvrSignal(session.userID.String(), SignalLockSession, nil)
	if _, err := p.matchRegistry.Signal(ctx, matchID.String(), signal.String()); err != nil {
		logger.Warn("Failed to signal match", zap.Error(err))
	}
	return nil
}

func (p *EvrPipeline) broadcasterPlayerSessionsUnlocked(ctx context.Context, logger *zap.Logger, session *sessionWS, in evr.Message) error {
	matchID, _, err := GameServerBySessionID(p.runtimeModule, session.ID())
	if err != nil {
		logger.Warn("Failed to get broadcaster's match by session ID", zap.Error(err))
	}
	signal := NewEvrSignal(session.userID.String(), SignalUnlockSession, nil)
	if _, err := p.matchRegistry.Signal(ctx, matchID.String(), signal.String()); err != nil {
		logger.Warn("Failed to signal match", zap.Error(err))
	}
	return nil
}

func UpdateGameServerBySessionID(nk runtime.NakamaModule, userID uuid.UUID, sessionID uuid.UUID, matchID MatchID) error {
	return nk.StreamUserUpdate(StreamModeGameServer, sessionID.String(), "", StreamLabelMatchService, userID.String(), sessionID.String(), true, false, matchID.String())
}

func GameServerBySessionID(nk runtime.NakamaModule, sessionID uuid.UUID) (MatchID, runtime.Presence, error) {

	presences, err := nk.StreamUserList(StreamModeGameServer, sessionID.String(), "", StreamLabelMatchService, true, true)
	if err != nil {
		return MatchID{}, nil, err
	}

	presence := presences[0]

	matchID, err := MatchIDFromString(presence.GetStatus())
	if err != nil {
		return MatchID{}, nil, err
	}
	return matchID, presence, nil
}
