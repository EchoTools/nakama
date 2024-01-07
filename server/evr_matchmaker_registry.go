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
	"encoding/json"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/gofrs/uuid/v5"
	"github.com/heroiclabs/nakama-common/api"
	"github.com/heroiclabs/nakama/v3/server/evr"
	"github.com/samber/lo"
	"go.uber.org/zap"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/wrapperspb"
)

const (
	findAttemptsExpiry = time.Minute * 3
	LatencyCacheExpiry = time.Minute * 3 * 60 // 3 hours

	MatchmakingStorageCollection = "MatchmakingRegistry"
	LatencyCacheStorageKey       = "LatencyCache"
)

var (
	ErrMatchmakingPingTimeout        = fmt.Errorf("ping timeout")
	ErrMatchmakingTimeout            = fmt.Errorf("matchmaking timeout")
	ErrMatchmakingNoAvailableServers = fmt.Errorf("no available servers")
	ErrMatchmakingCancelled          = fmt.Errorf("matchmaking cancelled")
	ErrMatchmakingRestarted          = fmt.Errorf("matchmaking restarted")
)

// MatchmakingSession represents a user session looking for a match.
type MatchmakingSession struct {
	sync.RWMutex
	UserId         uuid.UUID
	Ctx            context.Context
	CtxCancelFn    context.CancelCauseFunc
	JoinMatchCh    chan string // Channel for MatchId to join.
	PingCompleteCh chan error  // Channel for ping completion.
	Expiry         time.Time
	Label          *EvrMatchState
	Tickets        []string // Matchmaking tickets
}

// Cancel cancels the matchmaking session with a given reason.
func (s *MatchmakingSession) Cancel(reason error) {
	s.Lock()
	defer s.Unlock()

	// Check if context is already done before cancelling.
	if s.Ctx.Err() != nil {
		return
	}

	s.CtxCancelFn(reason)
}

func (s *MatchmakingSession) AddTicket(ticket string) {
	s.Lock()
	defer s.Unlock()
	s.Tickets = append(s.Tickets, ticket)
}

func (s *MatchmakingSession) RemoveTicket(ticket string) {
	s.Lock()
	defer s.Unlock()
	for i, t := range s.Tickets {
		if t == ticket {
			s.Tickets = append(s.Tickets[:i], s.Tickets[i+1:]...)
			return
		}
	}
}

// LatencyCache represents a cache for latencies.
type LatencyCache struct {
	sync.RWMutex
	Store map[string]*EndpointWithLatency
}

// NewLatencyCache initializes a new latency cache.
func NewLatencyCache() *LatencyCache {
	return &LatencyCache{
		Store: make(map[string]*EndpointWithLatency),
	}
}

// MatchmakingResult represents the outcome of a matchmaking request
type MatchmakingResult struct {
	err     error
	Message string
	Code    evr.LobbySessionFailureErrorCode
	Mode    evr.Symbol
	Channel uuid.UUID
}

// NewMatchmakingResult initializes a new instance of MatchmakingResult
func NewMatchmakingResult(mode evr.Symbol, channel uuid.UUID) *MatchmakingResult {
	return &MatchmakingResult{
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
	case codes.FailedPrecondition, codes.InvalidArgument:
		return evr.LobbySessionFailure_BadRequest
	case codes.ResourceExhausted:
		return evr.LobbySessionFailure_ServerIsFull
	case codes.Unavailable, codes.NotFound:
		return evr.LobbySessionFailure_ServerFindFailed
	case codes.PermissionDenied, codes.Unauthenticated:
		return evr.LobbySessionFailure_KickedFromLobbyGroup
	default:
		return evr.LobbySessionFailure_InternalError
	}
}

// SendErrorToSession sends an error message to a session
func (mr *MatchmakingResult) SendErrorToSession(s *sessionWS, err error) error {
	result := mr.SetErrorFromStatus(err)
	payload := []evr.Message{
		evr.NewLobbySessionFailure(result.Mode, result.Channel, result.Code, result.Message).Version4(),
	}
	return s.SendEvr(payload)
}

// EndpointWithLatency is a struct that holds an endpoint and its latency
type EndpointWithLatency struct {
	Endpoint  *evr.Endpoint
	RTT       time.Duration
	Timestamp time.Time
}

// String returns a string representation of the endpoint
func (e *EndpointWithLatency) String() string {
	return fmt.Sprintf("EndpointWithLatency(InternalIP=%s, ExternalIP=%s, RTT=%s, Timestamp=%s)", e.Endpoint.InternalIP, e.Endpoint.ExternalIP, e.RTT, e.Timestamp)
}

// ID returns a unique identifier for the endpoint
func (e *EndpointWithLatency) ID() string {
	s := fmt.Sprintf("%s:%s", e.Endpoint.InternalIP, e.Endpoint.ExternalIP)
	return s
}

