package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/gofrs/uuid/v5"
	"github.com/heroiclabs/nakama-common/api"
	"github.com/heroiclabs/nakama-common/runtime"
	"github.com/heroiclabs/nakama/v3/server/evr"
	"go.uber.org/zap"
	"google.golang.org/protobuf/types/known/wrapperspb"
)

const (
	modTestAuditorRole  = "role-auditor"
	modTestEnforcerRole = "role-enforcer"
	modTestModeratorTag = "ModeratorMarker"
)

// modFixture is a public arena match with every kind of row that matters to the
// moderator filter: a connected moderator, a moderator's held seat, two players,
// a spectator, a player's held seat, and a player who is a guild enforcer but
// plays in a normal role.
type modFixture struct {
	label *MatchLabel
	gid   string

	host, blue, orange, spectator, enforcerPlayer string
	moderator, moderatorSeat, heldSeat            PlayerInfo
}

func newModFixture(t *testing.T) *modFixture {
	t.Helper()
	label := newLocationTestLabel(t)
	label.Mode = evr.ModeArenaPublic
	label.MaxSize = 16
	label.PlayerLimit = 8
	label.TeamSize = 4

	row := func(name string, team int, reservation bool) PlayerInfo {
		return PlayerInfo{
			UserID:        uuid.Must(uuid.NewV4()).String(),
			SessionID:     uuid.Must(uuid.NewV4()).String(),
			Username:      name,
			DisplayName:   name,
			Team:          TeamIndex(team),
			IsReservation: reservation,
			ClientIP:      testLocPlayerIP,
			GeoHash:       testLocPlayerGeoHash,
		}
	}
	blue := row("BluePlayer", evr.TeamBlue, false)
	orange := row("OrangePlayer", evr.TeamOrange, false)
	spectator := row("SpectatorPlayer", evr.TeamSpectator, false)
	enforcerPlayer := row("EnforcerPlayer", evr.TeamBlue, false)
	moderator := row(modTestModeratorTag+"Connected", evr.TeamModerator, false)
	moderatorSeat := row(modTestModeratorTag+"Seat", evr.TeamModerator, true)
	heldSeat := row("HeldSeat", evr.TeamBlue, true)

	label.Players = []PlayerInfo{blue, orange, spectator, enforcerPlayer, moderator, moderatorSeat, heldSeat}
	label.Size = 5             // blue, orange, spectator, enforcer player, moderator
	label.PlayerCount = 3      // blue, orange, enforcer player
	label.ReservationCount = 2 // moderator seat, held seat
	label.TeamAlignments = map[string]int{moderator.UserID: evr.TeamModerator, blue.UserID: evr.TeamBlue}

	return &modFixture{
		label: label, gid: label.GroupID.String(),
		host: label.GameServer.OperatorID.String(), blue: blue.UserID, orange: orange.UserID,
		spectator: spectator.UserID, enforcerPlayer: enforcerPlayer.UserID,
		moderator: moderator, moderatorSeat: moderatorSeat, heldSeat: heldSeat,
	}
}

// moderatorIdentityStrings are the moderator values every label kind carries when it
// lists the moderator; the public view leaves session ids out.
func (f *modFixture) moderatorIdentityStrings() []string {
	return []string{f.moderator.UserID, f.moderator.DisplayName, f.moderatorSeat.UserID, f.moderatorSeat.DisplayName}
}

func (f *modFixture) moderatorStrings() []string {
	return []string{
		f.moderator.UserID, f.moderator.SessionID, f.moderator.DisplayName,
		f.moderatorSeat.UserID, f.moderatorSeat.SessionID, f.moderatorSeat.DisplayName,
		modTestModeratorTag,
	}
}

// modCaller is one kind of caller and what the fixture should look like to them.
type modCaller struct {
	name       string
	userID     string
	operator   bool
	auditor    bool
	owner      bool
	enforcer   bool
	member     bool
	seesMods   bool
	wantPlayer []string // user IDs that must be listed in "players" (nil: no players list)
}

// listedLabel is the part of a list label the assertions read.
type listedLabel struct {
	Size             int            `json:"size"`
	PlayerCount      int            `json:"player_count"`
	ReservationCount int            `json:"reservation_count"`
	Players          []listedPlayer `json:"players"`
	TeamAlignments   map[string]int `json:"team_alignments"`
}

type listedPlayer struct {
	UserID string    `json:"user_id"`
	Team   TeamIndex `json:"team"`
}

