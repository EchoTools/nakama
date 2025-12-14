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
	"time"

	"github.com/go-redis/redis"
	"github.com/gofrs/uuid/v5"
	"github.com/heroiclabs/nakama-common/api"
	"github.com/heroiclabs/nakama-common/rtapi"
	"github.com/heroiclabs/nakama-common/runtime"
	"go.uber.org/atomic"
	"go.uber.org/zap"
	"google.golang.org/protobuf/types/known/timestamppb"
)

const (
	// Redis key prefixes
	sessionKeyPrefix    = "session:"
	userSessionsPrefix  = "user_sessions:"
	nodeSessionsPrefix  = "node_sessions:"
	sessionPubSubPrefix = "session_events:"

	// Pub/Sub message types
	msgTypeDisconnect     = "disconnect"
	msgTypeSingleSession  = "single_session"
	msgTypeSessionRemoved = "session_removed"
)

// SessionMeta represents serializable session metadata stored in Redis
type SessionMeta struct {
	SessionID  string            `json:"session_id"`
	UserID     string            `json:"user_id"`
	Username   string            `json:"username"`
	Vars       map[string]string `json:"vars"`
	Expiry     int64             `json:"expiry"`
	Format     SessionFormat     `json:"format"`
	Node       string            `json:"node"`
	ClientIP   string            `json:"client_ip"`
	ClientPort string            `json:"client_port"`
	Lang       string            `json:"lang"`
	CreatedAt  int64             `json:"created_at"`
}

// ClusterSessionEvent represents a pub/sub message for session operations
type ClusterSessionEvent struct {
	Type      string `json:"type"`
	SessionID string `json:"session_id"`
	UserID    string `json:"user_id"`
	Node      string `json:"node"`
	Ban       bool   `json:"ban,omitempty"`
	Reason    uint32 `json:"reason,omitempty"`
}

// ClusterSessionRegistry implements SessionRegistry with Redis-backed distributed state
type ClusterSessionRegistry struct {
	logger        *zap.Logger
	metrics       Metrics
	config        *ClusterConfig
	nodeName      string
	redisClient   *redis.Client
	channelPrefix string

	// Local session storage (sessions physically on this node)
	localSessions *MapOf[uuid.UUID, Session]
	sessionCount  *atomic.Int32

	ctx         context.Context
	ctxCancelFn context.CancelFunc
}

// NewClusterSessionRegistry creates a new distributed session registry
func NewClusterSessionRegistry(
	ctx context.Context,
	logger *zap.Logger,
	metrics Metrics,
	config *ClusterConfig,
	nodeName string,
	redisClient *redis.Client,
) (SessionRegistry, error) {
	ctx, ctxCancelFn := context.WithCancel(ctx)

	r := &ClusterSessionRegistry{
		logger:        logger,
		metrics:       metrics,
		config:        config,
		nodeName:      nodeName,
		redisClient:   redisClient,
		channelPrefix: config.ChannelPrefix,

		localSessions: &MapOf[uuid.UUID, Session]{},
		sessionCount:  atomic.NewInt32(0),

		ctx:         ctx,
		ctxCancelFn: ctxCancelFn,
	}

	// Start pub/sub listener for cross-node operations
	go r.subscribeToEvents()

	logger.Info("Cluster session registry initialized",
		zap.String("node", nodeName),
		zap.String("redis_address", config.RedisAddress))

	return r, nil
}

// subscribeToEvents listens for session events from other nodes
func (r *ClusterSessionRegistry) subscribeToEvents() {
	channel := fmt.Sprintf("%s:%s%s", r.channelPrefix, sessionPubSubPrefix, r.nodeName)
	pubsub := r.redisClient.Subscribe(channel)
	defer pubsub.Close()

	r.logger.Debug("Subscribed to session events channel", zap.String("channel", channel))

	ch := pubsub.Channel()
	for {
		select {
		case <-r.ctx.Done():
			return
		case msg, ok := <-ch:
			if !ok {
				return
			}

			var event ClusterSessionEvent
			if err := json.Unmarshal([]byte(msg.Payload), &event); err != nil {
				r.logger.Warn("Failed to unmarshal session event", zap.Error(err))
				continue
			}

			r.handleEvent(&event)
		}
	}
}