// MatchmakingRegistryConfig is a configuration for the matchmaking registry
type MatchmakingRegistryConfig struct {
	Logger        *zap.Logger
	MatchRegistry MatchRegistry
	Metrics       Metrics
}

// MatchRegistry is a registry for matches
func NewMatchmakingRegistryConfig(logger *zap.Logger, matchRegistry MatchRegistry, metrics Metrics) *MatchmakingRegistryConfig {
	return &MatchmakingRegistryConfig{
		Logger:        logger,
		MatchRegistry: matchRegistry,
		Metrics:       metrics,
	}
}

// MatchmakingRegistry is a registry for matchmaking sessions
type MatchmakingRegistry struct {
	sync.RWMutex
	ctx         context.Context
	ctxCancelFn context.CancelFunc

	logger        *zap.Logger
	matchRegistry MatchRegistry
	metrics       Metrics

	matchingBySession map[uuid.UUID]*MatchmakingSession
	cacheByUserId     map[uuid.UUID]*LatencyCache
	broadcasters      map[string]*evr.Endpoint
}

func NewMatchmakingRegistry(cfg *MatchmakingRegistryConfig) *MatchmakingRegistry {
	ctx, ctxCancelFn := context.WithCancel(context.Background())

	c := &MatchmakingRegistry{
		ctx:         ctx,
		ctxCancelFn: ctxCancelFn,

		logger:        cfg.Logger,
		matchRegistry: cfg.MatchRegistry,
		metrics:       cfg.Metrics,

		matchingBySession: make(map[uuid.UUID]*MatchmakingSession, 200),
		broadcasters:      make(map[string]*evr.Endpoint, 200),
		cacheByUserId:     make(map[uuid.UUID]*LatencyCache, 1000),
	}

	go c.rebuildBroadcasters()

	return c
}

func (c *MatchmakingRegistry) rebuildBroadcasters() {
	ticker := time.NewTicker(1 * time.Minute)
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
	matches, err := c.listMatches(c.ctx, 1000, true, "", lo.ToPtr(1), lo.ToPtr(MaxMatchSize), "")
	if err != nil {
		c.logger.Error("Error listing matches", zap.Error(err))
	}
	endpoints := make(map[string]*evr.Endpoint)
	// Get the endpoints from the match labels
	for _, match := range matches {
		l := match.GetLabel().GetValue()
		s := &EvrMatchState{}
		if err := json.Unmarshal([]byte(l), s); err != nil {
			c.logger.Error("Error unmarshalling match label", zap.Error(err))
			continue
		}
		id := s.Endpoint.ID()
		endpoints[id] = &s.Endpoint
	}

	c.Lock()
	c.broadcasters = endpoints
	c.Unlock()
}

// GetPingCandidates returns a list of endpoints to ping for a user. It also updates the broadcasters.
func (r *MatchmakingRegistry) GetPingCandidates(userId uuid.UUID, endpoints []*evr.Endpoint) (candidates []*evr.Endpoint) {

	const LatencyCacheExpiry = 5 * time.Minute

	// Initialize candidates with a capacity of 16
	candidates = make([]*evr.Endpoint, 0, 16)

	// Return early if there are no endpoints
	if len(endpoints) == 0 {
		return candidates
	}

	// Update the existing broadcaster list with these endpoints
	r.UpdateBroadcasters(endpoints)

	// Retrieve the user's cache and lock it for use
	cache := r.GetCache(userId)
	cache.Lock()
	defer cache.Unlock()

	// Get the endpoint latencies from the cache
	cacheEntries := make([]*EndpointWithLatency, 0, len(endpoints))
	// Iterate over the endpoints
	for _, endpoint := range endpoints {
		if endpoint == nil {
			continue
		}

		id := endpoint.ID()
		entry, exists := cache.Store[id]
		if !exists || entry.RTT == 0 || time.Since(entry.Timestamp) > LatencyCacheExpiry {
			entry = &EndpointWithLatency{
				Endpoint:  endpoint,
				RTT:       0,
				Timestamp: time.Now(),
			}
			cache.Store[id] = entry
		}

		cacheEntries = append(cacheEntries, entry)
	}

	/*
		// Clean up the cache by removing expired entries
		for id, entry := range cache.Store {
			if time.Since(entry.Timestamp) > LatencyCacheExpiry {
				delete(cache.Store, id)
			}
		}
	*/

	// If there are no cache entries, return the empty endpoints
	if len(cacheEntries) == 0 {
		return candidates
	}

	// Sort the cache entries by timestamp in descending order.
	// This will prioritize the oldest endpoints first.
	r.sortCacheEntriesByTimestamp(cacheEntries)

	// Fill the rest of the candidates with the oldest entries
	for _, e := range cacheEntries {
		endpoint := e.Endpoint
		candidates = append(candidates, endpoint)
		if len(candidates) >= 16 {
			break
		}
	}
	return candidates
}

