package server

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/gofrs/uuid/v5"
	"github.com/heroiclabs/nakama-common/api"
	"github.com/heroiclabs/nakama/v3/server/evr"
)

// #625: a party member's own tracker entry named the leader's match M while
// the client's lobby find reported no current match (or another one). Every
// "already in leader's match" decision compared only the tracker entries, so
// the find ended with no join and no reply. The member's matchservice entry is
// cleared only at session close (UntrackAll), so it can be stale.
//
// The rule: when the client does not report M as current, the client's report
// wins over the member's tracker entry. The member is not in M and gets the
// normal follow: join the leader's match if it is joinable, otherwise the
// existing poll/matchmake path. A join is skipped only when the client reports
// M as current and the tracker agrees (#624).

// staleTrackerReadRegistry records every label read by match ID, so a test can
// tell TryFollowPartyLeader's validation read from the read lobbyJoin makes
// when it attempts the join. GetMatch reaches this type through the
// runtime.NakamaModule interface (MatchLabelByID -> MatchGet ->
// matchRegistry.GetMatch), so this override is the one that runs.
type staleTrackerReadRegistry struct {
	*mockFollowMatchRegistry
	mu    sync.Mutex
	reads map[string]int
}

func newStaleTrackerReadRegistry() *staleTrackerReadRegistry {
	return &staleTrackerReadRegistry{mockFollowMatchRegistry: newMockFollowMatchRegistry(), reads: map[string]int{}}
}

func (r *staleTrackerReadRegistry) GetMatch(ctx context.Context, id string) (*api.Match, string, error) {
	r.mu.Lock()
	r.reads[id]++
	r.mu.Unlock()
	return r.mockFollowMatchRegistry.GetMatch(ctx, id)
}

func (r *staleTrackerReadRegistry) readsOf(id MatchID) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.reads[id.String()]
}

// staleTrackerEnv puts the leader (idle, not queueing) and the member's stale
// tracker entry in match M. The client reports current, and requests mode.
// label == nil leaves M unreadable in the registry.
func staleTrackerEnv(t *testing.T, label *MatchLabel, requested evr.Symbol, current MatchID) (*followTestEnv, *staleTrackerReadRegistry, MatchID) {
	t.Helper()
	env := newFollowTestEnv(t)
	m := MatchID{UUID: uuid.Must(uuid.NewV4()), Node: "testnode"}
	env.setLeaderMatch(m)
	env.setFollowerMatch(m) // stale when the client does not report m

	registry := newStaleTrackerReadRegistry()
	if label != nil {
		label.ID = m
		groupID := env.groupID
		label.GroupID = &groupID
		registry.SetMatch(m, label)
	}
	// metrics lets lobbyJoin's authorization return its error (no session
	// parameters in this context) instead of panicking in its deferred counter.
	env.pipeline.nk = &RuntimeGoNakamaModule{matchRegistry: registry, metrics: &testMetrics{}}

	env.params.Mode = requested
	env.params.CurrentMatchID = current
	env.pipeline.pollFollowInterval = 10 * time.Millisecond
	env.pipeline.pollFollowMaxDuration = 200 * time.Millisecond
	return env, registry, m
}

