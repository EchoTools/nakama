package server

import (
	"context"
	"strings"
	"testing"

	"github.com/bwmarrin/discordgo"
)

// The command-registration paths read the bot's own application ID straight out
// of discordgo state as `dg.State.User.ID`, unlocked. #536 established why that
// is a data race and built botDiscordIDFromState for it; these call sites were
// not converted at the time.
//
// The accessor's race-safety is already witnessed by #536
// (TestBotDiscordIDFromState_ConcurrentWithReady). What is pinned here is the
// consequence of adopting it: the accessor returns "" where the old expression
// panicked, so every caller now needs to decide what "" means. Sending it on to
// Discord as an application ID is not an option — it is a well-formed call
// against the wrong resource.

// unreadyDiscordSession is a session whose State has not seen a READY yet, so
// State.User is nil. The old `dg.State.User.ID` expression panicked on this.
func unreadyDiscordSession() *discordgo.Session {
	return &discordgo.Session{State: discordgo.NewState()}
}

// TestUnregisterCommands_RefusesWithoutBotID pins that a pre-READY state stops
// the unregister sweep instead of asking Discord to list commands for
// application "".
func TestUnregisterCommands_RefusesWithoutBotID(t *testing.T) {
	logger := newCaptureLogger()
	d := &DiscordAppBot{}

	// Must not panic, and must not reach the network: a nil HTTP client in the
	// session would fault if ApplicationCommands were actually called.
	d.UnregisterCommands(context.Background(), logger, unreadyDiscordSession(), "guild-1")

	if _, found := logger.find("error", "Cannot unregister commands: bot user ID not in state yet"); !found {
		t.Error("pre-READY unregister did not report why it stopped; it must not silently no-op")
	}
}

// TestRegisterDiscordCommands_RefusesWithoutBotID pins the same for the
// reservation integration, which returns an error rather than logging.
func TestRegisterDiscordCommands_RefusesWithoutBotID(t *testing.T) {
	ri := &ReservationIntegration{}

	err := ri.RegisterDiscordCommands(unreadyDiscordSession())
	if err == nil {
		t.Fatal("pre-READY registration returned nil; it would have created the command against application \"\"")
	}
	if !strings.Contains(err.Error(), "bot user ID not in state yet") {
		t.Errorf("error does not say why registration was refused: %v", err)
	}
}
