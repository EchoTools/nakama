package server

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"regexp"
	"testing"

	"github.com/gofrs/uuid/v5"
	"github.com/heroiclabs/nakama/v3/server/evr"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const tabletGroupTestUser = "00000000-0000-0000-0000-0000000000a1"

// storedGroup is the group_id currently in storage for the user ("" when the object is absent).
func storedGroup(t *testing.T, nk *storableRaceNK) string {
	t.Helper()
	obj := nk.get(tabletGroupTestUser, MatchmakerStorageCollection, MatchmakingConfigStorageKey)
	if obj == nil {
		return ""
	}
	var s MatchmakingSettings
	require.NoError(t, json.Unmarshal([]byte(obj.Value), &s))
	return s.LobbyGroupName
}

func tabletGroupLogs(logs *observer.ObservedLogs) []observer.LoggedEntry {
	return logs.FilterMessage("Party group set to tablet").All()
}

// The SNS party requests a person makes: each sets the player's party group to "tablet".
var userPartyActions = map[string]evr.Message{
	"send invite":   &evr.SNSPartySendInviteRequest{},
	"join":          &evr.SNSPartyJoinRequest{},
	"accept invite": &evr.SNSPartyRespondToInviteRequest{},
	"set policy":    &evr.SNSPartySetJoinPolicyRequest{},
	"kick":          &evr.SNSPartyKickRequest{},
	"pass owner":    &evr.SNSPartyPassOwnershipRequest{},
}

// The SNS party requests that do NOT set the group. Create and DataUpdate are sent by the client with
// no player action (prod log, 36 h: 94% of creates and 2,582 of 9,256 data updates arrive within 2 s of
// login), Lock and Unlock look automatic too (about one per session, never within 2 s of login),
// Leave only ever leaves (it keeps whatever the group is), and Update, UpdateMember and
// InviteListRefresh were never seen. None of these may set the group until shown to be a person's
// action: setting it on an automatic one would put every nEVR player on "tablet" at login.
var nonGroupPartyRequests = map[string]evr.Message{
	"create":         &evr.SNSPartyCreateRequest{},
	"data update":    &evr.SNSPartyDataUpdateRequest{},
	"lock":           &evr.SNSPartyLockRequest{},
	"unlock":         &evr.SNSPartyUnlockRequest{},
	"leave":          &evr.SNSPartyLeaveRequest{},
	"update":         &evr.SNSPartyUpdateRequest{},
	"update member":  &evr.SNSPartyUpdateMemberRequest{},
	"refresh invite": &evr.SNSPartyInviteListRefreshRequest{},
}

func TestTabletPartyActionSetsPartyGroup(t *testing.T) {
	for name, msg := range userPartyActions {
		t.Run(name, func(t *testing.T) {
			require.True(t, isTabletPartyAction(msg), "%T must set the party group", msg)

			nk := newStorableRaceNK()
			nk.set(tabletGroupTestUser, MatchmakerStorageCollection, MatchmakingConfigStorageKey, `{"group_id":"oldcode"}`)
			core, logs := observer.New(zapcore.InfoLevel)

			require.NoError(t, setPartyGroupTablet(context.Background(), zap.New(core), nk, tabletGroupTestUser))

			require.Equal(t, "tablet", storedGroup(t, nk))
			entries := tabletGroupLogs(logs)
			require.Len(t, entries, 1, "exactly one Info record per change")
			require.Equal(t, zapcore.InfoLevel, entries[0].Level)
			fields := entries[0].ContextMap()
			require.Equal(t, tabletGroupTestUser, fields["uid"])
			require.Equal(t, "oldcode", fields["previous_group"])
		})
	}
}

// The automatic requests must not touch the group through the hook, however often the client sends them.
func TestAutomaticPartyRequestsDoNotSetPartyGroup(t *testing.T) {
	uid := uuid.FromStringOrNil(tabletGroupTestUser)
	for name, msg := range nonGroupPartyRequests {
		t.Run(name, func(t *testing.T) {
			require.False(t, isTabletPartyAction(msg), "%T must not set the party group", msg)
			nk := newStorableRaceNK()
			nk.set(tabletGroupTestUser, MatchmakerStorageCollection, MatchmakingConfigStorageKey, `{"group_id":"oldcode"}`)
			applyTabletPartyGroup(context.Background(), zap.NewNop(), nk, uid, msg)
			require.Equal(t, "oldcode", storedGroup(t, nk))
			_, writes, _ := nk.counts()
			require.Zero(t, writes)
		})
	}
}

