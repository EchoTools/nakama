package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"slices"
	"testing"
	"time"

	"github.com/gofrs/uuid/v5"
	"github.com/heroiclabs/nakama-common/api"
	"github.com/heroiclabs/nakama-common/runtime"
	"github.com/heroiclabs/nakama/v3/server/evr"
	"go.uber.org/zap"
	"google.golang.org/protobuf/types/known/wrapperspb"
)

// ---- invite/regions -----------------------------------------------------------

type testRegionRow struct {
	Code      string `json:"code"`
	Location  string `json:"location"`
	PingMin   int    `json:"ping_min_ms"`
	PingMax   int    `json:"ping_max_ms"`
	Available int    `json:"available"`
	Total     int    `json:"total"`
	InRange   bool   `json:"in_range"`
}

func (w *inviteTestWorld) regions(userID string, payload any) ([]testRegionRow, error) {
	w.t.Helper()
	if _, ok := w.groups[userID]; !ok {
		w.groups[userID] = uuid.Must(uuid.NewV4())
	}
	out, err := w.call(withInviteRecord("regions", InviteRegionsRPC), userID, payload)
	if err != nil {
		return nil, err
	}
	var rows []testRegionRow
	if err := json.Unmarshal([]byte(out), &rows); err != nil {
		w.t.Fatalf("regions answer %q: %v", out, err)
	}
	return rows, nil
}

func region(code, loc string, minPing, maxPing, avail, total int) *RegionAutocompleteData {
	return &RegionAutocompleteData{RegionCode: code, Location: loc, MinPing: minPing, MaxPing: maxPing, Available: avail, Total: total}
}

// A region set as /create would list it: nearest first, unmeasured last.
func fiveRegions() inviteRegionSet {
	listed := []*RegionAutocompleteData{
		region("us-ca-losangeles", "Los Angeles, CA, US", 20, 30, 2, 3),
		region("us-or-portland", "Portland, OR, US", 60, 60, 1, 1),
		region("us-va-ashburn", "Ashburn, VA, US", 120, 130, 4, 4),
		region("de-he-frankfurt", "Frankfurt, HE, DE", 0, 0, 1, 2),
	}
	known := map[string]bool{"us-tx-dallas": true} // known to the guild, but no free server right now
	for _, r := range listed {
		known[r.RegionCode] = true
	}
	return inviteRegionSet{Listed: listed, Known: known}
}

func TestInviteRegions_NoHistoryIsATokenAndAnInvalidArgument(t *testing.T) {
	w := newInviteWorld(t)
	w.regionSet = fiveRegions()
	w.noHistory[userA] = true

	_, err := w.regions(userA, map[string]any{})
	if err == nil {
		t.Fatal("an account with no latency history must be refused")
	}
	if got := inviteToken(t, err); got != "no_history" {
		t.Errorf("token = %q, want no_history", got)
	}
	var rerr *runtime.Error
	if !errors.As(err, &rerr) || rerr.Code != StatusInvalidArgument {
		t.Errorf("code = %+v, want %d (INVALID_ARGUMENT, HTTP 400)", rerr, StatusInvalidArgument)
	}
	if w.regionCall != 0 {
		t.Errorf("the region list was computed for an account that has no history")
	}
}

func TestInviteRegions_DefaultFirstThenCreatesOrderWithInRangeFlag(t *testing.T) {
	w := newInviteWorld(t)
	w.regionSet = fiveRegions()

	rows, err := w.regions(userA, map[string]any{})
	if err != nil {
		t.Fatalf("invite/regions: %v", err)
	}
	var codes []string
	for _, r := range rows {
		codes = append(codes, r.Code)
	}
	want := []string{"default", "us-ca-losangeles", "us-or-portland", "us-va-ashburn", "de-he-frankfurt"}
	if !slices.Equal(codes, want) {
		t.Fatalf("codes = %v, want %v", codes, want)
	}

	// default is the best server by ping: no name of its own, the nearest ping,
	// and the free/total count over everything on offer.
	if d := rows[0]; d.Location != "" || d.PingMin != 20 || d.PingMax != 20 || d.Available != 8 || d.Total != 10 || !d.InRange {
		t.Errorf("default = %+v", d)
	}
	for i, wnt := range []testRegionRow{
		{"us-ca-losangeles", "Los Angeles, CA, US", 20, 30, 2, 3, true},
		{"us-or-portland", "Portland, OR, US", 60, 60, 1, 1, true},
		{"us-va-ashburn", "Ashburn, VA, US", 120, 130, 4, 4, false}, // measured, beyond MaxRegionPingMs
		{"de-he-frankfurt", "Frankfurt, HE, DE", 0, 0, 1, 2, false}, // never measured
	} {
		if rows[i+1] != wnt {
			t.Errorf("row %d = %+v, want %+v", i+1, rows[i+1], wnt)
		}
	}
}

