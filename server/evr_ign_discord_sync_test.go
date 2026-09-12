package server

import "testing"

// EchoTools/nakama#586: an explicit /ign override was silently replaced by the
// player's Discord nickname. /ign sets IsOverride unconditionally but IsLocked
// only when the modal's lock box is checked
// (server/evr_discord_appbot_igp.go:887), so the common case — an override
// without the lock — was clobbered.
//
// The intent these tests pin is already written down in the codebase, at
// server/evr_runtime_rpc.go:2322: "Mark as an explicit override so
// Discord/member sync will not clobber it."

func TestGroupInGameNameIsProtectedFromDiscordSync(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name string
		ign  GroupInGameName
		want bool
	}{
		{
			// The #586 case. This is what /ign writes when the lock box is
			// left unchecked, and it must be shielded from Discord.
			name: "unlocked override is protected",
			ign:  GroupInGameName{DisplayName: "Kestrel", IsOverride: true},
			want: true,
		},
		{
			name: "locked override is protected",
			ign:  GroupInGameName{DisplayName: "Kestrel", IsOverride: true, IsLocked: true},
			want: true,
		},
		{
			name: "locked non-override is protected",
			ign:  GroupInGameName{DisplayName: "Kestrel", IsLocked: true},
			want: true,
		},
		{
			// An ordinary Discord-derived name. Discord stays authoritative
			// over it; this is the whole point of the resync.
			name: "plain name is not protected",
			ign:  GroupInGameName{DisplayName: "Kestrel"},
			want: false,
		},
		{
			// An override only shields a name that exists. Refusing the
			// refresh for an override carrying no DisplayName would strand the
			// player with no in-game name at all rather than protect anything.
			name: "override with no display name is not protected",
			ign:  GroupInGameName{IsOverride: true},
			want: false,
		},
		{
			// Locked-with-no-name keeps its pre-#586 meaning: a lock is an
			// explicit administrative freeze and outranks the empty-name
			// rescue. Unchanged by this fix, asserted so it stays that way.
			name: "locked with no display name is still protected",
			ign:  GroupInGameName{IsLocked: true},
			want: true,
		},
		{
			// EchoTools/nakama#602. "Has a name" is decided by
			// sanitizeDisplayName, which is what every consumer reads through.
			// "12345" carries no ASCII letter, sanitizes to "", and so shields
			// nothing — protecting it stranded the player with no IGN.
			name: "override whose name sanitizes to empty is not protected",
			ign:  GroupInGameName{DisplayName: "12345", IsOverride: true},
			want: false,
		},
		{
			name: "override of punctuation only is not protected",
			ign:  GroupInGameName{DisplayName: "!!!", IsOverride: true},
			want: false,
		},
		{
			// The transliterating case, and the reason the source-side check is
			// sanitizeDisplayName and not a raw-bytes regexp: this IS a name,
			// it renders as "Kirill", and it stays protected.
			name: "override that transliterates to letters is protected",
			ign:  GroupInGameName{DisplayName: "Кирилл", IsOverride: true},
			want: true,
		},
		{
			// The IsLocked short-circuit is untouched by #602: an
			// administrative freeze outranks the sanitization rescue exactly as
			// it outranks the empty-name one above.
			name: "locked name that sanitizes to empty is still protected",
			ign:  GroupInGameName{DisplayName: "12345", IsLocked: true},
			want: true,
		},
		{
			name: "zero value is not protected",
			ign:  GroupInGameName{},
			want: false,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := tc.ign.IsProtectedFromDiscordSync(); got != tc.want {
				t.Errorf("IsProtectedFromDiscordSync() = %v, want %v (ign=%+v)", got, tc.want, tc.ign)
			}
		})
	}
}

