// Copyright 2023 The Nakama Authors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package server

import (
	"sync"
	"sync/atomic"

	"github.com/gofrs/uuid/v5"
	"github.com/heroiclabs/nakama-common/rtapi"
	"github.com/heroiclabs/nakama-common/runtime"
)

type PartyPresenceList struct {
	sync.RWMutex
	maxSize       int
	presences     []*PartyPresenceListItem
	presenceMap   map[uuid.UUID]string
	presencesRead *atomic.Value
	reservedMap   map[uuid.UUID]struct{}
}

type PartyPresenceListItem struct {
	PresenceID   *PresenceID
	Presence     *Presence
	UserPresence *rtapi.UserPresence
}

func NewPartyPresenceList(maxSize int) *PartyPresenceList {
	m := &PartyPresenceList{
		maxSize:       maxSize,
		presences:     make([]*PartyPresenceListItem, 0, maxSize),
		presenceMap:   make(map[uuid.UUID]string, maxSize),
		presencesRead: &atomic.Value{},
		reservedMap:   make(map[uuid.UUID]struct{}, maxSize),
	}
	m.presencesRead.Store(make([]*PartyPresenceListItem, 0, maxSize))
	return m
}

func (m *PartyPresenceList) Reserve(presence *Presence) error {
	m.Lock()
	if _, found := m.reservedMap[presence.ID.SessionID]; found {
		m.Unlock()
		return nil
	}
	if len(m.presenceMap)+len(m.reservedMap) >= m.maxSize {
		m.Unlock()
		return runtime.ErrPartyFull
	}
	m.reservedMap[presence.ID.SessionID] = struct{}{}
	m.Unlock()
	return nil
}

func (m *PartyPresenceList) Release(presence *Presence) {
	m.Lock()
	delete(m.reservedMap, presence.ID.SessionID)
	m.Unlock()
}

func (m *PartyPresenceList) Join(joins []*Presence) ([]*Presence, error) {
	processed := make([]*Presence, 0, len(joins))
	m.Lock()

	// A join whose USER is already present on a different session is a session
	// replacement, not a new member -- the reconnect case. Counting it as new
	// is what made a party reject its own member: capacity is keyed on session
	// ID, so a reconnecting player's fresh session counted against a limit
	// their stale session was still occupying. The caller
	// (PartyHandler.Join, reached from the tracker's Join event) discards the
	// error as "should not happen", so the player was silently left tracked on
	// the party stream but absent from the roster, and the leader's matchmaking
	// cohort excluded them.
	//
	// Stale sessions for the same user are dropped below, so the party's
	// occupancy is unchanged by a replacement and MaxSize still holds.
	staleSessions := make(map[uuid.UUID]struct{})
	for _, join := range joins {
		for _, existing := range m.presences {
			if existing.Presence.UserID == join.UserID && existing.Presence.ID.SessionID != join.ID.SessionID {
				staleSessions[existing.Presence.ID.SessionID] = struct{}{}
			}
		}
	}

	var newPresences int
	for _, join := range joins {
		_, reservationFound := m.reservedMap[join.ID.SessionID]
		_, presenceFound := m.presenceMap[join.ID.SessionID]
		if reservationFound || presenceFound {
			continue
		}
		newPresences++
	}

	// Compare against the occupancy this call will actually produce, not the
	// current one: the stale sessions above are removed below, so counting them
	// would reject a batch that fits.
	//
	// That is not hypothetical. A user carrying two stale sessions (which a
	// reconnect storm could produce before this fix) whose replacement arrives
	// batched with one genuine newcomer would be refused, even though dropping
	// the two stale entries leaves room for both -- and the caller
	// (PartyHandler.Join) discards ErrPartyFull, so the refusal would be the
	// same silent roster drop this change exists to remove.
	if newPresences > 0 && len(m.reservedMap)+len(m.presenceMap)-len(staleSessions)+newPresences > m.maxSize {
		m.Unlock()
		return nil, runtime.ErrPartyFull
	}

	// Drop the superseded sessions before adding the replacements, so the user
	// never appears twice and occupancy never transiently exceeds MaxSize.
	if len(staleSessions) > 0 {
		kept := m.presences[:0]
		for _, existing := range m.presences {
			if _, stale := staleSessions[existing.Presence.ID.SessionID]; stale {
				delete(m.presenceMap, existing.Presence.ID.SessionID)
				continue
			}
			kept = append(kept, existing)
		}
		for i := len(kept); i < len(m.presences); i++ {
			m.presences[i] = nil
		}
		m.presences = kept
	}

	for _, join := range joins {
		delete(m.reservedMap, join.ID.SessionID)
		if _, ok := m.presenceMap[join.ID.SessionID]; !ok {
			m.presences = append(m.presences, &PartyPresenceListItem{
				PresenceID: &PresenceID{
					Node:      join.ID.Node,
					SessionID: join.ID.SessionID,
				},
				Presence: join,
				UserPresence: &rtapi.UserPresence{
					UserId:    join.GetUserId(),
					SessionId: join.GetSessionId(),
					Username:  join.GetUsername(),
				},
			})
			m.presenceMap[join.ID.SessionID] = join.ID.Node
			processed = append(processed, join)
		}
	}
	if len(processed) > 0 {
		presencesRead := make([]*PartyPresenceListItem, 0, len(m.presences))
		presencesRead = append(presencesRead, m.presences...)
		m.presencesRead.Store(presencesRead)
	}
	m.Unlock()
	return processed, nil
}

func (m *PartyPresenceList) Leave(leaves []*Presence) ([]*Presence, []*Presence) {
	processed := make([]*Presence, 0, len(leaves))
	reservations := make([]*Presence, 0, len(leaves))
	m.Lock()
	for _, leave := range leaves {
		if _, found := m.reservedMap[leave.ID.SessionID]; found {
			delete(m.reservedMap, leave.ID.SessionID)
			reservations = append(reservations, leave)
		}
		if _, ok := m.presenceMap[leave.ID.SessionID]; ok {
			for i, presence := range m.presences {
				if presence.Presence.ID.SessionID == leave.ID.SessionID && presence.Presence.ID.Node == leave.ID.Node {
					copy(m.presences[i:], m.presences[i+1:])
					m.presences[len(m.presences)-1] = nil
					m.presences = m.presences[:len(m.presences)-1]
					break
				}
			}
			delete(m.presenceMap, leave.ID.SessionID)
			processed = append(processed, leave)
		}
	}
	if len(processed) > 0 {
		presencesRead := make([]*PartyPresenceListItem, 0, len(m.presences))
		presencesRead = append(presencesRead, m.presences...)
		m.presencesRead.Store(presencesRead)
	}
	m.Unlock()
	return processed, reservations
}

func (m *PartyPresenceList) List() []*PartyPresenceListItem {
	return m.presencesRead.Load().([]*PartyPresenceListItem)
}

func (m *PartyPresenceList) Size() int {
	m.RLock()
	defer m.RUnlock()
	return len(m.presenceMap) + len(m.reservedMap)
}

func (m *PartyPresenceList) Oldest() (*PresenceID, *rtapi.UserPresence) {
	m.RLock()
	defer m.RUnlock()
	if len(m.presences) == 0 {
		return nil, nil
	}
	return &m.presences[0].Presence.ID, m.presences[0].UserPresence
}