func TestInviteRegions_NothingOnOfferStillAnswersDefault(t *testing.T) {
	w := newInviteWorld(t)
	rows, err := w.regions(userA, map[string]any{})
	if err != nil {
		t.Fatalf("invite/regions: %v", err)
	}
	if len(rows) != 1 || rows[0].Code != "default" || rows[0].Available != 0 || rows[0].InRange {
		t.Errorf("rows = %+v, want only a default with nothing available", rows)
	}
}

func TestInviteRegions_RequestIsValidatedLikeCreate(t *testing.T) {
	cases := []struct {
		name string
		body map[string]any
		want string // "" = accepted
	}{
		{"empty request", map[string]any{}, ""},
		{"mode alone", map[string]any{"mode": "arena_private"}, ""},
		{"experience alone", map[string]any{"experience": "social"}, ""},
		{"experience and its mode", map[string]any{"experience": "arena", "mode": "arena_public"}, ""},
		{"unknown experience", map[string]any{"experience": "bowling"}, "invalid"},
		{"mode of another experience", map[string]any{"experience": "arena", "mode": "combat_private"}, "invalid"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := newInviteWorld(t)
			w.regionSet = fiveRegions()
			_, err := w.regions(userA, tc.body)
			switch {
			case tc.want == "" && err != nil:
				t.Fatalf("refused: %v", err)
			case tc.want != "" && err == nil:
				t.Fatal("want a refusal")
			case tc.want != "":
				if got := inviteToken(t, err); got != tc.want {
					t.Errorf("token = %q, want %q", got, tc.want)
				}
			}
		})
	}
}

func TestInviteRegions_NotInAGuildIsDenied(t *testing.T) {
	w := newInviteWorld(t)
	w.regionSet = fiveRegions()
	out, err := w.call(withInviteRecord("regions", InviteRegionsRPC), userA, map[string]any{}) // no group for userA
	if err == nil {
		t.Fatalf("want a refusal, got %q", out)
	}
	if got := inviteToken(t, err); got != "denied" {
		t.Errorf("token = %q, want denied", got)
	}
}

// Opening the row is not a create: no match, no code, and no rate limit.
func TestInviteRegions_ReadOnlyAndNotRateLimited(t *testing.T) {
	w := newInviteWorld(t)
	w.regionSet = fiveRegions()
	for i := 0; i < 40; i++ {
		if _, err := w.regions(userA, map[string]any{}); err != nil {
			t.Fatalf("call %d refused: %v", i, err)
		}
	}
	if len(w.allocs) != 0 {
		t.Errorf("invite/regions allocated %d matches", len(w.allocs))
	}
	if n := w.nk.objectCount(InviteCodeCollection) + w.nk.objectCount(InviteOwnerCollection); n != 0 {
		t.Errorf("invite/regions wrote %d objects", n)
	}
}

