// Copyright 2024 The Nakama Authors
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
	"sync"
	syncAtomic "sync/atomic"
	"time"

	"github.com/go-redis/redis"
	"github.com/gofrs/uuid/v5"
	"github.com/heroiclabs/nakama-common/rtapi"
	"github.com/heroiclabs/nakama-common/runtime"
	"go.uber.org/atomic"
	"go.uber.org/zap"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/types/known/wrapperspb"
)

const (
	// Redis key prefixes for tracker
	presenceKeyPrefix      = "presence:"
	streamPresencesPrefix  = "stream_presences:"
	sessionPresencesPrefix = "session_presences:"
	trackerPubSubPrefix    = "tracker_events:"

	// Pub/Sub message types for tracker
	trackerMsgTypeJoin   = "join"
	trackerMsgTypeLeave  = "leave"
	trackerMsgTypeUpdate = "update"
)

// PresenceData represents serializable presence data stored in Redis
type PresenceData struct {
	Node        string        `json:"node"`
	SessionID   string        `json:"session_id"`
	UserID      string        `json:"user_id"`
	StreamMode  uint8         `json:"stream_mode"`
	StreamSubj  string        `json:"stream_subject"`
	StreamSubc  string        `json:"stream_subcontext"`
	StreamLabel string        `json:"stream_label"`
	Format      SessionFormat `json:"format"`
	Hidden      bool          `json:"hidden"`
	Persistence bool          `json:"persistence"`
	Username    string        `json:"username"`
	Status      string        `json:"status"`
	Reason      uint32        `json:"reason"`
}

// TrackerEvent represents a pub/sub message for tracker operations
type TrackerEvent struct {
	Type     string        `json:"type"`
	Presence *PresenceData `json:"presence"`
	Node     string        `json:"node"`
}

// ClusterTracker implements Tracker with Redis-backed distributed state
type ClusterTracker struct {
	sync.RWMutex
	logger             *zap.Logger
	config             *ClusterConfig
	nodeName           string
	redisClient        *redis.Client
	channelPrefix      string
	sessionRegistry    SessionRegistry
	statusRegistry     StatusRegistry
	metrics            Metrics
	protojsonMarshaler *protojson.MarshalOptions

	matchJoinListener  func(id uuid.UUID, joins []*MatchPresence)
	matchLeaveListener func(id uuid.UUID, leaves []*MatchPresence)
	partyJoinListener  func(id uuid.UUID, joins []*Presence)
	partyLeaveListener func(id uuid.UUID, leaves []*Presence)

	// Local presence storage (presences physically on this node)
	presencesByStream  map[uint8]map[PresenceStream]map[presenceCompact]*Presence
	presencesBySession map[uuid.UUID]map[presenceCompact]*Presence
	count              *atomic.Int64
	eventsCh           chan *PresenceEvent

	ctx         context.Context
	ctxCancelFn context.CancelFunc
}

// StartClusterTracker creates a new distributed tracker
func StartClusterTracker(
	ctx context.Context,
	logger *zap.Logger,
	config Config,
	clusterConfig *ClusterConfig,
	sessionRegistry SessionRegistry,
	statusRegistry StatusRegistry,
	metrics Metrics,
	protojsonMarshaler *protojson.MarshalOptions,
	redisClient *redis.Client,
) Tracker {
	ctx, ctxCancelFn := context.WithCancel(ctx)

	t := &ClusterTracker{
		logger:             logger,
		config:             clusterConfig,
		nodeName:           config.GetName(),
		redisClient:        redisClient,
		channelPrefix:      clusterConfig.ChannelPrefix,
		sessionRegistry:    sessionRegistry,
		statusRegistry:     statusRegistry,
		metrics:            metrics,
		protojsonMarshaler: protojsonMarshaler,

		presencesByStream:  make(map[uint8]map[PresenceStream]map[presenceCompact]*Presence),
		presencesBySession: make(map[uuid.UUID]map[presenceCompact]*Presence),
		count:              atomic.NewInt64(0),
		eventsCh:           make(chan *PresenceEvent, config.GetTracker().EventQueueSize),

		ctx:         ctx,
		ctxCancelFn: ctxCancelFn,
	}

	// Start event processor
	go t.processEvents()

	// Start pub/sub listener for cross-node presence events
	go t.subscribeToEvents()

	logger.Info("Cluster tracker initialized",
		zap.String("node", config.GetName()),
		zap.String("redis_address", clusterConfig.RedisAddress))

	return t
}

// processEvents handles local presence events
func (t *ClusterTracker) processEvents() {
	ticker := time.NewTicker(15 * time.Second)
	for {
		select {
		case <-t.ctx.Done():
			return
		case e := <-t.eventsCh:
			t.processEvent(e)
		case <-ticker.C:
			t.metrics.GaugePresences(float64(t.count.Load()))
		}
	}
}

