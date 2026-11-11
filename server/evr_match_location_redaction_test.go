package server

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"io"
	"net"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/gofrs/uuid/v5"
	"github.com/heroiclabs/nakama-common/api"
	"github.com/heroiclabs/nakama-common/runtime"
	"github.com/heroiclabs/nakama/v3/server/evr"
	"google.golang.org/protobuf/types/known/wrapperspb"
)

// Synthetic values only (documentation ranges); the player values are searched
// for verbatim in the marshalled response, so a field renamed to dodge a key
// check still fails the test.
const (
	testLocPlayerIP      = "203.0.113.77"
	testLocPlayerGeoHash = "gbsuv7ztq"
	testLocServerGeoHash = "u4pruydqq"
	testLocServerCity    = "Testville"
	testLocServerRegion  = "us-test-1"
	testLocServerCountry = "ZZ"
	testLocServerIP      = "198.51.100.9"
)

// playerLocationJSONKeys are the JSON keys of a player entry that say where the
// player is. Server location is not in this list: where a server runs is not a
// player's location.
var playerLocationJSONKeys = []string{"client_ip", "geohash"}

// stubMembershipDriver answers CheckSystemGroupMembership's one query: the DSN
// "member" returns a row (caller is in the group), anything else returns none.
type stubMembershipDriver struct{}

func (stubMembershipDriver) Open(name string) (driver.Conn, error) {
	return &stubMembershipConn{member: name == "member"}, nil
}

type stubMembershipConn struct{ member bool }

func (c *stubMembershipConn) Prepare(query string) (driver.Stmt, error) {
	return &stubMembershipStmt{member: c.member}, nil
}
func (c *stubMembershipConn) Close() error              { return nil }
func (c *stubMembershipConn) Begin() (driver.Tx, error) { return nil, driver.ErrSkip }

type stubMembershipStmt struct{ member bool }

func (s *stubMembershipStmt) Close() error  { return nil }
func (s *stubMembershipStmt) NumInput() int { return -1 }
func (s *stubMembershipStmt) Exec(args []driver.Value) (driver.Result, error) {
	return nil, driver.ErrSkip
}
func (s *stubMembershipStmt) Query(args []driver.Value) (driver.Rows, error) {
	return &stubMembershipRows{remaining: map[bool]int{true: 1, false: 0}[s.member]}, nil
}

type stubMembershipRows struct{ remaining int }

func (r *stubMembershipRows) Columns() []string { return []string{"state"} }
func (r *stubMembershipRows) Close() error      { return nil }
func (r *stubMembershipRows) Next(dest []driver.Value) error {
	if r.remaining == 0 {
		return io.EOF
	}
	r.remaining--
	dest[0] = int64(0)
	return nil
}

var registerStubMembershipDriver sync.Once

