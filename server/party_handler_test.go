// Copyright 2022 The Nakama Authors
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
	"testing"

	"github.com/gofrs/uuid/v5"
	"go.uber.org/zap"
)

// should add and remove from PartyMatchmaker
func TestPartyMatchmakerAddAndRemove(t *testing.T) {
	consoleLogger := loggerForTest(t)
	partyHandler, cleanup := createTestPartyHandler(t, consoleLogger)
	defer cleanup()

	sessionID, _ := uuid.NewV4()
	userID, _ := uuid.NewV4()
	node := "node1"

	partyHandler.Join([]*Presence{&Presence{
		ID: PresenceID{
			Node:      node,
			SessionID: sessionID,
		},
		// Presence stream not needed.
		UserID: userID,
		Meta: PresenceMeta{
			Username: "username",
			// Other meta fields not needed.
		},
	}})

	ticket, _, err := partyHandler.MatchmakerAdd(sessionID.String(), node, "", 1, 1, 1, nil, nil)
	if err != nil {
		t.Fatalf("MatchmakerAdd error %s", err)
	}

	err = partyHandler.MatchmakerRemove(sessionID.String(), node, ticket)
	if err != nil {
		t.Fatalf("MatchmakerRemove error %s", err)
	}
}

func createTestPartyHandler(t *testing.T, logger *zap.Logger) (*PartyHandler, func() error) {
	node := "node1"

	mm, cleanup, _ := createTestMatchmaker(t, logger, true, nil)
	tt := testTracker{}
	tsm := testStreamManager{}

	dmr := DummyMessageRouter{}

	pr := NewLocalPartyRegistry(logger, cfg, mm, &tt, &tsm, &dmr, node)
	ph := NewPartyHandler(logger, pr, mm, &tt, &tsm, &dmr, uuid.UUID{}, node, true, 10, nil)
	return ph, cleanup
}

// TestPartyLeaderFirst tests that the leader is always first in the party member list
func TestPartyLeaderFirst(t *testing.T) {
	consoleLogger := loggerForTest(t)
	partyHandler, cleanup := createTestPartyHandler(t, consoleLogger)
	defer cleanup()

	// Create test presences
	leaderSessionID, _ := uuid.NewV4()
	leaderUserID, _ := uuid.NewV4()
	member1SessionID, _ := uuid.NewV4()
	member1UserID, _ := uuid.NewV4()
	member2SessionID, _ := uuid.NewV4()
	member2UserID, _ := uuid.NewV4()
	node := "node1"

	leaderPresence := &Presence{
		ID: PresenceID{
			Node:      node,
			SessionID: leaderSessionID,
		},
		UserID: leaderUserID,
		Meta: PresenceMeta{
			Username: "leader",
		},
	}

	member1Presence := &Presence{
		ID: PresenceID{
			Node:      node,
			SessionID: member1SessionID,
		},
		UserID: member1UserID,
		Meta: PresenceMeta{
			Username: "member1",
		},
	}

	member2Presence := &Presence{
		ID: PresenceID{
			Node:      node,
			SessionID: member2SessionID,
		},
		UserID: member2UserID,
		Meta: PresenceMeta{
			Username: "member2",
		},
	}

	// Join leader first
	partyHandler.Join([]*Presence{leaderPresence})

	// Verify leader is first when only leader exists
	members := partyHandler.ListSorted()
	if len(members) != 1 {
		t.Fatalf("Expected 1 member, got %d", len(members))
	}
	if members[0].PresenceID.SessionID != leaderSessionID {
		t.Fatalf("Expected leader to be first, got %s", members[0].UserPresence.Username)
	}

	// Join other members
	partyHandler.Join([]*Presence{member1Presence, member2Presence})

	// Verify leader is still first
	members = partyHandler.ListSorted()
	if len(members) != 3 {
		t.Fatalf("Expected 3 members, got %d", len(members))
	}
	if members[0].PresenceID.SessionID != leaderSessionID {
		t.Fatalf("Expected leader to be first, got %s", members[0].UserPresence.Username)
	}

	// Promote member1 to leader (using current leader's session)
	member1UserPresence := &rtapi.UserPresence{
		UserId:    member1Presence.GetUserId(),
		SessionId: member1Presence.GetSessionId(),
		Username:  member1Presence.GetUsername(),
	}
	err := partyHandler.Promote(leaderSessionID.String(), node, member1UserPresence)
	if err != nil {
		t.Fatalf("Failed to promote member1: %v", err)
	}

	// Verify new leader (member1) is now first
	members = partyHandler.ListSorted()
	if len(members) != 3 {
		t.Fatalf("Expected 3 members, got %d", len(members))
	}
	if members[0].PresenceID.SessionID != member1SessionID {
		t.Fatalf("Expected new leader (member1) to be first, got %s", members[0].UserPresence.Username)
	}

	// Compare with unsorted list to ensure they're different when leader is not naturally first
	unsortedMembers := partyHandler.members.List()
	// Find where the leader is in the unsorted list
	var leaderIndexUnsorted int = -1
	for i, member := range unsortedMembers {
		if member.PresenceID.SessionID == member1SessionID {
			leaderIndexUnsorted = i
			break
		}
	}
	
	// If leader is not first in unsorted list, verify sorting worked
	if leaderIndexUnsorted > 0 {
		if members[0].PresenceID.SessionID != member1SessionID {
			t.Fatalf("Leader should be first in sorted list but wasn't")
		}
		if unsortedMembers[0].PresenceID.SessionID == member1SessionID {
			t.Fatalf("Leader shouldn't be first in unsorted list for this test case")
		}
	}
}