// subscribeToEvents listens for presence events from other nodes
func (t *ClusterTracker) subscribeToEvents() {
	// Subscribe to a broadcast channel that all nodes receive
	channel := fmt.Sprintf("%s:%sbroadcast", t.channelPrefix, trackerPubSubPrefix)
	pubsub := t.redisClient.Subscribe(channel)
	defer pubsub.Close()

	t.logger.Debug("Subscribed to tracker events channel", zap.String("channel", channel))

	ch := pubsub.Channel()
	for {
		select {
		case <-t.ctx.Done():
			return
		case msg, ok := <-ch:
			if !ok {
				return
			}

			var event TrackerEvent
			if err := json.Unmarshal([]byte(msg.Payload), &event); err != nil {
				t.logger.Warn("Failed to unmarshal tracker event", zap.Error(err))
				continue
			}

			// Skip events from our own node
			if event.Node == t.nodeName {
				continue
			}

			t.handleRemoteEvent(&event)
		}
	}
}

// handleRemoteEvent processes presence events from other nodes
func (t *ClusterTracker) handleRemoteEvent(event *TrackerEvent) {
	if event.Presence == nil {
		return
	}

	sessionID, err := uuid.FromString(event.Presence.SessionID)
	if err != nil {
		return
	}

	userID, err := uuid.FromString(event.Presence.UserID)
	if err != nil {
		return
	}

	streamSubject, _ := uuid.FromString(event.Presence.StreamSubj)
	streamSubcontext, _ := uuid.FromString(event.Presence.StreamSubc)

	stream := PresenceStream{
		Mode:       event.Presence.StreamMode,
		Subject:    streamSubject,
		Subcontext: streamSubcontext,
		Label:      event.Presence.StreamLabel,
	}

	meta := PresenceMeta{
		Format:      event.Presence.Format,
		Hidden:      event.Presence.Hidden,
		Persistence: event.Presence.Persistence,
		Username:    event.Presence.Username,
		Status:      event.Presence.Status,
		Reason:      event.Presence.Reason,
	}

	presence := &Presence{
		ID: PresenceID{
			Node:      event.Presence.Node,
			SessionID: sessionID,
		},
		Stream: stream,
		UserID: userID,
		Meta:   meta,
	}

	switch event.Type {
	case trackerMsgTypeJoin:
		// A presence joined on another node, add it to our local view if needed for stream lookups
		t.addRemotePresence(presence)
	case trackerMsgTypeLeave:
		// A presence left on another node, remove it from our local view
		t.removeRemotePresence(presence)
	case trackerMsgTypeUpdate:
		// A presence was updated on another node
		t.updateRemotePresence(presence)
	}
}

// addRemotePresence adds a presence from another node to the local view
func (t *ClusterTracker) addRemotePresence(p *Presence) {
	pc := presenceCompact{ID: p.ID, Stream: p.Stream, UserID: p.UserID}

	t.Lock()
	defer t.Unlock()

	// Update tracking for stream
	byStreamMode, ok := t.presencesByStream[p.Stream.Mode]
	if !ok {
		byStreamMode = make(map[PresenceStream]map[presenceCompact]*Presence)
		t.presencesByStream[p.Stream.Mode] = byStreamMode
	}

	if byStream, ok := byStreamMode[p.Stream]; !ok {
		byStream = make(map[presenceCompact]*Presence)
		byStream[pc] = p
		byStreamMode[p.Stream] = byStream
		t.count.Inc()
	} else if _, exists := byStream[pc]; !exists {
		byStream[pc] = p
		t.count.Inc()
	}
}

// removeRemotePresence removes a presence from another node from the local view
func (t *ClusterTracker) removeRemotePresence(p *Presence) {
	pc := presenceCompact{ID: p.ID, Stream: p.Stream, UserID: p.UserID}

	t.Lock()
	defer t.Unlock()

	byStreamMode, ok := t.presencesByStream[p.Stream.Mode]
	if !ok {
		return
	}

	byStream, ok := byStreamMode[p.Stream]
	if !ok {
		return
	}

	if _, exists := byStream[pc]; exists {
		delete(byStream, pc)
		t.count.Dec()

		if len(byStream) == 0 {
			delete(byStreamMode, p.Stream)
			if len(byStreamMode) == 0 {
				delete(t.presencesByStream, p.Stream.Mode)
			}
		}
	}
}