// Every SNSParty*Request the dispatch table routes is classified above, as a person's action or as
// one that does not set the group. This reads the table, so a request added to it and left
// unclassified fails here and gets a decision about whether it is automatic.
func TestEveryRoutedSNSPartyRequestIsClassified(t *testing.T) {
	src, err := os.ReadFile("evr_pipeline.go")
	require.NoError(t, err)
	routed := regexp.MustCompile(`case \*evr\.(SNSParty\w+Request):`).FindAllStringSubmatch(string(src), -1)
	require.Len(t, routed, 14, "the dispatch table routes 14 SNS party requests")

	classified := map[string]bool{}
	for _, group := range []map[string]evr.Message{userPartyActions, nonGroupPartyRequests} {
		for _, m := range group {
			classified[reflect.TypeOf(m).Elem().Name()] = true
		}
	}
	require.Len(t, classified, 14)
	for _, m := range routed {
		require.True(t, classified[m[1]], "%s is routed by ProcessRequest but is not classified in evr_party_group_tablet_test.go", m[1])
	}
}

// Messages that are not tablet party actions must not touch the group.
func TestNonPartyMessagesDoNotSetPartyGroup(t *testing.T) {
	for _, msg := range []evr.Message{
		&evr.SNSFriendInviteRequest{},
		&evr.SNSFriendListRefreshRequest{},
		&evr.LobbyFindSessionRequest{},
		&evr.LobbyPingResponse{},
	} {
		require.False(t, isTabletPartyAction(msg), "%T must not set the party group", msg)
	}
}

// Leaving the tablet party keeps "tablet". The previous code is never written back, on leave or on
// any later action: across the whole sequence, "oldcode" is never stored again.
func TestTabletPartyGroupIsNeverRestored(t *testing.T) {
	nk := newStorableRaceNK()
	nk.set(tabletGroupTestUser, MatchmakerStorageCollection, MatchmakingConfigStorageKey, `{"group_id":"oldcode"}`)
	var writtenGroups []string
	nk.afterWrite = func(m *storableRaceNK) {
		var s MatchmakingSettings
		if obj := m.get(tabletGroupTestUser, MatchmakerStorageCollection, MatchmakingConfigStorageKey); obj != nil {
			_ = json.Unmarshal([]byte(obj.Value), &s)
		}
		writtenGroups = append(writtenGroups, s.LobbyGroupName)
	}
	core, _ := observer.New(zapcore.InfoLevel)
	logger := zap.New(core)

	for _, msg := range []evr.Message{
		&evr.SNSPartySendInviteRequest{},
		&evr.SNSPartyLeaveRequest{}, // leaving: not a trigger, and it must not restore anything
		&evr.SNSPartyRespondToInviteRequest{},
		&evr.SNSPartyPassOwnershipRequest{},
	} {
		applyTabletPartyGroup(context.Background(), logger, nk, uuid.FromStringOrNil(tabletGroupTestUser), msg)
		require.Equal(t, "tablet", storedGroup(t, nk), "after %T", msg)
	}

	require.Equal(t, []string{"tablet"}, writtenGroups, "one write in total, and it is never the old code")
}

// Already "tablet": no write, no log. A repeat action is free and does not spam the log.
func TestTabletPartyGroupAlreadySetIsNoOp(t *testing.T) {
	nk := newStorableRaceNK()
	nk.set(tabletGroupTestUser, MatchmakerStorageCollection, MatchmakingConfigStorageKey, `{"group_id":"tablet"}`)
	core, logs := observer.New(zapcore.InfoLevel)

	require.NoError(t, setPartyGroupTablet(context.Background(), zap.New(core), nk, tabletGroupTestUser))

	_, writes, _ := nk.counts()
	require.Zero(t, writes)
	require.Empty(t, tabletGroupLogs(logs))
}

