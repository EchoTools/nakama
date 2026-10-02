package server

import (
	"context"
	"testing"

	"github.com/gofrs/uuid/v5"
	"github.com/stretchr/testify/require"
)

func TestSNSPartyPolicyAdmits(t *testing.T) {
	cases := []struct {
		name                                    string
		policy                                  uint8
		invited, friendOfLeader, friendOfMember bool
		want                                    bool
	}{
		{"invite only, invited", snsPartyPolicyInviteOnly, true, false, false, true},
		{"invite only, a friend of the leader", snsPartyPolicyInviteOnly, false, true, true, false},
		{"friends, friend of the leader", snsPartyPolicyFriends, false, true, false, true},
		{"friends, friend of a member only", snsPartyPolicyFriends, false, false, true, false},
		{"friends, invited stranger", snsPartyPolicyFriends, true, false, false, true},
		{"friends of members, friend of a member", snsPartyPolicyFriendsOfMembers, false, false, true, true},
		{"friends of members, friend of the leader", snsPartyPolicyFriendsOfMembers, false, true, false, true},
		{"friends of members, stranger", snsPartyPolicyFriendsOfMembers, false, false, false, false},
		{"everyone, stranger", snsPartyPolicyEveryone, false, false, false, true},
	}
	for _, c := range cases {
		if got := snsPartyPolicyAdmits(c.policy, c.invited, c.friendOfLeader, c.friendOfMember); got != c.want {
			t.Errorf("%s: admits = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestSNSPartyPolicyDefaultsToEveryone(t *testing.T) {
	p := &EvrPipeline{snsPartyPolicies: &MapOf[uuid.UUID, uint8]{}}
	if got := p.snsPartyPolicy(uuid.Must(uuid.NewV4())); got != snsPartyPolicyEveryone {
		t.Errorf("a party with no policy = %d, want everyone", got)
	}
}

// partyEndEnv is a pipeline whose tracker counts and untracks the party stream, so snsPartyLeaveCleanup
// can see the last member leave.
func partyEndEnv(t *testing.T) (*EvrPipeline, *countingTracker) {
	tracker := &countingTracker{listingTracker: &listingTracker{mockMatchmakingTracker: newMockMatchmakingTracker()}}
	return &EvrPipeline{
		node:             "testnode",
		nk:               &RuntimeGoNakamaModule{tracker: tracker, node: "testnode"},
		snsPartyData:     &MapOf[uuid.UUID, *snsPartyDataState]{},
		snsPartyPolicies: &MapOf[uuid.UUID, uint8]{},
	}, tracker
}

// countingTracker adds Untrack and CountByStream to the listing tracker.
type countingTracker struct {
	*listingTracker
}

func (t *countingTracker) Untrack(sessionID uuid.UUID, stream PresenceStream, userID uuid.UUID) {
	t.mu.Lock()
	defer t.mu.Unlock()
	delete(t.presences, presenceKey{sessionID: sessionID, stream: stream, userID: userID})
}

func (t *countingTracker) CountByStream(stream PresenceStream) int {
	return len(t.ListByStream(stream, true, true))
}

// A party's join policy (and its party data) is kept per party UUID for the party's life. When the last
// member leaves, both are dropped; while anyone is still in the party, both stay.
func TestPartyPolicyAndDataAreDroppedWhenTheLastMemberLeaves(t *testing.T) {
	p, tracker := partyEndEnv(t)
	partyUUID := uuid.Must(uuid.NewV4())
	stream := PresenceStream{Mode: StreamModeParty, Subject: partyUUID, Label: "testnode"}
	leader := newPartyMemberSession(t, "leader", tracker, nil, p)
	member := newPartyMemberSession(t, "member", tracker, nil, p)
	for _, s := range []*sessionWS{leader, member} {
		tracker.Track(context.Background(), s.id, stream, s.userID, PresenceMeta{})
		params, _ := LoadParams(s.Context())
		params.currentPartyID = partyUUID
		params.currentSNSPartyID = 7
	}
	p.snsPartyPolicies.Store(partyUUID, snsPartyPolicyFriends)
	p.snsPartyData.Store(partyUUID, newSNSPartyDataState())

	params, _ := LoadParams(leader.Context())
	p.snsPartyLeaveCleanup(context.Background(), loggerForTest(t), leader, params)
	_, kept := p.snsPartyPolicies.Load(partyUUID)
	require.True(t, kept, "the policy was dropped while a member is still in the party")
	require.Equal(t, snsPartyPolicyFriends, p.snsPartyPolicy(partyUUID))
	_, kept = p.snsPartyData.Load(partyUUID)
	require.True(t, kept, "the party data was dropped while a member is still in the party")

	params, _ = LoadParams(member.Context())
	p.snsPartyLeaveCleanup(context.Background(), loggerForTest(t), member, params)
	_, kept = p.snsPartyPolicies.Load(partyUUID)
	require.False(t, kept, "the last member left and the policy stayed")
	_, kept = p.snsPartyData.Load(partyUUID)
	require.False(t, kept, "the last member left and the party data stayed")
}

// The party data relay also finds a party over when it walks the members and nobody is there (the last
// member's session went away without a leave); the policy goes with the data.
func TestPartyPolicyIsDroppedWhenTheDataRelayFindsNobody(t *testing.T) {
	p, _ := partyEndEnv(t)
	partyUUID := uuid.Must(uuid.NewV4())
	p.snsPartyPolicies.Store(partyUUID, snsPartyPolicyInviteOnly)
	p.snsPartyData.Store(partyUUID, newSNSPartyDataState())

	members, readers := p.partyMembers(context.Background(), loggerForTest(t), partyUUID)
	require.Empty(t, members)
	require.False(t, readers)
	_, kept := p.snsPartyPolicies.Load(partyUUID)
	require.False(t, kept)
	_, kept = p.snsPartyData.Load(partyUUID)
	require.False(t, kept)
}
