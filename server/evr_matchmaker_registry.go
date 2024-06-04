// Copyright 2022 The Nakama Authors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package server

import (
	"context"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gofrs/uuid/v5"
	"github.com/heroiclabs/nakama-common/api"
	"github.com/heroiclabs/nakama-common/runtime"
	"github.com/heroiclabs/nakama/v3/server/evr"
	"github.com/samber/lo"
	"go.uber.org/zap"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/wrapperspb"
)

const (
	findAttemptsExpiry          = time.Minute * 3
	LatencyCacheRefreshInterval = time.Hour * 3
	LatencyCacheExpiry          = time.Hour * 72 // 3 hours

	MatchmakingStorageCollection = "MatchmakingRegistry"
	LatencyCacheStorageKey       = "LatencyCache"
)

var (
	ErrMatchmakingPingTimeout        = status.Errorf(codes.DeadlineExceeded, "Ping timeout")
	ErrMatchmakingTimeout            = status.Errorf(codes.DeadlineExceeded, "Matchmaking timeout")
	ErrMatchmakingNoAvailableServers = status.Errorf(codes.Unavailable, "No available servers")
	ErrMatchmakingCanceled           = status.Errorf(codes.Canceled, "Matchmaking canceled")
	ErrMatchmakingCanceledByPlayer   = status.Errorf(codes.Canceled, "Matchmaking canceled by player")
	ErrMatchmakingRestarted          = status.Errorf(codes.Canceled, "matchmaking restarted")
	ErrMatchmakingMigrationRequired  = status.Errorf(codes.FailedPrecondition, "Server upgraded, migration")
	MatchmakingStreamSubject         = uuid.NewV5(uuid.Nil, "matchmaking").String()

	MatchmakingConfigStorageCollection = "Matchmaker"
	MatchmakingConfigStorageKey        = "config"
)

type LatencyMetric struct {
	Endpoint  evr.Endpoint
	RTT       time.Duration
	Timestamp time.Time
}

// String returns a string representation of the endpoint
func (e *LatencyMetric) String() string {
	return fmt.Sprintf("EndpointRTT(InternalIP=%s, ExternalIP=%s, RTT=%s, Timestamp=%s)", e.Endpoint.InternalIP, e.Endpoint.ExternalIP, e.RTT, e.Timestamp)
}

// ID returns a unique identifier for the endpoint
func (e *LatencyMetric) ID() string {
	return fmt.Sprintf("%s:%s", e.Endpoint.InternalIP.String(), e.Endpoint.ExternalIP.String())
}

// The key used for matchmaking properties
func (e *LatencyMetric) AsProperty() (string, float64) {
	k := fmt.Sprintf("rtt%s", ipToKey(e.Endpoint.ExternalIP))
	v := float64(e.RTT / time.Millisecond)
	return k, v
}

// LatencyCache is a cache of broadcaster RTTs for a user
type LatencyCache struct {
	MapOf[string, LatencyMetric]
}

func NewLatencyCache() *LatencyCache {
	return &LatencyCache{
		MapOf[string, LatencyMetric]{},
	}
}

func (c *LatencyCache) SelectPingCandidates(endpoints ...evr.Endpoint) []evr.Endpoint {
	// Initialize candidates with a capacity of 16
	metrics := make([]LatencyMetric, 0, len(endpoints))
	for _, endpoint := range endpoints {
		id := endpoint.ID()
		e, ok := c.Load(id)
		if !ok {
			e = LatencyMetric{
				Endpoint:  endpoint,
				RTT:       0,
				Timestamp: time.Now(),
			}
			c.Store(id, e)
		}
		metrics = append(metrics, e)
	}

	sort.SliceStable(endpoints, func(i, j int) bool {
		// sort by expired first
		if time.Since(metrics[i].Timestamp) > LatencyCacheRefreshInterval {
			// Then by those that have responded
			if metrics[j].RTT > 0 {
				// Then by timestamp
				return metrics[i].Timestamp.Before(metrics[j].Timestamp)
			}
		}
		// Otherwise, sort by RTT
		return metrics[i].RTT < metrics[j].RTT
	})

	if len(endpoints) == 0 {
		return []evr.Endpoint{}
	}
	if len(endpoints) > 16 {
		endpoints = endpoints[:16]
	}
	return endpoints
}

// FoundMatch represents the match found and send over the match join channel
type FoundMatch struct {
	MatchID       string
	Ticket        string // matchmaking ticket if any
	Query         string
	TeamIndex     TeamIndex
	KeepTeamIndex bool
}

type TicketMeta struct {
	TicketID  string
	Query     string
	Timestamp time.Time
}

// MatchmakingSession represents a user session looking for a match.
type MatchmakingSession struct {
	sync.RWMutex
	Ctx           context.Context
	CtxCancelFn   context.CancelCauseFunc
	Logger        *zap.Logger
	UserId        uuid.UUID
	MatchJoinCh   chan FoundMatch               // Channel for MatchId to join.
	PingResultsCh chan []evr.EndpointPingResult // Channel for ping completion.
	Expiry        time.Time
	Label         *EvrMatchState
	Tickets       map[string]TicketMeta // map[ticketId]TicketMeta
	Party         *PartyHandler
	LatencyCache  *LatencyCache
}

func (s *MatchmakingSession) metricsTags() map[string]string {
	return map[string]string{
		"type":  s.Label.LobbyType.String(),
		"mode":  s.Label.Mode.String(),
		"level": s.Label.Level.String(),
		"team":  strconv.FormatInt(int64(s.Label.TeamIndex), 10),
	}
}