func openStubMembershipDB(t *testing.T, member bool) *sql.DB {
	t.Helper()
	registerStubMembershipDriver.Do(func() { sql.Register("stub-membership", stubMembershipDriver{}) })
	dsn := "none"
	if member {
		dsn = "member"
	}
	db, err := sql.Open("stub-membership", dsn)
	if err != nil {
		t.Fatalf("open stub db: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

// locationLabelNK serves one public match whose label carries every location
// field a player or the game server has.
type locationLabelNK struct {
	runtime.NakamaModule
	label *MatchLabel
}

func (n *locationLabelNK) match(t *testing.T) *api.Match {
	data, err := json.Marshal(n.label)
	if err != nil {
		t.Fatalf("marshal label: %v", err)
	}
	return &api.Match{
		MatchId:       n.label.ID.String(),
		Authoritative: true,
		Label:         wrapperspb.String(string(data)),
	}
}

func (n *locationLabelNK) MatchList(ctx context.Context, limit int, authoritative bool, label string, minSize, maxSize *int, query string) ([]*api.Match, error) {
	return []*api.Match{n.match(nil)}, nil
}

func newLocationTestLabel(t *testing.T) *MatchLabel {
	t.Helper()
	id, err := NewMatchID(uuid.Must(uuid.NewV4()), "testnode")
	if err != nil {
		t.Fatalf("match id: %v", err)
	}
	groupID := uuid.Must(uuid.NewV4())
	return &MatchLabel{
		ID:        id,
		Open:      true,
		LobbyType: PublicLobby,
		GroupID:   &groupID,
		Size:      2,
		Players: []PlayerInfo{
			{
				UserID:      uuid.Must(uuid.NewV4()).String(),
				SessionID:   uuid.Must(uuid.NewV4()).String(),
				Username:    "player-one",
				DisplayName: "PlayerOne",
				ClientIP:    testLocPlayerIP,
				GeoHash:     testLocPlayerGeoHash,
				PingMillis:  42,
			},
		},
		GameServer: &GameServerPresence{
			OperatorID:    uuid.Must(uuid.NewV4()),
			Endpoint:      evr.Endpoint{ExternalIP: net.ParseIP(testLocServerIP), InternalIP: net.ParseIP("10.0.0.9"), Port: 6792},
			EndpointID:    "10.0.0.9:" + testLocServerIP + ":6792",
			DefaultRegion: testLocServerRegion,
			Region:        testLocServerRegion,
			CountryCode:   testLocServerCountry,
			City:          testLocServerCity,
			GeoHash:       testLocServerGeoHash,
			Latitude:      12.5,
			Longitude:     34.5,
			ASNumber:      64500,
		},
	}
}

// assertNoPlayerLocation fails if any entry of a "players" array carries a
// player location key with a value, or if a player location value appears
// anywhere in the JSON document.
func assertNoPlayerLocation(t *testing.T, name, doc string) {
	t.Helper()
	var walk func(v any)
	walk = func(v any) {
		switch x := v.(type) {
		case map[string]any:
			for k, child := range x {
				if k == "players" {
					players, _ := child.([]any)
					for _, entry := range players {
						fields, _ := entry.(map[string]any)
						for _, key := range playerLocationJSONKeys {
							if value, ok := fields[key]; ok && value != "" && value != nil {
								t.Errorf("%s: player key %q present with value %v", name, key, value)
							}
						}
					}
				}
				walk(child)
			}
		case []any:
			for _, child := range x {
				walk(child)
			}
		}
	}
	var parsed any
	if err := json.Unmarshal([]byte(doc), &parsed); err != nil {
		t.Fatalf("%s: response is not JSON: %v", name, err)
	}
	walk(parsed)
	for _, value := range []string{testLocPlayerIP, testLocPlayerGeoHash} {
		if strings.Contains(doc, value) {
			t.Errorf("%s: player location value %q present", name, value)
		}
	}
}

func matchRPCContext(userID string) context.Context {
	ctx := context.WithValue(context.Background(), runtime.RUNTIME_CTX_USER_ID, userID) //nolint:staticcheck // nakama runtime ctx keys are plain strings
	return context.WithValue(ctx, runtime.RUNTIME_CTX_VARS, map[string]string{})        //nolint:staticcheck // nakama runtime ctx keys are plain strings
}

func TestMatchRPC_UnprivilegedCallerGetsNoPlayerLocation(t *testing.T) {
	nk := &locationLabelNK{label: newLocationTestLabel(t)}
	db := openStubMembershipDB(t, false)

	out, err := MatchRPC(matchRPCContext(uuid.Must(uuid.NewV4()).String()), nil, db, nk, "")
	if err != nil {
		t.Fatalf("MatchRPC: %v", err)
	}
	assertNoPlayerLocation(t, "MatchRPC unprivileged", out)

	// The region label for the whole match is allowed, and must survive.
	if !strings.Contains(out, testLocServerRegion) {
		t.Errorf("match-level region %q was dropped: %s", testLocServerRegion, out)
	}
}

// The detector itself: a privileged caller gets the raw label, which carries
// location. If this stops failing assertNoLocation, the unprivileged test above
// has stopped proving anything.
func TestMatchRPC_PrivilegedCallerKeepsPlayerLocation(t *testing.T) {
	nk := &locationLabelNK{label: newLocationTestLabel(t)}
	db := openStubMembershipDB(t, true)

	out, err := MatchRPC(matchRPCContext(uuid.Must(uuid.NewV4()).String()), nil, db, nk, "")
	if err != nil {
		t.Fatalf("MatchRPC: %v", err)
	}
	for _, want := range []string{testLocPlayerIP, testLocPlayerGeoHash} {
		if !strings.Contains(out, want) {
			t.Errorf("privileged response lost %q: %s", want, out)
		}
	}
}

// filterMatchForUser backs the ListMatches hook. Auditors, enforcers and the
// server host see the full label, so the full label is where location leaks.
func TestFilterMatchForUser_NoPlayerLocationWithoutPrivateDataAccess(t *testing.T) {
	label := newLocationTestLabel(t)
	nk := &locationLabelNK{label: label}
	userID := label.GameServer.OperatorID.String() // the server host: sees the full label
	groupID := label.GroupID.String()

	got := filterMatchForUser(nk.match(t), userID, false, false, nil, map[string]bool{}, map[string]bool{groupID: true})
	if got == nil {
		t.Fatal("server host got no match")
	}
	assertNoPlayerLocation(t, "filterMatchForUser host", got.GetLabel().GetValue())

	// Only players are narrowed: the server's own location is still in the label.
	for _, want := range []string{testLocServerGeoHash, testLocServerCity, testLocServerRegion} {
		if !strings.Contains(got.GetLabel().GetValue(), want) {
			t.Errorf("server field %q was removed from the host's label", want)
		}
	}
}

func TestFilterMatchForUser_PrivilegedKeepsPlayerLocation(t *testing.T) {
	for name, args := range map[string][2]bool{"global operator": {true, false}, "private data access": {false, true}} {
		label := newLocationTestLabel(t)
		nk := &locationLabelNK{label: label}
		userID := label.GameServer.OperatorID.String()
		got := filterMatchForUser(nk.match(t), userID, args[0], args[1], nil, map[string]bool{}, map[string]bool{label.GroupID.String(): true})
		if got == nil {
			t.Fatalf("%s: got no match", name)
		}
		for _, want := range []string{testLocPlayerIP, testLocPlayerGeoHash} {
			if !strings.Contains(got.GetLabel().GetValue(), want) {
				t.Errorf("%s: lost %q", name, want)
			}
		}
	}
}

// The member and anonymous views are built from PublicMatchLabel and PublicView.
func TestPublicViews_NoPlayerLocation(t *testing.T) {
	label := newLocationTestLabel(t)

	member, err := json.Marshal(createPublicMatchLabel(label))
	if err != nil {
		t.Fatal(err)
	}
	assertNoPlayerLocation(t, "createPublicMatchLabel", string(member))
	if !strings.Contains(string(member), testLocServerCity) {
		t.Errorf("server_city was dropped from the public match label: %s", member)
	}

	view, err := json.Marshal(label.PublicView())
	if err != nil {
		t.Fatal(err)
	}
	assertNoPlayerLocation(t, "PublicView", string(view))

	list := &api.MatchList{Matches: []*api.Match{(&locationLabelNK{label: label}).match(t)}}
	if err := filterMatchesForAnonymous(list); err != nil {
		t.Fatal(err)
	}
	if len(list.Matches) != 1 {
		t.Fatalf("anonymous list has %d matches, want 1", len(list.Matches))
	}
	assertNoPlayerLocation(t, "filterMatchesForAnonymous", list.Matches[0].GetLabel().GetValue())
}

// playerLocationClassification names every exported field of PlayerInfo and says
// whether the field tells where the player is. Adding a field to PlayerInfo fails
// TestRedactPlayerLocation_ClassifiesEveryField until it is listed here, which is
// the moment to decide whether RedactPlayerLocation has to clear it.
var playerLocationClassification = map[string]bool{
	"DisplayName": false, "PartyID": false, "IsReservation": false, "Team": false,
	"JoinTime": false, "RatingMu": false, "RatingSigma": false, "Username": false,
	"DiscordID": false, "UserID": false, "EvrID": false, "SessionID": false,
	"PingMillis": false, "MatchmakingAt": false, "ReservationKind": false,
	"ReservationExpiry": false, "ReservationID": false,
	"ClientIP": true, "GeoHash": true,
}

func TestRedactPlayerLocation_ClassifiesEveryField(t *testing.T) {
	typ := reflect.TypeOf(PlayerInfo{})
	for i := 0; i < typ.NumField(); i++ {
		if _, ok := playerLocationClassification[typ.Field(i).Name]; !ok {
			t.Errorf("PlayerInfo.%s is not classified in playerLocationClassification: decide whether it says where the player is (it does if it is derived from the player's IP), and teach MatchLabel.RedactPlayerLocation if so", typ.Field(i).Name)
		}
	}
	for field := range playerLocationClassification {
		if _, ok := typ.FieldByName(field); !ok {
			t.Errorf("playerLocationClassification names PlayerInfo.%s, which no longer exists", field)
		}
	}
}

func TestRedactPlayerLocation_ClearsEveryLocationFieldAndNothingElse(t *testing.T) {
	player := PlayerInfo{}
	pv := reflect.ValueOf(&player).Elem()
	for name, isLocation := range playerLocationClassification {
		if f := pv.FieldByName(name); isLocation && f.Kind() == reflect.String {
			f.SetString("x-" + name)
		}
	}
	player.Username, player.PingMillis = "kept", 42

	server := newLocationTestLabel(t).GameServer
	label := &MatchLabel{Players: []PlayerInfo{player}, GameServer: server}
	redacted := label.RedactPlayerLocation()

	rp := reflect.ValueOf(redacted.Players[0])
	for name, isLocation := range playerLocationClassification {
		if isLocation && !rp.FieldByName(name).IsZero() {
			t.Errorf("PlayerInfo.%s survived RedactPlayerLocation", name)
		}
	}
	if redacted.Players[0].Username != "kept" || redacted.Players[0].PingMillis != 42 {
		t.Errorf("non-location player fields were cleared: %+v", redacted.Players[0])
	}
	if redacted.GameServer != server {
		t.Error("the game server was copied or changed: only players are redacted")
	}

	// The input is not touched: a privileged reader of the same label still sees it.
	if label.Players[0].ClientIP == "" || label.Players[0].GeoHash == "" {
		t.Error("RedactPlayerLocation mutated the label it was given")
	}
}

func TestRedactPlayerLocation_NilSafe(t *testing.T) {
	if (*MatchLabel)(nil).RedactPlayerLocation() != nil {
		t.Error("nil label must redact to nil")
	}
	if got := (&MatchLabel{}).RedactPlayerLocation(); got == nil || got.GameServer != nil || got.Players != nil {
		t.Errorf("empty label changed shape: %+v", got)
	}
}
