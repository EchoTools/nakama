package server

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/gofrs/uuid/v5"
	"github.com/heroiclabs/nakama-common/api"
	"github.com/heroiclabs/nakama-common/runtime"
)

const (
	EventLobbySessionAuthorized = "lobby_session_authorized"
	EventUserLogin              = "user_login"
	EventAccountUpdated         = "account_updated"
	EventSessionStart           = "session_start"
)

type EventDispatch struct {
	ctx    context.Context
	logger runtime.Logger
	nk     runtime.NakamaModule
	db     *sql.DB

	cache *sync.Map
}

func NewEventDispatch(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, initializer runtime.Initializer) (*EventDispatch, error) {
	return &EventDispatch{
		ctx:    ctx,
		logger: logger,
		db:     db,
		nk:     nk,
		cache:  &sync.Map{},
	}, nil
}

func (h *EventDispatch) eventFn(ctx context.Context, logger runtime.Logger, evt *api.Event) {
	logger.WithField("event", evt).Debug("received event")

	eventMap := map[string]func(context.Context, runtime.Logger, map[string]string) error{
		EventLobbySessionAuthorized: h.eventSessionStart,
		EventUserLogin:              h.eventSessionStart,
		EventAccountUpdated:         h.eventSessionEnd,
		EventSessionStart:           h.eventSessionStart,
	}

	fields := map[string]any{
		"event":   evt.Name,
		"handler": fmt.Sprintf("%T", h),
	}

	for k, v := range evt.Properties {
		fields[k] = v
	}

	logger = logger.WithFields(fields)

	if fn, ok := eventMap[evt.Name]; ok {
		if err := fn(ctx, logger, evt.Properties); err != nil {
			logger.Error("error processing event: %v", err)
		}
		logger.Debug("processed event")
	} else {
		logger.Warn("unhandled event: %s", evt.Name)
	}

}

func (h *EventDispatch) eventSessionStart(ctx context.Context, logger runtime.Logger, properties map[string]string) error {
	logger.Debug("process event session start: %+v", properties)
	return nil
}

func (h *EventDispatch) eventSessionEnd(ctx context.Context, logger runtime.Logger, properties map[string]string) error {
	logger.Debug("process event session end: %+v", properties)
	return nil
}

func (h *EventDispatch) handleLobbyAuthorized(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, cache *sync.Map, properties map[string]string) error {
	groupID := properties["group_id"]
	userID := properties["user_id"]
	sessionID := properties["session_id"]

	var err error
	var md *GroupMetadata

	key := groupID + ":*GroupMetadata"

	if v, ok := cache.Load(key); ok {
		md = v.(*GroupMetadata)
	} else {
		// Notify the group of the login, if it's an alternate
		md, err = GetGuildGroupMetadata(ctx, db, groupID)
		if err != nil {
			return fmt.Errorf("failed to get guild group metadata: %w", err)
		}
		cache.Store(key, md)

		// Clear it after thirty seconds
		go func() {
			<-time.After(30 * time.Second)
			cache.Delete(key)
		}()
	}
	if md.LogAlternateAccounts {
		// Get the users session
		_nk := nk.(*RuntimeGoNakamaModule)

		s := _nk.sessionRegistry.Get(uuid.FromStringOrNil(sessionID))
		if s == nil {
			return fmt.Errorf("failed to get session")
		}

		params, ok := LoadParams(ctx)
		if !ok {
			return fmt.Errorf("failed to load params")
		}

		loginHistory, err := LoginHistoryLoad(ctx, nk, userID)
		if err != nil {
			logger.Error("error loading login history: %v", err)
		}

		if ok := loginHistory.NotifyGroup(groupID); ok {

			altAccounts, err := nk.AccountsGetId(ctx, loginHistory.SecondOrderAlternates)
			if err != nil {
				return fmt.Errorf("failed to get alternate accounts: %w", err)
			}
			alts := make([]string, 0, len(altAccounts))
			for _, a := range altAccounts {
				// Check if any are banned, or currently suspended by the guild
				if a.User.Id == userID {
					continue
				}
				s := "<@" + a.CustomId + ">"
				if md.IsSuspended(s) {
					s = s + " (suspended)"
				} else if a.DisableTime != nil {
					s = s + " (globally banned)"
				}

				alts = append(alts, s)
			}
			content := fmt.Sprintf("<@%s> detected as a possible alternate of %s", params.DiscordID(), strings.Join(alts, ", "))
			_, _ = s.(*sessionWS).evrPipeline.appBot.LogAuditMessage(ctx, groupID, content, false)

			if err := loginHistory.Store(ctx, nk); err != nil {
				return fmt.Errorf("failed to store login history: %w", err)
			}

		}
	}
	return nil
}

func (h *EventDispatch) handleUserLogin(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, evt *api.Event) error {
	userID := evt.Properties["user_id"]

	loginHistory, err := LoginHistoryLoad(ctx, nk, userID)
	if err != nil {
		logger.Error("error loading login history: %v", err)
	}
	loginHistory.UpdateAlternates(ctx, nk)

	if err := loginHistory.Store(ctx, nk); err != nil {
		logger.Error("failed to store login history: %v", err)
	}

	return nil
}
