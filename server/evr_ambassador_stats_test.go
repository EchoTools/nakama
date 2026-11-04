package server

import (
	"context"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/gofrs/uuid/v5"
	"github.com/heroiclabs/nakama/v3/server/evr"
	uatomic "go.uber.org/atomic"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"
)

// ---- stats off: S1 ratings, S2 type stats, S3 combat profile stats ----

type postMatchRun struct {
	byUser map[string][]*StatisticsQueueEntry
	blue   string // blue's user id
	orange string
	logs   *observer.ObservedLogs
}

// runPostMatchEntries feeds a two-player public match through
// processPostMatchMessages and returns every queued entry by user. ambBlue /
// ambOrange mark that player as having queued in ambassador mode.
func runPostMatchEntries(t *testing.T, mode evr.Symbol, ambBlue, ambOrange bool) postMatchRun {
	t.Helper()
	const blueXPID, orangeXPID = "OVR-ORG-1000000000000011", "OVR-ORG-1000000000000012"
	arenaKeys := func(xpid, result string) string {
		return strings.ReplaceAll(strings.ReplaceAll(combatWinnerPayload, "OVR-ORG-0000000000000000", xpid), `"CombatWins":1`, result)
	}
	blueMsg, err := evr.UnmarshalRemoteLog([]byte(arenaKeys(blueXPID, `"ArenaWins":1`)))
	if err != nil {
		t.Fatal(err)
	}
	orangeMsg, err := evr.UnmarshalRemoteLog([]byte(arenaKeys(orangeXPID, `"ArenaLosses":1`)))
	if err != nil {
		t.Fatal(err)
	}
	blueX, _ := evr.ParseEvrId(blueXPID)
	orangeX, _ := evr.ParseEvrId(orangeXPID)
	matchID := MatchID{UUID: uuid.Must(uuid.FromString("00000000-0000-4000-8000-00000000c210")), Node: "ambnode"}
	groupID := uuid.Must(uuid.FromString("00000000-0000-4000-8000-00000000c211"))
	const blueUID, orangeUID = "00000000-0000-4000-8000-00000000c221", "00000000-0000-4000-8000-00000000c222"
	label := &MatchLabel{
		ID: matchID, Mode: mode, GroupID: &groupID,
		Players: []PlayerInfo{
			{UserID: blueUID, SessionID: "00000000-0000-4000-8000-00000000c231", EvrID: *blueX, Team: BlueTeam, DisplayName: "synthetic-blue", Ambassador: ambBlue},
			{UserID: orangeUID, SessionID: "00000000-0000-4000-8000-00000000c232", EvrID: *orangeX, Team: OrangeTeam, DisplayName: "synthetic-orange", Ambassador: ambOrange},
		},
	}
	nk := &combatWinnerNK{evrTestNakamaModule: newEvrTestNakamaModule(), label: label}
	settings := &ServiceSettingsData{}
	FixDefaultServiceSettings(nil, settings)
	settings.Matchmaking.EnableSBMM = true
	withServiceSettings(t, settings)

	core, logs := observer.New(zapcore.DebugLevel)
	logger := NewRuntimeGoLogger(zap.New(core))
	queue := &StatisticsQueue{logger: logger, ch: make(chan []*StatisticsQueueEntry, 64)}
	s := &EventRemoteLogSet{Node: "ambnode", UserID: "00000000-0000-4000-8000-00000000c2ff", SessionID: "00000000-0000-4000-8000-00000000c2fe", XPID: evr.EvrId{}}
	if err := s.processPostMatchMessages(context.Background(), logger, nil, nk, NewLocalSessionRegistry(&testMetrics{}), queue, matchID, []evr.RemoteLog{blueMsg, orangeMsg}); err != nil {
		t.Fatalf("processPostMatchMessages: %v", err)
	}
	run := postMatchRun{byUser: map[string][]*StatisticsQueueEntry{}, blue: blueUID, orange: orangeUID, logs: logs}
	for len(queue.ch) > 0 {
		for _, e := range <-queue.ch {
			run.byUser[e.UserID] = append(run.byUser[e.UserID], e)
		}
	}
	return run
}

// entryKey identifies an entry by what it writes, so two runs can be compared.
func entryKeys(es []*StatisticsQueueEntry) []string {
	out := make([]string, 0, len(es))
	for _, e := range es {
		out = append(out, strings.Join([]string{e.BoardMeta.StatName, e.BoardMeta.ResetSchedule.String(), e.BoardMeta.Mode.String(), string(rune(e.Score)), string(rune(e.Subscore))}, "|"))
	}
	return out
}

