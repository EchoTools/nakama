package evr

import (
	"reflect"
	"strings"
	"testing"
)

// jsonFieldValues maps each field's json key to its value, so a parsed struct
// can be compared key-for-key with the object it was parsed from.
func jsonFieldValues(t *testing.T, v any) map[string]float64 {
	t.Helper()
	rv := reflect.ValueOf(v)
	rt := rv.Type()
	out := make(map[string]float64, rt.NumField())
	for i := 0; i < rt.NumField(); i++ {
		name, _, _ := strings.Cut(rt.Field(i).Tag.Get("json"), ",")
		if name == "" || name == "-" {
			continue
		}
		f := rv.Field(i)
		switch {
		case f.CanInt():
			out[name] = float64(f.Int())
		case f.CanFloat():
			out[name] = f.Float()
		default:
			t.Fatalf("field %s (json %q) is %s, not a number", rt.Field(i).Name, name, f.Kind())
		}
	}
	return out
}

func assertEveryKeyKept(t *testing.T, sent map[string]float64, parsed any) {
	t.Helper()
	got := jsonFieldValues(t, parsed)
	for key, want := range sent {
		v, ok := got[key]
		if !ok {
			t.Errorf("%s: the game sent %q (=%v) and %T has no field for it, so json.Unmarshal dropped it", key, key, want, parsed)
			continue
		}
		if v != want {
			t.Errorf("%s: parsed %v, the game sent %v", key, v, want)
		}
	}
}

func TestRemoteLogPostMatchTypeStats_CombatSampleKeepsEveryKey(t *testing.T) {
	raw := readRemoteLogFixture(t, "post_match_match_type_stats_combat.json")

	parsed, err := UnmarshalRemoteLog(raw)
	if err != nil {
		t.Fatalf("UnmarshalRemoteLog: %v", err)
	}
	msg, ok := parsed.(*RemoteLogPostMatchTypeStats)
	if !ok {
		t.Fatalf("parsed as %T, want *RemoteLogPostMatchTypeStats", parsed)
	}
	if msg.MatchType != "Echo_Combat" {
		t.Fatalf("match_type = %q, want Echo_Combat", msg.MatchType)
	}

	assertEveryKeyKept(t, fixtureStatsObject(t, raw, "stats"), msg.Stats)
}

func TestRemoteLogPostMatchMatchStats_CombatSampleKeepsEveryKey(t *testing.T) {
	raw := readRemoteLogFixture(t, "post_match_match_stats_combat.json")

	parsed, err := UnmarshalRemoteLog(raw)
	if err != nil {
		t.Fatalf("UnmarshalRemoteLog: %v", err)
	}
	msg, ok := parsed.(*RemoteLogPostMatchMatchStats)
	if !ok {
		t.Fatalf("parsed as %T, want *RemoteLogPostMatchMatchStats", parsed)
	}
	if msg.MatchType != "Echo_Combat_Private" {
		t.Fatalf("match_type = %q, want Echo_Combat_Private", msg.MatchType)
	}

	assertEveryKeyKept(t, fixtureStatsObject(t, raw, "match_stats"), msg.MatchStats)
}

// The eight match_stats keys nakama had no name for at all. The private-match
// fixture carries three of them; this pins the other five by name, so a typo
// in a tag fails here rather than silently dropping the stat in production.
func TestMatchStats_NamesTheEightUnnamedCombatKeys(t *testing.T) {
	got := jsonFieldValues(t, MatchStats{})
	for _, key := range []string{
		"CombatHealingReceived",
		"CombatStunsReceived",
		"CombatScans",
		"CombatScansReceived",
		"CombatSuicides",
		"CombatSelfDamage",
		"CombatCurrentEliminationStreak",
		"CombatEliminationDeathRatio",
	} {
		if _, ok := got[key]; !ok {
			t.Errorf("MatchStats has no field with json key %q", key)
		}
	}
}

