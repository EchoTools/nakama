package server

import (
	"container/list"
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/gofrs/uuid/v5"
	"github.com/heroiclabs/nakama-common/api"
	"github.com/heroiclabs/nakama/v3/server/evr"
	"go.uber.org/zap"
)

const configCacheTTL = 5 * time.Minute

// configCacheMaxEntries bounds the number of distinct config Types held in
// memory. ConfigRequest is reachable with only the (public) ServerKey, so the
// cache must never be able to grow without a hard size cap (SEC-1).
const configCacheMaxEntries = 256

// configStorageRead is the storage read used by configRequest. It is a package
// variable so tests can substitute a counting stub without a live database;
// production always uses StorageReadObjects.
var configStorageRead = StorageReadObjects

// cachedConfigEntry holds the result of a storage read for a config Type. A
// negative result (json="") is cached too, so that a flood of requests with
// distinct client-supplied IDs for the same Type collapses to a single storage
// read instead of one read per packet (SEC-1 amplification).
type cachedConfigEntry struct {
	json   string
	expiry time.Time
}

// configCacheStore is a bounded, TTL'd LRU keyed on the config Type only — the
// same key the storage read itself uses (Collection "Config:"+Type, Key Type;
// the read ignores ID). The previous implementation keyed the cache on the
// client-controlled Type+":"+ID, which let any remote caller holding the
// public ServerKey (a) miss the cache on every packet and force one DB read
// each, and (b) grow the cache without bound when a stored Config:<Type>
// object existed. Keying on Type plus an LRU size cap removes both (SEC-1).
type configCacheStore struct {
	mu      sync.Mutex
	ll      *list.List
	entries map[string]*list.Element
	maxSize int
}

type configCacheItem struct {
	key   string
	entry *cachedConfigEntry
}

func newConfigCacheStore(maxSize int) *configCacheStore {
	return &configCacheStore{
		ll:      list.New(),
		entries: make(map[string]*list.Element),
		maxSize: maxSize,
	}
}

// get returns the cached entry for key if present and unexpired.
func (c *configCacheStore) get(key string) (*cachedConfigEntry, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	el, ok := c.entries[key]
	if !ok {
		return nil, false
	}
	item := el.Value.(*configCacheItem)
	if time.Now().After(item.entry.expiry) {
		c.ll.Remove(el)
		delete(c.entries, key)
		return nil, false
	}
	c.ll.MoveToFront(el)
	return item.entry, true
}

// put stores entry under key, evicting the least-recently-used entries once
// the store exceeds maxSize.
func (c *configCacheStore) put(key string, entry *cachedConfigEntry) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if el, ok := c.entries[key]; ok {
		el.Value.(*configCacheItem).entry = entry
		c.ll.MoveToFront(el)
		return
	}
	el := c.ll.PushFront(&configCacheItem{key: key, entry: entry})
	c.entries[key] = el
	for c.maxSize > 0 && c.ll.Len() > c.maxSize {
		oldest := c.ll.Back()
		if oldest == nil {
			break
		}
		c.ll.Remove(oldest)
		delete(c.entries, oldest.Value.(*configCacheItem).key)
	}
}

func (c *configCacheStore) len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.ll.Len()
}

func (c *configCacheStore) clear() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.ll.Init()
	c.entries = make(map[string]*list.Element)
}

var configCache = newConfigCacheStore(configCacheMaxEntries)

// Test helpers (used by evr_pipeline_config_sec_test.go).
func clearConfigCacheForTest()   { configCache.clear() }
func configCacheLenForTest() int { return configCache.len() }

