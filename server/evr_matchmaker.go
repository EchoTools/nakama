package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/gofrs/uuid/v5"
	"github.com/heroiclabs/nakama-common/api"
	"github.com/heroiclabs/nakama-common/rtapi"
	"github.com/heroiclabs/nakama-common/runtime"
	"github.com/heroiclabs/nakama/v3/server/evr"
	"github.com/samber/lo"
	"go.uber.org/zap"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const (
	MatchJoinGracePeriod = 5 * time.Second
)

var (
	ErrDiscordLinkNotFound = errors.New("discord link not found")
)

func (p *EvrPipeline) ListUnassignedLobbies(ctx context.Context, session *sessionWS, ml *EvrMatchState) ([]*EvrMatchState, error) {
	// TODO Move this into the matchmaking registry
	qparts := make([]string, 0, 10)

	// MUST be an unassigned lobby
	qparts = append(qparts, LobbyType(evr.UnassignedLobby).Query(Must, 0))

	// MUST be one of the accessible channels if provided
	if len(ml.BroadcasterChannels) > 0 {
		// Add the channels to the query
		qparts = append(qparts, HostedChannels(ml.BroadcasterChannels).Query(Must, 0))
	}

	// SHOULD be in the specify region
	if ml.Region != evr.Symbol(0) {
		qparts = append(qparts, Region(ml.Region).Query(Should, 0))
	}

	// TODO FIXME Add version lock and appid
	query := strings.Join(qparts, " ")

	// setup a channel on the registry that the ping cycle can use to signal that the pings are finished.
	limit := 100
	minSize, maxSize := 1, 1 // Only the 1 broadcaster should be there.
	matches, err := listMatches(ctx, p, limit, minSize, maxSize, query)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "Failed to find matches: %v", err)
	}

	// If no servers are available, return immediately.
	if len(matches) == 0 {
		return nil, status.Errorf(codes.NotFound, "No available servers")
	}

	// Create a slice containing the matches' labels
	labels := make([]*EvrMatchState, 0, len(matches))
	for _, match := range matches {
		label := &EvrMatchState{}
		if err := json.Unmarshal([]byte(match.GetLabel().GetValue()), label); err != nil {
			return nil, status.Errorf(codes.Internal, "Failed to unmarshal match label: %v", err)
		}
		labels = append(labels, label)
	}

	return labels, nil
}

// Backfill returns a list of matches that the player can backfill
func (p *EvrPipeline) Backfill(ctx context.Context, session *sessionWS, msession *MatchmakingSession) ([]*EvrMatchState, error) { // Create a new matching session
	// TODO Move this into the matchmaking registry
	// TODO Add a goroutine to look for matches that:
	// Are short 1 or more players
	// Have been short a player for X amount of time (~30 seconds?)
	// afterwhich, the match is considered a backfill candidate and the goroutine
	// Will open a matchmaking ticket. Any players that have a (backfill) ticket that matches
	// The match will be added to the match.

	var err error
	logger := session.logger

	labels := make([]*EvrMatchState, 0)

	if msession.Label.LobbyType != PublicLobby {
		// Do not backfill for private lobbies
		return labels, status.Errorf(codes.InvalidArgument, "Cannot backfill private lobbies")
	}

	// Search for existing matches
	if labels, err = p.MatchSearch(ctx, logger, session, msession.Label); err != nil {
		return nil, status.Errorf(codes.Internal, "Failed to search for matches: %v", err)
	}

	// Filter/sort the results
	if labels, _, err = p.MatchFilter(ctx, session, msession, labels); err != nil {
		return nil, status.Errorf(codes.Internal, "Failed to filter matches: %v", err)
	}

	return labels, nil
}

