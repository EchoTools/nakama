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
	"fmt"
	"sync"

	"github.com/go-redis/redis"
	"github.com/gofrs/uuid/v5"
	"github.com/heroiclabs/nakama-common/api"
	"github.com/heroiclabs/nakama-common/rtapi"
	"go.uber.org/zap"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

const (
	// Redis key prefixes for status registry
	onlineUsersPrefix     = "online_users:"
	userSessionsOnlineKey = "user_sessions_online:"
	sessionFollowsPrefix  = "session_follows:"
	userFollowersPrefix   = "user_followers:"
)

var _ StatusRegistry = (*ClusterStatusRegistry)(nil)

// ClusterStatusRegistry implements StatusRegistry with Redis-backed distributed state
type ClusterStatusRegistry struct {
	sync.RWMutex
	logger             *zap.Logger
	config             *ClusterConfig
	nodeName           string
	sessionRegistry    SessionRegistry
	protojsonMarshaler *protojson.MarshalOptions
	redisClient        *redis.Client
	channelPrefix      string

	ctx         context.Context
	ctxCancelFn context.CancelFunc

	eventsCh chan *statusEvent

	// Local tracking for fast access
	bySession map[uuid.UUID]map[uuid.UUID]struct{}
	byUser    map[uuid.UUID]map[uuid.UUID]struct{}

	onlineMutex *sync.RWMutex
	onlineCache map[uuid.UUID]map[string]struct{}
}

// NewClusterStatusRegistry creates a new distributed status registry
func NewClusterStatusRegistry(
	ctx context.Context,
	logger *zap.Logger,
	config Config,
	clusterConfig *ClusterConfig,
	sessionRegistry SessionRegistry,
	protojsonMarshaler *protojson.MarshalOptions,
	redisClient *redis.Client,
) StatusRegistry {
	ctx, ctxCancelFn := context.WithCancel(ctx)

	s := &ClusterStatusRegistry{
		logger:             logger,
		config:             clusterConfig,
		nodeName:           config.GetName(),
		sessionRegistry:    sessionRegistry,
		protojsonMarshaler: protojsonMarshaler,
		redisClient:        redisClient,
		channelPrefix:      clusterConfig.ChannelPrefix,

		ctx:         ctx,
		ctxCancelFn: ctxCancelFn,

		eventsCh:  make(chan *statusEvent, config.GetTracker().EventQueueSize),
		bySession: make(map[uuid.UUID]map[uuid.UUID]struct{}),
		byUser:    make(map[uuid.UUID]map[uuid.UUID]struct{}),

		onlineMutex: &sync.RWMutex{},
		onlineCache: make(map[uuid.UUID]map[string]struct{}),
	}

	go s.processEvents()

	logger.Info("Cluster status registry initialized",
		zap.String("node", config.GetName()))

	return s
}

// processEvents handles status events
func (s *ClusterStatusRegistry) processEvents() {
	for {
		select {
		case <-s.ctx.Done():
			return
		case e := <-s.eventsCh:
			// Track overall user online status locally
			s.onlineMutex.Lock()
			existing, found := s.onlineCache[e.userID]
			for _, leave := range e.leaves {
				if !found {
					continue
				}
				delete(existing, leave.SessionId)
			}
			for _, join := range e.joins {
				if !found {
					existing = make(map[string]struct{}, 1)
					s.onlineCache[e.userID] = existing
					found = true
				}
				existing[join.SessionId] = struct{}{}
			}
			if found && len(existing) == 0 {
				delete(s.onlineCache, e.userID)
			}
			s.onlineMutex.Unlock()

			// Also update Redis for cluster-wide online status
			s.updateRedisOnlineStatus(e.userID, e.joins, e.leaves)

			// Process status update if the user has any followers
			s.RLock()
			ids, hasFollowers := s.byUser[e.userID]
			if !hasFollowers {
				s.RUnlock()
				continue
			}
			sessionIDs := make([]uuid.UUID, 0, len(ids))
			for id := range ids {
				sessionIDs = append(sessionIDs, id)
			}
			s.RUnlock()

			// Prepare payload
			var payloadProtobuf []byte
			var payloadJSON []byte
			envelope := &rtapi.Envelope{Message: &rtapi.Envelope_StatusPresenceEvent{StatusPresenceEvent: &rtapi.StatusPresenceEvent{
				Joins:  e.joins,
				Leaves: e.leaves,
			}}}

			// Deliver event to local sessions
			for _, sessionID := range sessionIDs {
				session := s.sessionRegistry.Get(sessionID)
				if session == nil {
					s.logger.Debug("Could not deliver status event, no session", zap.String("sid", sessionID.String()))
					continue
				}

				var err error
				switch session.Format() {
				case SessionFormatEVR:
					err = session.Send(envelope, true)
				case SessionFormatProtobuf:
					if payloadProtobuf == nil {
						payloadProtobuf, err = proto.Marshal(envelope)
						if err != nil {
							s.logger.Error("Could not marshal status event", zap.Error(err))
							continue
						}
					}
					err = session.SendBytes(payloadProtobuf, true)
				case SessionFormatJson:
					fallthrough
				default:
					if payloadJSON == nil {
						if buf, err := s.protojsonMarshaler.Marshal(envelope); err == nil {
							payloadJSON = buf
						} else {
							s.logger.Error("Could not marshal status event", zap.Error(err))
							continue
						}
					}
					err = session.SendBytes(payloadJSON, true)
				}
				if err != nil {
					s.logger.Error("Failed to deliver status event", zap.String("sid", sessionID.String()), zap.Error(err))
				}
			}
		}
	}
}

