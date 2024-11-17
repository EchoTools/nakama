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

type UserLogJournalEntry struct {
	SessionID uuid.UUID `json:"sessionID"`
	Timestamp time.Time `json:"timestamp"`
	Message   string    `json:"message"`
}

type UserLogJouralRegistry struct {
	sync.RWMutex
	logger *zap.Logger
	nk     runtime.NakamaModule

	sessionRegistry SessionRegistry
	journalRegistry map[uuid.UUID][]*UserLogJournalEntry // map[sessionID]messages
}

func NewUserRemoteLogJournalRegistry(logger *zap.Logger, nk runtime.NakamaModule, sessionRegistry SessionRegistry) *UserLogJouralRegistry {

	registry := &UserLogJouralRegistry{
		logger: logger,
		nk:     nk,

		sessionRegistry: sessionRegistry,
		journalRegistry: make(map[uuid.UUID][]*UserLogJournalEntry),
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
func (r *UserLogJouralRegistry) AddEntries(sessionID uuid.UUID, e []string) (found bool) {
	r.Lock()
	defer r.Unlock()

	if _, found = r.journalRegistry[sessionID]; !found {
		r.journalRegistry[sessionID] = make([]*UserLogJournalEntry, 0, len(e))
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

	for _, entry := range e {
		r.journalRegistry[sessionID] = append(r.journalRegistry[sessionID], &UserLogJournalEntry{
			SessionID: sessionID,
			Timestamp: time.Now(),
			Message:   entry,
		})
	}

	return found
}

func (r *UserLogJouralRegistry) loadAndDelete(sessionID uuid.UUID) []*UserLogJournalEntry {
	r.Lock()
	defer r.Unlock()

	if _, ok := r.journalRegistry[sessionID]; !ok {
		return nil
	}

	entries := r.journalRegistry[sessionID]
	delete(r.journalRegistry, sessionID)
	return entries
}

func (r *UserLogJouralRegistry) GetLogs(sessionID uuid.UUID) []*UserLogJournalEntry {
	r.RLock()
	defer r.RUnlock()

	if _, ok := r.journalRegistry[sessionID]; !ok {
		return nil
	}

	return r.journalRegistry[sessionID]
}

func (r *UserLogJouralRegistry) storageLoad(ctx context.Context, userID uuid.UUID) (map[time.Time][]*UserLogJournalEntry, error) {

	storageRead := &runtime.StorageRead{
		UserID:     userID.String(),
		Collection: RemoteLogStorageCollection,
		Key:        RemoteLogStorageJournalKey,
	}

	objs, err := r.nk.StorageRead(ctx, []*runtime.StorageRead{storageRead})
	if err != nil {
		return nil, err
	}

	if len(objs) == 0 {
		return nil, nil
	}

	var journal map[time.Time][]*UserLogJournalEntry
	if err := json.Unmarshal([]byte(objs[0].Value), &journal); err != nil {
		return nil, err
	}

	return journal, nil
}

func (r *UserLogJouralRegistry) StoreLogs(ctx context.Context, sessionID, userID uuid.UUID) error {

	messages := r.loadAndDelete(sessionID)

	journal, err := r.storageLoad(ctx, userID)
	if err != nil {
		r.logger.Warn("Failed to load remote log, overwriting.", zap.Error(err))
	}
	if journal == nil {
		journal = make(map[time.Time][]*UserLogJournalEntry)
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
