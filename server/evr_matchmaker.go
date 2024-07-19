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
	"github.com/samber/lo"
	"go.uber.org/zap"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const (
	MatchJoinGracePeriod        = 10 * time.Second
	MatchmakingStartGracePeriod = 3 * time.Second
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
	endpoints := make([]evr.Endpoint, 0, 100)
	p.broadcasterRegistrationBySession.Range(func(_ string, b *MatchBroadcaster) bool {
		endpoints = append(endpoints, b.Endpoint)
		return true
	})

	allRTTs, err := p.GetLatencyMetricByEndpoint(ctx, msession, endpoints, true)
	if err != nil {
		return "", err
	}
	// Get the EVR ID from the context
	evrID, ok := ctx.Value(ctxEvrIDKey{}).(evr.EvrId)
	if !ok {
		return "", status.Errorf(codes.Internal, "EVR ID not found in context")
	}

	query, stringProps, numericProps, err := msession.BuildQuery(allRTTs, evrID)
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

	minCount := 2
	maxCount := 8
	if msession.Label.Mode == evr.ModeCombatPublic {
		maxCount = 10
	} else {
		maxCount = 8
	}
	countMultiple := 2

	// Create a status presence for the user
	stream := PresenceStream{Mode: StreamModeMatchmaking, Subject: uuid.NewV5(uuid.Nil, "matchmaking")}
	meta := PresenceMeta{Format: session.format, Username: session.Username(), Status: query, Hidden: true}
	ok = session.tracker.Update(ctx, session.id, stream, session.userID, meta)
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

	label, err := MatchLabelByID(ctx, p.runtimeModule, matchID)
	if err != nil {
		return fmt.Errorf("failed to get match label: %w", err)
	}
	partyID := uuid.Nil
	msession, ok := p.matchmakingRegistry.GetMatchingBySessionId(session.ID())
	if !ok {
		if msession != nil && msession.Party != nil {
			partyID = msession.Party.ID()
		}
	}

	// Determine the display name
	displayName, err := SetDisplayNameByChannelBySession(ctx, p.runtimeModule, logger, p.discordRegistry, session, label.GetGroupID().String())
	if err != nil {
		logger.Warn("Failed to set display name.", zap.Error(err))
	}

	// Set the profile's display name.
	evrID, ok := ctx.Value(ctxEvrIDKey{}).(evr.EvrId)
	if !ok {
		return fmt.Errorf("failed to get evrID from session context")
	}

	loginSession, ok := ctx.Value(ctxLoginSessionKey{}).(*sessionWS)
	if !ok {
		return fmt.Errorf("failed to get login session from session context")
	}

	profile, found := p.profileRegistry.Load(session.UserID(), evrID)
	if !found {
		defer session.Close("profile not found", runtime.PresenceReasonUnknown)
		return fmt.Errorf("profile not found: %s", session.UserID())
	}

	profile.UpdateDisplayName(displayName)
	profile.SetEvrID(evrID)

	// Get the most recent past thursday
	serverProfile := profile.GetServer()

	for t := range serverProfile.Statistics {
		if t == "arena" || t == "combat" {
			continue
		}
		if strings.HasPrefix(t, "daily_") {
			// Parse the date
			date, err := time.Parse("2006_01_02", strings.TrimPrefix(t, "daily_"))
			// Keep anything less than 48 hours old
			if err == nil && time.Since(date) < 48*time.Hour {
				continue
			}
		} else if strings.HasPrefix(t, "weekly_") {
			// Parse the date
			date, err := time.Parse("2006_01_02", strings.TrimPrefix(t, "weekly_"))
			// Keep anything less than 2 weeks old
			if err == nil && time.Since(date) < 14*24*time.Hour {
				continue
			}
		}
		delete(serverProfile.Statistics, t)
	}

	// Add the user's profile to the cache (by EvrID)
	err = p.profileCache.Add(matchID, evrID, profile.GetServer())
	if err != nil {
		logger.Warn("Failed to add profile to cache", zap.Error(err))
	}

	// Prepare the player session metadata.
	discordID, err := GetDiscordIDByUserID(ctx, p.db, session.userID.String())
	if err != nil {
		logger.Error("Failed to get discord id", zap.Error(err))
	}

	mp := EvrMatchPresence{
		EntrantID:      uuid.NewV5(matchID.UUID(), evrID.String()),
		Node:           p.node,
		UserID:         session.userID,
		SessionID:      session.id,
		LoginSessionID: loginSession.id,
		Username:       session.Username(),
		DisplayName:    displayName,
		EvrID:          evrID,
		PartyID:        partyID,
		TeamIndex:      int(teamIndex),
		DiscordID:      discordID,
		Query:          query,
		ClientIP:       session.clientIP,
		ClientPort:     session.clientPort,
	}

	if label.LobbyType == PublicLobby && mp.TeamIndex == evr.TeamUnassigned {
		var allow bool
		mp.TeamIndex, allow = selectTeamForPlayer(NewRuntimeGoLogger(logger), &mp, label)
		if !allow {
			return fmt.Errorf("failed to join lobby: lobby full")
		}
	}

	label, _, err = EVRMatchJoinAttempt(ctx, logger, matchID, p.sessionRegistry, p.matchRegistry, p.tracker, mp)
	if err != nil {
		return fmt.Errorf("failed to join match: %w", err)
	}

	// Get the broadcasters session
	bsession := p.sessionRegistry.Get(uuid.FromStringOrNil(label.Broadcaster.SessionID)).(*sessionWS)
	if bsession == nil {
		return fmt.Errorf("broadcaster session not found: %s", label.Broadcaster.SessionID)
	}

	// Send the lobbysessionSuccess, this will trigger the broadcaster to send a lobbysessionplayeraccept once the player connects to the broadcaster.
	msg := evr.NewLobbySessionSuccess(label.Mode, label.ID.UUID(), label.GetGroupID(), label.GetEndpoint(), int16(mp.TeamIndex))
	messages := []evr.Message{msg.Version4(), msg.Version5(), evr.NewSTcpConnectionUnrequireEvent()}

	if err = bsession.SendEvr(messages...); err != nil {
		return fmt.Errorf("failed to send messages to broadcaster: %w", err)
	}

	if err = session.SendEvr(messages...); err != nil {
		err = fmt.Errorf("failed to send messages to player: %w", err)
	}
	if msession != nil {
		msession.Cancel(err)
	}
	return err
}

