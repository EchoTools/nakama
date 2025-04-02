package server

import (
	"context"
	"encoding/json"
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
	patterns := make([]string, 0, len(loginHistory.History)*3)

	for _, e := range loginHistory.History {

		for _, s := range [...]string{
			e.XPID.Token(),
			e.ClientIP,
			e.LoginData.HMDSerialNumber,
		} {
			if _, found := seen[s]; !found {
				if _, found := IgnoredLoginValues[s]; !found {
					patterns = append(patterns, s)
				}
				seen[s] = struct{}{}
			}
		}
	}

	query := fmt.Sprintf("+value.cache:/(%s)/", Query.Join(patterns, "|"))

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
			otherHistory := LoginHistory{}
			if err := json.Unmarshal([]byte(obj.Value), &otherHistory); err != nil {
				return nil, fmt.Errorf("error unmarshalling alt history: %w", err)
			}
			// Set the user ID based on the object owner.
			otherHistory.userID = obj.UserId

			// Compare the entries.
			matches = append(matches, loginHistoryCompare(loginHistory, &otherHistory)...)
		}

		if cursor == "" {
			break
		}
	}

	return matches, nil
}

func LoginDeniedClientIPAddressSearch(ctx context.Context, nk runtime.NakamaModule, clientIPAddress string) (map[string]*LoginHistory, error) {

	query := fmt.Sprintf("+value.denied_client_addrs:/%s/", Query.Escape(clientIPAddress))
	// Perform the storage list operation

	cursor := ""
	histories := make(map[string]*LoginHistory)
	for {
		result, cursor, err := nk.StorageIndexList(ctx, SystemUserID, LoginHistoryCacheIndex, query, 10, nil, cursor)
		if err != nil {
			return nil, fmt.Errorf("error listing display name history: %w", err)
		}

		for _, obj := range result.Objects {
			var history LoginHistory
			if err := json.Unmarshal([]byte(obj.Value), &history); err != nil {
				return nil, fmt.Errorf("error unmarshalling display name history: %w", err)
			}
			history.userID = obj.UserId
			history.version = obj.Version
			histories[obj.UserId] = &history
		}

		if cursor == "" {
			break
		}
	}
	return histories, nil

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
