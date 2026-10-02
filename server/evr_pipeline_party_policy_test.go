package server

import (
	"testing"

	"github.com/gofrs/uuid/v5"
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
