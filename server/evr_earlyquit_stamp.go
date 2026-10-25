package server

import (
	"sync"
	"time"

	"github.com/heroiclabs/nakama/v3/server/evr"
)

// earlyQuitStampSweepSecs is how far the clock must move past the newest stamp before the registry
// drops the entries it holds.
const earlyQuitStampSweepSecs = 60

// earlyQuitStamps hands out the stamp every early quit update carries (+0x18, #712).
//
// The game ignores an update whose stamp is not newer than the one it holds, so each stamp sent to a
// player must be greater than the last one sent to that player: max(previous+1, now), in Unix seconds.
// With a stamp of about now, a client that still reads +0x18 as the lockout expiry sees a lockout that
// is already over, and shows no countdown rather than a wrong one.
//
// The previous stamp is held in memory, not in the stored early quit state. The sender is also given
// states that are not the player's stored one (the expiry scheduler sends a zeroed state), so it
// cannot write the stamp back, and a versioned write from there would race the other writers. What a
// restart forgets only matters if a player was sent more than one update a second for long enough to
// run the stamp ahead of the clock.
type earlyQuitStamps struct {
	mu      sync.Mutex
	last    map[string]int64
	sweptAt int64
}

func newEarlyQuitStamps() *earlyQuitStamps {
	return &earlyQuitStamps{last: make(map[string]int64)}
}

// earlyQuitStampRegistry is the process-wide registry; every sender goes through it.
var earlyQuitStampRegistry = newEarlyQuitStamps()

// Next returns the stamp for the next update to userID.
func (r *earlyQuitStamps) Next(userID string, now time.Time) int64 {
	r.mu.Lock()
	defer r.mu.Unlock()

	stamp := now.Unix()
	if prev, ok := r.last[userID]; ok && prev+1 > stamp {
		stamp = prev + 1
	}
	r.last[userID] = stamp

	// An entry older than the clock says nothing a fresh one would not: max(prev+1, now) is now.
	if now.Unix() >= r.sweptAt+earlyQuitStampSweepSecs {
		for id, v := range r.last {
			if v < now.Unix() {
				delete(r.last, id)
			}
		}
		r.sweptAt = now.Unix()
	}
	return stamp
}

// Len is the number of players the registry holds a stamp for.
func (r *earlyQuitStamps) Len() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.last)
}

// loginEarlyQuitFeatures is the earlyquit block of the profile sent at login. The client reads its
// penaltyts as the first stamp it holds, so it is a stamp from the same sequence as the updates, and
// the update sent right after the profile is newer. The lockout expiry is not in the profile: the
// client's login path passes none to the game's setter (it gets -1), so it comes in that update.
func loginEarlyQuitFeatures(userID string, state *EarlyQuitPlayerState, now time.Time) evr.EarlyQuitFeatures {
	return evr.EarlyQuitFeatures{
		PenaltyTimestamp:    earlyQuitStampRegistry.Next(userID, now),
		NumEarlyQuits:       int(state.NumEarlyQuits),
		NumSteadyMatches:    int(state.NumSteadyMatches),
		NumSteadyEarlyQuits: int(state.NumSteadyEarlyQuits),
		PenaltyLevel:        int(state.PenaltyLevel),
		SteadyPlayerLevel:   int(state.SteadyPlayerLevel),
	}
}