func modCallers(f *modFixture) []modCaller {
	stranger := uuid.Must(uuid.NewV4()).String()
	allRows := []string{f.blue, f.orange, f.spectator, f.enforcerPlayer}
	return []modCaller{
		{name: "global operator", userID: stranger, operator: true, seesMods: true,
			wantPlayer: append([]string{f.moderator.UserID, f.moderatorSeat.UserID, f.heldSeat.UserID}, allRows...)},
		{name: "auditor", userID: "auditor-user", auditor: true, member: true, seesMods: true,
			wantPlayer: append([]string{f.moderator.UserID, f.moderatorSeat.UserID, f.heldSeat.UserID}, allRows...)},
		{name: "guild admin", userID: "owner-user", owner: true, member: true, seesMods: true},
		{name: "server host", userID: f.host,
			wantPlayer: append([]string{f.heldSeat.UserID}, allRows...)},
		{name: "enforcer playing in a normal role", userID: f.enforcerPlayer, enforcer: true, member: true,
			wantPlayer: append([]string{f.heldSeat.UserID}, allRows...)},
		{name: "participant", userID: f.blue,
			wantPlayer: []string{f.blue, f.orange, f.spectator, f.enforcerPlayer}},
		{name: "guild member", userID: stranger, member: true},
		{name: "outsider", userID: stranger},
	}
}

// assertModeratorView checks one caller's label against the fixture.
func assertModeratorView(t *testing.T, name, doc string, matchSize int32, f *modFixture, c modCaller) {
	t.Helper()
	var got listedLabel
	if err := json.Unmarshal([]byte(doc), &got); err != nil {
		t.Fatalf("%s: label is not JSON: %v", name, err)
	}

	wantSize, wantSeats := 4, 1
	if c.seesMods {
		wantSize, wantSeats = 5, 2
	}
	if got.Size != wantSize {
		t.Errorf("%s: label size = %d, want %d", name, got.Size, wantSize)
	}
	if int(matchSize) != wantSize {
		t.Errorf("%s: match size = %d, want %d", name, matchSize, wantSize)
	}
	if got.PlayerCount != 3 {
		t.Errorf("%s: player_count = %d, want 3", name, got.PlayerCount)
	}
	fullLabel := strings.Contains(doc, "reservation_count")
	if fullLabel && got.ReservationCount != wantSeats {
		t.Errorf("%s: reservation_count = %d, want %d", name, got.ReservationCount, wantSeats)
	}

	if !c.seesMods {
		for _, s := range f.moderatorStrings() {
			if strings.Contains(doc, s) {
				t.Errorf("%s: label contains moderator value %q: %s", name, s, doc)
			}
		}
		if _, ok := got.TeamAlignments[f.moderator.UserID]; ok {
			t.Errorf("%s: team_alignments lists the moderator", name)
		}
		for _, p := range got.Players {
			if int(p.Team) == evr.TeamModerator {
				t.Errorf("%s: players lists a moderator-role entry", name)
			}
		}
	}

	if c.wantPlayer == nil {
		if len(got.Players) != 0 {
			t.Errorf("%s: players listed, want none: %s", name, doc)
		}
		return
	}
	listed := make(map[string]bool, len(got.Players))
	for _, p := range got.Players {
		listed[p.UserID] = true
	}
	if len(got.Players) != len(c.wantPlayer) {
		t.Errorf("%s: %d players listed, want %d: %s", name, len(got.Players), len(c.wantPlayer), doc)
	}
	for _, id := range c.wantPlayer {
		if !listed[id] {
			t.Errorf("%s: player %s missing from players: %s", name, id, doc)
		}
	}
}

// assertModeratorValues checks that doc carries the moderator's values when want
// is true and none of them when it is false.
func assertModeratorValues(t *testing.T, name, doc string, f *modFixture, want bool) {
	t.Helper()
	values := f.moderatorStrings()
	if want {
		values = f.moderatorIdentityStrings()
	}
	for _, s := range values {
		if strings.Contains(doc, s) != want {
			t.Errorf("%s: moderator value %q listed = %v, want %v: %s", name, s, !want, want, doc)
		}
	}
}

func (f *modFixture) apiMatch(t *testing.T) *api.Match {
	m := (&locationLabelNK{label: f.label}).match(t)
	m.Size = int32(f.label.Size)
	return m
}