// updateRemotePresence updates a presence from another node in the local view
func (t *ClusterTracker) updateRemotePresence(p *Presence) {
	pc := presenceCompact{ID: p.ID, Stream: p.Stream, UserID: p.UserID}

	t.Lock()
	defer t.Unlock()

	byStreamMode, ok := t.presencesByStream[p.Stream.Mode]
	if !ok {
		return
	}

	byStream, ok := byStreamMode[p.Stream]
	if !ok {
		return
	}

	if _, exists := byStream[pc]; exists {
		byStream[pc] = p
	}
}

// publishEvent broadcasts a tracker event to all nodes
func (t *ClusterTracker) publishEvent(eventType string, presence *Presence) {
	presenceData := &PresenceData{
		Node:        presence.ID.Node,
		SessionID:   presence.ID.SessionID.String(),
		UserID:      presence.UserID.String(),
		StreamMode:  presence.Stream.Mode,
		StreamSubj:  presence.Stream.Subject.String(),
		StreamSubc:  presence.Stream.Subcontext.String(),
		StreamLabel: presence.Stream.Label,
		Format:      presence.Meta.Format,
		Hidden:      presence.Meta.Hidden,
		Persistence: presence.Meta.Persistence,
		Username:    presence.Meta.Username,
		Status:      presence.Meta.Status,
		Reason:      syncAtomic.LoadUint32(&presence.Meta.Reason),
	}

	event := &TrackerEvent{
		Type:     eventType,
		Presence: presenceData,
		Node:     t.nodeName,
	}

	data, err := json.Marshal(event)
	if err != nil {
		t.logger.Warn("Failed to marshal tracker event", zap.Error(err))
		return
	}

	channel := fmt.Sprintf("%s:%sbroadcast", t.channelPrefix, trackerPubSubPrefix)
	if err := t.redisClient.Publish(channel, data).Err(); err != nil {
		t.logger.Warn("Failed to publish tracker event", zap.Error(err))
	}
}

// streamKey returns the Redis key for a stream's presence set
func (t *ClusterTracker) streamKey(stream PresenceStream) string {
	return fmt.Sprintf("%s:%s%d:%s:%s:%s", t.channelPrefix, streamPresencesPrefix,
		stream.Mode, stream.Subject.String(), stream.Subcontext.String(), stream.Label)
}

func (t *ClusterTracker) SetMatchJoinListener(f func(id uuid.UUID, joins []*MatchPresence)) {
	t.matchJoinListener = f
}

func (t *ClusterTracker) SetMatchLeaveListener(f func(id uuid.UUID, leaves []*MatchPresence)) {
	t.matchLeaveListener = f
}

func (t *ClusterTracker) SetPartyJoinListener(f func(id uuid.UUID, joins []*Presence)) {
	t.partyJoinListener = f
}

func (t *ClusterTracker) SetPartyLeaveListener(f func(id uuid.UUID, leaves []*Presence)) {
	t.partyLeaveListener = f
}

func (t *ClusterTracker) Stop() {
	t.ctxCancelFn()
}

func (t *ClusterTracker) getSession(sessionID uuid.UUID) Session {
	session := t.sessionRegistry.Get(sessionID)
	if session != nil {
		session.CloseLock()
	}
	return session
}

func (t *ClusterTracker) Track(ctx context.Context, sessionID uuid.UUID, stream PresenceStream, userID uuid.UUID, meta PresenceMeta) (bool, bool) {
	if session := t.getSession(sessionID); session == nil {
		return false, false
	} else {
		defer session.CloseUnlock()
	}

	syncAtomic.StoreUint32(&meta.Reason, uint32(runtime.PresenceReasonJoin))
	pc := presenceCompact{ID: PresenceID{Node: t.nodeName, SessionID: sessionID}, Stream: stream, UserID: userID}
	p := &Presence{ID: PresenceID{Node: t.nodeName, SessionID: sessionID}, Stream: stream, UserID: userID, Meta: meta}

	t.Lock()

	select {
	case <-ctx.Done():
		t.Unlock()
		return false, false
	default:
	}

	// See if this session has any presences tracked at all.
	if bySession, anyTracked := t.presencesBySession[sessionID]; anyTracked {
		// Then see if the exact presence we need is tracked.
		if _, alreadyTracked := bySession[pc]; !alreadyTracked {
			// If the current session had others tracked, but not this presence.
			bySession[pc] = p
		} else {
			t.Unlock()
			return true, false
		}
	} else {
		// If nothing at all was tracked for the current session, begin tracking.
		bySession = make(map[presenceCompact]*Presence)
		bySession[pc] = p
		t.presencesBySession[sessionID] = bySession
	}
	t.count.Inc()

	// Update tracking for stream.
	byStreamMode, ok := t.presencesByStream[stream.Mode]
	if !ok {
		byStreamMode = make(map[PresenceStream]map[presenceCompact]*Presence)
		t.presencesByStream[stream.Mode] = byStreamMode
	}

	if byStream, ok := byStreamMode[stream]; !ok {
		byStream = make(map[presenceCompact]*Presence)
		byStream[pc] = p
		byStreamMode[stream] = byStream
	} else {
		byStream[pc] = p
	}

	t.Unlock()

	// Publish to Redis for other nodes
	t.publishEvent(trackerMsgTypeJoin, p)

	if !meta.Hidden {
		t.queueEvent([]*Presence{p}, nil)
	}
	return true, true
}

