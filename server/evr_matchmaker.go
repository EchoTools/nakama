package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/gofrs/uuid/v5"
	"github.com/heroiclabs/nakama-common/api"
	"github.com/heroiclabs/nakama-common/runtime"
	"github.com/heroiclabs/nakama/v3/server/evr"
	"go.uber.org/zap"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const (
	MatchJoinGracePeriod = 3 * time.Second
)

var (
	ErrDiscordLinkNotFound = errors.New("discord link not found")
)

func (p *EvrPipeline) ListUnassignedLobbies(ctx context.Context, session *sessionWS, ml *EvrMatchState) ([]*EvrMatchState, error) {

	// TODO Move this into the matchmaking registry
	qparts := make([]string, 0, 10)

	// MUST be an unassigned lobby
	qparts = append(qparts, LobbyType(evr.UnassignedLobby).Query(Must, 0))

	// MUST be one of the accessible channels (if provided)
	if len(ml.Broadcaster.GroupIDs) > 0 {
		// Add the channels to the query
		qparts = append(qparts, HostedChannels(ml.Broadcaster.GroupIDs).Query(Must, 0))
	}

	// Add each hosted channel as a SHOULD, with decreasing boost

	for i, channel := range ml.Broadcaster.GroupIDs {
		qparts = append(qparts, Channel(channel).Query(Should, len(ml.Broadcaster.GroupIDs)-i))
	}

	// Add the regions in descending order of priority
	for i, region := range ml.Broadcaster.Regions {
		qparts = append(qparts, Region(region).Query(Should, len(ml.Broadcaster.Regions)-i))
	}

	// Add tag query for prioritizing certain modes to specific hosts
	// remove dots from the mode string to avoid issues with the query parser
	s := strings.NewReplacer(".", "").Replace(ml.Mode.String())
	qparts = append(qparts, "label.broadcaster.tags:priority_mode_"+s+"^10")

	// Add the user's region request to the query
	qparts = append(qparts, Regions(ml.Broadcaster.Regions).Query(Must, 0))

	// SHOULD/MUST have the same features
	for _, f := range ml.RequiredFeatures {
		qparts = append(qparts, "+label.broadcaster.features:"+f)
	}

	for _, f := range ml.Broadcaster.Features {
		qparts = append(qparts, "label.broadcaster.features:"+f)
	}

	// TODO FIXME Add version lock and appid
	query := strings.Join(qparts, " ")

	// Load the matchmaking config and add the user's config to the query

	gconfig, err := LoadMatchmakingSettings(ctx, p.runtimeModule, SystemUserID)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "Failed to load global matchmaking config: %v", err)
	}

	config, err := LoadMatchmakingSettings(ctx, p.runtimeModule, session.UserID().String())
	if err != nil {
		return nil, status.Errorf(codes.Internal, "Failed to load matchmaking config: %v", err)
	}
	query = fmt.Sprintf("%s %s %s", query, gconfig.CreateQueryAddon, config.CreateQueryAddon)

	limit := 100
	minSize, maxSize := 1, 1 // Only the 1 broadcaster should be there.
	session.logger.Debug("Listing unassigned lobbies", zap.String("query", query))
	matches, err := listMatches(ctx, p, limit, minSize, maxSize, query)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "Failed to find matches: %v", err)
	}

	// If no servers are available, return immediately.
	if len(matches) == 0 {
		return nil, ErrMatchmakingNoAvailableServers
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

type LabelLatencies struct {
	label   *EvrMatchState
	latency *LatencyMetric
}

// Backfill returns a match that the player can backfill
func (p *EvrPipeline) Backfill(ctx context.Context, session *sessionWS, msession *MatchmakingSession, minCount int, limit int) ([]*EvrMatchState, string, error) {

	logger := msession.Logger
	candidates, query, err := p.matchmakingRegistry.listUnfilledLobbies(ctx, logger, msession.Label, minCount)
	if err != nil {
		return nil, query, err
	}
	if len(candidates) == 0 {
		return nil, query, nil
	}

	endpoints := make([]evr.Endpoint, 0, len(candidates))
	for _, label := range candidates {
		endpoints = append(endpoints, label.Broadcaster.Endpoint)
	}

	// Ping the endpoints
	latencies, err := p.GetLatencyMetricByEndpoint(ctx, msession, endpoints, true)
	if err != nil {
		return nil, query, err
	}

	labelLatencies := make([]LabelLatencies, 0, len(candidates))
	for _, label := range candidates {
		for _, latency := range latencies {
			if label.Broadcaster.Endpoint.GetExternalIP() == latency.Endpoint.GetExternalIP() {
				labelLatencies = append(labelLatencies, LabelLatencies{label, &latency})
				break
			}
		}
	}

	var sortFn func(i, j time.Duration, o, p int) bool
	switch msession.Label.Mode {
	case evr.ModeArenaPublic:
		sortFn = RTTweightedPopulationCmp
	default:
		sortFn = PopulationCmp
	}

	sort.SliceStable(labelLatencies, func(i, j int) bool {
		return sortFn(labelLatencies[i].latency.RTT, labelLatencies[j].latency.RTT, labelLatencies[i].label.PlayerCount, labelLatencies[j].label.PlayerCount)
	})

	options := make([]*EvrMatchState, 0, limit)
	// Select the first match
	for _, label := range candidates {
		// Check that the match is not full
		logger = logger.With(zap.String("mid", label.ID.String()))

		match, _, err := p.matchRegistry.GetMatch(ctx, label.ID.String())
		if err != nil {
			logger.Debug("Failed to get match: %s", zap.Error(err))
			continue
		}

		isPlayer := msession.Label.TeamIndex != Spectator && msession.Label.TeamIndex != Moderator
		if ok, err := checkMatchForBackfill(match, minCount, isPlayer); err != nil {
			logger.Debug("Failed to check match for backfill", zap.Error(err))
			continue
		} else if !ok {
			continue
		}
		options = append(options, label)
		if len(options) >= limit {
			break
		}
	}

	return options, query, nil
}

func checkMatchForBackfill(match *api.Match, minCount int, isPlayer bool) (ok bool, err error) {

	if match == nil {
		return false, fmt.Errorf("match is nil")
	}

	label := &EvrMatchState{}
	// Extract the latest label
	if err := json.Unmarshal([]byte(match.GetLabel().GetValue()), label); err != nil {
		return false, fmt.Errorf("failed to get match label: %w", err)
	}

	if match.GetSize()-1 >= int32(label.MaxSize) { // -1 for the broadcaster
		return false, nil
	}

	if !isPlayer {
		return true, nil
	}

	if (label.PlayerLimit - label.PlayerCount) < minCount {
		return false, nil
	}

	/*
		if label.Mode == evr.ModeCombatPublic && (label.PlayerCount+minCount)%2 != 0 {
			return false, nil
		}
	*/
	return true, nil
}

// TODO FIXME Create a broadcaster registry

type BroadcasterLatencies struct {
	Endpoint evr.Endpoint
	Latency  time.Duration
}

// Matchmake attempts to find/create a match for the user using the nakama matchmaker
func (p *EvrPipeline) MatchMake(session *sessionWS, msession *MatchmakingSession) (ticket string, err error) {
	// TODO Move this into the matchmaking registry
	ctx := msession.Context()
	// TODO FIXME Add a custom matcher for broadcaster matching
	// Get a list of all the broadcasters

	// Ping endpoints
	endpoints := make([]evr.Endpoint, 0, 100)
	p.broadcasterRegistrationBySession.Range(func(_ string, b *MatchBroadcaster) bool {
		endpoints = append(endpoints, b.Endpoint)
		return true
	})

	allRTTs, err := p.GetLatencyMetricByEndpoint(ctx, msession, endpoints, true)
	if err != nil {
		return "", err
	}

	query, stringProps, numericProps, err := msession.BuildQuery(allRTTs)
	if err != nil {
		return "", status.Errorf(codes.Internal, "Failed to build matchmaking query: %v", err)
	}

	// Add the user to the matchmaker
	sessionID := session.ID()

	userID := session.UserID().String()
	presences := []*MatchmakerPresence{
		{
			UserId:    userID,
			SessionId: session.ID().String(),
			Username:  session.Username(),
			Node:      p.node,
			SessionID: sessionID,
		},
	}
	// Load the global matchmaking config
	gconfig, err := LoadMatchmakingSettings(ctx, p.runtimeModule, SystemUserID)
	if err != nil {
		return "", status.Errorf(codes.Internal, "Failed to load global matchmaking config: %v", err)
	}

	// Load the user's matchmaking config
	config, err := LoadMatchmakingSettings(ctx, p.runtimeModule, userID)
	if err != nil {
		return "", status.Errorf(codes.Internal, "Failed to load matchmaking config: %v", err)
	}
	// Merge the user's config with the global config
	query = fmt.Sprintf("%s %s %s", query, gconfig.BackfillQueryAddon, config.BackfillQueryAddon)

	// Get the EVR ID from the context
	evrID, ok := ctx.Value(ctxEvrIDKey{}).(evr.EvrId)
	if !ok {
		return "", status.Errorf(codes.Internal, "EVR ID not found in context")
	}
	stringProps["evr_id"] = evrID.Token()

	minCount := 2
	maxCount := 8
	if msession.Label.Mode == evr.ModeCombatPublic {
		maxCount = 10
	} else {
		maxCount = 8
	}
	countMultiple := 2

	subcontext := uuid.NewV5(uuid.Nil, "matchmaking")
	// Create a status presence for the user
	ok = session.tracker.TrackMulti(ctx, session.id, []*TrackerOp{
		// EVR packet data stream for the login session by user ID, and service ID, with EVR ID
		{
			Stream: PresenceStream{Mode: StreamModeEvr, Subject: session.id, Subcontext: subcontext, Label: query},
			Meta:   PresenceMeta{Format: session.format, Username: session.Username(), Hidden: true},
		},
	}, session.userID)
	if !ok {
		return "", status.Errorf(codes.Internal, "Failed to track user: %v", err)
	}
	tags := map[string]string{
		"type":  msession.Label.LobbyType.String(),
		"mode":  msession.Label.Mode.String(),
		"level": msession.Label.Level.String(),
	}

	p.metrics.CustomCounter("matchmaker_tickets_count", tags, 1)
	// Add the user to the matchmaker
	pID := ""
	if msession.Party != nil {
		pID = msession.Party.ID().String()
		stringProps["party_group"] = config.GroupID
		query = fmt.Sprintf("%s properties.party_group:%s^5", query, config.GroupID)
	}
	ticket, _, err = session.matchmaker.Add(ctx, presences, sessionID.String(), pID, query, minCount, maxCount, countMultiple, stringProps, numericProps)
	if err != nil {
		return "", fmt.Errorf("failed to add to matchmaker with query `%s`: %v", query, err)
	}
	msession.AddTicket(ticket, query)
	return ticket, nil
}

// Wrapper for the matchRegistry.ListMatches function.
func listMatches(ctx context.Context, p *EvrPipeline, limit int, minSize int, maxSize int, query string) ([]*api.Match, error) {
	return p.runtimeModule.MatchList(ctx, limit, true, "", &minSize, &maxSize, query)
}

func buildMatchQueryFromLabel(ml *EvrMatchState) string {
	var boost int = 0 // Default booster

	qparts := []string{
		// MUST be an open lobby
		OpenLobby.Query(Must, boost),
		// MUST be the same lobby type
		LobbyType(ml.LobbyType).Query(Must, boost),
		// MUST be the same mode
		GameMode(ml.Mode).Query(Must, boost),
	}

	if ml.TeamIndex != Spectator && ml.TeamIndex != Moderator {
		// MUST have room for this party on the teams
		qparts = append(qparts, fmt.Sprintf("+label.player_count:<=%d", ml.PlayerCount))
	}

	// MUST NOT much into the same lobby
	if ml.ID.UUID() != uuid.Nil {
		qparts = append(qparts, MatchId(ml.ID.UUID()).Query(MustNot, boost))
	}

	// MUST be a broadcaster on a channel the user has access to
	if len(ml.Broadcaster.GroupIDs) != 0 {
		qparts = append(qparts, Channels(ml.Broadcaster.GroupIDs).Query(Must, 0))
	}

	// SHOULD Add the current channel as a high boost SHOULD
	if *ml.GroupID != uuid.Nil {
		qparts = append(qparts, Channel(*ml.GroupID).Query(Should, 3))
	}

	switch len(ml.Broadcaster.Regions) {
	case 0:
		// If no regions are specified, use default
		qparts = append(qparts, Region(evr.Symbol(0)).Query(Must, 2))
	case 1:
		// If only one region is specified, use it as a MUST
		qparts = append(qparts, Region(ml.Broadcaster.Regions[0]).Query(Must, 2))
	default:
		// If multiple regions are specified, use the first as a MUST and the rest as SHOULD
		qparts = append(qparts, Region(ml.Broadcaster.Regions[0]).Query(Must, 2))
		for _, r := range ml.Broadcaster.Regions[1:] {
			qparts = append(qparts, Region(r).Query(Should, 2))
		}
	}

	for _, r := range ml.Broadcaster.Regions {
		qparts = append(qparts, Region(r).Query(Should, 2))
	}

	// Setup the query and logger
	return strings.Join(qparts, " ")
}

// mroundRTT rounds the rtt to the nearest modulus
func mroundRTT(rtt time.Duration, modulus time.Duration) time.Duration {
	if rtt == 0 {
		return 0
	}
	if rtt < modulus {
		return rtt
	}
	r := float64(rtt) / float64(modulus)
	return time.Duration(math.Round(r)) * modulus
}

// RTTweightedPopulationCmp compares two RTTs and populations
func RTTweightedPopulationCmp(i, j time.Duration, o, p int) bool {
	if i == 0 && j != 0 {
		return false
	}

	// Sort by if over or under 90ms
	if i < 90*time.Millisecond && j > 90*time.Millisecond {
		return true
	}
	if i > 90*time.Millisecond && j < 90*time.Millisecond {
		return false
	}

	// Sort by Population
	if o != p {
		return o > p
	}

	// If all else equal, sort by rtt
	return i < j
}

// PopulationCmp compares two populations
func PopulationCmp(i, j time.Duration, o, p int) bool {
	if o == p {
		// If all else equal, sort by rtt
		return i != 0 && i < j
	}
	return o > p
}

// LatencyCmp compares by latency, round to the nearest 10ms
func LatencyCmp(i, j time.Duration) bool {
	// Round to the closest 10ms
	i = mroundRTT(i, 10*time.Millisecond)
	j = mroundRTT(j, 10*time.Millisecond)
	return i < j
}

// TODO FIXME This need to use allocateBroadcaster instad.
// MatchCreate creates a match on an available unassigned broadcaster using the given label
func (p *EvrPipeline) MatchCreate(ctx context.Context, session *sessionWS, msession *MatchmakingSession, ml *EvrMatchState) (matchID MatchID, err error) {
	ml.MaxSize = MatchMaxSize
	// Lock the broadcaster's until the match is created
	p.matchmakingRegistry.Lock()

	defer func() {
		// Hold onto the lock for one extra second
		<-time.After(1 * time.Second)
		p.matchmakingRegistry.Unlock()
	}()

	select {
	case <-msession.Ctx.Done():
		return MatchID{}, nil
	default:
	}

	// TODO Move this into the matchmaking registry
	// Create a new match
	labels, err := p.ListUnassignedLobbies(ctx, session, ml)
	if err != nil {
		return MatchID{}, err
	}

	if len(labels) == 0 {
		return MatchID{}, ErrMatchmakingNoAvailableServers
	}

	endpoints := make([]evr.Endpoint, 0, len(labels))
	for _, label := range labels {
		endpoints = append(endpoints, label.Broadcaster.Endpoint)
	}

	// Ping the endpoints
	latencies, err := p.GetLatencyMetricByEndpoint(ctx, msession, endpoints, true)
	if err != nil {
		return MatchID{}, err
	}

	labelLatencies := make([]LabelLatencies, 0, len(labels))
	for _, label := range labels {
		for _, latency := range latencies {
			if label.Broadcaster.Endpoint.GetExternalIP() == latency.Endpoint.GetExternalIP() {
				labelLatencies = append(labelLatencies, LabelLatencies{label, &latency})
				break
			}
		}
	}
	region := ml.Broadcaster.Regions[0]
	sort.SliceStable(labelLatencies, func(i, j int) bool {
		// Sort by region, then by latency (zero'd RTTs at the end)
		if labelLatencies[i].label.Broadcaster.Regions[0] == region && labelLatencies[j].label.Broadcaster.Regions[0] != region {
			return true
		}
		if labelLatencies[i].label.Broadcaster.Regions[0] != region && labelLatencies[j].label.Broadcaster.Regions[0] == region {
			return false
		}
		if labelLatencies[i].latency.RTT == 0 && labelLatencies[j].latency.RTT != 0 {
			return false
		}
		if labelLatencies[i].latency.RTT != 0 && labelLatencies[j].latency.RTT == 0 {
			return true
		}
		return LatencyCmp(labelLatencies[i].latency.RTT, labelLatencies[j].latency.RTT)
	})

	matchID = labelLatencies[0].label.ID

	ml.SpawnedBy = session.UserID().String()

	// Prepare the match
	response, err := SignalMatch(ctx, p.matchRegistry, matchID, SignalPrepareSession, ml)
	if err != nil {
		return MatchID{}, ErrMatchmakingUnknownError
	}

	msession.Logger.Info("Match created", zap.String("match_id", matchID.String()), zap.String("label", response))

	// Return the prepared session
	return matchID, nil
}

// JoinEvrMatch allows a player to join a match.
func (p *EvrPipeline) JoinEvrMatch(ctx context.Context, logger *zap.Logger, session *sessionWS, query string, matchID MatchID, teamIndex int) error {
	// Append the node to the matchID if it doesn't already contain one.

	partyID := uuid.Nil

	if msession, ok := p.matchmakingRegistry.GetMatchingBySessionId(session.ID()); ok {
		if msession != nil && msession.Party != nil {
			partyID = msession.Party.ID()
		}
	}

	// Retrieve the evrID from the context.
	evrID, ok := ctx.Value(ctxEvrIDKey{}).(evr.EvrId)
	if !ok {
		return errors.New("evr id not found in context")
	}

	// Get the match label
	match, _, err := p.matchRegistry.GetMatch(ctx, matchID.String())
	if err != nil {
		return fmt.Errorf("failed to get match: %w", err)
	}
	if match == nil {
		return fmt.Errorf("match not found: %s", matchID.String())
	}

	label := &EvrMatchState{}
	if err := json.Unmarshal([]byte(match.GetLabel().GetValue()), label); err != nil {
		return fmt.Errorf("failed to unmarshal match label: %w", err)
	}

	groupID := uuid.Nil
	// Get the channel
	if label.GroupID != nil {
		groupID = uuid.FromStringOrNil((*label.GroupID).String())
	}

	// Determine the display name
	displayName, err := SetDisplayNameByChannelBySession(ctx, p.runtimeModule, logger, p.discordRegistry, session, groupID.String())
	if err != nil {
		logger.Warn("Failed to set display name.", zap.Error(err))
	}

	// If this is a NoVR user, give the profile's displayName a bot suffix
	// Get the NoVR key from context
	if flags, ok := ctx.Value(ctxFlagsKey{}).(int); ok {
		if flags&FlagNoVR != 0 {
			displayName = fmt.Sprintf("%s [BOT]", displayName)
		}
	}

	// Set the profile's display name.
	profile, found := p.profileRegistry.Load(session.UserID(), evrID)
	if !found {
		defer session.Close("profile not found", runtime.PresenceReasonUnknown)
		return fmt.Errorf("profile not found: %s", session.UserID())
	}

	profile.UpdateDisplayName(displayName)

	// Add the user's profile to the cache (by EvrID)
	err = p.profileRegistry.Cache(profile.GetServer())
	if err != nil {
		logger.Warn("Failed to add profile to cache", zap.Error(err))
	}

	// Prepare the player session metadata.

	discordID, err := p.discordRegistry.GetDiscordIdByUserId(ctx, session.UserID())
	if err != nil {
		logger.Error("Failed to get discord id", zap.Error(err))
	}

	mp := EvrMatchPresence{
		Node:          p.node,
		UserID:        session.userID,
		SessionID:     session.id,
		Username:      session.Username(),
		DisplayName:   displayName,
		EvrID:         evrID,
		PlayerSession: uuid.Must(uuid.NewV4()),
		PartyID:       partyID,
		TeamIndex:     int(teamIndex),
		DiscordID:     discordID,
		Query:         query,
		ClientIP:      session.clientIP,
	}

	// Marshal the player metadata into JSON.
	jsonMeta, err := json.Marshal(mp)
	if err != nil {
		return fmt.Errorf("failed to marshal player meta: %w", err)
	}
	metadata := map[string]string{"playermeta": string(jsonMeta)}
	// Do the join attempt to avoid race conditions
	found, allowed, isNew, reason, _, _ := p.matchRegistry.JoinAttempt(ctx, matchID.UUID(), matchID.Node(), session.UserID(), session.ID(), session.Username(), session.Expiry(), session.Vars(), session.clientIP, session.clientPort, p.node, metadata)
	if !found {
		return fmt.Errorf("match not found: %s", matchID.String())
	}
	if !allowed {
		switch reason {
		case ErrJoinRejectedUnassignedLobby:
			return status.Errorf(codes.NotFound, "join not allowed: %s", reason)
		case ErrJoinRejectedDuplicateJoin:
			return status.Errorf(codes.AlreadyExists, "join not allowed: %s", reason)
		case ErrJoinRejectedNotModerator:
			return status.Errorf(codes.PermissionDenied, "join not allowed: %s", reason)
		case ErrJoinRejectedLobbyFull:
			return status.Errorf(codes.ResourceExhausted, "join not allowed: %s", reason)
		default:
			return status.Errorf(codes.Internal, "join not allowed: %s", reason)
		}
	}

	if isNew {
		// Trigger the MatchJoin event.
		stream := PresenceStream{Mode: StreamModeMatchAuthoritative, Subject: matchID.UUID(), Label: matchID.Node()}
		m := PresenceMeta{
			Username: session.Username(),
			Format:   session.Format(),
			Status:   mp.Query,
		}
		if success, _ := p.tracker.Track(session.Context(), session.ID(), stream, session.UserID(), m); success {
			// Kick the user from any other matches they may be part of.
			// WARNING This cannot be used during transition. It will kick the player from their current match.
			//p.tracker.UntrackLocalByModes(session.ID(), matchStreamModes, stream)
		}
	}

	p.matchBySessionID.Store(session.ID().String(), matchID.String())

	return nil
}

// PingEndpoints pings the endpoints and returns the latencies.
func (p *EvrPipeline) GetLatencyMetricByEndpoint(ctx context.Context, msession *MatchmakingSession, endpoints []evr.Endpoint, update bool) ([]LatencyMetric, error) {
	if len(endpoints) == 0 {
		return nil, nil
	}
	logger := msession.Logger

	if update {
		// Get the candidates for pinging
		candidates := msession.GetPingCandidates(endpoints...)
		if len(candidates) > 0 {
			if err := p.sendPingRequest(logger, msession.Session, candidates); err != nil {
				return nil, err
			}

			select {
			case <-msession.Ctx.Done():
				return nil, ErrMatchmakingCanceled
			case <-time.After(5 * time.Second):
				// Just ignore the ping results if the ping times out
				logger.Warn("Ping request timed out")
			case results := <-msession.PingResultsCh:
				cache := msession.LatencyCache
				// Look up the endpoint in the cache and update the latency

				p.broadcasterRegistrationBySession.Range(func(_ string, b *MatchBroadcaster) bool {
					for _, response := range results {
						if b.Endpoint.ExternalIP.Equal(response.ExternalIP) && b.Endpoint.InternalIP.Equal(response.InternalIP) {
							r := LatencyMetric{
								Endpoint:  b.Endpoint,
								RTT:       response.RTT(),
								Timestamp: time.Now(),
							}

							cache.Store(b.Endpoint.GetExternalIP(), r)
						}
					}
					return true
				})

			}
		}
	}

	results := p.matchmakingRegistry.GetLatencies(msession.UserId, endpoints)
	return results, nil
}

// sendPingRequest sends a ping request to the given candidates.
func (p *EvrPipeline) sendPingRequest(logger *zap.Logger, session *sessionWS, candidates []evr.Endpoint) error {

	if err := session.SendEvr(
		evr.NewLobbyPingRequest(275, candidates),
		evr.NewSTcpConnectionUnrequireEvent(),
	); err != nil {
		return err
	}

	logger.Debug("Sent ping request", zap.Any("candidates", candidates))
	return nil
}

// checkSuspensionStatus checks if the user is suspended from the channel and returns the suspension status.
func (p *EvrPipeline) checkSuspensionStatus(ctx context.Context, logger *zap.Logger, userID string, channel uuid.UUID) (statuses []*SuspensionStatus, err error) {
	if channel == uuid.Nil {
		return nil, nil
	}

	// Get the guild group metadata
	md, err := p.discordRegistry.GetGuildGroupMetadata(ctx, channel.String())
	if err != nil {
		return nil, status.Errorf(codes.Internal, "Failed to get guild group metadata: %v", err)
	}
	if md == nil {
		return nil, status.Errorf(codes.Internal, "Metadata is nil for channel: %s", channel)
	}

	if md.SuspensionRole == "" {
		return nil, nil
	}

	// Get the user's discordId
	discordId, err := p.discordRegistry.GetDiscordIdByUserId(ctx, uuid.FromStringOrNil(userID))
	if err != nil {
		return nil, status.Errorf(codes.Internal, "Failed to get discord id: %v", err)

	}

	// Get the guild member
	member, err := p.discordRegistry.GetGuildMember(ctx, md.GuildID, discordId)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "Failed to get guild member: %v", err)
	}

	if member == nil {
		return nil, status.Errorf(codes.Internal, "Member is nil for discordId: %s", discordId)
	}

	if !slices.Contains(member.Roles, md.SuspensionRole) {
		return nil, nil
	}

	// TODO FIXME This needs to be refactored. extract method.
	// Check if the user has a detailed suspension status in storage
	keys := make([]string, 0, 2)
	// List all the storage objects in the SuspensionStatusCollection for this user
	ids, _, err := p.runtimeModule.StorageList(ctx, uuid.Nil.String(), userID, SuspensionStatusCollection, 1000, "")
	if err != nil {
		return nil, status.Errorf(codes.Internal, "Failed to list suspension status: %v", err)
	}
	if len(ids) == 0 {
		// Get the guild name and Id
		guild, err := p.discordRegistry.GetGuild(ctx, md.GuildID)
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
		guild, err := p.discordRegistry.GetGuild(ctx, md.GuildID)
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
