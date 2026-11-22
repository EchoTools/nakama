package server

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/heroiclabs/nakama-common/api"
	"github.com/heroiclabs/nakama-common/runtime"
	"github.com/heroiclabs/nakama/v3/server/evr"
	"go.uber.org/atomic"
)

const (
	inhSrc   = "group-src"   // issues the enforcement
	inhChild = "group-child" // inherits from inhSrc
	inhOther = "group-other" // also inherits from inhSrc, with no filter
)

// inhJournal returns a journal holding one enforcement in inhSrc.
func inhJournal(userID string, duration time.Duration, allowPrivate bool) *GuildEnforcementJournal {
	j := NewGuildEnforcementJournal(userID)
	j.AddRecord(inhSrc, "enforcer", "enforcer-discord", "notice", "notes", false, allowPrivate, duration)
	return j
}

func inhCheck(t *testing.T, journals GuildEnforcementJournalList, filters InheritanceFilters) ActiveGuildEnforcements {
	t.Helper()
	got, err := CheckEnforcementSuspensions(journals, map[string][]string{inhSrc: {inhChild, inhOther}}, filters)
	if err != nil {
		t.Fatalf("CheckEnforcementSuspensions: %v", err)
	}
	return got
}

func inhFilter(f SuspensionInheritanceFilter) InheritanceFilters {
	return InheritanceFilters{inhSrc: {inhChild: f}}
}

func TestInheritanceFilter_MinimumDuration(t *testing.T) {
	const week = 7 * 24 * time.Hour
	filter := inhFilter(SuspensionInheritanceFilter{MinimumDurationSeconds: int64(week / time.Second)})

	for _, tc := range []struct {
		name     string
		duration time.Duration
		inherits bool
	}{
		{"30 minutes is shorter than 7d", 30 * time.Minute, false},
		{"exactly 7d is not longer than 7d", week, false},
		{"8d is longer than 7d", 8 * 24 * time.Hour, true},
		{"permanent is longer than any minimum", LifetimeSuspensionDuration, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := inhCheck(t, GuildEnforcementJournalList{"u": inhJournal("u", tc.duration, false)}, filter)

			if len(got[inhSrc]) == 0 {
				t.Fatalf("source guild lost its own enforcement: %v", got)
			}
			if inherited := len(got[inhChild]) > 0; inherited != tc.inherits {
				t.Fatalf("child inherited = %v, want %v", inherited, tc.inherits)
			}
			// A link with no filter is unaffected by the child's filter.
			if len(got[inhOther]) == 0 {
				t.Fatalf("unfiltered link did not inherit: %v", got)
			}
		})
	}
}

func TestInheritanceFilter_Scope(t *testing.T) {
	filter := inhFilter(SuspensionInheritanceFilter{Scope: InheritScopeFullBans})

	for _, tc := range []struct {
		name         string
		allowPrivate bool
		inherits     bool
	}{
		{"public-only restriction stays with the source", true, false},
		{"full ban that restricts private matches is inherited", false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := inhCheck(t, GuildEnforcementJournalList{"u": inhJournal("u", time.Hour, tc.allowPrivate)}, filter)

			if len(got[inhSrc]) == 0 {
				t.Fatalf("source guild lost its own enforcement: %v", got)
			}
			if inherited := len(got[inhChild]) > 0; inherited != tc.inherits {
				t.Fatalf("child inherited = %v, want %v", inherited, tc.inherits)
			}
			if tc.inherits {
				if _, ok := got[inhChild][evr.ModeArenaPrivate]; !ok {
					t.Fatalf("an inherited full ban must restrict private matches: %v", got[inhChild])
				}
			}
		})
	}
}

func TestInheritanceFilter_BothFiltersMustPass(t *testing.T) {
	filter := inhFilter(SuspensionInheritanceFilter{MinimumDurationSeconds: int64((24 * time.Hour) / time.Second), Scope: InheritScopeFullBans})

	for _, tc := range []struct {
		name         string
		duration     time.Duration
		allowPrivate bool
		inherits     bool
	}{
		{"long and full", 48 * time.Hour, false, true},
		{"long but public-only", 48 * time.Hour, true, false},
		{"full but short", time.Hour, false, false},
		{"permanent public-only", LifetimeSuspensionDuration, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := inhCheck(t, GuildEnforcementJournalList{"u": inhJournal("u", tc.duration, tc.allowPrivate)}, filter)
			if inherited := len(got[inhChild]) > 0; inherited != tc.inherits {
				t.Fatalf("child inherited = %v, want %v", inherited, tc.inherits)
			}
		})
	}
}