// Shape 1: M is the leader's social lobby, the leader is idle, the client is at
// the menu. lobbyFind's steps, in order: the fast-path skip, the
// heading-to-social rewrite, then TryFollowPartyLeader. The member must reach a
// join attempt on M, for both a social and an arena request.
func TestFollow_StaleTrackerInLeaderSocial_ClientNotInIt_JoinsLeader(t *testing.T) {
	other := MatchID{UUID: uuid.Must(uuid.NewV4()), Node: "testnode"}
	cases := []struct {
		name      string
		requested evr.Symbol
		current   MatchID
	}{
		{"nil current, requesting arena", evr.ModeArenaPublic, MatchID{}},
		{"nil current, requesting social", evr.ModeSocialPublic, MatchID{}},
		{"other current, requesting arena", evr.ModeArenaPublic, other},
		{"other current, requesting social", evr.ModeSocialPublic, other},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			logger, logs := followSkipObservedLogger()
			env, registry, m := staleTrackerEnv(t,
				&MatchLabel{Mode: evr.ModeSocialPublic, Open: true, PlayerLimit: 12}, // does not list the member
				tc.requested, tc.current)
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()

			if env.pipeline.isFollowerAlreadyInLeaderMatch(ctx, logger, env.session, env.lobbyGroup, env.params.CurrentMatchID, env.params.Mode) {
				t.Fatalf("lobbyFind's fast path skipped the find: member's tracker names the leader's social lobby %s, "+
					"client reports current=%q; no join and no reply", m.String(), tc.current.String())
			}
			if !env.pipeline.isLeaderHeadingToSocial(ctx, logger, env.session, env.params, env.lobbyGroup) {
				t.Fatal("fixture: expected the idle leader in a social lobby to be heading to social")
			}
			env.params.Mode = evr.ModeSocialPublic // what lobbyFind does when heading to social
			env.params.Level = evr.LevelUnspecified

			readsBefore := registry.readsOf(m)
			result := env.pipeline.TryFollowPartyLeader(ctx, logger, env.session, env.params, env.lobbyGroup)

			if n := logs.FilterMessage("Already in leader's match").Len(); n != 0 {
				t.Errorf("TryFollowPartyLeader trusted the member's tracker entry over the client's report (current=%q): "+
					"result=%v, no join attempted", tc.current.String(), result)
			}
			// TryFollowPartyLeader reads M's label once to validate it, and
			// lobbyJoin reads it again as its first step. (With a non-nil
			// current match, the failed join goes on to the poll, which reads
			// it again; that is the existing join-failure handling.)
			if got := registry.readsOf(m) - readsBefore; got < 2 {
				t.Errorf("expected a join attempt on the leader's lobby %s (validation read + lobbyJoin read = 2), got %d label read(s)",
					m.String(), got)
			}
		})
	}
}

// Shape 2: M is not social, or its label cannot be read. The leader is idle in
// M and the client is at the menu, requesting arena. TryFollowPartyLeader must
// not end the find at "Already in leader's match"; it returns false, and the
// poll (the existing path for a non-social follower, reached after lobbyFind
// has armed the matchmaking timeout) must release the member to matchmaking
// rather than report convergence on the stale entry.
func TestFollow_StaleTrackerInLeaderNonSocialOrUnreadable_ClientAtMenu_ReachesPoll(t *testing.T) {
	cases := []struct {
		name  string
		label *MatchLabel
	}{
		{"arena match", &MatchLabel{Mode: evr.ModeArenaPublic, Open: true, PlayerLimit: 8}},
		{"unreadable label", nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			logger, logs := followSkipObservedLogger()
			env, _, m := staleTrackerEnv(t, tc.label, evr.ModeArenaPublic, MatchID{})
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()

			if env.pipeline.isFollowerAlreadyInLeaderMatch(ctx, logger, env.session, env.lobbyGroup, env.params.CurrentMatchID, env.params.Mode) {
				t.Fatal("lobbyFind's fast path skipped the find")
			}
			if env.pipeline.isLeaderHeadingToSocial(ctx, logger, env.session, env.params, env.lobbyGroup) {
				t.Fatal("fixture: leader in a non-social or unreadable match is not heading to social")
			}

			if env.pipeline.TryFollowPartyLeader(ctx, logger, env.session, env.params, env.lobbyGroup) {
				t.Errorf("TryFollowPartyLeader returned true for a member at the menu whose stale tracker entry names %s "+
					"(\"Already in leader's match\" logged %d time(s)); lobbyFind returns with no join and no reply",
					m.String(), logs.FilterMessage("Already in leader's match").Len())
			}
			if env.pipeline.pollFollowPartyLeader(ctx, logger, env.session, env.params, env.lobbyGroup) {
				t.Errorf("pollFollowPartyLeader reported convergence on the member's stale tracker entry for %s; "+
					"lobbyFind returns with no join and no reply instead of releasing the member to matchmaking", m.String())
			}
		})
	}
}

