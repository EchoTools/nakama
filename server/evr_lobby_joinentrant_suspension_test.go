package server

import (
	"testing"
	"time"

	"github.com/gofrs/uuid/v5"
	"github.com/heroiclabs/nakama/v3/server/evr"
)

// TestEntrantSuspension_PartyFollowBypass_RejectsBuiltGroup is the RED regression
// test for the party-follow suspension bypass.
//
// Confirmed prod vector: a player with an ACTIVE LIFETIME guild suspension for
// guild X party-follows a leader. The follower AUTHORIZES into the leader's
// guild (where the follower is NOT suspended) via lobbyAuthorize, but the
// matchmaker then BUILDS / places him into a guild-X match. The entrant-join
// path (LobbyJoinEntrants) never re-checks suspension against the BUILT match's
// actual groupID, so the suspended player is admitted to guild X.
//
// The fix factors the suspension decision into entrantSuspensionRejection and
// runs it against the BUILT match's groupID + mode at entrant-join time. This
// test drives that shared gate directly with the prod scenario.
//
// NOTE: SUSPENDED (per-guild GuildEnforcementJournal) — NOT DISABLED
// (account-level ban). The two are distinct enforcement classes.
func TestEntrantSuspension_PartyFollowBypass_RejectsBuiltGroup(t *testing.T) {
	guildX := uuid.Must(uuid.NewV4()).String() // EVRL — follower is suspended here
	userID := uuid.Must(uuid.NewV4()).String()
	mode := evr.ModeArenaPublic // public echo_arena, like the prod matches

	// Active LIFETIME suspension for guild X (self, not an alt).
	journal := NewGuildEnforcementJournal(userID)
	journal.AddRecord(
		guildX,
		"moderator-1",
		"moderator-discord-1",
		"Lifetime ban from EVRL",
		"cheating",
		false,                        // requireCommunityValues
		false,                        // allowPrivateLobbies (i.e. suspension covers public lobbies)
		LifetimeSuspensionDuration,   // lifetime
	)

	// This is the enforcement map the player carries in their session params,
	// computed at login/authorize against ALL groups via inheritance. It already
	// contains guild X regardless of which guild the follower authorized into.
	enforcements, err := CheckEnforcementSuspensions(
		GuildEnforcementJournalList{userID: journal},
		map[string][]string{guildX: {}},
	)
	if err != nil {
		t.Fatalf("CheckEnforcementSuspensions: %v", err)
	}

	// Sanity: the suspension is present in the map for guild X / this mode.
	if _, ok := enforcements[guildX][mode]; !ok {
		t.Fatalf("test setup: expected active suspension for guild X mode %s", mode)
	}

	// The BUILT match is in guild X. The entrant-join gate MUST reject.
	record, rejected := entrantSuspensionRejection(enforcements, guildX, mode, userID, false, false)
	if !rejected {
		t.Fatalf("BUG: suspended player admitted to built guild-X match via entrant path (no suspension check). record=%+v", record)
	}
	if record.UserID != userID {
		t.Errorf("expected rejection record for the suspended user %s, got %s", userID, record.UserID)
	}
	if record.Expiry.IsZero() {
		t.Errorf("expected a non-zero suspension expiry on the rejection record")
	}
}

// TestEntrantSuspension_NotSuspendedInBuiltGroup_Allowed confirms the gate does
// NOT reject a player who has no suspension for the built match's group — i.e.
// the fix does not over-block. A suspension in guild Y must not bleed into a
// guild Z match.
func TestEntrantSuspension_NotSuspendedInBuiltGroup_Allowed(t *testing.T) {
	guildY := uuid.Must(uuid.NewV4()).String() // suspended here
	guildZ := uuid.Must(uuid.NewV4()).String() // built match is here — clean
	userID := uuid.Must(uuid.NewV4()).String()
	mode := evr.ModeArenaPublic

	journal := NewGuildEnforcementJournal(userID)
	journal.AddRecord(guildY, "mod", "moddisc", "ban", "notes", false, false, time.Hour)

	enforcements, err := CheckEnforcementSuspensions(
		GuildEnforcementJournalList{userID: journal},
		map[string][]string{guildY: {}},
	)
	if err != nil {
		t.Fatalf("CheckEnforcementSuspensions: %v", err)
	}

	_, rejected := entrantSuspensionRejection(enforcements, guildZ, mode, userID, false, false)
	if rejected {
		t.Fatalf("over-block: player with no suspension in built guild Z was rejected")
	}
}

// TestEntrantSuspension_ExpiredSuspension_Allowed confirms an expired suspension
// does not reject (CheckEnforcementSuspensions already drops expired records, so
// the built group has no entry).
func TestEntrantSuspension_ExpiredSuspension_Allowed(t *testing.T) {
	guildX := uuid.Must(uuid.NewV4()).String()
	userID := uuid.Must(uuid.NewV4()).String()
	mode := evr.ModeArenaPublic

	journal := NewGuildEnforcementJournal(userID)
	// Negative duration -> already expired.
	journal.AddRecord(guildX, "mod", "moddisc", "old ban", "notes", false, false, -time.Hour)

	enforcements, err := CheckEnforcementSuspensions(
		GuildEnforcementJournalList{userID: journal},
		map[string][]string{guildX: {}},
	)
	if err != nil {
		t.Fatalf("CheckEnforcementSuspensions: %v", err)
	}

	_, rejected := entrantSuspensionRejection(enforcements, guildX, mode, userID, false, false)
	if rejected {
		t.Fatalf("expired suspension should not reject")
	}
}