func hasStat(es []*StatisticsQueueEntry, name string) bool {
	for _, e := range es {
		if e.BoardMeta.StatName == name {
			return true
		}
	}
	return false
}

func TestPostMatchStats_AmbassadorWritesNothingForThemselves(t *testing.T) {
	for _, mode := range []evr.Symbol{evr.ModeArenaPublic, evr.ModeCombatPublic} {
		t.Run(mode.String(), func(t *testing.T) {
			control := runPostMatchEntries(t, mode, false, false)
			// The control must have written both kinds, or the test below proves nothing.
			if !hasStat(control.byUser[control.blue], TeamSkillRatingMuStatisticID) {
				t.Fatal("control wrote no rating entries for blue: the harness is vacuous")
			}
			nonRating := 0
			for _, e := range control.byUser[control.blue] {
				if !strings.Contains(e.BoardMeta.StatName, "SkillRating") {
					nonRating++
				}
			}
			if nonRating == 0 {
				t.Fatal("control wrote no type-stat entries for blue: the harness is vacuous")
			}

			run := runPostMatchEntries(t, mode, true, false)
			if got := run.byUser[run.blue]; len(got) != 0 {
				t.Errorf("an ambassador's own rating and stats were written: %d entries, e.g. %s", len(got), got[0].BoardMeta.StatName)
			}
			// Everyone else in the match counts as normal: the other player's entries are identical to the control's.
			if !reflect.DeepEqual(entryKeys(run.byUser[run.orange]), entryKeys(control.byUser[control.orange])) {
				t.Errorf("the other player's entries changed.\n got: %v\nwant: %v", entryKeys(run.byUser[run.orange]), entryKeys(control.byUser[control.orange]))
			}
			// Each suppressed site is logged, with the player and the match.
			ev := run.logs.FilterMessage("ambassador_event").All()
			sites := map[string]bool{}
			for _, e := range ev {
				m := e.ContextMap()
				if m["event"] == "stats_suppressed" {
					if m["target_user_id"] != run.blue || m["match_id"] == nil {
						t.Errorf("stats_suppressed lacks the player or the match: %v", m)
					}
					sites[m["site"].(string)] = true
				}
			}
			if !sites["S1"] || !sites["S2"] {
				t.Errorf("want stats_suppressed logged for S1 and S2, got %v", sites)
			}
		})
	}
}

func TestPostMatchStats_NoAmbassadorFlagWritesEverything(t *testing.T) {
	// Their normal games count like anyone's: the flag is the only thing that turns it off.
	run := runPostMatchEntries(t, evr.ModeArenaPublic, false, false)
	if len(run.byUser[run.blue]) == 0 || len(run.byUser[run.orange]) == 0 {
		t.Fatalf("a normal match wrote entries for blue=%d orange=%d", len(run.byUser[run.blue]), len(run.byUser[run.orange]))
	}
	if n := len(run.logs.FilterMessage("ambassador_event").All()); n != 0 {
		t.Errorf("a normal match logged %d ambassador events", n)
	}
}

func TestCombatProfileUpdate_AmbassadorSuppressed(t *testing.T) {
	f := newSEC3Fixture(t)
	f.label.Players[0].Ambassador = true
	core, logs := observer.New(zapcore.DebugLevel)
	err := f.pipeline.processUserServerProfileUpdate(context.Background(), zap.New(core), f.nk, f.serverOpsID, f.evrID, f.label, f.payload)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(f.nk.events) != 0 {
		t.Fatalf("an ambassador's combat profile stats were applied: %d event(s)", len(f.nk.events))
	}
	found := false
	for _, e := range logs.FilterMessage("ambassador_event").All() {
		if m := e.ContextMap(); m["event"] == "stats_suppressed" && m["site"] == "S3" {
			found = true
		}
	}
	if !found {
		t.Error("S3 suppression was not logged")
	}
}

// ---- the stamp ----

func stampParams(mode evr.Symbol, groupID uuid.UUID, expires time.Time) *SessionParameters {
	p := &SessionParameters{}
	p.ambassadorQueue.Store(&ambassadorQueueStamp{Mode: mode, GroupID: groupID, ExpiresAt: expires})
	return p
}