// The social find's no-op guard is the same decision: in a follow, the
// intended target is the leader's lobby, and a member whose stale tracker
// entry names it must not be treated as already there when the client does
// not report it. Reached when the join above fails at the menu and lobbyFind
// falls through to lobbyFindOrCreateSocial.
func TestCurrentSocialLobby_FollowStaleTracker_ClientNotInIt_NotNoop(t *testing.T) {
	other := MatchID{UUID: uuid.Must(uuid.NewV4()), Node: "testnode"}
	for _, current := range []MatchID{{}, other} {
		t.Run("current="+current.String(), func(t *testing.T) {
			env, _, m := staleTrackerEnv(t, &MatchLabel{Mode: evr.ModeSocialPublic, Open: true, PlayerLimit: 12},
				evr.ModeSocialPublic, current)
			env.params.PartyGroupName = "squad"

			if got := env.pipeline.currentSocialLobbyForSession(context.Background(), loggerForTest(t), env.session, env.params, env.lobbyGroup); !got.IsNil() {
				t.Errorf("social find treated the member as already in the leader's lobby %s from its tracker entry alone "+
					"(client current=%q); lobbyFindOrCreateSocial returns nil with no join and no reply", m.String(), current.String())
			}
		})
	}
}

// A stale entry naming M at poll start must not hide a real placement into
// the same M during the poll. The client reports lobby X; TryFollow's join to
// M failed (M full), so the member polls. The member is then placed into M
// (match accept rewrites the entry with tracker.Update,
// evr_pipeline_lobby.go:63) and M's label read errors. The poll must report
// convergence, not wait out its budget and release the member to solo
// matchmaking while it sits in M (a party split).
func TestPoll_StaleEntryRewrittenToSameMatch_LabelErrors_Converges(t *testing.T) {
	x := MatchID{UUID: uuid.Must(uuid.NewV4()), Node: "testnode"}
	env, _, m := staleTrackerEnv(t, nil, evr.ModeSocialPublic, x) // M's label read errors
	env.pipeline.pollFollowInterval = 20 * time.Millisecond
	env.pipeline.pollFollowMaxDuration = 2 * time.Second

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	placed := make(chan struct{})
	go func() {
		time.Sleep(50 * time.Millisecond)
		env.tracker.Update(context.Background(), env.followerSID,
			PresenceStream{Mode: StreamModeService, Subject: env.followerSID, Label: StreamLabelMatchService},
			env.followerUID, PresenceMeta{Status: m.String()})
		close(placed)
	}()

	start := time.Now()
	result := env.pipeline.pollFollowPartyLeader(ctx, loggerForTest(t), env.session, env.params, env.lobbyGroup)
	elapsed := time.Since(start)
	<-placed

	if !result {
		t.Errorf("poll released the member after %v (budget %v) although it was placed into the leader's match %s during the poll; "+
			"the stale-entry check matched the rewritten entry by match ID alone (party split)",
			elapsed, env.pipeline.pollFollowMaxDuration, m.String())
	}
}

// The client reports M as current and the tracker agrees: every site still
// treats the member as there, and nothing attempts a join (#624's guard).
func TestFollow_ClientReportsLeaderSocial_TrackerAgrees_NoJoin(t *testing.T) {
	logger, logs := followSkipObservedLogger()
	env, registry, m := staleTrackerEnv(t, &MatchLabel{Mode: evr.ModeSocialPublic, Open: true, PlayerLimit: 12},
		evr.ModeSocialPublic, MatchID{})
	env.params.CurrentMatchID = m
	env.params.PartyGroupName = "squad"
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	if !env.pipeline.isFollowerAlreadyInLeaderMatch(ctx, logger, env.session, env.lobbyGroup, env.params.CurrentMatchID, env.params.Mode) {
		t.Error("fast path did not skip a member the client reports in the leader's social lobby")
	}
	readsBefore := registry.readsOf(m)
	if !env.pipeline.TryFollowPartyLeader(ctx, logger, env.session, env.params, env.lobbyGroup) {
		t.Error("TryFollowPartyLeader did not treat the member as already in the leader's social lobby")
	}
	// One read is isLeavingSharedMatch's social check; a second would be
	// lobbyJoin's.
	if got := registry.readsOf(m) - readsBefore; got > 1 {
		t.Errorf("TryFollowPartyLeader read the leader's label %d time(s); a join attempt was made on the lobby the client reports it is in", got)
	}
	if got := env.pipeline.currentSocialLobbyForSession(ctx, logger, env.session, env.params, env.lobbyGroup); got != m {
		t.Errorf("social find guard: expected no-op on %s, got %q", m.String(), got.String())
	}
	if n := followSkipJoinAttempts(logs); n != 0 {
		t.Errorf("%d join attempt(s) on the lobby the client reports it is in", n)
	}
}