// Cancel cancels the matchmaking session with a given reason, and returns the reason.
func (s *MatchmakingSession) Cancel(reason error) error {
	s.Lock()
	defer s.Unlock()
	select {
	case <-s.Ctx.Done():
		return nil
	default:
	}

	s.CtxCancelFn(reason)
	return nil
}

func (s *MatchmakingSession) AddTicket(ticket string, query string) {
	s.Lock()
	defer s.Unlock()
	ticketMeta := TicketMeta{
		TicketID:  ticket,
		Query:     query,
		Timestamp: time.Now(),
	}
	s.Tickets[ticket] = ticketMeta
}

func (s *MatchmakingSession) RemoveTicket(ticket string) {
	s.Lock()
	defer s.Unlock()
	delete(s.Tickets, ticket)
}

func (s *MatchmakingSession) Wait() error {
	<-s.Ctx.Done()
	return nil
}

func (s *MatchmakingSession) TicketsCount() int {
	s.RLock()
	defer s.RUnlock()
	return len(s.Tickets)
}

func (s *MatchmakingSession) Context() context.Context {
	s.RLock()
	defer s.RUnlock()
	return s.Ctx
}

// MatchmakingResult represents the outcome of a matchmaking request
type MatchmakingResult struct {
	err     error
	Message string
	Code    evr.LobbySessionFailureErrorCode
	Mode    evr.Symbol
	Channel uuid.UUID
	Logger  *zap.Logger
}

// NewMatchmakingResult initializes a new instance of MatchmakingResult
func NewMatchmakingResult(logger *zap.Logger, mode evr.Symbol, channel uuid.UUID) *MatchmakingResult {
	return &MatchmakingResult{
		Logger:  logger,
		Mode:    mode,
		Channel: channel,
	}
}

// SetErrorFromStatus updates the error and message from a status
func (mr *MatchmakingResult) SetErrorFromStatus(err error) *MatchmakingResult {
	if err == nil {
		return nil
	}
	mr.err = err

	if s, ok := status.FromError(err); ok {
		mr.Message = s.Message()
		mr.Code = determineErrorCode(s.Code())
	} else {
		mr.Message = err.Error()
		mr.Code = evr.LobbySessionFailure_InternalError
	}
	return mr
}

// determineErrorCode maps grpc status codes to evr lobby session failure codes
func determineErrorCode(code codes.Code) evr.LobbySessionFailureErrorCode {
	switch code {
	case codes.DeadlineExceeded:
		return evr.LobbySessionFailure_Timeout0
	case codes.InvalidArgument:
		return evr.LobbySessionFailure_BadRequest
	case codes.ResourceExhausted:
		return evr.LobbySessionFailure_ServerIsFull
	case codes.Unavailable:
		return evr.LobbySessionFailure_ServerFindFailed
	case codes.NotFound:
		return evr.LobbySessionFailure_ServerDoesNotExist
	case codes.PermissionDenied, codes.Unauthenticated:
		return evr.LobbySessionFailure_KickedFromLobbyGroup
	case codes.FailedPrecondition:
		return evr.LobbySessionFailure_UpdateRequired

	default:
		return evr.LobbySessionFailure_InternalError
	}
}

// SendErrorToSession sends an error message to a session
func (mr *MatchmakingResult) SendErrorToSession(s *sessionWS, err error) error {
	result := mr.SetErrorFromStatus(err)
	if result == nil {
		return nil
	}
	// If it was cancelled by the user, don't send and error
	if result.err == ErrMatchmakingCanceledByPlayer || result.err.Error() == "context canceled" {
		return nil
	}

	mr.Logger.Warn("Matchmaking error", zap.String("message", result.Message), zap.Error(result.err))
	return s.SendEvr(evr.NewLobbySessionFailure(result.Mode, result.Channel, result.Code, result.Message).Version4())
}

// MatchmakingRegistry is a registry for matchmaking sessions
type MatchmakingRegistry struct {
	sync.RWMutex

	ctx         context.Context
	ctxCancelFn context.CancelFunc

	nk            runtime.NakamaModule
	db            *sql.DB
	logger        *zap.Logger
	matchRegistry MatchRegistry
	metrics       Metrics
	config        Config
	evrPipeline   *EvrPipeline

	matchingBySession *MapOf[uuid.UUID, *MatchmakingSession]
	cacheByUserId     *MapOf[uuid.UUID, *LatencyCache]
	broadcasters      *MapOf[string, evr.Endpoint] // EndpointID -> Endpoint

}

func NewMatchmakingRegistry(logger *zap.Logger, matchRegistry MatchRegistry, matchmaker Matchmaker, metrics Metrics, db *sql.DB, nk runtime.NakamaModule, config Config, evrPipeline *EvrPipeline) *MatchmakingRegistry {
	ctx, ctxCancelFn := context.WithCancel(context.Background())
	c := &MatchmakingRegistry{
		ctx:         ctx,
		ctxCancelFn: ctxCancelFn,

		nk:            nk,
		db:            db,
		logger:        logger,
		matchRegistry: matchRegistry,
		metrics:       metrics,
		config:        config,

		matchingBySession: &MapOf[uuid.UUID, *MatchmakingSession]{},
		cacheByUserId:     &MapOf[uuid.UUID, *LatencyCache]{},
		broadcasters:      &MapOf[string, evr.Endpoint]{},
	}
	// Set the matchmaker's OnMatchedEntries callback
	matchmaker.OnMatchedEntries(c.matchedEntriesFn)
	go c.rebuildBroadcasters()

	return c
}

