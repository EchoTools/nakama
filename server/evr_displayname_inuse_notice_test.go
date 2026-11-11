package server

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/heroiclabs/nakama-common/api"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"
)

// Tests for EchoTools/nakama#777: the reserved-display-name DM.
//
// initializeSession cannot be driven from a unit test (it needs a live session,
// storage and a Discord client), so these exercise the two steps it delegates
// to -- pruneDisplayNamesOwnedByOthers and sendDisplayNameNotices -- the same
// way the login loop calls them.

const (
	noticePlayerID   = "11111111-1111-1111-1111-111111111111"
	noticeOwnerID    = "22222222-2222-2222-2222-222222222222"
	noticeOtherOwner = "33333333-3333-3333-3333-333333333333"
	noticeGroupA     = "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"
	noticeGroupB     = "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb"
)

// noticeRig records every DM and log line a simulated login produces.
type noticeRig struct {
	mu    sync.Mutex
	sent  []string // message text of each DM
	to    []string // Discord ID each DM went to
	logs  *observer.ObservedLogs
	log   *zap.Logger
	owner string // username the owner lookup resolves ("" = lookup fails)
	err   error  // error every send returns
}

func newNoticeRig(ownerUsername string) *noticeRig {
	core, logs := observer.New(zapcore.DebugLevel)
	return &noticeRig{logs: logs, log: zap.New(core), owner: ownerUsername}
}

func (r *noticeRig) deps() displayNameNoticeDeps {
	return displayNameNoticeDeps{
		OwnerUsername: func(context.Context, string) string { return r.owner },
		SendDM: func(_ context.Context, discordID, message string) error {
			r.mu.Lock()
			defer r.mu.Unlock()
			r.sent = append(r.sent, message)
			r.to = append(r.to, discordID)
			return r.err
		},
	}
}

// login simulates one login's prune-then-notify, against a profile that holds
// name in every group of groupIDs (the Discord nick refresh puts it back each
// login, which is why the same name is owed a notice again).
func (r *noticeRig) login(history *DisplayNameHistory, name string, ownerID string, notify bool, groupIDs ...string) *EVRProfile {
	profile := &EVRProfile{
		account:     &api.Account{User: &api.User{Id: noticePlayerID, Username: "playerhandle"}},
		InGameNames: make(map[string]GroupInGameName),
	}
	for _, g := range groupIDs {
		profile.InGameNames[g] = GroupInGameName{GroupID: g, DisplayName: name}
	}
	ownerMap := map[string][]string{name: {ownerID}}

	_, notices := pruneDisplayNamesOwnedByOthers(profile, history, ownerMap, notify, time.Now())
	sendDisplayNameNotices(context.Background(), r.log, r.deps(),
		displayNameNoticePlayer{UserID: noticePlayerID, DiscordID: "100", Username: "playerhandle"}, notices)
	return profile
}

// (a) The same owned nick on two logins is one notice, not two.
func TestDisplayNameInUseNotice_SameNameSameOwnerSentOnce(t *testing.T) {
	r := newNoticeRig("ownerhandle")
	history := NewDisplayNameHistory()

	r.login(history, "Taken", noticeOwnerID, true, noticeGroupA)
	r.login(history, "Taken", noticeOwnerID, true, noticeGroupA)

	require.Len(t, r.sent, 1, "second login must not repeat the notice; got %q", r.sent)
}

// A different owner of the same name is a new fact the player has not been told.
func TestDisplayNameInUseNotice_NewOwnerIsNotified(t *testing.T) {
	r := newNoticeRig("ownerhandle")
	history := NewDisplayNameHistory()

	r.login(history, "Taken", noticeOwnerID, true, noticeGroupA)
	r.login(history, "Taken", noticeOtherOwner, true, noticeGroupA)

	require.Len(t, r.sent, 2)
}

// (b) One login, the nick matching IGNs in two guilds: one DM.
func TestDisplayNameInUseNotice_TwoGuildsOneDM(t *testing.T) {
	r := newNoticeRig("ownerhandle")

	profile := r.login(NewDisplayNameHistory(), "Taken", noticeOwnerID, true, noticeGroupA, noticeGroupB)

	require.Len(t, r.sent, 1, "one login must send one DM, not one per guild; got %q", r.sent)
	require.Empty(t, profile.DisplayNamesByGroupID(), "the owned name must still be pruned from every guild")
}

