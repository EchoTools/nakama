package server

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/heroiclabs/nakama-common/runtime"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"
)

// The old /ambassador command and its settings are retired; see the
// new-player ambassador spec. These fail against the code before retirement.

func TestOldAmbassadorCommandRetired(t *testing.T) {
	for _, c := range mainSlashCommands {
		if c.Name == "ambassador" {
			t.Fatal("mainSlashCommands still registers /ambassador")
		}
	}
}

func TestServiceSettings_NoRetiredAmbassadorKeys(t *testing.T) {
	data := &ServiceSettingsData{}
	FixDefaultServiceSettings(nil, data)
	b, err := json.Marshal(data)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{
		"enable_ambassador_program",
		"ambassador_mu_reduction",
		"ambassador_cooldown_matches",
		"ambassador_min_games_played",
		"ambassador_min_mu",
	} {
		if strings.Contains(string(b), `"`+key+`"`) {
			t.Errorf("service settings still carry retired key %q", key)
		}
	}
}

// ---- service tests: role, mode, ready, self-promote, demote, DMs, logging ----

type ambassadorSentDM struct {
	DiscordID string
	Text      string
}

type ambassadorTestEnv struct {
	nk   *occTestNakamaModule
	svc  *ambassadorService
	logs *observer.ObservedLogs
	dms  []ambassadorSentDM
	// dmErr, when set, makes every DM fail.
	dmErr error
}

func newAmbassadorTestEnv(t *testing.T) *ambassadorTestEnv {
	t.Helper()
	core, logs := observer.New(zapcore.DebugLevel)
	env := &ambassadorTestEnv{nk: newOCCTestNakamaModule(), logs: logs}
	env.svc = newAmbassadorService(env.nk, NewRuntimeGoLogger(zap.New(core)), func(_ context.Context, discordID, text string) error {
		if env.dmErr != nil {
			return env.dmErr
		}
		env.dms = append(env.dms, ambassadorSentDM{DiscordID: discordID, Text: text})
		return nil
	})
	return env
}

// events returns the ambassador_event records with the given event name.
func (e *ambassadorTestEnv) events(name string) []map[string]any {
	var out []map[string]any
	for _, entry := range e.logs.FilterMessage("ambassador_event").All() {
		m := entry.ContextMap()
		if m["event"] == name {
			out = append(out, m)
		}
	}
	return out
}

func (e *ambassadorTestEnv) all() []map[string]any {
	var out []map[string]any
	for _, entry := range e.logs.FilterMessage("ambassador_event").All() {
		out = append(out, entry.ContextMap())
	}
	return out
}

func (e *ambassadorTestEnv) account(t *testing.T, userID string) *AmbassadorAccount {
	t.Helper()
	a, err := LoadAmbassadorAccount(context.Background(), e.nk, userID)
	if err != nil {
		t.Fatalf("load account: %v", err)
	}
	return a
}

// seed stores an account directly, bypassing the service.
func (e *ambassadorTestEnv) seed(t *testing.T, userID string, a AmbassadorAccount) {
	t.Helper()
	a.version = "*"
	if err := StorableWrite(context.Background(), e.nk, userID, &a); err != nil {
		t.Fatalf("seed: %v", err)
	}
}

// Synthetic ids only: no real player ids in tests.
const (
	tModUID     = "00000000-0000-4000-8000-0000000000a1"
	tModDID     = "100000000000000001"
	tAmbUID     = "00000000-0000-4000-8000-0000000000b1"
	tAmbDID     = "100000000000000002"
	tNewUID     = "00000000-0000-4000-8000-0000000000c1"
	tNewDID     = "100000000000000003"
	tOtherUID   = "00000000-0000-4000-8000-0000000000d1"
	tGuildID    = "200000000000000001"
	tGroupID    = "00000000-0000-4000-8000-0000000000e1"
	tNoSuchUser = "00000000-0000-4000-8000-0000000000f1"
)

func tMod(cmd string, args map[string]string) ambassadorCaller {
	return ambassadorCaller{UserID: tModUID, DiscordID: tModDID, Staff: capacityEnforcer, GuildID: tGuildID, GroupID: tGroupID, Command: cmd, Args: args}
}

func tPlayer(uid, did, cmd string, args map[string]string) ambassadorCaller {
	return ambassadorCaller{UserID: uid, DiscordID: did, GuildID: tGuildID, GroupID: tGroupID, Command: cmd, Args: args}
}

var (
	tAmb = ambassadorTarget{UserID: tAmbUID, DiscordID: tAmbDID, DisplayName: "Amb"}
	tNew = ambassadorTarget{UserID: tNewUID, DiscordID: tNewDID, DisplayName: "Newbie"}
)

// ---- item 1: the role ----