// sortCacheEntriesByTimestamp sorts the cache entries by timestamp in descending order
func (r *MatchmakingRegistry) sortCacheEntriesByTimestamp(entries []*EndpointWithLatency) {
	sort.SliceStable(entries, func(i, j int) bool {
		return entries[i].Timestamp.After(entries[j].Timestamp)
	})
}

// GetCache returns the latency cache for a user
func (r *MatchmakingRegistry) GetCache(userId uuid.UUID) *LatencyCache {
	r.Lock()
	defer r.Unlock()
	cache, ok := r.cacheByUserId[userId]
	if !ok {
		cache = NewLatencyCache()
		r.cacheByUserId[userId] = cache
	}
	return cache
}

// UpdateBroadcasters updates the broadcasters map
func (r *MatchmakingRegistry) UpdateBroadcasters(endpoints []*evr.Endpoint) {
	r.Lock()
	defer r.Unlock()
	for _, e := range endpoints {
		r.broadcasters[e.ID()] = e
	}
}

// listMatches returns a list of matches
func (c *MatchmakingRegistry) listMatches(ctx context.Context, limit int, authoritative bool, label string, minSize, maxSize *int, query string) ([]*api.Match, error) {
	authoritativeWrapper := &wrapperspb.BoolValue{Value: authoritative}
	var labelWrapper *wrapperspb.StringValue
	if label != "" {
		labelWrapper = &wrapperspb.StringValue{Value: label}
	}
	var queryWrapper *wrapperspb.StringValue
	if query != "" {
		queryWrapper = &wrapperspb.StringValue{Value: query}
	}
	var minSizeWrapper *wrapperspb.Int32Value
	if minSize != nil {
		minSizeWrapper = &wrapperspb.Int32Value{Value: int32(*minSize)}
	}
	var maxSizeWrapper *wrapperspb.Int32Value
	if maxSize != nil {
		maxSizeWrapper = &wrapperspb.Int32Value{Value: int32(*maxSize)}
	}
	matches, _, err := c.matchRegistry.ListMatches(ctx, limit, authoritativeWrapper, labelWrapper, minSizeWrapper, maxSizeWrapper, queryWrapper, nil)
	return matches, err
}

// LoadLatencyCache loads the latency cache for a user
func (c *MatchmakingRegistry) LoadLatencyCache(ctx context.Context, logger *zap.Logger, session *sessionWS, msession *MatchmakingSession) error {
	// Load the latency cache
	// retrieve the document from storage
	userId := session.UserID()
	// Get teh user's latency cache
	cache := c.GetCache(userId)
	cache.Lock()
	defer cache.Unlock()
	result, err := StorageReadObjects(ctx, logger, session.pipeline.db, uuid.Nil, []*api.ReadStorageObjectId{
		{
			Collection: MatchmakingStorageCollection,
			Key:        LatencyCacheStorageKey,
			UserId:     userId.String(),
		},
	})
	if err != nil {
		logger.Error("SNSDocumentRequest: failed to read objects", zap.Error(err))
		return status.Errorf(codes.Internal, "Failed to read latency cache: %v", err)
	}

	objs := result.Objects
	if len(objs) != 0 {
		// Latency cache was found. Unmarshal it.
		if err := json.Unmarshal([]byte(objs[0].Value), &cache.Store); err != nil {
			return status.Errorf(codes.Internal, "Failed to unmarshal latency cache: %v", err)
		}
	}

	return nil
}

// StoreLatencyCache stores the latency cache for a user
func (c *MatchmakingRegistry) StoreLatencyCache(session *sessionWS) {
	cache := c.GetCache(session.UserID())

	if cache == nil {
		return
	}

	cache.RLock()
	defer cache.RUnlock()

	// Save the latency cache
	jsonBytes, err := json.Marshal(cache.Store)
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
	c.RLock()
	defer c.RUnlock()
	session, ok = c.matchingBySession[sessionId]
	return
}

// Delete removes a matching session from the registry
func (c *MatchmakingRegistry) Delete(sessionId uuid.UUID) {
	c.Lock()
	defer c.Unlock()
	delete(c.matchingBySession, sessionId)
}