// ActiveSuspensions keeps one record per guild: the one that expires last. A
// filter must be asked about every active record, not only that one, or a longer
// public-only restriction would hide a shorter full ban from the inheriting guild.
func TestInheritanceFilter_ChecksEveryActiveRecordNotJustTheLongest(t *testing.T) {
	j := NewGuildEnforcementJournal("u")
	j.AddRecord(inhSrc, "e", "ed", "long public-only", "", false, true, 30*24*time.Hour)
	j.AddRecord(inhSrc, "e", "ed", "shorter full ban", "", false, false, 8*24*time.Hour)

	got := inhCheck(t, GuildEnforcementJournalList{"u": j}, inhFilter(SuspensionInheritanceFilter{Scope: InheritScopeFullBans}))

	rec, ok := got[inhChild][evr.ModeArenaPrivate]
	if !ok {
		t.Fatalf("child must inherit the full ban even though a longer public-only record exists: %v", got[inhChild])
	}
	if rec.UserNoticeText != "shorter full ban" {
		t.Fatalf("child inherited %q, want the full ban", rec.UserNoticeText)
	}
	if _, ok := got[inhChild][evr.ModeArenaPublic]; !ok {
		t.Fatalf("the full ban restricts public matches too: %v", got[inhChild])
	}
	// The source guild's own result is unchanged: its longest record, public-only.
	if _, ok := got[inhSrc][evr.ModeArenaPrivate]; ok {
		t.Fatalf("source guild's own enforcement must not change: %v", got[inhSrc])
	}
}

// Every existing link has no stored filter. It must inherit exactly what it did
// before the filter existed.
func TestInheritanceFilter_DefaultInheritsEverything(t *testing.T) {
	for _, tc := range []struct {
		name    string
		filters InheritanceFilters
	}{
		{"nil filters", nil},
		{"empty filters", InheritanceFilters{}},
		{"zero filter on the link", inhFilter(SuspensionInheritanceFilter{})},
		{"explicit scope all", inhFilter(SuspensionInheritanceFilter{Scope: InheritScopeAll})},
	} {
		t.Run(tc.name, func(t *testing.T) {
			j := NewGuildEnforcementJournal("u")
			j.AddRecord(inhSrc, "e", "ed", "short public-only", "", false, true, 30*time.Minute)
			journals := GuildEnforcementJournalList{"u": j}

			got := inhCheck(t, journals, tc.filters)
			if len(got[inhChild]) == 0 {
				t.Fatalf("default link must inherit a 30 minute public-only record: %v", got)
			}
			if _, ok := got[inhChild][evr.ModeArenaPrivate]; ok {
				t.Fatalf("a public-only record must not restrict private matches: %v", got[inhChild])
			}
		})
	}
}

func TestSuspensionInheritanceFilter_Validate(t *testing.T) {
	for _, tc := range []struct {
		name string
		f    SuspensionInheritanceFilter
		ok   bool
	}{
		{"zero", SuspensionInheritanceFilter{}, true},
		{"full bans and a week", SuspensionInheritanceFilter{Scope: InheritScopeFullBans, MinimumDurationSeconds: 604800}, true},
		{"negative minimum", SuspensionInheritanceFilter{MinimumDurationSeconds: -1}, false},
		{"minimum that would overflow a Duration", SuspensionInheritanceFilter{MinimumDurationSeconds: 1 << 62}, false},
		{"unknown scope", SuspensionInheritanceFilter{Scope: "everything"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.f.Validate(); (err == nil) != tc.ok {
				t.Fatalf("Validate() = %v, want ok=%v", err, tc.ok)
			}
		})
	}
}

func TestGroupMetadata_ValidateSuspensionInheritanceFilters(t *testing.T) {
	md := &GroupMetadata{
		SuspensionInheritanceGroupIDs: []string{inhSrc},
		SuspensionInheritanceFilters:  map[string]SuspensionInheritanceFilter{inhSrc: {Scope: InheritScopeFullBans}},
	}
	if err := md.ValidateSuspensionInheritanceFilters(); err != nil {
		t.Fatalf("valid filter rejected: %v", err)
	}

	md.SuspensionInheritanceFilters = map[string]SuspensionInheritanceFilter{"not-a-link": {Scope: InheritScopeFullBans}}
	if err := md.ValidateSuspensionInheritanceFilters(); err == nil {
		t.Fatal("a filter for a source the guild does not inherit from must be rejected")
	}

	md.SuspensionInheritanceFilters = map[string]SuspensionInheritanceFilter{inhSrc: {Scope: "bogus"}}
	if err := md.ValidateSuspensionInheritanceFilters(); err == nil {
		t.Fatal("an invalid filter must be rejected")
	}
}

