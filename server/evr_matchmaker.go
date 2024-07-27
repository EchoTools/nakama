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

	"github.com/bwmarrin/discordgo"
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
func (p *EvrPipeline) GetBackfillCandidates(msession *MatchmakingSession) ([]*EvrMatchState, string, error) {
	ctx := msession.Context()

	partySize := 1
	if msession.Party != nil {
		partySize = len(msession.Party.GetMembers())
	}

	logger := msession.Logger
	labels, query, err := p.matchmakingRegistry.listUnfilledLobbies(ctx, logger, msession.Label, partySize)
	if err != nil || len(labels) == 0 {
		return nil, query, err
	}

	endpoints := make([]evr.Endpoint, 0, len(labels))
	for _, l := range labels {
		if (l.PlayerLimit - l.PlayerCount) < partySize {
			continue
		}
		endpoints = append(endpoints, l.Broadcaster.Endpoint)
	}
	latencies := p.matchmakingRegistry.GetLatencies(msession.UserId, endpoints)

	labelLatencies := make([]LabelLatencies, 0, len(labels))
	for _, label := range labels {
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

	candidates := make([]*EvrMatchState, 0, len(labelLatencies))
	for _, ll := range labelLatencies {
		candidates = append(candidates, ll.label)
	}

	return candidates, query, nil
}

type BroadcasterLatencies struct {
	Endpoint evr.Endpoint
	Latency  time.Duration
}

func (p *EvrPipeline) MatchBackfill(msession *MatchmakingSession) error {
	logger := msession.Logger
	ctx := msession.Context()

	delayStartTimer := time.NewTimer(100 * time.Millisecond)
	retryTicker := time.NewTicker(4 * time.Second)
	createTicker := time.NewTicker(6 * time.Second)
	defer retryTicker.Stop()
	defer createTicker.Stop()

	msessions := make([]*MatchmakingSession, 0)
	if msession.Party != nil {
		for _, member := range msession.Party.GetMembers() {
			ms, found := p.matchmakingRegistry.GetMatchingBySessionId(uuid.FromStringOrNil(member.Presence.GetSessionId()))
			if !found || ms == nil {
				continue
			}
			msessions = append(msessions, ms)
		}
		logger = logger.With(zap.Any("party", msession.Party.GetMembers()))
	} else {
		msessions = append(msessions, msession)
	}

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-msession.Ctx.Done():
			return nil
		case <-delayStartTimer.C:
		case <-retryTicker.C:
		}
		// Backfill any existing matches
		labels, query, err := p.GetBackfillCandidates(msession)
		if err != nil {
			return msession.Cancel(fmt.Errorf("failed to find backfill match: %w", err))
		}

		for _, label := range labels {

			select {
			case <-ctx.Done():
				return nil
			case <-msession.Ctx.Done():
				return nil
			case <-time.After(250 * time.Millisecond):
			}

			if err := p.LobbyJoin(msession.Session.Context(), logger, label.ID, int(AnyTeam), query, msessions...); err != nil {
				logger.Warn("Failed to backfill match", zap.Error(err))
				continue
			}
			p.metrics.CustomCounter("match_join_backfill_count", msession.metricsTags(), int64(len(msessions)))
			logger.Debug("Backfilled match", zap.String("mid", label.ID.UUID().String()))
			return nil
		}
		// After trying to backfill, try to create a match on an interval
		select {
		case <-createTicker.C:
			if msession.Label.Mode == evr.ModeSocialPublic && p.createLobbyMu.TryLock() {
				if matchID, err := p.MatchCreate(ctx, msession, msession.Label); err != nil {
					logger.Warn("Failed to create match", zap.Error(err))
					p.createLobbyMu.Unlock()
					continue
				} else if err := p.LobbyJoin(msession.Session.Context(), logger, matchID, int(AnyTeam), query, msessions...); err != nil {
					logger.Warn("Failed to join created match", zap.Error(err))
					p.createLobbyMu.Unlock()
					return err
				}
				return nil
			}
		default:
		}

	}
}