func (p *EvrPipeline) configRequest(ctx context.Context, logger *zap.Logger, session *sessionWS, in evr.Message) error {
	message, ok := in.(*evr.ConfigRequest)
	if !ok {
		return fmt.Errorf("expected *evr.ConfigRequest, got %T", in)
	}

	// Look up (and cache) the stored config for this Type. The lookup is keyed
	// on Type only — the client-supplied ID never touches the cache key or the
	// storage read — so distinct-ID floods for the same Type cost at most one
	// DB read (SEC-1).
	jsonResource, err := p.loadConfigJSON(ctx, logger, session, message.Type)
	if err != nil {
		// DB error: log and fall back to the default below.
		logger.Warn("failed to read config objects, falling back to default", zap.Error(err))
	}

	// Fall back to the built-in default if no valid stored entry was found. The
	// default is resolved per (Type, ID) and requires no DB access.
	if jsonResource == "" {
		jsonResource = evr.GetDefaultConfigResource(message.Type, message.ID)
	}

	if jsonResource == "" {
		logger.Warn("config resource not found", zap.String("type", message.Type), zap.String("id", message.ID))
		session.SendEvrUnrequire(evr.NewConfigFailure(message.Type, message.ID))
		return fmt.Errorf("config resource not found: type=%s id=%s", message.Type, message.ID)
	}

	// Parse the JSON resource into a generic map for re-serialization.
	resource := make(map[string]any)
	if err := json.Unmarshal([]byte(jsonResource), &resource); err != nil {
		// This should not happen for defaults; log and report.
		logger.Error("failed to parse config resource JSON",
			zap.String("type", message.Type), zap.Error(err))
		session.SendEvrUnrequire(evr.NewConfigFailure(message.Type, message.ID))
		return fmt.Errorf("failed to parse config JSON: type=%s: %w", message.Type, err)
	}

	// Send the resource to the client.
	if err := session.SendEvrUnrequire(evr.NewConfigSuccess(message.Type, message.ID, resource)); err != nil {
		return fmt.Errorf("failed to send SNSConfigSuccess: %w", err)
	}
	return nil
}

// loadConfigJSON returns the stored config JSON for a Type, using the bounded
// TTL cache keyed on Type. On a cache miss it performs exactly one storage
// read and caches the result — including "no stored object" — so repeated
// requests for the same Type, regardless of the client-supplied ID, do not hit
// the database again. Transient DB errors are not cached so a real config can
// still appear once the database recovers.
func (p *EvrPipeline) loadConfigJSON(ctx context.Context, logger *zap.Logger, session *sessionWS, configType string) (string, error) {
	// Whitelist gate (SEC-1): only the small, fixed set of legitimate config
	// types may reach the cache or the storage read. configType is the fully
	// client-controlled message.Type; keying the cache on Type alone (the SEC-1
	// re-key) does not help when Type itself is unvalidated — an attacker sending
	// a unique Type per packet misses the cache every time, forcing one DB read
	// each (negative-cached, then LRU-evicted), which reproduces the exact
	// per-packet DB amplification SEC-1 exists to kill. Rejecting unrecognized
	// Types here — before the cache and before configStorageRead — makes an
	// unknown-Type flood cost zero DB reads. The valid set is
	// evr.IsValidConfigType, derived from the same table GetDefaultConfigResource
	// uses (single source of truth: server/evr/config_success.go).
	if !evr.IsValidConfigType(configType) {
		return "", nil
	}

	if entry, ok := configCache.get(configType); ok {
		return entry.json, nil
	}

	objs, err := configStorageRead(ctx, logger, session.pipeline.db, uuid.Nil, []*api.ReadStorageObjectId{
		{
			Collection: "Config:" + configType,
			Key:        configType,
			UserId:     uuid.Nil.String(),
		},
	})
	if err != nil {
		return "", err
	}

	entry := &cachedConfigEntry{expiry: time.Now().Add(configCacheTTL)}
	if len(objs.Objects) != 0 {
		jsonCandidate := objs.Objects[0].Value
		// Validate the stored JSON before using (and caching) it.
		var probe map[string]any
		if jsonErr := json.Unmarshal([]byte(jsonCandidate), &probe); jsonErr != nil {
			logger.Warn("stored config resource is invalid JSON, falling back to default",
				zap.String("type", configType), zap.Error(jsonErr))
		} else {
			entry.json = jsonCandidate
		}
	}

	configCache.put(configType, entry)
	return entry.json, nil
}