func TestSetAmbassador_StaffGrants(t *testing.T) {
	for _, staff := range []ambassadorCapacity{capacityOperator, capacityEnforcer} {
		t.Run(string(staff), func(t *testing.T) {
			env := newAmbassadorTestEnv(t)
			c := tMod("set-ambassador", map[string]string{"user": tAmbDID})
			c.Staff = staff
			res, err := env.svc.Grant(context.Background(), c, tAmb)
			if err != nil || res.Outcome != outcomeOK {
				t.Fatalf("Grant = %+v, %v; want ok", res, err)
			}
			a := env.account(t, tAmbUID)
			if !a.Ambassador || a.Mode {
				t.Errorf("after grant Ambassador=%v Mode=%v; want role on, mode off", a.Ambassador, a.Mode)
			}
			if a.GrantedBy != tModUID || a.GrantedGuildID != tGuildID || a.GrantedGroupID != tGroupID || a.GrantedAt.IsZero() {
				t.Errorf("grant provenance not recorded: %+v", a)
			}
			if len(env.dms) != 0 {
				t.Errorf("grant sent %d DMs; want none", len(env.dms))
			}
			ev := env.events("grant")
			if len(ev) != 1 {
				t.Fatalf("grant events = %d, want 1", len(ev))
			}
			if ev[0]["actor_user_id"] != tModUID || ev[0]["target_user_id"] != tAmbUID || ev[0]["actor_capacity"] != string(staff) {
				t.Errorf("grant event lacks actor/target/capacity: %v", ev[0])
			}
		})
	}
}

func TestSetAmbassador_NonStaffDenied(t *testing.T) {
	// An ambassador, a plain player, and an auditor-or-enforcer-of-another-guild
	// all reach the service with no staff capacity.
	for _, who := range []struct{ name, uid, did string }{
		{"player", tOtherUID, "100000000000000009"},
		{"ambassador", tAmbUID, tAmbDID},
	} {
		t.Run(who.name, func(t *testing.T) {
			env := newAmbassadorTestEnv(t)
			env.seed(t, tAmbUID, AmbassadorAccount{Ambassador: true})
			c := tPlayer(who.uid, who.did, "set-ambassador", map[string]string{"user": tNewDID})
			res, err := env.svc.Grant(context.Background(), c, tNew)
			if err != nil || res.Outcome != outcomeDenied {
				t.Fatalf("Grant = %+v, %v; want denied", res, err)
			}
			if env.account(t, tNewUID).Ambassador {
				t.Error("a denied grant still wrote the role")
			}
			ev := env.events("denied")
			if len(ev) != 1 || ev[0]["actor_user_id"] != who.uid {
				t.Errorf("denied event missing or lacks actor id: %v", ev)
			}
		})
	}
}

func TestSetAmbassador_SelfGrantRefused(t *testing.T) {
	env := newAmbassadorTestEnv(t)
	self := ambassadorTarget{UserID: tModUID, DiscordID: tModDID}
	res, _ := env.svc.Grant(context.Background(), tMod("set-ambassador", nil), self)
	if res.Outcome != outcomeSelfTarget {
		t.Fatalf("outcome = %q, want self_target", res.Outcome)
	}
	if env.account(t, tModUID).Ambassador {
		t.Error("self-grant wrote the role")
	}
}

func TestSetAmbassador_AlreadyIsNoopNoDM(t *testing.T) {
	env := newAmbassadorTestEnv(t)
	env.seed(t, tAmbUID, AmbassadorAccount{Ambassador: true, Mode: true})
	res, _ := env.svc.Grant(context.Background(), tMod("set-ambassador", nil), tAmb)
	if res.Outcome != outcomeNoop {
		t.Fatalf("outcome = %q, want noop", res.Outcome)
	}
	if a := env.account(t, tAmbUID); !a.Mode {
		t.Error("a no-op grant reset the holder's mode")
	}
	if len(env.events("noop")) != 1 || len(env.events("grant")) != 0 || len(env.dms) != 0 {
		t.Errorf("noop grant logged/sent wrongly: events=%v dms=%v", env.all(), env.dms)
	}
}

func removalText(user, reason string) string {
	return "Hi " + user + ". Please read the following:\n" +
		"We (Echo VR Lounge) have unfortunately decided to remove you from the ambassador team. We are thankful for your work, but some things came up that we don't allow our ambassadors to do. We are not trying to cause any issues, but we just want what's best for the community.\n" +
		"Reason: " + reason + "\n" +
		"Again, thank you for volunteering,\n" +
		"-The Ambassador Leads"
}