// Add adds a matching session to the registry
func (c *MatchmakingRegistry) Create(ctx context.Context, session *sessionWS, ml *EvrMatchState, partySize int) (*MatchmakingSession, error) {
	// Cancel the existing session if it exists
	c.Cancel(session.ID(), ErrMatchmakingCancelled)

	// Set defaults for the matching label
	ml.Open = true // Open for joining
	ml.MaxSize = MaxMatchSize

	// Set defaults for public matches
	switch {
	case ml.Mode == evr.ModeSocialPrivate || ml.Mode == evr.ModeSocialPublic:
		ml.Level = evr.LevelSocial // Include the level in the search
		ml.MaxTeamSize = MaxMatchSize
		ml.Size = MaxMatchSize

	case ml.Mode == evr.ModeArenaPublic || ml.Mode == evr.ModeCombatPublic:
		ml.MaxTeamSize = 4
		ml.Size = ml.MaxTeamSize*2 - partySize // Both teams, minus the party size

	default: // Privates
		ml.Size = MaxMatchSize - partySize
		ml.MaxTeamSize = 5
	}

	// Add the user's accessible groups/guilds
	userGroups, err := ListUserGroups(ctx, session.logger, session.pipeline.db, session.UserID(), 1000, wrapperspb.Int32(2), "")
	if err != nil {
		return nil, status.Errorf(codes.Internal, "Failed to list user groups: %v", err)
	}

	// Get the channels the user has access to
	for _, g := range userGroups.GetUserGroups() {
		// Verify the group is a guild
		if g.Group.GetLangTag() != "guild" {
			continue
		}
		cid := uuid.FromStringOrNil(g.Group.GetId())
		if cid == uuid.Nil {
			continue
		}
		ml.BroadcasterChannels = append(ml.BroadcasterChannels, cid)
	}

	ctx, cancel := context.WithCancelCause(ctx)
	msession := &MatchmakingSession{
		UserId:         session.UserID(),
		Ctx:            ctx,
		CtxCancelFn:    cancel,
		JoinMatchCh:    make(chan string, 1),
		PingCompleteCh: make(chan error, 1),
		Expiry:         time.Now().UTC().Add(findAttemptsExpiry),
		Label:          ml,
	}

	// Load the latency cache
	if err := c.LoadLatencyCache(ctx, session.logger, session, msession); err != nil {
		return nil, status.Errorf(codes.Internal, "Failed to load latency cache: %v", err)
	}

	go func() {
		<-ctx.Done()
		// Store the latency values
		c.StoreLatencyCache(session)
		// Remove the session from the registry
		c.Delete(session.id)
	}()

	c.Add(session.id, msession)
	return msession, nil
}

func (c *MatchmakingRegistry) Cancel(sessionId uuid.UUID, reason error) {
	if session, ok := c.GetMatchingBySessionId(sessionId); ok {
		session.Cancel(reason)
	}
}

func (c *MatchmakingRegistry) Add(id uuid.UUID, s *MatchmakingSession) {
	c.Lock()
	defer c.Unlock()
	c.matchingBySession[id] = s

}

// BroadcasterEndpoints returns the broadcaster endpoints
func (c *MatchmakingRegistry) BroadcasterEndpoints() map[string]*evr.Endpoint {
	c.RLock()
	defer c.RUnlock()
	return c.broadcasters
}

// ProcessPingResults adds latencies for a userId It takes ping responses.
func (c *MatchmakingRegistry) ProcessPingResults(userId uuid.UUID, responses []evr.EndpointPingResult) {
	// Get the user's cache
	cache := c.GetCache(userId)
	broadcasters := c.BroadcasterEndpoints()

	cache.Lock()
	defer cache.Unlock()
	// Look up the endpoint in the cache and update the latency

	// Add the latencies to the cache
	for _, response := range responses {
		broadcaster, ok := broadcasters[response.EndpointID()]
		if !ok {
			c.logger.Warn("Endpoint not found in cache", zap.String("endpoint", response.EndpointID()))
			continue
		}

		r := &EndpointWithLatency{
			Endpoint:  broadcaster,
			RTT:       response.RTT(),
			Timestamp: time.Now(),
		}

		cache.Store[r.ID()] = r
	}
}

// GetLatencies returns the cached latencies for a user
func (c *MatchmakingRegistry) GetLatencies(userId uuid.UUID, endpoints []*evr.Endpoint) map[string]*EndpointWithLatency {

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
	cache.Lock()
	defer cache.Unlock()

	// If no endpoints are provided, get all the latencies from the cache
	// build a new map to avoid locking the cache for too long
	if len(endpointIds) == 0 {
		results := make(map[string]*EndpointWithLatency, len(cache.Store))
		for k, v := range cache.Store {
			results[k] = v
		}
		return results
	}
	// Return just the endpoints requested
	results := make(map[string]*EndpointWithLatency, len(endpointIds))
	for _, id := range endpointIds {
		if v, ok := cache.Store[id]; ok {
			results[id] = v
		} else {
			// Parse the endpoint ID
			endpoint := evr.FromEndpointID(id)
			// Create the endpoint and add it to the cache
			results[id] = &EndpointWithLatency{
				Endpoint:  &endpoint,
				RTT:       0,
				Timestamp: time.Now(),
			}
			cache.Store[id] = results[id]
		}
	}
	return results
}
