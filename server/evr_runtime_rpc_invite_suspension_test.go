package server

import (
	"errors"
	"testing"
	"time"

	"github.com/gofrs/uuid/v5"
	"github.com/heroiclabs/nakama/v3/server/evr"
)

// suspend writes an active journal suspension for user in their guild.
func (w *inviteTestWorld) suspend(user string, allowPrivate bool, d time.Duration) {
	w.t.Helper()
	if _, ok := w.groups[user]; !ok {
		w.groups[user] = uuid.Must(uuid.NewV4())
	}
	j, ok := w.journals[user]
	if !ok {
		j = NewGuildEnforcementJournal(user)
		w.journals[user] = j
	}
	j.AddRecord(w.groups[user].String(), "enforcer", "enforcer-discord", "notice", "notes", false, allowPrivate, d)
}

func wantDenied(t *testing.T, err error, what string) {
	t.Helper()
	if err == nil {
		t.Fatalf("%s: want refusal denied, got none", what)
	}
	if got := inviteToken(t, err); got != "denied" {
		t.Fatalf("%s: token = %q, want denied", what, got)
	}
}

func TestInviteCreate_JournalSuspensionIsDenied(t *testing.T) {
	for _, exp := range []string{"arena", "combat", "social"} {
		t.Run(exp, func(t *testing.T) {
			w := newInviteWorld(t)
			w.suspend(userA, false, time.Hour)
			_, err := w.call(withInviteRecord("create", InviteCreateRPC), userA, map[string]any{"experience": exp})
			wantDenied(t, err, "suspended create")
			if len(w.allocs) != 0 {
				t.Errorf("allocations = %d, want none", len(w.allocs))
			}
			if n := w.nk.objectCount(InviteCodeCollection); n != 0 {
				t.Errorf("codes stored = %d, want none", n)
			}
		})
	}
}

func TestInviteCreate_SuspensionThatAllowsPrivateLobbiesMayCreate(t *testing.T) {
	w := newInviteWorld(t)
	w.suspend(userA, true, time.Hour)
	w.create(userA, map[string]any{"experience": "arena"})
	if len(w.allocs) != 1 {
		t.Fatalf("allocations = %d, want 1", len(w.allocs))
	}
}

func TestInviteCreate_OtherGuildOrExpiredSuspensionDoesNotRefuse(t *testing.T) {
	w := newInviteWorld(t)
	w.groups[userA] = uuid.Must(uuid.NewV4())
	j := NewGuildEnforcementJournal(userA)
	j.AddRecord(uuid.Must(uuid.NewV4()).String(), "enforcer", "e", "elsewhere", "", false, false, time.Hour)
	j.AddRecord(w.groups[userA].String(), "enforcer", "e", "lapsed", "", false, false, -time.Hour)
	w.journals[userA] = j
	w.create(userA, map[string]any{"experience": "arena"})
}

func TestInviteCreate_AltSuspensionFollowsTheGuildSetting(t *testing.T) {
	for _, tc := range []struct {
		name      string
		altsCount bool
		denied    bool
	}{{"alts count", true, true}, {"alts ignored", false, false}} {
		t.Run(tc.name, func(t *testing.T) {
			w := newInviteWorld(t)
			w.altsCount = tc.altsCount
			w.groups[userA] = uuid.Must(uuid.NewV4())
			w.suspend(userB, false, time.Hour) // userB's journal, its own group
			alt := NewGuildEnforcementJournal(userB)
			alt.AddRecord(w.groups[userA].String(), "enforcer", "e", "alt", "", false, false, time.Hour)
			w.journals[userB] = alt
			_, err := w.call(withInviteRecord("create", InviteCreateRPC), userA, map[string]any{"experience": "arena"})
			if tc.denied {
				wantDenied(t, err, "alt suspension")
				return
			}
			if err != nil {
				t.Fatalf("an ignored alt suspension refused the player: %v", err)
			}
		})
	}
}

func TestInviteCreate_SuspensionLookupFailureRefuses(t *testing.T) {
	w := newInviteWorld(t)
	w.groups[userA] = uuid.Must(uuid.NewV4())
	w.suspendErr = errors.New("storage down")
	_, err := w.call(withInviteRecord("create", InviteCreateRPC), userA, map[string]any{"experience": "arena"})
	if err == nil {
		t.Fatal("create succeeded although suspensions could not be read")
	}
	if len(w.allocs) != 0 {
		t.Errorf("allocations = %d, want none", len(w.allocs))
	}
}

func TestInviteRegions_JournalSuspensionIsDenied(t *testing.T) {
	w := newInviteWorld(t)
	w.regionSet = fiveRegions()
	w.suspend(userA, false, time.Hour)
	_, err := w.regions(userA, map[string]any{"experience": "arena"})
	wantDenied(t, err, "suspended regions with an experience")
	_, err = w.regions(userA, map[string]any{})
	wantDenied(t, err, "suspended regions with no experience")
	if w.regionCall != 0 {
		t.Error("the guild's servers were listed to a suspended caller")
	}
}

func TestInviteRegions_SuspensionThatAllowsPrivateLobbiesIsListed(t *testing.T) {
	w := newInviteWorld(t)
	w.regionSet = fiveRegions()
	w.suspend(userA, true, time.Hour)
	if _, err := w.regions(userA, map[string]any{"experience": "arena"}); err != nil {
		t.Fatalf("limited-access player refused: %v", err)
	}
}

func TestInviteSuspensionSet_Refuses(t *testing.T) {
	full := GuildEnforcementRecord{}
	limited := GuildEnforcementRecord{AllowPrivateLobbies: true}
	for _, tc := range []struct {
		name string
		set  inviteSuspensionSet
		mode evr.Symbol
		want bool
	}{
		{"none", inviteSuspensionSet{}, evr.ModeArenaPrivate, false},
		{"exact private", inviteSuspensionSet{evr.ModeArenaPrivate: full}, evr.ModeArenaPrivate, true},
		{"public counterpart only", inviteSuspensionSet{evr.ModeArenaPublic: full}, evr.ModeArenaPrivate, true},
		{"public counterpart, private allowed", inviteSuspensionSet{evr.ModeArenaPublic: limited}, evr.ModeArenaPrivate, false},
		{"combat counterpart", inviteSuspensionSet{evr.ModeCombatPublic: full}, evr.ModeCombatPrivate, true},
		{"social counterpart", inviteSuspensionSet{evr.ModeSocialPublic: full}, evr.ModeSocialPrivate, true},
		{"other experience", inviteSuspensionSet{evr.ModeCombatPublic: full}, evr.ModeArenaPrivate, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.set.refuses(tc.mode); got != tc.want {
				t.Errorf("refuses = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestDefaultInviteSuspensions_FailsClosedWithoutTheBot(t *testing.T) {
	prev := globalAppBot.Load()
	globalAppBot.Store(nil)
	t.Cleanup(func() { globalAppBot.Store(prev) })
	if _, err := defaultInviteSuspensions(t.Context(), nil, userA, uuid.Must(uuid.NewV4())); err == nil {
		t.Fatal("no bot, no registry: the absence of a suspension must not be assumed")
	}
}