func TestAmbassadorRemove_ClearsRoleAndModeAndSendsFinalDM(t *testing.T) {
	env := newAmbassadorTestEnv(t)
	env.seed(t, tAmbUID, AmbassadorAccount{Ambassador: true, Mode: true})
	// Someone the ambassador graduated must stay graduated.
	env.seed(t, tNewUID, AmbassadorAccount{Graduated: true, GraduatedBy: tAmbUID, GraduatedVia: "ambassador"})

	c := tMod("ambassador-remove", map[string]string{"user_id": tAmbDID, "reason": "harassed a player"})
	res, err := env.svc.Remove(context.Background(), c, tAmb, "harassed a player")
	if err != nil || res.Outcome != outcomeOK || res.DMFailed {
		t.Fatalf("Remove = %+v, %v; want ok, DM delivered", res, err)
	}
	a := env.account(t, tAmbUID)
	if a.Ambassador || a.Mode {
		t.Errorf("after remove Ambassador=%v Mode=%v; want both cleared", a.Ambassador, a.Mode)
	}
	if a.RemovedBy != tModUID || a.RemovalReason != "harassed a player" {
		t.Errorf("removal provenance not recorded: %+v", a)
	}
	if !env.account(t, tNewUID).Graduated {
		t.Error("removing the ambassador un-graduated someone they graduated")
	}
	if len(env.dms) != 1 || env.dms[0].DiscordID != tAmbDID {
		t.Fatalf("DMs = %+v, want exactly one to the removed ambassador", env.dms)
	}
	if want := removalText("Amb", "harassed a player"); env.dms[0].Text != want {
		t.Errorf("removal DM text mismatch.\n got: %q\nwant: %q", env.dms[0].Text, want)
	}
	ev := env.events("remove")
	if len(ev) != 1 || ev[0]["removal_reason"] != "harassed a player" || ev[0]["actor_user_id"] != tModUID || ev[0]["target_user_id"] != tAmbUID {
		t.Errorf("remove event lacks actor/target/removal_reason: %v", ev)
	}
}

func TestAmbassadorRemove_DisplayNameIsMarkdownEscaped(t *testing.T) {
	env := newAmbassadorTestEnv(t)
	env.seed(t, tAmbUID, AmbassadorAccount{Ambassador: true})
	target := ambassadorTarget{UserID: tAmbUID, DiscordID: tAmbDID, DisplayName: "*bold*"}
	if _, err := env.svc.Remove(context.Background(), tMod("ambassador-remove", nil), target, "r"); err != nil {
		t.Fatal(err)
	}
	if len(env.dms) != 1 || strings.Contains(env.dms[0].Text, "Hi *bold*.") {
		t.Errorf("display name not escaped in DM: %+v", env.dms)
	}
}

func TestAmbassadorRemove_ReasonRequiredAndBounded(t *testing.T) {
	for _, tc := range []struct {
		name, reason string
		want         ambassadorOutcome
	}{
		{"empty", "", outcomeReasonRequired},
		{"whitespace", "  \n\t ", outcomeReasonRequired},
		{"too long", strings.Repeat("x", AmbassadorRemovalReasonMaxLen+1), outcomeReasonTooLong},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env := newAmbassadorTestEnv(t)
			env.seed(t, tAmbUID, AmbassadorAccount{Ambassador: true})
			res, _ := env.svc.Remove(context.Background(), tMod("ambassador-remove", nil), tAmb, tc.reason)
			if res.Outcome != tc.want {
				t.Fatalf("outcome = %q, want %q", res.Outcome, tc.want)
			}
			if !env.account(t, tAmbUID).Ambassador || len(env.dms) != 0 {
				t.Error("a refused removal changed state or sent a DM")
			}
		})
	}
	t.Run("exactly at the limit is accepted", func(t *testing.T) {
		env := newAmbassadorTestEnv(t)
		env.seed(t, tAmbUID, AmbassadorAccount{Ambassador: true})
		res, _ := env.svc.Remove(context.Background(), tMod("ambassador-remove", nil), tAmb, strings.Repeat("x", AmbassadorRemovalReasonMaxLen))
		if res.Outcome != outcomeOK {
			t.Fatalf("outcome = %q, want ok", res.Outcome)
		}
	})
}

func TestAmbassadorRemove_NonStaffSelfAndNotAmbassador(t *testing.T) {
	env := newAmbassadorTestEnv(t)
	env.seed(t, tAmbUID, AmbassadorAccount{Ambassador: true})

	res, _ := env.svc.Remove(context.Background(), tPlayer(tOtherUID, "100000000000000009", "ambassador-remove", nil), tAmb, "r")
	if res.Outcome != outcomeDenied || !env.account(t, tAmbUID).Ambassador {
		t.Errorf("non-staff remove = %q, role still held=%v; want denied and role kept", res.Outcome, env.account(t, tAmbUID).Ambassador)
	}

	res, _ = env.svc.Remove(context.Background(), tMod("ambassador-remove", nil), ambassadorTarget{UserID: tModUID, DiscordID: tModDID}, "r")
	if res.Outcome != outcomeSelfTarget {
		t.Errorf("self remove = %q, want self_target", res.Outcome)
	}

	res, _ = env.svc.Remove(context.Background(), tMod("ambassador-remove", nil), tNew, "r")
	if res.Outcome != outcomeNoop || len(env.dms) != 0 {
		t.Errorf("remove of a non-ambassador = %q with %d DMs; want noop and none", res.Outcome, len(env.dms))
	}
}

