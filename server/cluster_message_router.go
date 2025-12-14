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

	"github.com/go-redis/redis"
	"github.com/gofrs/uuid/v5"
	"github.com/heroiclabs/nakama-common/rtapi"
	"go.uber.org/zap"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

const (
	// Redis key prefixes for message router
	routerPubSubPrefix = "router_messages:"
)

// RouterMessage represents a message to be delivered via pub/sub
type RouterMessage struct {
	SessionID    string `json:"session_id"`
	EnvelopeJSON []byte `json:"envelope_json"` // JSON-encoded envelope for easy cross-node serialization
	Reliable     bool   `json:"reliable"`
	FromNode     string `json:"from_node"`
}

// ClusterMessageRouter implements MessageRouter with cross-node message delivery
type ClusterMessageRouter struct {
	logger             *zap.Logger
	protojsonMarshaler *protojson.MarshalOptions
	sessionRegistry    SessionRegistry
	tracker            Tracker
	redisClient        *redis.Client
	channelPrefix      string
	nodeName           string

	ctx         context.Context
	ctxCancelFn context.CancelFunc
}

// NewClusterMessageRouter creates a new distributed message router
func NewClusterMessageRouter(
	ctx context.Context,
	logger *zap.Logger,
	sessionRegistry SessionRegistry,
	tracker Tracker,
	protojsonMarshaler *protojson.MarshalOptions,
	redisClient *redis.Client,
	channelPrefix string,
	nodeName string,
) MessageRouter {
	ctx, ctxCancelFn := context.WithCancel(ctx)

	r := &ClusterMessageRouter{
		logger:             logger,
		protojsonMarshaler: protojsonMarshaler,
		sessionRegistry:    sessionRegistry,
		tracker:            tracker,
		redisClient:        redisClient,
		channelPrefix:      channelPrefix,
		nodeName:           nodeName,

		ctx:         ctx,
		ctxCancelFn: ctxCancelFn,
	}

	// Start listening for messages from other nodes
	go r.subscribeToMessages()

	logger.Info("Cluster message router initialized", zap.String("node", nodeName))

	return r
}

// subscribeToMessages listens for messages from other nodes
func (r *ClusterMessageRouter) subscribeToMessages() {
	channel := fmt.Sprintf("%s:%s%s", r.channelPrefix, routerPubSubPrefix, r.nodeName)
	pubsub := r.redisClient.Subscribe(channel)
	defer pubsub.Close()

	r.logger.Debug("Subscribed to router messages channel", zap.String("channel", channel))

	ch := pubsub.Channel()
	for {
		select {
		case <-r.ctx.Done():
			return
		case msg, ok := <-ch:
			if !ok {
				return
			}

			var routerMsg RouterMessage
			if err := json.Unmarshal([]byte(msg.Payload), &routerMsg); err != nil {
				r.logger.Warn("Failed to unmarshal router message", zap.Error(err))
				continue
			}

			r.deliverLocalMessage(&routerMsg)
		}
	}
}

// deliverLocalMessage delivers a message that was forwarded from another node
func (r *ClusterMessageRouter) deliverLocalMessage(msg *RouterMessage) {
	sessionID, err := uuid.FromString(msg.SessionID)
	if err != nil {
		r.logger.Warn("Invalid session ID in router message", zap.String("session_id", msg.SessionID))
		return
	}

	session := r.sessionRegistry.Get(sessionID)
	if session == nil {
		r.logger.Debug("No local session for forwarded message", zap.String("sid", msg.SessionID))
		return
	}

	// Unmarshal the envelope
	var envelope rtapi.Envelope
	if err := protojson.Unmarshal(msg.EnvelopeJSON, &envelope); err != nil {
		r.logger.Warn("Failed to unmarshal forwarded envelope", zap.Error(err))
		return
	}

	// Deliver based on session format
	var sendErr error
	switch session.Format() {
	case SessionFormatEVR:
		sendErr = session.Send(&envelope, msg.Reliable)
	case SessionFormatProtobuf:
		payloadProtobuf, err := proto.Marshal(&envelope)
		if err != nil {
			r.logger.Error("Could not marshal message for forwarded delivery", zap.Error(err))
			return
		}
		sendErr = session.SendBytes(payloadProtobuf, msg.Reliable)
	case SessionFormatJson:
		fallthrough
	default:
		sendErr = session.SendBytes(msg.EnvelopeJSON, msg.Reliable)
	}

	if sendErr != nil {
		r.logger.Error("Failed to deliver forwarded message", zap.String("sid", msg.SessionID), zap.Error(sendErr))
	}
}