// Matchmake attempts to find/create a match for the user using the nakama matchmaker
func (p *EvrPipeline) AddMatchmakingTicket(ctx context.Context, session *sessionWS, matchLogger *zap.Logger, msession *MatchmakingSession) (string, error) {
	// TODO Move this into the matchmaking registry

	// Get a list of the unassigned lobbies
	broadcasters, err := p.ListUnassignedLobbies(ctx, session, msession.Label)
	if err != nil {
		return "", err
	}

	// Get the endpoints for the available broadcasters
	endpoints := make([]*evr.Endpoint, len(broadcasters))
	for _, match := range broadcasters {
		endpoints = append(endpoints, &match.Endpoint)
	}

	// Ping endpoints
	result, err := p.PingEndpoints(ctx, session, msession, endpoints)
	if err != nil {
		return "", err
	}

	// Create a map of endpoint Ids to latencies
	latencies := make(map[string]time.Duration, len(result))
	for _, r := range result {
		latencies[r.Endpoint.ID()] = r.RTT
	}

	// Round all latencies to the closest 10 milliseconds
	for k, v := range latencies {
		latencies[k] = mroundRTT(v, 10*time.Millisecond)
	}

	// Sort by latency
	sort.SliceStable(broadcasters, func(i, j int) bool {
		return latencies[broadcasters[i].Endpoint.ID()] < latencies[broadcasters[j].Endpoint.ID()]
	})
	query, stringProps, numericProps, err := p.BuildMatchmakingQuery(ctx, broadcasters, latencies, session, msession)
	if err != nil {
		return "", status.Errorf(codes.Internal, "Failed to build matchmaking query: %v", err)
	}

	sessionID := session.ID()
	partyID := ""
	minCount := 1
	maxCount := 8
	countMultiple := 2

	presences := []*MatchmakerPresence{
		{
			UserId:    session.UserID().String(),
			SessionId: session.ID().String(),
			Username:  session.Username(),
			Node:      p.node,
			SessionID: sessionID,
		},
	}

	ticket, _, err := session.matchmaker.Add(ctx, presences, sessionID.String(), partyID, query, minCount, maxCount, countMultiple, stringProps, numericProps)
	if err != nil {
		return "", fmt.Errorf("failed to add to matchmaker: %v", err)
	}

	return ticket, nil
}

func (p *EvrPipeline) BuildMatchmakingQuery(ctx context.Context, broadcasters []*EvrMatchState, latencies map[string]time.Duration, session *sessionWS, msession *MatchmakingSession) (string, map[string]string, map[string]float64, error) {
	// Create the properties maps
	stringProps := make(map[string]string)
	numericProps := make(map[string]float64)
	qparts := make([]string, 0, 10)

	// Add a property of the external IP's RTT
	for i, e := range broadcasters {
		latency := latencies[e.Endpoint.ID()]
		// Turn the IP into a hex string like 127.0.0.1 -> 7f000001
		ip := fmt.Sprintf("%02x%02x%02x%02x", e.Endpoint.ExternalIP[0], e.Endpoint.ExternalIP[1], e.Endpoint.ExternalIP[2], e.Endpoint.ExternalIP[3])
		// Add the property
		numericProps[ip] = float64(latency.Milliseconds())

		// Add properties where the desired

		if i <= 15 || latency < 120*time.Millisecond {
			b := 0
			switch {
			case latency < 25:
				b = 10
			case latency < 40:
				b = 7
			case latency < 60:
				b = 5
			case latency < 80:
				b = 2
			}
			// Match other players properties with the
			p := fmt.Sprintf("properties.%s:<=%d^%d", ip, latency+15, b)
			qparts = append(qparts, p)
		}
	}

	groups, err := p.discordRegistry.GetGuildGroups(ctx, session.UserID().String())
	if err != nil {
		return "", nil, nil, status.Errorf(codes.Internal, "Failed to get guild groups: %v", err)
	}

	for _, g := range groups {

		// Add the properties
		// Strip out teh hiphens form the UUID and add it ot teh porties and query
		s := strings.ReplaceAll(g.Id, "-", "")
		stringProps[s] = "T"

		// If this is the user's current channel, then give it a +3 boost
		if g.Id == msession.Label.Channel.String() {
			qparts = append(qparts, fmt.Sprintf("properties.%s:T^3", s, 3))
		} else {
			qparts = append(qparts, fmt.Sprintf("properties.%s:T"))
		}
	}
	query := strings.Join(qparts, " ")
	// TODO Promote friends

	// TODO Avoid ghosted

	return query, stringProps, numericProps, nil
}

