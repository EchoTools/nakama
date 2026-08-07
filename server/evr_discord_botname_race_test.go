package server

import (
	"fmt"
	"testing"

	"github.com/bwmarrin/discordgo"
)

// The bot's own username was read as `State.User.Username` with no lock held in
// nine places: the Ready handler's "Bot ready" line, and six reads in the login
// pipeline. State embeds Ready, so State.User is a *User that discordgo
// REPLACES wholesale on every gateway READY -- `s.Ready = *r` under
// State.Lock() (state.go:911/916/935 @ v0.29.0) writes the pointer word itself.
//
// The login-pipeline reads are the reason this matters more than the guild
// count: they run on a session goroutine once per player login, so the exposure
// is the life of the process rather than startup.

// readyWriter starts a goroutine performing exactly the write State.onReady
// performs, and returns a stop function. started is closed after the first
// write, so a reader can wait for the writer to actually be running: a reader
// loop that finishes before the writer is scheduled never overlaps it, and
// -race reports nothing at all. (That is not hypothetical -- an earlier
// concurrency test in this package passed against a deliberately unlocked
// accessor for exactly that reason.)
func readyWriter(state *discordgo.State) (stop func()) {
	stopCh := make(chan struct{})
	done := make(chan struct{})
	started := make(chan struct{})

	go func() {
		defer close(done)
		for i := 0; ; i++ {
			select {
			case <-stopCh:
				return
			default:
			}
			state.Lock()
			state.Ready = discordgo.Ready{User: &discordgo.User{
				ID:         fmt.Sprintf("bot-%d", i),
				Username:   fmt.Sprintf("EchoTools%d", i),
				GlobalName: fmt.Sprintf("Echo Tools %d", i),
			}}
			state.Unlock()
			if i == 0 {
				close(started)
			}
		}
	}()

	<-started
	return func() {
		close(stopCh)
		<-done
	}
}

// TestBotUsernameFromState_ConcurrentWithReady fails under -race if the
// accessor is ever changed back to an unlocked read.
func TestBotUsernameFromState_ConcurrentWithReady(t *testing.T) {
	state := discordgo.NewState()
	state.User = &discordgo.User{ID: "bot-0", Username: "EchoTools0"}
	stop := readyWriter(state)

	for i := 0; i < 20000; i++ {
		name, present := botUsernameFromState(state)
		if !present || name == "" {
			stop()
			t.Fatalf("read an absent/empty bot username at iteration %d; the login pipeline would fall back or skip verification", i)
		}
	}
	stop()
}

// TestBotDisplayNameFromState_ConcurrentWithReady covers the two-field
// accessor, whose fallback would otherwise be able to straddle a READY.
func TestBotDisplayNameFromState_ConcurrentWithReady(t *testing.T) {
	state := discordgo.NewState()
	state.User = &discordgo.User{ID: "bot-0", Username: "EchoTools0", GlobalName: "Echo Tools 0"}
	stop := readyWriter(state)

	for i := 0; i < 20000; i++ {
		if got := botDisplayNameFromState(state); got == "" {
			stop()
			t.Fatalf("read an empty display name at iteration %d", i)
		}
	}
	stop()
}

// TestBotUsernameFromState_PresentIsNotNonEmpty pins the distinction the login
// pipeline depends on. Its IP-verification block is entered on "State.User was
// present", not on "the username is non-empty", because every path inside that
// block returns an error -- skipping it lets the login through. Collapsing the
// two would turn a degenerate state into a fail-open.
func TestBotUsernameFromState_PresentIsNotNonEmpty(t *testing.T) {
	if _, present := botUsernameFromState(nil); present {
		t.Error("nil state reported present")
	}
	if _, present := botUsernameFromState(discordgo.NewState()); present {
		t.Error("pre-READY state (nil User) reported present")
	}

	state := discordgo.NewState()
	state.User = &discordgo.User{ID: "bot-1"} // present, but no username
	name, present := botUsernameFromState(state)
	if !present {
		t.Error("User present but reported absent; the login guard would skip IP verification")
	}
	if name != "" {
		t.Errorf("got username %q, want empty", name)
	}
}

// TestBotDisplayNameFromState_PrefersGlobalName pins the fallback order the
// Ready handler used to open-code.
func TestBotDisplayNameFromState_PrefersGlobalName(t *testing.T) {
	if got := botDisplayNameFromState(nil); got != "" {
		t.Errorf("nil state: got %q, want empty", got)
	}
	if got := botDisplayNameFromState(discordgo.NewState()); got != "" {
		t.Errorf("pre-READY: got %q, want empty", got)
	}

	state := discordgo.NewState()
	state.User = &discordgo.User{Username: "echotools", GlobalName: "Echo Tools"}
	if got := botDisplayNameFromState(state); got != "Echo Tools" {
		t.Errorf("got %q, want the global name", got)
	}

	state.User = &discordgo.User{Username: "echotools"}
	if got := botDisplayNameFromState(state); got != "echotools" {
		t.Errorf("got %q, want the username fallback", got)
	}
}