func (t *ClusterTracker) TrackMulti(ctx context.Context, sessionID uuid.UUID, ops []*TrackerOp, userID uuid.UUID) bool {
	if session := t.getSession(sessionID); session == nil {
		return false
	} else {
		defer session.CloseUnlock()
	}

	joins := make([]*Presence, 0, len(ops))
	t.Lock()

	select {
	case <-ctx.Done():
		t.Unlock()
		return false
	default:
	}

	for _, op := range ops {
		syncAtomic.StoreUint32(&op.Meta.Reason, uint32(runtime.PresenceReasonJoin))
		pc := presenceCompact{ID: PresenceID{Node: t.nodeName, SessionID: sessionID}, Stream: op.Stream, UserID: userID}
		p := &Presence{ID: PresenceID{Node: t.nodeName, SessionID: sessionID}, Stream: op.Stream, UserID: userID, Meta: op.Meta}

		// See if this session has any presences tracked at all.
		if bySession, anyTracked := t.presencesBySession[sessionID]; anyTracked {
			// Then see if the exact presence we need is tracked.
			if _, alreadyTracked := bySession[pc]; !alreadyTracked {
				// If the current session had others tracked, but not this presence.
				bySession[pc] = p
			} else {
				continue
			}
		} else {
			// If nothing at all was tracked for the current session, begin tracking.
			bySession = make(map[presenceCompact]*Presence)
			bySession[pc] = p
			t.presencesBySession[sessionID] = bySession
		}
		t.count.Inc()

		// Update tracking for stream.
		byStreamMode, ok := t.presencesByStream[op.Stream.Mode]
		if !ok {
			byStreamMode = make(map[PresenceStream]map[presenceCompact]*Presence)
			t.presencesByStream[op.Stream.Mode] = byStreamMode
		}

		if byStream, ok := byStreamMode[op.Stream]; !ok {
			byStream = make(map[presenceCompact]*Presence)
			byStream[pc] = p
			byStreamMode[op.Stream] = byStream
		} else {
			byStream[pc] = p
		}

		// Publish to Redis for other nodes
		t.publishEvent(trackerMsgTypeJoin, p)

		if !op.Meta.Hidden {
			joins = append(joins, p)
		}
	}
	t.Unlock()

	if len(joins) != 0 {
		t.queueEvent(joins, nil)
	}
	return true
}

func (t *ClusterTracker) Untrack(sessionID uuid.UUID, stream PresenceStream, userID uuid.UUID) {
	pc := presenceCompact{ID: PresenceID{Node: t.nodeName, SessionID: sessionID}, Stream: stream, UserID: userID}
	t.Lock()

	bySession, anyTracked := t.presencesBySession[sessionID]
	if !anyTracked {
		t.Unlock()
		return
	}
	p, found := bySession[pc]
	if !found {
		t.Unlock()
		return
	}

	// Update the tracking for session.
	if len(bySession) == 1 {
		delete(t.presencesBySession, sessionID)
	} else {
		delete(bySession, pc)
	}
	t.count.Dec()

	// Update the tracking for stream.
	if byStreamMode := t.presencesByStream[stream.Mode]; len(byStreamMode) == 1 {
		if byStream := byStreamMode[stream]; len(byStream) == 1 {
			delete(t.presencesByStream, stream.Mode)
		} else {
			delete(byStream, pc)
		}
	} else {
		if byStream := byStreamMode[stream]; len(byStream) == 1 {
			delete(byStreamMode, stream)
		} else {
			delete(byStream, pc)
		}
	}

	t.Unlock()

	// Publish to Redis for other nodes
	syncAtomic.StoreUint32(&p.Meta.Reason, uint32(runtime.PresenceReasonLeave))
	t.publishEvent(trackerMsgTypeLeave, p)

	if !p.Meta.Hidden {
		t.queueEvent(nil, []*Presence{p})
	}
}

