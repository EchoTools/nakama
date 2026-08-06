package server

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/bwmarrin/discordgo"
	"github.com/heroiclabs/nakama-common/api"
	"github.com/heroiclabs/nakama-common/runtime"
	"go.uber.org/zap"
)

// The tests in this file guard the WIRING of the prune pass rather than its
// policy. executePrunePlan is a pure function, so every test that calls it
// directly proves only that the policy is right for the inputs it was handed.
// A one-line change in the wiring -- dropping the unavailable-guild lookup,
// dropping the ID-cache purge, flipping guildSync's leaveOnBannedOwner, or
// deleting the GUILD_DELETE bookkeeping -- silently restores the original
// outage and cache bugs while leaving every policy test green. These tests are
// what make those single-line deletions fail.

// prunePassNakamaModule is the Nakama side of the prune pass: paginated group
// listing plus group deletes. Everything else the integrator might reach for
// is left nil so an unexpected call panics loudly instead of passing silently.
type prunePassNakamaModule struct {
	runtime.NakamaModule

	// pages are returned one per call; the cursor is the next page index.
	pages     [][]*api.Group
	listCalls []groupsListCall
	deleted   []string
	deleteErr map[string]error
}

type groupsListCall struct {
	name    string
	langTag string
	limit   int
	cursor  string
}

func (m *prunePassNakamaModule) GroupsList(_ context.Context, name, langTag string, _ *int, _ *bool, limit int, cursor string) ([]*api.Group, string, error) {
	m.listCalls = append(m.listCalls, groupsListCall{name: name, langTag: langTag, limit: limit, cursor: cursor})

	idx := 0
	if cursor != "" {
		var err error
		if idx, err = strconv.Atoi(cursor); err != nil {
			return nil, "", fmt.Errorf("unexpected cursor %q", cursor)
		}
	}
	if idx >= len(m.pages) {
		return nil, "", nil
	}
	next := ""
	if idx+1 < len(m.pages) {
		next = strconv.Itoa(idx + 1)
	}
	return m.pages[idx], next, nil
}

func (m *prunePassNakamaModule) GroupDelete(_ context.Context, id string) error {
	m.deleted = append(m.deleted, id)
	return m.deleteErr[id]
}

// recordingTransport answers every Discord REST call with 204 No Content and
// records the request, so a test can assert which endpoint was called without
// touching the network.
type recordingTransport struct {
	requests []*http.Request
}

func (rt *recordingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	rt.requests = append(rt.requests, req)
	return &http.Response{
		StatusCode: http.StatusNoContent,
		Body:       io.NopCloser(strings.NewReader("")),
		Request:    req,
	}, nil
}

// newOfflineDiscordSession builds a real *discordgo.Session whose HTTP client
// cannot reach the network. discordgo.New performs no I/O, so the session is a
// faithful stand-in for the production one: REST helpers such as GuildLeave
// build the same request they always do, and recordingTransport captures it.
func newOfflineDiscordSession(t *testing.T) (*discordgo.Session, *recordingTransport) {
	t.Helper()
	sess, err := discordgo.New("Bot test-token")
	if err != nil {
		t.Fatalf("discordgo.New: %v", err)
	}
	rt := &recordingTransport{}
	sess.Client = &http.Client{Transport: rt}
	return sess, rt
}

func testGuildGroup(guildID, groupID string) *api.Group {
	return &api.Group{
		Id:       groupID,
		Name:     "group-" + guildID,
		Metadata: fmt.Sprintf(`{"guild_id":%q}`, guildID),
	}
}