// A guild's metadata written before the filters existed has no such key. It must
// load with every link at the default, and the key must not appear when unset.
func TestGroupMetadata_FiltersJSON(t *testing.T) {
	var old GroupMetadata
	if err := json.Unmarshal([]byte(`{"suspension_inheritence_group_ids":["`+inhSrc+`"]}`), &old); err != nil {
		t.Fatal(err)
	}
	if f := old.InheritanceFilterFor(inhSrc); !f.IsDefault() {
		t.Fatalf("legacy link filter = %+v, want default", f)
	}
	b, _ := json.Marshal(&old)
	var m map[string]any
	_ = json.Unmarshal(b, &m)
	if _, ok := m["suspension_inheritance_filters"]; ok {
		t.Fatalf("unset filters must not be written: %s", b)
	}

	var set GroupMetadata
	in := `{"suspension_inheritence_group_ids":["` + inhSrc + `"],"suspension_inheritance_filters":{"` + inhSrc + `":{"minimum_duration_seconds":604800,"scope":"full_bans"}}}`
	if err := json.Unmarshal([]byte(in), &set); err != nil {
		t.Fatal(err)
	}
	if f := set.InheritanceFilterFor(inhSrc); f.MinimumDurationSeconds != 604800 || f.Scope != InheritScopeFullBans {
		t.Fatalf("filter = %+v", f)
	}
}

func TestAddInheritanceLinks_RecordsOnlyNonDefaultFilters(t *testing.T) {
	inheritance := map[string][]string{}
	filters := InheritanceFilters{}

	filtered := &GroupMetadata{
		SuspensionInheritanceGroupIDs: []string{inhSrc},
		SuspensionInheritanceFilters:  map[string]SuspensionInheritanceFilter{inhSrc: {Scope: InheritScopeFullBans}},
	}
	plain := &GroupMetadata{SuspensionInheritanceGroupIDs: []string{inhSrc}}
	addInheritanceLinks(inhChild, filtered, inheritance, filters)
	addInheritanceLinks(inhOther, plain, inheritance, filters)

	if got := inheritance[inhSrc]; len(got) != 2 {
		t.Fatalf("links = %v, want both guilds", got)
	}
	if f := filters.For(inhSrc, inhChild); f.Scope != InheritScopeFullBans {
		t.Fatalf("filtered link = %+v", f)
	}
	if _, ok := filters[inhSrc][inhOther]; ok {
		t.Fatalf("a default link must not carry a filter: %v", filters)
	}
}

func TestGuildGroupRegistry_InheritanceFiltersNilSafe(t *testing.T) {
	r := &GuildGroupRegistry{}
	if f := r.InheritanceFilters(); f.For(inhSrc, inhChild).Accepts(GuildEnforcementRecord{}) != true {
		t.Fatal("a registry with no filters must inherit everything")
	}
}

// A guild admin saves the filters through GuildGroupUpdateRPC. A filter that
// could not have been meant is refused before anything is written.
func TestGuildGroupUpdateRPC_RejectsInvalidInheritanceFilter(t *testing.T) {
	ownerCtx := context.WithValue(context.Background(), runtime.RUNTIME_CTX_USER_ID, "owner-uid") //nolint:staticcheck // SA1029: key type is dictated by the runtime API
	nk, gid := newRulesRPCFixture(t, "")

	for name, md := range map[string]string{
		"unknown scope":            `{"suspension_inheritence_group_ids":["` + inhSrc + `"],"suspension_inheritance_filters":{"` + inhSrc + `":{"scope":"bogus"}}}`,
		"negative minimum":         `{"suspension_inheritence_group_ids":["` + inhSrc + `"],"suspension_inheritance_filters":{"` + inhSrc + `":{"minimum_duration_seconds":-5}}}`,
		"filter without a link":    `{"suspension_inheritance_filters":{"` + inhSrc + `":{"scope":"full_bans"}}}`,
		"filter on the wrong link": `{"suspension_inheritence_group_ids":["` + inhOther + `"],"suspension_inheritance_filters":{"` + inhSrc + `":{"scope":"full_bans"}}}`,
	} {
		t.Run(name, func(t *testing.T) {
			_, err := GuildGroupUpdateRPC(ownerCtx, newCaptureLogger(), nil, nk, `{"group_id":"`+gid+`","metadata":`+md+`}`)
			rerr, ok := err.(*runtime.Error)
			if !ok || rerr.Code != 3 {
				t.Fatalf("err = %#v, want runtime error code 3 (invalid argument)", err)
			}
		})
	}
}

// guildListNakama lists its guild groups to the registry's rebuild and serves
// their state objects from the shared storage double.
type guildListNakama struct {
	*occTestNakamaModule
	groups []*api.Group
}

func (n *guildListNakama) GroupsList(ctx context.Context, name, langTag string, members *int, open *bool, limit int, cursor string) ([]*api.Group, string, error) {
	return n.groups, "", nil
}