// directViewer builds the inputs the hook builds for filterMatchForUser.
func (f *modFixture) directInputs(c modCaller) (map[string]*GuildGroup, map[string]bool, *moderatorViewer) {
	guildGroups := map[string]*GuildGroup{}
	members := map[string]bool{}
	if c.member {
		members[f.gid] = true
		gg := &GuildGroup{GroupMetadata: GroupMetadata{
			RoleMap: GuildGroupRoles{Auditor: modTestAuditorRole, Enforcer: modTestEnforcerRole},
		}}
		if c.owner {
			gg.OwnerID = c.userID
		}
		cache := map[string]map[string]bool{modTestAuditorRole: {}, modTestEnforcerRole: {}}
		if c.auditor {
			cache[modTestAuditorRole][c.userID] = true
		}
		if c.enforcer {
			cache[modTestEnforcerRole][c.userID] = true
		}
		gg.State = &GuildGroupState{RoleCache: cache}
		guildGroups[f.gid] = gg
	}
	viewer := newModeratorViewer(c.userID, c.operator,
		func(id string) *GuildGroup { return guildGroups[id] },
		func(gg *GuildGroup) bool { return gg.IsOwner(c.userID) })
	return guildGroups, members, viewer
}

func TestFilterMatchForUser_ModeratorListedOnlyForOperatorAuditorAndGuildAdmin(t *testing.T) {
	for _, c := range modCallers(newModFixture(t)) {
		t.Run(c.name, func(t *testing.T) {
			f := newModFixture(t)
			c := modCallers(f)[indexOfCaller(modCallers(f), c.name)]
			guildGroups, members, viewer := f.directInputs(c)
			got := filterMatchForUser(f.apiMatch(t), c.userID, c.operator, false, guildGroups, map[string]bool{}, members, viewer)
			if got == nil {
				t.Fatal("caller got no match")
			}
			assertModeratorView(t, c.name, got.GetLabel().GetValue(), got.Size, f, c)
		})
	}
}

func indexOfCaller(callers []modCaller, name string) int {
	for i, c := range callers {
		if c.name == name {
			return i
		}
	}
	return -1
}

// The label the match handler published is never changed by a caller's view.
func TestFilterMatchForUser_PublishedLabelKeepsItsModerator(t *testing.T) {
	f := newModFixture(t)
	c := modCallers(f)[indexOfCaller(modCallers(f), "outsider")]
	_, members, viewer := f.directInputs(c)
	_ = filterMatchForUser(f.apiMatch(t), c.userID, false, false, nil, map[string]bool{}, members, viewer)
	if f.label.Size != 5 || len(f.label.Players) != 7 || f.label.ReservationCount != 2 {
		t.Errorf("source label changed: size=%d players=%d reservations=%d", f.label.Size, len(f.label.Players), f.label.ReservationCount)
	}
}

// Suspension is checked before the moderator rule: a suspended admin sees nothing.
func TestFilterMatchForUser_SuspendedGuildAdminSeesNoMatch(t *testing.T) {
	f := newModFixture(t)
	c := modCallers(f)[indexOfCaller(modCallers(f), "guild admin")]
	guildGroups, members, viewer := f.directInputs(c)
	got := filterMatchForUser(f.apiMatch(t), c.userID, false, false, guildGroups, map[string]bool{f.gid: true}, members, viewer)
	if got != nil {
		t.Errorf("suspended guild admin saw the match: %s", got.GetLabel().GetValue())
	}
}

// A label with no moderator in it is listed exactly as before for every caller.
func TestFilterMatchForUser_LabelWithoutModeratorUnchanged(t *testing.T) {
	f := newModFixture(t)
	f.label.Players = f.label.Players[:4]
	f.label.Size, f.label.ReservationCount = 4, 0
	f.label.TeamAlignments = map[string]int{f.blue: evr.TeamBlue}
	c := modCallers(f)[indexOfCaller(modCallers(f), "server host")]
	_, members, viewer := f.directInputs(c)
	got := filterMatchForUser(f.apiMatch(t), c.userID, false, false, nil, map[string]bool{}, members, viewer)
	var l listedLabel
	if err := json.Unmarshal([]byte(got.GetLabel().GetValue()), &l); err != nil {
		t.Fatal(err)
	}
	if l.Size != 4 || got.Size != 4 || len(l.Players) != 4 || l.TeamAlignments[f.blue] != evr.TeamBlue {
		t.Errorf("label without a moderator changed: %s (match size %d)", got.GetLabel().GetValue(), got.Size)
	}
}

