package server

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/gofrs/uuid/v5"
	"github.com/heroiclabs/nakama-common/runtime"
	"go.uber.org/zap"
)

const (
	RemoteLogStorageCollection = "RemoteLogs"
	RemoteLogStorageJournalKey = "journal"
)

type UserLogJournal struct {
	sync.Mutex
	UserID  uuid.UUID              `json:"userID"`
	Entries []*UserLogJournalEntry `json:"entries"`
}

type UserLogJournalEntry struct {
	SessionID uuid.UUID `json:"sessionID"`
	Timestamp time.Time `json:"timestamp"`
	Message   string    `json:"message"`
}

type UserLogJouralRegistry struct {
	sync.RWMutex
	logger *zap.Logger
	nk     runtime.NakamaModule

	storeQueueCh    chan *UserLogJournal
	sessionRegistry SessionRegistry
	journalRegistry map[uuid.UUID]*UserLogJournal // map[sessionID]messages
}

func NewUserRemoteLogJournalRegistry(ctx context.Context, logger *zap.Logger, nk runtime.NakamaModule, sessionRegistry SessionRegistry) *UserLogJouralRegistry {

	registry := &UserLogJouralRegistry{
		logger: logger,
		nk:     nk,

		storeQueueCh:    make(chan *UserLogJournal, 200),
		sessionRegistry: sessionRegistry,
		journalRegistry: make(map[uuid.UUID]*UserLogJournal),
	}

	// Every 10 minutes, remove all entries that do not have a session.
	go func() {

		cleanUpTicker := time.NewTicker(10 * time.Minute)
		defer cleanUpTicker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case j := <-registry.storeQueueCh:
				j.Lock()
				userID := j.UserID
				entries := j.Entries
				j.Entries = nil
				j.Unlock()

				if err := registry.WriteUserJournal(ctx, userID, entries); err != nil {
					registry.logger.Warn("Failed to write user journal", zap.String("userID", userID.String()), zap.Error(err))
					continue
				}
			case <-cleanUpTicker.C:

				registry.Lock()
				for sessionID := range registry.journalRegistry {
					if registry.sessionRegistry.Get(sessionID) == nil {
						delete(registry.journalRegistry, sessionID)
					}
				}
				registry.Unlock()
			}
		}
	}()
	return registry
}

// returns true if found
func (r *UserLogJouralRegistry) AddEntries(sessionID uuid.UUID, e []string) (found bool) {

	r.RLock()
	journal, found := r.journalRegistry[sessionID]
	defer r.RUnlock()

	if !found {
		session := r.sessionRegistry.Get(sessionID)
		if session == nil {
			return false
		}
		journal = &UserLogJournal{
			UserID:  session.UserID(),
			Entries: make([]*UserLogJournalEntry, 0, 10),
		}

		r.Lock()
		r.journalRegistry[sessionID] = journal
		r.Unlock()

		// Write it after the session closes.
		go func() {
			<-session.Context().Done()
			r.QueueStorage(sessionID)
		}()
	}

	journal.Lock()
	for _, entry := range e {
		journal.Entries = append(journal.Entries, &UserLogJournalEntry{
			SessionID: sessionID,
			Timestamp: time.Now(),
			Message:   entry,
		})

	}
	journal.Unlock()
	return found
}

func (r *UserLogJouralRegistry) loadAndDelete(sessionID uuid.UUID) *UserLogJournal {
	r.Lock()
	defer r.Unlock()

	journal, ok := r.journalRegistry[sessionID]
	if !ok {
		return nil
	}

	delete(r.journalRegistry, sessionID)
	return journal
}

func (r *UserLogJouralRegistry) GetLogs(sessionID uuid.UUID) *UserLogJournal {
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

func (r *UserLogJouralRegistry) QueueStorage(sessionID uuid.UUID) {
	journal := r.loadAndDelete(sessionID)
	select {
	case r.storeQueueCh <- journal:
		return
	default:
		r.logger.Warn("Failed to queue log storage")
	}
}
func (r *UserLogJouralRegistry) WriteUserJournal(ctx context.Context, userID uuid.UUID, messages []*UserLogJournalEntry) error {

	storedJournal, err := r.storageLoad(ctx, userID)
	if err != nil {
		r.logger.Warn("Failed to load remote log, overwriting.", zap.Error(err))
	}
	if storedJournal == nil {
		storedJournal = make(map[time.Time][]*UserLogJournalEntry)
	}

	// Remove old messages
	for k := range storedJournal {
		if time.Since(k) > time.Hour*24*3 {
			delete(storedJournal, k)
		}
	}

	// Add the current messages
	storedJournal[time.Now()] = messages

	data, err := json.Marshal(storedJournal)
	if err != nil {
		return err
	}

	if len(data) == 0 {
		return nil
	}

	// Limit the overall size
	for {
		if len(data) < 4*1024*1024 {
			break
		}

		// Remove the oldest messages
		oldest := time.Now()
		for k := range storedJournal {
			if k.Before(oldest) {
				oldest = k
			}
		}

		delete(storedJournal, oldest)

		data, err = json.Marshal(storedJournal)
		if err != nil {
			return fmt.Errorf("failed to marshal journal: %w", err)
		}
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