func TestAmbassadorRemove_DMFailureStillRemovesAndLogsDMFailed(t *testing.T) {
	env := newAmbassadorTestEnv(t)
	env.seed(t, tAmbUID, AmbassadorAccount{Ambassador: true, Mode: true})
	env.dmErr = errors.New("cannot send messages to this user")
	res, err := env.svc.Remove(context.Background(), tMod("ambassador-remove", nil), tAmb, "r")
	if err != nil || res.Outcome != outcomeOK || !res.DMFailed {
		t.Fatalf("Remove = %+v, %v; want ok with DMFailed", res, err)
	}
	if a := env.account(t, tAmbUID); a.Ambassador || a.Mode {
		t.Error("a failed DM left the role in place")
	}
	if len(env.events("dm_failed")) != 1 || len(env.events("remove")) != 1 {
		t.Errorf("want one dm_failed and one remove event, got %v", env.all())
	}
}

func TestAmbassadorRemove_RemovedAmbassadorCannotToggleAndIsNotEffective(t *testing.T) {
	env := newAmbassadorTestEnv(t)
	env.seed(t, tAmbUID, AmbassadorAccount{Ambassador: true, Mode: true})
	if _, err := env.svc.Remove(context.Background(), tMod("ambassador-remove", nil), tAmb, "r"); err != nil {
		t.Fatal(err)
	}
	if env.account(t, tAmbUID).EffectiveMode() {
		t.Error("EffectiveMode still true after removal")
	}
	res, _ := env.svc.Toggle(context.Background(), tPlayer(tAmbUID, tAmbDID, "ambassador-toggle", nil))
	if res.Outcome != outcomeDenied {
		t.Errorf("toggle after removal = %q, want denied", res.Outcome)
	}
}

// ---- item 2: the toggle ----

func TestAmbassadorToggle_RequiresRole(t *testing.T) {
	env := newAmbassadorTestEnv(t)
	res, _ := env.svc.Toggle(context.Background(), tPlayer(tNewUID, tNewDID, "ambassador-toggle", nil))
	if res.Outcome != outcomeDenied {
		t.Fatalf("toggle by a non-ambassador = %q, want denied", res.Outcome)
	}
	if a := env.account(t, tNewUID); a.Mode {
		t.Error("a denied toggle set the mode")
	}
	if ev := env.events("denied"); len(ev) != 1 || ev[0]["actor_user_id"] != tNewUID {
		t.Errorf("denied toggle not logged with the actor: %v", ev)
	}
}

func TestAmbassadorToggle_FlipsAndLogs(t *testing.T) {
	env := newAmbassadorTestEnv(t)
	env.seed(t, tAmbUID, AmbassadorAccount{Ambassador: true})
	c := tPlayer(tAmbUID, tAmbDID, "ambassador-toggle", nil)

	res, err := env.svc.Toggle(context.Background(), c)
	if err != nil || res.Outcome != outcomeOK || !res.On {
		t.Fatalf("first toggle = %+v, %v; want on", res, err)
	}
	if !env.account(t, tAmbUID).EffectiveMode() {
		t.Error("mode not effective after toggling on")
	}
	res, _ = env.svc.Toggle(context.Background(), c)
	if res.On || env.account(t, tAmbUID).EffectiveMode() {
		t.Error("second toggle did not turn the mode off")
	}
	on, off := env.events("toggle_on"), env.events("toggle_off")
	if len(on) != 1 || len(off) != 1 || on[0]["actor_user_id"] != tAmbUID || on[0]["target_user_id"] != tAmbUID {
		t.Errorf("toggle events wrong: on=%v off=%v", on, off)
	}
}

func TestSetAmbassador_NewGrantStartsInNormalMode(t *testing.T) {
	env := newAmbassadorTestEnv(t)
	// A previous tenure left the mode on; a new grant must not inherit it.
	env.seed(t, tAmbUID, AmbassadorAccount{Mode: true})
	if _, err := env.svc.Grant(context.Background(), tMod("set-ambassador", nil), tAmb); err != nil {
		t.Fatal(err)
	}
	if env.account(t, tAmbUID).EffectiveMode() {
		t.Error("a new grant started in ambassador mode")
	}
}

// ---- items 3-5: ready, self-promote, the DM ----

