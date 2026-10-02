package evr

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// A log line about a social message must say which message it is and carry the fields an operator
// searches by (the friend, the party, the policy, the sizes). Each String names the type and shows
// its key fields; the party data messages show only the size of the JSON, never the JSON itself, since
// that is the player's script data.
func TestSocialMessageStringsNameTheTypeAndKeyFields(t *testing.T) {
	cases := []struct {
		name string
		msg  interface{ String() string }
		want []string
	}{
		{"friend status", SNSFriendStatusNotify{FriendID: 0xabc, StatusCode: 2},
			[]string{"SNSFriendStatusNotify", "0000000000000abc", "status=2"}},
		{"friend presence", SNSFriendPresenceNotify{FriendID: 0xabc, PartyID: 77, Joinable: 1, StatusCode: 0, Text: []byte("In Main Menu")},
			[]string{"SNSFriendPresenceNotify", "0000000000000abc", "party=77", "joinable=1", `"In Main Menu"`}},
		{"party lock", SNSPartyLockRequest{RoutingID: 1, SessionGUID: 2, TargetParam: 3},
			[]string{"SNSPartyLockRequest", "routing=0000000000000001", "session=0000000000000002"}},
		{"party join policy", SNSPartySetJoinPolicyRequest{RoutingID: 1, SessionGUID: 2, TargetParam: 3},
			[]string{"SNSPartySetJoinPolicyRequest", "policy=3"}},
		{"party data update", SNSPartyDataUpdateRequest{TargetParam: 1, Seq: 9, Json: []byte(`{"secret":"x"}`)},
			[]string{"SNSPartyDataUpdateRequest", "scope=1", "seq=9", "json_bytes=14"}},
		{"party data notify", SNSPartyDataNotify{PartyID: 5, MemberID: 6, Seq: 7, Json: []byte(`{}`)},
			[]string{"SNSPartyDataNotify", "party=5", "member=6", "seq=7", "json_bytes=2"}},
		{"recently met refresh", SNSRecentlyMetRefreshRequest{RoutingID: 1},
			[]string{"SNSRecentlyMetRefreshRequest"}},
		{"recently met list", SNSRecentlyMetListResponse{Entries: []RecentlyMetEntry{{AccountID: 1}, {AccountID: 2}}},
			[]string{"SNSRecentlyMetListResponse", "entries=2"}},
	}
	for _, c := range cases {
		got := c.msg.String()
		for _, w := range c.want {
			require.Contains(t, got, w, c.name)
		}
	}
	// The party data messages never put the JSON in the log line.
	require.False(t, strings.Contains(SNSPartyDataUpdateRequest{Json: []byte(`{"secret":"x"}`)}.String(), "secret"))
}

// The friend status and presence messages are what the game client is sent for each friend: the
// token names the message on the wire, the symbol is its hash, and the bytes decode back to the same
// fields. The presence text length is computed on encode, not trusted from the struct.
func TestFriendMessagesTokenSymbolAndWireRoundTrip(t *testing.T) {
	status := &SNSFriendStatusNotify{FriendID: 0x1122334455667788, StatusCode: 2}
	require.Equal(t, "SNSFriendStatusNotify", status.Token())
	require.Equal(t, ToSymbol("SNSFriendStatusNotify"), status.Symbol())
	presence := &SNSFriendPresenceNotify{FriendID: 42, PartyID: 99, Joinable: 1, StatusCode: 0, Text: []byte("Public Arena Match"), TextLen: 3}
	require.Equal(t, "SNSFriendPresenceNotify", presence.Token())
	require.Equal(t, ToSymbol("SNSFriendPresenceNotify"), presence.Symbol())

	for _, in := range []Message{status, presence} {
		b, err := Marshal(in)
		require.NoError(t, err)
		out, err := ParsePacket(b)
		require.NoError(t, err)
		require.Len(t, out, 1)
		switch want := in.(type) {
		case *SNSFriendStatusNotify:
			got, ok := out[0].(*SNSFriendStatusNotify)
			require.True(t, ok, "got %T", out[0])
			require.Equal(t, want.FriendID, got.FriendID)
			require.Equal(t, want.StatusCode, got.StatusCode)
		case *SNSFriendPresenceNotify:
			got, ok := out[0].(*SNSFriendPresenceNotify)
			require.True(t, ok, "got %T", out[0])
			require.Equal(t, uint64(42), got.FriendID)
			require.Equal(t, uint64(99), got.PartyID)
			require.Equal(t, uint8(1), got.Joinable)
			require.Equal(t, "Public Arena Match", string(got.Text))
			require.Equal(t, uint16(len("Public Arena Match")), got.TextLen)
		}
	}
}

// A login request knows who is logging in: the account the game client signed in as, from the
// request's header, not from the JSON payload.
func TestLoginRequestGetEvrIDIsTheHeaderXPID(t *testing.T) {
	id := EvrId{PlatformCode: 4, AccountId: 900000000000000101}
	req, err := NewLoginRequest([16]byte{}, id, LoginProfile{AccountId: 1})
	require.NoError(t, err)
	require.Equal(t, id, req.GetEvrID())
}