// Wrapper for the matchRegistry.ListMatches function.
func listMatches(ctx context.Context, p *EvrPipeline, limit int, minSize int, maxSize int, query string) ([]*api.Match, error) {
	return p.runtimeModule.MatchList(ctx, limit, true, "", &minSize, &maxSize, query)
}

func buildMatchQueryFromLabel(ml *EvrMatchState) string {
	var boost int = 0 // Default booster

	qparts := []string{
		// MUST be a open lobby
		OpenLobby.Query(Must, boost),
		// MUST be the same lobby type
		LobbyType(ml.LobbyType).Query(Must, boost),
		// MUST be the same mode
		GameMode(ml.Mode).Query(Must, boost),
		// MUST have room for this party
		fmt.Sprintf("+label.size:<=%d", ml.Size),
	}

	// MUST not much into the same lobby
	if ml.MatchId != uuid.Nil {
		qparts = append(qparts, MatchId(ml.MatchId).Query(MustNot, boost))
	}

	// MUST be a broadcaster on a channel the user has access to
	if len(ml.BroadcasterChannels) != 0 {
		qparts = append(qparts, Channels(ml.BroadcasterChannels).Query(Must, 0))
	}

	// SHOULD Add the current channel as a high boost SHOULD
	if ml.Channel != uuid.Nil {
		qparts = append(qparts, Channel(ml.Channel).Query(Should, 3))
	}

	// Add the region as a SHOULD
	if ml.Region != evr.Symbol(0) {
		qparts = append(qparts, Region(ml.Region).Query(Should, 2))
	}
	// Setup the query and logger
	return strings.Join(qparts, " ")
}

// MatchSearch attempts to find/create a match for the user.
func (p *EvrPipeline) MatchSearch(ctx context.Context, logger *zap.Logger, session *sessionWS, ml *EvrMatchState) ([]*EvrMatchState, error) {
	// TODO FIXME Handle spectators separately from players.
	// TODO Move this into the matchmaking registry
	query := buildMatchQueryFromLabel(ml)

	// Basic search defaults
	const (
		minSize = 0
		maxSize = MaxMatchSize + 1 // +1 for the broadcaster
		limit   = 50
	)

	logger = logger.With(zap.String("query", query), zap.Any("label", ml))

	// Search for possible matches
	matches, err := listMatches(ctx, p, limit, minSize, maxSize, query)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "Failed to find matches: %v", err)
	}

	// Create a label slice of the matches
	labels := make([]*EvrMatchState, len(matches))
	for i, match := range matches {
		label := &EvrMatchState{}
		if err := json.Unmarshal([]byte(match.GetLabel().GetValue()), label); err != nil {
			logger.Error("Error unmarshalling match label", zap.Error(err))
			continue
		}
		labels[i] = label
	}

	return labels, nil
}

// mroundRTT rounds the rtt to the nearest modulus
func mroundRTT(rtt time.Duration, modulus time.Duration) time.Duration {
	r := float64(rtt) / float64(modulus)
	return time.Duration(math.Round(r)) * modulus
}

// RTTweightedPopulationCmp compares two RTTs and populations
func RTTweightedPopulationCmp(i, j time.Duration, o, p int) bool {
	// Sort by if over or under 90ms
	if i > 90*time.Millisecond && j < 90*time.Millisecond {
		return true
	}
	if i < 90*time.Millisecond && j > 90*time.Millisecond {
		return false
	}
	// Sort by Population
	if o > p {
		return true
	}
	if o < p {
		return false
	}
	// If all else equal, sort by rtt
	return i < j
}

// PopulationCmp compares two populations
func PopulationCmp(o, p int, i, j time.Duration) bool {
	if o == p {
		// If all else equal, sort by rtt
		return i < j
	}
	return o > p
}

