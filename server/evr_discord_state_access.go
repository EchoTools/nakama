package server

import "github.com/bwmarrin/discordgo"

// botDiscordIDFromState returns the bot's own Discord user ID, read under the
// discordgo state's read lock.
//
// discordgo.State embeds Ready (state.go:36-38 @ v0.29.0), so State.User is
// Ready.User — a *User that discordgo REPLACES wholesale on every gateway
// READY: State.onReady takes State.Lock() and assigns `s.Ready = *r`
// (state.go:911, :916, :935 @ v0.29.0), which writes the User pointer word
// itself, not just the pointee.
//
// guildSync reads the bot ID on the 15-minute prune-ticker goroutine
// (pruneGuildGroups -> reconcileOrphanGuilds -> guildSync), not the gateway
// goroutine, so an unlocked `d.dg.State.User.ID` is a plain data race against a
// gateway reconnect. A torn/stale read yields "" and turns every orphan guild
// into a prune-leave candidate.
//
// It also removes a nil dereference: before the first READY, State.User is nil
// and the unlocked expression `State.User.ID` panicked. Returning "" instead
// lets guildSync fail with its existing "failed to get bot user ID from state"
// error, which the prune path already handles as "guild stays an orphan
// candidate" rather than crashing the integrator goroutine.
//
// The lock is released before the caller's DiscordIDToUserID lookup, so the
// discordgo state lock is never held across a database call.
func botDiscordIDFromState(state *discordgo.State) string {
	if state == nil {
		return ""
	}
	state.RLock()
	defer state.RUnlock()
	if state.User == nil {
		return ""
	}
	return state.User.ID
}