// moderatorNK serves the hook and the RPCs: the caller's groups, one guild with its
// owner and role cache, and the one match.
type moderatorNK struct {
	locationLabelNK
	f      *modFixture
	caller modCaller
}

func (n *moderatorNK) guildGroup() *api.Group {
	md, _ := json.Marshal(map[string]any{
		"guild_id": "900000000000000042",
		"owner_id": map[bool]string{true: n.caller.userID}[n.caller.owner],
		"roles":    map[string]string{"auditor": modTestAuditorRole, "moderator": modTestEnforcerRole},
	})
	return &api.Group{Id: n.f.gid, Name: "mod test guild", LangTag: GuildGroupLangTag, Metadata: string(md)}
}

func (n *moderatorNK) UserGroupsList(ctx context.Context, userID string, limit int, state *int, cursor string) ([]*api.UserGroupList_UserGroup, string, error) {
	var groups []*api.UserGroupList_UserGroup
	if n.caller.operator {
		groups = append(groups, &api.UserGroupList_UserGroup{
			Group: &api.Group{Id: uuid.Must(uuid.NewV4()).String(), Name: GroupGlobalOperators},
			State: wrapperspb.Int32(0),
		})
	}
	if n.caller.member {
		groups = append(groups, &api.UserGroupList_UserGroup{Group: n.guildGroup(), State: wrapperspb.Int32(int32(api.UserGroupList_UserGroup_MEMBER))})
	}
	return groups, "", nil
}

func (n *moderatorNK) GroupsGetId(ctx context.Context, groupIDs []string) ([]*api.Group, error) {
	return []*api.Group{n.guildGroup()}, nil
}

func (n *moderatorNK) StorageRead(ctx context.Context, reads []*runtime.StorageRead) ([]*api.StorageObject, error) {
	cache := map[string]map[string]bool{modTestAuditorRole: {}, modTestEnforcerRole: {}}
	if n.caller.auditor {
		cache[modTestAuditorRole][n.caller.userID] = true
	}
	if n.caller.enforcer {
		cache[modTestEnforcerRole][n.caller.userID] = true
	}
	objs := make([]*api.StorageObject, 0, len(reads))
	for _, r := range reads {
		val, _ := json.Marshal(map[string]any{"group_id": r.Key, "role_cache": cache})
		objs = append(objs, &api.StorageObject{Collection: StorageCollectionState, Key: r.Key, Value: string(val)})
	}
	return objs, nil
}

func (n *moderatorNK) MatchGet(ctx context.Context, id string) (*api.Match, error) {
	return n.f.apiMatch(nil), nil
}

func (n *moderatorNK) MatchList(ctx context.Context, limit int, authoritative bool, label string, minSize, maxSize *int, query string) ([]*api.Match, error) {
	return []*api.Match{n.f.apiMatch(nil)}, nil
}

func (n *moderatorNK) GroupsList(ctx context.Context, name, langTag string, members *int, open *bool, limit int, cursor string) ([]*api.Group, string, error) {
	return nil, "", nil
}

func (n *moderatorNK) StreamUserList(mode uint8, subject, subcontext, label string, includeHidden, includeNotHidden bool) ([]runtime.Presence, error) {
	return nil, nil
}

func TestAfterListMatchesHook_ModeratorListedOnlyForOperatorAuditorAndGuildAdmin(t *testing.T) {
	for _, name := range func() []string {
		var names []string
		for _, c := range modCallers(newModFixture(t)) {
			names = append(names, c.name)
		}
		return names
	}() {
		t.Run(name, func(t *testing.T) {
			f := newModFixture(t)
			c := modCallers(f)[indexOfCaller(modCallers(f), name)]
			nk := &moderatorNK{locationLabelNK: locationLabelNK{label: f.label}, f: f, caller: c}
			out := &api.MatchList{Matches: []*api.Match{f.apiMatch(t)}}
			if err := AfterListMatchesHook(matchRPCContext(c.userID), NewRuntimeGoLogger(zap.NewNop()), nil, nk, out, &api.ListMatchesRequest{}); err != nil {
				t.Fatalf("AfterListMatchesHook: %v", err)
			}
			if len(out.Matches) != 1 {
				t.Fatalf("caller sees %d matches, want 1", len(out.Matches))
			}
			assertModeratorView(t, c.name, out.Matches[0].GetLabel().GetValue(), out.Matches[0].Size, f, c)
		})
	}
}