func TestInviteRegions_WritesOneRecordPerCall(t *testing.T) {
	w := newInviteWorld(t)
	w.regionSet = fiveRegions()
	w.noHistory[userB] = true
	if _, err := w.regions(userA, map[string]any{"experience": "arena"}); err != nil {
		t.Fatal(err)
	}
	if _, err := w.regions(userB, map[string]any{}); err == nil {
		t.Fatal("want no_history")
	}
	recs := w.logs.FilterMessage("RPC").All()
	if len(recs) != 2 {
		t.Fatalf("%d RPC records for 2 calls: %v", len(recs), w.logs.All())
	}
	for i, wnt := range []struct{ user, outcome string }{{userA, "ok"}, {userB, "no_history"}} {
		f := recs[i].ContextMap()
		if f["rpc_id"] != "invite/regions" || f["user_id"] != wnt.user || f["outcome"] != wnt.outcome {
			t.Errorf("record %d = %v, want invite/regions %+v", i, f, wnt)
		}
		if _, ok := f["group_id"]; !ok {
			t.Errorf("record %d lacks the resolved group id: %v", i, f)
		}
	}
}

// ---- invite/create ------------------------------------------------------------

func TestInviteCreate_NoHistoryIsATokenAndComesBeforeTheAllocation(t *testing.T) {
	w := newInviteWorld(t)
	w.groups[userA] = uuid.Must(uuid.NewV4())
	w.noHistory[userA] = true
	// handleCreateMatch spends the creator's rate token before it reads the
	// history; the refusal must not depend on reaching it.
	w.failAs = errors.New("rate")

	_, err := w.call(withInviteRecord("create", InviteCreateRPC), userA, map[string]any{"experience": "arena"})
	if err == nil {
		t.Fatal("want a refusal")
	}
	if got := inviteToken(t, err); got != "no_history" {
		t.Errorf("token = %q, want no_history", got)
	}
	var rerr *runtime.Error
	if !errors.As(err, &rerr) || rerr.Code != StatusInvalidArgument {
		t.Errorf("code = %+v, want %d", rerr, StatusInvalidArgument)
	}
	if len(w.allocs) != 0 {
		t.Errorf("a create with no history reached the allocator %d times", len(w.allocs))
	}
	if n := w.nk.objectCount(InviteCodeCollection); n != 0 {
		t.Errorf("a refused create left %d code objects", n)
	}
}

// The create path itself can learn there is no history (the account's history
// was pruned between the check and the allocation): that is no_history as well,
// never unavailable.
func TestInviteCreate_AllocatorsNoHistoryIsATokenToo(t *testing.T) {
	w := newInviteWorld(t)
	w.groups[userA] = uuid.Must(uuid.NewV4())
	w.failAs = fmt.Errorf("failed to allocate: %w", ErrNoLatencyHistory)
	_, err := w.call(withInviteRecord("create", InviteCreateRPC), userA, map[string]any{"experience": "arena"})
	if err == nil {
		t.Fatal("want a refusal")
	}
	if got := inviteToken(t, err); got != "no_history" {
		t.Errorf("token = %q, want no_history", got)
	}
}

func TestInviteCreate_UnavailableStillMeansHistoryButNoServer(t *testing.T) {
	w := newInviteWorld(t)
	w.groups[userA] = uuid.Must(uuid.NewV4())
	w.failAs = errors.New("failed to allocate game server: no servers")
	_, err := w.call(withInviteRecord("create", InviteCreateRPC), userA, map[string]any{"experience": "arena"})
	if err == nil {
		t.Fatal("want a refusal")
	}
	if got := inviteToken(t, err); got != "unavailable" {
		t.Errorf("token = %q, want unavailable", got)
	}
}