func (t *ClusterTracker) UntrackMulti(sessionID uuid.UUID, streams []*PresenceStream, userID uuid.UUID) {
	leaves := make([]*Presence, 0, len(streams))
	t.Lock()

	for _, stream := range streams {
		pc := presenceCompact{ID: PresenceID{Node: t.nodeName, SessionID: sessionID}, Stream: *stream, UserID: userID}

		bySession, anyTracked := t.presencesBySession[sessionID]
		if !anyTracked {
			t.Unlock()
			return
		}
		p, found := bySession[pc]
		if !found {
			continue
		}

		// Update the tracking for session.
		if len(bySession) == 1 {
			delete(t.presencesBySession, sessionID)
		} else {
			delete(bySession, pc)
		}
		t.count.Dec()

		// Update the tracking for stream.
		if byStreamMode := t.presencesByStream[stream.Mode]; len(byStreamMode) == 1 {
			if byStream := byStreamMode[*stream]; len(byStream) == 1 {
				delete(t.presencesByStream, stream.Mode)
			} else {
				delete(byStream, pc)
			}
		} else {
			if byStream := byStreamMode[*stream]; len(byStream) == 1 {
				delete(byStreamMode, *stream)
			} else {
				delete(byStream, pc)
			}
		}

		// Publish to Redis for other nodes
		syncAtomic.StoreUint32(&p.Meta.Reason, uint32(runtime.PresenceReasonLeave))
		t.publishEvent(trackerMsgTypeLeave, p)

		if !p.Meta.Hidden {
			leaves = append(leaves, p)
		}
	}
	t.Unlock()

	if len(leaves) != 0 {
		t.queueEvent(nil, leaves)
	}
}

func (t *ClusterTracker) UntrackAll(sessionID uuid.UUID, reason runtime.PresenceReason) {
	t.Lock()

	bySession, anyTracked := t.presencesBySession[sessionID]
	if !anyTracked {
		t.Unlock()
		return
	}

	leaves := make([]*Presence, 0, len(bySession))
	for pc, p := range bySession {
		// Update the tracking for stream.
		if byStreamMode := t.presencesByStream[pc.Stream.Mode]; len(byStreamMode) == 1 {
			if byStream := byStreamMode[pc.Stream]; len(byStream) == 1 {
				delete(t.presencesByStream, pc.Stream.Mode)
			} else {
				delete(byStream, pc)
			}
		} else {
			if byStream := byStreamMode[pc.Stream]; len(byStream) == 1 {
				delete(byStreamMode, pc.Stream)
			} else {
				delete(byStream, pc)
			}
		}

		syncAtomic.StoreUint32(&p.Meta.Reason, uint32(reason))

		// Publish to Redis for other nodes
		t.publishEvent(trackerMsgTypeLeave, p)

		if !p.Meta.Hidden {
			leaves = append(leaves, p)
		}

		t.count.Dec()
	}
	delete(t.presencesBySession, sessionID)

	t.Unlock()
	if len(leaves) != 0 {
		t.queueEvent(nil, leaves)
	}
}

func (t *ClusterTracker) Update(ctx context.Context, sessionID uuid.UUID, stream PresenceStream, userID uuid.UUID, meta PresenceMeta) bool {
	if session := t.getSession(sessionID); session == nil {
		return false
	} else {
		defer session.CloseUnlock()
	}

	syncAtomic.StoreUint32(&meta.Reason, uint32(runtime.PresenceReasonUpdate))
	pc := presenceCompact{ID: PresenceID{Node: t.nodeName, SessionID: sessionID}, Stream: stream, UserID: userID}
	p := &Presence{ID: PresenceID{Node: t.nodeName, SessionID: sessionID}, Stream: stream, UserID: userID, Meta: meta}
	t.Lock()

	select {
	case <-ctx.Done():
		t.Unlock()
		return false
	default:
	}

	bySession, anyTracked := t.presencesBySession[sessionID]
	if !anyTracked {
		bySession = make(map[presenceCompact]*Presence)
		t.presencesBySession[sessionID] = bySession
	}

	previousP, alreadyTracked := bySession[pc]
	bySession[pc] = p
	if !alreadyTracked {
		t.count.Inc()
	}

	// Update tracking for stream.
	byStreamMode, ok := t.presencesByStream[stream.Mode]
	if !ok {
		byStreamMode = make(map[PresenceStream]map[presenceCompact]*Presence)
		t.presencesByStream[stream.Mode] = byStreamMode
	}

	if byStream, ok := byStreamMode[stream]; !ok {
		byStream = make(map[presenceCompact]*Presence)
		byStream[pc] = p
		byStreamMode[stream] = byStream
	} else {
		byStream[pc] = p
	}

	t.Unlock()

	// Publish to Redis for other nodes
	t.publishEvent(trackerMsgTypeUpdate, p)

	if !meta.Hidden || (alreadyTracked && !previousP.Meta.Hidden) {
		var joins []*Presence
		if !meta.Hidden {
			joins = []*Presence{p}
		}
		var leaves []*Presence
		if alreadyTracked && !previousP.Meta.Hidden {
			syncAtomic.StoreUint32(&previousP.Meta.Reason, uint32(runtime.PresenceReasonUpdate))
			leaves = []*Presence{previousP}
		}
		t.queueEvent(joins, leaves)
	}
	return true
}