// Matchmake attempts to find/create a match for the user using the nakama matchmaker
func (p *EvrPipeline) MatchMake(session *sessionWS, msession *MatchmakingSession) (err error) {
	// TODO Move this into the matchmaking registry
	ctx := msession.Context()
	// TODO FIXME Add a custom matcher for broadcaster matching
	// Get a list of all the broadcasters
	endpoints := make([]evr.Endpoint, 0, 100)
	p.broadcasterRegistrationBySession.Range(func(_ string, b *MatchBroadcaster) bool {
		endpoints = append(endpoints, b.Endpoint)
		return true
	})

	allRTTs := p.matchmakingRegistry.GetLatencies(msession.UserId, endpoints)

	// Get the EVR ID from the context
	evrID, ok := ctx.Value(ctxEvrIDKey{}).(evr.EvrId)
	if !ok {
		return status.Errorf(codes.Internal, "EVR ID not found in context")
	}

	query, stringProps, numericProps, err := msession.BuildQuery(allRTTs, evrID)
	if err != nil {
		return status.Errorf(codes.Internal, "Failed to build matchmaking query: %v", err)
	}

	// Add the user to the matchmaker
	sessionID := session.ID()

	userID := session.UserID().String()

	// Load the global matchmaking config
	gconfig, err := LoadMatchmakingSettings(ctx, p.runtimeModule, SystemUserID)
	if err != nil {
		return status.Errorf(codes.Internal, "Failed to load global matchmaking config: %v", err)
	}

	// Load the user's matchmaking config
	config, err := LoadMatchmakingSettings(ctx, p.runtimeModule, userID)
	if err != nil {
		return status.Errorf(codes.Internal, "Failed to load matchmaking config: %v", err)
	}
	// Merge the user's config with the global config
	query = fmt.Sprintf("%s %s %s", query, gconfig.MatchmakingQueryAddon, config.MatchmakingQueryAddon)

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
		return status.Errorf(codes.Internal, "Failed to track user: %v", err)
	}
	tags := map[string]string{
		"type":       msession.Label.LobbyType.String(),
		"mode":       msession.Label.Mode.String(),
		"level":      msession.Label.Level.String(),
		"party_size": "1",
	}

	// Add the user to the matchmaker

	// If this is a party, then wait 30 seconds for everyone to start matchmaking, then only hte leader will put in a ticket
	if msession.Party == nil {
		presences := []*MatchmakerPresence{
			{
				UserId:    userID,
				SessionId: session.ID().String(),
				Username:  session.Username(),
				Node:      p.node,
				SessionID: sessionID,
			},
		}

		// If the user is not in a party, then they can put in a ticket immediately
		ticket, _, err := session.matchmaker.Add(ctx, presences, sessionID.String(), "", query, minCount, maxCount, countMultiple, stringProps, numericProps)
		if err != nil {
			msession.Logger.Error("Failed to add to solo matchmaker ticket", zap.String("query", query), zap.Error(err))
		}
		msession.AddTicket(ticket, query)
		p.metrics.CustomCounter("matchmaker_tickets_count", tags, 1)
		msession.Logger.Info("Added solo matchmaking ticket", zap.String("query", query), zap.String("ticket", ticket), zap.Any("presences", presences))
		go func() {
			<-msession.Ctx.Done()
			session.pipeline.matchmaker.RemoveSessionAll(sessionID.String())
		}()
		return nil

	}

	partyID := msession.Party.ID().String()
	stringProps["party_group"] = config.GroupID

	// This is the leader. Provide a grace period for players to start matchmaking
	select {
	case <-msession.Ctx.Done():
		// The leader has canceled matchmaking before the grace period is over
		// Cancel any active matchmaking
		for _, member := range msession.Party.GetMembers() {
			ms, found := p.matchmakingRegistry.GetMatchingBySessionId(uuid.FromStringOrNil(member.Presence.GetSessionId()))
			if !found || ms == nil {
				continue
			}
			msession.Cancel(ErrMatchmakingCanceledByParty)
		}
		return
	case <-time.After(15 * time.Second):
	}

	// Create a slice and map of who is matchmaking
	msessions := make([]*MatchmakingSession, 0, len(msession.Party.GetMembers()))
	msessionMap := make(map[string]*MatchmakingSession)
	for _, member := range msession.Party.GetMembers() {
		ms, found := p.matchmakingRegistry.GetMatchingBySessionId(uuid.FromStringOrNil(member.Presence.GetSessionId()))
		if !found || ms == nil {
			continue
		}
		msessions = append(msessions, ms)
		msessionMap[member.Presence.GetSessionId()] = ms
	}

	// Remove anyone who is not matchmaking
	for _, member := range msession.Party.GetMembers() {
		if _, found := msessionMap[member.Presence.GetSessionId()]; !found {
			p.runtimeModule.StreamUserLeave(StreamModeParty, msession.Party.ph.IDStr, "", p.node, member.Presence.GetUserId(), member.Presence.GetSessionId())
		}
	}

	// Add the ticket through the party handler
	ticket, presenceIDs, err := msession.Party.ph.MatchmakerAdd(session.id.String(), p.node, query, minCount, maxCount, countMultiple, stringProps, numericProps)
	if err != nil {
		msession.Logger.Error("Failed to add to matchmaker with query", zap.String("query", query), zap.Error(err))
	}
	msession.AddTicket(ticket, query)
	tags["party_size"] = fmt.Sprintf("%d", len(presenceIDs))
	p.metrics.CustomCounter("matchmaker_tickets_count", tags, 1)
	go func() {
		err := PartySyncMatchmaking(ctx, msessions, msession.Party.ph, 4*time.Minute)
		if err != nil {
			msession.Logger.Debug("PartySyncMatchmaking errored", zap.Error(err))
		}
	}()
	msession.Logger.Info("Added party matchmaking ticket", zap.String("query", query), zap.String("ticket", ticket), zap.Any("presences", msession.Party.GetMembers()), zap.Any("presence_ids", presenceIDs))

	// Send a message to everyone in the group that they are matchmaking with the leader and others
	discordIDs := make([]string, 0, len(msession.Party.GetMembers()))

	for _, pid := range presenceIDs {
		ms, found := msessionMap[pid.SessionID.String()]
		if !found || ms == nil {
			continue
		}
		discordID, err := GetDiscordIDByUserID(ctx, p.db, ms.Session.userID.String())
		if err != nil {
			msession.Logger.Error("Failed to get discord id", zap.Error(err))
			continue
		}

		// If this is the leader, then put them first in the slice
		if pid.SessionID == session.id {
			discordIDs = append([]string{discordID}, discordIDs...)
			continue
		}
		discordIDs = append(discordIDs, discordID)

	}
	mentions := make([]string, 0, len(presenceIDs))
	for _, discordID := range discordIDs {
		mentions = append(mentions, fmt.Sprintf("<@%s>", discordID))
	}

	dg := p.discordRegistry.GetBot()
	// Send a DM to each user
	if len(discordIDs) > 1 {
		for _, discordID := range discordIDs {
			channel, err := dg.UserChannelCreate(discordID)
			if err != nil {
				msession.Logger.Error("Failed to create DM channel", zap.Error(err))
				continue
			}
			if _, err = dg.ChannelMessageSend(channel.ID, fmt.Sprintf(`%s's party (%s) is matchmaking...`, mentions[0], strings.Join(mentions[1:], ", "))); err != nil {
				msession.Logger.Error("Failed to send message", zap.Error(err))
				continue
			}
		}
	}

	<-msession.Ctx.Done()
	session.pipeline.matchmaker.RemovePartyAll(partyID)
	return msession.Cause()
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
		"+label.open:T",
		// Must not be the same lobby
	}

	if !ml.ID.IsNil() {
		qparts = append(qparts, "-label.id:"+ml.ID.String())
	}

	if ml.TeamIndex != Spectator && ml.TeamIndex != Moderator {
		// MUST have room for this party on the teams
		qparts = append(qparts, fmt.Sprintf("+label.player_count:<=%d", ml.PlayerCount))
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
func (p *EvrPipeline) MatchCreate(ctx context.Context, msession *MatchmakingSession, ml *EvrMatchState) (matchID MatchID, err error) {
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
	labels, err := p.ListUnassignedLobbies(ctx, msession.Session, ml)
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

	latencies := p.matchmakingRegistry.GetLatencies(msession.UserId, endpoints)

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

	ml.SpawnedBy = msession.Session.UserID().String()

	// Prepare the match
	response, err := SignalMatch(ctx, p.matchRegistry, matchID, SignalPrepareSession, ml)
	if err != nil {
		return MatchID{}, ErrMatchmakingUnknownError
	}

	msession.Logger.Info("Match created", zap.String("match_id", matchID.String()), zap.String("label", response))

	// Return the prepared session
	return matchID, nil
}

func NewMatchPresenceFromSession(msession *MatchmakingSession, matchID MatchID, displayName string, role int, query string) (*EvrMatchPresence, error) {
	// Set the profile's display name.
	ctx := msession.Session.Context()

	evrID, ok := ctx.Value(ctxEvrIDKey{}).(evr.EvrId)
	if !ok {
		return nil, fmt.Errorf("failed to get evrID from session context")
	}

	discordID, ok := ctx.Value(ctxDiscordIDKey{}).(string)
	if !ok {
		return nil, fmt.Errorf("failed to get discordID from session context")
	}

	loginSession, ok := ctx.Value(ctxLoginSessionKey{}).(*sessionWS)
	if !ok {
		return nil, fmt.Errorf("failed to get login session from session context")
	}

	partyID := uuid.Nil
	if msession.Party != nil {
		partyID = msession.Party.ID()
	}
	session := msession.Session
	return &EvrMatchPresence{
		EntrantID:      uuid.NewV5(matchID.UUID(), evrID.String()),
		Node:           session.pipeline.node,
		UserID:         session.userID,
		SessionID:      session.id,
		LoginSessionID: loginSession.id,
		Username:       session.Username(),
		DisplayName:    displayName,
		EvrID:          evrID,
		PartyID:        partyID,
		RoleAlignment:  role,
		DiscordID:      discordID,
		Query:          query,
		ClientIP:       session.clientIP,
		ClientPort:     session.clientPort,
	}, nil

}

func (p *EvrPipeline) LobbyJoinPrepare(ctx context.Context, logger *zap.Logger, msession *MatchmakingSession, matchID MatchID, query string, roleAlignment int) (*EvrMatchPresence, error) {
	session := msession.Session
	// Determine the display name
	displayName, err := SetDisplayNameByChannelBySession(ctx, logger, p.db, p.runtimeModule, p.discordRegistry, session.userID.String(), session.Username(), msession.Label.GetGroupID().String())
	if err != nil {
		logger.Warn("Failed to set display name.", zap.Error(err))
	}
	// Set the profile's display name.
	evrID, ok := ctx.Value(ctxEvrIDKey{}).(evr.EvrId)
	if !ok {
		return nil, fmt.Errorf("failed to get evrID from session context")
	}

	if err := p.profileRegistry.SetLobbyProfile(ctx, msession.Session.userID, evrID, displayName); err != nil {
		return nil, fmt.Errorf("failed to set lobby profile: %w", err)
	}

	return NewMatchPresenceFromSession(msession, matchID, displayName, roleAlignment, query)
}

func (p *EvrPipeline) LobbyJoin(ctx context.Context, logger *zap.Logger, matchID MatchID, teamIndex int, query string, msessions ...*MatchmakingSession) error {
	label, err := MatchLabelByID(ctx, p.runtimeModule, matchID)
	if err != nil || label == nil {
		return fmt.Errorf("failed to get match label: %w", err)
	}
	logger = logger.With(zap.String("mid", matchID.String()), zap.String("label", label.String()))
	// Prepare all of the presences
	presences := make([]*EvrMatchPresence, 0, len(msessions))
	for _, msession := range msessions {
		presence, err := p.LobbyJoinPrepare(msession.Context(), logger, msession, matchID, query, teamIndex)
		if err != nil {
			logger.Warn("Failed to prepare presences for match", zap.Any("presence", presence), zap.Any("presences", presences), zap.Error(err))
			return fmt.Errorf("failed to prepare lobby join: %w", err)
		}
		presences = append(presences, presence)
	}

	matchPresences := make([]*EvrMatchPresence, 0, len(presences))

	// Trigger MatchJoinAttempt
	for i, presence := range presences {

		_, presence, _, err = EVRMatchJoinAttempt(msessions[i].Context(), logger, matchID, p.sessionRegistry, p.matchRegistry, p.tracker, *presence)
		if err != nil {
			logger.Warn("Failed to join presences to match", zap.Any("presence", presence), zap.Any("presences", presences), zap.Error(err))
			return err
		}
		matchPresences = append(matchPresences, presence)
	}

	// Get the broadcasters session
	bsession := p.sessionRegistry.Get(uuid.FromStringOrNil(label.Broadcaster.SessionID)).(*sessionWS)
	if bsession == nil {
		return fmt.Errorf("broadcaster session not found: %s", label.Broadcaster.SessionID)
	}

	// If this part errors, the matchmaking session must be canceled.
	for i, presence := range matchPresences {
		session := msessions[i].Session
		// Send the lobbysessionSuccess, this will trigger the broadcaster to send a lobbysessionplayeraccept once the player connects to the broadcaster.
		msg := evr.NewLobbySessionSuccess(label.Mode, label.ID.UUID(), label.GetGroupID(), label.GetEndpoint(), int16(presence.RoleAlignment))

		if err = bsession.SendEvr(msg.Version5()); err != nil {
			err = fmt.Errorf("failed to send messages to broadcaster: %w", err)
			break
		}

		if err = session.SendEvr(msg.Version5()); err != nil {
			err = fmt.Errorf("failed to send messages to player: %w", err)
			break
		}
	}

	for _, msession := range msessions {
		msession.Cancel(err)
	}

	return nil
}

var ErrDuplicateJoin = errors.New(JoinRejectReasonDuplicateJoin)

// Triggers MatchJoinAttempt
func EVRMatchJoinAttempt(ctx context.Context, logger *zap.Logger, matchID MatchID, sessionRegistry SessionRegistry, matchRegistry MatchRegistry, tracker Tracker, presence EvrMatchPresence) (string, *EvrMatchPresence, []*MatchPresence, error) {

	matchIDStr := matchID.String()
	metadata := JoinMetadata{Presence: presence}.MarshalMap()

	found, allowed, isNew, reason, labelStr, presences := matchRegistry.JoinAttempt(ctx, matchID.UUID(), matchID.Node(), presence.UserID, presence.SessionID, presence.Username, presence.SessionExpiry, nil, presence.ClientIP, presence.ClientPort, matchID.Node(), metadata)
	if !found {
		return "", nil, nil, fmt.Errorf("match not found: %s", matchIDStr)
	} else if labelStr == "" {
		return "", nil, nil, fmt.Errorf("match label not found: %s", matchIDStr)
	}

	label := &EvrMatchState{}
	if err := json.Unmarshal([]byte(labelStr), label); err != nil {
		return "", nil, presences, fmt.Errorf("failed to unmarshal match label: %w", err)
	}

	if !allowed {
		return labelStr, nil, presences, fmt.Errorf("join not allowed: %s", reason)
	} else if !isNew {
		return labelStr, nil, presences, ErrDuplicateJoin
	}

	presence = EvrMatchPresence{}
	if err := json.Unmarshal([]byte(reason), &presence); err != nil {
		return labelStr, nil, presences, fmt.Errorf("failed to unmarshal attempt response: %w", err)
	}

	ops := []*TrackerOp{
		{
			PresenceStream{Mode: StreamModeEntrant, Subject: matchID.uuid, Subcontext: presence.EntrantID, Label: matchID.node},
			PresenceMeta{Format: SessionFormatEvr, Username: presence.Username, Status: presence.String(), Hidden: true},
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
			return labelStr, &presence, presences, fmt.Errorf("failed to track session ID: %s", presence.SessionID)
		}
	}

	return labelStr, &presence, presences, nil
}

// PingEndpoints pings the endpoints and returns the latencies.
func (p *EvrPipeline) PingGameServers(msession *MatchmakingSession) error {
	logger := msession.Logger

	endpoints := make([]evr.Endpoint, 0, 100)
	p.broadcasterRegistrationBySession.Range(func(_ string, b *MatchBroadcaster) bool {
		endpoints = append(endpoints, b.Endpoint)
		return true
	})

	// Get the candidates for pinging
	candidates := msession.GetPingCandidates(endpoints...)
	if len(candidates) == 0 {
		return nil
	}

	if err := p.sendPingRequest(logger, msession.Session, candidates); err != nil {
		return err
	}

	select {
	case <-msession.Ctx.Done():
		return ErrMatchmakingCanceled
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

	return nil
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

func (p *EvrPipeline) MatchSpectateStreamLoop(msession *MatchmakingSession) error {
	logger := msession.Logger
	ctx := msession.Context()
	p.metrics.CustomCounter("spectatestream_active_count", msession.metricsTags(), 1)

	limit := 100
	minSize := 1
	maxSize := MatchMaxSize - 1
	query := fmt.Sprintf("+label.open:T +label.lobby_type:public +label.mode:%s +label.size:>=%d +label.size:<=%d", msession.Label.Mode.Token(), minSize, maxSize)
	// creeate a delay timer
	timer := time.NewTimer(0 * time.Second)
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-timer.C:
		case <-ticker.C:
		}

		// list existing matches
		matches, err := listMatches(ctx, p, limit, minSize+1, maxSize+1, query)
		if err != nil {
			return msession.Cancel(fmt.Errorf("failed to find spectate match: %w", err))
		}

		if len(matches) != 0 {
			// sort matches by population
			sort.SliceStable(matches, func(i, j int) bool {
				// Sort by newer matches
				return matches[i].Size > matches[j].Size
			})

			if err := p.LobbyJoin(ctx, logger, MatchIDFromStringOrNil(matches[0].GetMatchId()), evr.TeamSpectator, query, msession); err != nil {
				logger.Error("Error joining player to matchmaker match", zap.Error(err))
			}
			p.metrics.CustomCounter("match_join_spectatestream_count", msession.metricsTags(), 1)
		}
	}
}

func (p *EvrPipeline) MatchCreateLoop(session *sessionWS, msession *MatchmakingSession, pruneDelay time.Duration) error {
	ctx := msession.Context()
	logger := msession.Logger
	// set a timeout
	//stageTimer := time.NewTimer(pruneDelay)
	p.metrics.CustomCounter("match_create_active_count", msession.metricsTags(), 1)
	for {

		select {
		case <-ctx.Done():
			return nil
		default:
		}
		// Stage 1: Check if there is an available broadcaster
		matchID, err := p.MatchCreate(ctx, msession, msession.Label)

		switch status.Code(err) {

		case codes.OK:
			if matchID.IsNil() {
				return msession.Cancel(fmt.Errorf("match is nil"))
			}
			if err := p.LobbyJoin(ctx, logger, matchID, int(msession.Label.TeamIndex), "", msession); err != nil {
				logger.Warn("Error joining player to created match", zap.Error(err))
			}
			p.metrics.CustomCounter("match_join_created_count", msession.metricsTags(), 1)
			// Keep trying until the context is done
		case codes.NotFound, codes.ResourceExhausted, codes.Unavailable:
			p.metrics.CustomCounter("create_unavailable_count", msession.metricsTags(), 1)
		case codes.Unknown, codes.Internal:
			logger.Warn("Failed to create match", zap.Error(err))
		default:
			return msession.Cancel(err)
		}
		<-time.After(10 * time.Second)
	}
}

func (p *EvrPipeline) MatchFind(parentCtx context.Context, logger *zap.Logger, session *sessionWS, ml *EvrMatchState) error {
	if s, found := p.matchmakingRegistry.GetMatchingBySessionId(session.id); found {
		// Replace the session
		logger.Debug("Matchmaking session already exists", zap.Any("tickets", s.Tickets))
	}

	// Create a new matching session
	logger.Debug("Creating a new matchmaking session")
	partySize := 1
	timeout := 5 * time.Minute

	if ml.TeamIndex == TeamIndex(evr.TeamSpectator) {
		timeout = 12 * time.Hour
	}

	msession, err := p.matchmakingRegistry.Create(parentCtx, logger, session, ml, partySize, timeout)
	if err != nil {
		logger.Error("Failed to create matchmaking session", zap.Error(err))
		return err
	}
	logger = msession.Logger

	p.metrics.CustomCounter("match_find_active_count", msession.metricsTags(), 1)
	// Load the user's matchmaking config

	config, err := LoadMatchmakingSettings(msession.Ctx, p.runtimeModule, session.userID.String())
	if err != nil {
		logger.Error("Failed to load matchmaking config", zap.Error(err))
	}

	var matchID MatchID
	// Check for a direct match first
	if !config.NextMatchID.IsNil() {
		matchID = config.NextMatchID
	}

	config.NextMatchID = MatchID{}
	err = StoreMatchmakingSettings(msession.Ctx, p.runtimeModule, session.userID.String(), config)
	if err != nil {
		logger.Error("Failed to save matchmaking config", zap.Error(err))
	}

	if !matchID.IsNil() {
		logger.Debug("Attempting to join match from settings", zap.String("mid", matchID.String()))
		match, _, err := p.matchRegistry.GetMatch(msession.Ctx, matchID.String())
		if err != nil {
			logger.Error("Failed to get match", zap.Error(err))
		} else {
			if match == nil {
				logger.Warn("Match not found", zap.String("mid", matchID.String()))
			} else {
				// Join the match
				if err := p.LobbyJoin(msession.Ctx, logger, matchID, int(AnyTeam), "", msession); err != nil {
					logger.Warn("Error joining player to found match", zap.Error(err))
					return err
				}
				p.metrics.CustomCounter("match_join_next_count", map[string]string{}, 1)
				return nil
			}
		}
	}

	if config.GroupID != "" && ml.TeamIndex != TeamIndex(evr.TeamSpectator) {
		msession.Party, err = JoinPartyGroup(session, config.GroupID)
		if err != nil {
			logger.Warn("Failed to join party group", zap.String("group_id", config.GroupID), zap.Error(err))
		} else {

			logger.Debug("Joined party", zap.String("party_id", msession.Party.ID().String()), zap.Any("members", msession.Party.GetMembers()))

			if leader := msession.Party.GetLeader(); leader != nil && leader.SessionId != session.id.String() {
				// Delay to allow the leader to end up in a social lobby
				//<-time.After(3 * time.Second)
			}
		}
	}

	if ml.TeamIndex == TeamIndex(evr.TeamModerator) {
		// Check that the user is a moderator for this channel, or globally
		guild, err := p.discordRegistry.GetGuildByGroupId(parentCtx, ml.GroupID.String())
		if err != nil || guild == nil {
			logger.Warn("failed to get guild: %v", zap.Error(err))
			ml.TeamIndex = TeamIndex(evr.TeamSpectator)
		} else {
			discordID, err := p.discordRegistry.GetDiscordIdByUserId(parentCtx, session.userID)
			if err != nil {
				return fmt.Errorf("failed to get discord id: %v", err)
			}
			if ok, _, err := p.discordRegistry.isModerator(parentCtx, guild.ID, discordID); err != nil || !ok {
				ml.TeamIndex = TeamIndex(evr.TeamSpectator)
			}
		}
	}
	if ml.TeamIndex == TeamIndex(evr.TeamSpectator) {
		if ml.Mode != evr.ModeArenaPublic && ml.Mode != evr.ModeCombatPublic {
			return fmt.Errorf("spectators are only allowed in arena and combat matches")
		}
		// Spectators don't matchmake, and they don't have a delay for backfill.
		// Spectators also don't time out.
		return p.MatchSpectateStreamLoop(msession)
	}

	// Start a ping to broadcaster endpoints
	go p.PingGameServers(msession)

	switch ml.Mode {
	// For public matches, backfill or matchmake
	// If it's a social match, backfill or create immediately
	case evr.ModeSocialPublic:
		// Continue to try to backfill
		return p.MatchBackfill(msession)

	case evr.ModeCombatPublic:

		env := p.config.GetRuntime().Environment
		channelID, ok := env["COMBAT_MATCHMAKING_CHANNEL_ID"]
		if ok {

			if bot := p.discordRegistry.GetBot(); bot != nil && ml.TeamIndex != TeamIndex(evr.TeamSpectator) {
				// Count how many players are matchmaking for this mode right now
				sessionsByMode := p.matchmakingRegistry.SessionsByMode()

				userIDs := make([]uuid.UUID, 0)
				for _, s := range sessionsByMode[ml.Mode] {

					if s.Session.userID == session.userID || s.Label.TeamIndex == TeamIndex(evr.TeamSpectator) {
						continue
					}

					userIDs = append(userIDs, s.Session.userID)
				}

				matchID, _, err := GetMatchBySessionID(p.runtimeModule, session.id)
				if err != nil {
					logger.Warn("Failed to get match by session ID", zap.Error(err))
				}

				// Translate the userID's to discord ID's
				discordIDs := make([]string, 0, len(userIDs))
				for _, userID := range userIDs {

					did, err := p.discordRegistry.GetDiscordIdByUserId(parentCtx, userID)
					if err != nil {
						logger.Warn("Failed to get discord ID", zap.Error(err))
						continue
					}
					discordIDs = append(discordIDs, fmt.Sprintf("<@%s>", did))
				}

				discordID, err := p.discordRegistry.GetDiscordIdByUserId(parentCtx, session.userID)
				if err != nil {
					logger.Warn("Failed to get discord ID", zap.Error(err))
				}

				msg := fmt.Sprintf("<@%s> is matchmaking...", discordID)
				if len(discordIDs) > 0 {
					msg = fmt.Sprintf("%s along with %s...", msg, strings.Join(discordIDs, ", "))
				}

				embed := discordgo.MessageEmbed{
					Title:       "Matchmaking",
					Description: msg,
					Color:       0x00cc00,
				}

				if !matchID.IsNil() {
					embed.Footer = &discordgo.MessageEmbedFooter{
						Text: fmt.Sprintf("https://echo.taxi/spark://j/%s", strings.ToUpper(matchID.UUID().String())),
					}
				}

				// Notify the channel that this person started queuing
				message, err := bot.ChannelMessageSendEmbed(channelID, &embed)
				if err != nil {
					logger.Warn("Failed to send message", zap.Error(err))
				}
				go func() {
					// Delete the message when the player stops matchmaking
					select {
					case <-msession.Ctx.Done():
						if message != nil {
							err := bot.ChannelMessageDelete(channelID, message.ID)
							if err != nil {
								logger.Warn("Failed to delete message", zap.Error(err))
							}
						}
					case <-time.After(15 * time.Minute):
						if message != nil {
							err := bot.ChannelMessageDelete(channelID, message.ID)
							if err != nil {
								logger.Warn("Failed to delete message", zap.Error(err))
							}
						}
					}
				}()
			}
		}
		// Join any on-going combat match without delay

	case evr.ModeArenaPublic:

	default:
		return status.Errorf(codes.InvalidArgument, "invalid mode")
	}

	// If this is not the leader, then wait for the leader.
	if msession.Party != nil && msession.Party.GetLeader().GetSessionId() != session.id.String() {
		// try to join the leaders lobby the entire time.
		go FollowLeader(logger, msession, p.runtimeModule)

		// Hold for 4 minutes.
		select {
		case <-msession.Ctx.Done():
			return msession.Cause()
		case <-time.After(4 * time.Minute):
		}
		return msession.Cancel(ErrMatchmakingTimeout)
	}

	// Only solo players and leaders backfill
	if msession.Party == nil || msession.Party.GetLeader().SessionId == session.id.String() {
		go p.MatchBackfill(msession)
	}

	// Put a ticket in for matching
	return p.MatchMake(session, msession)
}

func (p *EvrPipeline) GetGuildPriorityList(ctx context.Context, userID uuid.UUID) (all []uuid.UUID, selected []uuid.UUID, err error) {

	currentGroupID, ok := ctx.Value(ctxGroupIDKey{}).(uuid.UUID)
	if !ok {
		return nil, nil, status.Errorf(codes.InvalidArgument, "Failed to get group ID from context")
	}

	// Get the guild priority from the context
	memberships, err := p.discordRegistry.GetGuildGroupMemberships(ctx, userID, nil)
	if err != nil {
		return nil, nil, status.Errorf(codes.Internal, "Failed to get guilds: %v", err)
	}

	// Sort the groups by size descending
	sort.Slice(memberships, func(i, j int) bool {
		return memberships[i].GuildGroup.Size() > memberships[j].GuildGroup.Size()
	})

	groupIDs := make([]uuid.UUID, 0)
	for _, group := range memberships {
		groupIDs = append(groupIDs, group.GuildGroup.ID())
	}

	guildPriority := make([]uuid.UUID, 0)
	params, ok := ctx.Value(ctxUrlParamsKey{}).(map[string][]string)
	if ok {
		// If the params are set, use them
		for _, gid := range params["guilds"] {
			for _, guildId := range strings.Split(gid, ",") {
				s := strings.Trim(guildId, " ")
				if s != "" {
					// Get the groupId for the guild
					groupIDstr, found := p.discordRegistry.Get(s)
					if !found {
						continue
					}
					groupID := uuid.FromStringOrNil(groupIDstr)
					if groupID != uuid.Nil && lo.Contains(groupIDs, groupID) {
						guildPriority = append(guildPriority, groupID)
					}
				}
			}
		}
	}

	if len(guildPriority) == 0 {
		// If the params are not set, use the user's guilds
		guildPriority = []uuid.UUID{currentGroupID}
		for _, groupID := range groupIDs {
			if groupID != currentGroupID {
				guildPriority = append(guildPriority, groupID)
			}
		}
	}

	p.logger.Debug("guild priorites", zap.Any("prioritized", guildPriority), zap.Any("all", groupIDs))
	return groupIDs, guildPriority, nil
}
