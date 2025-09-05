package service

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/heroiclabs/nakama-common/api"
	"github.com/heroiclabs/nakama-common/runtime"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

var _ = SystemMigrator(&MigrationRebuildLoginHistory{})

type MigrationRebuildLoginHistory struct{}

func (m *MigrationRebuildLoginHistory) MigrateSystem(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule) error {

	// Get A list of all users, by listing all groups

	var (
		err                     error
		groupCursor, userCursor string
		groups                  []*api.Group
		users                   []*api.GroupUserList_GroupUser
		seenUserIDSet           = make(map[string]bool)
	)

	var numAccounts, totalPrevNumAlts, totalCurNumAlts, numActive, numErrors int

	for {

		groups, groupCursor, err = nk.GroupsList(ctx, "", "", nil, nil, 100, groupCursor)
		if err != nil {
			logger.WithField("error", err).Warn("Failed to list groups")
			return nil
		}

		for _, group := range groups {
			for {
				users, userCursor, err = nk.GroupUsersList(ctx, group.Id, 100, nil, userCursor)
				if err != nil {
					logger.WithField("error", err).Warn("Failed to list group users")
					continue
				}

				for _, user := range users {
					startTime := time.Now()
					numAccounts++

					if numAccounts%1000 == 0 {
						logger.WithField("count", numAccounts).Info("Processed users")
					}

					if _, ok := seenUserIDSet[user.User.Id]; ok {
						continue
					}
					seenUserIDSet[user.User.Id] = true

					logger := logger.WithField("user_id", user.User.Id)
					loginHistory := NewLoginHistory(user.User.Id)
					// adapter := loginHistory.CreateStorableAdapter()
					if err := StorableReadNk(ctx, nk, user.User.Id, loginHistory, false); err != nil {
						if status.Code(err) != codes.NotFound {
							logger.WithField("error", err).Warn("Failed to read login history")
						}
						continue
					}
					loginHistory.rebuildCache()
					if _, err := loginHistory.UpdateAlternates(ctx, logger, nk); err != nil {
						logger.WithField("error", err).Warn("Failed to update alternates")
						continue
					}
					// Save the new login history
					if err := StorableWriteNk(ctx, nk, user.User.Id, loginHistory); err != nil {
						logger.WithField("error", err).Warn("Failed to write login history")
						continue
					}
					// wait the duration that it took to process the user

					<-time.After(time.Since(startTime))
				}
				if userCursor == "" {
					break
				}
			}
		}
		if groupCursor == "" {
			break
		}
	}
	logger.WithFields(map[string]any{
		"num_accounts":  numAccounts,
		"prev_num_alts": totalPrevNumAlts,
		"cur_num_alts":  totalCurNumAlts,
		"delta":         totalCurNumAlts - totalPrevNumAlts,
		"num_active":    numActive,
		"num_errors":    numErrors,
	}).Info("Completed migration check for alternate patterns")

	return nil
}

func validateAlternatePatterns(ctx context.Context, logger runtime.Logger, nk runtime.NakamaModule, userID string, h *LoginHistory) error {

	for _, item := range h.SearchPatterns() {
		// Search each item individually.
		matches, _, err := LoginAlternatePatternSearch(ctx, nk, h, []string{item}, false)
		if err != nil {
			return fmt.Errorf("error searching for alternate logins: %w", err)
		}
		if matches == nil && err == nil {
			return nil
		}
		userIDs := make([]string, 0, len(matches))

		found := false
		for _, m := range matches {
			if m.OtherUserID == userID {
				found = true
			}
		}

		if !found {
			logger.WithFields(map[string]interface{}{
				"item":    item,
				"matches": userIDs,
			}).Warn("Found pattern that does not match self.")
			continue
		}
	}
	return nil
}
