package server

import (
	"encoding/json"
	"fmt"
	"maps"
	"strings"
	"testing"
	"time"

	"github.com/heroiclabs/nakama-common/api"
	"github.com/heroiclabs/nakama-common/runtime"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"
)

// --- record side -------------------------------------------------------------

func TestEnforcementRecord_StringPropertiesJSON(t *testing.T) {
	r := GuildEnforcementRecord{ID: "r", StringProperties: map[string]string{"tier": "high"}}
	b, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"string_properties":{"tier":"high"}`) {
		t.Fatalf("string_properties not written: %s", b)
	}
	var old GuildEnforcementRecord
	if err := json.Unmarshal([]byte(`{"id":"old"}`), &old); err != nil {
		t.Fatal(err)
	}
	if old.StringProperties != nil {
		t.Fatalf("a record written before string properties must decode untagged, got %v", old.StringProperties)
	}
	b, _ = json.Marshal(old)
	if strings.Contains(string(b), "string_properties") {
		t.Fatalf("an untagged record must not write the key: %s", b)
	}
}

// Nakama writes its own facts onto every record it adds, on every write path.
func TestAddRecord_WritesNakamaStringProperties(t *testing.T) {
	j := NewGuildEnforcementJournal("u")
	short := j.AddRecord("g", "e", "ed", "n", "", false, false, time.Hour)
	want := map[string]string{
		StringPropertyPermanent:               "false",
		StringPropertyAllowPrivates:           "false",
		StringPropertyCommunityValuesRequired: "false",
		StringPropertyIsPubliclyVisible:       "false",
	}
	if !maps.Equal(short.StringProperties, want) {
		t.Fatalf("1h record properties = %v, want %v", short.StringProperties, want)
	}
	if !maps.Equal(j.GetRecord("g", short.ID).StringProperties, want) {
		t.Fatalf("the journal's copy must carry the properties too: %v", j.GetRecord("g", short.ID).StringProperties)
	}

	life := j.AddRecordWithOptions("g", "e", "ed", "n", "", "hacks", true, true, true, LifetimeSuspensionDuration, "", "", "")
	want = map[string]string{
		StringPropertyPermanent:               "true",
		StringPropertyAllowPrivates:           "true",
		StringPropertyCommunityValuesRequired: "true",
		StringPropertyIsPubliclyVisible:       "true",
		StringPropertyRuleViolated:            "hacks",
	}
	if !maps.Equal(life.StringProperties, want) {
		t.Fatalf("lifetime record properties = %v, want %v", life.StringProperties, want)
	}
}

// A caller's value for a nakama-owned key is overwritten, and reported.
func TestSetRecordStringProperties_NakamaOwnedKeysWin(t *testing.T) {
	j := NewGuildEnforcementJournal("u")
	r := j.AddRecord("g", "e", "ed", "n", "", false, false, LifetimeSuspensionDuration)
	got, owned := j.SetRecordStringProperties("g", r.ID, map[string]string{"tier": "high", "permanent": "false", "rule_violated": "x"})
	if got == nil {
		t.Fatal("record not found")
	}
	if got.StringProperties["tier"] != "high" {
		t.Fatalf("caller property not stored: %v", got.StringProperties)
	}
	if got.StringProperties[StringPropertyPermanent] != "true" {
		t.Fatalf("permanent must stay nakama's fact, got %v", got.StringProperties)
	}
	if _, ok := got.StringProperties[StringPropertyRuleViolated]; ok {
		t.Fatalf("rule_violated is nakama-owned and the record has none, got %v", got.StringProperties)
	}
	if fmt.Sprint(owned) != "[permanent rule_violated]" {
		t.Fatalf("owned keys reported = %v", owned)
	}
}

// Records written before string properties existed have no map. Nakama-owned
// keys are derived from their fields on read; a stored value wins.
func TestStringProperty_DerivesNakamaOwnedKeysOnRead(t *testing.T) {
	now := time.Now()
	old := GuildEnforcementRecord{CreatedAt: now, Expiry: now.Add(LifetimeSuspensionDuration), AllowPrivateLobbies: true}
	for key, want := range map[string]string{
		StringPropertyPermanent:               "true",
		StringPropertyAllowPrivates:           "true",
		StringPropertyCommunityValuesRequired: "false",
		StringPropertyIsPubliclyVisible:       "false",
	} {
		if v, ok := old.StringProperty(key); !ok || v != want {
			t.Errorf("derived %s = %q,%v want %q", key, v, ok, want)
		}
	}
	if _, ok := old.StringProperty(StringPropertyRuleViolated); ok {
		t.Error("rule_violated must be absent when the record has none")
	}
	if _, ok := old.StringProperty("tier"); ok {
		t.Error("a caller key is never derived")
	}
	stored := old
	stored.StringProperties = map[string]string{StringPropertyPermanent: "stored"}
	if v, _ := stored.StringProperty(StringPropertyPermanent); v != "stored" {
		t.Errorf("a stored value must win, got %q", v)
	}
}

func TestValidateStringProperties(t *testing.T) {
	many := map[string]string{}
	for i := range maxStringProperties + 1 {
		many[fmt.Sprint("k", i)] = "v"
	}
	long := strings.Repeat("x", maxStringPropertyBytes+1)
	for _, tc := range []struct {
		name     string
		props    map[string]string
		edit, ok bool
	}{
		{"nil", nil, false, true},
		{"tier", map[string]string{"tier": "high"}, false, true},
		{"empty key", map[string]string{"": "v"}, false, false},
		{"long key", map[string]string{long: "v"}, false, false},
		{"long value", map[string]string{"k": long}, false, false},
		{"too many", many, false, false},
		{"empty value on kick", map[string]string{"k": ""}, false, false},
		{"empty value on edit deletes", map[string]string{"k": ""}, true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := validateStringProperties(tc.props, tc.edit); (err == nil) != tc.ok {
				t.Fatalf("validateStringProperties = %v, want ok=%v", err, tc.ok)
			}
		})
	}
}

// --- RPCs --------------------------------------------------------------------

// keepLatestStorage drops every stored version but the last, so StorableRead on
// the mock (which appends per write) sees one object.
func keepLatestStorage(nk *mockEnforcementNakamaModule) {
	for k, objs := range nk.storageObjects {
		nk.storageObjects[k] = []*api.StorageObject{objs[len(objs)-1]}
	}
}

func propsLoadJournal(t *testing.T, f *kickLogFixture) *GuildEnforcementJournal {
	t.Helper()
	keepLatestStorage(f.nk.mockEnforcementNakamaModule)
	j := NewGuildEnforcementJournal(f.targetID)
	if err := StorableRead(f.ctx, f.nk, f.targetID, j, false); err != nil {
		t.Fatalf("read journal: %v", err)
	}
	return j
}

func onlyRecord(t *testing.T, f *kickLogFixture) GuildEnforcementRecord {
	t.Helper()
	recs := propsLoadJournal(t, f).GroupRecords(f.groupID)
	if len(recs) != 1 {
		t.Fatalf("records = %d, want 1", len(recs))
	}
	return recs[0]
}

func propsObservedLogger() (runtime.Logger, *observer.ObservedLogs) {
	core, logs := observer.New(zapcore.DebugLevel)
	return NewRuntimeGoLogger(zap.New(core)), logs
}

func TestEnforcementKickRPC_StoresStringProperties(t *testing.T) {
	f := newKickLogFixture(t)
	useSuspensionLogSender(t, &fakeNoticeSender{})
	runKickRPC(t, f, f.payload(`,"string_properties":{"tier":"high","permanent":"true"}`))

	r := onlyRecord(t, f)
	if r.StringProperties["tier"] != "high" {
		t.Fatalf("tier not stored: %v", r.StringProperties)
	}
	if r.StringProperties[StringPropertyPermanent] != "false" {
		t.Fatalf("a 1h kick's permanent must be nakama's \"false\", got %v", r.StringProperties)
	}
	if r.StringProperties[StringPropertyAllowPrivates] == "" {
		t.Fatalf("nakama-owned keys missing: %v", r.StringProperties)
	}
}

func TestEnforcementKickRPC_RejectsInvalidStringProperties(t *testing.T) {
	f := newKickLogFixture(t)
	useSuspensionLogSender(t, &fakeNoticeSender{})
	_, err := EnforcementKickRPC(f.ctx, NewRuntimeGoLogger(zap.NewNop()), nil, f.nk, f.payload(`,"string_properties":{"":"x"}`))
	if err == nil {
		t.Fatal("an empty property key must be refused")
	}
	keepLatestStorage(f.nk.mockEnforcementNakamaModule)
	j := NewGuildEnforcementJournal(f.targetID)
	if err := StorableRead(f.ctx, f.nk, f.targetID, j, false); err == nil && len(j.GroupRecords(f.groupID)) != 0 {
		t.Fatal("a refused kick must write no record")
	}
}

// The portal sends community_values_required; the older key keeps working.
func TestEnforcementKickRPC_AcceptsPortalCommunityValuesKey(t *testing.T) {
	for _, key := range []string{"community_values_required", "require_community_values"} {
		t.Run(key, func(t *testing.T) {
			f := newKickLogFixture(t)
			useSuspensionLogSender(t, &fakeNoticeSender{})
			runKickRPC(t, f, f.payload(fmt.Sprintf(`,%q:true`, key)))
			r := onlyRecord(t, f)
			if !r.CommunityValuesRequired {
				t.Fatalf("%s:true must require community values", key)
			}
			if r.StringProperties[StringPropertyCommunityValuesRequired] != "true" {
				t.Fatalf("property = %v", r.StringProperties)
			}
		})
	}
}

func assertUnknownKeysLogged(t *testing.T, logs *observer.ObservedLogs, want []string, secret string) {
	t.Helper()
	var found bool
	for _, e := range logs.All() {
		for _, fld := range e.Context {
			if strings.Contains(fmt.Sprint(fld.Interface, fld.String), secret) {
				t.Fatalf("a value was logged: %+v", e)
			}
		}
		if strings.Contains(e.Message, secret) {
			t.Fatalf("a value was logged: %q", e.Message)
		}
		if e.Level == zapcore.WarnLevel && strings.Contains(e.Message, "unknown") {
			if got := fmt.Sprint(e.ContextMap()["unknown_keys"]); got == fmt.Sprint(want) {
				found = true
			}
		}
	}
	if !found {
		t.Fatalf("no warn naming unknown keys %v; logs: %+v", want, logs.All())
	}
}

func TestEnforcementKickRPC_LogsUnknownKeyNamesOnly(t *testing.T) {
	f := newKickLogFixture(t)
	useSuspensionLogSender(t, &fakeNoticeSender{})
	logger, logs := propsObservedLogger()
	out, err := EnforcementKickRPC(f.ctx, logger, nil, f.nk, f.payload(`,"tier":"SECRETVALUE","rule_violated":"SECRETVALUE"`))
	if err != nil {
		t.Fatalf("kick: %v (%s)", err, out)
	}
	assertUnknownKeysLogged(t, logs, []string{"rule_violated", "tier"}, "SECRETVALUE")
}

func runEditRPC(t *testing.T, f *kickLogFixture, logger runtime.Logger, payload string) (GuildEnforcementRecord, error) {
	t.Helper()
	keepLatestStorage(f.nk.mockEnforcementNakamaModule)
	l := logger
	if l == nil {
		l = NewRuntimeGoLogger(zap.NewNop())
	}
	out, err := EnforcementRecordEditRPC(f.ctx, l, nil, f.nk, payload)
	if err != nil {
		return GuildEnforcementRecord{}, err
	}
	var resp EnforcementRecordEditResponse
	if err := json.Unmarshal([]byte(out), &resp); err != nil || resp.Record == nil {
		t.Fatalf("edit response %q: %v", out, err)
	}
	return *resp.Record, nil
}

func (f *kickLogFixture) editPayload(recordID, extra string) string {
	return fmt.Sprintf(`{"group_id":%q,"target_user_id":%q,"record_id":%q%s}`, f.groupID, f.targetID, recordID, extra)
}

func TestEnforcementRecordEditRPC_StringProperties(t *testing.T) {
	f := newKickLogFixture(t)
	useSuspensionLogSender(t, &fakeNoticeSender{})
	runKickRPC(t, f, f.payload(`,"string_properties":{"tier":"high","source":"portal"}`))
	rec := onlyRecord(t, f)

	// A properties-only edit is a change: merged, "" deletes, nakama keys kept.
	got, err := runEditRPC(t, f, nil, f.editPayload(rec.ID, `,"string_properties":{"tier":"low","source":"","permanent":"true"}`))
	if err != nil {
		t.Fatalf("a properties-only edit must be accepted: %v", err)
	}
	if got.StringProperties["tier"] != "low" {
		t.Fatalf("tier not changed: %v", got.StringProperties)
	}
	if _, ok := got.StringProperties["source"]; ok {
		t.Fatalf("an empty value must delete the key: %v", got.StringProperties)
	}
	if got.StringProperties[StringPropertyPermanent] != "false" {
		t.Fatalf("permanent is nakama-owned: %v", got.StringProperties)
	}
	last := got.EditLog[len(got.EditLog)-1]
	if last.PreviousStringProperties["tier"] != "high" || last.NewStringProperties["tier"] != "low" {
		t.Fatalf("edit log = %+v", last)
	}

	// Extending to lifetime recomputes permanent; community values can be set.
	lifetime := rec.CreatedAt.Add(LifetimeSuspensionDuration).Unix()
	got, err = runEditRPC(t, f, nil, f.editPayload(rec.ID, fmt.Sprintf(`,"expiry":%d,"community_values_required":true`, lifetime)))
	if err != nil {
		t.Fatalf("edit: %v", err)
	}
	if got.StringProperties[StringPropertyPermanent] != "true" || got.StringProperties[StringPropertyCommunityValuesRequired] != "true" || !got.CommunityValuesRequired {
		t.Fatalf("after lifetime + community values edit: cvr=%v props=%v", got.CommunityValuesRequired, got.StringProperties)
	}
	if got.StringProperties["tier"] != "low" {
		t.Fatalf("an edit without string_properties must keep them: %v", got.StringProperties)
	}

	// A properties edit that changes nothing is still "no changes".
	if _, err := runEditRPC(t, f, nil, f.editPayload(rec.ID, `,"string_properties":{"tier":"low"}`)); err == nil {
		t.Fatal("an edit that changes nothing must be refused")
	}
	// Invalid properties are refused.
	if _, err := runEditRPC(t, f, nil, f.editPayload(rec.ID, `,"string_properties":{"":"x"}`)); err == nil {
		t.Fatal("an empty key must be refused")
	}
}

func TestEnforcementRecordEditRPC_LogsUnknownKeyNamesOnly(t *testing.T) {
	f := newKickLogFixture(t)
	useSuspensionLogSender(t, &fakeNoticeSender{})
	runKickRPC(t, f, f.payload(""))
	rec := onlyRecord(t, f)
	logger, logs := propsObservedLogger()
	if _, err := runEditRPC(t, f, logger, f.editPayload(rec.ID, `,"user_notice":"changed","tier":"SECRETVALUE"`)); err != nil {
		t.Fatalf("edit: %v", err)
	}
	assertUnknownKeysLogged(t, logs, []string{"tier"}, "SECRETVALUE")
}

// --- filter side -------------------------------------------------------------

func propRecord(duration time.Duration, props map[string]string) GuildEnforcementRecord {
	now := time.Now()
	return GuildEnforcementRecord{CreatedAt: now, Expiry: now.Add(duration), StringProperties: props}
}

func rule(k, v string) SuspensionInheritanceRule { return SuspensionInheritanceRule{Key: k, Value: v} }

func TestInheritanceFilter_StringPropertyRules(t *testing.T) {
	high := map[string]string{"tier": "high"}
	low := map[string]string{"tier": "low"}
	for _, tc := range []struct {
		name     string
		f        SuspensionInheritanceFilter
		r        GuildEnforcementRecord
		inherits bool
	}{
		{"exclude match is not inherited", SuspensionInheritanceFilter{ExcludeRules: []SuspensionInheritanceRule{rule("tier", "low")}}, propRecord(time.Hour, low), false},
		{"exclude miss falls to default include", SuspensionInheritanceFilter{ExcludeRules: []SuspensionInheritanceRule{rule("tier", "low")}}, propRecord(time.Hour, high), true},
		{"include match beats default exclude", SuspensionInheritanceFilter{Default: InheritDefaultExclude, IncludeRules: []SuspensionInheritanceRule{rule("tier", "high")}}, propRecord(time.Hour, high), true},
		{"include miss falls to default exclude", SuspensionInheritanceFilter{Default: InheritDefaultExclude, IncludeRules: []SuspensionInheritanceRule{rule("tier", "high")}}, propRecord(time.Hour, low), false},
		{"exclude beats include", SuspensionInheritanceFilter{IncludeRules: []SuspensionInheritanceRule{rule("tier", "high")}, ExcludeRules: []SuspensionInheritanceRule{rule("source", "bot")}}, propRecord(time.Hour, map[string]string{"tier": "high", "source": "bot"}), false},
		{"any exclude rule matches", SuspensionInheritanceFilter{ExcludeRules: []SuspensionInheritanceRule{rule("tier", "low"), rule("tier", "high")}}, propRecord(time.Hour, high), false},
		{"untagged falls to default exclude", SuspensionInheritanceFilter{Default: InheritDefaultExclude, IncludeRules: []SuspensionInheritanceRule{rule("tier", "high")}}, propRecord(time.Hour, nil), false},
		{"untagged falls to default include", SuspensionInheritanceFilter{ExcludeRules: []SuspensionInheritanceRule{rule("tier", "low")}}, propRecord(time.Hour, nil), true},
		{"exact match: case differs", SuspensionInheritanceFilter{ExcludeRules: []SuspensionInheritanceRule{rule("tier", "low")}}, propRecord(time.Hour, map[string]string{"tier": "Low"}), true},
		{"exact match: no wildcard", SuspensionInheritanceFilter{ExcludeRules: []SuspensionInheritanceRule{rule("tier", "*")}}, propRecord(time.Hour, low), true},
		{"exact match: key case differs", SuspensionInheritanceFilter{ExcludeRules: []SuspensionInheritanceRule{rule("Tier", "low")}}, propRecord(time.Hour, low), true},
		{"empty value rule does not match a missing key", SuspensionInheritanceFilter{ExcludeRules: []SuspensionInheritanceRule{rule("tier", "")}}, propRecord(time.Hour, nil), true},
		{"old record: derived permanent matches exclude", SuspensionInheritanceFilter{ExcludeRules: []SuspensionInheritanceRule{rule(StringPropertyPermanent, "true")}}, propRecord(LifetimeSuspensionDuration, nil), false},
		{"permanent faces exclude even with a minimum duration", SuspensionInheritanceFilter{MinimumDurationSeconds: 3600, ExcludeRules: []SuspensionInheritanceRule{rule("tier", "low")}}, propRecord(LifetimeSuspensionDuration, low), false},
		{"permanent with a minimum duration still falls to default exclude", SuspensionInheritanceFilter{MinimumDurationSeconds: 3600, Default: InheritDefaultExclude}, propRecord(LifetimeSuspensionDuration, nil), false},
		{"minimum duration still rejects before include", SuspensionInheritanceFilter{MinimumDurationSeconds: 7200, IncludeRules: []SuspensionInheritanceRule{rule("tier", "high")}}, propRecord(time.Hour, high), false},
		{"scope still rejects before include", SuspensionInheritanceFilter{Scope: InheritScopeFullBans, IncludeRules: []SuspensionInheritanceRule{rule("tier", "high")}}, GuildEnforcementRecord{CreatedAt: time.Now(), Expiry: time.Now().Add(time.Hour), AllowPrivateLobbies: true, StringProperties: high}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.f.Accepts(tc.r); got != tc.inherits {
				t.Fatalf("Accepts = %v, want %v", got, tc.inherits)
			}
		})
	}
}

// A guild that inherits today, with none of the new fields, inherits a tagged
// record exactly as before: default = include.
func TestInheritanceFilter_ExistingFiltersInheritTaggedRecords(t *testing.T) {
	for _, f := range []SuspensionInheritanceFilter{{}, {Scope: InheritScopeAll}, {MinimumDurationSeconds: 60}} {
		if !f.Accepts(propRecord(time.Hour, map[string]string{"tier": "low", StringPropertyPermanent: "false"})) {
			t.Fatalf("%+v must inherit a tagged record", f)
		}
	}
}

func TestInheritanceFilter_RulesAreNotDefault(t *testing.T) {
	for _, f := range []SuspensionInheritanceFilter{
		{ExcludeRules: []SuspensionInheritanceRule{rule("tier", "low")}},
		{IncludeRules: []SuspensionInheritanceRule{rule("tier", "high")}},
		{Default: InheritDefaultExclude},
	} {
		if f.IsDefault() {
			t.Errorf("%+v must not be default", f)
		}
	}
	if !(SuspensionInheritanceFilter{Default: InheritDefaultInclude}).IsDefault() {
		t.Error("default include alone inherits everything")
	}

	inheritance := map[string][]string{}
	filters := InheritanceFilters{}
	md := &GroupMetadata{
		SuspensionInheritanceGroupIDs: []string{inhSrc},
		SuspensionInheritanceFilters:  map[string]SuspensionInheritanceFilter{inhSrc: {ExcludeRules: []SuspensionInheritanceRule{rule("tier", "low")}}},
	}
	addInheritanceLinks(inhChild, md, inheritance, filters)
	if len(filters.For(inhSrc, inhChild).ExcludeRules) != 1 {
		t.Fatalf("a rules-only filter must be carried into the registry: %v", filters)
	}
}

func TestSuspensionInheritanceFilter_ValidateRules(t *testing.T) {
	var many []SuspensionInheritanceRule
	for i := range maxInheritanceRulesList + 1 {
		many = append(many, rule("k", fmt.Sprint(i)))
	}
	long := strings.Repeat("x", maxStringPropertyBytes+1)
	for _, tc := range []struct {
		name string
		f    SuspensionInheritanceFilter
		ok   bool
	}{
		{"exclude tier low", SuspensionInheritanceFilter{ExcludeRules: []SuspensionInheritanceRule{rule("tier", "low")}}, true},
		{"default exclude + include", SuspensionInheritanceFilter{Default: InheritDefaultExclude, IncludeRules: []SuspensionInheritanceRule{rule("tier", "high")}}, true},
		{"default include", SuspensionInheritanceFilter{Default: InheritDefaultInclude}, true},
		{"empty value is allowed", SuspensionInheritanceFilter{ExcludeRules: []SuspensionInheritanceRule{rule("tier", "")}}, true},
		{"unknown default", SuspensionInheritanceFilter{Default: "maybe"}, false},
		{"empty key", SuspensionInheritanceFilter{ExcludeRules: []SuspensionInheritanceRule{rule("", "x")}}, false},
		{"long key", SuspensionInheritanceFilter{IncludeRules: []SuspensionInheritanceRule{rule(long, "x")}}, false},
		{"long value", SuspensionInheritanceFilter{IncludeRules: []SuspensionInheritanceRule{rule("k", long)}}, false},
		{"too many", SuspensionInheritanceFilter{ExcludeRules: many}, false},
		{"duplicate in one list", SuspensionInheritanceFilter{ExcludeRules: []SuspensionInheritanceRule{rule("tier", "low"), rule("tier", "low")}}, false},
		{"same rule in both lists", SuspensionInheritanceFilter{IncludeRules: []SuspensionInheritanceRule{rule("tier", "low")}, ExcludeRules: []SuspensionInheritanceRule{rule("tier", "low")}}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.f.Validate(); (err == nil) != tc.ok {
				t.Fatalf("Validate() = %v, want ok=%v", err, tc.ok)
			}
		})
	}
}

func TestGroupMetadata_FiltersJSON_Rules(t *testing.T) {
	in := `{"suspension_inheritence_group_ids":["` + inhSrc + `"],"suspension_inheritance_filters":{"` + inhSrc +
		`":{"include_rules":[{"key":"tier","value":"high"}],"exclude_rules":[{"key":"permanent","value":"false"}],"default":"exclude"}}}`
	var md GroupMetadata
	if err := json.Unmarshal([]byte(in), &md); err != nil {
		t.Fatal(err)
	}
	f := md.InheritanceFilterFor(inhSrc)
	if f.Default != InheritDefaultExclude || len(f.IncludeRules) != 1 || f.IncludeRules[0] != rule("tier", "high") ||
		len(f.ExcludeRules) != 1 || f.ExcludeRules[0] != rule("permanent", "false") {
		t.Fatalf("filter = %+v", f)
	}
	b, _ := json.Marshal(&md)
	for _, k := range []string{`"include_rules":[{"key":"tier","value":"high"}]`, `"exclude_rules":[{"key":"permanent","value":"false"}]`, `"default":"exclude"`} {
		if !strings.Contains(string(b), k) {
			t.Fatalf("%s missing from %s", k, b)
		}
	}
}

// End to end through CheckEnforcementSuspensions: the child excludes tier=low,
// the other inheritor has no filter.
func TestInheritanceFilter_StringPropertiesEndToEnd(t *testing.T) {
	j := NewGuildEnforcementJournal("u")
	r := j.AddRecord(inhSrc, "e", "ed", "n", "", false, false, 24*time.Hour)
	j.SetRecordStringProperties(inhSrc, r.ID, map[string]string{"tier": "low"})
	got := inhCheck(t, GuildEnforcementJournalList{"u": j}, inhFilter(SuspensionInheritanceFilter{ExcludeRules: []SuspensionInheritanceRule{rule("tier", "low")}}))
	if len(got[inhChild]) != 0 {
		t.Fatalf("child excludes tier=low but inherited: %v", got[inhChild])
	}
	if len(got[inhOther]) == 0 {
		t.Fatalf("unfiltered inheritor must inherit: %v", got)
	}
}