const (
	wantGraduationFirst = "You have reached the milestone to be raised a tier in matchmaking. Please view the following:\n\n"
)

func TestReady_GraduatesWithProvenanceAndExactDM(t *testing.T) {
	env := newAmbassadorTestEnv(t)
	env.seed(t, tAmbUID, AmbassadorAccount{Ambassador: true, Mode: true})
	c := tPlayer(tAmbUID, tAmbDID, "ambassador-ready", map[string]string{"user_id": tNewDID})
	res, err := env.svc.Ready(context.Background(), c, tNew)
	if err != nil || res.Outcome != outcomeOK || res.DMFailed {
		t.Fatalf("Ready = %+v, %v", res, err)
	}
	a := env.account(t, tNewUID)
	if !a.Graduated || a.GraduatedBy != tAmbUID || a.GraduatedVia != "ambassador" || a.GraduatedAt.IsZero() {
		t.Errorf("graduation provenance wrong: %+v", a)
	}
	want := wantGraduationFirst + "Students ---> Intermediate\nYou gained a tier from an ambassador."
	if len(env.dms) != 1 || env.dms[0].DiscordID != tNewDID || env.dms[0].Text != want {
		t.Errorf("DMs = %+v\nwant one to %s with %q", env.dms, tNewDID, want)
	}
	if amb := env.account(t, tAmbUID); !amb.Ambassador || !amb.Mode || amb.Graduated {
		t.Errorf("ready touched the ambassador's own state: %+v", amb)
	}
	for _, name := range []string{"ready", "graduate"} {
		ev := env.events(name)
		if len(ev) != 1 || ev[0]["actor_user_id"] != tAmbUID || ev[0]["target_user_id"] != tNewUID {
			t.Errorf("%s event lacks actor/target: %v", name, ev)
		}
	}
}

func TestReady_NeedsTheRoleAndNoSelfTarget(t *testing.T) {
	env := newAmbassadorTestEnv(t)
	res, _ := env.svc.Ready(context.Background(), tPlayer(tOtherUID, "100000000000000009", "ambassador-ready", nil), tNew)
	if res.Outcome != outcomeDenied || env.account(t, tNewUID).Graduated || len(env.dms) != 0 {
		t.Errorf("non-ambassador ready = %q, graduated=%v, dms=%d; want denied, no change", res.Outcome, env.account(t, tNewUID).Graduated, len(env.dms))
	}
	// A staff member who is not an ambassador still cannot mark ready.
	res, _ = env.svc.Ready(context.Background(), tMod("ambassador-ready", nil), tNew)
	if res.Outcome != outcomeDenied {
		t.Errorf("staff-but-not-ambassador ready = %q, want denied", res.Outcome)
	}

	env.seed(t, tAmbUID, AmbassadorAccount{Ambassador: true})
	res, _ = env.svc.Ready(context.Background(), tPlayer(tAmbUID, tAmbDID, "ambassador-ready", nil), tAmb)
	if res.Outcome != outcomeSelfTarget || env.account(t, tAmbUID).Graduated {
		t.Errorf("self ready = %q graduated=%v; want self_target and no change", res.Outcome, env.account(t, tAmbUID).Graduated)
	}
}

func TestReady_AlreadyGraduatedIsNoopNoSecondDM(t *testing.T) {
	env := newAmbassadorTestEnv(t)
	env.seed(t, tAmbUID, AmbassadorAccount{Ambassador: true})
	c := tPlayer(tAmbUID, tAmbDID, "ambassador-ready", nil)
	if res, _ := env.svc.Ready(context.Background(), c, tNew); res.Outcome != outcomeOK {
		t.Fatalf("first ready = %q", res.Outcome)
	}
	res, _ := env.svc.Ready(context.Background(), c, tNew)
	if res.Outcome != outcomeNoop || len(env.dms) != 1 {
		t.Errorf("second ready = %q with %d DMs total; want noop and still 1 DM", res.Outcome, len(env.dms))
	}
}

func TestReady_NoModeRequirement(t *testing.T) {
	env := newAmbassadorTestEnv(t)
	env.seed(t, tAmbUID, AmbassadorAccount{Ambassador: true, Mode: false})
	res, _ := env.svc.Ready(context.Background(), tPlayer(tAmbUID, tAmbDID, "ambassador-ready", nil), tNew)
	if res.Outcome != outcomeOK {
		t.Errorf("ready by an ambassador in normal mode = %q, want ok", res.Outcome)
	}
}