// updateRedisOnlineStatus updates the online status in Redis
func (s *ClusterStatusRegistry) updateRedisOnlineStatus(userID uuid.UUID, joins, leaves []*rtapi.UserPresence) {
	key := fmt.Sprintf("%s:%s%s", s.channelPrefix, userSessionsOnlineKey, userID.String())

	pipe := s.redisClient.Pipeline()

	for _, leave := range leaves {
		pipe.SRem(key, leave.SessionId)
	}

	for _, join := range joins {
		pipe.SAdd(key, join.SessionId)
		pipe.Expire(key, s.config.GetSessionTTL())
	}

	if _, err := pipe.Exec(); err != nil {
		s.logger.Warn("Failed to update online status in Redis", zap.Error(err), zap.String("user_id", userID.String()))
	}
}

func (s *ClusterStatusRegistry) Stop() {
	s.ctxCancelFn()
}

func (s *ClusterStatusRegistry) Follow(sessionID uuid.UUID, userIDs map[uuid.UUID]struct{}) {
	if len(userIDs) == 0 {
		return
	}

	s.Lock()

	sessionFollows, ok := s.bySession[sessionID]
	if !ok {
		sessionFollows = make(map[uuid.UUID]struct{})
		s.bySession[sessionID] = sessionFollows
	}
	for userID := range userIDs {
		if _, alreadyFollowing := sessionFollows[userID]; alreadyFollowing {
			continue
		}
		sessionFollows[userID] = struct{}{}

		userFollowers, ok := s.byUser[userID]
		if !ok {
			userFollowers = make(map[uuid.UUID]struct{})
			s.byUser[userID] = userFollowers
		}

		if _, alreadyFollowing := userFollowers[sessionID]; alreadyFollowing {
			continue
		}
		userFollowers[sessionID] = struct{}{}
	}

	s.Unlock()
}

func (s *ClusterStatusRegistry) Unfollow(sessionID uuid.UUID, userIDs []uuid.UUID) {
	if len(userIDs) == 0 {
		return
	}

	s.Lock()

	sessionFollows, ok := s.bySession[sessionID]
	if !ok {
		s.Unlock()
		return
	}
	for _, userID := range userIDs {
		if _, wasFollowed := sessionFollows[userID]; !wasFollowed {
			continue
		}

		if userFollowers := s.byUser[userID]; len(userFollowers) == 1 {
			delete(s.byUser, userID)
		} else {
			delete(userFollowers, sessionID)
		}

		if len(sessionFollows) == 1 {
			delete(s.bySession, sessionID)
			break
		} else {
			delete(sessionFollows, userID)
		}
	}

	s.Unlock()
}

func (s *ClusterStatusRegistry) UnfollowAll(sessionID uuid.UUID) {
	s.Lock()

	sessionFollows, ok := s.bySession[sessionID]
	if !ok {
		s.Unlock()
		return
	}
	for userID := range sessionFollows {
		if userFollowers := s.byUser[userID]; len(userFollowers) == 1 {
			delete(s.byUser, userID)
		} else {
			delete(userFollowers, sessionID)
		}
	}
	delete(s.bySession, sessionID)

	s.Unlock()
}

