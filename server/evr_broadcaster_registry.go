package server

import (
	"context"
	"sync"
	"time"
)

type Broadcaster struct {
	IsInMatch bool
	LastPing  time.Time
}

type BroadcasterRegistry struct {
	Broadcasters map[string]*Broadcaster
	mutex        sync.RWMutex
}

func NewBroadcasterRegistry() *BroadcasterRegistry {
	return &BroadcasterRegistry{
		Broadcasters: make(map[string]*Broadcaster),
	}
}

func (br *BroadcasterRegistry) AddBroadcaster(id string, b *Broadcaster) {
	br.mutex.Lock()
	defer br.mutex.Unlock()
	br.Broadcasters[id] = b
}

func (br *BroadcasterRegistry) RemoveBroadcaster(id string) {
	br.mutex.Lock()
	defer br.mutex.Unlock()
	delete(br.Broadcasters, id)
}

func (br *BroadcasterRegistry) HealthCheck(ctx context.Context) {
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}

		var toRemove []string

		br.mutex.RLock()
		for id, b := range br.Broadcasters {
			if time.Since(b.LastPing) > 10*time.Second {
				if b.IsInMatch {
					if time.Since(b.LastPing) > 30*time.Second {
						toRemove = append(toRemove, id)
					}
				} else {
					toRemove = append(toRemove, id)
				}
			}
		}
		br.mutex.RUnlock()

		for _, id := range toRemove {
			br.RemoveBroadcaster(id)
		}
	}
}