func TestSelfPromote_GraduatesWithSelfLineAndLogs(t *testing.T) {
	env := newAmbassadorTestEnv(t)
	c := tPlayer(tNewUID, tNewDID, "level-self-promote", nil)
	res, err := env.svc.SelfPromote(context.Background(), c)
	if err != nil || res.Outcome != outcomeOK {
		t.Fatalf("SelfPromote = %+v, %v", res, err)
	}
	a := env.account(t, tNewUID)
	if !a.Graduated || a.GraduatedBy != tNewUID || a.GraduatedVia != "self" {
		t.Errorf("self-promote provenance wrong: %+v", a)
	}
	want := wantGraduationFirst + "Students ---> Intermediate\nYou gained a tier by promoting yourself."
	if len(env.dms) != 1 || env.dms[0].DiscordID != tNewDID || env.dms[0].Text != want {
		t.Errorf("DMs = %+v, want one with %q", env.dms, want)
	}
	if ev := env.events("self_promote"); len(ev) != 1 || ev[0]["actor_user_id"] != tNewUID || ev[0]["actor_capacity"] != "self" {
		t.Errorf("self_promote event wrong: %v", ev)
	}
	// Again: no-op, no second DM.
	if res, _ := env.svc.SelfPromote(context.Background(), c); res.Outcome != outcomeNoop || len(env.dms) != 1 {
		t.Errorf("second self-promote = %q with %d DMs; want noop, 1 DM", res.Outcome, len(env.dms))
	}
}

func TestGraduate_DMFailureStillGraduatesAndLogs(t *testing.T) {
	env := newAmbassadorTestEnv(t)
	env.dmErr = errors.New("closed DMs")
	res, err := env.svc.SelfPromote(context.Background(), tPlayer(tNewUID, tNewDID, "level-self-promote", nil))
	if err != nil || res.Outcome != outcomeOK || !res.DMFailed {
		t.Fatalf("SelfPromote = %+v, %v; want ok with DMFailed", res, err)
	}
	if !env.account(t, tNewUID).Graduated {
		t.Error("a failed DM undid the graduation")
	}
	if len(env.events("dm_failed")) != 1 {
		t.Errorf("want one dm_failed event, got %v", env.all())
	}
}

// ---- item 6: staff demote ----

func TestDemote_StaffClearsGraduatedAndSendsReversedDM(t *testing.T) {
	env := newAmbassadorTestEnv(t)
	env.seed(t, tNewUID, AmbassadorAccount{Graduated: true, GraduatedBy: tAmbUID, GraduatedVia: "ambassador"})
	c := tMod("level-demote", map[string]string{"user_id": tNewDID})
	res, err := env.svc.Demote(context.Background(), c, tNew)
	if err != nil || res.Outcome != outcomeOK {
		t.Fatalf("Demote = %+v, %v", res, err)
	}
	a := env.account(t, tNewUID)
	if a.Graduated || a.DemotedBy != tModUID || a.DemotedAt.IsZero() {
		t.Errorf("demotion not recorded: %+v", a)
	}
	want := wantGraduationFirst + "Intermediate ---> Students\nYou were moved down a tier by staff."
	if len(env.dms) != 1 || env.dms[0].DiscordID != tNewDID || env.dms[0].Text != want {
		t.Errorf("DMs = %+v, want one with %q", env.dms, want)
	}
	if ev := env.events("demote"); len(ev) != 1 || ev[0]["actor_user_id"] != tModUID || ev[0]["target_user_id"] != tNewUID {
		t.Errorf("demote event lacks actor/target: %v", ev)
	}
}

func TestDemote_NonStaffDeniedSelfRefusedNotGraduatedNoop(t *testing.T) {
	env := newAmbassadorTestEnv(t)
	env.seed(t, tNewUID, AmbassadorAccount{Graduated: true})
	env.seed(t, tAmbUID, AmbassadorAccount{Ambassador: true})

	for _, c := range []ambassadorCaller{
		tPlayer(tNewUID, tNewDID, "level-demote", nil),                // the player themself
		tPlayer(tAmbUID, tAmbDID, "level-demote", nil),                // an ambassador
		tPlayer(tOtherUID, "100000000000000009", "level-demote", nil), // anyone
	} {
		res, _ := env.svc.Demote(context.Background(), c, tNew)
		if res.Outcome != outcomeDenied {
			t.Errorf("demote by %s = %q, want denied", c.UserID, res.Outcome)
		}
	}
	if !env.account(t, tNewUID).Graduated || len(env.dms) != 0 {
		t.Error("a denied demote changed state or sent a DM")
	}

	res, _ := env.svc.Demote(context.Background(), tMod("level-demote", nil), ambassadorTarget{UserID: tModUID, DiscordID: tModDID})
	if res.Outcome != outcomeSelfTarget {
		t.Errorf("self demote = %q, want self_target", res.Outcome)
	}

	res, _ = env.svc.Demote(context.Background(), tMod("level-demote", nil), tAmb)
	if res.Outcome != outcomeNoop || len(env.dms) != 0 {
		t.Errorf("demote of a non-graduated account = %q with %d DMs; want noop, none", res.Outcome, len(env.dms))
	}
}

