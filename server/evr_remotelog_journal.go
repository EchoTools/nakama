package server

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/gofrs/uuid/v5"
	"github.com/heroiclabs/nakama-common/runtime"
	"go.uber.org/zap"
)

const (
	RemoteLogStorageCollection = "RemoteLogs"
	RemoteLogStorageJournalKey = "journal"
)

type RemoteUserLogMessage struct {
	SessionID uuid.UUID `json:"sessionID"`
	Timestamp time.Time `json:"timestamp"`
	Message   string    `json:"message"`
}

type JournalPresence struct {
	UserID    uuid.UUID
	SessionID uuid.UUID
}

type UserLogJouralRegistry struct {
	logger *zap.Logger
	nk     runtime.NakamaModule

	queueCh chan map[JournalPresence][]string
}

func NewUserRemoteLogJournalRegistry(ctx context.Context, logger *zap.Logger, nk runtime.NakamaModule, sessionRegistry SessionRegistry) *UserLogJouralRegistry {

	registry := &UserLogJouralRegistry{
		logger: logger,
		nk:     nk,

		queueCh: make(chan map[JournalPresence][]string, 200),
	}

	// Every 10 minutes, remove all entries that do not have a session.
	go func() {
		journals := make(map[JournalPresence]map[time.Time][]string, 200)
		storageCh := make(chan JournalPresence, 200)

		cleanUpTicker := time.NewTicker(30 * time.Second)
		defer cleanUpTicker.Stop()

		for {
			select {
			case <-ctx.Done():
				// Write all remaining entries
				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				defer cancel()

				if err := registry.storageWrite(ctx, logger, journals); err != nil {
					registry.logger.Warn("Failed to write user journals", zap.Error(err))
				}

				return

			case entries := <-registry.queueCh:
				now := time.Now().UTC()
				for presence, logs := range entries {

					if _, found := journals[presence]; !found {
						// This is the first entry for this session.
						if s := sessionRegistry.Get(presence.SessionID); s != nil {
							// Write it after the session closes.
							go func() {
								<-s.Context().Done()
								<-time.After(10 * time.Second)
								select {
								case storageCh <- presence:
									return
								default:
									logger.Warn("Failed to queue log storage")
								}
							}()
						}
						journals[presence] = make(map[time.Time][]string, len(logs))
					}

					journals[presence][now] = append(journals[presence][now], logs...)

				}

			case p := <-storageCh:
				journal := journals[p]
				delete(journals, p)
				if err := registry.storageWrite(ctx, logger, map[JournalPresence]map[time.Time][]string{p: journal}); err != nil {
					registry.logger.Warn("Failed to write user journals", zap.Error(err))
				}
			case <-cleanUpTicker.C:

				queue := make(map[JournalPresence]map[time.Time][]string, 200)

				for p, j := range journals {
					if s := sessionRegistry.Get(p.SessionID); s == nil {
						queue[p] = j
						delete(journals, p)
					}
				}

				if err := registry.storageWrite(ctx, logger, queue); err != nil {
					registry.logger.Warn("Failed to write user journals", zap.Error(err))
				}
			}
		}

	}()
	return registry
}

// returns true if found
func (r *UserLogJouralRegistry) Add(sessionID, userID uuid.UUID, e []string) {
	select {
	case r.queueCh <- map[JournalPresence][]string{
		{
			UserID:    userID,
			SessionID: sessionID,
		}: e,
	}:
	default:
		r.logger.Warn("Failed to queue log storage")
	}
}

func (r *UserLogJouralRegistry) storageRead(ctx context.Context, userID uuid.UUID) (map[time.Time][]*RemoteUserLogMessage, error) {

	objs, err := r.nk.StorageRead(ctx, []*runtime.StorageRead{{
		UserID:     userID.String(),
		Collection: RemoteLogStorageCollection,
		Key:        RemoteLogStorageJournalKey,
	}})
	if err != nil {
		return nil, err
	}

	if len(objs) == 0 {
		return nil, nil
	}

	var journal map[time.Time][]*RemoteUserLogMessage
	if err := json.Unmarshal([]byte(objs[0].Value), &journal); err != nil {
		return nil, err
	}

	return journal, nil
}

func (r *UserLogJouralRegistry) storageWrite(ctx context.Context, logger *zap.Logger, journals map[JournalPresence]map[time.Time][]string) error {

	now := time.Now().UTC()
	var data []byte
	var journal map[time.Time][]*RemoteUserLogMessage
	var err error

	ops := make([]*runtime.StorageWrite, 0, len(journals))

	for presence, entries := range journals {

		journal, err = r.storageRead(ctx, presence.UserID)
		if err != nil {
			r.logger.Warn("Failed to load remote log, overwriting.", zap.Error(err))
		}
		if journal == nil {
			journal = make(map[time.Time][]*RemoteUserLogMessage)
		}

		// Remove old messages
		for k := range journal {
			if time.Since(k) > time.Hour*24*7 {
				delete(journal, k)
			}
		}

		for ts, e := range entries {
			for _, m := range e {
				journal[now] = append(journal[now], &RemoteUserLogMessage{
					SessionID: presence.SessionID,
					Timestamp: ts,
					Message:   m,
				})
			}
		}

		// Limit the overall size

		for {

			if data, err = json.Marshal(journal); err != nil {
				return err
			} else if len(data) < 8*1024*1024 {
				break
			}

			// Remove the oldest messages
			oldest := time.Now()
			for k := range journal {
				if k.Before(oldest) {
					oldest = k
				}
			}

			delete(journal, oldest)
		}

		ops = append(ops, &runtime.StorageWrite{
			UserID:          presence.UserID.String(),
			Collection:      RemoteLogStorageCollection,
			Key:             RemoteLogStorageJournalKey,
			Value:           string(data),
			PermissionRead:  0,
			PermissionWrite: 0,
		})
	}

	if _, err = r.nk.StorageWrite(ctx, ops); err != nil {
		return fmt.Errorf("Failed to write remote log: %v", err)
	}

	return nil
}