func (t *ClusterTracker) UntrackByStream(stream PresenceStream) {
	t.Lock()

	byStream, anyTracked := t.presencesByStream[stream.Mode][stream]
	if !anyTracked {
		t.Unlock()
		return
	}

	for pc, p := range byStream {
		if bySession := t.presencesBySession[pc.ID.SessionID]; len(bySession) == 1 {
			delete(t.presencesBySession, pc.ID.SessionID)
		} else {
			delete(bySession, pc)
		}
		t.count.Dec()

		// Publish leave event for remote presences if they're ours
		if pc.ID.Node == t.nodeName {
			syncAtomic.StoreUint32(&p.Meta.Reason, uint32(runtime.PresenceReasonLeave))
			t.publishEvent(trackerMsgTypeLeave, p)
		}
	}

	if byStreamMode := t.presencesByStream[stream.Mode]; len(byStreamMode) == 1 {
		delete(t.presencesByStream, stream.Mode)
	} else {
		delete(byStreamMode, stream)
	}

	t.Unlock()
}

func (t *ClusterTracker) UntrackLocalByStream(stream PresenceStream) {
	t.Lock()

	byStream, anyTracked := t.presencesByStream[stream.Mode][stream]
	if !anyTracked {
		t.Unlock()
		return
	}

	for pc, p := range byStream {
		// Only untrack local presences
		if pc.ID.Node != t.nodeName {
			continue
		}

		if bySession := t.presencesBySession[pc.ID.SessionID]; len(bySession) == 1 {
			delete(t.presencesBySession, pc.ID.SessionID)
		} else {
			delete(bySession, pc)
		}
		t.count.Dec()

		// Publish leave event
		syncAtomic.StoreUint32(&p.Meta.Reason, uint32(runtime.PresenceReasonLeave))
		t.publishEvent(trackerMsgTypeLeave, p)

		delete(byStream, pc)
	}

	if len(byStream) == 0 {
		if byStreamMode := t.presencesByStream[stream.Mode]; len(byStreamMode) == 1 {
			delete(t.presencesByStream, stream.Mode)
		} else {
			delete(byStreamMode, stream)
		}
	}

	t.Unlock()
}

func (t *ClusterTracker) UntrackLocalByModes(sessionID uuid.UUID, modes map[uint8]struct{}, skipStream PresenceStream) {
	leaves := make([]*Presence, 0, 1)

	t.Lock()
	bySession, anyTracked := t.presencesBySession[sessionID]
	if !anyTracked {
		t.Unlock()
		return
	}

	for pc, p := range bySession {
		if _, found := modes[pc.Stream.Mode]; !found {
			continue
		}
		if pc.Stream == skipStream {
			continue
		}

		if len(bySession) == 1 {
			delete(t.presencesBySession, sessionID)
		} else {
			delete(bySession, pc)
		}
		t.count.Dec()

		if byStreamMode := t.presencesByStream[pc.Stream.Mode]; len(byStreamMode) == 1 {
			if byStream := byStreamMode[pc.Stream]; len(byStream) == 1 {
				delete(t.presencesByStream, pc.Stream.Mode)
			} else {
				delete(byStream, pc)
			}
		} else {
			if byStream := byStreamMode[pc.Stream]; len(byStream) == 1 {
				delete(byStreamMode, pc.Stream)
			} else {
				delete(byStream, pc)
			}
		}

		syncAtomic.StoreUint32(&p.Meta.Reason, uint32(runtime.PresenceReasonLeave))
		t.publishEvent(trackerMsgTypeLeave, p)

		if !p.Meta.Hidden {
			leaves = append(leaves, p)
		}
	}
	t.Unlock()

	if len(leaves) > 0 {
		t.queueEvent(nil, leaves)
	}
}

func (t *ClusterTracker) ListNodesForStream(stream PresenceStream) map[string]struct{} {
	nodes := make(map[string]struct{})

	t.RLock()
	byStream, anyTracked := t.presencesByStream[stream.Mode][stream]
	if anyTracked {
		for pc := range byStream {
			nodes[pc.ID.Node] = struct{}{}
		}
	}
	t.RUnlock()

	return nodes
}

func (t *ClusterTracker) StreamExists(stream PresenceStream) bool {
	var exists bool
	t.RLock()
	exists = t.presencesByStream[stream.Mode][stream] != nil
	t.RUnlock()
	return exists
}