func TestDemote_DMFailureStillDemotesAndDoesNotTouchRole(t *testing.T) {
	env := newAmbassadorTestEnv(t)
	env.seed(t, tAmbUID, AmbassadorAccount{Ambassador: true, Mode: true, Graduated: true})
	env.dmErr = errors.New("closed DMs")
	res, _ := env.svc.Demote(context.Background(), tMod("level-demote", nil), tAmb)
	if res.Outcome != outcomeOK || !res.DMFailed {
		t.Fatalf("Demote = %+v; want ok with DMFailed", res)
	}
	a := env.account(t, tAmbUID)
	if a.Graduated || !a.Ambassador || !a.Mode {
		t.Errorf("demote should clear only the graduated mark: %+v", a)
	}
}

// ---- only level-demote clears graduated ----

func TestGraduated_OnlyDemoteClearsIt(t *testing.T) {
	ctx := context.Background()
	env := newAmbassadorTestEnv(t)
	env.seed(t, tAmbUID, AmbassadorAccount{Ambassador: true, Graduated: true})

	// Everything else that writes the account leaves the mark alone.
	mod := tMod("x", nil)
	amb := tPlayer(tAmbUID, tAmbDID, "x", nil)
	steps := []struct {
		name string
		run  func()
	}{
		{"toggle on", func() { _, _ = env.svc.Toggle(ctx, amb) }},
		{"toggle off", func() { _, _ = env.svc.Toggle(ctx, amb) }},
		{"grant (noop)", func() { _, _ = env.svc.Grant(ctx, mod, tAmb) }},
		{"ready on self refused", func() { _, _ = env.svc.Ready(ctx, amb, tAmb) }},
		{"self-promote (noop)", func() { _, _ = env.svc.SelfPromote(ctx, amb) }},
		{"remove role", func() { _, _ = env.svc.Remove(ctx, mod, tAmb, "r") }},
		{"grant again", func() { _, _ = env.svc.Grant(ctx, mod, tAmb) }},
	}
	for _, s := range steps {
		s.run()
		if !env.account(t, tAmbUID).Graduated {
			t.Fatalf("graduated was cleared by %q", s.name)
		}
	}
	if res, _ := env.svc.Demote(ctx, mod, tAmb); res.Outcome != outcomeOK || env.account(t, tAmbUID).Graduated {
		t.Errorf("level-demote did not clear graduated: %+v", res)
	}
}

// ---- logging: every command emits a record on every path ----

func TestEveryCommandPathLogsAnEvent(t *testing.T) {
	ctx := context.Background()
	type call struct {
		name string
		run  func(env *ambassadorTestEnv)
	}
	amb := tPlayer(tAmbUID, tAmbDID, "c", nil)
	player := tPlayer(tOtherUID, "100000000000000009", "c", nil)
	mod := tMod("c", nil)
	calls := []call{
		{"grant ok", func(e *ambassadorTestEnv) { _, _ = e.svc.Grant(ctx, mod, tAmb) }},
		{"grant denied", func(e *ambassadorTestEnv) { _, _ = e.svc.Grant(ctx, player, tAmb) }},
		{"grant noop", func(e *ambassadorTestEnv) {
			e.seed(t, tAmbUID, AmbassadorAccount{Ambassador: true})
			_, _ = e.svc.Grant(ctx, mod, tAmb)
		}},
		{"remove ok", func(e *ambassadorTestEnv) {
			e.seed(t, tAmbUID, AmbassadorAccount{Ambassador: true})
			_, _ = e.svc.Remove(ctx, mod, tAmb, "r")
		}},
		{"remove denied", func(e *ambassadorTestEnv) { _, _ = e.svc.Remove(ctx, player, tAmb, "r") }},
		{"remove noop", func(e *ambassadorTestEnv) { _, _ = e.svc.Remove(ctx, mod, tAmb, "r") }},
		{"remove no reason", func(e *ambassadorTestEnv) { _, _ = e.svc.Remove(ctx, mod, tAmb, "") }},
		{"toggle ok", func(e *ambassadorTestEnv) {
			e.seed(t, tAmbUID, AmbassadorAccount{Ambassador: true})
			_, _ = e.svc.Toggle(ctx, amb)
		}},
		{"toggle denied", func(e *ambassadorTestEnv) { _, _ = e.svc.Toggle(ctx, player) }},
		{"ready ok", func(e *ambassadorTestEnv) {
			e.seed(t, tAmbUID, AmbassadorAccount{Ambassador: true})
			_, _ = e.svc.Ready(ctx, amb, tNew)
		}},
		{"ready denied", func(e *ambassadorTestEnv) { _, _ = e.svc.Ready(ctx, player, tNew) }},
		{"ready noop", func(e *ambassadorTestEnv) {
			e.seed(t, tAmbUID, AmbassadorAccount{Ambassador: true})
			e.seed(t, tNewUID, AmbassadorAccount{Graduated: true})
			_, _ = e.svc.Ready(ctx, amb, tNew)
		}},
		{"self-promote ok", func(e *ambassadorTestEnv) { _, _ = e.svc.SelfPromote(ctx, player) }},
		{"self-promote noop", func(e *ambassadorTestEnv) {
			_, _ = e.svc.SelfPromote(ctx, player)
			_, _ = e.svc.SelfPromote(ctx, player)
		}},
		{"demote ok", func(e *ambassadorTestEnv) {
			e.seed(t, tNewUID, AmbassadorAccount{Graduated: true})
			_, _ = e.svc.Demote(ctx, mod, tNew)
		}},
		{"demote denied", func(e *ambassadorTestEnv) { _, _ = e.svc.Demote(ctx, player, tNew) }},
		{"demote noop", func(e *ambassadorTestEnv) { _, _ = e.svc.Demote(ctx, mod, tNew) }},
	}
	for _, c := range calls {
		t.Run(c.name, func(t *testing.T) {
			env := newAmbassadorTestEnv(t)
			c.run(env)
			evs := env.all()
			if len(evs) == 0 {
				t.Fatal("the path returned without logging an ambassador_event")
			}
			for _, ev := range evs {
				for _, k := range []string{"event", "actor_user_id", "outcome", "command", "args"} {
					if _, ok := ev[k]; !ok {
						t.Errorf("event %v lacks field %q", ev["event"], k)
					}
				}
			}
		})
	}
}