func TestAmbassadorStampFor(t *testing.T) {
	now := time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)
	g := uuid.Must(uuid.FromString("00000000-0000-4000-8000-0000000000e1"))
	other := uuid.Must(uuid.FromString("00000000-0000-4000-8000-0000000000e2"))
	valid := stampParams(evr.ModeArenaPublic, g, now.Add(time.Minute))

	if got := ambassadorStampFor(valid, g.String(), evr.TeamBlue, now); got != evr.ModeArenaPublic {
		t.Errorf("valid stamp = %v, want arena public", got)
	}
	if got := ambassadorStampFor(valid, g.String(), evr.TeamUnassigned, now); got != evr.ModeArenaPublic {
		t.Errorf("valid stamp with unassigned role = %v, want arena public", got)
	}
	for _, tc := range []struct {
		name string
		p    *SessionParameters
		gid  string
		role int
	}{
		{"no stamp", &SessionParameters{}, g.String(), evr.TeamBlue},
		{"nil params", nil, g.String(), evr.TeamBlue},
		{"other group", valid, other.String(), evr.TeamBlue},
		{"expired", stampParams(evr.ModeArenaPublic, g, now.Add(-time.Second)), g.String(), evr.TeamBlue},
		{"expires exactly now", stampParams(evr.ModeArenaPublic, g, now), g.String(), evr.TeamBlue},
		{"spectator", valid, g.String(), evr.TeamSpectator},
		{"moderator", valid, g.String(), evr.TeamModerator},
		{"non-public mode", stampParams(evr.ModeSocialPublic, g, now.Add(time.Minute)), g.String(), evr.TeamBlue},
	} {
		if got := ambassadorStampFor(tc.p, tc.gid, tc.role, now); got != 0 {
			t.Errorf("%s: stamp = %v, want none", tc.name, got)
		}
	}
}

