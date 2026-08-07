package server

import (
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPerKeyLogThrottle_AllowsFirstThenSuppressesWithinWindow(t *testing.T) {
	th := newPerKeyLogThrottle(time.Minute)

	now := time.Now()
	th.setClock(func() time.Time { return now })

	assert.True(t, th.allow("guild-a"), "the first line for a key always passes")
	assert.False(t, th.allow("guild-a"), "a second line inside the window is suppressed")

	assert.True(t, th.allow("guild-b"), "throttling is per key, not global")

	now = now.Add(59 * time.Second)
	assert.False(t, th.allow("guild-a"), "still inside the window")

	now = now.Add(2 * time.Second)
	assert.True(t, th.allow("guild-a"), "the window elapsed, so the condition re-announces itself")
}

// reset is the API tests must use instead of reassigning a shared throttle var.
// It has to actually forget prior emissions, or a test that resets and then
// asserts "exactly one log line" would silently observe zero.
func TestPerKeyLogThrottle_ResetForgetsEmissionsAndRestoresTheRealClock(t *testing.T) {
	th := newPerKeyLogThrottle(time.Hour)

	frozen := time.Now()
	th.setClock(func() time.Time { return frozen })

	require.True(t, th.allow("guild-a"))
	require.False(t, th.allow("guild-a"))

	th.reset()

	assert.True(t, th.allow("guild-a"),
		"after reset the key must behave as if it had never emitted")

	th.mu.Lock()
	clockIsRestored := th.now != nil
	th.mu.Unlock()
	assert.True(t, clockIsRestored, "reset must leave a usable clock behind for the next test")
}

// Review follow-up (fragile global): the degraded-VPN throttle is a package-level
// var that production code reads on every lobby authorize. Tests used to swap it
// wholesale, which is an unsynchronized write to shared state — safe only for as
// long as nothing runs in parallel. Mutating through reset/setClock instead puts
// every write behind the throttle's own lock, so this stays clean under -race
// even with concurrent readers.
func TestPerKeyLogThrottle_MutationIsRaceSafeAgainstConcurrentReaders(t *testing.T) {
	th := newPerKeyLogThrottle(time.Millisecond)

	var wg sync.WaitGroup
	stop := make(chan struct{})

	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
					th.allow("guild-a")
				}
			}
		}()
	}

	for i := 0; i < 200; i++ {
		th.reset()
		th.setClock(time.Now)
	}

	close(stop)
	wg.Wait()
}