func TestInviteCreate_RegionCodes(t *testing.T) {
	cases := []struct {
		name       string
		region     string
		want       string // "" = allocated
		wantRegion string
		wantLookup bool
	}{
		{"a code invite/regions returned", "us-or-portland", "", "us-or-portland", true},
		{"the nearest region", "us-ca-losangeles", "", "us-ca-losangeles", true},
		{"a region never measured", "de-he-frankfurt", "", "de-he-frankfurt", true},
		{"known to the guild but full right now", "us-tx-dallas", "", "us-tx-dallas", true},
		{"a made-up code", "us-west", "invalid", "", true},
		{"a code in the wrong case", "US-OR-Portland", "invalid", "", true},
		{"default", "default", "", "default", false},
		{"empty", "", "", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := newInviteWorld(t)
			w.groups[userA] = uuid.Must(uuid.NewV4())
			w.regionSet = fiveRegions()
			if !tc.wantLookup {
				w.regionsErr = errors.New("the region list must not be computed for the default path")
			}
			out, err := w.call(withInviteRecord("create", InviteCreateRPC), userA, map[string]any{"experience": "arena", "region": tc.region})
			if tc.want != "" {
				if err == nil {
					t.Fatalf("want %q, got %q", tc.want, out)
				}
				if got := inviteToken(t, err); got != tc.want {
					t.Errorf("token = %q, want %q", got, tc.want)
				}
				if len(w.allocs) != 0 || w.nk.objectCount(InviteCodeCollection) != 0 {
					t.Errorf("a refused create allocated %d matches and left %d codes", len(w.allocs), w.nk.objectCount(InviteCodeCollection))
				}
				return
			}
			if err != nil {
				t.Fatalf("refused: %v", err)
			}
			if len(w.allocs) != 1 || w.allocs[0].Region != tc.wantRegion {
				t.Errorf("allocations = %+v, want one with region %q", w.allocs, tc.wantRegion)
			}
		})
	}
}

// ---- the computation behind both -----------------------------------------------

// regionTestNakama is the storage double plus the one list call regionsFor makes.
type regionTestNakama struct {
	*inviteTestNakama
	matches []*api.Match
}

func (n *regionTestNakama) MatchList(_ context.Context, _ int, _ bool, _ string, _, _ *int, _ string) ([]*api.Match, error) {
	return n.matches, nil
}

func (n *regionTestNakama) server(extIP, country, region, city string, inUse bool) {
	mode := evr.ModeUnloaded
	if inUse {
		mode = evr.ModeArenaPrivate
	}
	label := &MatchLabel{
		ID:   MatchID{UUID: uuid.Must(uuid.NewV4()), Node: "testnode"},
		Mode: mode,
		GameServer: &GameServerPresence{
			Endpoint:    evr.Endpoint{ExternalIP: net.ParseIP(extIP), Port: 6792}, // a zero port marshals to no endpoint
			CountryCode: country, Region: region, City: city,
		},
	}
	n.matches = append(n.matches, &api.Match{MatchId: label.ID.String(), Label: wrapperspb.String(string(label.GetLabel()))})
}

func (n *regionTestNakama) history(userID string, rtts map[string]time.Duration) {
	h := NewLatencyHistory()
	for ip, rtt := range rtts {
		h.GameServerLatencies[ip] = []LatencyHistoryItem{{Timestamp: time.Now(), RTT: rtt}, {Timestamp: time.Now(), RTT: rtt}}
	}
	b, err := json.Marshal(h)
	if err != nil {
		panic(err)
	}
	n.seedObject(userID, LatencyHistoryStorageCollection, LatencyHistoryStorageKey, string(b))
}

func TestRegionsFor_IsWhatCreateAutocompleteComputes(t *testing.T) {
	nk := &regionTestNakama{inviteTestNakama: newInviteTestNakama()}
	nk.server("198.51.100.1", "US", "CA", "Los Angeles", false)
	nk.server("198.51.100.2", "US", "CA", "Los Angeles", true) // in use: counts toward Total, not Available
	nk.server("198.51.100.3", "DE", "HE", "Frankfurt", false)
	nk.history(userA, map[string]time.Duration{
		"198.51.100.1": 24 * time.Millisecond, // rounds to 20
		"198.51.100.2": 36 * time.Millisecond, // rounds to 40
		"198.51.100.3": 151 * time.Millisecond,
	})
	logger := NewRuntimeGoLogger(zap.NewNop())

	view, err := regionsFor(context.Background(), logger, nk, userA, uuid.Must(uuid.NewV4()).String())
	if err != nil {
		t.Fatal(err)
	}
	if !view.HasHistory {
		t.Fatal("an account with stored RTTs has history")
	}
	la, ok := view.All["us-ca-losangeles"]
	if !ok {
		t.Fatalf("regions = %v", view.All)
	}
	if want := (RegionAutocompleteData{RegionCode: "us-ca-losangeles", Location: "Los Angeles, CA, US", Total: 2, Available: 1, MinPing: 20, MaxPing: 20}); *la != want {
		t.Errorf("los angeles = %+v, want %+v", *la, want)
	}
	if fr := view.All["de-he-frankfurt"]; fr == nil || fr.MinPing != 150 || fr.Total != 1 || fr.Available != 1 {
		t.Errorf("frankfurt = %+v", fr)
	}
}

