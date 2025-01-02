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

func afterUpdateAccount(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, in *api.UpdateAccountRequest) error {
	evt := &api.Event{
		Name:       "account_updated",
		Properties: map[string]string{},
		External:   false,
	}
	return nk.Event(context.Background(), evt)
}

func eventSessionEnd(ctx context.Context, logger runtime.Logger, evt *api.Event) {
	logger.Debug("process event session end: %+v", evt)
}

func eventSessionStart(ctx context.Context, logger runtime.Logger, evt *api.Event) {
	logger.Debug("process event session start: %+v", evt)
}

func eventLobbySessionAuthorized(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, cache *sync.Map, evt *api.Event) error {
	groupID := evt.Properties["group_id"]
	userID := evt.Properties["user_id"]
	sessionID := evt.Properties["session_id"]

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

		loginHistory := params.loginHistory

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
			content := fmt.Sprintf("<@%s> detected as a possible alternate of %s", params.discordID, strings.Join(alts, ", "))
			_, _ = s.(*sessionWS).evrPipeline.appBot.LogAuditMessage(ctx, groupID, content, false)

			if err := loginHistory.Store(ctx, nk); err != nil {
				return fmt.Errorf("failed to store login history: %w", err)
			}

		}
	}
	logger.Debug("process event lobby session authorized: %+v", evt)
	return nil
}