// TestNewPrunePassDepsWiresIntegratorDependencies asserts that every input and
// every side effect of a prune pass is connected to the right receiver on the
// integrator. Each assertion here corresponds to a one-line deletion that would
// otherwise reintroduce a shipped bug.
func TestNewPrunePassDepsWiresIntegratorDependencies(t *testing.T) {
	ctx := context.Background()

	sess, rt := newOfflineDiscordSession(t)
	sess.State.Guilds = []*discordgo.Guild{{ID: "guild_in_state", Name: "in state"}}

	nk := &prunePassNakamaModule{
		pages:     [][]*api.Group{{testGuildGroup("guild_in_state", "grp_in_state")}},
		deleteErr: map[string]error{},
	}

	d := &DiscordIntegrator{
		nk:                nk,
		dg:                sess,
		logger:            zap.NewNop(),
		idcache:           &MapOf[string, string]{},
		unavailableGuilds: &MapOf[string, time.Time]{},
	}
	// Purge must clear both directions of the cache, so seed both.
	d.idcache.Store("guild_gone", "grp_gone")
	d.idcache.Store("grp_gone", "guild_gone")

	now := time.Now()
	d.markGuildUnavailable("guild_down", now)

	type syncCall struct {
		guildID            string
		leaveOnBannedOwner bool
	}
	var syncCalls []syncCall
	syncGuild := func(_ context.Context, logger *zap.Logger, guild *discordgo.Guild, leaveOnBannedOwner bool) error {
		if logger == nil {
			t.Error("repair pass called guildSync with a nil logger")
		}
		syncCalls = append(syncCalls, syncCall{guildID: guild.ID, leaveOnBannedOwner: leaveOnBannedOwner})
		return nil
	}

	deps := d.newPrunePassDeps(ctx, syncGuild)

	t.Run("listGroups reads guild groups from Nakama", func(t *testing.T) {
		groups, cursor, err := deps.listGroups(ctx, "")
		if err != nil {
			t.Fatalf("listGroups: %v", err)
		}
		if cursor != "" {
			t.Errorf("cursor = %q; want empty (one page)", cursor)
		}
		if len(groups) != 1 || groups[0].GetId() != "grp_in_state" {
			t.Fatalf("groups = %v; want the group the module returned", groups)
		}
		if len(nk.listCalls) != 1 {
			t.Fatalf("GroupsList calls = %d; want 1", len(nk.listCalls))
		}
		// The lang-tag filter is what restricts the pass to guild groups. Lose
		// it and every group in the database becomes a prune candidate.
		if got := nk.listCalls[0].langTag; got != GuildGroupLangTag {
			t.Errorf("GroupsList langTag = %q; want %q -- otherwise non-guild groups become prune candidates", got, GuildGroupLangTag)
		}
		if got := nk.listCalls[0].limit; got != prunePageSize {
			t.Errorf("GroupsList limit = %d; want %d", got, prunePageSize)
		}
	})

	t.Run("stateGuilds reads the live Discord session state", func(t *testing.T) {
		got := orphanGuildIDs(deps.stateGuilds())
		if len(got) != 1 || got[0] != "guild_in_state" {
			t.Fatalf("stateGuilds() = %v; want [guild_in_state] from d.dg.State.Guilds", got)
		}
	})

	t.Run("unavailableGuildIDs reads the integrator's outage record", func(t *testing.T) {
		got := deps.unavailableGuildIDs(now)
		if _, ok := got["guild_down"]; !ok || len(got) != 1 {
			t.Fatalf("unavailableGuildIDs(now) = %v; want {guild_down} -- without it a shard outage deletes the guild's group", got)
		}
	})

	t.Run("syncGuild repairs without leaving", func(t *testing.T) {
		if err := deps.actions.syncGuild(&discordgo.Guild{ID: "guild_orphan", Name: "orphan"}); err != nil {
			t.Fatalf("syncGuild: %v", err)
		}
		if len(syncCalls) != 1 || syncCalls[0].guildID != "guild_orphan" {
			t.Fatalf("syncGuild calls = %+v; want one call for guild_orphan", syncCalls)
		}
		// The repair pass must never leave a guild: every leave has to go
		// through the safety-threshold-checked path in executePrunePlan.
		if syncCalls[0].leaveOnBannedOwner {
			t.Fatal("repair pass called guildSync with leaveOnBannedOwner=true; reconciliation would then leave guilds outside the safety valve")
		}
	})

	t.Run("deleteGroup deletes through Nakama", func(t *testing.T) {
		if err := deps.actions.deleteGroup("grp_gone"); err != nil {
			t.Fatalf("deleteGroup: %v", err)
		}
		if len(nk.deleted) != 1 || nk.deleted[0] != "grp_gone" {
			t.Fatalf("GroupDelete calls = %v; want [grp_gone]", nk.deleted)
		}
	})

	t.Run("purgeGuild clears both directions of the ID cache", func(t *testing.T) {
		deps.actions.purgeGuild("guild_gone")
		if _, ok := d.idcache.Load("guild_gone"); ok {
			t.Error("guild_gone -> group ID mapping survived the purge; the guild's return would resolve a deleted group forever")
		}
		if _, ok := d.idcache.Load("grp_gone"); ok {
			t.Error("grp_gone -> guild ID mapping survived the purge")
		}
	})

	t.Run("leaveGuild calls Discord's leave endpoint", func(t *testing.T) {
		if err := deps.actions.leaveGuild("guild_gone"); err != nil {
			t.Fatalf("leaveGuild: %v", err)
		}
		if len(rt.requests) != 1 {
			t.Fatalf("Discord REST calls = %d; want exactly 1", len(rt.requests))
		}
		req := rt.requests[0]
		if req.Method != http.MethodDelete {
			t.Errorf("method = %s; want DELETE", req.Method)
		}
		if !strings.Contains(req.URL.Path, "guild_gone") {
			t.Errorf("URL %s does not name the guild being left", req.URL)
		}
	})
}