func TestPresenceAmbassadorInMatch(t *testing.T) {
	for _, tc := range []struct {
		name  string
		stamp evr.Symbol
		mode  evr.Symbol
		want  bool
	}{
		{"arena stamp, arena match", evr.ModeArenaPublic, evr.ModeArenaPublic, true},
		{"combat stamp, combat match", evr.ModeCombatPublic, evr.ModeCombatPublic, true},
		{"combat stamp leaks into an arena match", evr.ModeCombatPublic, evr.ModeArenaPublic, false},
		{"arena stamp in a social lobby", evr.ModeArenaPublic, evr.ModeSocialPublic, false},
		{"no stamp", 0, evr.ModeArenaPublic, false},
		{"stamp on a private match", evr.ModeArenaPublic, evr.ModeArenaPrivate, false},
	} {
		p := &EvrMatchPresence{AmbassadorQueueMode: tc.stamp}
		if got := p.ambassadorInMatch(tc.mode); got != tc.want {
			t.Errorf("%s: = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestStamp_SurvivesTheLabelRebuildButNotThePublicLabel(t *testing.T) {
	state := newSocialTestMatchLabel()
	state.Mode = evr.ModeArenaPublic
	state.MaxSize = 8
	state.PlayerLimit = 8

	mk := func(uid string, team int, stamp evr.Symbol, n uint64) *EvrMatchPresence {
		sid := uuid.Must(uuid.NewV4())
		return &EvrMatchPresence{
			UserID: uuid.Must(uuid.FromString(uid)), SessionID: sid, RoleAlignment: team,
			Username: "u" + uid[len(uid)-2:], DisplayName: "n" + uid[len(uid)-2:],
			EvrID: evr.EvrId{PlatformCode: 4, AccountId: n}, AmbassadorQueueMode: stamp,
		}
	}
	amb := mk("00000000-0000-4000-8000-0000000000b1", evr.TeamBlue, evr.ModeArenaPublic, 1)
	norm := mk("00000000-0000-4000-8000-0000000000c1", evr.TeamOrange, 0, 2)
	stale := mk("00000000-0000-4000-8000-0000000000d1", evr.TeamBlue, evr.ModeCombatPublic, 3) // combat stamp in an arena match
	for _, p := range []*EvrMatchPresence{amb, norm, stale} {
		state.presenceMap[p.SessionID.String()] = p
	}
	state.rebuildCache()

	flag := map[string]bool{}
	for _, p := range state.Players {
		flag[p.UserID] = p.Ambassador
	}
	if !flag[amb.UserID.String()] {
		t.Error("the ambassador's stamp did not reach the label's PlayerInfo")
	}
	if flag[norm.UserID.String()] {
		t.Error("a normal player was marked an ambassador")
	}
	if flag[stale.UserID.String()] {
		t.Error("a combat-mode stamp marked a player in an arena match")
	}

	// The stamp is in the label JSON the stats writer may read back from storage.
	if label := state.GetLabel(); !strings.Contains(label, `"ambassador":true`) {
		t.Errorf("the label JSON does not carry the stamp: %s", label)
	}

	// The public label never carries it.
	pub := state.PublicView()
	for _, p := range pub.Players {
		if p.Ambassador {
			t.Error("the public label exposes the ambassador mark")
		}
	}
}

// ---- the real presence builder ----

// stampSession is the few Session methods the presence builder calls. Any other
// method panics on the nil embedded interface, which is the point: the builder
// must not start reaching for more.
type stampSession struct {
	Session
	ctx context.Context
	uid uuid.UUID
	sid uuid.UUID
}

func (s *stampSession) Context() context.Context { return s.ctx }
func (s *stampSession) UserID() uuid.UUID        { return s.uid }
func (s *stampSession) ID() uuid.UUID            { return s.sid }
func (s *stampSession) Username() string         { return "synthetic" }
func (s *stampSession) ClientIP() string         { return "192.0.2.1" }
func (s *stampSession) ClientPort() string       { return "1" }

func newStampSession(stamp *ambassadorQueueStamp) *stampSession {
	params := &SessionParameters{profile: &EVRProfile{}, loginSession: &sessionWS{id: uuid.Must(uuid.NewV4())}}
	params.ambassadorQueue.Store(stamp)
	return &stampSession{
		ctx: context.WithValue(context.Background(), ctxSessionParametersKey{}, uatomic.NewPointer(params)),
		uid: uuid.Must(uuid.NewV4()), sid: uuid.Must(uuid.NewV4()),
	}
}

func TestEntrantPresenceFromSession_StampsAndOptsOut(t *testing.T) {
	g := uuid.Must(uuid.FromString("00000000-0000-4000-8000-0000000000e1"))
	valid := &ambassadorQueueStamp{Mode: evr.ModeArenaPublic, GroupID: g, ExpiresAt: time.Now().Add(time.Minute)}
	rating := NewDefaultRating()

	p, err := EntrantPresenceFromSession(newStampSession(valid), uuid.Nil, evr.TeamBlue, rating, g.String(), 0, "")
	if err != nil || p.AmbassadorQueueMode != evr.ModeArenaPublic {
		t.Fatalf("queue-derived presence = %v, %v; want the stamp", p, err)
	}

	// Spectate and set-next-match are not queue entries.
	p, err = EntrantPresenceFromSessionNoStamp(newStampSession(valid), uuid.Nil, evr.TeamBlue, rating, g.String(), 0, "")
	if err != nil || p.AmbassadorQueueMode != 0 {
		t.Errorf("NoStamp presence = %v, %v; want no stamp", p, err)
	}

	// Wrong group, spectator and an expired stamp all get nothing.
	if p, _ := EntrantPresenceFromSession(newStampSession(valid), uuid.Nil, evr.TeamBlue, rating, uuid.Must(uuid.NewV4()).String(), 0, ""); p.AmbassadorQueueMode != 0 {
		t.Error("a stamp for another guild was applied")
	}
	if p, _ := EntrantPresenceFromSession(newStampSession(valid), uuid.Nil, evr.TeamSpectator, rating, g.String(), 0, ""); p.AmbassadorQueueMode != 0 {
		t.Error("a spectator was stamped")
	}
	expired := &ambassadorQueueStamp{Mode: evr.ModeArenaPublic, GroupID: g, ExpiresAt: time.Now().Add(-time.Second)}
	if p, _ := EntrantPresenceFromSession(newStampSession(expired), uuid.Nil, evr.TeamBlue, rating, g.String(), 0, ""); p.AmbassadorQueueMode != 0 {
		t.Error("an expired stamp was applied")
	}
	if p, _ := EntrantPresenceFromSession(newStampSession(nil), uuid.Nil, evr.TeamBlue, rating, g.String(), 0, ""); p.AmbassadorQueueMode != 0 {
		t.Error("a session with no stamp was stamped")
	}
}

// Spectating and set-next-match must not carry the ambassador stamp. They call
// the builder from places a unit test cannot reach, so pin the call sites.
func TestStampOptOutsAreWiredAtTheCallSites(t *testing.T) {
	for _, file := range []string{"evr_lobby_find_spectate.go", "evr_runtime_rpc_setnextmatch.go"} {
		src, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		text := string(src)
		if !strings.Contains(text, "EntrantPresenceFromSessionNoStamp(") {
			t.Errorf("%s does not use EntrantPresenceFromSessionNoStamp", file)
		}
		if strings.Contains(text, "EntrantPresenceFromSession(") {
			t.Errorf("%s still builds a stamped presence", file)
		}
	}
}
