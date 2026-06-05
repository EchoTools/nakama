package server

import (
	"testing"
	"time"

	"github.com/gofrs/uuid/v5"
	"github.com/heroiclabs/nakama-common/api"
	"github.com/heroiclabs/nakama/v3/server/evr"
)

// newTestGuildGroup builds a GuildGroup with a populated role cache so the
// role-based helpers (IsMember/IsSuspended/IsAccountAgeBypass/IsVPNBypass)
// resolve in a unit test without a backing store.
func newEnfTestGuildGroup(groupID string, md GroupMetadata, roleCache map[string]map[string]bool) *GuildGroup {
	if roleCache == nil {
		roleCache = map[string]map[string]bool{}
	}
	return &GuildGroup{
		GroupMetadata: md,
		State:         &GuildGroupState{GroupID: groupID, RoleCache: roleCache},
		Group:         &api.Group{Id: groupID, Name: "Test Guild"},
	}
}

func newEnfTestParams() *SessionParameters {
	return &SessionParameters{}
}

// TestEvaluateEntrantEnforcement_FullSet drives the SHARED full-enforcement
// decision (evaluateEntrantEnforcement) across every dimension that lobbyAuthorize
// checks. This is the function the chokepoint (LobbyJoinEntrants) now runs for
// every entrant against the BUILT match's group, so request-path and placement-
// path cannot diverge.
//
// Against pristine /srv/src/nakama this function does not exist (RED: compile
// failure); with the fix it must reject for each enforcement dimension and allow
// a clean user.
func TestEvaluateEntrantEnforcement_FullSet(t *testing.T) {
	groupID := uuid.Must(uuid.NewV4()).String()
	userID := uuid.Must(uuid.NewV4()).String()
	mode := evr.ModeArenaPublic

	t.Run("clean_user_allowed", func(t *testing.T) {
		gg := newEnfTestGuildGroup(groupID, GroupMetadata{}, nil)
		d := evaluateEntrantEnforcement(gg, newEnfTestParams(), ActiveGuildEnforcements{}, groupID, mode, userID, "")
		if d.rejected {
			t.Fatalf("clean user wrongly rejected: %+v", d)
		}
	})

	t.Run("private_guild_nonmember_rejected", func(t *testing.T) {
		gg := newEnfTestGuildGroup(groupID, GroupMetadata{
			EnableMembersOnlyMatchmaking: true,
			RoleMap:                      GuildGroupRoles{Member: "member-role"},
		}, nil) // empty cache => not a member
		d := evaluateEntrantEnforcement(gg, newEnfTestParams(), ActiveGuildEnforcements{}, groupID, mode, userID, "")
		if !d.rejected || d.metricTag != "not_member" {
			t.Fatalf("non-member of private guild should be rejected: %+v", d)
		}
	})

	t.Run("private_guild_member_allowed", func(t *testing.T) {
		gg := newEnfTestGuildGroup(groupID, GroupMetadata{
			EnableMembersOnlyMatchmaking: true,
			RoleMap:                      GuildGroupRoles{Member: "member-role"},
		}, map[string]map[string]bool{"member-role": {userID: true}})
		d := evaluateEntrantEnforcement(gg, newEnfTestParams(), ActiveGuildEnforcements{}, groupID, mode, userID, "")
		if d.rejected {
			t.Fatalf("member of private guild should be allowed: %+v", d)
		}
	})

	t.Run("role_suspended_rejected", func(t *testing.T) {
		gg := newEnfTestGuildGroup(groupID, GroupMetadata{
			RoleMap: GuildGroupRoles{Suspended: "suspended-role"},
		}, map[string]map[string]bool{"suspended-role": {userID: true}})
		d := evaluateEntrantEnforcement(gg, newEnfTestParams(), ActiveGuildEnforcements{}, groupID, mode, userID, "")
		if !d.rejected || d.metricTag != "suspended_user" {
			t.Fatalf("role-suspended user should be rejected: %+v", d)
		}
	})

	t.Run("journal_suspended_rejected", func(t *testing.T) {
		gg := newEnfTestGuildGroup(groupID, GroupMetadata{}, nil)
		journal := NewGuildEnforcementJournal(userID)
		journal.AddRecord(groupID, "mod", "moddisc", "banned", "notes", false, false, time.Hour)
		enf, err := CheckEnforcementSuspensions(GuildEnforcementJournalList{userID: journal}, map[string][]string{groupID: {}})
		if err != nil {
			t.Fatalf("CheckEnforcementSuspensions: %v", err)
		}
		d := evaluateEntrantEnforcement(gg, newEnfTestParams(), enf, groupID, mode, userID, "")
		if !d.rejected || d.metricTag != "suspended_user" {
			t.Fatalf("journal-suspended user should be rejected: %+v", d)
		}
	})

	t.Run("account_age_too_new_rejected", func(t *testing.T) {
		gg := newEnfTestGuildGroup(groupID, GroupMetadata{MinimumAccountAgeDays: 3650}, nil)
		// A Discord snowflake whose embedded timestamp is recent (now).
		recentSnowflake := snowflakeForTime(time.Now())
		d := evaluateEntrantEnforcement(gg, newEnfTestParams(), ActiveGuildEnforcements{}, groupID, mode, userID, recentSnowflake)
		if !d.rejected || d.metricTag != "account_age" {
			t.Fatalf("too-new account should be rejected: %+v", d)
		}
	})

	t.Run("feature_not_allowed_rejected", func(t *testing.T) {
		gg := newEnfTestGuildGroup(groupID, GroupMetadata{AllowedFeatures: []string{"vr"}}, nil)
		p := newEnfTestParams()
		p.supportedFeatures = []string{"vr", "cheatmod"}
		d := evaluateEntrantEnforcement(gg, p, ActiveGuildEnforcements{}, groupID, mode, userID, "")
		if !d.rejected || d.metricTag != "feature_not_allowed" {
			t.Fatalf("disallowed feature should be rejected: %+v", d)
		}
	})

	t.Run("suspended_alt_rejected_when_guild_rejects_alts", func(t *testing.T) {
		altUserID := uuid.Must(uuid.NewV4()).String()
		gg := newEnfTestGuildGroup(groupID, GroupMetadata{RejectPlayersWithSuspendedAlternates: true}, nil)
		// Suspension belongs to the alt (different userID), guild rejects alts.
		journal := NewGuildEnforcementJournal(altUserID)
		journal.AddRecord(groupID, "mod", "moddisc", "alt banned", "notes", false, false, time.Hour)
		enf, err := CheckEnforcementSuspensions(GuildEnforcementJournalList{altUserID: journal}, map[string][]string{groupID: {}})
		if err != nil {
			t.Fatalf("CheckEnforcementSuspensions: %v", err)
		}
		d := evaluateEntrantEnforcement(gg, newEnfTestParams(), enf, groupID, mode, userID, "")
		if !d.rejected || d.metricTag != "suspended_alt_user" {
			t.Fatalf("suspended alt should be rejected when guild rejects alts: %+v", d)
		}
	})
}

// snowflakeForTime builds a Discord snowflake string whose embedded creation
// timestamp equals t. Discord epoch is 2015-01-01T00:00:00Z (1420070400000ms).
func snowflakeForTime(t time.Time) string {
	const discordEpochMs = 1420070400000
	ms := t.UnixMilli() - discordEpochMs
	if ms < 0 {
		ms = 0
	}
	id := uint64(ms) << 22
	return uintToString(id)
}

func uintToString(v uint64) string {
	if v == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for v > 0 {
		i--
		buf[i] = byte('0' + v%10)
		v /= 10
	}
	return string(buf[i:])
}
