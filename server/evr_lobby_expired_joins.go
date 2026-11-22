package server

import (
	"sync"
	"time"

	"github.com/gofrs/uuid/v5"
)

// A join marker expires when the game client has not connected to the game server within the join deadline.
// The match then removes the entrant, and the periodic backfill, which still sees the ticket, places the same
// entrant straight back into the same match with a fresh success. That cycle repeats while the client is
// still joining. So the match records each expiry here, and backfill leaves that session out of that match
// for a while.
//
// The record is a package-level store, not a method on the match registry. The sweep runs in a match
// handler's goroutine and backfill runs in the lobby builder's: neither holds a reference to the other, and
// adding a method to MatchRegistry would change every test double that implements it. It is node-local, as
// the lobby session success cache is: backfill only resolves sessions on its own node, and the deployment
// runs one nakama node, so every expiry backfill can act on was recorded in this process.
const expiredJoinBackoff = 5 * time.Minute

type expiredJoinKey struct {
	match   uuid.UUID
	session uuid.UUID
}

var expiredJoins = struct {
	sync.Mutex
	until map[expiredJoinKey]time.Time // the end of the backoff window for each (match, session)
}{until: map[expiredJoinKey]time.Time{}}

// rememberExpiredJoin records that session's join marker expired in match at now, and drops windows that
// have already ended.
func rememberExpiredJoin(match, session uuid.UUID, now time.Time) {
	c := &expiredJoins
	c.Lock()
	defer c.Unlock()
	for k, until := range c.until {
		if !now.Before(until) {
			delete(c.until, k)
		}
	}
	c.until[expiredJoinKey{match, session}] = now.Add(expiredJoinBackoff)
}

// expiredJoinBlocked reports whether session's join marker expired in match and the backoff window is still
// open at now.
func expiredJoinBlocked(match, session uuid.UUID, now time.Time) bool {
	c := &expiredJoins
	c.Lock()
	defer c.Unlock()
	until, ok := c.until[expiredJoinKey{match, session}]
	return ok && now.Before(until)
}

// candidateExpiredFrom reports whether any member of the candidate's ticket is in an open backoff window
// for match at now. A party is placed whole, so one member keeps the whole party out of the match.
func candidateExpiredFrom(candidate *preparedBackfillCandidate, match uuid.UUID, now time.Time) bool {
	for _, entry := range candidate.Entries {
		if expiredJoinBlocked(match, uuid.FromStringOrNil(entry.Presence.GetSessionId()), now) {
			return true
		}
	}
	return false
}