// The setting is the gate: off means no DM and nothing remembered, so turning
// it on later still notifies.
func TestDisplayNameInUseNotice_DisabledSendsNothingAndRemembersNothing(t *testing.T) {
	r := newNoticeRig("ownerhandle")
	history := NewDisplayNameHistory()

	profile := r.login(history, "Taken", noticeOwnerID, false, noticeGroupA)
	require.Empty(t, r.sent)
	require.Empty(t, profile.DisplayNamesByGroupID(), "the name is pruned whether or not notices are on")

	r.login(history, "Taken", noticeOwnerID, true, noticeGroupA)
	require.Len(t, r.sent, 1)
}

// (c) The DM names the owner in a form that does not rely on a mention that
// may not resolve for the recipient.
func TestDisplayNameInUseNotice_NamesOwnerByUsername(t *testing.T) {
	r := newNoticeRig("ownerhandle")

	r.login(NewDisplayNameHistory(), "Taken", noticeOwnerID, true, noticeGroupA)

	require.Len(t, r.sent, 1)
	require.Contains(t, r.sent[0], "ownerhandle")
	require.NotContains(t, r.sent[0], "<@", "a mention shows as @unknown-user when the recipient cannot resolve it")
	require.Contains(t, r.sent[0], "Taken")
	require.Contains(t, r.sent[0], "playerhandle", "the fallback in-game name is still stated")
}

// (c) When the owner lookup fails the DM still reads sensibly: never "unknown".
func TestDisplayNameInUseNotice_OwnerLookupFailsFallsBackGracefully(t *testing.T) {
	r := newNoticeRig("")

	r.login(NewDisplayNameHistory(), "Taken", noticeOwnerID, true, noticeGroupA)

	require.Len(t, r.sent, 1)
	require.NotContains(t, strings.ToLower(r.sent[0]), "unknown")
	require.NotContains(t, r.sent[0], "<@")
	require.Contains(t, r.sent[0], "another player")
}

// (d) A successful send is logged at Info, attributable by player and owner id.
func TestDisplayNameInUseNotice_SuccessfulSendIsLogged(t *testing.T) {
	r := newNoticeRig("ownerhandle")

	r.login(NewDisplayNameHistory(), "Taken", noticeOwnerID, true, noticeGroupA)

	entries := r.logs.FilterLevelExact(zapcore.InfoLevel).All()
	require.Len(t, entries, 1, "one Info record per send; all logs: %v", r.logs.All())
	e := entries[0]
	require.Equal(t, "Sent display name in use notification", e.Message)
	require.NotContains(t, e.Message, "Taken", "no player-chosen text in the message; it goes in fields")
	f := e.ContextMap()
	require.Equal(t, noticePlayerID, f["user_id"])
	require.Equal(t, noticeOwnerID, f["owner_user_id"])
	require.Equal(t, "Taken", f["display_name"])
}

// A send that fails is logged loudly and is not logged as a success.
func TestDisplayNameInUseNotice_FailedSendIsLoggedAsFailure(t *testing.T) {
	r := newNoticeRig("ownerhandle")
	r.err = errors.New("boom")

	r.login(NewDisplayNameHistory(), "Taken", noticeOwnerID, true, noticeGroupA)

	require.Empty(t, r.logs.FilterLevelExact(zapcore.InfoLevel).All(), "a failed send is not a success record")
	require.Len(t, r.logs.FilterLevelExact(zapcore.WarnLevel).All(), 1)
}

// The record survives a round trip through the stored JSON, which is how the
// next login sees it.
func TestDisplayNameHistory_InUseNoticeSurvivesStorageRoundTrip(t *testing.T) {
	r := newNoticeRig("ownerhandle")
	history := NewDisplayNameHistory()
	r.login(history, "Taken", noticeOwnerID, true, noticeGroupA)

	raw, err := history.MarshalJSON()
	require.NoError(t, err)
	reloaded := NewDisplayNameHistory()
	require.NoError(t, json.Unmarshal(raw, reloaded))

	r.login(reloaded, "Taken", noticeOwnerID, true, noticeGroupA)
	require.Len(t, r.sent, 1)
}
