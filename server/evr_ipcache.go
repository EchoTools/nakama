package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"sync"

	"github.com/gofrs/uuid/v5"
	"github.com/heroiclabs/nakama-common/runtime"
	"github.com/ipinfo/go/v2/ipinfo"
	"go.uber.org/zap"
)

type IPinfoCache struct {
	token  string
	logger *zap.Logger
	store  map[string]*ipinfo.Core
	mu     sync.RWMutex
	client *ipinfo.Client
}

func NewIPinfoCache(logger *zap.Logger, db *sql.DB, token string) *IPinfoCache {
	client := ipinfo.NewClient(nil, nil, token)
	return &IPinfoCache{
		logger: logger,
		client: client,
		store:  make(map[string]*ipinfo.Core),
	}
}

func (c *IPinfoCache) LoadCache(ctx context.Context, nk runtime.NakamaModule, db *sql.DB) error {
	objs, err := nk.StorageRead(ctx, []*runtime.StorageRead{
		{
			Collection: CacheStorageCollection,
			Key:        IPinfoCacheKey,
			UserID:     uuid.Nil.String(),
		},
	})
	if err != nil {
		return fmt.Errorf("failed to read IPinfo cache: %w", err)
	}
	if len(objs) == 0 {
		return nil
	}

	var data map[string]*ipinfo.Core
	if err := json.Unmarshal([]byte(objs[0].Value), &data); err != nil {
		return fmt.Errorf("failed to unmarshal IPinfo cache: %w", err)
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	for k, v := range data {
		c.store[k] = v
	}

	return nil
}

func (c *IPinfoCache) StoreCache(ctx context.Context, nk runtime.NakamaModule, db *sql.DB) error {
	data := make(map[string]*ipinfo.Core)
	c.mu.RLock()
	for k, v := range c.store {
		data[k] = v
	}
	c.mu.RUnlock()

	dataBytes, err := json.Marshal(data)
	if err != nil {
		return fmt.Errorf("failed to marshal IPinfo cache: %w", err)
	}

	if _, err := nk.StorageWrite(ctx, []*runtime.StorageWrite{
		{
			Collection: CacheStorageCollection,
			Key:        IPinfoCacheKey,
			UserID:     uuid.Nil.String(),
			Value:      string(dataBytes),
		},
	}); err != nil {
		return fmt.Errorf("failed to write IPinfo cache: %w", err)
	}

	return nil
}

func (c *IPinfoCache) retrieveIPinfo(ctx context.Context, logger *zap.Logger, ip net.IP) (*ipinfo.Core, error) {
	info, err := c.client.GetIPInfo(ip)
	if err != nil {
		return nil, err
	}
	return info, nil
}

func (c *IPinfoCache) DetermineExternalIPAddress() (net.IP, error) {
	response, err := http.Get("https://api.ipify.org?format=text")
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()

	data, _ := io.ReadAll(response.Body)
	addr := net.ParseIP(string(data))
	if addr == nil {
		return nil, errors.New("failed to parse IP address")
	}
	return addr, nil
}

func (c *IPinfoCache) isPrivateIP(ip net.IP) bool {
	privateRanges := []string{
		"127.0.0.0/8",
		"10.0.0.0/8",
		"172.16.0.0/12",
		"192.168.0.0/16",
	}
	for _, cidr := range privateRanges {
		_, subnet, _ := net.ParseCIDR(cidr)
		if subnet.Contains(ip) {
			return true
		}
	}
	return false
}
