package server

import (
	"testing"

	"github.com/bwmarrin/discordgo"
)

// EchoTools/nakama#602, the regression half.
//
// `?ign=` is parsed with pattern nil (server/session_ws.go:200), truncated to 20
// bytes and stored verbatim with IsOverride: true for every guild group
// (server/evr_pipeline_login.go:936-939). IsProtectedFromDiscordSync then tests
// the RAW stored string for emptiness, but every consumer renders through
// sanitizeDisplayName, which returns "" for anything carrying no ASCII letter
// after transliteration. So `?ign=12345` is "protected", the login-time Discord
// refresh is skipped, and the player is left with no in-game name at all.
//
// What makes this a regression rather than a long-standing hole is #586. At
// v3.27.2-evr.322 the refresh was gated on IsLocked alone (`if
// !groupIGN.IsLocked` @ 2b5f45bbe: server/evr_pipeline_login.go:925), so an
// override that renders as nothing was overwritten by the Discord nickname
// instead of sticking. #586 added the IsOverride shield — correctly — but asked
// it of the raw string.
//
// `?ign=` itself accepted these values at .322 too, byte for byte; validating it
// would be a behaviour change, not a regression fix, and is deliberately not
// done here. #587 covers the unsanitized write.

// ignRecordThatSanitizesEmpty is the exact record the issue reproduces with: an
// unlocked override whose stored name carries no ASCII letter.
func ignRecordThatSanitizesEmpty(groupID string) GroupInGameName {
	return GroupInGameName{
		GroupID:     groupID,
		DisplayName: "12345",
		IsOverride:  true,
		IsLocked:    false,
	}
}

// TestIGNOverrideDoesNotStrandEmptyDisplayName is the #602 reproduction.
//
// Part 1 is the predicate: a stored name that sanitizes to empty protects
// nothing, so it must not suppress the Discord refresh. Part 2 is the
// consequence: with the refresh suppressed the record survives login unchanged
// and the player's active-group IGN resolves to "".
//
// Part 3 replays the two statements the login loop runs when the refresh is
// allowed (server/evr_pipeline_login.go:952-966 — GuildMember, then
// `groupIGN.DisplayName = memberNick`). initializeSession itself needs a live
// session, pipeline, storage and Discord client, so the rescue is asserted on
// its operands rather than through the loop; the same limitation is already
// recorded on TestShouldRefreshIGNFromDiscord.
func TestIGNOverrideDoesNotStrandEmptyDisplayName(t *testing.T) {
	t.Parallel()

	const groupID = "1c4c1b5d-6f9e-4b0a-9c3a-2f1d0e8a7b6c"

	rec := ignRecordThatSanitizesEmpty(groupID)

	// Part 1 — the refresh must not be suppressed.
	if got := rec.IsProtectedFromDiscordSync(); got {
		t.Errorf("IsProtectedFromDiscordSync(%+v) = true, want false: a name that sanitizes to %q protects nothing", rec, sanitizeDisplayName(rec.DisplayName))
	}
	if got := shouldRefreshIGNFromDiscord(rec, true); !got {
		t.Errorf("shouldRefreshIGNFromDiscord(%+v, isActiveGroup=true) = false, want true", rec)
	}

	// Part 2 — the symptom, asserted rather than logged: with the refresh
	// suppressed the record is what the player is left holding, and it renders
	// as nothing. This is unaffected by the predicate fix (the predicate decides
	// only whether login refreshes), so it holds before and after and is what
	// makes Part 3 meaningful.
	profile := &EVRProfile{
		ActiveGroupID: groupID,
		InGameNames:   map[string]GroupInGameName{groupID: rec},
	}
	if got := profile.GetActiveGroupDisplayName(); got != "" {
		t.Fatalf("GetActiveGroupDisplayName() = %q, want \"\": this test reproduces the stranded state, and it is no longer reproducing it", got)
	}

	// Part 3 — the rescue, once the refresh is allowed to run.
	member := &discordgo.Member{Nick: "Kestrel", User: &discordgo.User{ID: "1", Username: "kestrel"}}
	if memberNick := InGameName(member); memberNick != "" {
		rec.DisplayName = memberNick
	}
	profile.SetGroupIGNData(groupID, rec)
	if got := profile.GetActiveGroupDisplayName(); got == "" {
		t.Error("after the Discord refresh the active-group IGN is still empty")
	}
}

