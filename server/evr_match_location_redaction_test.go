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

// Synthetic values only (documentation ranges); each is searched for verbatim
// in the marshalled response, so a field renamed to dodge a key check still
// fails the test.
const (
	testLocPlayerIP      = "203.0.113.77"
	testLocPlayerGeoHash = "gbsuv7ztq"
	testLocServerGeoHash = "u4pruydqq"
	testLocServerCity    = "Testville"
	testLocServerRegion  = "us-test-1"
	testLocServerCountry = "ZZ"
)

// locationJSONKeys are the JSON keys that carry where a player or a server is.
// Region and country_code are deliberately absent: a region label for the whole
// match is allowed.
var locationJSONKeys = []string{
	"client_ip", "geohash", "latitude", "longitude", "city", "asn",
	"endpoint", "endpoint_id", "InternalIP", "ExternalIP",
}

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
			Endpoint:      evr.Endpoint{ExternalIP: net.ParseIP("198.51.100.9"), InternalIP: net.ParseIP("10.0.0.9"), Port: 6792},
			EndpointID:    "10.0.0.9:198.51.100.9:6792",
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

// isEmptyJSON reports whether a decoded JSON value carries nothing: null, "",
// 0, false, or an object or array holding only such values. An always-emitted
// but empty key (GameServerPresence.Endpoint has no omitempty) tells a caller
// nothing about where anyone is.
func isEmptyJSON(v any) bool {
	switch x := v.(type) {
	case nil:
		return true
	case string:
		return x == ""
	case float64:
		return x == 0
	case bool:
		return !x
	case map[string]any:
		for _, child := range x {
			if !isEmptyJSON(child) {
				return false
			}
		}
		return true
	case []any:
		for _, child := range x {
			if !isEmptyJSON(child) {
				return false
			}
		}
		return true
	}
	return false
}

