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

// stateGuildCount returns the number of guilds the bot is in, read under the
// discordgo state's read lock.
//
// The count is only ever logged, so the stakes look low — but the read is a
// plain data race and the writer is the busiest one discordgo has. GuildAdd
// takes State.Lock() and APPENDS to State.Guilds (state.go:89-95, :146 @
// v0.29.0), and for a bot over the large-guild threshold the gateway delivers
// GUILD_CREATE for every guild AFTER READY. So the window is not an exotic
// interleaving; it is startup.
//
// Both callers run on a handler goroutine rather than the gateway goroutine:
// discordgo dispatches handlers with `go eh.eventHandler.Handle(s, i)` unless
// Session.SyncEvents is set (event.go:171, :180 @ v0.29.0), and this repo never
// sets it.
//
// An unlocked len() of a slice being appended to reads a torn slice header. On
// amd64 the realistic outcome is a wrong number in one log line rather than a
// crash, which is precisely why it survived: nothing downstream notices, and
// -race is the only thing that ever complains.
func stateGuildCount(state *discordgo.State) int {
	if state == nil {
		return 0
	}
	state.RLock()
	defer state.RUnlock()
	return len(state.Guilds)
}
