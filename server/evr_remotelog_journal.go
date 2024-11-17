package server

import (
	"context"
	"encoding/json"
	"sync"
	"time"

	"github.com/gofrs/uuid/v5"
	"github.com/heroiclabs/nakama-common/runtime"
	"go.uber.org/zap"
)

type UserLogJouralRegistry struct {
	sync.RWMutex
	logger *zap.Logger
	nk     runtime.NakamaModule

	sessionRegistry SessionRegistry
	journalRegistry map[uuid.UUID]map[string]string // map[sessionID]map[messageType]messageJSON
}

func NewUserRemoteLogJournalRegistry(logger *zap.Logger, nk runtime.NakamaModule, sessionRegistry SessionRegistry) *UserLogJouralRegistry {

	registry := &UserLogJouralRegistry{
		logger: logger,
		nk:     nk,

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
func (r *UserLogJouralRegistry) AddEntries(sessionID uuid.UUID, e []*GenericRemoteLog) (found bool) {
	r.Lock()
	defer r.Unlock()

	if _, found = r.journalRegistry[sessionID]; !found {
		r.journalRegistry[sessionID] = make(map[string]string)
	}
	for _, entry := range e {
		r.journalRegistry[sessionID][entry.MessageType] = entry.Message
	}

	if !found {
		go func() {

			session := r.sessionRegistry.Get(sessionID)
			if session == nil {
				return
			}
			<-session.Context().Done()
			ctx, cancel := context.WithTimeout(context.Background(), time.Second*5)
			defer cancel()
			if err := r.StoreLogs(ctx, sessionID, session.UserID()); err != nil {
				r.logger.Error("Failed to store logs", zap.Error(err))
			}

		}()
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

func (r *UserLogJouralRegistry) StoreLogs(ctx context.Context, sessionID, userID uuid.UUID) error {

	messages := r.RemoveSessionAll(sessionID)

	// Get the existing remoteLog from storage.
	journal := make(map[time.Time]map[string]string)

	rops := []*runtime.StorageRead{
		{
			UserID:     userID.String(),
			Collection: RemoteLogStorageCollection,
			Key:        RemoteLogStorageJournalKey,
		},
	}

	results, err := r.nk.StorageRead(ctx, rops)
	if err != nil {
		return err
	}

	if len(results) > 0 {
		if err := json.Unmarshal([]byte(results[0].Value), &journal); err != nil {
			return err
		}
	}

	// Remove any logs over a month old
	for k := range journal {
		if time.Since(k) > time.Hour*24*30 {
			delete(journal, k)
		}
	}

	// Add the current messages
	journal[time.Now()] = messages

	data, err := json.Marshal(journal)
	if err != nil {
		return err
	}
	// Write the remoteLog to storage.
	wops := []*runtime.StorageWrite{
		{
			UserID:          userID.String(),
			Collection:      RemoteLogStorageCollection,
			Key:             RemoteLogStorageJournalKey,
			Value:           string(data),
			PermissionRead:  1,
			PermissionWrite: 0,
		},
	}

	_, err = r.nk.StorageWrite(ctx, wops)
	if err != nil {
		return err
	}
	return nil
}