// forwardToNode sends a message to another node via Redis pub/sub
func (r *ClusterMessageRouter) forwardToNode(targetNode string, sessionID uuid.UUID, envelope *rtapi.Envelope, reliable bool) {
	// Marshal envelope to JSON for cross-node transport
	envelopeJSON, err := r.protojsonMarshaler.Marshal(envelope)
	if err != nil {
		r.logger.Error("Could not marshal envelope for forwarding", zap.Error(err))
		return
	}

	msg := &RouterMessage{
		SessionID:    sessionID.String(),
		EnvelopeJSON: envelopeJSON,
		Reliable:     reliable,
		FromNode:     r.nodeName,
	}

	data, err := json.Marshal(msg)
	if err != nil {
		r.logger.Error("Could not marshal router message", zap.Error(err))
		return
	}

	channel := fmt.Sprintf("%s:%s%s", r.channelPrefix, routerPubSubPrefix, targetNode)
	if err := r.redisClient.Publish(channel, data).Err(); err != nil {
		r.logger.Warn("Failed to forward message to node", zap.String("node", targetNode), zap.Error(err))
	}
}

func (r *ClusterMessageRouter) SendToPresenceIDs(logger *zap.Logger, presenceIDs []*PresenceID, envelope *rtapi.Envelope, reliable bool) {
	if len(presenceIDs) == 0 {
		return
	}

	// Prepare payload variables but do not initialize until we hit a session that needs them to avoid unnecessary work.
	var payloadProtobuf []byte
	var payloadJSON []byte

	// Group by node for efficient cross-node delivery
	remotePresences := make(map[string][]*PresenceID)

	for _, presenceID := range presenceIDs {
		// Check if session is local
		session := r.sessionRegistry.Get(presenceID.SessionID)
		if session != nil {
			// Local delivery
			var err error
			switch session.Format() {
			case SessionFormatEVR:
				err = session.Send(envelope, reliable)
			case SessionFormatProtobuf:
				if payloadProtobuf == nil {
					payloadProtobuf, err = proto.Marshal(envelope)
					if err != nil {
						logger.Error("Could not marshal message", zap.Error(err))
						return
					}
				}
				err = session.SendBytes(payloadProtobuf, reliable)
			case SessionFormatJson:
				fallthrough
			default:
				if payloadJSON == nil {
					if buf, err := r.protojsonMarshaler.Marshal(envelope); err == nil {
						payloadJSON = buf
					} else {
						logger.Error("Could not marshal message", zap.Error(err))
						return
					}
				}
				err = session.SendBytes(payloadJSON, reliable)
			}
			if err != nil {
				logger.Error("Failed to route message", zap.String("sid", presenceID.SessionID.String()), zap.Error(err))
			}
		} else if presenceID.Node != "" && presenceID.Node != r.nodeName {
			// Session is on another node, queue for forwarding
			remotePresences[presenceID.Node] = append(remotePresences[presenceID.Node], presenceID)
		} else {
			logger.Debug("No session to route to", zap.String("sid", presenceID.SessionID.String()))
		}
	}

	// Forward messages to remote nodes
	for targetNode, presences := range remotePresences {
		for _, presence := range presences {
			r.forwardToNode(targetNode, presence.SessionID, envelope, reliable)
		}
	}
}

func (r *ClusterMessageRouter) SendToStream(logger *zap.Logger, stream PresenceStream, envelope *rtapi.Envelope, reliable bool) {
	presenceIDs := r.tracker.ListPresenceIDByStream(stream)
	r.SendToPresenceIDs(logger, presenceIDs, envelope, reliable)
}

func (r *ClusterMessageRouter) SendDeferred(logger *zap.Logger, messages []*DeferredMessage) {
	for _, message := range messages {
		r.SendToPresenceIDs(logger, message.PresenceIDs, message.Envelope, message.Reliable)
	}
}

func (r *ClusterMessageRouter) SendToAll(logger *zap.Logger, envelope *rtapi.Envelope, reliable bool) {
	// Prepare payload variables
	var payloadProtobuf []byte
	var payloadJSON []byte

	// Send to all local sessions
	r.sessionRegistry.Range(func(session Session) bool {
		var err error
		switch session.Format() {
		case SessionFormatEVR:
			err = session.Send(envelope, reliable)
		case SessionFormatProtobuf:
			if payloadProtobuf == nil {
				payloadProtobuf, err = proto.Marshal(envelope)
				if err != nil {
					logger.Error("Could not marshal message", zap.Error(err))
					return false
				}
			}
			err = session.SendBytes(payloadProtobuf, reliable)
		case SessionFormatJson:
			fallthrough
		default:
			if payloadJSON == nil {
				if buf, err := r.protojsonMarshaler.Marshal(envelope); err == nil {
					payloadJSON = buf
				} else {
					logger.Error("Could not marshal message", zap.Error(err))
					return false
				}
			}
			err = session.SendBytes(payloadJSON, reliable)
		}
		if err != nil {
			logger.Error("Failed to route message", zap.String("sid", session.ID().String()), zap.Error(err))
		}
		return true
	})

	// For a true cluster-wide broadcast, we'd need to publish to all nodes
	// This could be done by getting the list of active nodes and forwarding
	// For now, SendToAll only sends to local sessions
	// A cluster-wide broadcast would require a separate pub/sub channel for broadcasts
}

// Stop gracefully shuts down the router
func (r *ClusterMessageRouter) Stop() {
	r.ctxCancelFn()
}
