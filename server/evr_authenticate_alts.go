package server

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"

	"github.com/heroiclabs/nakama-common/api"
	"github.com/heroiclabs/nakama-common/runtime"
)

type AlternateSearchMatch struct {
	EntryUserID string
	OtherUserID string
	Entry       *LoginHistoryEntry
	Other       *LoginHistoryEntry
}

func NewAlternateSearchMatch(entryUserID, otherUserID string, entry, other *LoginHistoryEntry) *AlternateSearchMatch {
	return &AlternateSearchMatch{
		EntryUserID: entryUserID,
		OtherUserID: otherUserID,
		Entry:       entry,
		Other:       other,
	}
}

func (m *AlternateSearchMatch) IsMatch() bool {
	return (m.IsXPIMatch() && m.IsSystemProfileMatch()) || m.IsHMDSerialNumberMatch() || m.IsClientIPMatch()
}

func (m *AlternateSearchMatch) IsXPIMatch() bool {
	return m.Entry.XPID == m.Entry.XPID
}

func (m *AlternateSearchMatch) IsHMDSerialNumberMatch() bool {
	return m.Entry.LoginData.HMDSerialNumber == m.Entry.LoginData.HMDSerialNumber
}

func (m *AlternateSearchMatch) IsClientIPMatch() bool {
	return m.Entry.ClientIP == m.Other.ClientIP
}

func (m *AlternateSearchMatch) IsSystemProfileMatch() bool {
	return m.Entry.SystemProfile() == m.Other.SystemProfile()
}

func (m *AlternateSearchMatch) Matches() (xpi bool, hmdSerialNumber bool, clientIP bool, systemProfile bool) {
	xpi = m.IsXPIMatch()
	hmdSerialNumber = m.IsHMDSerialNumberMatch()
	clientIP = m.IsClientIPMatch()
	systemProfile = m.IsSystemProfileMatch()
	return
}

func LoginAlternateSearch(ctx context.Context, nk runtime.NakamaModule, loginHistory *LoginHistory) ([]*AlternateSearchMatch, error) {

	patterns := make([]string, 0)

	for _, e := range loginHistory.History {
		patterns = append(patterns, e.XPID.Token())

		if _, ok := IgnoredLoginValues[e.LoginData.HMDSerialNumber]; !ok {
			patterns = append(patterns, e.LoginData.HMDSerialNumber)
		}

		patterns = append(patterns, e.ClientIP)

		//patterns = append(patterns, e.SystemProfile())
	}

	slices.Sort(patterns)
	patterns = slices.Compact(patterns)

	matches := make([]*AlternateSearchMatch, 0) // map[userID]AlternateSearchMatch

	var cursor string
	var err error
	var result *api.StorageObjects
	for {

		values := make([]string, 0, len(loginHistory.History)*4)

		for _, pattern := range patterns {
			if _, ok := IgnoredLoginValues[pattern]; ok {
				continue
			}
			values = append(values, pattern)
		}

		if len(values) == 0 {
			break
		}

		query := fmt.Sprintf("value.cache:/(%s)/", Query.Join(values, "|"))
		result, cursor, err = nk.StorageIndexList(ctx, SystemUserID, LoginHistoryCacheIndex, query, 100, nil, cursor)
		if err != nil {
			return nil, fmt.Errorf("error listing alt index: %w", err)
		}

		for _, obj := range result.Objects {
			if obj.UserId == loginHistory.userID {
				continue
			}
			otherHistory := LoginHistory{}
			if err := json.Unmarshal([]byte(obj.Value), &otherHistory); err != nil {
				return nil, fmt.Errorf("error unmarshalling alt history: %w", err)
			}

			for _, e := range loginHistory.History {
				for _, o := range otherHistory.History {
					m := NewAlternateSearchMatch(loginHistory.userID, obj.UserId, e, o)
					if m.IsMatch() {
						matches = append(matches, m)
					}
				}
			}
		}

		if cursor == "" {
			break
		}
	}

	return matches, nil
}
