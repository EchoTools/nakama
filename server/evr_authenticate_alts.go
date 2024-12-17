package server

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"slices"
	"strings"

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
	return m.IsXPIMatch() || m.IsHMDSerialNumberMatch() || m.IsClientIPMatch()
}

func (m *AlternateSearchMatch) IsXPIMatch() bool {
	return m.Entry.DeviceAuth.EvrID == m.Other.DeviceAuth.EvrID
}

func (m *AlternateSearchMatch) IsHMDSerialNumberMatch() bool {
	return m.Entry.DeviceAuth.HMDSerialNumber == m.Other.DeviceAuth.HMDSerialNumber
}

func (m *AlternateSearchMatch) IsClientIPMatch() bool {
	return m.Entry.DeviceAuth.ClientIP == m.Other.DeviceAuth.ClientIP
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

func LoginAlternateSearch(ctx context.Context, nk runtime.NakamaModule, userID string, loginHistory *LoginHistory) ([]*AlternateSearchMatch, error) {

	ignoredHMDSerialNumbers := []string{"N/A", "unknown", "", "1PASH5D1P17365"}

	patterns := make([]string, 0)

	for _, e := range loginHistory.History {
		patterns = append(patterns, e.DeviceAuth.EvrID.String())

		if !slices.Contains(ignoredHMDSerialNumbers, e.DeviceAuth.HMDSerialNumber) {
			patterns = append(patterns, e.DeviceAuth.HMDSerialNumber)
		}

		patterns = append(patterns, e.DeviceAuth.ClientIP)

		//patterns = append(patterns, e.SystemProfile())
	}

	slices.Sort(patterns)
	patterns = slices.Compact(patterns)

	matches := make([]*AlternateSearchMatch, 0) // map[userID]AlternateSearchMatch

	var cursor string
	var err error
	var result *api.StorageObjects
	for {
		qparts := make([]string, len(patterns))
		for _, pattern := range patterns {
			if pattern == "" {
				continue
			}
			qparts = append(qparts, fmt.Sprintf("value.cache:%s", Query.Escape(pattern)))
		}
		query := strings.Trim(strings.Join(qparts, " "), " ")
		log.Printf("query: %s", query)
		result, cursor, err = nk.StorageIndexList(ctx, SystemUserID, LoginHistoryCacheIndex, strings.Join(qparts[:1], " "), 100, nil, cursor)
		if err != nil {
			return nil, fmt.Errorf("error listing display name history: %w", err)
		}

		for _, obj := range result.Objects {
			if obj.UserId == userID {
				continue
			}
			otherHistory := LoginHistory{}
			if err := json.Unmarshal([]byte(obj.Value), &otherHistory); err != nil {
				return nil, fmt.Errorf("error unmarshalling display name history: %w", err)
			}

			for _, e := range loginHistory.History {
				for _, o := range otherHistory.History {
					m := NewAlternateSearchMatch(userID, obj.UserId, e, o)
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
