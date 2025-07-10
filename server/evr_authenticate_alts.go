package server

import (
	"context"
	"fmt"
	"slices"

	"github.com/gofrs/uuid/v5"
	"github.com/heroiclabs/nakama-common/api"
	"github.com/heroiclabs/nakama-common/runtime"
)

type AlternateSearchMatch struct {
	OtherUserID string   `json:"other_user_id"`
	Items       []string `json:"items"`
}

func LoginAlternateSearch(ctx context.Context, nk runtime.NakamaModule, loginHistory *LoginHistory, skipSelf bool) ([]*AlternateSearchMatch, map[string]*LoginHistory, error) {

	// Build a list of patterns to search for in the index.

	//items := loginHistory.SearchPatterns()
	// Compile all of the users identifiers
	items := make([]string, 0, len(loginHistory.History)*3)
	for _, e := range loginHistory.History {
		for _, s := range [...]string{
			e.ClientIP,
			e.LoginData.HMDSerialNumber,
		} {
			items = append(items, s)
		}
	}
	for xpi := range loginHistory.XPIs {
		items = append(items, xpi)
	}

	slices.Sort(items)
	items = slices.Compact(items)

	// Filter out any items that are ignored by the pattern.
	for i := 0; i < len(items); i++ {
		if matchIgnoredAltPattern(items[i]) {
			items = slices.Delete(items, i, i+1)
			i-- // Adjust index since we removed an item.
		}
	}

	if len(items) == 0 {
		return nil, nil, nil
	}

	return LoginAlternatePatternSearch(ctx, nk, loginHistory, items, skipSelf)
}

func LoginAlternatePatternSearch(ctx context.Context, nk runtime.NakamaModule, loginHistory *LoginHistory, items []string, skipSelf bool) ([]*AlternateSearchMatch, map[string]*LoginHistory, error) {

	query := fmt.Sprintf("+value.cache:%s", Query.CreateMatchPattern(items))
	otherHistories := make(map[string]*LoginHistory)
	matches := make([]*AlternateSearchMatch, 0)
	var err error
	var result *api.StorageObjects
	var cursor string

	seen := make(map[string]struct{}, 0)

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

			if _, found := seen[obj.UserId]; found {
				continue
			}
			seen[obj.UserId] = struct{}{}

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

	query := fmt.Sprintf("+value.denied_client_addrs:/%s/", Query.QuoteStringValue(clientIPAddress))
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

	aEntries := make([][]string, 0, len(a.History)*3)
	bEntries := make([][]string, 0, len(b.History)*3)

	for _, aEntry := range a.History {
		itemsA := []string{
			aEntry.XPID.String(),
			aEntry.ClientIP,
			aEntry.SystemProfile(),
		}
		if aEntry.LoginData.HMDSerialNumber != "" {
			itemsA = append(itemsA, aEntry.LoginData.HMDSerialNumber)
		} else {
			itemsA = append(itemsA, uuid.Must(uuid.NewV4()).String())
		}
		aEntries = append(aEntries, itemsA)
	}

	for _, bEntry := range b.History {
		itemsB := []string{
			bEntry.XPID.String(),
			bEntry.ClientIP,
			bEntry.SystemProfile(),
		}
		if bEntry.LoginData.HMDSerialNumber != "" {
			itemsB = append(itemsB, bEntry.LoginData.HMDSerialNumber)
		} else {
			itemsB = append(itemsB, uuid.Must(uuid.NewV4()).String())
		}
		bEntries = append(bEntries, itemsB)
	}

	for _, aEntry := range aEntries {
		for _, bEntry := range bEntries {

			matchingItems := make([]string, 0, len(aEntry))
			for i := range aEntry {
				if aEntry[i] == bEntry[i] {
					matchingItems = append(matchingItems, aEntry[i])
				}
			}

			if len(matchingItems) > 0 {
				slices.Sort(matchingItems)
				matches = append(matches, &AlternateSearchMatch{
					OtherUserID: b.userID,
					Items:       matchingItems,
				})
			}
		}
	}
	return matches
}