func TestFilterMatchesForAnonymous_LabelHasNoModerator(t *testing.T) {
	f := newModFixture(t)
	list := &api.MatchList{Matches: []*api.Match{f.apiMatch(t)}}
	if err := filterMatchesForAnonymous(list); err != nil {
		t.Fatal(err)
	}
	if len(list.Matches) != 1 {
		t.Fatalf("anonymous list has %d matches, want 1", len(list.Matches))
	}
	doc := list.Matches[0].GetLabel().GetValue()
	for _, s := range f.moderatorStrings() {
		if strings.Contains(doc, s) {
			t.Errorf("anonymous label contains moderator value %q: %s", s, doc)
		}
	}
	var got listedLabel
	if err := json.Unmarshal([]byte(doc), &got); err != nil {
		t.Fatal(err)
	}
	if got.Size != 4 || got.ReservationCount != 1 || list.Matches[0].Size != 4 {
		t.Errorf("anonymous label size=%d reservation_count=%d match size=%d, want 4, 1, 4", got.Size, got.ReservationCount, list.Matches[0].Size)
	}
	if len(got.Players) != 5 {
		t.Errorf("anonymous label lists %d players, want the 4 players and the held seat", len(got.Players))
	}
}

// rpcCaller is a caller of the match RPCs: what the request context and the
// privileged-group lookup say about them.
type rpcCaller struct {
	modCaller
	privateData bool
}

// useBotUserID gives GuildGroupLoad the bot user it reads guild state as.
func useBotUserID(t *testing.T) {
	t.Helper()
	prev := serviceSettings.Load()
	ServiceSettingsUpdate(&ServiceSettingsData{DiscordBotUserID: "bot-uid"})
	t.Cleanup(func() { serviceSettings.Store(prev) })
}

func rpcContext(c rpcCaller) context.Context {
	ctx := matchRPCContext(c.userID)
	return WithUserPermissions(ctx, &UserPermissions{IsGlobalOperator: c.operator})
}

func rpcDB(t *testing.T, c rpcCaller) *sql.DB {
	return openStubMembershipDB(t, c.privateData)
}

func rpcCallers(f *modFixture) []rpcCaller {
	by := func(name string) modCaller { return modCallers(f)[indexOfCaller(modCallers(f), name)] }
	return []rpcCaller{
		{modCaller: by("outsider")},
		{modCaller: by("guild member")},
		{modCaller: by("auditor")},
		{modCaller: by("guild admin")},
		{modCaller: by("global operator"), privateData: true},
		{modCaller: modCaller{name: "private data access without operator", userID: uuid.Must(uuid.NewV4()).String()}, privateData: true},
	}
}

func rpcLabels(t *testing.T, out string) []json.RawMessage {
	t.Helper()
	var resp struct {
		Labels []json.RawMessage `json:"labels"`
	}
	if err := json.Unmarshal([]byte(out), &resp); err != nil {
		t.Fatalf("response is not JSON: %v: %s", err, out)
	}
	return resp.Labels
}

func TestMatchRPC_ModeratorListedOnlyForOperatorAuditorAndGuildAdmin(t *testing.T) {
	for _, c := range rpcCallers(newModFixture(t)) {
		t.Run(c.name, func(t *testing.T) {
			useBotUserID(t)
			f := newModFixture(t)
			nk := &moderatorNK{locationLabelNK: locationLabelNK{label: f.label}, f: f, caller: c.modCaller}
			out, err := MatchRPC(rpcContext(c), NewRuntimeGoLogger(zap.NewNop()), rpcDB(t, c), nk, "")
			if err != nil {
				t.Fatalf("MatchRPC: %v", err)
			}
			labels := rpcLabels(t, out)
			if len(labels) != 1 {
				t.Fatalf("%d labels, want 1", len(labels))
			}
			doc := string(labels[0])
			assertModeratorValues(t, "label", doc, f, c.seesMods)
			var got listedLabel
			if err := json.Unmarshal(labels[0], &got); err != nil {
				t.Fatal(err)
			}
			wantSize := map[bool]int{true: 5, false: 4}[c.seesMods]
			if got.Size != wantSize {
				t.Errorf("label size = %d, want %d", got.Size, wantSize)
			}
		})
	}
}