type MatchmakingSettings struct {
	BackfillQueryAddon   string     `json:"backfill_query_addon"`  // Additional query to add to the matchmaking query
	CreateQueryAddon     string     `json:"create_query_addon"`    // Additional query to add to the matchmaking query
	GroupID              string     `json:"group_id"`              // Group ID to matchmake with
	PriorityBroadcasters []string   `json:"priority_broadcasters"` // Prioritize these broadcasters
	NextMatchToken       MatchToken `json:"next_match_id"`         // Try to join this match immediately when finding a match
}

func (mr *MatchmakingRegistry) LoadMatchmakingSettings(ctx context.Context, logger *zap.Logger, userID string) (config MatchmakingSettings, err error) {
	return LoadMatchmakingSettings(ctx, NewRuntimeGoLogger(logger), mr.nk, userID)
}

func (mr *MatchmakingRegistry) StoreMatchmakingSettings(ctx context.Context, logger *zap.Logger, config MatchmakingSettings, userID string) error {
	return StoreMatchmakingSettings(mr.ctx, NewRuntimeGoLogger(logger), mr.nk, config, userID)
}

func LoadMatchmakingSettings(ctx context.Context, logger runtime.Logger, nk runtime.NakamaModule, userID string) (config MatchmakingSettings, err error) {

	objs, err := nk.StorageRead(ctx, []*runtime.StorageRead{
		{
			Collection: MatchmakingConfigStorageCollection,
			Key:        MatchmakingConfigStorageKey,
			UserID:     userID,
		},
	})
	if err != nil {
		logger.Error("Failed to read matchmaking config", zap.Error(err))
		return
	}

	if len(objs) == 0 {
		logger.Warn("No matchmaking config found, writing new one")
		err = StoreMatchmakingSettings(ctx, logger, nk, config, userID)
		if err != nil {
			logger.Error("Failed to write matchmaking config", zap.Error(err))
		}
		return
	}

	if err = json.Unmarshal([]byte(objs[0].Value), &config); err != nil {
		logger.Error("Failed to unmarshal matchmaking config", zap.Error(err))
		return
	}
	return
}

func StoreMatchmakingSettings(ctx context.Context, logger runtime.Logger, nk runtime.NakamaModule, config MatchmakingSettings, userID string) error {
	data, err := json.Marshal(config)
	if err != nil {
		logger.Error("Failed to marshal matchmaking config", zap.Error(err))
	}
	_, err = nk.StorageWrite(ctx, []*runtime.StorageWrite{
		{
			UserID:          userID,
			Collection:      MatchmakingConfigStorageCollection,
			Key:             MatchmakingConfigStorageKey,
			Value:           string(data),
			PermissionRead:  0,
			PermissionWrite: 0,
		},
	})
	if err != nil {
		logger.Error("Failed to write matchmaking config", zap.Error(err))
	}
	return err
}

func ipToKey(ip net.IP) string {
	b := ip.To4()
	return fmt.Sprintf("rtt%02x%02x%02x%02x", b[0], b[1], b[2], b[3])
}
func keyToIP(key string) net.IP {
	b, _ := hex.DecodeString(key[3:])
	return net.IPv4(b[0], b[1], b[2], b[3])
}

func (mr *MatchmakingRegistry) matchedEntriesFn(entries [][]*MatchmakerEntry) {
	// Get the matchmaking config from the storage
	config, err := mr.LoadMatchmakingSettings(mr.ctx, mr.logger, SystemUserID)
	if err != nil {
		mr.logger.Error("Failed to load matchmaking config", zap.Error(err))
		return
	}

	for _, entrants := range entries {
		go mr.buildMatch(entrants, config)
	}
}

func (mr *MatchmakingRegistry) listUnfilledLobbies(ctx context.Context, logger *zap.Logger, searchLabel *EvrMatchState) ([]*EvrMatchState, string, error) {
	var err error
	var query string

	var labels []*EvrMatchState

	if searchLabel.LobbyType != PublicLobby {
		// Do not backfill for private lobbies
		return nil, "", status.Errorf(codes.InvalidArgument, "Cannot backfill private lobbies")
	}

	// TODO Move this into the matchmaking registry
	query = buildMatchQueryFromLabel(searchLabel)

	// Basic search defaults
	const (
		minSize = 1
		maxSize = MatchMaxSize - 1 // the broadcaster is included, so this has one free spot
		limit   = 50
	)

	logger = logger.With(zap.String("query", query))

	// Search for possible matches
	logger.Debug("Searching for matches")
	matches, err := mr.listMatches(ctx, limit, minSize, maxSize, query)
	if err != nil {
		return nil, "", status.Errorf(codes.Internal, "Failed to find matches: %v", err)
	}

	// Create a label slice of the matches
	labels = make([]*EvrMatchState, 0, len(matches))
	for _, match := range matches {
		label := &EvrMatchState{}
		if err := json.Unmarshal([]byte(match.GetLabel().GetValue()), label); err != nil {
			logger.Error("Error unmarshalling match label", zap.Error(err))
			continue
		}
		labels = append(labels, label)
	}

	return labels, query, nil
}

