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
	"time"
)

// ClusterConfig is configuration relevant to distributed/clustered operation.
type ClusterConfig struct {
	Enabled bool `yaml:"enabled" json:"enabled" usage:"Enable distributed/clustered mode. Default false."`

	// Redis configuration for distributed state
	RedisAddress  string `yaml:"redis_address" json:"redis_address" usage:"Redis server address (host:port). Required when cluster is enabled."`
	RedisPassword string `yaml:"redis_password" json:"redis_password" usage:"Redis server password. Optional."`
	RedisDB       int    `yaml:"redis_db" json:"redis_db" usage:"Redis database number. Default 0."`
	RedisTLS      bool   `yaml:"redis_tls" json:"redis_tls" usage:"Use TLS for Redis connection. Default false."`

	// Pub/Sub channel configuration
	ChannelPrefix string `yaml:"channel_prefix" json:"channel_prefix" usage:"Prefix for Redis pub/sub channels. Default 'nakama'."`

	// Session registry configuration
	SessionTTLSec int `yaml:"session_ttl_sec" json:"session_ttl_sec" usage:"TTL in seconds for session entries in Redis. Should be greater than expected session duration. Default 86400 (24 hours)."`

	// Heartbeat configuration
	HeartbeatIntervalSec int `yaml:"heartbeat_interval_sec" json:"heartbeat_interval_sec" usage:"Interval in seconds between node heartbeats. Default 5."`
	HeartbeatTimeoutSec  int `yaml:"heartbeat_timeout_sec" json:"heartbeat_timeout_sec" usage:"Timeout in seconds before a node is considered dead. Default 15."`
}

func (cfg *ClusterConfig) Clone() *ClusterConfig {
	if cfg == nil {
		return nil
	}
	cfgCopy := *cfg
	return &cfgCopy
}

func NewClusterConfig() *ClusterConfig {
	return &ClusterConfig{
		Enabled:              false,
		RedisAddress:         "localhost:6379",
		RedisPassword:        "",
		RedisDB:              0,
		RedisTLS:             false,
		ChannelPrefix:        "nakama",
		SessionTTLSec:        86400, // 24 hours
		HeartbeatIntervalSec: 5,
		HeartbeatTimeoutSec:  15,
	}
}

// GetSessionTTL returns the session TTL as a time.Duration
func (cfg *ClusterConfig) GetSessionTTL() time.Duration {
	return time.Duration(cfg.SessionTTLSec) * time.Second
}

// GetHeartbeatInterval returns the heartbeat interval as a time.Duration
func (cfg *ClusterConfig) GetHeartbeatInterval() time.Duration {
	return time.Duration(cfg.HeartbeatIntervalSec) * time.Second
}

// GetHeartbeatTimeout returns the heartbeat timeout as a time.Duration
func (cfg *ClusterConfig) GetHeartbeatTimeout() time.Duration {
	return time.Duration(cfg.HeartbeatTimeoutSec) * time.Second
}