// newRebuiltRegistry builds a registry the way production does, through
// rebuildGuildGroups, over guilds whose stored metadata is given by group id.
func newRebuiltRegistry(t *testing.T, metadata map[string]string) *GuildGroupRegistry {
	t.Helper()
	prev := serviceSettings.Load()
	ServiceSettingsUpdate(&ServiceSettingsData{DiscordBotUserID: "bot-uid"})
	t.Cleanup(func() { serviceSettings.Store(prev) })

	nk := &guildListNakama{occTestNakamaModule: newOCCTestNakamaModule()}
	for id, md := range metadata {
		nk.groups = append(nk.groups, &api.Group{Id: id, Name: id, LangTag: GuildGroupLangTag, Metadata: md})
		state, _ := json.Marshal(map[string]any{"group_id": id})
		nk.seedObject("bot-uid", StorageCollectionState, id, string(state))
	}
	r := &GuildGroupRegistry{
		ctx:                context.Background(),
		logger:             newCaptureLogger(),
		nk:                 nk,
		guildGroups:        atomic.NewPointer(&map[string]*GuildGroup{}),
		inheritanceMap:     atomic.NewPointer(&map[string][]string{}),
		inheritanceFilters: atomic.NewPointer(&InheritanceFilters{}),
	}
	r.rebuildGuildGroups()
	return r
}

// The registry production code reads must carry the filter a guild stored.
func TestRebuildGuildGroups_CarriesStoredInheritanceFilters(t *testing.T) {
	r := newRebuiltRegistry(t, map[string]string{
		inhChild: `{"suspension_inheritence_group_ids":["` + inhSrc + `"],"suspension_inheritance_filters":{"` + inhSrc + `":{"minimum_duration_seconds":604800,"scope":"full_bans"}}}`,
		inhOther: `{"suspension_inheritence_group_ids":["` + inhSrc + `"]}`,
		inhSrc:   `{}`,
	})
	if got := r.InheritanceByParentGroupID()[inhSrc]; len(got) != 2 {
		t.Fatalf("links = %v, want both guilds", got)
	}
	f := r.InheritanceFilters().For(inhSrc, inhChild)
	if f.MinimumDurationSeconds != 604800 || f.Scope != InheritScopeFullBans {
		t.Fatalf("filter on %s<-%s = %+v, want the stored one", inhChild, inhSrc, f)
	}
	if !r.InheritanceFilters().For(inhSrc, inhOther).IsDefault() {
		t.Fatalf("an unfiltered link must stay default")
	}
}

// A guild admin saves a filter through the RPC as the client sends it; once
// stored, the guild group and the registry both see it.
func TestGuildGroupUpdateRPC_ValidFilterReachesGuildGroupAndRegistry(t *testing.T) {
	ownerCtx := context.WithValue(context.Background(), runtime.RUNTIME_CTX_USER_ID, "owner-uid") //nolint:staticcheck // SA1029: key type is dictated by the runtime API
	nk, gid := newRulesRPCFixture(t, "")

	prevSave := saveGroupMetadata
	t.Cleanup(func() { saveGroupMetadata = prevSave })
	var saved int
	saveGroupMetadata = func(_ context.Context, _ runtime.NakamaModule, groupID string, md *GroupMetadata) error {
		saved++
		b, err := json.Marshal(md) // what GroupMetadataSave writes to the groups table
		nk.group.Metadata = string(b)
		return err
	}

	payload := `{"group_id":"` + gid + `","metadata":{"suspension_inheritence_group_ids":["` + inhSrc + `"],"suspension_inheritance_filters":{"` + inhSrc + `":{"minimum_duration_seconds":604800,"scope":"full_bans"}}}}`
	if _, err := GuildGroupUpdateRPC(ownerCtx, newCaptureLogger(), nil, nk, payload); err != nil {
		t.Fatalf("rpc: %v", err)
	}
	if saved != 1 {
		t.Fatalf("metadata saved %d times, want 1", saved)
	}

	gg, err := GuildGroupLoad(ownerCtx, nk, gid)
	if err != nil {
		t.Fatal(err)
	}
	if f := gg.InheritanceFilterFor(inhSrc); f.MinimumDurationSeconds != 604800 || f.Scope != InheritScopeFullBans {
		t.Fatalf("GuildGroup filter = %+v", f)
	}

	// The registry rebuilt from the same stored group reaches the same answer.
	r := newRebuiltRegistry(t, map[string]string{gid: nk.group.Metadata})
	if f := r.InheritanceFilters().For(inhSrc, gid); f.Scope != InheritScopeFullBans {
		t.Fatalf("registry filter = %+v", f)
	}
}