// handleEvent processes session events from other nodes
func (r *ClusterSessionRegistry) handleEvent(event *ClusterSessionEvent) {
	sessionID, err := uuid.FromString(event.SessionID)
	if err != nil {
		r.logger.Warn("Invalid session ID in event", zap.String("session_id", event.SessionID))
		return
	}

	switch event.Type {
	case msgTypeDisconnect:
		r.handleDisconnectEvent(sessionID, event.Ban, runtime.PresenceReason(event.Reason))
	case msgTypeSingleSession:
		r.handleSingleSessionEvent(sessionID, event.UserID)
	case msgTypeSessionRemoved:
		// Another node removed a session, we can clean up any local references
		r.logger.Debug("Session removed on another node",
			zap.String("session_id", event.SessionID),
			zap.String("node", event.Node))
	}
}

// handleDisconnectEvent processes a disconnect request from another node
func (r *ClusterSessionRegistry) handleDisconnectEvent(sessionID uuid.UUID, ban bool, reason runtime.PresenceReason) {
	session, ok := r.localSessions.Load(sessionID)
	if !ok {
		return
	}

	if ban {
		session.Close("server-side session disconnect", runtime.PresenceReasonDisconnect,
			&rtapi.Envelope{Message: &rtapi.Envelope_Notifications{
				Notifications: &rtapi.Notifications{
					Notifications: []*api.Notification{
						{
							Id:         uuid.Must(uuid.NewV4()).String(),
							Subject:    "banned",
							Content:    "{}",
							Code:       NotificationCodeUserBanned,
							SenderId:   "",
							CreateTime: &timestamppb.Timestamp{Seconds: time.Now().Unix()},
							Persistent: false,
						},
					},
				},
			}})
	} else {
		session.Close("server-side session disconnect", reason)
	}
}

// handleSingleSessionEvent disconnects a session as part of single-session enforcement
func (r *ClusterSessionRegistry) handleSingleSessionEvent(sessionID uuid.UUID, userIDStr string) {
	session, ok := r.localSessions.Load(sessionID)
	if !ok {
		return
	}

	session.Close("server-side session disconnect", runtime.PresenceReasonDisconnect,
		&rtapi.Envelope{Message: &rtapi.Envelope_Notifications{
			Notifications: &rtapi.Notifications{
				Notifications: []*api.Notification{
					{
						Id:         uuid.Must(uuid.NewV4()).String(),
						Subject:    "single_socket",
						Content:    "{}",
						Code:       NotificationCodeSingleSocket,
						SenderId:   "",
						CreateTime: &timestamppb.Timestamp{Seconds: time.Now().Unix()},
						Persistent: false,
					},
				},
			},
		}})
}

// publishEvent sends a session event to a specific node
func (r *ClusterSessionRegistry) publishEvent(targetNode string, event *ClusterSessionEvent) error {
	data, err := json.Marshal(event)
	if err != nil {
		return fmt.Errorf("failed to marshal event: %w", err)
	}

	channel := fmt.Sprintf("%s:%s%s", r.channelPrefix, sessionPubSubPrefix, targetNode)
	return r.redisClient.Publish(channel, data).Err()
}

// sessionKey returns the Redis key for a session
func (r *ClusterSessionRegistry) sessionKey(sessionID uuid.UUID) string {
	return fmt.Sprintf("%s:%s%s", r.channelPrefix, sessionKeyPrefix, sessionID.String())
}

// userSessionsKey returns the Redis key for a user's session set
func (r *ClusterSessionRegistry) userSessionsKey(userID uuid.UUID) string {
	return fmt.Sprintf("%s:%s%s", r.channelPrefix, userSessionsPrefix, userID.String())
}

