package server

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/heroiclabs/nakama-common/api"
	"github.com/heroiclabs/nakama-common/runtime"
	"github.com/heroiclabs/nakama/v3/server/evr"
	"go.uber.org/atomic"
	"google.golang.org/protobuf/types/known/wrapperspb"
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

// groupWriteNakama is the guild group GuildGroupUpdateRPC writes to: it records
// GroupUpdate calls and applies them to the group the RPC reads back.
type groupWriteNakama struct {
	*rulesRPCNakama
	groupUpdates int
}

func (n *groupWriteNakama) GroupUpdate(_ context.Context, _, _, name, _, _, description, avatarURL string, open bool, _ map[string]any, _ int) error {
	n.groupUpdates++
	if name != "" {
		n.group.Name = name
	}
	if description != "" {
		n.group.Description = description
	}
	if avatarURL != "" {
		n.group.AvatarUrl = avatarURL
	}
	n.group.Open = wrapperspb.Bool(open)
	return nil
}

// groupsTableConnector stands in for the groups table GroupMetadataSave reads
// and writes: SELECT metadata returns *metadata, UPDATE stores its first
// argument there, so the real GroupMetadataSave runs with no database.
type groupsTableConnector struct {
	metadata *string
	updates  int
}

func (c *groupsTableConnector) Connect(context.Context) (driver.Conn, error) {
	return groupsTableConn{c}, nil
}
func (c *groupsTableConnector) Driver() driver.Driver { return nil }

type groupsTableConn struct{ t *groupsTableConnector }

func (c groupsTableConn) Prepare(query string) (driver.Stmt, error) {
	return groupsTableStmt{c.t, query}, nil
}
func (groupsTableConn) Close() error              { return nil }
func (groupsTableConn) Begin() (driver.Tx, error) { return nil, driver.ErrSkip }

type groupsTableStmt struct {
	t     *groupsTableConnector
	query string
}

func (groupsTableStmt) Close() error  { return nil }
func (groupsTableStmt) NumInput() int { return -1 }
func (s groupsTableStmt) Exec(args []driver.Value) (driver.Result, error) {
	if !strings.HasPrefix(s.query, "UPDATE groups SET") {
		return nil, errors.New("unexpected exec: " + s.query)
	}
	s.t.updates++
	*s.t.metadata = args[0].(string)
	return driver.RowsAffected(1), nil
}
func (s groupsTableStmt) Query([]driver.Value) (driver.Rows, error) {
	if !strings.HasPrefix(s.query, "SELECT metadata FROM groups") {
		return nil, errors.New("unexpected query: " + s.query)
	}
	return &groupsTableRows{value: *s.t.metadata}, nil
}

type groupsTableRows struct {
	value string
	done  bool
}

func (r *groupsTableRows) Columns() []string { return []string{"metadata"} }
func (r *groupsTableRows) Close() error      { return nil }
func (r *groupsTableRows) Next(dest []driver.Value) error {
	if r.done {
		return io.EOF
	}
	r.done = true
	dest[0] = r.value
	return nil
}

// newGroupWriteFixture is a guild group whose stored metadata is `stored`, and
// a metadata save that goes through the real GroupMetadataSave against a stub
// groups table (what saveGroupMetadata does in production, minus the cast to the
// real runtime module, which needs a live database).
func newGroupWriteFixture(t *testing.T, stored string) (*groupWriteNakama, string, *groupsTableConnector) {
	t.Helper()
	base, gid := newRulesRPCFixture(t, "old rules")
	nk := &groupWriteNakama{rulesRPCNakama: base}
	if stored != "" {
		nk.group.Metadata = stored
	}
	table := &groupsTableConnector{metadata: &nk.group.Metadata}
	db := sql.OpenDB(table)
	t.Cleanup(func() { db.Close() })

	prev := saveGroupMetadata
	t.Cleanup(func() { saveGroupMetadata = prev })
	saveGroupMetadata = func(ctx context.Context, _ runtime.NakamaModule, groupID string, md *GroupMetadata) error {
		return GroupMetadataSave(ctx, db, groupID, md)
	}
	return nk, gid, table
}

var inhOwnerCtx = context.WithValue(context.Background(), runtime.RUNTIME_CTX_USER_ID, "owner-uid") //nolint:staticcheck // SA1029: key type is dictated by the runtime API