func (mr *MatchmakingRegistry) buildMatch(entrants []*MatchmakerEntry, config MatchmakingSettings) {
	logger := mr.logger.With(zap.Int("entrants", len(entrants)))

	logger.Debug("Building match", zap.Any("entrants", entrants))
	// Use the properties from the first entrant to get the channel

	channelMap := make(map[uuid.UUID]int, len(entrants))
	for _, e := range entrants {
		channel := uuid.FromStringOrNil(e.StringProperties["channel"])
		if channel == uuid.Nil {
			continue
		}
		channelMap[channel]++
	}

	channels := make([]uuid.UUID, 0, len(channelMap))
	for k := range channelMap {
		channels = append(channels, k)
	}

	sort.SliceStable(channels, func(i, j int) bool {
		return channelMap[channels[i]] > channelMap[channels[j]]
	})

	// Get a map of all broadcasters by their key
	broadcastersByExtIP := make(map[string]evr.Endpoint, 100)
	mr.broadcasters.Range(func(k string, v evr.Endpoint) bool {
		broadcastersByExtIP[k] = v
		return true
	})

	// Create a map of each endpoint and it's latencies to each entrant
	latencies := make(map[string][]int, 100)
	for _, e := range entrants {
		nprops := e.NumericProperties
		//sprops := e.StringProperties

		// loop over the number props and get the latencies
		for k, v := range nprops {
			if strings.HasPrefix(k, "rtt") {
				latencies[k] = append(latencies[k], int(v))
			}
		}
	}

	// Score each endpoint based on the latencies
	scored := make(map[string]int, len(latencies))
	for k, v := range latencies {
		// Sort the latencies
		sort.Ints(v)
		// Get the average
		average := 0
		for _, i := range v {
			average += i
		}
		average /= len(v)
		scored[k] = average
	}
	// Sort the scored endpoints
	sorted := make([]string, 0, len(scored))
	for k := range scored {
		sorted = append(sorted, k)
	}
	sort.SliceStable(sorted, func(i, j int) bool {
		return scored[sorted[i]] < scored[sorted[j]]
	})

	parties := make(map[string][]*MatchmakerEntry, 8)
	for _, e := range entrants {
		id := e.GetPartyId()
		parties[id] = append(parties[id], e)
	}

	// Get the ml from the first participant
	var ml *EvrMatchState

	for _, e := range entrants {
		if s, ok := mr.GetMatchingBySessionId(e.Presence.SessionID); ok {
			ml = s.Label
			break
		}
	}
	if ml == nil {
		mr.logger.Error("No label found")
		return // No label found
	}

	// Try to backfill matches with parties
	var backfillMatches []*EvrMatchState
	backfillMatches, _, err := mr.listUnfilledLobbies(mr.ctx, logger, ml)
	if err != nil {
		logger.Error("Failed to list unfilled lobbies", zap.Error(err))
		return
	}

	for partyID, party := range parties {
		partySize := len(party)
		if partySize == 1 {
			continue
		}

		for i, m := range backfillMatches {
			if m.PlayerLimit-m.Size < partySize {
				continue
			}
			// Make sure adding these players makes the teams the same size
			if (m.Size+partySize)%2 != 0 {
				continue
			}
			logger.Info("Backfilling match", zap.String("matchID", m.MatchID.String()), zap.String("partyID", partyID), zap.Any("party", party))
			// Add the party to the match
			for _, e := range party {
				// Remove the player from the entrants list
				for j, e2 := range entrants {
					if e2.Presence.SessionID == e.Presence.SessionID {
						entrants = append(entrants[:j], entrants[j+1:]...)
						break
					}
				}

				msession, ok := mr.GetMatchingBySessionId(e.Presence.SessionID)
				if !ok {
					logger.Warn("Could not find matching session for user", zap.String("sessionID", e.Presence.SessionID.String()))
					continue
				}

				err := joinPlayerToMatch(logger, msession, m.MatchID.String()+"."+m.Node)
				if err != nil {
					logger.Error("Error joining player to match", zap.Error(err))
				}
			}

			// Remove the party from the party list
			delete(parties, partyID)
			// Set the backfill match size to the new size
			backfillMatches[i].Size += partySize

		}
	}

	teams := distributeParties(lo.Values(parties))
	if len(teams) != 2 {
		logger.Error("Invalid number of teams", zap.Int("teams", len(teams)))
		return
	}
	ml.Players = make([]PlayerInfo, 0, len(entrants))
	for i, players := range teams {
		for _, p := range players {
			// Update the label with the teams
			evrID, err := evr.ParseEvrId(p.StringProperties["evr_id"])
			if err != nil {
				logger.Error("Failed to parse evr id", zap.Error(err))
				continue
			}
			pi := PlayerInfo{
				Team:  TeamIndex(i),
				EvrID: *evrID,
			}
			ml.Players = append(ml.Players, pi)
		}
	}
	metricsTags := map[string]string{
		"type":     strconv.FormatInt(int64(ml.LobbyType), 10),
		"mode":     ml.Mode.String(),
		"channel":  ml.Channel.String(),
		"level":    ml.Level.String(),
		"team_idx": strconv.FormatInt(int64(ml.TeamIndex), 10),
	}
	mr.metrics.CustomCounter("matchmaking_matched_participant_count", metricsTags, int64(len(entrants)))
	// Find a valid participant to get the label from

	ml.SpawnedBy = uuid.Nil.String()

	// Loop until a server becomes available or matchmaking times out.
	timeout := time.After(10 * time.Minute)
	interval := time.NewTicker(10 * time.Second)

	select {
	case <-mr.ctx.Done(): // Context cancelled
		return
	default:
	}
	matchID := ""

	for {
		var err error
		matchID, err = mr.allocateBroadcaster(channels, config, sorted, ml)
		if err != nil {
			mr.logger.Warn("Error allocating broadcaster", zap.Error(err))
		}
		if matchID != "" {
			break
		}

		select {
		case <-mr.ctx.Done(): // Context cancelled
			return

		case <-timeout: // Timeout

			mr.logger.Info("Matchmaking timeout looking for an available server")
			for _, e := range entrants {
				s, ok := mr.GetMatchingBySessionId(e.Presence.SessionID)
				if !ok {
					logger.Warn("Could not find matching session for user", zap.String("sessionID", e.Presence.SessionID.String()))
					continue
				}
				s.CtxCancelFn(ErrMatchmakingNoAvailableServers)
			}
			return
		case <-interval.C: // List all the unassigned lobbies on this channel
			// Check if the match is still unassigned

		}
	}
	// Assign the teams to the match, taking one from each team at a time
	// and sending the join instruction to the server

	if matchID == "" {
		logger.Error("No match ID found")
		return
	}

	for i, players := range teams {
		// Assign each player in the team to the match
		for _, entry := range players {
			s, ok := mr.GetMatchingBySessionId(entry.Presence.SessionID)
			if !ok {
				logger.Warn("Could not find matching session for user", zap.String("sessionID", entry.Presence.SessionID.String()))
				continue
			}
			// Get the ticket metadata
			ticket := entry.GetTicket()
			ticketMeta, ok := s.Tickets[ticket]
			if !ok {
				logger.Warn("Could not find ticket metadata for user", zap.String("sessionID", entry.Presence.SessionID.String()), zap.String("ticket", ticket))
				continue
			}

			foundMatch := FoundMatch{
				MatchID:       matchID,
				Ticket:        ticketMeta.TicketID,
				Query:         ticketMeta.Query,
				TeamIndex:     TeamIndex(i),
				KeepTeamIndex: true,
			}
			// Send the join instruction
			select {
			case <-s.Ctx.Done():
				logger.Warn("Matchmaking session cancelled", zap.String("sessionID", entry.Presence.SessionID.String()))
				continue
			case s.MatchJoinCh <- foundMatch:
				logger.Info("Sent match join instruction", zap.String("sessionID", entry.Presence.SessionID.String()))
			case <-time.After(2 * time.Second):
				logger.Warn("Failed to send match join instruction", zap.String("sessionID", entry.Presence.SessionID.String()))
			}
		}
	}
}