func EVRMatchJoinAttempt(ctx context.Context, logger *zap.Logger, matchID MatchID, sessionRegistry SessionRegistry, matchRegistry MatchRegistry, tracker Tracker, presence EvrMatchPresence) (*EvrMatchState, []*MatchPresence, error) {
	// Append the node to the matchID if it doesn't already contain one.

	data, err := json.Marshal(presence)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to marshal match presence: %w", err)
	}
	matchIDStr := matchID.String()
	metadata := map[string]string{"playermeta": string(data)}

	found, allowed, isNew, reason, labelStr, presences := matchRegistry.JoinAttempt(ctx, matchID.UUID(), matchID.Node(), presence.UserID, presence.SessionID, presence.Username, presence.SessionExpiry, nil, presence.ClientIP, presence.ClientPort, matchID.Node(), metadata)
	if !found {
		return nil, nil, fmt.Errorf("match not found: %s", matchIDStr)
	} else if labelStr == "" {
		return nil, nil, fmt.Errorf("match label not found: %s", matchIDStr)
	}

	label := &EvrMatchState{}
	if err := json.Unmarshal([]byte(labelStr), label); err != nil {
		return nil, presences, fmt.Errorf("failed to unmarshal match label: %w", err)
	}

	if !allowed {
		return label, presences, fmt.Errorf("join not allowed: %s", reason)
	} else if !isNew {
		return label, presences, fmt.Errorf("player already in match: %s", matchIDStr)
	}

	ops := []*TrackerOp{
		{
			PresenceStream{Mode: StreamModeEntrant, Subject: matchID.uuid, Subcontext: presence.EntrantID, Label: matchID.node},
			PresenceMeta{Format: SessionFormatEvr, Username: presence.Username, Status: string(data), Hidden: true},
		},
		{
			PresenceStream{Mode: StreamModeService, Subject: presence.SessionID, Label: StreamLabelMatchService},
			PresenceMeta{Format: SessionFormatEvr, Username: presence.Username, Status: matchIDStr, Hidden: true},
		},
		{
			PresenceStream{Mode: StreamModeService, Subject: presence.LoginSessionID, Label: StreamLabelMatchService},
			PresenceMeta{Format: SessionFormatEvr, Username: presence.Username, Status: matchIDStr, Hidden: true},
		},
		{
			PresenceStream{Mode: StreamModeService, Subject: presence.UserID, Label: StreamLabelMatchService},
			PresenceMeta{Format: SessionFormatEvr, Username: presence.Username, Status: matchIDStr, Hidden: true},
		},
		{
			PresenceStream{Mode: StreamModeService, Subject: presence.EvrID.UUID(), Label: StreamLabelMatchService},
			PresenceMeta{Format: SessionFormatEvr, Username: presence.Username, Status: matchIDStr, Hidden: true},
		},
	}

	// Update the statuses
	for _, op := range ops {
		if ok := tracker.Update(ctx, presence.SessionID, op.Stream, presence.UserID, op.Meta); !ok {
			return label, presences, fmt.Errorf("failed to track user: %w", err)
		}
	}

	return label, presences, nil
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