// MatchFilter pings the matches and filters the matches by the user's cached latencies.
func (p *EvrPipeline) MatchFilter(ctx context.Context, session *sessionWS, msession *MatchmakingSession, labels []*EvrMatchState) (filtered []*EvrMatchState, rtts []time.Duration, err error) {
	// TODO Move this into the matchmaking registry

	// Create a slice for the rtts of the filtered matches
	rtts = make([]time.Duration, 0, len(labels))

	// If there are no matches, return
	if len(labels) == 0 {
		return labels, rtts, nil
	}

	// Only ping the unique endpoints
	endpoints := make(map[string]*evr.Endpoint, len(labels))
	for _, label := range labels {
		endpoints[label.Endpoint.ID()] = &label.Endpoint
	}

	// Ping the endpoints
	result, err := p.PingEndpoints(ctx, session, msession, lo.Values(endpoints))
	if err != nil {
		return nil, nil, err
	}

	modulus := 10 * time.Millisecond
	// Create a map of endpoint Ids to endpointRTTs, rounding the endpointRTTs to the nearest 10ms
	endpointRTTs := make(map[string]time.Duration, len(result))
	for _, r := range result {
		endpointRTTs[r.Endpoint.ID()] = mroundRTT(r.RTT, modulus)
	}

	type labelData struct {
		Id   string
		Size int
		RTT  time.Duration
	}
	// Create a map of endpoint Ids to sizes and latencies
	datas := make([]labelData, 0, len(labels))
	for _, label := range labels {
		id := label.Endpoint.ID()
		rtt := endpointRTTs[id]
		// If the rtt is 0 or over 270ms, skip the match
		if rtt == 0 || rtt > 270*time.Millisecond {
			continue
		}
		datas = append(datas, labelData{id, label.Size, rtt})
	}

	// Sort the matches
	switch msession.Label.Mode {
	case evr.ModeArenaPublic:
		// Split Arena matches into two groups: over and under 90ms
		// Sort by if over or under 90ms, then population, then latency
		sort.SliceStable(datas, func(i, j int) bool {
			return RTTweightedPopulationCmp(datas[i].RTT, datas[j].RTT, datas[i].Size, datas[j].Size)
		})

	case evr.ModeCombatPublic:
		// Sort Combat matches by population, then latency
		fallthrough

	case evr.ModeSocialPublic:
		fallthrough

	default:
		sort.SliceStable(datas, func(i, j int) bool {
			return PopulationCmp(datas[i].Size, datas[j].Size, datas[i].RTT, datas[j].RTT)
		})
	}

	// Create a slice of the filtered matches
	filtered = make([]*EvrMatchState, 0, len(datas))
	for _, data := range datas {
		for _, label := range labels {
			if label.Endpoint.ID() == data.Id {
				filtered = append(filtered, label)
				rtts = append(rtts, data.RTT)
				break
			}
		}
	}
	return filtered, rtts, nil
}

// MatchCreate creates a match on an available unassigned broadcaster using the given label
func (p *EvrPipeline) MatchCreate(ctx context.Context, session *sessionWS, msession *MatchmakingSession, label *EvrMatchState) (match *EvrMatchState, err error) {
	label.MaxSize = MaxMatchSize
	// TODO Move this into the matchmaking registry
	// Create a new match
	matches, err := p.ListUnassignedLobbies(ctx, session, label)
	if err != nil {
		return nil, err
	}

	// Filter/sort the results
	matches, _, err = p.MatchFilter(ctx, session, msession, matches)
	if err != nil {
		return nil, fmt.Errorf("failed to filter matches: %v", err)
	}

	// Join the lowest rtt match
	match = matches[0]

	// Load the level.
	matchId := fmt.Sprintf("%s.%s", match.MatchId, p.node)

	// Instruct the server to load the level
	_, err = SignalMatch(ctx, p.matchRegistry, matchId, SignalStartSession, label)
	if err != nil {
		return nil, fmt.Errorf("failed to load level: %v", err)
	}
	// Return the newly active match.
	return match, nil
}

