package server

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/gofrs/uuid/v5"
	"github.com/heroiclabs/nakama-common/api"
	"github.com/heroiclabs/nakama-common/runtime"
	"github.com/heroiclabs/nakama/v3/server/evr"
	"go.uber.org/atomic"
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

// A record reaches the set already expanded to the modes it covers, so the set is
// asked about real records, not hand-built ones. A full record covers every
// invite mode; one that allows private lobbies covers none of them.
func TestInviteSuspensionsFrom_RecordCoverageOfInviteModes(t *testing.T) {
	for _, tc := range []struct {
		name         string
		allowPrivate bool
		want         bool
	}{{"full record", false, true}, {"allows private lobbies", true, false}} {
		t.Run(tc.name, func(t *testing.T) {
			group := uuid.Must(uuid.NewV4()).String()
			j := NewGuildEnforcementJournal(userA)
			j.AddRecord(group, "enforcer", "e", "notice", "", false, tc.allowPrivate, time.Hour)
			set, err := inviteSuspensionsFrom(userA, group, GuildEnforcementJournalList{userA: j}, nil, true)
			if err != nil {
				t.Fatal(err)
			}
			for _, m := range inviteModes {
				if got := set.refuses(m); got != tc.want {
					t.Errorf("refuses(%v) = %v, want %v", m, got, tc.want)
				}
			}
			if got := set.refusesEvery(); got != tc.want {
				t.Errorf("refusesEvery = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestInviteSuspensionSet_RefusesOnlyTheModesItHolds(t *testing.T) {
	set := inviteSuspensionSet{evr.ModeCombatPrivate: {}}
	if !set.refuses(evr.ModeCombatPrivate) || set.refuses(evr.ModeArenaPrivate) || set.refusesEvery() {
		t.Errorf("set %v: want combat refused, arena not, not every mode", set)
	}
	if (inviteSuspensionSet{}).refuses(evr.ModeArenaPrivate) {
		t.Error("an empty set refused")
	}
}

// The player's own 1h suspension is hidden in the merge by an alt's 24h record in
// the same guild. With alt suspensions ignored, the refusal must come from the
// player's own record: ignoring alts is not ignoring the player.
func TestInviteCreate_OwnSuspensionHiddenByLongerAltIsStillDenied(t *testing.T) {
	w := newInviteWorld(t)
	w.altsCount = false
	w.groups[userA] = uuid.Must(uuid.NewV4())
	w.suspend(userA, false, time.Hour)
	alt := NewGuildEnforcementJournal(userB)
	alt.AddRecord(w.groups[userA].String(), "enforcer", "e", "alt", "", false, false, 24*time.Hour)
	w.journals[userB] = alt
	_, err := w.call(withInviteRecord("create", InviteCreateRPC), userA, map[string]any{"experience": "arena"})
	wantDenied(t, err, "own suspension under a longer alt suspension, alts ignored")
	if len(w.allocs) != 0 {
		t.Errorf("allocations = %d, want none", len(w.allocs))
	}
}

// failingReadNakama fails storage reads of one collection.
type failingReadNakama struct {
	*occTestNakamaModule
	collection string
}

func (n *failingReadNakama) StorageRead(ctx context.Context, reads []*runtime.StorageRead) ([]*api.StorageObject, error) {
	for _, r := range reads {
		if r.Collection == n.collection {
			return nil, errors.New("storage down")
		}
	}
	return n.occTestNakamaModule.StorageRead(ctx, reads)
}

func TestDefaultInviteSuspensions_Wiring(t *testing.T) {
	const strongItem, weakItem = "hmd-serial-1", "unknown"
	group := uuid.Must(uuid.NewV4())

	for _, tc := range []struct {
		name         string
		noHistory    bool
		altItems     []string // login evidence linking userA to the alt userB; nil: no alt
		altSuspended bool
		ownSuspended bool
		ignoreAlts   bool   // the player's own IgnoreSuspensionsOfAltAccounts
		guild        string // "" no registered guild, "rejects", "tolerates"
		detector     bool
		want         bool // denied for arena_private
	}{
		{name: "strong alt suspended, guild rejects alts", altItems: []string{strongItem}, altSuspended: true, guild: "rejects", detector: true, want: true},
		{name: "strong alt suspended, guild tolerates alts", altItems: []string{strongItem}, altSuspended: true, guild: "tolerates", detector: true, want: false},
		{name: "strong alt suspended, player ignores alts", altItems: []string{strongItem}, altSuspended: true, guild: "rejects", detector: true, ignoreAlts: true, want: false},
		{name: "weak-only alt dropped by the detector", altItems: []string{weakItem}, altSuspended: true, guild: "rejects", detector: true, want: false},
		{name: "weak-only alt kept with no detector", altItems: []string{weakItem}, altSuspended: true, guild: "rejects", detector: false, want: true},
		{name: "guild not registered counts alts", altItems: []string{strongItem}, altSuspended: true, guild: "", detector: true, want: true},
		{name: "no login history, own suspension", noHistory: true, ownSuspended: true, guild: "rejects", detector: true, want: true},
		{name: "no login history, no suspension", noHistory: true, guild: "rejects", detector: true, want: false},
		{name: "own suspension, alts tolerated", altItems: []string{strongItem}, ownSuspended: true, guild: "tolerates", detector: true, want: true},
		{name: "alt not suspended", altItems: []string{strongItem}, guild: "rejects", detector: true, want: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			prevBot, prevDet := globalAppBot.Load(), GetCGNATDetector()
			t.Cleanup(func() { globalAppBot.Store(prevBot); SetCGNATDetector(prevDet) })
			if tc.detector {
				SetCGNATDetector(NewCGNATDetector(nil))
			} else {
				SetCGNATDetector(nil)
			}
			groups := map[string]*GuildGroup{}
			switch tc.guild {
			case "rejects", "tolerates":
				groups[group.String()] = &GuildGroup{GroupMetadata: GroupMetadata{RejectPlayersWithSuspendedAlternates: tc.guild == "rejects"}}
			}
			globalAppBot.Store(&DiscordAppBot{guildGroupRegistry: &GuildGroupRegistry{
				guildGroups:    atomic.NewPointer(&groups),
				inheritanceMap: atomic.NewPointer(&map[string][]string{}),
			}})

			nk := newOCCTestNakamaModule()
			seed := func(user, collection, key string, v any) {
				b, err := json.Marshal(v)
				if err != nil {
					t.Fatal(err)
				}
				nk.seedObject(user, collection, key, string(b))
			}
			if !tc.noHistory {
				h := NewLoginHistory(userA)
				h.IgnoreSuspensionsOfAltAccounts = tc.ignoreAlts
				if tc.altItems != nil {
					h.AlternateMatches = map[string][]*AlternateSearchMatch{userB: {{OtherUserID: userB, Items: tc.altItems}}}
				}
				seed(userA, LoginStorageCollection, LoginHistoryStorageKey, h)
			}
			journal := func(user string) {
				j := NewGuildEnforcementJournal(user)
				j.AddRecord(group.String(), "enforcer", "e", "notice", "", false, false, time.Hour)
				seed(user, StorageCollectionEnforcementJournal, StorageKeyEnforcementJournal, j)
			}
			if tc.altSuspended {
				journal(userB)
			}
			if tc.ownSuspended {
				journal(userA)
			}

			set, err := defaultInviteSuspensions(t.Context(), nk, userA, group)
			if err != nil {
				t.Fatalf("defaultInviteSuspensions: %v", err)
			}
			if got := set.refuses(evr.ModeArenaPrivate); got != tc.want {
				t.Errorf("refuses(arena_private) = %v, want %v (set %v)", got, tc.want, set)
			}
		})
	}
}

func TestDefaultInviteSuspensions_StorageFailuresRefuse(t *testing.T) {
	prev := globalAppBot.Load()
	t.Cleanup(func() { globalAppBot.Store(prev) })
	groups := map[string]*GuildGroup{}
	globalAppBot.Store(&DiscordAppBot{guildGroupRegistry: &GuildGroupRegistry{
		guildGroups:    atomic.NewPointer(&groups),
		inheritanceMap: atomic.NewPointer(&map[string][]string{}),
	}})
	for _, collection := range []string{LoginStorageCollection, StorageCollectionEnforcementJournal} {
		t.Run(collection, func(t *testing.T) {
			nk := &failingReadNakama{occTestNakamaModule: newOCCTestNakamaModule(), collection: collection}
			if _, err := defaultInviteSuspensions(t.Context(), nk, userA, uuid.Must(uuid.NewV4())); err == nil {
				t.Fatal("a storage failure was read as no suspension")
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