func joinPlayerToMatch(logger *zap.Logger, msession *MatchmakingSession, matchID string) error {
	foundMatch := FoundMatch{
		MatchID:   matchID,
		TeamIndex: TeamIndex(-1),
	}
	// Send the join instruction
	select {
	case <-msession.Ctx.Done():
		return ErrMatchmakingCanceled
	case msession.MatchJoinCh <- foundMatch:
		return nil
	case <-time.After(2 * time.Second):
		return status.Errorf(codes.DeadlineExceeded, "Failed to send match join instruction")
	}
	return nil
}

func distributeParties(parties [][]*MatchmakerEntry) [][]*MatchmakerEntry {
	// Distribute the players from each party on the two teams.
	// Try to keep the parties together, but the teams must be balanced.
	// The algorithm is greedy and may not always produce the best result.
	// Each team must be 4 players or less
	teams := [][]*MatchmakerEntry{{}, {}}

	// Sort the parties by size, single players last
	sort.SliceStable(parties, func(i, j int) bool {
		if len(parties[i]) == 1 {
			return false
		}
		return len(parties[i]) < len(parties[j])
	})

	// Distribute the parties to the teams
	for _, party := range parties {
		// Find the team with the least players
		team := 0
		for i, t := range teams {
			if len(t) < len(teams[team]) {
				team = i
			}
		}
		teams[team] = append(teams[team], party...)
	}
	// sort the teams by size
	sort.SliceStable(teams, func(i, j int) bool {
		return len(teams[i]) > len(teams[j])
	})

	for i, player := range teams[0] {
		// If the team is more than two players larger than the other team, distribute the players evenly
		if len(teams[0]) > len(teams[1])+1 {
			// Move a player from teams[0] to teams[1]
			teams[1] = append(teams[1], player)
			teams[0] = append(teams[0][:i], teams[0][i+1:]...)
		}
	}

	return teams
}

func (mr *MatchmakingRegistry) allocateBroadcaster(channels []uuid.UUID, config MatchmakingSettings, sorted []string, label *EvrMatchState) (string, error) {
	// Lock the broadcasters so that they aren't double allocated
	mr.Lock()
	defer mr.Unlock()
	available, err := mr.ListUnassignedLobbies(mr.ctx, channels)
	if err != nil {
		return "", err
	}

	availableByExtIP := make(map[string]string, len(available))

	for _, label := range available {
		k := ipToKey(label.Broadcaster.Endpoint.ExternalIP)
		v := fmt.Sprintf("%s.%s", label.MatchID, mr.config.GetName()) // Parking match ID
		availableByExtIP[k] = v
	}

	// Convert the priority broadcasters to a list of rtt's
	priority := make([]string, 0, len(config.PriorityBroadcasters))
	for i, ip := range config.PriorityBroadcasters {
		priority[i] = ipToKey(net.ParseIP(ip))
	}

	sorted = append(priority, sorted...)

	var matchID string
	var found bool
	for _, k := range sorted {
		// Get the endpoint
		matchID, found = availableByExtIP[k]
		if !found {
			continue
		}
		break
	}
	// Found a match
	label.SpawnedBy = SystemUserID
	// Instruct the server to prepare the level
	response, err := SignalMatch(mr.ctx, mr.matchRegistry, matchID, SignalPrepareSession, label)
	if err != nil {
		return "", fmt.Errorf("error signaling match: %s: %v", response, err)
	}
	// Instruct the server to load the level
	response, err = SignalMatch(mr.ctx, mr.matchRegistry, matchID, SignalStartSession, label)
	if err != nil {
		return "", fmt.Errorf("error signaling match: %s: %v", response, err)
	}
	if response != "session started" {
		return "", fmt.Errorf("error signaling match: %s", response)
	}

	return matchID, nil
}