// selectTeamForPlayer decides which team to assign a player to.
func selectTeamForPlayer(logger runtime.Logger, presence *EvrMatchPresence, state *EvrMatchState) (int, bool) {
	t := presence.TeamIndex

	teams := lo.GroupBy(lo.Values(state.presences), func(p *EvrMatchPresence) int { return p.TeamIndex })

	blueTeam := teams[evr.TeamBlue]
	orangeTeam := teams[evr.TeamOrange]
	playerpop := len(blueTeam) + len(orangeTeam)
	spectators := len(teams[evr.TeamSpectator]) + len(teams[evr.TeamModerator])
	teamsFull := playerpop >= state.TeamSize*2
	specsFull := spectators >= int(state.MaxSize)-state.TeamSize*2

	// If the lobby is full, reject
	if len(state.presences) >= int(state.MaxSize) {
		return evr.TeamUnassigned, false
	}

	// If the player is a moderator and the spectators are not full, return
	if t == evr.TeamModerator && !specsFull {
		return t, true
	}

	// If the match has been running for less than 15 seconds check the presets for the team
	if time.Since(state.StartTime) < 15*time.Second {
		if teamIndex, ok := state.TeamAlignments[presence.GetUserId()]; ok {
			// Make sure the team isn't already full
			if len(teams[teamIndex]) < state.TeamSize {
				return teamIndex, true
			}
		}
	}

	// If this is a social lobby, put the player on the social team.
	if state.Mode == evr.ModeSocialPublic || state.Mode == evr.ModeSocialPrivate {
		return evr.TeamSocial, true
	}

	// If the player is unassigned, assign them to a team.
	if t == evr.TeamUnassigned {
		t = evr.TeamBlue
	}

	// If this is a private lobby, and the teams are full, put them on spectator
	if t != evr.TeamSpectator && teamsFull {
		if state.LobbyType == PrivateLobby {
			t = evr.TeamSpectator
		} else {
			// The lobby is full, reject the player.
			return evr.TeamUnassigned, false
		}
	}

	// If this is a spectator, and the spectators are not full, return
	if t == evr.TeamSpectator {
		if specsFull {
			return evr.TeamUnassigned, false
		}
		return t, true
	}
	// If this is a private lobby, and their team is not full,  put the player on the team they requested
	if state.LobbyType == PrivateLobby && len(teams[t]) < state.TeamSize {
		return t, true
	}

	// If the players team is unbalanced, put them on the other team
	if len(teams[evr.TeamBlue]) != len(teams[evr.TeamOrange]) {
		if len(teams[evr.TeamBlue]) < len(teams[evr.TeamOrange]) {
			t = evr.TeamBlue
		} else {
			t = evr.TeamOrange
		}
	}

	logger.Debug("picked team", zap.Int("team", t))
	return t, true
}
