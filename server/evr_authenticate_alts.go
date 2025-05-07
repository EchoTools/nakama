package server

import (
	"context"
	"fmt"

	"github.com/heroiclabs/nakama-common/api"
	"github.com/heroiclabs/nakama-common/runtime"
)

type AlternateSearchMatch struct {
	otherHistory *LoginHistory
	sourceEntry  *LoginHistoryEntry
	MatchEntry   *LoginHistoryEntry `json:"entry"`
	Items        []string           `json:"items"`
}

func LoginAlternateSearch(ctx context.Context, nk runtime.NakamaModule, loginHistory *LoginHistory) ([]*AlternateSearchMatch, error) {

	// Build a list of patterns to search for in the index.
	seen := make(map[string]struct{}, 0)
	items := make([]string, 0, len(loginHistory.History)*3)

	for _, e := range loginHistory.History {

		for _, s := range [...]string{
			e.XPID.Token(),
			e.ClientIP,
			e.LoginData.HMDSerialNumber,
		} {
			if _, found := seen[s]; !found {
				if _, found := IgnoredLoginValues[s]; !found {
					items = append(items, s)
				}
				seen[s] = struct{}{}
			}
		}
	}

	query := fmt.Sprintf("+value.cache:%s", Query.MatchItem(items))

	matches := make([]*AlternateSearchMatch, 0)

	var cursor string
	var err error
	var result *api.StorageObjects

	for {
		result, cursor, err = nk.StorageIndexList(ctx, SystemUserID, LoginHistoryCacheIndex, query, 100, nil, cursor)
		if err != nil {
			return nil, fmt.Errorf("error listing alt index: %w", err)
		}

		for _, obj := range result.Objects {

			// Skip the current user.
			if obj.UserId == loginHistory.userID {
				continue
			}

			// Unmarshal the alternate history.
			otherHistory := NewLoginHistory(obj.UserId)
			if err := StorageRead(ctx, nk, obj.UserId, otherHistory, false); err != nil {
				return nil, fmt.Errorf("error reading alt history: %w", err)
			}
			// Compare the entries.
			matches = append(matches, loginHistoryCompare(loginHistory, otherHistory)...)
		}

		if cursor == "" {
			break
		}
	}

	return matches, nil
}

func LoginDeniedClientIPAddressSearch(ctx context.Context, nk runtime.NakamaModule, clientIPAddress string) ([]string, error) {

	query := fmt.Sprintf("+value.denied_client_addrs:/%s/", Query.Escape(clientIPAddress))
	// Perform the storage list operation

	cursor := ""
	userIDs := make([]string, 0)
	for {
		result, cursor, err := nk.StorageIndexList(ctx, SystemUserID, LoginHistoryCacheIndex, query, 10, nil, cursor)
		if err != nil {
			return nil, fmt.Errorf("error listing display name history: %w", err)
		}

		for _, obj := range result.Objects {
			userIDs = append(userIDs, obj.UserId)
		}

		if cursor == "" {
			break
		}
	}
	return userIDs, nil

}

func loginHistoryCompare(a, b *LoginHistory) []*AlternateSearchMatch {
	matches := make([]*AlternateSearchMatch, 0)
	for _, aEntry := range a.History {
		for _, bEntry := range b.History {

			items := make([]string, 0)

			for k, v := range map[string]string{
				aEntry.XPID.String():             bEntry.XPID.String(),
				aEntry.LoginData.HMDSerialNumber: bEntry.LoginData.HMDSerialNumber,
				aEntry.ClientIP:                  bEntry.ClientIP,
				aEntry.SystemProfile():           bEntry.SystemProfile(),
			} {
				if k == v {
					if _, found := IgnoredLoginValues[k]; found {
						continue
					}
					items = append(items, k)
				}
			}

			if len(items) > 0 {
				matches = append(matches, &AlternateSearchMatch{
					sourceEntry:  aEntry,
					MatchEntry:   bEntry,
					Items:        items,
					otherHistory: b,
				})
			}
		}
	}
	return matches
}