// A combat key the game prints with a decimal point must not fail the parse of
// the whole message. An int64 field rejects "3.000000" with an error, and
// UnmarshalRemoteLog then drops the entire POST_MATCH_MATCH_TYPE_STATS --
// including the arena fields every combat match already relies on today.
func TestRemoteLogPostMatchStats_CombatIntegersSentAsDecimalsStillParse(t *testing.T) {
	for _, tc := range []struct {
		name, raw string
	}{
		{"type stats", `{"message_type":"POST_MATCH_MATCH_TYPE_STATS","match_type":"Echo_Combat","stats":{"CombatKills":7.000000,"CombatWins":1.000000,"CombatBestEliminationStreak":4.000000}}`},
		{"match stats", `{"message_type":"POST_MATCH_MATCH_STATS","match_type":"Echo_Combat","match_stats":{"CombatKills":5.000000,"CombatScans":2.000000,"CombatSuicides":1.000000}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := UnmarshalRemoteLog([]byte(tc.raw)); err != nil {
				t.Fatalf("UnmarshalRemoteLog: %v", err)
			}
		})
	}
}

// Values is what a match record keeps. On the fixture it must give back
// exactly the object the game sent: no key lost, none invented.
func TestMatchStats_ValuesIsWhatTheGameSent(t *testing.T) {
	raw := readRemoteLogFixture(t, "post_match_match_stats_combat.json")
	parsed, err := UnmarshalRemoteLog(raw)
	if err != nil {
		t.Fatalf("UnmarshalRemoteLog: %v", err)
	}
	got := parsed.(*RemoteLogPostMatchMatchStats).MatchStats.Values()
	sent := fixtureStatsObject(t, raw, "match_stats")
	if !reflect.DeepEqual(got, sent) {
		t.Fatalf("Values() = %v\nthe game sent %v", got, sent)
	}
}

func combatJSONKeys(t *testing.T, v any) map[string]bool {
	t.Helper()
	rt := reflect.TypeOf(v)
	out := map[string]bool{}
	for i := 0; i < rt.NumField(); i++ {
		name, _, _ := strings.Cut(rt.Field(i).Tag.Get("json"), ",")
		if strings.HasPrefix(name, "Combat") {
			out[name] = true
		}
	}
	return out
}

// CombatStatistics is the combat schema: every combat key the game sends, with
// its op and type. It cannot parse the remote logs itself -- they carry bare
// numbers, it carries {"val","cnt","op"} -- so MatchTypeStats and MatchStats
// mirror its names in the wire shape. This pins the mirror: the two remote-log
// structs together name exactly CombatStatistics' combat keys, and every key in
// both real fixtures is a CombatStatistics key.
func TestCombatStatistics_IsTheSchemaTheRemoteLogStructsMirror(t *testing.T) {
	schema := combatJSONKeys(t, CombatStatistics{})
	mirror := combatJSONKeys(t, MatchTypeStats{})
	for k := range combatJSONKeys(t, MatchStats{}) {
		mirror[k] = true
	}
	for k := range mirror {
		if !schema[k] {
			t.Errorf("%s is parsed from the remote log but CombatStatistics has no field for it", k)
		}
	}
	for k := range schema {
		if !mirror[k] {
			t.Errorf("%s is in CombatStatistics but neither remote-log struct parses it", k)
		}
	}

	cs := reflect.TypeOf(CombatStatistics{})
	names := map[string]bool{}
	for i := 0; i < cs.NumField(); i++ {
		name, _, _ := strings.Cut(cs.Field(i).Tag.Get("json"), ",")
		names[name] = true
		if cs.Field(i).Name != name && name != "-" {
			// buildPlayerStatisticsMaps names boards by the Go field name and
			// StatisticsToEntries by the json name; they must agree.
			t.Errorf("CombatStatistics.%s has json name %q", cs.Field(i).Name, name)
		}
	}
	for _, fixture := range []struct{ file, key string }{
		{"post_match_match_type_stats_combat.json", "stats"},
		{"post_match_match_stats_combat.json", "match_stats"},
	} {
		for k := range fixtureStatsObject(t, readRemoteLogFixture(t, fixture.file), fixture.key) {
			if !names[k] {
				t.Errorf("%s: the game sent %q and CombatStatistics has no field for it", fixture.file, k)
			}
		}
	}
}