// JoinEvrMatch allows a player to join a match.
func (p *EvrPipeline) JoinEvrMatch(ctx context.Context, session *sessionWS, matchID string, channel uuid.UUID, teamIndex int, gracePeriod time.Duration) error {
	// Append the node to the matchID if it doesn't already contain one.
	if !strings.Contains(matchID, ".") {
		matchID = fmt.Sprintf("%s.%s", matchID, p.node)
	}

	// Retrieve the evrID from the context.
	evrID, ok := ctx.Value(ctxEvrIDKey{}).(evr.EvrId)
	if !ok {
		return errors.New("evr id not found in context")
	}

	// Fetch the account details.
	account, err := GetAccount(ctx, session.logger, p.db, p.statusRegistry, session.UserID())
	if err != nil {
		return fmt.Errorf("failed to get account: %w", err)
	}

	// Extract the display name.
	displayName := account.GetUser().GetDisplayName()

	// Prepare the player session metadata.
	playermeta := JoinMeta{
		TeamIndex:     int16(teamIndex),
		PlayerSession: uuid.Must(uuid.NewV4()),
		EvrId:         evrID,
		DisplayName:   displayName,
	}

	// Marshal the player metadata into JSON.
	jsonMeta, err := json.Marshal(playermeta)
	if err != nil {
		return fmt.Errorf("failed to marshal player meta: %w", err)
	}

	// Construct the match join request.
	msg := &rtapi.Envelope{
		Message: &rtapi.Envelope_MatchJoin{
			MatchJoin: &rtapi.MatchJoin{
				Id:       &rtapi.MatchJoin_MatchId{MatchId: matchID},
				Metadata: map[string]string{"playermeta": string(jsonMeta)},
			},
		},
	}

	// Wait for the grace period before attempting to join the match.
	select {
	case <-time.After(gracePeriod):
	case <-ctx.Done():
		return ErrMatchmakingCancelled
	}

	// Send the join request.
	if ok := session.pipeline.ProcessRequest(session.logger, session, msg); !ok {
		return errors.New("failed to send join request")
	}

	return nil
}

// PingEndpoints pings the endpoints and returns the latencies.
func (p *EvrPipeline) PingEndpoints(ctx context.Context, session *sessionWS, msession *MatchmakingSession, endpoints []*evr.Endpoint) ([]*EndpointWithLatency, error) {
	if len(endpoints) == 0 {
		return nil, nil
	}

	candidates := p.matchmakingRegistry.GetPingCandidates(session.UserID(), endpoints)
	if len(candidates) > 0 {
		if err := p.sendPingRequest(session, candidates); err != nil {
			return nil, err
		}

		if err := p.waitForPingCompletion(msession); err != nil {
			return nil, err
		}
	}

	return p.getEndpointLatencies(session, endpoints), nil
}

// sendPingRequest sends a ping request to the given candidates.
func (p *EvrPipeline) sendPingRequest(session *sessionWS, candidates []*evr.Endpoint) error {
	messages := []evr.Message{
		evr.NewLobbyPingRequest(275, candidates),
		evr.NewSTcpConnectionUnrequireEvent(),
	}

	if err := session.SendEvr(messages); err != nil {
		return err
	}

	session.logger.Debug("Sent ping request", zap.Any("candidates", candidates))
	return nil
}

// waitForPingCompletion waits for the ping to complete or for an error to occur.
func (p *EvrPipeline) waitForPingCompletion(msession *MatchmakingSession) error {
	select {
	case <-time.After(2 * time.Second):
		return ErrMatchmakingPingTimeout
	case <-msession.Ctx.Done():
		return ErrMatchmakingCancelled
	case err := <-msession.PingCompleteCh:
		return err
	}
}

// getEndpointLatencies returns the latencies for the given endpoints.
func (p *EvrPipeline) getEndpointLatencies(session *sessionWS, endpoints []*evr.Endpoint) []*EndpointWithLatency {
	endpointRTTs := p.matchmakingRegistry.GetLatencies(session.UserID(), endpoints)

	results := make([]*EndpointWithLatency, 0, len(endpoints))
	for _, e := range endpoints {
		if e != nil {
			if l, ok := endpointRTTs[e.ID()]; ok {
				results = append(results, l)
			}
		}
	}

	return results
}