func (t *ClusterTracker) Count() int {
	return int(t.count.Load())
}

func (t *ClusterTracker) CountByStream(stream PresenceStream) int {
	var count int
	t.RLock()
	if byStream, anyTracked := t.presencesByStream[stream.Mode][stream]; anyTracked {
		count = len(byStream)
	}
	t.RUnlock()
	return count
}

func (t *ClusterTracker) CountByStreamModeFilter(modes map[uint8]*uint8) map[*PresenceStream]int32 {
	counts := make(map[*PresenceStream]int32)
	t.RLock()
	for mode, byStreamMode := range t.presencesByStream {
		if modes[mode] == nil {
			continue
		}
		for s, ps := range byStreamMode {
			cs := s
			counts[&cs] = int32(len(ps))
		}
	}
	t.RUnlock()
	return counts
}

func (t *ClusterTracker) GetLocalBySessionIDStreamUserID(sessionID uuid.UUID, stream PresenceStream, userID uuid.UUID) *PresenceMeta {
	pc := presenceCompact{ID: PresenceID{Node: t.nodeName, SessionID: sessionID}, Stream: stream, UserID: userID}
	t.RLock()
	bySession, anyTracked := t.presencesBySession[sessionID]
	if !anyTracked {
		t.RUnlock()
		return nil
	}
	p, found := bySession[pc]
	t.RUnlock()
	if !found {
		return nil
	}
	return &p.Meta
}

func (t *ClusterTracker) ListByStream(stream PresenceStream, includeHidden bool, includeNotHidden bool) []*Presence {
	if !includeHidden && !includeNotHidden {
		return []*Presence{}
	}

	t.RLock()
	byStream, anyTracked := t.presencesByStream[stream.Mode][stream]
	if !anyTracked {
		t.RUnlock()
		return []*Presence{}
	}
	ps := make([]*Presence, 0, len(byStream))
	for _, p := range byStream {
		if (p.Meta.Hidden && includeHidden) || (!p.Meta.Hidden && includeNotHidden) {
			ps = append(ps, p)
		}
	}
	t.RUnlock()
	return ps
}

func (t *ClusterTracker) ListLocalSessionIDByStream(stream PresenceStream) []uuid.UUID {
	t.RLock()
	byStream, anyTracked := t.presencesByStream[stream.Mode][stream]
	if !anyTracked {
		t.RUnlock()
		return []uuid.UUID{}
	}
	ps := make([]uuid.UUID, 0, len(byStream))
	for pc := range byStream {
		// Only include local sessions
		if pc.ID.Node == t.nodeName {
			ps = append(ps, pc.ID.SessionID)
		}
	}
	t.RUnlock()
	return ps
}

func (t *ClusterTracker) ListPresenceIDByStream(stream PresenceStream) []*PresenceID {
	t.RLock()
	byStream, anyTracked := t.presencesByStream[stream.Mode][stream]
	if !anyTracked {
		t.RUnlock()
		return []*PresenceID{}
	}
	ps := make([]*PresenceID, 0, len(byStream))
	for pc := range byStream {
		pid := pc.ID
		ps = append(ps, &pid)
	}
	t.RUnlock()
	return ps
}

func (t *ClusterTracker) ListPresenceIDByStreams(fill map[PresenceStream][]*PresenceID) {
	if len(fill) == 0 {
		return
	}

	t.RLock()
	for stream, presences := range fill {
		byStream, anyTracked := t.presencesByStream[stream.Mode][stream]
		if !anyTracked {
			continue
		}
		for pc := range byStream {
			pid := pc.ID
			presences = append(presences, &pid)
		}
		fill[stream] = presences
	}
	t.RUnlock()
}

func (t *ClusterTracker) queueEvent(joins, leaves []*Presence) {
	select {
	case t.eventsCh <- &PresenceEvent{Joins: joins, Leaves: leaves, QueueTime: time.Now()}:
	default:
		t.logger.Error("Presence event dispatch queue is full, presence events may be lost")
		for {
			select {
			case <-t.eventsCh:
			default:
				return
			}
		}
	}
}