func TestRegionsFor_NoHistory(t *testing.T) {
	nk := &regionTestNakama{inviteTestNakama: newInviteTestNakama()}
	nk.server("198.51.100.1", "US", "CA", "Los Angeles", false)
	logger := NewRuntimeGoLogger(zap.NewNop())

	view, err := regionsFor(context.Background(), logger, nk, userA, uuid.Must(uuid.NewV4()).String())
	if err != nil {
		t.Fatal(err)
	}
	if view.HasHistory {
		t.Error("an account with no stored RTTs has no history")
	}
	if len(view.All) != 1 {
		t.Errorf("the region list is still computed: %v", view.All)
	}
}

// Equal pings used to come out in map-iteration order, so /create could list the
// same two regions in a different order from one call to the next.
func TestFilterAndSortRegions_TiesBreakByCode(t *testing.T) {
	in := func() map[string]*RegionAutocompleteData {
		return map[string]*RegionAutocompleteData{
			"c": region("c", "C", 40, 50, 1, 1),
			"a": region("a", "A", 40, 50, 1, 1),
			"b": region("b", "B", 40, 50, 1, 1),
			"z": region("z", "Z", 0, 0, 1, 1),
			"y": region("y", "Y", 0, 0, 1, 1),
		}
	}
	for i := 0; i < 50; i++ {
		var codes []string
		for _, r := range filterAndSortRegions(in(), true) {
			codes = append(codes, r.RegionCode)
		}
		if want := []string{"a", "b", "c", "y", "z"}; !slices.Equal(codes, want) {
			t.Fatalf("run %d: %v, want %v", i, codes, want)
		}
	}
}

// invite/regions and /create's autocomplete share one ordering.
func TestFilterAndSortRegions_MatchesChoicesOrder(t *testing.T) {
	datas := map[string]*RegionAutocompleteData{
		"far":  region("far", "Far", 120, 130, 1, 1),
		"near": region("near", "Near", 20, 30, 1, 1),
		"full": region("full", "Full", 10, 10, 0, 3),
		"none": region("none", "None", 0, 0, 1, 1),
	}
	var fromRegions, fromChoices []string
	for _, r := range filterAndSortRegions(datas, true) {
		fromRegions = append(fromRegions, r.RegionCode)
	}
	for _, c := range filterAndSortRegionChoices(datas, true) {
		fromChoices = append(fromChoices, fmt.Sprint(c.Value))
	}
	if !slices.Equal(fromRegions, fromChoices) || !slices.Equal(fromRegions, []string{"near", "far", "none"}) {
		t.Errorf("regions %v, choices %v", fromRegions, fromChoices)
	}
}