// A player who never set a group (no settings object) ends up on "tablet", with previous_group empty,
// and the rest of their settings are kept.
func TestTabletPartyGroupKeepsOtherSettings(t *testing.T) {
	nk := newStorableRaceNK()
	nk.set(tabletGroupTestUser, MatchmakerStorageCollection, MatchmakingConfigStorageKey, `{"disable_arena_backfill":true,"matchmaker_query_addon":"+x"}`)
	core, logs := observer.New(zapcore.InfoLevel)

	require.NoError(t, setPartyGroupTablet(context.Background(), zap.New(core), nk, tabletGroupTestUser))

	obj := nk.get(tabletGroupTestUser, MatchmakerStorageCollection, MatchmakingConfigStorageKey)
	var s MatchmakingSettings
	require.NoError(t, json.Unmarshal([]byte(obj.Value), &s))
	require.Equal(t, "tablet", s.LobbyGroupName)
	require.True(t, s.DisableArenaBackfill)
	require.Equal(t, "+x", s.MatchmakerQueryAddon)
	entries := tabletGroupLogs(logs)
	require.Len(t, entries, 1)
	require.Equal(t, "", entries[0].ContextMap()["previous_group"])
}

// "tablet" is the marker for "no party group, use the tablet party": it must never reach the lobby
// as a group name, or every tablet player would be a member of one shared group called "tablet".
func TestTabletPartyGroupIsNotALobbyGroupName(t *testing.T) {
	require.Equal(t, "", lobbyGroupNameFromSettings(MatchmakingSettings{LobbyGroupName: "tablet"}))
	require.Equal(t, "", lobbyGroupNameFromSettings(MatchmakingSettings{}))
	require.Equal(t, "oldcode", lobbyGroupNameFromSettings(MatchmakingSettings{LobbyGroupName: "oldcode"}))
}

// The ProcessRequest hook: sets the group for a party action by a known user, does nothing for a
// non-party message or a nil user, and a storage failure neither panics nor escapes.
func TestApplyTabletPartyGroup(t *testing.T) {
	uid := uuid.FromStringOrNil(tabletGroupTestUser)
	seeded := func() *storableRaceNK {
		nk := newStorableRaceNK()
		nk.set(tabletGroupTestUser, MatchmakerStorageCollection, MatchmakingConfigStorageKey, `{"group_id":"oldcode"}`)
		return nk
	}

	t.Run("party action sets it", func(t *testing.T) {
		nk := seeded()
		applyTabletPartyGroup(context.Background(), zap.NewNop(), nk, uid, &evr.SNSPartySendInviteRequest{})
		require.Equal(t, "tablet", storedGroup(t, nk))
	})
	t.Run("non-party message does not", func(t *testing.T) {
		nk := seeded()
		applyTabletPartyGroup(context.Background(), zap.NewNop(), nk, uid, &evr.SNSFriendInviteRequest{})
		require.Equal(t, "oldcode", storedGroup(t, nk))
	})
	t.Run("nil user does not touch storage", func(t *testing.T) {
		nk := seeded()
		applyTabletPartyGroup(context.Background(), zap.NewNop(), nk, uuid.Nil, &evr.SNSPartySendInviteRequest{})
		reads, writes, _ := nk.counts()
		require.Zero(t, reads+writes)
	})
	t.Run("storage failure is a Warn and the caller carries on", func(t *testing.T) {
		nk := seeded()
		nk.readErrFrom, nk.readErr = 1, errors.New("storage down")
		core, logs := observer.New(zapcore.WarnLevel)
		applyTabletPartyGroup(context.Background(), zap.New(core), nk, uid, &evr.SNSPartySendInviteRequest{})
		require.Len(t, logs.FilterMessage("Failed to set party group to tablet").All(), 1)
		require.Equal(t, "oldcode", storedGroup(t, nk))
	})
}

// GetPartyGroupUserIDs refuses the tablet marker: it names no group, and listing it would return
// every tablet player.
func TestGetPartyGroupUserIDsRefusesTabletMarker(t *testing.T) {
	_, err := GetPartyGroupUserIDs(context.Background(), nil, "tablet")
	require.Error(t, err)
	require.Equal(t, codes.InvalidArgument, status.Code(err))
}
