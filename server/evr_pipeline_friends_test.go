package server

import (
	"testing"

	"github.com/gofrs/uuid/v5"
	"github.com/heroiclabs/nakama-common/api"
	"github.com/stretchr/testify/require"
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

// Each confirmed friend's account id is a database lookup. It is resolved once and used for both the
// status notify every client gets and the presence a nevr-runtime client gets, so a stock client (which
// is never sent presence) costs exactly the lookups it had before presence existed: one per confirmed
// friend. Before, presence resolved every friend a second time, whatever the client.
func TestFriendAccountIDsAreResolvedOncePerFriend(t *testing.T) {
	online, offline, unresolvable := uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4())
	friends := []*api.Friend{
		friendFixture(online.String(), FriendStateFriends, true),
		friendFixture(offline.String(), FriendStateFriends, false),
		friendFixture(unresolvable.String(), FriendStateFriends, true),
		friendFixture(uuid.Must(uuid.NewV4()).String(), FriendInvitationSent, false),
	}
	accountIDs := map[string]uint64{online.String(): 111, offline.String(): 222}
	lookups := map[string]int{}
	notifications, targets := friendNotifies(friends, func(f *api.Friend) (uint64, bool) {
		lookups[f.User.Id]++
		id, ok := accountIDs[f.User.Id]
		return id, ok
	})

	require.Equal(t, map[string]int{online.String(): 1, offline.String(): 1, unresolvable.String(): 1}, lookups,
		"one lookup per confirmed friend, none for an invitation")
	require.Len(t, notifications, 2)
	require.Equal(t, []friendPresenceTarget{
		{userID: online, accountID: 111, online: true},
		{userID: offline, accountID: 222, online: false},
	}, targets, "presence goes to the friends the notifies resolved, with their ids; an unresolvable friend gets neither")
}

// A player who was offline when a friend request arrived is told who asked, once per login (#684): the
// subscribe reply replays one invite per pending received request, after the status notifies. The refresh
// reply never does, because the nevr runtime answers every invite notify with a refresh and a replay there
// would be answered by a refresh and replayed again, for ever.
func TestBuildFriendListReply_ReplaysPendingRequestsOnlyWhenAsked(t *testing.T) {
	friends := []*api.Friend{
		friendFixture("friend-online", FriendStateFriends, true),
		friendFixture("asked-first", FriendInvitationReceived, false),
		friendFixture("friend-offline", FriendStateFriends, false),
		friendFixture("i-asked", FriendInvitationSent, false),
		friendFixture("asked-unresolvable", FriendInvitationReceived, false),
		friendFixture("blocked", FriendStateBlocked, false),
		friendFixture("asked-second", FriendInvitationReceived, true),
	}
	accounts := map[string]uint64{
		"friend-online":  111,
		"friend-offline": 222,
		"asked-first":    333,
		"asked-second":   444,
		"i-asked":        555,
		"blocked":        666,
	}
	lookups := map[string]int{}
	resolve := func(f *api.Friend) (uint64, bool) {
		lookups[f.User.Id]++
		id, ok := accounts[f.User.Id]
		return id, ok
	}

	subscribe := buildFriendListReply(friends, resolve, true)
	require.Equal(t, []uint64{333, 444}, subscribe.inviteSenders,
		"one per pending received request, in list order; not the request this player sent, not the blocked, not the unresolvable")
	require.EqualValues(t, 1, subscribe.counts.NOnline)
	require.EqualValues(t, 1, subscribe.counts.NOffline)
	require.EqualValues(t, 1, subscribe.counts.NSent)
	require.EqualValues(t, 3, subscribe.counts.NRecv, "the count is of requests, resolvable or not, as before")
	require.Len(t, subscribe.statuses, 2, "the roster is unchanged by a replay")
	require.Equal(t, map[string]int{
		"friend-online": 1, "friend-offline": 1, "asked-first": 1, "asked-unresolvable": 1, "asked-second": 1,
	}, lookups, "one lookup per confirmed friend and per replayed request; none for a request this player sent")

	lookups = map[string]int{}
	refresh := buildFriendListReply(friends, resolve, false)
	require.Empty(t, refresh.inviteSenders, "a refresh is never answered with a replay")
	require.Equal(t, subscribe.counts, refresh.counts)
	require.Equal(t, subscribe.statuses, refresh.statuses)
	require.Equal(t, map[string]int{"friend-online": 1, "friend-offline": 1}, lookups,
		"a refresh resolves only the confirmed friends, as before")

	none := buildFriendListReply([]*api.Friend{friendFixture("friend-online", FriendStateFriends, true)}, resolve, true)
	require.Empty(t, none.inviteSenders, "nothing pending, nothing replayed")
}

// Which request replays: the subscribe (once per login) and no other. A refresh that replayed would loop
// with a runtime that refreshes on every invite notify.
func TestFriendListRequest_OnlySubscribeReplaysInvites(t *testing.T) {
	require.True(t, friendListSubscribe.replaysInvites())
	require.False(t, friendListRefresh.replaysInvites(), "a refresh never replays: notify -> refresh -> notify would not end")
}