func TestInviteRegions_InRangeIsMeasuredAndWithinMaxRegionPingMs(t *testing.T) {
	w := newInviteWorld(t)
	w.regionSet = inviteRegionSet{Listed: []*RegionAutocompleteData{
		region("at-limit", "At", MaxRegionPingMs, MaxRegionPingMs, 1, 1),
		region("past-limit", "Past", MaxRegionPingMs+10, MaxRegionPingMs+10, 1, 1),
	}}
	rows, err := w.regions(userA, map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	if !rows[1].InRange || rows[2].InRange {
		t.Errorf("at %dms in_range=%v, at %dms in_range=%v; want true, false", MaxRegionPingMs, rows[1].InRange, MaxRegionPingMs+10, rows[2].InRange)
	}
}

func TestDefaultInviteHasHistory(t *testing.T) {
	nk := &regionTestNakama{inviteTestNakama: newInviteTestNakama()}
	if has, err := defaultInviteHasHistory(context.Background(), nk, userA); err != nil || has {
		t.Errorf("no stored history: has=%v err=%v, want false", has, err)
	}
	nk.history(userA, map[string]time.Duration{"198.51.100.1": 24 * time.Millisecond})
	if has, err := defaultInviteHasHistory(context.Background(), nk, userA); err != nil || !has {
		t.Errorf("stored history: has=%v err=%v, want true", has, err)
	}
	if has, err := defaultInviteHasHistory(context.Background(), nk, userB); err != nil || has {
		t.Errorf("another account: has=%v err=%v, want false", has, err)
	}
}

// The real list: /create's order, the full region known, and no invented entry
// for a server that has no location.
func TestDefaultInviteRegions(t *testing.T) {
	nk := &regionTestNakama{inviteTestNakama: newInviteTestNakama()}
	nk.server("198.51.100.1", "DE", "HE", "Frankfurt", false)
	nk.server("198.51.100.2", "US", "CA", "Los Angeles", false)
	nk.server("198.51.100.3", "US", "TX", "Dallas", true) // full
	nk.server("198.51.100.4", "", "", "", false)          // no location, so no region code
	nk.history(userA, map[string]time.Duration{
		"198.51.100.1": 150 * time.Millisecond,
		"198.51.100.2": 20 * time.Millisecond,
		"198.51.100.3": 30 * time.Millisecond,
		"198.51.100.4": 10 * time.Millisecond,
	})
	set, err := defaultInviteRegions(context.Background(), NewRuntimeGoLogger(zap.NewNop()), nk, userA, uuid.Must(uuid.NewV4()))
	if err != nil {
		t.Fatal(err)
	}
	var listed []string
	for _, r := range set.Listed {
		listed = append(listed, r.RegionCode)
	}
	if want := []string{"us-ca-losangeles", "de-he-frankfurt"}; !slices.Equal(listed, want) {
		t.Errorf("listed = %v, want %v (nearest first; the full and the location-less excluded)", listed, want)
	}
	for code, want := range map[string]bool{"us-ca-losangeles": true, "de-he-frankfurt": true, "us-tx-dallas": true, "": false, "us-west": false} {
		if set.Known[code] != want {
			t.Errorf("Known[%q] = %v, want %v", code, set.Known[code], want)
		}
	}
}

// The default row is the nearest measured region whatever order the list arrives in.
func TestInviteRegions_DefaultTakesTheNearestMeasuredPing(t *testing.T) {
	w := newInviteWorld(t)
	w.regionSet = inviteRegionSet{Listed: []*RegionAutocompleteData{
		region("b", "B", 80, 80, 1, 1),
		region("c", "C", 0, 0, 1, 1),
		region("a", "A", 30, 40, 1, 1),
	}}
	rows, err := w.regions(userA, map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	if rows[0].PingMin != 30 || rows[0].PingMax != 30 {
		t.Errorf("default = %+v, want ping 30", rows[0])
	}
}

// The RPC as the server registers it writes its record under its own name.
func TestInviteRegions_RegisteredHandlerRecordsAsInviteRegions(t *testing.T) {
	w := newInviteWorld(t)
	w.regionSet = fiveRegions()
	w.groups[userA] = uuid.Must(uuid.NewV4())
	var handler func(context.Context, runtime.Logger, *sql.DB, runtime.NakamaModule, string) (string, error)
	for _, r := range buildEVRRPCRegistrations(&RPCHandler{}, nil) {
		if r.ID == "invite/regions" {
			handler = r.Handler
		}
	}
	if handler == nil {
		t.Fatal("invite/regions is not registered")
	}
	if _, err := w.call(handler, userA, map[string]any{}); err != nil {
		t.Fatal(err)
	}
	recs := w.logs.FilterMessage("RPC").All()
	if len(recs) != 1 || recs[0].ContextMap()["rpc_id"] != "invite/regions" {
		t.Errorf("records = %v", w.logs.All())
	}
}
