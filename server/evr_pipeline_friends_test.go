package server

import (
	"testing"

	"github.com/heroiclabs/nakama-common/api"
	"google.golang.org/protobuf/types/known/wrapperspb"
)

// TestFriendStatusCode locks the online/offline StatusCode mapping consumed by
// pnsrad's CNSRADFriends::StatusNotifyCB (confirmed via ReVault 2026-09-13,
// AddFriend @ pnsrad.dll/libpnsrad.so — see evr_pipeline_friends.go's
// friendStatusCode doc comment for the full evidence trail). The busy slot (1)
// is not covered here because nothing in this pipeline ever produces it: the
// only UI consumer found (R15NETFRIENDSEXPRESSION) has no "nbusy" output, so
// there is nothing to assert against yet.
func TestFriendStatusCode(t *testing.T) {
	if got := friendStatusCode(true); got != 0 {
		t.Errorf("online: got StatusCode %d, want 0", got)
	}
	if got := friendStatusCode(false); got != 2 {
		t.Errorf("offline: got StatusCode %d, want 2", got)
	}
}

func friendFixture(userID string, state int32, online bool) *api.Friend {
	return &api.Friend{
		User:  &api.User{Id: userID, Online: online},
		State: &wrapperspb.Int32Value{Value: state},
	}
}

// TestFriendStatusNotifications_OnlyConfirmedFriends asserts that pending
// invitations and blocks never produce a notification — only
// State == FriendStateFriends should reach the wire. Before this fix,
// SNSFriendStatusNotify was never sent at all (see the fix's commit message);
// this test guards the filter now that it exists, since a bug here would
// leak pending-invite users into the visible roster.
func TestFriendStatusNotifications_OnlyConfirmedFriends(t *testing.T) {
	friends := []*api.Friend{
		friendFixture("friend-online", FriendStateFriends, true),
		friendFixture("friend-offline", FriendStateFriends, false),
		friendFixture("invite-sent", FriendInvitationSent, false),
		friendFixture("invite-received", FriendInvitationReceived, false),
		friendFixture("blocked", FriendStateBlocked, false),
	}

	resolved := map[string]uint64{
		"friend-online":  111,
		"friend-offline": 222,
	}
	resolveAccountID := func(f *api.Friend) (uint64, bool) {
		id, ok := resolved[f.User.Id]
		return id, ok
	}

	got := friendStatusNotifications(friends, resolveAccountID)

	want := map[uint64]uint8{
		111: 0, // online
		222: 2, // offline
	}
	if len(got) != len(want) {
		t.Fatalf("got %d notifications, want %d: %+v", len(got), len(want), got)
	}
	for _, n := range got {
		wantCode, ok := want[n.FriendID]
		if !ok {
			t.Errorf("unexpected notification for friend id %d (pending invite or block leaked through): %+v", n.FriendID, n)
			continue
		}
		if n.StatusCode != wantCode {
			t.Errorf("friend id %d: got StatusCode %d, want %d", n.FriendID, n.StatusCode, wantCode)
		}
	}
}

// TestFriendStatusNotifications_UnresolvableFriendSkipped asserts that a
// friend the resolver can't place (e.g. no user_device row) is skipped
// rather than sent with a zero-value FriendID — a zero FriendID would show up
// client-side as a bogus/garbage roster entry instead of just being absent.
func TestFriendStatusNotifications_UnresolvableFriendSkipped(t *testing.T) {
	friends := []*api.Friend{
		friendFixture("unresolvable", FriendStateFriends, true),
	}
	resolveAccountID := func(f *api.Friend) (uint64, bool) {
		return 0, false
	}

	got := friendStatusNotifications(friends, resolveAccountID)
	if len(got) != 0 {
		t.Errorf("got %d notifications, want 0 (unresolvable friend must be skipped): %+v", len(got), got)
	}
}