// TestShouldRefreshIGNFromDiscord covers the login-site decision in
// initializeSession (server/evr_pipeline_login.go). This predicate is the sole
// determinant of whether the Discord nickname overwrites groupIGN.DisplayName
// before the loop writes it back with SetGroupIGNData, so "must keep the
// override in params.profile" is exactly "must not refresh from Discord".
//
// initializeSession itself is not reachable from a unit test — it needs a live
// session, pipeline, storage round-trips and a Discord client — which is why
// the decision is tested here rather than through the loop.
func TestShouldRefreshIGNFromDiscord(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name          string
		ign           GroupInGameName
		isActiveGroup bool
		want          bool
	}{
		{
			// The reported defect: the enforcer's active guild. Her IGN was
			// overwritten by her Discord nickname on every login, which is why
			// her own tablet view and the end-of-match award board disagreed
			// with what everyone else saw in the lobby.
			name:          "unlocked override in the active group is kept",
			ign:           GroupInGameName{DisplayName: "Kestrel", IsOverride: true},
			isActiveGroup: true,
			want:          false,
		},
		{
			name:          "unlocked override in a non-active group is kept",
			ign:           GroupInGameName{DisplayName: "Kestrel", IsOverride: true},
			isActiveGroup: false,
			want:          false,
		},
		{
			// The issue's own falsifier: with the lock box checked, IsLocked
			// short-circuits and the override was never at risk.
			name:          "locked override in the active group is kept",
			ign:           GroupInGameName{DisplayName: "Kestrel", IsOverride: true, IsLocked: true},
			isActiveGroup: true,
			want:          false,
		},
		{
			// Unchanged behaviour: Discord remains authoritative for a name it
			// supplied, in the guild the player is currently playing under.
			name:          "plain name in the active group refreshes",
			ign:           GroupInGameName{DisplayName: "Kestrel"},
			isActiveGroup: true,
			want:          true,
		},
		{
			// Unchanged behaviour: non-active guilds are not resynced.
			name:          "plain name in a non-active group is left alone",
			ign:           GroupInGameName{DisplayName: "Kestrel"},
			isActiveGroup: false,
			want:          false,
		},
		{
			// Unchanged behaviour: a group with no name is filled from
			// Discord no matter which group it is.
			name:          "empty name in a non-active group refreshes",
			ign:           GroupInGameName{},
			isActiveGroup: false,
			want:          true,
		},
		{
			// The hole in the one-line fix. Gating the whole block on
			// !IsOverride would skip the empty-name rescue for these records
			// and leave the player with no in-game name.
			name:          "override with no display name still refreshes in the active group",
			ign:           GroupInGameName{IsOverride: true},
			isActiveGroup: true,
			want:          true,
		},
		{
			name:          "override with no display name still refreshes in a non-active group",
			ign:           GroupInGameName{IsOverride: true},
			isActiveGroup: false,
			want:          true,
		},
		{
			// EchoTools/nakama#602, the reported reproduction: `?ign=12345`
			// stored an override that sanitizes to "", the refresh was skipped,
			// and GetGroupIGN(active) resolved to "".
			name:          "override whose name sanitizes to empty refreshes in the active group",
			ign:           GroupInGameName{DisplayName: "12345", IsOverride: true},
			isActiveGroup: true,
			want:          true,
		},
		{
			// Characterization, NOT endorsement. #602 fixes the predicate, so
			// this record is no longer "protected" — but the second clause
			// (`ign.DisplayName == ""`) still tests the raw string, and it is
			// byte-identical at v3.27.2-evr.322
			// (2b5f45bbe:server/evr_pipeline_login.go:926). A non-active group
			// holding an unrenderable name is stranded exactly as it was before
			// #586: pre-existing, not a regression, so not fixed here. See
			// TestGetGroupIGNStrandsAnUnrenderableNameInANonActiveGroup.
			name:          "override whose name sanitizes to empty is still not refreshed in a non-active group",
			ign:           GroupInGameName{DisplayName: "!!!", IsOverride: true},
			isActiveGroup: false,
			want:          false,
		},
		{
			// Still shielded — a locked name is frozen whether or not it
			// renders, and #602 does not relax that.
			name:          "locked name that sanitizes to empty is still kept",
			ign:           GroupInGameName{DisplayName: "12345", IsLocked: true},
			isActiveGroup: true,
			want:          false,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := shouldRefreshIGNFromDiscord(tc.ign, tc.isActiveGroup); got != tc.want {
				t.Errorf("shouldRefreshIGNFromDiscord(%+v, isActiveGroup=%v) = %v, want %v",
					tc.ign, tc.isActiveGroup, got, tc.want)
			}
		})
	}
}

// TestSetGroupDisplayNameClearsOverride pins why the syncMembersIGN guard has to
// hold. That function's write path is SetGroupDisplayName
// (server/evr_discord_integrator.go:1107), and it is persisted immediately by
// EVRProfileUpdate. Letting a protected name reach it does not merely shadow the
// override for one session — it erases the flag in storage, for everyone.
func TestSetGroupDisplayNameClearsOverride(t *testing.T) {
	t.Parallel()

	const groupID = "1c4c1b5d-6f9e-4b0a-9c3a-2f1d0e8a7b6c"

	profile := &EVRProfile{
		InGameNames: map[string]GroupInGameName{
			groupID: {GroupID: groupID, DisplayName: "Kestrel", IsOverride: true},
		},
	}

	if !profile.SetGroupDisplayName(groupID, "DiscordNick") {
		t.Fatal("SetGroupDisplayName reported no update, want an update")
	}

	got := profile.GetGroupIGNData(groupID)
	if got.IsOverride {
		t.Error("SetGroupDisplayName kept IsOverride; this test is stale and the guard rationale needs revisiting")
	}
	if got.DisplayName != "DiscordNick" {
		t.Errorf("DisplayName = %q, want %q", got.DisplayName, "DiscordNick")
	}
}