func (c *MatchmakingRegistry) rebuildBroadcasters() {
	ticker := time.NewTicker(20 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-c.ctx.Done():
			return
		case <-ticker.C:
			c.updateBroadcasters()
		}
	}
}

func (c *MatchmakingRegistry) updateBroadcasters() {
	matches, err := c.listMatches(c.ctx, 1000, 1, MatchMaxSize, "")
	if err != nil {
		c.logger.Error("Error listing matches", zap.Error(err))
	}
	// Get the endpoints from the match labels
	for _, match := range matches {
		l := match.GetLabel().GetValue()
		s := &EvrMatchState{}
		if err := json.Unmarshal([]byte(l), s); err != nil {
			c.logger.Error("Error unmarshalling match label", zap.Error(err))
			continue
		}
		id := s.Broadcaster.Endpoint.ID()
		c.broadcasters.Store(id, s.Broadcaster.Endpoint)
	}
}

func (c *MatchmakingRegistry) ListUnassignedLobbies(ctx context.Context, channels []uuid.UUID) ([]*EvrMatchState, error) {

	qparts := make([]string, 0, 10)

	// MUST be an unassigned lobby
	qparts = append(qparts, LobbyType(evr.UnassignedLobby).Query(Must, 0))

	if len(channels) > 0 {
		// MUST be hosting for this channel
		qparts = append(qparts, HostedChannels(channels).Query(Must, 0))
	}

	// TODO FIXME Add version lock and appid
	query := strings.Join(qparts, " ")
	c.logger.Debug("Listing unassigned lobbies", zap.String("query", query))
	limit := 200
	minSize, maxSize := 1, 1 // Only the 1 broadcaster should be there.
	matches, err := c.listMatches(ctx, limit, minSize, maxSize, query)
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

// GetPingCandidates returns a list of endpoints to ping for a user. It also updates the broadcasters.
func (m *MatchmakingSession) GetPingCandidates(endpoints ...evr.Endpoint) (candidates []evr.Endpoint) {

	const LatencyCacheExpiry = 6 * time.Hour

	// Initialize candidates with a capacity of 16
	candidates = make([]evr.Endpoint, 0, 16)

	// Return early if there are no endpoints
	if len(endpoints) == 0 {
		return candidates
	}

	// Retrieve the user's cache and lock it for use
	cache := m.LatencyCache

	// Get the endpoint latencies from the cache
	entries := make([]LatencyMetric, 0, len(endpoints))

	// Iterate over the endpoints, and load/create their cache entry
	for _, endpoint := range endpoints {
		id := endpoint.ID()

		e, ok := cache.Load(id)
		if !ok {
			e = LatencyMetric{
				Endpoint:  endpoint,
				RTT:       0,
				Timestamp: time.Now(),
			}
			cache.Store(id, e)
		}
		entries = append(entries, e)
	}

	// If there are no cache entries, return the empty endpoints
	if len(entries) == 0 {
		return candidates
	}

	// Sort the cache entries by timestamp in descending order.
	// This will prioritize the oldest endpoints first.
	sort.SliceStable(entries, func(i, j int) bool {
		return entries[i].RTT == 0 && entries[i].Timestamp.Before(entries[j].Timestamp)
	})

	// Fill the rest of the candidates with the oldest entries
	for _, e := range entries {
		endpoint := e.Endpoint
		candidates = append(candidates, endpoint)
		if len(candidates) >= 16 {
			break
		}
	}

	/*
		// Clean up the cache by removing expired entries
		for id, e := range cache.Store {
			if time.Since(e.Timestamp) > LatencyCacheExpiry {
				delete(cache.Store, id)
			}
		}
	*/

	return candidates
}

// GetCache returns the latency cache for a user
func (r *MatchmakingRegistry) GetCache(userId uuid.UUID) *LatencyCache {
	cache, _ := r.cacheByUserId.LoadOrStore(userId, &LatencyCache{})
	return cache
}

// UpdateBroadcasters updates the broadcasters map
func (r *MatchmakingRegistry) UpdateBroadcasters(endpoints []evr.Endpoint) {
	for _, e := range endpoints {
		r.broadcasters.Store(e.ID(), e)
	}
}

// listMatches returns a list of matches
func (c *MatchmakingRegistry) listMatches(ctx context.Context, limit int, minSize, maxSize int, query string) ([]*api.Match, error) {
	authoritativeWrapper := &wrapperspb.BoolValue{Value: true}
	var labelWrapper *wrapperspb.StringValue
	var queryWrapper *wrapperspb.StringValue
	if query != "" {
		queryWrapper = &wrapperspb.StringValue{Value: query}
	}
	minSizeWrapper := &wrapperspb.Int32Value{Value: int32(minSize)}

	maxSizeWrapper := &wrapperspb.Int32Value{Value: int32(maxSize)}

	matches, _, err := c.matchRegistry.ListMatches(ctx, limit, authoritativeWrapper, labelWrapper, minSizeWrapper, maxSizeWrapper, queryWrapper, nil)
	return matches, err
}

type LatencyCacheStorageObject struct {
	Entries map[string]LatencyMetric `json:"entries"`
}

// LoadLatencyCache loads the latency cache for a user
func (c *MatchmakingRegistry) LoadLatencyCache(ctx context.Context, logger *zap.Logger, session *sessionWS, msession *MatchmakingSession) (*LatencyCache, error) {
	// Load the latency cache
	// retrieve the document from storage
	userId := session.UserID()
	// Get teh user's latency cache
	cache := c.GetCache(userId)
	result, err := StorageReadObjects(ctx, logger, session.pipeline.db, uuid.Nil, []*api.ReadStorageObjectId{
		{
			Collection: MatchmakingStorageCollection,
			Key:        LatencyCacheStorageKey,
			UserId:     userId.String(),
		},
	})
	if err != nil {
		logger.Error("failed to read objects", zap.Error(err))
		return nil, status.Errorf(codes.Internal, "Failed to read latency cache: %v", err)
	}

	objs := result.Objects
	if len(objs) != 0 {
		store := &LatencyCacheStorageObject{}
		if err := json.Unmarshal([]byte(objs[0].Value), store); err != nil {
			return nil, status.Errorf(codes.Internal, "Failed to unmarshal latency cache: %v", err)
		}
		// Load the entries into the cache
		for k, v := range store.Entries {
			cache.Store(k, v)

		}
	}

	return cache, nil
}

// StoreLatencyCache stores the latency cache for a user
func (c *MatchmakingRegistry) StoreLatencyCache(session *sessionWS) {
	cache := c.GetCache(session.UserID())

	if cache == nil {
		return
	}

	store := &LatencyCacheStorageObject{
		Entries: make(map[string]LatencyMetric, 100),
	}

	cache.Range(func(k string, v LatencyMetric) bool {
		store.Entries[k] = v
		return true
	})

	// Save the latency cache
	jsonBytes, err := json.Marshal(store)
	if err != nil {
		session.logger.Error("Failed to marshal latency cache", zap.Error(err))
		return
	}
	data := string(jsonBytes)
	ops := StorageOpWrites{
		{
			OwnerID: session.UserID().String(),
			Object: &api.WriteStorageObject{
				Collection:      MatchmakingStorageCollection,
				Key:             LatencyCacheStorageKey,
				Value:           data,
				PermissionRead:  &wrapperspb.Int32Value{Value: int32(0)},
				PermissionWrite: &wrapperspb.Int32Value{Value: int32(0)},
			},
		},
	}
	if _, _, err = StorageWriteObjects(context.Background(), session.logger, session.pipeline.db, session.metrics, session.storageIndex, true, ops); err != nil {
		session.logger.Error("Failed to save latency cache", zap.Error(err))
	}
}

// Stop stops the matchmaking registry
func (c *MatchmakingRegistry) Stop() {
	c.ctxCancelFn()
}

// GetMatchingBySessionId returns the matching session for a given session ID
func (c *MatchmakingRegistry) GetMatchingBySessionId(sessionId uuid.UUID) (session *MatchmakingSession, ok bool) {
	session, ok = c.matchingBySession.Load(sessionId)
	return session, ok
}

// Delete removes a matching session from the registry
func (c *MatchmakingRegistry) Delete(sessionId uuid.UUID) {
	c.matchingBySession.Delete(sessionId)
}

// Add adds a matching session to the registry
func (c *MatchmakingRegistry) Create(ctx context.Context, logger *zap.Logger, session *sessionWS, ml *EvrMatchState, partySize int, timeout time.Duration, errorFn func(err error) error, joinFn func(matchId string, query string) error) (*MatchmakingSession, error) {
	// Check if there is an existing session
	if _, ok := c.GetMatchingBySessionId(session.ID()); ok {
		// Cancel it
		c.Cancel(session.ID(), ErrMatchmakingCanceledByPlayer)
	}

	// Set defaults for the matching label
	ml.Open = true // Open for joining
	ml.MaxSize = MatchMaxSize

	// Set defaults for public matches
	switch {
	case ml.Mode == evr.ModeSocialPrivate || ml.Mode == evr.ModeSocialPublic:
		ml.Level = evr.LevelSocial // Include the level in the search
		ml.TeamSize = MatchMaxSize
		ml.Size = MatchMaxSize - partySize

	case ml.Mode == evr.ModeArenaPublic:
		ml.TeamSize = 4
		ml.Size = ml.TeamSize*2 - partySize // Both teams, minus the party size

	case ml.Mode == evr.ModeCombatPublic:
		ml.TeamSize = 5
		ml.Size = ml.TeamSize*2 - partySize // Both teams, minus the party size

	default: // Privates
		ml.Size = MatchMaxSize - partySize
		ml.TeamSize = 5
	}

	logger = logger.With(zap.String("msid", session.ID().String()))
	ctx, cancel := context.WithCancelCause(ctx)
	msession := &MatchmakingSession{
		Ctx:         ctx,
		CtxCancelFn: cancel,

		Logger:        logger,
		UserId:        session.UserID(),
		MatchJoinCh:   make(chan FoundMatch, 5),
		PingResultsCh: make(chan []evr.EndpointPingResult),
		Expiry:        time.Now().UTC().Add(findAttemptsExpiry),
		Label:         ml,
		Tickets:       make(map[string]TicketMeta),
	}

	// Load the latency cache
	cache, err := c.LoadLatencyCache(ctx, logger, session, msession)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "Failed to load latency cache: %v", err)
	}
	msession.LatencyCache = cache

	// listen for a match ID to join
	go func() {
		// Create a timer for this session
		startedAt := time.Now().UTC()
		metricsTags := map[string]string{
			"mode":    ml.Mode.String(),
			"channel": ml.Channel.String(),
			"level":   ml.Level.String(),
			"team":    strconv.FormatInt(int64(ml.TeamIndex), 10),
			"result":  "success",
		}
		defer func() {
			c.metrics.CustomTimer("matchmaking_session_duration_ms", metricsTags, time.Since(startedAt)*time.Millisecond)
		}()

		defer cancel(nil)
		var err error
		select {
		case <-ctx.Done():
			if ctx.Err() != nil {
				err = context.Cause(ctx)
			}
		case <-time.After(timeout):
			// Timeout
			err = ErrMatchmakingTimeout
			c.metrics.CustomCounter("matchmaking_session_timeout_count", metricsTags, 1)
		case matchFound := <-msession.MatchJoinCh:
			// join the match
			err = joinFn(matchFound.MatchID, matchFound.Query)

			c.metrics.CustomCounter("matchmaking_session_success_count", metricsTags, 1)

		}
		if err != nil {
			if err == ErrMatchmakingCanceledByPlayer {
				metricsTags["result"] = "canceled"
			} else {
				metricsTags["result"] = "error"
				defer errorFn(err)
			}
		}
		c.StoreLatencyCache(session)
		c.Delete(session.id)
		if err := session.matchmaker.RemoveSessionAll(session.id.String()); err != nil {
			logger.Error("Failed to remove session from matchmaker", zap.Error(err))
		}
	}()
	c.Add(session.id, msession)
	return msession, nil
}

