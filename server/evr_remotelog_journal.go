package server

import (
	"sync"
	"time"

	"github.com/gofrs/uuid/v5"
)

type UserLogJouralRegistry struct {
	sync.RWMutex
	sessionRegistry SessionRegistry
	journalRegistry map[uuid.UUID]map[string]string // map[sessionID]map[messageType]messageJSON
}

func NewUserRemoteLogJournalRegistry(sessionRegistry SessionRegistry) *UserLogJouralRegistry {

	registry := &UserLogJouralRegistry{
		sessionRegistry: sessionRegistry,
		journalRegistry: make(map[uuid.UUID]map[string]string),
	}

	// Every 10 minutes, remove all entries that do not have a session.
	go func() {
		for {
			registry.Lock()
			for sessionID := range registry.journalRegistry {
				if registry.sessionRegistry.Get(sessionID) == nil {
					delete(registry.journalRegistry, sessionID)
				}
			}
			registry.Unlock()
			time.Sleep(10 * time.Minute)
		}
	}()
	return registry
}

// returns true if found
func (r *UserLogJouralRegistry) AddEntries(sessionID uuid.UUID, e []GenericRemoteLog) (found bool) {
	r.Lock()
	defer r.Unlock()

	if _, found = r.journalRegistry[sessionID]; !found {
		r.journalRegistry[sessionID] = make(map[string]string)
	}
	for _, entry := range e {
		r.journalRegistry[sessionID][entry.MessageType] = entry.Message
	}
	return found
}

func (r *UserLogJouralRegistry) RemoveSessionAll(sessionID uuid.UUID) map[string]string {
	r.Lock()
	defer r.Unlock()

	if _, ok := r.journalRegistry[sessionID]; !ok {
		return nil
	}

	defer delete(r.journalRegistry, sessionID)
	return r.journalRegistry[sessionID]
}

func (r *UserLogJouralRegistry) GetSession(sessionID uuid.UUID) (map[string]string, bool) {
	r.RLock()
	defer r.RUnlock()

	if _, ok := r.journalRegistry[sessionID]; !ok {
		return nil, false
	}
	messages, ok := r.journalRegistry[sessionID]
	return messages, ok
}
