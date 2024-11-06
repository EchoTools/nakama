package server

import (
	"sync"
	"time"
)

type Cache struct {
	sync.RWMutex
	Store map[string]*CacheEntry
}

type CacheEntry struct {
	Value     any
	Timestamp time.Time
}

func NewCache() *Cache {
	return &Cache{
		Store: make(map[string]*CacheEntry),
	}
}

func (c *Cache) Set(key string, value any, ttl time.Duration) {
	c.Lock()
	defer c.Unlock()
	c.Store[key] = &CacheEntry{
		Value:     value,
		Timestamp: time.Now(),
	}

	time.AfterFunc(ttl, func() { c.Remove(key) })
}

func (c *Cache) Get(key string) (value any, timestamp time.Time, found bool) {
	c.RLock()
	defer c.RUnlock()
	if entry, found := c.Store[key]; found {
		return entry.Value, entry.Timestamp, true
	}
	return
}

// get if the timestamp is not expired
func (c *Cache) GetIfNotExpired(key string, expiry time.Time) (value any, found bool) {
	value, timestamp, found := c.Get(key)
	if !found || timestamp.After(expiry) {
		return nil, false
	}
	return value, true
}

func (c *Cache) Count() int {
	c.RLock()
	defer c.RUnlock()
	return len(c.Store)
}

func (c *Cache) Clear() {
	c.Lock()
	defer c.Unlock()
	c.Store = make(map[string]*CacheEntry)
}

func (c *Cache) Remove(key string) {
	c.Lock()
	defer c.Unlock()
	delete(c.Store, key)
}

func (c *Cache) Range(f func(key string, value any, timestamp time.Time) bool) {
	c.Lock()
	defer c.Unlock()
	for k, v := range c.Store {
		if !f(k, v.Value, v.Timestamp) {
			break
		}
	}
}