func (t *ClusterTracker) processEvent(e *PresenceEvent) {
	dequeueTime := time.Now()
	defer func() {
		t.metrics.PresenceEvent(dequeueTime.Sub(e.QueueTime), time.Since(dequeueTime))
	}()

	t.logger.Debug("Processing presence event", zap.Int("joins", len(e.Joins)), zap.Int("leaves", len(e.Leaves)))

	streamJoins := make(map[PresenceStream][]*rtapi.UserPresence, 0)
	streamLeaves := make(map[PresenceStream][]*rtapi.UserPresence, 0)
	matchJoins := make(map[uuid.UUID][]*MatchPresence, 0)
	matchLeaves := make(map[uuid.UUID][]*MatchPresence, 0)
	partyJoins := make(map[uuid.UUID][]*Presence, 0)
	partyLeaves := make(map[uuid.UUID][]*Presence, 0)

	for _, p := range e.Joins {
		pWire := &rtapi.UserPresence{
			UserId:      p.UserID.String(),
			SessionId:   p.ID.SessionID.String(),
			Username:    p.Meta.Username,
			Persistence: p.Meta.Persistence,
		}
		if p.Stream.Mode == StreamModeStatus {
			pWire.Status = &wrapperspb.StringValue{Value: p.Meta.Status}
		}
		if j, ok := streamJoins[p.Stream]; ok {
			streamJoins[p.Stream] = append(j, pWire)
		} else {
			streamJoins[p.Stream] = []*rtapi.UserPresence{pWire}
		}

		if p.Stream.Mode == StreamModeMatchAuthoritative && p.Stream.Label == t.nodeName {
			mp := &MatchPresence{
				Node:      p.ID.Node,
				UserID:    p.UserID,
				SessionID: p.ID.SessionID,
				Username:  p.Meta.Username,
				Reason:    runtime.PresenceReason(syncAtomic.LoadUint32(&p.Meta.Reason)),
			}
			if j, ok := matchJoins[p.Stream.Subject]; ok {
				matchJoins[p.Stream.Subject] = append(j, mp)
			} else {
				matchJoins[p.Stream.Subject] = []*MatchPresence{mp}
			}
		}

		if p.Stream.Mode == StreamModeParty && p.Stream.Label == t.nodeName {
			c := p
			if j, ok := partyJoins[p.Stream.Subject]; ok {
				partyJoins[p.Stream.Subject] = append(j, c)
			} else {
				partyJoins[p.Stream.Subject] = []*Presence{c}
			}
		}
	}

	for _, p := range e.Leaves {
		pWire := &rtapi.UserPresence{
			UserId:      p.UserID.String(),
			SessionId:   p.ID.SessionID.String(),
			Username:    p.Meta.Username,
			Persistence: p.Meta.Persistence,
		}
		if p.Stream.Mode == StreamModeStatus {
			pWire.Status = &wrapperspb.StringValue{Value: p.Meta.Status}
		}
		if l, ok := streamLeaves[p.Stream]; ok {
			streamLeaves[p.Stream] = append(l, pWire)
		} else {
			streamLeaves[p.Stream] = []*rtapi.UserPresence{pWire}
		}

		if p.Stream.Mode == StreamModeMatchAuthoritative && p.Stream.Label == t.nodeName {
			mp := &MatchPresence{
				Node:      p.ID.Node,
				UserID:    p.UserID,
				SessionID: p.ID.SessionID,
				Username:  p.Meta.Username,
				Reason:    runtime.PresenceReason(syncAtomic.LoadUint32(&p.Meta.Reason)),
			}
			if l, ok := matchLeaves[p.Stream.Subject]; ok {
				matchLeaves[p.Stream.Subject] = append(l, mp)
			} else {
				matchLeaves[p.Stream.Subject] = []*MatchPresence{mp}
			}
		}

		if p.Stream.Mode == StreamModeParty && p.Stream.Label == t.nodeName {
			c := p
			if l, ok := partyLeaves[p.Stream.Subject]; ok {
				partyLeaves[p.Stream.Subject] = append(l, c)
			} else {
				partyLeaves[p.Stream.Subject] = []*Presence{c}
			}
		}
	}

	// Send match events
	for matchID, joins := range matchJoins {
		if t.matchJoinListener != nil {
			t.matchJoinListener(matchID, joins)
		}
	}
	for matchID, leaves := range matchLeaves {
		if t.matchLeaveListener != nil {
			t.matchLeaveListener(matchID, leaves)
		}
	}

	// Send party events
	for partyID, joins := range partyJoins {
		if t.partyJoinListener != nil {
			t.partyJoinListener(partyID, joins)
		}
	}
	for partyID, leaves := range partyLeaves {
		if t.partyLeaveListener != nil {
			t.partyLeaveListener(partyID, leaves)
		}
	}

	// Send joins, together with any leaves for the same stream.
	for stream, joins := range streamJoins {
		leaves, ok := streamLeaves[stream]
		if ok {
			delete(streamLeaves, stream)
		}

		if stream.Mode == StreamModeStatus {
			t.statusRegistry.Queue(stream.Subject, joins, leaves)
		}
	}

	// Also process any remaining status leaves that had no corresponding joins
	for stream, leaves := range streamLeaves {
		if stream.Mode == StreamModeStatus {
			t.statusRegistry.Queue(stream.Subject, nil, leaves)
		}
	}
}
