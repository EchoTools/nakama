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
	"crypto/tls"
	"fmt"

	"github.com/go-redis/redis"
	"go.uber.org/zap"
	"google.golang.org/protobuf/encoding/protojson"
)

// ClusterComponents holds all cluster-enabled components
type ClusterComponents struct {
	RedisClient     *redis.Client
	SessionRegistry SessionRegistry
	StatusRegistry  StatusRegistry
	Tracker         Tracker
	MessageRouter   MessageRouter
}

// NewClusterComponents initializes all cluster components with Redis backing
func NewClusterComponents(
	ctx context.Context,
	logger *zap.Logger,
	startupLogger *zap.Logger,
	config Config,
	metrics Metrics,
	protojsonMarshaler *protojson.MarshalOptions,
) (*ClusterComponents, error) {
	clusterConfig := config.GetCluster()
	if clusterConfig == nil || !clusterConfig.Enabled {
		return nil, nil
	}

	startupLogger.Info("Initializing cluster mode",
		zap.String("redis_address", clusterConfig.RedisAddress),
		zap.String("node", config.GetName()))

	// Connect to Redis
	redisOpts := &redis.Options{
		Addr:     clusterConfig.RedisAddress,
		Password: clusterConfig.RedisPassword,
		DB:       clusterConfig.RedisDB,
	}

	if clusterConfig.RedisTLS {
		redisOpts.TLSConfig = &tls.Config{
			MinVersion: tls.VersionTLS12,
		}
	}

	redisClient := redis.NewClient(redisOpts)

	// Test Redis connection
	if err := redisClient.Ping().Err(); err != nil {
		return nil, fmt.Errorf("failed to connect to Redis: %w", err)
	}

	startupLogger.Info("Connected to Redis", zap.String("address", clusterConfig.RedisAddress))

	// Create cluster session registry
	sessionRegistry, err := NewClusterSessionRegistry(
		ctx,
		logger,
		metrics,
		clusterConfig,
		config.GetName(),
		redisClient,
	)
	if err != nil {
		redisClient.Close()
		return nil, fmt.Errorf("failed to create cluster session registry: %w", err)
	}

	// Create cluster status registry (needs session registry)
	statusRegistry := NewClusterStatusRegistry(
		ctx,
		logger,
		config,
		clusterConfig,
		sessionRegistry,
		protojsonMarshaler,
		redisClient,
	)

	// Create cluster tracker (needs session registry and status registry)
	tracker := StartClusterTracker(
		ctx,
		logger,
		config,
		clusterConfig,
		sessionRegistry,
		statusRegistry,
		metrics,
		protojsonMarshaler,
		redisClient,
	)

	// Create cluster message router (needs session registry and tracker)
	messageRouter := NewClusterMessageRouter(
		ctx,
		logger,
		sessionRegistry,
		tracker,
		protojsonMarshaler,
		redisClient,
		clusterConfig.ChannelPrefix,
		config.GetName(),
	)

	startupLogger.Info("Cluster components initialized successfully")

	return &ClusterComponents{
		RedisClient:     redisClient,
		SessionRegistry: sessionRegistry,
		StatusRegistry:  statusRegistry,
		Tracker:         tracker,
		MessageRouter:   messageRouter,
	}, nil
}

// Stop gracefully shuts down all cluster components
func (c *ClusterComponents) Stop() {
	if c == nil {
		return
	}

	// Stop components in reverse order of initialization
	if c.MessageRouter != nil {
		if router, ok := c.MessageRouter.(*ClusterMessageRouter); ok {
			router.Stop()
		}
	}

	if c.Tracker != nil {
		c.Tracker.Stop()
	}

	if c.StatusRegistry != nil {
		c.StatusRegistry.Stop()
	}

	if c.SessionRegistry != nil {
		c.SessionRegistry.Stop()
	}

	if c.RedisClient != nil {
		c.RedisClient.Close()
	}
}

// ValidateClusterConfig validates the cluster configuration
func ValidateClusterConfig(logger *zap.Logger, config Config) {
	clusterConfig := config.GetCluster()
	if clusterConfig == nil || !clusterConfig.Enabled {
		return
	}

	if clusterConfig.RedisAddress == "" {
		logger.Fatal("Cluster mode enabled but Redis address not set", zap.String("param", "cluster.redis_address"))
	}

	if clusterConfig.SessionTTLSec < 60 {
		logger.Fatal("Cluster session TTL must be >= 60 seconds", zap.Int("cluster.session_ttl_sec", clusterConfig.SessionTTLSec))
	}

	if clusterConfig.HeartbeatIntervalSec < 1 {
		logger.Fatal("Cluster heartbeat interval must be >= 1 second", zap.Int("cluster.heartbeat_interval_sec", clusterConfig.HeartbeatIntervalSec))
	}

	if clusterConfig.HeartbeatTimeoutSec <= clusterConfig.HeartbeatIntervalSec {
		logger.Fatal("Cluster heartbeat timeout must be greater than heartbeat interval",
			zap.Int("cluster.heartbeat_timeout_sec", clusterConfig.HeartbeatTimeoutSec),
			zap.Int("cluster.heartbeat_interval_sec", clusterConfig.HeartbeatIntervalSec))
	}

	logger.Info("Cluster mode enabled",
		zap.String("redis_address", clusterConfig.RedisAddress),
		zap.String("channel_prefix", clusterConfig.ChannelPrefix),
		zap.Int("session_ttl_sec", clusterConfig.SessionTTLSec))
}