// TestRunPrunePassProtectsUnavailableGuildsEndToEnd drives a whole pass through
// runPrunePass -- paginated listing, plan computation, execution -- with only
// the integrator boundary faked. It is the test that fails if the pass stops
// consulting the unavailable-guild record, or stops purging the ID cache after
// a delete.
func TestRunPrunePassProtectsUnavailableGuildsEndToEnd(t *testing.T) {
	logger, _ := observedLogger()
	rec := newPruneRecorder()
	now := time.Now()

	nk := &prunePassNakamaModule{
		// Two pages, so a pass that stops after the first would miss
		// grp_departed entirely.
		pages: [][]*api.Group{
			{testGuildGroup("g_up", "grp_up"), testGuildGroup("g_down", "grp_down")},
			{testGuildGroup("g_departed", "grp_departed"), {Id: "grp_nokey", Name: "no guild id", Metadata: `{}`}},
		},
	}

	deps := prunePassDeps{
		listGroups: func(ctx context.Context, cursor string) ([]*api.Group, string, error) {
			return nk.GroupsList(ctx, "", GuildGroupLangTag, nil, nil, prunePageSize, cursor)
		},
		stateGuilds: func() []*discordgo.Guild {
			// g_down is absent: discordgo already dropped it when the shard
			// went down.
			return []*discordgo.Guild{{ID: "g_up", Name: "up"}}
		},
		unavailableGuildIDs: func(time.Time) map[string]struct{} {
			return map[string]struct{}{"g_down": {}}
		},
		actions: rec.actions(),
	}

	outcome, err := runPrunePass(context.Background(), logger, deps, prunePolicy(false, true, 100), now)
	if err != nil {
		t.Fatalf("runPrunePass: %v", err)
	}

	if len(rec.deleted) != 1 || rec.deleted[0] != "grp_departed" {
		t.Fatalf("deleted = %v; want only [grp_departed] -- g_down is merely unavailable and grp_nokey has no guild ID", rec.deleted)
	}
	if len(rec.purged) != 1 || rec.purged[0] != "g_departed" {
		t.Fatalf("purged = %v; want [g_departed] -- a delete without a cache purge strands the guild", rec.purged)
	}
	if outcome.groupsDeleted != 1 {
		t.Errorf("outcome.groupsDeleted = %d; want 1", outcome.groupsDeleted)
	}
	if len(nk.listCalls) != 2 {
		t.Errorf("GroupsList calls = %d; want 2 (the pass must follow the cursor)", len(nk.listCalls))
	}
}