// TestGetGroupIGNStrandsAnUnrenderableNameInANonActiveGroup is a
// CHARACTERIZATION test. It asserts what the code does today, not what it ought
// to do, and it must not be read as endorsement.
//
// The #602 fix restores the rescue for the ACTIVE group only, because that is
// the whole of the regression: shouldRefreshIGNFromDiscord's second clause
// (`isActiveGroup || ign.DisplayName == ""`) and GetGroupIGN's first rung both
// test the raw stored string, and both are byte-identical at
// v3.27.2-evr.322 — `2b5f45bbe:server/evr_pipeline_login.go:926` and
// `2b5f45bbe:server/evr_account.go:276-296`. So a NON-active group holding a
// name that sanitizes to empty is stranded exactly as it was before #586:
// never refreshed by any login, and GetGroupIGN short-circuits on the
// raw-non-empty value and returns "" rather than falling through to the active
// group or the username.
//
// That is a real defect and a live feed for the evr_lobby_joinentrant.go:746
// write loop below, but it is pre-existing, not a regression, so it is pinned
// here and filed rather than fixed in a regression-only release.
func TestGetGroupIGNStrandsAnUnrenderableNameInANonActiveGroup(t *testing.T) {
	t.Parallel()

	const (
		activeGroupID = "1c4c1b5d-6f9e-4b0a-9c3a-2f1d0e8a7b6c"
		otherGroupID  = "2d5d2c6e-7a0f-4c1b-8d4b-3a2e1f9b8c7d"
	)

	profile := &EVRProfile{
		ActiveGroupID: activeGroupID,
		InGameNames: map[string]GroupInGameName{
			activeGroupID: {GroupID: activeGroupID, DisplayName: "Kestrel"},
			otherGroupID:  ignRecordThatSanitizesEmpty(otherGroupID),
		},
	}

	rec := profile.GetGroupIGNData(otherGroupID)

	// The predicate is fixed, so the record is no longer "protected"...
	if rec.IsProtectedFromDiscordSync() {
		t.Fatalf("IsProtectedFromDiscordSync(%+v) = true; the #602 fix has regressed", rec)
	}
	// ...but the refresh still will not run for a non-active group, because the
	// emptiness test there is on the raw string. Unchanged since .322.
	if shouldRefreshIGNFromDiscord(rec, false) {
		t.Errorf("shouldRefreshIGNFromDiscord(%+v, isActiveGroup=false) = true; behaviour changed — if this is deliberate, the strand below is fixed and this test should go", rec)
	}
	// ...and so it renders as nothing, rather than borrowing the active group's
	// name the fallback chain exists to supply.
	if got := profile.GetGroupIGN(otherGroupID); got != "" {
		t.Errorf("GetGroupIGN(otherGroup) = %q, want %q (characterization); the pre-existing strand is fixed and this test should go", got, "")
	}

	// The active group, which #602 does fix, is unaffected.
	if got := profile.GetGroupIGN(activeGroupID); got != "Kestrel" {
		t.Errorf("GetGroupIGN(activeGroup) = %q, want %q", got, "Kestrel")
	}
}

// TestForceNickToIGNGuardNeverConverges pins the write loop.
//
// server/evr_lobby_joinentrant.go:746 runs the DisplayNameForceNickToIGN block
// on every lobby join, guarded by `if displayName != InGameName(member)`, where
// displayName is params.profile.GetGroupIGN(groupID) at :731. When the stranded
// record above makes that "" the guard cannot converge: the block's own write is
// GuildMemberNickname(displayName), and InGameName falls back Nick ->
// GlobalName -> Username (server/evr_discord_integrator.go:598-608), so for any
// member carrying a usable name the comparison is true forever — a
// GuildMemberNickname PATCH plus an AuditLogSendGuild post on every join, per
// player, against a per-guild Discord rate limit.
//
// The block itself is an inline goroutine holding p.discordCache.dg, so it is
// not reachable without a live Discord client. What is reachable, and what
// actually decides the loop, is the guard's right-hand operand: this asserts
// InGameName never yields "" for a member Discord can produce, which is exactly
// the condition under which `"" != InGameName(member)` never becomes false.
func TestForceNickToIGNGuardNeverConverges(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name   string
		member *discordgo.Member
	}{
		{
			name:   "guild nickname",
			member: &discordgo.Member{Nick: "Kestrel", User: &discordgo.User{ID: "1", Username: "kestrel"}},
		},
		{
			name:   "no nickname, global name",
			member: &discordgo.Member{User: &discordgo.User{ID: "2", Username: "kestrel", GlobalName: "Kestrel"}},
		},
		{
			name:   "no nickname, no global name, username only",
			member: &discordgo.Member{User: &discordgo.User{ID: "3", Username: "kestrel"}},
		},
		{
			// Transliterated, so it still resolves — the fallback chain is
			// wider than a raw ASCII test would suggest.
			name:   "non-ascii nickname",
			member: &discordgo.Member{Nick: "Кирилл", User: &discordgo.User{ID: "4", Username: "kirill"}},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			nick := InGameName(tc.member)
			if nick == "" {
				t.Fatalf("InGameName(%+v) = %q; this test's premise is stale", tc.member, nick)
			}

			// The guard at evr_lobby_joinentrant.go:746, with the stranded
			// display name. It writes `displayName` and then re-compares
			// against InGameName, so a write can never make it false.
			const strandedDisplayName = ""
			if strandedDisplayName == nick {
				t.Fatalf("guard converged unexpectedly: %q == %q", strandedDisplayName, nick)
			}

			// Replay the block's own write: GuildMemberNickname sets the guild
			// nick to displayName. Discord rejects an empty nick, but even
			// granting it, InGameName falls through to the username.
			after := &discordgo.Member{Nick: strandedDisplayName, User: tc.member.User}
			if InGameName(after) == strandedDisplayName {
				t.Fatalf("InGameName after the write = %q; the guard would converge and there is no loop", strandedDisplayName)
			}
		})
	}
}