func (s *ClusterStatusRegistry) IsOnline(userID uuid.UUID) bool {
	// First check local cache
	s.onlineMutex.RLock()
	_, found := s.onlineCache[userID]
	s.onlineMutex.RUnlock()
	if found {
		return true
	}

	// Check Redis for cluster-wide status
	key := fmt.Sprintf("%s:%s%s", s.channelPrefix, userSessionsOnlineKey, userID.String())
	count, err := s.redisClient.SCard(key).Result()
	if err != nil {
		s.logger.Warn("Failed to check online status in Redis", zap.Error(err), zap.String("user_id", userID.String()))
		return false
	}
	return count > 0
}

func (s *ClusterStatusRegistry) FillOnlineUsers(users []*api.User) {
	if len(users) == 0 {
		return
	}

	// Build a map of user IDs to check
	userIDs := make([]uuid.UUID, len(users))
	for i, user := range users {
		userIDs[i] = uuid.FromStringOrNil(user.Id)
	}

	// Check online status for all users
	onlineStatuses := s.getOnlineStatuses(userIDs)

	for i, user := range users {
		user.Online = onlineStatuses[userIDs[i]]
	}
}

func (s *ClusterStatusRegistry) FillOnlineAccounts(accounts []*api.Account) {
	if len(accounts) == 0 {
		return
	}

	userIDs := make([]uuid.UUID, len(accounts))
	for i, account := range accounts {
		userIDs[i] = uuid.FromStringOrNil(account.User.Id)
	}

	onlineStatuses := s.getOnlineStatuses(userIDs)

	for i, account := range accounts {
		account.User.Online = onlineStatuses[userIDs[i]]
	}
}

func (s *ClusterStatusRegistry) FillOnlineFriends(friends []*api.Friend) {
	if len(friends) == 0 {
		return
	}

	userIDs := make([]uuid.UUID, len(friends))
	for i, friend := range friends {
		userIDs[i] = uuid.FromStringOrNil(friend.User.Id)
	}

	onlineStatuses := s.getOnlineStatuses(userIDs)

	for i, friend := range friends {
		friend.User.Online = onlineStatuses[userIDs[i]]
	}
}

func (s *ClusterStatusRegistry) FillOnlineGroupUsers(groupUsers []*api.GroupUserList_GroupUser) {
	if len(groupUsers) == 0 {
		return
	}

	userIDs := make([]uuid.UUID, len(groupUsers))
	for i, groupUser := range groupUsers {
		userIDs[i] = uuid.FromStringOrNil(groupUser.User.Id)
	}

	onlineStatuses := s.getOnlineStatuses(userIDs)

	for i, groupUser := range groupUsers {
		groupUser.User.Online = onlineStatuses[userIDs[i]]
	}
}

// getOnlineStatuses checks online status for multiple users efficiently
func (s *ClusterStatusRegistry) getOnlineStatuses(userIDs []uuid.UUID) map[uuid.UUID]bool {
	result := make(map[uuid.UUID]bool, len(userIDs))
	toCheckRedis := make([]uuid.UUID, 0, len(userIDs))

	// First check local cache
	s.onlineMutex.RLock()
	for _, userID := range userIDs {
		if _, found := s.onlineCache[userID]; found {
			result[userID] = true
		} else {
			toCheckRedis = append(toCheckRedis, userID)
		}
	}
	s.onlineMutex.RUnlock()

	// Check Redis for remaining users
	if len(toCheckRedis) > 0 {
		pipe := s.redisClient.Pipeline()
		cmds := make([]*redis.IntCmd, len(toCheckRedis))

		for i, userID := range toCheckRedis {
			key := fmt.Sprintf("%s:%s%s", s.channelPrefix, userSessionsOnlineKey, userID.String())
			cmds[i] = pipe.SCard(key)
		}

		if _, err := pipe.Exec(); err != nil {
			s.logger.Warn("Failed to check online statuses in Redis", zap.Error(err))
		} else {
			for i, userID := range toCheckRedis {
				count, err := cmds[i].Result()
				if err == nil && count > 0 {
					result[userID] = true
				}
			}
		}
	}

	return result
}

func (s *ClusterStatusRegistry) Queue(userID uuid.UUID, joins, leaves []*rtapi.UserPresence) {
	s.eventsCh <- &statusEvent{
		userID: userID,
		joins:  joins,
		leaves: leaves,
	}
}