func (c *MatchmakingRegistry) Cancel(sessionId uuid.UUID, reason error) {
	if session, ok := c.GetMatchingBySessionId(sessionId); ok {
		c.logger.Debug("Canceling matchmaking session", zap.String("reason", reason.Error()))
		session.Cancel(reason)
	}
}

func (c *MatchmakingRegistry) Add(id uuid.UUID, s *MatchmakingSession) {
	c.matchingBySession.Store(id, s)

}

// GetLatencies returns the cached latencies for a user
func (c *MatchmakingRegistry) GetLatencies(userId uuid.UUID, endpoints []evr.Endpoint) map[string]LatencyMetric {

	// If the user ID is nil or there are no endpoints, return nil
	if userId == uuid.Nil {
		return nil
	}
	// Create endpoint id slice
	endpointIds := make([]string, 0, len(endpoints))
	for _, e := range endpoints {
		endpointIds = append(endpointIds, e.ID())
	}

	cache := c.GetCache(userId)

	// If no endpoints are provided, get all the latencies from the cache
	// build a new map to avoid locking the cache for too long
	if len(endpointIds) == 0 {
		results := make(map[string]LatencyMetric, 100)
		cache.Range(func(k string, v LatencyMetric) bool {
			results[k] = v
			return true
		})
		return results
	}
	// Return just the endpoints requested
	results := make(map[string]LatencyMetric, len(endpointIds))
	for _, id := range endpointIds {
		endpoint := evr.FromEndpointID(id)
		// Create the endpoint and add it to the cache
		e := LatencyMetric{
			Endpoint:  endpoint,
			RTT:       0,
			Timestamp: time.Now(),
		}
		r, _ := cache.LoadOrStore(id, e)
		results[id] = r
	}
	return results
}