// assertNoLocation fails if any location key carries a value, or any location
// value appears anywhere, in the JSON document.
func assertNoLocation(t *testing.T, name, doc string) {
	t.Helper()
	var walk func(v any)
	walk = func(v any) {
		switch x := v.(type) {
		case map[string]any:
			for k, child := range x {
				for _, forbidden := range locationJSONKeys {
					if strings.EqualFold(k, forbidden) && !isEmptyJSON(child) {
						t.Errorf("%s: location key %q present with value %v", name, k, child)
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
	for _, value := range []string{testLocPlayerIP, testLocPlayerGeoHash, testLocServerGeoHash, testLocServerCity, "198.51.100.9", "10.0.0.9"} {
		if strings.Contains(doc, value) {
			t.Errorf("%s: location value %q present", name, value)
		}
	}
}

func matchRPCContext(userID string) context.Context {
	ctx := context.WithValue(context.Background(), runtime.RUNTIME_CTX_USER_ID, userID) //nolint:staticcheck // nakama runtime ctx keys are plain strings
	return context.WithValue(ctx, runtime.RUNTIME_CTX_VARS, map[string]string{})        //nolint:staticcheck // nakama runtime ctx keys are plain strings
}

func TestMatchRPC_UnprivilegedCallerGetsNoLocation(t *testing.T) {
	nk := &locationLabelNK{label: newLocationTestLabel(t)}
	db := openStubMembershipDB(t, false)

	out, err := MatchRPC(matchRPCContext(uuid.Must(uuid.NewV4()).String()), nil, db, nk, "")
	if err != nil {
		t.Fatalf("MatchRPC: %v", err)
	}
	assertNoLocation(t, "MatchRPC unprivileged", out)

	// The region label for the whole match is allowed, and must survive.
	if !strings.Contains(out, testLocServerRegion) {
		t.Errorf("match-level region %q was dropped: %s", testLocServerRegion, out)
	}
}

// The detector itself: a privileged caller gets the raw label, which carries
// location. If this stops failing assertNoLocation, the unprivileged test above
// has stopped proving anything.
func TestMatchRPC_PrivilegedCallerKeepsLocation(t *testing.T) {
	nk := &locationLabelNK{label: newLocationTestLabel(t)}
	db := openStubMembershipDB(t, true)

	out, err := MatchRPC(matchRPCContext(uuid.Must(uuid.NewV4()).String()), nil, db, nk, "")
	if err != nil {
		t.Fatalf("MatchRPC: %v", err)
	}
	for _, want := range []string{testLocPlayerIP, testLocPlayerGeoHash, testLocServerGeoHash} {
		if !strings.Contains(out, want) {
			t.Errorf("privileged response lost %q: %s", want, out)
		}
	}
}

// filterMatchForUser backs the ListMatches hook. Auditors, enforcers and the
// server host see the full label, so the full label is where location leaks.
func TestFilterMatchForUser_NoLocationWithoutPrivateDataAccess(t *testing.T) {
	label := newLocationTestLabel(t)
	nk := &locationLabelNK{label: label}
	userID := label.GameServer.OperatorID.String() // the server host: sees the full label
	groupID := label.GroupID.String()

	got := filterMatchForUser(nk.match(t), userID, false, false, nil, map[string]bool{}, map[string]bool{groupID: true})
	if got == nil {
		t.Fatal("server host got no match")
	}
	assertNoLocation(t, "filterMatchForUser host", got.GetLabel().GetValue())
}

func TestFilterMatchForUser_PrivilegedKeepsLocation(t *testing.T) {
	for name, args := range map[string][2]bool{"global operator": {true, false}, "private data access": {false, true}} {
		label := newLocationTestLabel(t)
		nk := &locationLabelNK{label: label}
		userID := label.GameServer.OperatorID.String()
		got := filterMatchForUser(nk.match(t), userID, args[0], args[1], nil, map[string]bool{}, map[string]bool{label.GroupID.String(): true})
		if got == nil {
			t.Fatalf("%s: got no match", name)
		}
		for _, want := range []string{testLocPlayerIP, testLocPlayerGeoHash, testLocServerGeoHash} {
			if !strings.Contains(got.GetLabel().GetValue(), want) {
				t.Errorf("%s: lost %q", name, want)
			}
		}
	}
}

// The member and anonymous views are built from PublicMatchLabel and PublicView.
func TestPublicViews_NoLocation(t *testing.T) {
	label := newLocationTestLabel(t)

	member, err := json.Marshal(createPublicMatchLabel(label))
	if err != nil {
		t.Fatal(err)
	}
	assertNoLocation(t, "createPublicMatchLabel", string(member))

	view, err := json.Marshal(label.PublicView())
	if err != nil {
		t.Fatal(err)
	}
	assertNoLocation(t, "PublicView", string(view))

	list := &api.MatchList{Matches: []*api.Match{(&locationLabelNK{label: label}).match(t)}}
	if err := filterMatchesForAnonymous(list); err != nil {
		t.Fatal(err)
	}
	if len(list.Matches) != 1 {
		t.Fatalf("anonymous list has %d matches, want 1", len(list.Matches))
	}
	assertNoLocation(t, "filterMatchesForAnonymous", list.Matches[0].GetLabel().GetValue())
}

// locationFieldClassification names every exported field of the two structs a
// match label carries to callers, and says whether the field tells where a
// player or the game server is. Adding a field to either struct fails
// TestRedactLocation_ClassifiesEveryField until it is listed here, which is the
// moment to decide whether RedactLocation has to clear it.
var locationFieldClassification = map[string]map[string]bool{
	"PlayerInfo": {
		"DisplayName": false, "PartyID": false, "IsReservation": false, "Team": false,
		"JoinTime": false, "RatingMu": false, "RatingSigma": false, "Username": false,
		"DiscordID": false, "UserID": false, "EvrID": false, "SessionID": false,
		"PingMillis": false, "MatchmakingAt": false, "ReservationKind": false,
		"ReservationExpiry": false, "ReservationID": false,
		"ClientIP": true, "GeoHash": true,
	},
	"GameServerPresence": {
		"Node": false, "Username": false, "SessionID": false, "OperatorID": false,
		"GroupIDs": false, "VersionLock": false, "AppID": false, "DefaultRegion": false,
		"RegionCodes": false, "ServerID": false, "Features": false, "Tags": false,
		"DesignatedModes": false, "TimeStepUsecs": false, "NativeSupport": false,
		"NativeVersion": false, "Region": false, "CountryCode": false,
		"Endpoint": true, "EndpointID": true, "City": true, "GeoHash": true,
		"Latitude": true, "Longitude": true, "ASNumber": true,
	},
}

func TestRedactLocation_ClassifiesEveryField(t *testing.T) {
	for name, typ := range map[string]reflect.Type{
		"PlayerInfo":         reflect.TypeOf(PlayerInfo{}),
		"GameServerPresence": reflect.TypeOf(GameServerPresence{}),
	} {
		classified := locationFieldClassification[name]
		for i := 0; i < typ.NumField(); i++ {
			if _, ok := classified[typ.Field(i).Name]; !ok {
				t.Errorf("%s.%s is not classified in locationFieldClassification: decide whether it is location, and teach MatchLabel.RedactLocation if it is", name, typ.Field(i).Name)
			}
		}
		for field := range classified {
			if _, ok := typ.FieldByName(field); !ok {
				t.Errorf("locationFieldClassification names %s.%s, which no longer exists", name, field)
			}
		}
	}
}

// fillNonZero sets every field of the struct behind v to a non-zero value, for
// the fields chosen by want.
func fillNonZero(t *testing.T, v reflect.Value, typeName string, want bool) {
	t.Helper()
	for i := 0; i < v.NumField(); i++ {
		name := v.Type().Field(i).Name
		if locationFieldClassification[typeName][name] != want {
			continue
		}
		f := v.Field(i)
		switch f.Kind() {
		case reflect.String:
			f.SetString("x-" + name)
		case reflect.Float64:
			f.SetFloat(1.5)
		case reflect.Int:
			f.SetInt(7)
		case reflect.Struct:
			if f.Type() == reflect.TypeOf(evr.Endpoint{}) {
				f.Set(reflect.ValueOf(evr.Endpoint{ExternalIP: net.ParseIP("198.51.100.9"), Port: 6792}))
			}
		}
	}
}

func TestRedactLocation_ClearsEveryLocationFieldAndNothingElse(t *testing.T) {
	player := PlayerInfo{}
	fillNonZero(t, reflect.ValueOf(&player).Elem(), "PlayerInfo", true)
	server := GameServerPresence{}
	fillNonZero(t, reflect.ValueOf(&server).Elem(), "GameServerPresence", true)

	// Region labels and identity fields the redaction must leave alone.
	player.Username, player.PingMillis = "kept", 42
	server.Region, server.DefaultRegion, server.CountryCode = "us-test-1", "us-test-1", "ZZ"

	label := &MatchLabel{Players: []PlayerInfo{player}, GameServer: &server}
	redacted := label.RedactLocation()

	rp, rs := redacted.Players[0], redacted.GameServer
	for name, isLocation := range locationFieldClassification["PlayerInfo"] {
		if isLocation && !reflect.ValueOf(rp).FieldByName(name).IsZero() {
			t.Errorf("PlayerInfo.%s survived RedactLocation", name)
		}
	}
	for name, isLocation := range locationFieldClassification["GameServerPresence"] {
		if isLocation && !reflect.ValueOf(*rs).FieldByName(name).IsZero() {
			t.Errorf("GameServerPresence.%s survived RedactLocation", name)
		}
	}
	if rp.Username != "kept" || rp.PingMillis != 42 {
		t.Errorf("non-location player fields were cleared: %+v", rp)
	}
	if rs.Region != "us-test-1" || rs.DefaultRegion != "us-test-1" || rs.CountryCode != "ZZ" {
		t.Errorf("match-level region label was cleared: %+v", rs)
	}

	// The input is not touched: a privileged reader of the same label still sees it.
	if label.Players[0].ClientIP == "" || label.Players[0].GeoHash == "" || label.GameServer.GeoHash == "" {
		t.Error("RedactLocation mutated the label it was given")
	}
}

func TestRedactLocation_NilSafe(t *testing.T) {
	if (*MatchLabel)(nil).RedactLocation() != nil {
		t.Error("nil label must redact to nil")
	}
	if got := (&MatchLabel{}).RedactLocation(); got == nil || got.GameServer != nil || got.Players != nil {
		t.Errorf("empty label changed shape: %+v", got)
	}
}