// TestRunPrunePassSkipsWhenDiscordStateIsEmpty pins the short-circuit. An empty
// State.Guilds means the gateway has not delivered (or has lost) the guild
// list, not that the bot is in no guilds -- every group would look orphaned.
func TestRunPrunePassSkipsWhenDiscordStateIsEmpty(t *testing.T) {
	logger, logs := observedLogger()
	rec := newPruneRecorder()

	deps := prunePassDeps{
		listGroups: func(context.Context, string) ([]*api.Group, string, error) {
			return []*api.Group{testGuildGroup("g1", "grp1")}, "", nil
		},
		stateGuilds:         func() []*discordgo.Guild { return nil },
		unavailableGuildIDs: func(time.Time) map[string]struct{} { return nil },
		actions:             rec.actions(),
	}

	outcome, err := runPrunePass(context.Background(), logger, deps, prunePolicy(true, true, 1000), time.Now())
	if err != nil {
		t.Fatalf("runPrunePass: %v", err)
	}
	if outcome != (pruneOutcome{}) {
		t.Errorf("outcome = %+v; want the zero outcome", outcome)
	}
	if len(rec.deleted) != 0 || len(rec.left) != 0 || len(rec.synced) != 0 {
		t.Fatalf("acted on an empty Discord state: deleted=%v left=%v synced=%v", rec.deleted, rec.left, rec.synced)
	}
	if len(logs.FilterMessage("No guilds found in Discord state, skipping pruning operation").All()) != 1 {
		t.Error("the skip was silent; an operator needs to see why a prune tick did nothing")
	}
}

// TestOnGuildDeleteRecordsAvailability pins the GUILD_DELETE bookkeeping that
// the outage protection depends on. discordgo removes a guild from State.Guilds
// for BOTH kinds of GUILD_DELETE, so this handler is the only place the
// difference between "the shard is down" and "we are out of the guild" is ever
// recorded.
func TestOnGuildDeleteRecordsAvailability(t *testing.T) {
	t.Run("an unavailable guild is remembered", func(t *testing.T) {
		d := &DiscordIntegrator{unavailableGuilds: &MapOf[string, time.Time]{}}

		d.onGuildDelete(zap.NewNop(), nil, &discordgo.GuildDelete{
			Guild: &discordgo.Guild{ID: "g_down", Unavailable: true},
		})

		ids := d.unavailableGuildIDsAsOf(time.Now())
		if _, ok := ids["g_down"]; !ok {
			t.Fatalf("unavailable guilds = %v; want g_down recorded -- otherwise the next prune tick deletes its group", ids)
		}
	})

	t.Run("a real departure clears the record", func(t *testing.T) {
		d := &DiscordIntegrator{unavailableGuilds: &MapOf[string, time.Time]{}}
		d.markGuildUnavailable("g_left", time.Now())

		// handleGuildDelete needs a database, which this test does not have.
		// The bookkeeping under test runs before it, so recovering here still
		// proves the record was cleared -- and if a refactor moved the clear
		// after handleGuildDelete, the assertion below would fail.
		func() {
			defer func() { _ = recover() }()
			d.onGuildDelete(zap.NewNop(), nil, &discordgo.GuildDelete{
				Guild: &discordgo.Guild{ID: "g_left"},
			})
		}()

		if ids := d.unavailableGuildIDsAsOf(time.Now()); len(ids) != 0 {
			t.Fatalf("unavailable guilds = %v; want empty -- a guild we genuinely left must not be protected from pruning", ids)
		}
	})
}
