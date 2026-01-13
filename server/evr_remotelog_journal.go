package server

import (
	"context"
	"time"

	"github.com/gofrs/uuid/v5"
	"go.uber.org/zap"
)

type JournalPresence struct {
	UserID    uuid.UUID
	SessionID uuid.UUID
}

type UserLogJouralRegistry struct {
	logger     *zap.Logger
	fileWriter *RemoteLogFileWriter

	queueCh chan map[JournalPresence][]string
}

func NewUserRemoteLogJournalRegistry(ctx context.Context, logger *zap.Logger, fileWriter *RemoteLogFileWriter, sessionRegistry SessionRegistry) *UserLogJouralRegistry {

	registry := &UserLogJouralRegistry{
		logger:     logger,
		fileWriter: fileWriter,

		queueCh: make(chan map[JournalPresence][]string, 200),
	}

	// Background goroutine to write logs to disk
	go func() {
		journals := make(map[JournalPresence][]string, 200)
		writeCh := make(chan JournalPresence, 200)

		cleanUpTicker := time.NewTicker(30 * time.Second)
		defer cleanUpTicker.Stop()

		for {
			select {
			case <-ctx.Done():
				// Write all remaining entries
				for presence, logs := range journals {
					if err := registry.fileWriter.Write(presence.UserID, presence.SessionID, logs); err != nil {
						registry.logger.Warn("Failed to write user journals on shutdown", zap.Error(err))
					}
				}
				return

			case entries := <-registry.queueCh:
				for presence, logs := range entries {
					if _, found := journals[presence]; !found {
						// This is the first entry for this session.
						if s := sessionRegistry.Get(presence.SessionID); s != nil {
							// Write it after the session closes.
							go func(p JournalPresence) {
								<-s.Context().Done()
								<-time.After(10 * time.Second)
								select {
								case writeCh <- p:
								default:
									logger.Warn("Failed to queue log write")
								}
							}(presence)
						}
						journals[presence] = make([]string, 0, len(logs))
					}

					journals[presence] = append(journals[presence], logs...)
				}

			case p := <-writeCh:
				if logs, found := journals[p]; found {
					if err := registry.fileWriter.Write(p.UserID, p.SessionID, logs); err != nil {
						registry.logger.Warn("Failed to write user journals", zap.Error(err))
					}
					delete(journals, p)
				}

			case <-cleanUpTicker.C:
				// Write logs for sessions that no longer exist
				for p, logs := range journals {
					if s := sessionRegistry.Get(p.SessionID); s == nil {
						if err := registry.fileWriter.Write(p.UserID, p.SessionID, logs); err != nil {
							registry.logger.Warn("Failed to write user journals", zap.Error(err))
						}
						delete(journals, p)
					}
				}
			}
		}
	}()
	return registry
}

// Add queues log entries for writing
func (r *UserLogJouralRegistry) Add(sessionID, userID uuid.UUID, e []string) {
	select {
	case r.queueCh <- map[JournalPresence][]string{
		{
			UserID:    userID,
			SessionID: sessionID,
		}: e,
	}:
	default:
		r.logger.Warn("Failed to queue log write")
	}
}

// Read retrieves remote log entries for a user (for debugging)
func (r *UserLogJouralRegistry) Read(userID uuid.UUID, since time.Time) ([]RemoteLogFileEntry, error) {
	return r.fileWriter.Read(userID, since)
}
