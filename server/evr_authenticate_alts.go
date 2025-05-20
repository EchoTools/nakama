package server

import (
	"context"
	"fmt"
	"slices"

	"github.com/heroiclabs/nakama-common/api"
	"github.com/heroiclabs/nakama-common/runtime"
)

type AlternateSearchMatch struct {
	OtherUserID string             `json:"other_user_id"`
	OtherEntry  *LoginHistoryEntry `json:"other_entry"`
	Items       []string           `json:"items"`
}

func LoginAlternateSearch(ctx context.Context, nk runtime.NakamaModule, loginHistory *LoginHistory, skipSelf bool) ([]*AlternateSearchMatch, map[string]*LoginHistory, error) {

	// Build a list of patterns to search for in the index.
	seen := make(map[string]struct{}, 0)
	items := make([]string, 0, len(loginHistory.History)*3)
	//items := loginHistory.SearchPatterns()
	// Compile all of the users identifiers

	for _, e := range loginHistory.History {
		for _, s := range [...]string{
			e.XPID.Token(),
			e.ClientIP,
			e.LoginData.HMDSerialNumber,
		} {
			if _, found := seen[s]; !found {
				// Add the item to the list of items to search for.
				items = append(items, s)
				seen[s] = struct{}{}
			}
		}
	}
	items = filterAltSearchPatterns(items)
	// Sort the items to ensure that the search is consistent.
	slices.Sort(items)

	if len(items) == 0 {
		return nil, nil, nil
	}

	return LoginAlternatePatternSearch(ctx, nk, loginHistory, items, skipSelf)
}

func LoginAlternatePatternSearch(ctx context.Context, nk runtime.NakamaModule, loginHistory *LoginHistory, items []string, skipSelf bool) ([]*AlternateSearchMatch, map[string]*LoginHistory, error) {

	query := fmt.Sprintf("+value.cache:%s", Query.MatchItem(items))
	otherHistories := make(map[string]*LoginHistory)
	matches := make([]*AlternateSearchMatch, 0)
	var err error
	var result *api.StorageObjects
	var cursor string
	for {
		result, cursor, err = nk.StorageIndexList(ctx, SystemUserID, LoginHistoryCacheIndex, query, 100, nil, cursor)
		if err != nil {
			return nil, nil, fmt.Errorf("error listing alt index: %w", err)
		}

		for _, obj := range result.Objects {

			// Skip the current user.
			if skipSelf && obj.UserId == loginHistory.userID {
				continue
			}

			// Unmarshal the alternate history.
			otherHistory := NewLoginHistory(obj.UserId)
			if err := StorageRead(ctx, nk, obj.UserId, otherHistory, false); err != nil {
				return nil, nil, fmt.Errorf("error reading alt history: %w", err)
			}
			// Compare the entries.
			matches = append(matches, loginHistoryCompare(loginHistory, otherHistory)...)
		}

		if cursor == "" {
			break
		}
	}

	return matches, otherHistories, nil
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
					items = append(items, k)
				}
			}
			items = filterAltSearchPatterns(items)
			if len(items) > 0 {
				slices.Sort(items)
				matches = append(matches, &AlternateSearchMatch{
					OtherUserID: b.userID,
					OtherEntry:  bEntry,
					Items:       items,
				})
			}
		}
	}
	return matches
}