// nodeSessionsKey returns the Redis key for a node's session set
func (r *ClusterSessionRegistry) nodeSessionsKey() string {
	return fmt.Sprintf("%s:%s%s", r.channelPrefix, nodeSessionsPrefix, r.nodeName)
}

// Stop gracefully shuts down the registry
func (r *ClusterSessionRegistry) Stop() {
	r.ctxCancelFn()

	// Clean up all local sessions from Redis
	r.localSessions.Range(func(sessionID uuid.UUID, session Session) bool {
		r.removeFromRedis(sessionID, session.UserID())
		return true
	})

	r.logger.Info("Cluster session registry stopped")
}

// Count returns the total number of sessions across the cluster
func (r *ClusterSessionRegistry) Count() int {
	// For a quick count, return local count
	// For cluster-wide count, we'd need to query Redis
	return int(r.sessionCount.Load())
}

// Get retrieves a session by ID (local only, sessions are node-bound)
func (r *ClusterSessionRegistry) Get(sessionID uuid.UUID) Session {
	session, ok := r.localSessions.Load(sessionID)
	if !ok {
		return nil
	}
	return session
}

// GetSessionMeta retrieves session metadata from Redis (works across nodes)
func (r *ClusterSessionRegistry) GetSessionMeta(ctx context.Context, sessionID uuid.UUID) (*SessionMeta, error) {
	result := r.redisClient.Get(r.sessionKey(sessionID))
	if result.Err() != nil {
		if result.Err() == redis.Nil {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to get session from Redis: %w", result.Err())
	}

	data, err := result.Bytes()
	if err != nil {
		return nil, fmt.Errorf("failed to get session bytes: %w", err)
	}

	var meta SessionMeta
	if err := json.Unmarshal(data, &meta); err != nil {
		return nil, fmt.Errorf("failed to unmarshal session meta: %w", err)
	}

	return &meta, nil
}

// GetSessionsByUserID retrieves all session IDs for a user across the cluster
func (r *ClusterSessionRegistry) GetSessionsByUserID(ctx context.Context, userID uuid.UUID) ([]uuid.UUID, error) {
	result := r.redisClient.SMembers(r.userSessionsKey(userID))
	if result.Err() != nil {
		return nil, fmt.Errorf("failed to get user sessions from Redis: %w", result.Err())
	}

	sessionIDs := result.Val()
	uuidResult := make([]uuid.UUID, 0, len(sessionIDs))
	for _, sidStr := range sessionIDs {
		sid, err := uuid.FromString(sidStr)
		if err != nil {
			continue
		}
		uuidResult = append(uuidResult, sid)
	}

	return uuidResult, nil
}

// Add registers a new session
func (r *ClusterSessionRegistry) Add(session Session) {
	sessionID := session.ID()
	userID := session.UserID()

	// Store locally
	r.localSessions.Store(sessionID, session)
	count := r.sessionCount.Inc()
	r.metrics.GaugeSessions(float64(count))

	// Store metadata in Redis
	meta := &SessionMeta{
		SessionID:  sessionID.String(),
		UserID:     userID.String(),
		Username:   session.Username(),
		Vars:       session.Vars(),
		Expiry:     session.Expiry(),
		Format:     session.Format(),
		Node:       r.nodeName,
		ClientIP:   session.ClientIP(),
		ClientPort: session.ClientPort(),
		Lang:       session.Lang(),
		CreatedAt:  time.Now().Unix(),
	}

	data, err := json.Marshal(meta)
	if err != nil {
		r.logger.Error("Failed to marshal session meta", zap.Error(err))
		return
	}

	pipe := r.redisClient.Pipeline()

	// Store session metadata
	pipe.Set(r.sessionKey(sessionID), data, r.config.GetSessionTTL())

	// Add to user's session set
	pipe.SAdd(r.userSessionsKey(userID), sessionID.String())
	pipe.Expire(r.userSessionsKey(userID), r.config.GetSessionTTL())

	// Add to node's session set
	pipe.SAdd(r.nodeSessionsKey(), sessionID.String())
	pipe.Expire(r.nodeSessionsKey(), r.config.GetSessionTTL())

	if _, err := pipe.Exec(); err != nil {
		r.logger.Error("Failed to store session in Redis", zap.Error(err), zap.String("session_id", sessionID.String()))
	}

	r.logger.Debug("Session added to cluster",
		zap.String("session_id", sessionID.String()),
		zap.String("user_id", userID.String()),
		zap.String("node", r.nodeName))
}

// Remove removes a session from the registry
func (r *ClusterSessionRegistry) Remove(sessionID uuid.UUID) {
	session, ok := r.localSessions.Load(sessionID)
	if !ok {
		return
	}

	userID := session.UserID()

	// Remove locally
	r.localSessions.Delete(sessionID)
	count := r.sessionCount.Dec()
	r.metrics.GaugeSessions(float64(count))

	// Remove from Redis
	r.removeFromRedis(sessionID, userID)

	r.logger.Debug("Session removed from cluster",
		zap.String("session_id", sessionID.String()),
		zap.String("user_id", userID.String()))
}

// removeFromRedis removes session data from Redis
func (r *ClusterSessionRegistry) removeFromRedis(sessionID, userID uuid.UUID) {
	pipe := r.redisClient.Pipeline()

	pipe.Del(r.sessionKey(sessionID))
	pipe.SRem(r.userSessionsKey(userID), sessionID.String())
	pipe.SRem(r.nodeSessionsKey(), sessionID.String())

	if _, err := pipe.Exec(); err != nil {
		r.logger.Error("Failed to remove session from Redis", zap.Error(err), zap.String("session_id", sessionID.String()))
	}
}

// Disconnect disconnects a session, potentially on another node
func (r *ClusterSessionRegistry) Disconnect(ctx context.Context, sessionID uuid.UUID, ban bool, reason ...runtime.PresenceReason) error {
	reasonOverride := runtime.PresenceReasonDisconnect
	if len(reason) > 0 {
		reasonOverride = reason[0]
	}

	// Try local first
	session, ok := r.localSessions.Load(sessionID)
	if ok {
		if ban {
			session.Close("server-side session disconnect", runtime.PresenceReasonDisconnect,
				&rtapi.Envelope{Message: &rtapi.Envelope_Notifications{
					Notifications: &rtapi.Notifications{
						Notifications: []*api.Notification{
							{
								Id:         uuid.Must(uuid.NewV4()).String(),
								Subject:    "banned",
								Content:    "{}",
								Code:       NotificationCodeUserBanned,
								SenderId:   "",
								CreateTime: &timestamppb.Timestamp{Seconds: time.Now().Unix()},
								Persistent: false,
							},
						},
					},
				}})
		} else {
			session.Close("server-side session disconnect", reasonOverride)
		}
		return nil
	}

	// Session not local, check Redis for the node that owns it
	meta, err := r.GetSessionMeta(ctx, sessionID)
	if err != nil {
		return fmt.Errorf("failed to get session meta: %w", err)
	}
	if meta == nil {
		// Session doesn't exist
		return nil
	}

	// Send disconnect event to the owning node
	event := &ClusterSessionEvent{
		Type:      msgTypeDisconnect,
		SessionID: sessionID.String(),
		UserID:    meta.UserID,
		Node:      r.nodeName,
		Ban:       ban,
		Reason:    uint32(reasonOverride),
	}

	if err := r.publishEvent(meta.Node, event); err != nil {
		return fmt.Errorf("failed to publish disconnect event: %w", err)
	}

	r.logger.Debug("Disconnect event sent to remote node",
		zap.String("session_id", sessionID.String()),
		zap.String("target_node", meta.Node))

	return nil
}

// SingleSession disconnects all other sessions for a user except the specified one
func (r *ClusterSessionRegistry) SingleSession(ctx context.Context, tracker Tracker, userID, sessionID uuid.UUID) {
	// Get all sessions for this user from Redis
	userSessionIDs, err := r.GetSessionsByUserID(ctx, userID)
	if err != nil {
		r.logger.Error("Failed to get user sessions for single session enforcement",
			zap.Error(err),
			zap.String("user_id", userID.String()))
		return
	}

	for _, foundSessionID := range userSessionIDs {
		if foundSessionID == sessionID {
			// Skip the current session
			continue
		}

		// Try local first
		session, ok := r.localSessions.Load(foundSessionID)
		if ok {
			session.Close("server-side session disconnect", runtime.PresenceReasonDisconnect,
				&rtapi.Envelope{Message: &rtapi.Envelope_Notifications{
					Notifications: &rtapi.Notifications{
						Notifications: []*api.Notification{
							{
								Id:         uuid.Must(uuid.NewV4()).String(),
								Subject:    "single_socket",
								Content:    "{}",
								Code:       NotificationCodeSingleSocket,
								SenderId:   "",
								CreateTime: &timestamppb.Timestamp{Seconds: time.Now().Unix()},
								Persistent: false,
							},
						},
					},
				}})
			continue
		}

		// Session not local, find the owning node and send event
		meta, err := r.GetSessionMeta(ctx, foundSessionID)
		if err != nil || meta == nil {
			continue
		}

		event := &ClusterSessionEvent{
			Type:      msgTypeSingleSession,
			SessionID: foundSessionID.String(),
			UserID:    userID.String(),
			Node:      r.nodeName,
		}

		if err := r.publishEvent(meta.Node, event); err != nil {
			r.logger.Warn("Failed to publish single session event",
				zap.Error(err),
				zap.String("session_id", foundSessionID.String()),
				zap.String("target_node", meta.Node))
		}
	}
}

// Range iterates over local sessions only
func (r *ClusterSessionRegistry) Range(fn func(Session) bool) {
	r.localSessions.Range(func(id uuid.UUID, session Session) bool {
		return fn(session)
	})
}

// RangeCluster iterates over all session metadata in the cluster
func (r *ClusterSessionRegistry) RangeCluster(ctx context.Context, fn func(*SessionMeta) bool) error {
	var cursor uint64
	pattern := fmt.Sprintf("%s:%s*", r.channelPrefix, sessionKeyPrefix)

	for {
		result := r.redisClient.Scan(cursor, pattern, 100)
		keys, nextCursor, err := result.Result()
		if err != nil {
			return fmt.Errorf("failed to scan sessions: %w", err)
		}

		for _, key := range keys {
			data, err := r.redisClient.Get(key).Bytes()
			if err != nil {
				continue
			}

			var meta SessionMeta
			if err := json.Unmarshal(data, &meta); err != nil {
				continue
			}

			if !fn(&meta) {
				return nil
			}
		}

		cursor = nextCursor
		if cursor == 0 {
			break
		}
	}

	return nil
}

// ClusterCount returns the total session count across all nodes
func (r *ClusterSessionRegistry) ClusterCount(ctx context.Context) (int, error) {
	var count int
	pattern := fmt.Sprintf("%s:%s*", r.channelPrefix, sessionKeyPrefix)

	var cursor uint64
	for {
		result := r.redisClient.Scan(cursor, pattern, 1000)
		keys, nextCursor, err := result.Result()
		if err != nil {
			return 0, fmt.Errorf("failed to scan sessions: %w", err)
		}

		count += len(keys)
		cursor = nextCursor
		if cursor == 0 {
			break
		}
	}

	return count, nil
}

// GetNodeSessions returns all session IDs for a specific node
func (r *ClusterSessionRegistry) GetNodeSessions(ctx context.Context, nodeName string) ([]uuid.UUID, error) {
	key := fmt.Sprintf("%s:%s%s", r.channelPrefix, nodeSessionsPrefix, nodeName)
	sessionIDs, err := r.redisClient.SMembers(key).Result()
	if err != nil {
		return nil, fmt.Errorf("failed to get node sessions: %w", err)
	}

	result := make([]uuid.UUID, 0, len(sessionIDs))
	for _, sidStr := range sessionIDs {
		sid, err := uuid.FromString(sidStr)
		if err != nil {
			continue
		}
		result = append(result, sid)
	}

	return result, nil
}