type publicListResponse struct {
	PlayerCount int `json:"player_count"`
	Labels      []listedLabel
}

func listPublic(t *testing.T, h *RPCHandler, c rpcCaller, f *modFixture) (string, publicListResponse) {
	t.Helper()
	nk := &moderatorNK{locationLabelNK: locationLabelNK{label: f.label}, f: f, caller: c.modCaller}
	out, err := h.MatchListPublicRPC(rpcContext(c), NewRuntimeGoLogger(zap.NewNop()), rpcDB(t, c), nk, "")
	if err != nil {
		t.Fatalf("MatchListPublicRPC: %v", err)
	}
	var resp publicListResponse
	if err := json.Unmarshal([]byte(out), &resp); err != nil {
		t.Fatal(err)
	}
	return out, resp
}

func TestMatchListPublicRPC_ModeratorListedOnlyForOperatorAuditorAndGuildAdmin(t *testing.T) {
	for _, c := range rpcCallers(newModFixture(t)) {
		t.Run(c.name, func(t *testing.T) {
			useBotUserID(t)
			f := newModFixture(t)
			h := NewRPCHandler(context.Background(), nil, nil)

			out, resp := listPublic(t, h, c, f)

			assertModeratorValues(t, "public list", out, f, c.seesMods)
			// Every distinct row but the moderator's two: four players and the held seat.
			wantPlayers, wantSize, wantReservations := 5, 4, 1
			if c.seesMods {
				wantPlayers, wantSize, wantReservations = 7, 5, 2
			}
			if resp.PlayerCount != wantPlayers {
				t.Errorf("player_count = %d, want %d", resp.PlayerCount, wantPlayers)
			}
			if len(resp.Labels) != 1 || resp.Labels[0].Size != wantSize || resp.Labels[0].ReservationCount != wantReservations {
				t.Errorf("public list label = %+v, want size %d and reservation_count %d", resp.Labels, wantSize, wantReservations)
			}
		})
	}
}

// The cached response stays the one without moderators: a privileged caller is
// answered from a fresh listing, and neither a privileged caller before it nor one
// after it changes what the others get.
func TestMatchListPublicRPC_CacheStaysFilteredForOthers(t *testing.T) {
	useBotUserID(t)
	f := newModFixture(t)
	callers := rpcCallers(f)
	pick := func(name string) rpcCaller {
		for _, c := range callers {
			if c.name == name {
				return c
			}
		}
		t.Fatalf("no caller %q", name)
		return rpcCaller{}
	}
	h := NewRPCHandler(context.Background(), nil, nil)

	for _, step := range []struct {
		caller string
		sees   bool
	}{
		{"global operator", true}, // fills the cache
		{"outsider", false},       // served from the cache
		{"guild admin", true},     // cache is warm; still unfiltered
		{"auditor", true},
		{"guild member", false},
	} {
		out, _ := listPublic(t, h, pick(step.caller), f)
		assertModeratorValues(t, step.caller, out, f, step.sees)
	}
}

func TestMatchStatusRPC_LabelsListModeratorOnlyForOperatorAuditorAndGuildAdmin(t *testing.T) {
	for _, c := range rpcCallers(newModFixture(t)) {
		t.Run(c.name, func(t *testing.T) {
			useBotUserID(t)
			f := newModFixture(t)
			f.label.SpawnedBy = c.userID // full label for the caller, so both labels are read
			nk := &moderatorNK{locationLabelNK: locationLabelNK{label: f.label}, f: f, caller: c.modCaller}
			payload := fmt.Sprintf(`{"match_id":%q}`, f.label.ID.String())
			out, err := MatchStatusRPC(rpcContext(c), NewRuntimeGoLogger(zap.NewNop()), nil, nk, payload)
			if err != nil {
				t.Fatalf("MatchStatusRPC: %v", err)
			}
			var resp struct {
				MatchLabel  json.RawMessage `json:"match_label"`
				PublicLabel json.RawMessage `json:"public_label"`
			}
			if err := json.Unmarshal([]byte(out), &resp); err != nil {
				t.Fatal(err)
			}
			for name, doc := range map[string]string{"match_label": string(resp.MatchLabel), "public_label": string(resp.PublicLabel)} {
				if doc == "" {
					t.Fatalf("%s missing: %s", name, out)
				}
				assertModeratorValues(t, name, doc, f, c.seesMods)
			}
		})
	}
}
