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
	otherHistory *LoginHistory
	sourceEntry  *LoginHistoryEntry
	MatchEntry   *LoginHistoryEntry `json:"entry"`
	Items        []string           `json:"items"`
}

func NewAlternateSearchMatch(source, match *LoginHistoryEntry, history *LoginHistory) *AlternateSearchMatch {
	items := make([]string, 0)
	for _, item := range source.Items() {
		for _, otherItem := range match.Items() {
			if item == otherItem {
				items = append(items, item)
			}
		}
	}

	return &AlternateSearchMatch{
		sourceEntry:  source,
		MatchEntry:   match,
		Items:        items,
		otherHistory: history,
	}
}

func (m *AlternateSearchMatch) IsMatch() bool {
	return (m.IsXPIMatch() && m.IsSystemProfileMatch()) || m.IsHMDSerialNumberMatch() || m.IsClientIPMatch()
}

func (m *AlternateSearchMatch) IsXPIMatch() bool {
	return m.sourceEntry.XPID == m.MatchEntry.XPID
}

func (m *AlternateSearchMatch) IsHMDSerialNumberMatch() bool {
	return m.sourceEntry.LoginData.HMDSerialNumber == m.MatchEntry.LoginData.HMDSerialNumber
}

func (m *AlternateSearchMatch) IsClientIPMatch() bool {
	return m.sourceEntry.ClientIP == m.MatchEntry.ClientIP
}

func (m *AlternateSearchMatch) IsSystemProfileMatch() bool {
	return m.sourceEntry.SystemProfile() == m.MatchEntry.SystemProfile()
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
			otherHistory.userID = obj.UserId

			for _, e := range loginHistory.History {
				for _, o := range otherHistory.History {
					m := NewAlternateSearchMatch(e, o, &otherHistory)
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
