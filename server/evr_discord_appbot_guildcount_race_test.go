package server

import (
	"fmt"
	"testing"

	"github.com/bwmarrin/discordgo"
)

// The `guild_count` reads in evr_discord_appbot.go (the Ready handler's "Bot
// ready" line, and RegisterSlashCommands' "Slash commands registered/updated"
// line) took len(dg.State.Guilds) with no lock held.
//
// discordgo mutates State from the gateway goroutine under State.Lock():
// GuildAdd (state.go:89-95 @ v0.29.0) appends to State.Guilds (:146). Both
// reads run on a handler goroutine instead — discordgo dispatches handlers with
// `go eh.eventHandler.Handle(s, i)` unless Session.SyncEvents is set
// (event.go:171, :180), and this repo never sets it. For a bot over the
// large-guild threshold the gateway delivers GUILD_CREATE for every guild AFTER
// READY, so the reader and the appender overlap during ordinary startup.
//
// This test fails under -race if stateGuildCount is ever changed back to an
// unlocked len(state.Guilds). It says nothing useful without -race: a data race
// is not an assertion failure, and the count it returns is allowed to be any
// value the gateway has produced so far.

// TestStateGuildCount_SafeAgainstGatewayGuildAdd runs the accessor against a
// gateway goroutine that is appending to State.Guilds the whole time.
func TestStateGuildCount_SafeAgainstGatewayGuildAdd(t *testing.T) {
	state := discordgo.NewState()

	stop := make(chan struct{})
	gatewayDone := make(chan struct{})
	// started is closed after the gateway's first append. Without this barrier
	// the reader loop can run to completion before the writer goroutine is ever
	// scheduled, and -race reports nothing because the two accesses never
	// overlap — a green run that proves nothing. Measured: an earlier version of
	// this test passed under -race against a deliberately UNLOCKED accessor.
	started := make(chan struct{})

	go func() {
		defer close(gatewayDone)
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			default:
			}
			// A fresh ID appends to State.Guilds; a repeat ID overwrites an
			// existing *Guild in place. Both happen during a GUILD_CREATE burst.
			id := fmt.Sprintf("guild-%d", i%64)
			_ = state.GuildAdd(&discordgo.Guild{ID: id, Name: id, OwnerID: "owner"})
			if i == 0 {
				close(started)
			}
		}
	}()

	<-started
	for i := 0; i < 200000; i++ {
		// The value is deliberately unchecked: any count the gateway has
		// reached is legitimate. What is under test is that reading it does not
		// race the append.
		_ = stateGuildCount(state)
	}

	close(stop)
	<-gatewayDone
}

// TestStateGuildCount_NilAndEmpty pins the degenerate cases, so the accessor can
// be called before the first READY without the caller guarding it.
func TestStateGuildCount_NilAndEmpty(t *testing.T) {
	if got := stateGuildCount(nil); got != 0 {
		t.Errorf("nil state: got %d, want 0", got)
	}
	if got := stateGuildCount(discordgo.NewState()); got != 0 {
		t.Errorf("empty state: got %d, want 0", got)
	}
}

// TestStateGuildCount_CountsGuilds pins that the lock did not cost correctness.
func TestStateGuildCount_CountsGuilds(t *testing.T) {
	state := discordgo.NewState()
	for i := 0; i < 5; i++ {
		id := fmt.Sprintf("guild-%d", i)
		if err := state.GuildAdd(&discordgo.Guild{ID: id, Name: id, OwnerID: "owner"}); err != nil {
			t.Fatalf("GuildAdd: %v", err)
		}
	}
	if got := stateGuildCount(state); got != 5 {
		t.Errorf("got %d, want 5", got)
	}
}
