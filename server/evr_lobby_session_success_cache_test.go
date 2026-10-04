package server

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/gofrs/uuid/v5"
	"github.com/heroiclabs/nakama/v3/server/evr"
	"github.com/stretchr/testify/require"
)

// dupJoinRegistry answers JoinAttempt from a script: the first attempt grants the seat, the second is
// the duplicate join a game client sends when it never acted on the LobbySessionSuccess.
type dupJoinRegistry struct {
	*mockFollowMatchRegistry
	answers []func() (bool, bool, bool, string, string)
}

func (r *dupJoinRegistry) JoinAttempt(context.Context, uuid.UUID, string, uuid.UUID, uuid.UUID, string, int64, map[string]string, string, string, string, map[string]string) (bool, bool, bool, string, string, []*MatchPresence) {
	next := r.answers[0]
	r.answers = r.answers[1:]
	found, allowed, isNew, reason, label := next()
	return found, allowed, isNew, reason, label, nil
}

func capturingSession(t *testing.T, tracker Tracker) *sessionWS {
	s := &sessionWS{}
	s.id = uuid.Must(uuid.NewV4())
	s.userID = uuid.Must(uuid.NewV4())
	s.ctx = context.Background()
	s.logger = loggerForTest(t)
	s.format = SessionFormatEVR
	s.outgoingCh = make(chan []byte, 16)
	s.tracker = tracker
	return s
}

func drain(ch chan []byte) [][]byte {
	out := [][]byte{}
	for {
		select {
		case b := <-ch:
			out = append(out, b)
		default:
			return out
		}
	}
}

// A duplicate join is answered with the same LobbySessionSuccess the seat was granted with, byte for
// byte (same keys as the game server's copy), to the game client only.
func TestDuplicateJoinReSendsTheSameLobbySessionSuccess(t *testing.T) {
	tracker := newMockMatchmakingTracker()
	client := capturingSession(t, tracker)
	server := capturingSession(t, tracker)
	matchUUID := uuid.Must(uuid.NewV4())
	groupID := uuid.Must(uuid.NewV4())
	label := &MatchLabel{ID: MatchID{UUID: matchUUID, Node: "testnode"}, Mode: evr.ModeArenaPublic, GroupID: &groupID,
		GameServer: &GameServerPresence{}, RequiredFeatures: []string{}}
	labelJSON, err := json.Marshal(label)
	require.NoError(t, err)
	entrant := &EvrMatchPresence{SessionID: client.id, UserID: client.userID, Node: "testnode",
		EvrID: evr.EvrId{PlatformCode: 4, AccountId: 900000000000000101}, RoleAlignment: evr.TeamOrange,
		EntrantID: uuid.Must(uuid.NewV4())}
	granted, err := json.Marshal(entrant)
	require.NoError(t, err)
	registry := &dupJoinRegistry{mockFollowMatchRegistry: newMockFollowMatchRegistry(), answers: []func() (bool, bool, bool, string, string){
		func() (bool, bool, bool, string, string) { return true, true, true, string(granted), string(labelJSON) },
		func() (bool, bool, bool, string, string) {
			return true, false, false, ErrJoinRejectReasonDuplicateJoin.Error(), ""
		},
	}}

	// The seat is granted: the game server and the game client are both sent the success.
	require.NoError(t, LobbyJoinEntrants(loggerForTest(t), registry, tracker, client, server, label, entrant))
	first := drain(client.outgoingCh)
	require.Len(t, first, 1, "the game client is sent one LobbySessionSuccess")
	toServer := drain(server.outgoingCh)
	require.NotEmpty(t, toServer)
	msgs, err := evr.ParsePacket(first[0])
	require.NoError(t, err)
	require.Len(t, msgs, 1)
	sent, ok := msgs[0].(*evr.LobbySessionSuccessv5)
	require.True(t, ok, "got %T", msgs[0])
	require.Equal(t, int16(evr.TeamOrange), sent.TeamIndex)

	// The game client ignored it and asks to join again: the same bytes go back to it, nothing to
	// the game server.
	require.NoError(t, LobbyJoinEntrants(loggerForTest(t), registry, tracker, client, server, label, entrant))
	again := drain(client.outgoingCh)
	require.Len(t, again, 1, "the duplicate join is answered")
	require.True(t, bytes.Equal(first[0], again[0]), "the re-sent success must carry the keys the game server holds")
	require.Empty(t, drain(server.outgoingCh), "the game server already has this success")

	// A retry gets the same answer: the entry stays until it expires.
	registry.answers = append(registry.answers, func() (bool, bool, bool, string, string) {
		return true, false, false, ErrJoinRejectReasonDuplicateJoin.Error(), ""
	})
	require.NoError(t, LobbyJoinEntrants(loggerForTest(t), registry, tracker, client, server, label, entrant))
	third := drain(client.outgoingCh)
	require.Len(t, third, 1, "a retry is answered again")
	require.True(t, bytes.Equal(first[0], third[0]))
}

// With nothing kept (expired, another node, a restart), a duplicate join is unanswered, as before.
func TestDuplicateJoinWithNothingKeptSendsNothing(t *testing.T) {
	tracker := newMockMatchmakingTracker()
	client := capturingSession(t, tracker)
	require.NoError(t, resendLobbySessionSuccess(loggerForTest(t), client, uuid.Must(uuid.NewV4()),
		&EvrMatchPresence{SessionID: client.id, UserID: client.userID}))
	require.Empty(t, drain(client.outgoingCh))
}

func TestKeptLobbySessionSuccessExpires(t *testing.T) {
	match, session := uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4())
	now := time.Now()
	require.NoError(t, rememberLobbySessionSuccess(match, session, &evr.LobbySessionSuccessv5{TeamIndex: 1}, now))
	_, ok := sentLobbySessionSuccessFor(match, session, now.Add(lobbySessionSuccessKeep-time.Second))
	require.True(t, ok)
	_, ok = sentLobbySessionSuccessFor(match, session, now.Add(lobbySessionSuccessKeep+time.Second))
	require.False(t, ok)
	// A later remember drops expired entries.
	require.NoError(t, rememberLobbySessionSuccess(uuid.Must(uuid.NewV4()), session, &evr.LobbySessionSuccessv5{}, now.Add(lobbySessionSuccessKeep+time.Second)))
	sentLobbySessionSuccesses.Lock()
	_, kept := sentLobbySessionSuccesses.m[lobbySessionSuccessKey{match, session}]
	sentLobbySessionSuccesses.Unlock()
	require.False(t, kept)
}

// What is replayed is the message as it was when sent: a later change to the message object does not
// reach the kept bytes.
func TestKeptLobbySessionSuccessIsTheMessageAsSent(t *testing.T) {
	tracker := newMockMatchmakingTracker()
	client := capturingSession(t, tracker)
	match := uuid.Must(uuid.NewV4())
	message := &evr.LobbySessionSuccessv5{TeamIndex: int16(evr.TeamOrange), ClientMacKey: []byte{1, 2, 3}}
	want, err := evr.Marshal(message)
	require.NoError(t, err)
	require.NoError(t, rememberLobbySessionSuccess(match, client.id, message, time.Now()))
	message.TeamIndex = int16(evr.TeamBlue)
	message.ClientMacKey[0] = 9
	require.NoError(t, resendLobbySessionSuccess(loggerForTest(t), client, match, &EvrMatchPresence{SessionID: client.id, UserID: client.userID}))
	got := drain(client.outgoingCh)
	require.Len(t, got, 1)
	require.True(t, bytes.Equal(want, got[0]))
}