func (ms *MatchmakingSession) BuildQuery(latencies []LatencyMetric) (query string, stringProps map[string]string, numericProps map[string]float64, err error) {
	// Create the properties maps
	stringProps = make(map[string]string)
	numericProps = make(map[string]float64, len(latencies))
	qparts := make([]string, 0, 10)
	ml := ms.Label
	// SHOULD match this channel
	chstr := strings.Replace(ms.Label.Channel.String(), "-", "", -1)
	qparts = append(qparts, fmt.Sprintf("properties.channel:%s^3", chstr))
	stringProps["channel"] = chstr

	// Add this user's ID to the string props
	stringProps["userid"] = strings.Replace(ms.UserId.String(), "-", "", -1)

	// Add a property of the external IP's RTT
	for _, b := range latencies {
		if b.RTT == 0 {
			continue
		}

		latency := mroundRTT(b.RTT, 10)
		// Turn the IP into a hex string like 127.0.0.1 -> 7f000001
		ip := ipToKey(b.Endpoint.ExternalIP)
		// Add the property
		numericProps[ip] = float64(latency.Milliseconds())

		// TODO FIXME Add second matchmaking ticket for over 150ms
		n := 0
		switch {
		case latency < 25:
			n = 10
		case latency < 40:
			n = 7
		case latency < 60:
			n = 5
		case latency < 80:
			n = 2

			// Add a score for each endpoint
			p := fmt.Sprintf("properties.%s:<=%d^%d", ip, latency+15, n)
			qparts = append(qparts, p)
		}
	}
	// MUST be the same mode
	qparts = append(qparts, GameMode(ml.Mode).Label(Must, 0).Property())
	stringProps["mode"] = ml.Mode.Token().String()

	for _, groupId := range ml.Broadcaster.Channels {
		// Add the properties
		// Strip out the hyphens from the group ID
		s := strings.ReplaceAll(groupId.String(), "-", "")
		stringProps[s] = "T"

		// If this is the user's current channel, then give it a +3 boost
		if groupId == *ms.Label.Channel {
			qparts = append(qparts, fmt.Sprintf("properties.%s:T^3", s))
		} else {
			qparts = append(qparts, fmt.Sprintf("properties.%s:T", s))
		}
	}

	query = strings.Join(qparts, " ")
	// TODO Promote friends
	ms.Logger.Debug("Matchmaking query", zap.String("query", query), zap.Any("stringProps", stringProps), zap.Any("numericProps", numericProps))
	// TODO Avoid ghosted
	return query, stringProps, numericProps, nil
}