func TestWriteFailureIsLoudAndReportedNotSuccess(t *testing.T) {
	env := newAmbassadorTestEnv(t)
	env.nk.failNonVersion = errors.New("storage down")
	res, err := env.svc.Grant(context.Background(), tMod("set-ambassador", nil), tAmb)
	if err == nil || res.Outcome != outcomeError {
		t.Fatalf("Grant with failing storage = %+v, %v; want an error and outcome error", res, err)
	}
	if len(env.events("error")) != 1 || len(env.events("grant")) != 0 {
		t.Errorf("a failed write must log error and never log a grant: %v", env.all())
	}
}

func TestAmbassadorAccount_StorageMetaIsServerOnlyAndNewKey(t *testing.T) {
	m := (&AmbassadorAccount{}).StorageMeta()
	if m.Collection != "Ambassador" || m.Key != "account" {
		t.Errorf("storage = %s/%s, want Ambassador/account (not the retired Ambassador/state)", m.Collection, m.Key)
	}
	if m.PermissionRead != int(runtime.STORAGE_PERMISSION_NO_READ) || m.PermissionWrite != int(runtime.STORAGE_PERMISSION_NO_WRITE) {
		t.Errorf("permissions read=%d write=%d, want server-only", m.PermissionRead, m.PermissionWrite)
	}
}

func TestLoadAmbassadorAccount_MissingIsZeroAndDoesNotCreate(t *testing.T) {
	env := newAmbassadorTestEnv(t)
	a := env.account(t, tNoSuchUser)
	if a.Ambassador || a.Mode || a.Graduated {
		t.Errorf("missing account not zero: %+v", a)
	}
	objs, _ := env.nk.StorageRead(context.Background(), []*runtime.StorageRead{{Collection: "Ambassador", Key: "account", UserID: tNoSuchUser}})
	if len(objs) != 0 {
		t.Error("reading a missing account created it; login and queue read this for every player")
	}
}

func TestAmbassadorOldStateObjectIsNeverRead(t *testing.T) {
	env := newAmbassadorTestEnv(t)
	env.nk.seedObject(tAmbUID, "Ambassador", "state", `{"is_active":true}`)
	if a := env.account(t, tAmbUID); a.Ambassador || a.Mode {
		t.Errorf("the retired Ambassador/state object leaked into the new account: %+v", a)
	}
}

func TestAmbassadorWriteRetriesOnVersionConflict(t *testing.T) {
	env := newAmbassadorTestEnv(t)
	env.seed(t, tAmbUID, AmbassadorAccount{Ambassador: true})
	// A competing writer advances the version after our read: the first write is
	// rejected, the retry re-reads and applies on top of the fresh state.
	wrapped := &failOnceNakamaModule{occTestNakamaModule: env.nk}
	env.svc.nk = wrapped
	res, err := env.svc.Toggle(context.Background(), tPlayer(tAmbUID, tAmbDID, "ambassador-toggle", nil))
	if err != nil || res.Outcome != outcomeOK || !res.On {
		t.Fatalf("Toggle across a version conflict = %+v, %v", res, err)
	}
}