// A guild admin saves the filters through GuildGroupUpdateRPC. A request that
// could not have been meant is refused before anything is written: the group
// keeps its name, description, avatar, open flag, rules and metadata.
func TestGuildGroupUpdateRPC_RejectsInvalidRequestWithoutWriting(t *testing.T) {
	const groupFields = `"name":"renamed","description":"new desc","avatar_url":"http://x/y.png","open":true,"rules_text":"new rules",`
	badFilter := func(f string) string {
		return `"metadata":{"suspension_inheritence_group_ids":["` + inhSrc + `"],"suspension_inheritance_filters":{"` + inhSrc + `":` + f + `}}`
	}
	cases := map[string]string{
		"unknown scope":            badFilter(`{"scope":"bogus"}`),
		"negative minimum":         badFilter(`{"minimum_duration_seconds":-5}`),
		"filter without a link":    `"metadata":{"suspension_inheritance_filters":{"` + inhSrc + `":{"scope":"full_bans"}}}`,
		"filter on the wrong link": `"metadata":{"suspension_inheritence_group_ids":["` + inhOther + `"],"suspension_inheritance_filters":{"` + inhSrc + `":{"scope":"full_bans"}}}`,
		"rules text too long":      `"metadata":{"minimum_discord_account_age_days":9},"rules_text":"` + strings.Repeat("x", maxGuildRulesTextLen+1) + `"`,
	}
	for name, tail := range cases {
		t.Run(name, func(t *testing.T) {
			nk, gid, table := newGroupWriteFixture(t, "")
			beforeMeta := nk.group.Metadata
			beforeGroup := fmt.Sprintf("%s|%s|%s|%v", nk.group.Name, nk.group.Description, nk.group.AvatarUrl, nk.group.Open.GetValue())

			payload := `{"group_id":"` + gid + `",` + groupFields + tail + `}`
			if name == "rules text too long" {
				payload = `{"group_id":"` + gid + `","name":"renamed","description":"new desc",` + tail + `}`
			}
			_, err := GuildGroupUpdateRPC(inhOwnerCtx, newCaptureLogger(), nil, nk, payload)
			rerr, ok := err.(*runtime.Error)
			if !ok || rerr.Code != 3 {
				t.Fatalf("err = %#v, want runtime error code 3 (invalid argument)", err)
			}
			if nk.groupUpdates != 0 || table.updates != 0 {
				t.Fatalf("refused request wrote: GroupUpdate %d, metadata saves %d", nk.groupUpdates, table.updates)
			}
			if got := fmt.Sprintf("%s|%s|%s|%v", nk.group.Name, nk.group.Description, nk.group.AvatarUrl, nk.group.Open.GetValue()); got != beforeGroup {
				t.Fatalf("group name|description|avatar|open changed to %q, was %q", got, beforeGroup)
			}
			if nk.group.Metadata != beforeMeta {
				t.Fatalf("metadata changed: %s", nk.group.Metadata)
			}
			if got := storedRules(t, nk.rulesRPCNakama, gid); got != "old rules" {
				t.Fatalf("rules text changed to %q", got)
			}
		})
	}
}

// A guild admin saves a filter through the RPC as the client sends it; it goes
// through GroupMetadataSave, and once stored the guild group and the registry
// both see it.
func TestGuildGroupUpdateRPC_ValidFilterReachesGuildGroupAndRegistry(t *testing.T) {
	nk, gid, table := newGroupWriteFixture(t, "")

	payload := `{"group_id":"` + gid + `","name":"renamed","metadata":{"suspension_inheritence_group_ids":["` + inhSrc + `"],"suspension_inheritance_filters":{"` + inhSrc + `":{"minimum_duration_seconds":604800,"scope":"full_bans"}}}}`
	if _, err := GuildGroupUpdateRPC(inhOwnerCtx, newCaptureLogger(), nil, nk, payload); err != nil {
		t.Fatalf("rpc: %v", err)
	}
	if table.updates != 1 || nk.groupUpdates != 1 {
		t.Fatalf("metadata saves %d, GroupUpdate %d, want 1 each", table.updates, nk.groupUpdates)
	}

	gg, err := GuildGroupLoad(inhOwnerCtx, nk, gid)
	if err != nil {
		t.Fatal(err)
	}
	if f := gg.InheritanceFilterFor(inhSrc); f.MinimumDurationSeconds != 604800 || f.Scope != InheritScopeFullBans {
		t.Fatalf("GuildGroup filter = %+v", f)
	}

	// The registry rebuilt from the same stored group reaches the same answer.
	r := newRebuiltRegistry(t, map[string]string{gid: nk.group.Metadata})
	if f := r.InheritanceFilters().For(inhSrc, gid); f.MinimumDurationSeconds != 604800 || f.Scope != InheritScopeFullBans {
		t.Fatalf("registry filter = %+v", f)
	}
}

// req.Metadata replaces the stored metadata whole, so the filters need their own
// rule: a client that saves without the key keeps them, and one that sends the
// key, even empty or null, replaces them.
func TestGuildGroupUpdateRPC_InheritanceFiltersOmittedKeptEmptyCleared(t *testing.T) {
	stored := `{"guild_id":"222","owner_id":"owner-uid","suspension_inheritence_group_ids":["` + inhSrc + `"],"suspension_inheritance_filters":{"` + inhSrc + `":{"minimum_duration_seconds":604800,"scope":"full_bans"}}}`
	link := `"suspension_inheritence_group_ids":["` + inhSrc + `"]`
	cases := []struct {
		name, metadata string
		want           SuspensionInheritanceFilter
	}{
		{"omitted keeps", `{` + link + `,"minimum_discord_account_age_days":5}`, SuspensionInheritanceFilter{MinimumDurationSeconds: 604800, Scope: InheritScopeFullBans}},
		{"empty object clears", `{` + link + `,"suspension_inheritance_filters":{}}`, SuspensionInheritanceFilter{}},
		{"null clears", `{` + link + `,"suspension_inheritance_filters":null}`, SuspensionInheritanceFilter{}},
		{"replaced", `{` + link + `,"suspension_inheritance_filters":{"` + inhSrc + `":{"scope":"all"}}}`, SuspensionInheritanceFilter{Scope: InheritScopeAll}},
		{"omitted with the link dropped drops the filter", `{"minimum_discord_account_age_days":5}`, SuspensionInheritanceFilter{}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			nk, gid, table := newGroupWriteFixture(t, stored)
			if _, err := GuildGroupUpdateRPC(inhOwnerCtx, newCaptureLogger(), nil, nk, `{"group_id":"`+gid+`","metadata":`+c.metadata+`}`); err != nil {
				t.Fatalf("rpc: %v", err)
			}
			if table.updates != 1 {
				t.Fatalf("metadata saves = %d, want 1", table.updates)
			}
			gg, err := GuildGroupLoad(inhOwnerCtx, nk, gid)
			if err != nil {
				t.Fatal(err)
			}
			if got := gg.InheritanceFilterFor(inhSrc); !reflect.DeepEqual(got, c.want) {
				t.Fatalf("filter after save = %+v, want %+v (stored %s)", got, c.want, nk.group.Metadata)
			}
		})
	}
}
