package server

import (
	"testing"

	"github.com/gofrs/uuid/v5"
)

// TestFollowerAlreadyInLeaderMatch_BothLeavingArena reproduces the
// TOCTOU race from the bigduckii case (2026-06-17): leader and follower
// are both still present in a dying arena match. The check returns true
// ("already in leader's match") because both stream presences point to
// the same arena UUID — but that match is ending and neither player is
// staying there.
//
// The check should return false when the shared match is the one the
// follower is trying to LEAVE (i.e., it matches CurrentMatchID from
// the LobbyFindSessionRequest).
func TestFollowerAlreadyInLeaderMatch_BothLeavingArena(t *testing.T) {
	arenaMatchID := MatchID{UUID: uuid.Must(uuid.NewV4()), Node: "testnode"}

	// Both players are in the arena match that's ending.
	// The follower's LobbyFindSessionRequest has CurrentMatchID = arena.
	result := isFollowerInLeaderDestination(arenaMatchID, arenaMatchID, arenaMatchID)

	if result {
		t.Fatal("returned true when both players are in the match being " +
			"LEFT — follower should NOT be skipped (TOCTOU bug)")
	}
}

// TestFollowerAlreadyInLeaderMatch_FollowerInLeaderSocialLobby is the
// valid case: the follower IS already in the leader's social lobby.
// The check should return true to prevent redundant join attempts.
func TestFollowerAlreadyInLeaderMatch_FollowerInLeaderSocialLobby(t *testing.T) {
	socialMatchID := MatchID{UUID: uuid.Must(uuid.NewV4()), Node: "testnode"}

	// Both in the same social lobby. CurrentMatchID is nil (follower is
	// NOT leaving this match — this is the destination).
	result := isFollowerInLeaderDestination(socialMatchID, socialMatchID, MatchID{})

	if !result {
		t.Fatal("returned false when follower IS in leader's social " +
			"lobby — should return true to skip redundant join")
	}
}

// TestFollowerAlreadyInLeaderMatch_DifferentMatches is the normal case:
// follower and leader are in different matches. Should return false.
func TestFollowerAlreadyInLeaderMatch_DifferentMatches(t *testing.T) {
	leaderMatch := MatchID{UUID: uuid.Must(uuid.NewV4()), Node: "testnode"}
	followerMatch := MatchID{UUID: uuid.Must(uuid.NewV4()), Node: "testnode"}

	result := isFollowerInLeaderDestination(leaderMatch, followerMatch, followerMatch)

	if result {
		t.Fatal("returned true when follower and leader are in different matches")
	}
}

// TestFollowerAlreadyInLeaderMatch_LeaderMatchNil means the leader has
// no match presence. Should return false.
func TestFollowerAlreadyInLeaderMatch_LeaderMatchNil(t *testing.T) {
	followerMatch := MatchID{UUID: uuid.Must(uuid.NewV4()), Node: "testnode"}

	result := isFollowerInLeaderDestination(MatchID{}, followerMatch, followerMatch)

	if result {
		t.Fatal("returned true when leader has no match presence")
	}
}

// isFollowerInLeaderDestination is the pure logic extracted from
// isFollowerAlreadyInLeaderMatch. It answers: is the follower already
// at the leader's DESTINATION (not their origin)?
//
// Returns true only when both are in the same match AND that match is
// NOT the one the follower is leaving.
func isFollowerInLeaderDestination(leaderMatchID, followerMatchID, currentMatchID MatchID) bool {
	if leaderMatchID.IsNil() {
		return false
	}

	if followerMatchID != leaderMatchID {
		return false
	}

	// Both in the same match — but is that the match being left?
	if !currentMatchID.IsNil() && followerMatchID == currentMatchID {
		return false
	}

	return true
}
