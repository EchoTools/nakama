package server

import (
	"context"
	"fmt"
	"math/rand"
	"slices"
	"strings"
	"time"

	"github.com/heroiclabs/nakama-common/api"
	"github.com/heroiclabs/nakama-common/runtime"
	"github.com/heroiclabs/nakama/v3/server/evr"
)

var _ = Event(&EventUserAuthenticated{})

type EventUserAuthenticated struct {
	UserID                   string            `json:"user_id"`
	XPID                     evr.EvrId         `json:"xpid"`
	ClientIP                 string            `json:"client_ip"`
	LoginPayload             *evr.LoginProfile `json:"login_data"`
	IsWebSocketAuthenticated bool              `json:"is_websocket_authenticated"`
}

func NewUserAuthenticatedEvent(userID string, xpid evr.EvrId, clientIP string, loginPayload *evr.LoginProfile, isWebSocketAuthenticated bool) *EventUserAuthenticated {
	return &EventUserAuthenticated{
		UserID:                   userID,
		XPID:                     xpid,
		ClientIP:                 clientIP,
		LoginPayload:             loginPayload,
		IsWebSocketAuthenticated: isWebSocketAuthenticated,
	}
}

func (e *EventUserAuthenticated) Process(ctx context.Context, logger runtime.Logger, dispatcher *EventDispatcher) error {

	if e.UserID == "" {
		return fmt.Errorf("user ID is empty")
	}
	var (
		err    error
		nk     = dispatcher.nk
		dg     = dispatcher.dg
		userID = e.UserID
	)

	loginHistory := NewLoginHistory(userID)
	if err := StorableRead(ctx, nk, userID, loginHistory, true); err != nil {
		return fmt.Errorf("failed to load login history: %w", err)
	}

	// Update the last used time for their ip
	isNew, allowed := loginHistory.Update(e.XPID, e.ClientIP, e.LoginPayload, e.IsWebSocketAuthenticated)

	if allowed && isNew {
		// Get the account to retrieve the Discord ID
		account, err := nk.AccountGetId(ctx, userID)
		if err != nil {
			logger.WithField("error", err).Warn("Failed to get account for IP authorization notification")
		} else if account.CustomId != "" {
			if err := SendIPAuthorizationNotification(dg, account.CustomId, e.ClientIP); err != nil {
				// Log the error, but don't return it.
				logger.WithField("error", err).Warn("Failed to send IP authorization notification")
			}
		}
	}

	hasDiabledAlts, err := loginHistory.UpdateAlternates(ctx, logger, nk)
	if err != nil {
		return fmt.Errorf("failed to update alternates: %w", err)
	}

	if err := StorableWrite(ctx, nk, userID, loginHistory); err != nil {
		return fmt.Errorf("failed to store login history: %w", err)
	}

	if hasDiabledAlts && ServiceSettings().KickPlayersWithDisabledAlternates {
		go func() {

			// Set random time to disable and kick player
			var (
				firstIDs, _        = loginHistory.AlternateIDs()
				altNames           = make([]string, 0, len(loginHistory.AlternateMatches))
				accountMap         = make(map[string]*api.Account, len(loginHistory.AlternateMatches))
				delayMin, delayMax = 1, 4
				kickDelay          = time.Duration(delayMin+rand.Intn(delayMax)) * time.Minute
			)

			if accounts, err := nk.AccountsGetId(ctx, append(firstIDs, userID)); err != nil {
				logger.Error("failed to get alternate accounts: %v", err)
				return
			} else {
				for _, a := range accounts {
					accountMap[a.User.Id] = a
					altNames = append(altNames, fmt.Sprintf("<@%s> (%s)", a.CustomId, a.User.Username))
				}
			}

			if len(altNames) == 0 || accountMap[userID] == nil {
				logger.Error("failed to get alternate accounts: %v", err)
				return
			}

			slices.Sort(altNames)
			altNames = slices.Compact(altNames)

			// Send audit log message
			content := fmt.Sprintf("<@%s> (%s) has disabled alternates, disconnecting session(s) in %d seconds.\n%s", accountMap[userID].CustomId, accountMap[userID].User.Username, int(kickDelay.Seconds()), strings.Join(altNames, ", "))
			AuditLogSend(dg, ServiceSettings().ServiceAuditChannelID, content)

			logger.WithField("delay", kickDelay).Info("kicking (with delay) user %s has disabled alternates", userID)
			<-time.After(kickDelay)
			if c, err := DisconnectUserID(ctx, nk, userID, true, true, false); err != nil {
				logger.Error("failed to disconnect user: %v", err)
			} else {
				logger.Info("user %s disconnected: %v sessions", userID, c)
			}
		}()
	}

	return nil
}
