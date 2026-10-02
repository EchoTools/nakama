package server

import (
	"sync"
	"time"

	"github.com/gofrs/uuid/v5"
	"github.com/heroiclabs/nakama/v3/server/evr"
	"go.uber.org/zap"
)

// A LobbySessionSuccess is what makes a game client join a match: it carries the game server's
// endpoint and the per-join keys, and the game client then connects to the game server. The game
// service sends the same message, with the same keys, to the game server and to the game client
// (LobbyJoinEntrants); the keys are random per message (evr.NewLobbySessionCryptoMaterial).
//
// A game client acts on it only while it is creating, finding or joining a lobby (echovr.exe
// LobbySessionSuccessCB 0x14017db90). A party member who was not searching when the leader's party
// ticket matched is granted a seat and sent the message, drops it, and later asks to join that match
// itself (the game's party follow, or a find): the join is a "duplicate join" because the session
// already holds the seat, and nothing answered it. So the game service keeps each message it sent for
// a while and, on a duplicate join, sends the same message again: the game client joins with the keys
// the game server already holds. A fresh message would carry keys the game server never received.

const lobbySessionSuccessKeep = 2 * time.Minute

type sentLobbySessionSuccess struct {
	payload []byte // the message as sent (evr.Marshal), so a replay is byte-identical
	team    int16  // for the re-send's log line
	expiry  time.Time
}

type lobbySessionSuccessKey struct {
	match   uuid.UUID
	session uuid.UUID
}

var sentLobbySessionSuccesses = struct {
	sync.Mutex
	m map[lobbySessionSuccessKey]sentLobbySessionSuccess
}{m: map[lobbySessionSuccessKey]sentLobbySessionSuccess{}}

// rememberLobbySessionSuccess keeps the message sent to a session for a match, serialized as it was
// sent, and drops expired ones.
func rememberLobbySessionSuccess(match, session uuid.UUID, message *evr.LobbySessionSuccessv5, now time.Time) error {
	payload, err := evr.Marshal(message)
	if err != nil {
		return err
	}
	c := &sentLobbySessionSuccesses
	c.Lock()
	defer c.Unlock()
	for k, v := range c.m {
		if now.After(v.expiry) {
			delete(c.m, k)
		}
	}
	c.m[lobbySessionSuccessKey{match, session}] = sentLobbySessionSuccess{payload: payload, team: message.TeamIndex,
		expiry: now.Add(lobbySessionSuccessKeep)}
	return nil
}

// sentLobbySessionSuccessFor is the message last sent to the session for the match, if kept and
// unexpired. The entry stays after a re-send: the game client may ask again (a lost message, a retry),
// and each ask gets the same answer until it expires.
func sentLobbySessionSuccessFor(match, session uuid.UUID, now time.Time) (sentLobbySessionSuccess, bool) {
	c := &sentLobbySessionSuccesses
	c.Lock()
	defer c.Unlock()
	v, ok := c.m[lobbySessionSuccessKey{match, session}]
	if !ok || now.After(v.expiry) {
		return sentLobbySessionSuccess{}, false
	}
	return v, true
}

// resendLobbySessionSuccess answers a duplicate join: the session holds a seat in the match but its
// game client asked to join again, so it never acted on the message that grants it. Sends that same
// message to the game client only (the game server already has it). With none kept, nothing is sent,
// as before, and the miss is logged.
func resendLobbySessionSuccess(logger *zap.Logger, session Session, match uuid.UUID, e *EvrMatchPresence) error {
	fields := []zap.Field{zap.String("mid", match.String()), zap.String("uid", e.UserID.String()), zap.String("sid", e.SessionID.String())}
	sent, ok := sentLobbySessionSuccessFor(match, e.SessionID, time.Now())
	if !ok {
		logger.Warn("Duplicate join: no kept lobby session success to re-send; the game client is not answered", fields...)
		return nil
	}
	if err := session.SendBytes(sent.payload, true); err != nil {
		logger.Error("Duplicate join: failed to re-send lobby session success to the game client", append(fields, zap.Error(err))...)
		return err
	}
	logger.Info("Duplicate join: re-sent the lobby session success to the game client",
		append(fields, zap.Int16("team", sent.team))...)
	return nil
}