func (p *EvrPipeline) checkSuspensionStatus(ctx context.Context, logger *zap.Logger, userID string, channel uuid.UUID) (statuses []*SuspensionStatus, err error) {
	// Get the guild group metadata
	md, err := p.discordRegistry.GetGuildGroupMetadata(ctx, channel.String())
	if err != nil {
		return nil, status.Errorf(codes.Internal, "Failed to get guild group metadata: %v", err)
	}
	if md == nil {
		return nil, status.Errorf(codes.Internal, "Failed to get guild group metadata: %v", err)
	}

	// Check if the channel has suspension roles
	if len(md.SuspensionRoles) == 0 {
		// The channel has no suspension roles
		return nil, nil
	}

	// Get the user's discordId
	discordId, err := p.discordRegistry.GetDiscordIdByUserId(ctx, userID)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "Failed to get discord id: %v", err)

	}

	// Get the guild member
	member, err := p.discordRegistry.GetGuildMember(ctx, md.GuildId, discordId)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "Failed to get guild member: %v", err)
	}

	// Check if the members roles contain any of the suspension roles
	if len(lo.Intersect(member.Roles, md.SuspensionRoles)) == 0 {
		// The user is not suspended from this channel
		return nil, nil
	}

	// Check if the user has a detailed suspension status in storage
	keys := make([]string, 0, 2)
	// List all the storage objects in the SuspensionStatusCollection for this user
	ids, _, err := p.runtimeModule.StorageList(ctx, uuid.Nil.String(), userID, SuspensionStatusCollection, 1000, "")
	if err != nil {
		return nil, status.Errorf(codes.Internal, "Failed to list suspension status: %v", err)
	}
	if len(ids) == 0 {
		// Get the guild name and Id
		guild, err := p.discordRegistry.GetGuild(ctx, md.GuildId)
		if err != nil {
			return nil, status.Errorf(codes.Internal, "Failed to get guild: %v", err)
		}
		// Return the basic suspension status
		return []*SuspensionStatus{
			{
				GuildId:       guild.ID,
				GuildName:     guild.Name,
				UserId:        userID,
				UserDiscordId: discordId,
				Reason:        fmt.Sprintf("You are currently suspended from %s.", guild.Name),
			},
		}, nil
	}

	for _, id := range ids {
		keys = append(keys, id.Key)
	}

	// Construct the read operations
	ops := make([]*runtime.StorageRead, 0, len(keys))
	for _, id := range keys {
		ops = append(ops, &runtime.StorageRead{
			Collection: SuspensionStatusCollection,
			Key:        id,
			UserID:     userID,
		})
	}
	objs, err := p.runtimeModule.StorageRead(ctx, ops)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "Failed to read suspension status: %v", err)
	}
	// If no suspension status was found, return the basic suspension status
	if len(objs) == 0 {
		// Get the guild name and Id
		guild, err := p.discordRegistry.GetGuild(ctx, md.GuildId)
		if err != nil {
			return nil, status.Errorf(codes.Internal, "Failed to get guild: %v", err)
		}

		// Return the basic suspension status
		return []*SuspensionStatus{
			{
				GuildId:       guild.ID,
				GuildName:     guild.Name,
				UserId:        userID,
				UserDiscordId: discordId,
				Reason:        "You are suspended from this channel.\nContact a moderator for more information.",
			},
		}, nil
	}

	// Check the status to see if it's expired.
	suspensions := make([]*SuspensionStatus, 0, len(objs))
	// Suspension status was found. Check its expiry
	for _, obj := range objs {
		// Unmarshal the suspension status
		suspension := &SuspensionStatus{}
		if err := json.Unmarshal([]byte(obj.Value), suspension); err != nil {
			return nil, status.Errorf(codes.Internal, "Failed to unmarshal suspension status: %v", err)
		}
		// Check if the suspension has expired
		if suspension.Expiry.After(time.Now()) {
			// The user is suspended from this lobby
			suspensions = append(suspensions, suspension)
		} else {
			// The suspension has expired, delete the object
			if err := p.runtimeModule.StorageDelete(ctx, []*runtime.StorageDelete{
				{
					Collection: SuspensionStatusCollection,
					Key:        obj.Key,
					UserID:     userID,
				},
			}); err != nil {
				logger.Error("Failed to delete suspension status", zap.Error(err))
			}
		}
	}
	return suspensions, nil
}
