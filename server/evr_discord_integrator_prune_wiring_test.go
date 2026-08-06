package server

import (
	"context"
	"fmt"
	"sync"
	"testing"

	"github.com/bwmarrin/discordgo"
	"github.com/heroiclabs/nakama-common/api"
	"github.com/heroiclabs/nakama-common/runtime"
	"go.uber.org/zap"
)

// The tests in this file exist because snapshotStateGuilds is only a fix if
// pruneGuildGroups actually CALLS it. The snapshot contract tests in
// evr_discord_integrator_state_race_test.go all invoke snapshotStateGuilds
// directly, so every one of them would keep passing if the read site at
// evr_discord_integrator_pruning.go reverted to `d.dg.State.Guilds` and the
// snapshot became dead code. These tests drive pruneGuildGroups itself.
//
// TestPruneGuildGroups_UsesSnapshotNotLiveState is deliberately deterministic
// rather than -race-dependent: CI runs `just test` (justfile:70-71 —
// `go test ./server/... -count=1`) with NO -race flag, so a race-only test
// would not gate a revert.

// prunePhaseLogger records the field maps that pruneGuildGroups attaches to its
// log lines, so a test can inspect the *discordgo.Guild values the prune path
// actually operated on.
type prunePhaseLogger struct {
	mu     *sync.Mutex
	events *[]pruneLogEvent
	fields map[string]any
}

type pruneLogEvent struct {
	level  string
	msg    string
	fields map[string]any
}

func newPrunePhaseLogger() *prunePhaseLogger {
	return &prunePhaseLogger{
		mu:     &sync.Mutex{},
		events: &[]pruneLogEvent{},
		fields: map[string]any{},
	}
}

func (l *prunePhaseLogger) record(level, format string, v ...interface{}) {
	l.mu.Lock()
	defer l.mu.Unlock()
	msg := format
	if len(v) > 0 {
		msg = fmt.Sprintf(format, v...)
	}
	*l.events = append(*l.events, pruneLogEvent{level: level, msg: msg, fields: l.fields})
}

func (l *prunePhaseLogger) Debug(format string, v ...interface{}) { l.record("debug", format, v...) }
func (l *prunePhaseLogger) Info(format string, v ...interface{})  { l.record("info", format, v...) }
func (l *prunePhaseLogger) Warn(format string, v ...interface{})  { l.record("warn", format, v...) }
func (l *prunePhaseLogger) Error(format string, v ...interface{}) { l.record("error", format, v...) }

func (l *prunePhaseLogger) WithField(key string, value interface{}) runtime.Logger {
	return l.WithFields(map[string]interface{}{key: value})
}

func (l *prunePhaseLogger) WithFields(fields map[string]interface{}) runtime.Logger {
	merged := make(map[string]any, len(l.fields)+len(fields))
	for k, v := range l.fields {
		merged[k] = v
	}
	for k, v := range fields {
		merged[k] = v
	}
	return &prunePhaseLogger{mu: l.mu, events: l.events, fields: merged}
}

func (l *prunePhaseLogger) Fields() map[string]interface{} { return l.fields }

// orphanGuildsLogged returns the guild slice pruneGuildGroups attached to its
// mass-leave safety abort.
func (l *prunePhaseLogger) orphanGuildsLogged(t *testing.T) []*discordgo.Guild {
	t.Helper()
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, e := range *l.events {
		if v, ok := e.fields["orphan_guilds"]; ok {
			guilds, ok := v.([]*discordgo.Guild)
			if !ok {
				t.Fatalf("orphan_guilds logged as %T, want []*discordgo.Guild", v)
			}
			return guilds
		}
	}
	t.Fatalf("pruneGuildGroups never logged orphan_guilds; events=%+v", *l.events)
	return nil
}

// emptyGroupsNakamaModule reports that Nakama has no guild groups at all, so
// every guild in the discordgo state becomes an orphan candidate. Every other
// NakamaModule method is nil: if pruneGuildGroups reaches one, the test panics
// rather than silently hitting a database.
type emptyGroupsNakamaModule struct {
	runtime.NakamaModule
}

func (m *emptyGroupsNakamaModule) GroupsList(ctx context.Context, name, langTag string, members *int, open *bool, limit int, cursor string) ([]*api.Group, string, error) {
	return nil, "", nil
}

// newPruneTestIntegrator builds the minimum DiscordIntegrator pruneGuildGroups
// needs. db is deliberately nil: no code path exercised here may touch it.
//
// idcache is primed so DiscordIDToUserID short-circuits on the bot lookup and
// returns "". That makes guildSync fail at its first check ("failed to get bot
// user ID from state", evr_discord_integrator.go) and return immediately,
// which is what keeps reconcileOrphanGuilds from making network calls while
// still leaving the guild in the orphan set the safety check logs.
func newPruneTestIntegrator(t *testing.T, state *discordgo.State) *DiscordIntegrator {
	t.Helper()
	idcache := &MapOf[string, string]{}
	idcache.Store(botDiscordIDForPruneTest, "")
	return &DiscordIntegrator{
		ctx:     context.Background(),
		logger:  zap.NewNop(),
		nk:      &emptyGroupsNakamaModule{},
		dg:      &discordgo.Session{State: state},
		idcache: idcache,
	}
}

const botDiscordIDForPruneTest = "bot-discord-id"

// TestPruneGuildGroups_UsesSnapshotNotLiveState is the wiring pin: it drives
// pruneGuildGroups end to end and proves the *discordgo.Guild values it works
// with came from snapshotStateGuilds, not from d.dg.State.Guilds.
//
// It fails deterministically (no -race needed) if the read site reverts to live
// state, because the live guild carries a populated Roles slice that
// copyStateGuild clears, and the live *Guild / *Channel pointers are the ones
// the gateway overwrites in place.
func TestPruneGuildGroups_UsesSnapshotNotLiveState(t *testing.T) {
	state := discordgo.NewState()
	state.User = &discordgo.User{ID: botDiscordIDForPruneTest}
	if err := state.GuildAdd(&discordgo.Guild{
		ID:      "guild-orphan",
		Name:    "Orphan",
		OwnerID: "owner-1",
		// Cleared by copyStateGuild; still present on the live object.
		Roles:   []*discordgo.Role{{ID: "role-1"}},
		Members: []*discordgo.Member{{User: &discordgo.User{ID: "u1"}}},
		Channels: []*discordgo.Channel{
			{ID: "c1", GuildID: "guild-orphan", Type: discordgo.ChannelTypeGuildText, Name: "rules", Topic: "be nice"},
		},
	}); err != nil {
		t.Fatalf("GuildAdd: %v", err)
	}
	// A nil entry in State.Guilds: snapshotStateGuilds skips it, an unguarded
	// live read panics on g.ID.
	state.Guilds = append(state.Guilds, nil)

	live, err := state.Guild("guild-orphan")
	if err != nil {
		t.Fatalf("Guild: %v", err)
	}
	liveChannel := live.Channels[0]

	d := newPruneTestIntegrator(t, state)
	logger := newPrunePhaseLogger()

	// pruneSafetyThreshold 0 with one orphan guild trips the mass-leave abort,
	// which logs the orphan guilds and returns before any GuildLeave or
	// GroupDelete call. doGuildLeaves/doGroupDeletes are false as well.
	err = d.pruneGuildGroups(context.Background(), logger, false, false, 0)
	if err == nil {
		t.Fatal("expected the mass-leave safety abort to return an error")
	}

	got := logger.orphanGuildsLogged(t)
	if len(got) != 1 {
		t.Fatalf("got %d orphan guilds, want 1 (the nil State.Guilds entry must be skipped)", len(got))
	}
	g := got[0]

	if g == live {
		t.Error("pruneGuildGroups operated on the LIVE *Guild from discordgo state; it must use snapshotStateGuilds, whose copies the gateway cannot overwrite in place")
	}
	if len(g.Roles) != 0 {
		t.Error("pruneGuildGroups' guild still carries Roles; copyStateGuild clears them, so this guild came from live state")
	}
	if len(g.Members) != 0 {
		t.Error("pruneGuildGroups' guild still carries Members; copyStateGuild clears them, so this guild came from live state")
	}
	if len(g.Channels) != 1 {
		t.Fatalf("got %d channels, want 1", len(g.Channels))
	}
	if g.Channels[0] == liveChannel {
		t.Error("pruneGuildGroups' guild aliases the LIVE *Channel pointers; ChannelAdd overwrites them in place under State.Lock()")
	}
	// The scalars the prune path and guildSync read must survive the copy.
	if g.ID != "guild-orphan" || g.Name != "Orphan" || g.OwnerID != "owner-1" {
		t.Errorf("snapshot dropped scalars the prune path reads: %+v", g)
	}
}

// TestPruneGuildGroups_SafeAgainstGatewayMutation drives pruneGuildGroups
// concurrently with the gateway mutations discordgo performs under State.Lock()
// (GuildAdd append + in-place overwrite, ChannelAdd, ChannelRemove, and the
// READY assignment of State.User). It is the -race companion to the
// deterministic wiring test above: it catches ANY unlocked read of live state
// on the prune path, including ones a copy-shaped but unlocked implementation
// would still perform.
//
// Because nk reports no groups, every state guild is an orphan, so this also
// drives reconcileOrphanGuilds -> guildSync — pinning the bot-ID read at its
// real production call site rather than only through the accessor's own test.
func TestPruneGuildGroups_SafeAgainstGatewayMutation(t *testing.T) {
	const guilds = 4
	state := newTestDiscordState(t, guilds)
	state.User = &discordgo.User{ID: botDiscordIDForPruneTest}

	d := newPruneTestIntegrator(t, state)
	logger := newPrunePhaseLogger()

	stop := make(chan struct{})
	gatewayDone := make(chan struct{})
	go func() {
		defer close(gatewayDone)
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			default:
			}
			guildID := fmt.Sprintf("guild-%d", i%guilds)
			_ = state.GuildAdd(&discordgo.Guild{
				ID:      guildID,
				Name:    fmt.Sprintf("Guild %d rev %d", i%guilds, i),
				OwnerID: fmt.Sprintf("owner-%d", i),
				Icon:    fmt.Sprintf("icon-%d", i),
				Channels: []*discordgo.Channel{
					{ID: guildID + "-rules", GuildID: guildID, Type: discordgo.ChannelTypeGuildText, Name: "rules", Topic: "rev"},
				},
			})
			ch := &discordgo.Channel{
				ID:      guildID + "-churn",
				GuildID: guildID,
				Type:    discordgo.ChannelTypeGuildText,
				Name:    "rules",
				Topic:   "churn",
			}
			_ = state.ChannelAdd(ch)
			_ = state.ChannelRemove(ch)

			// The READY write, narrowed to the one word that matters. onReady
			// assigns the whole embedded Ready struct (`s.Ready = *r`) under
			// State.Lock(), which rewrites the User pointer; assigning
			// State.User directly hits the same word without also clearing
			// State.Guilds (State embeds Ready, so State.Guilds IS
			// Ready.Guilds). The ID is held constant so the integrator's
			// idcache still short-circuits and no database lookup is reached.
			state.Lock()
			state.User = &discordgo.User{ID: botDiscordIDForPruneTest}
			state.Unlock()
		}
	}()

	// A threshold above the guild count keeps the safety abort quiet, so the
	// full prune body runs. doGuildLeaves/doGroupDeletes stay false, so no
	// GuildLeave/GroupDelete call is ever made.
	for i := 0; i < 200; i++ {
		if err := d.pruneGuildGroups(context.Background(), logger, false, false, 1000); err != nil {
			close(stop)
			<-gatewayDone
			t.Fatalf("pruneGuildGroups: %v", err)
		}
	}

	close(stop)
	<-gatewayDone
}

// TestBotDiscordIDFromState covers the accessor that replaced the unlocked
// `d.dg.State.User.ID` read in guildSync.
func TestBotDiscordIDFromState(t *testing.T) {
	if got := botDiscordIDFromState(nil); got != "" {
		t.Errorf("nil state: got %q, want %q", got, "")
	}

	// Pre-READY: State.User is nil. The old expression panicked here.
	if got := botDiscordIDFromState(discordgo.NewState()); got != "" {
		t.Errorf("nil State.User: got %q, want %q", got, "")
	}

	state := discordgo.NewState()
	state.User = &discordgo.User{ID: "bot-42"}
	if got := botDiscordIDFromState(state); got != "bot-42" {
		t.Errorf("got %q, want %q", got, "bot-42")
	}
}

// TestBotDiscordIDFromState_ConcurrentWithReady proves the read is serialized
// against the gateway's READY handling, which assigns the whole embedded Ready
// struct (`s.Ready = *r`) under State.Lock() and so writes the User pointer
// word itself. Fails under -race without the lock.
func TestBotDiscordIDFromState_ConcurrentWithReady(t *testing.T) {
	state := discordgo.NewState()
	state.User = &discordgo.User{ID: "bot-0"}

	stop := make(chan struct{})
	gatewayDone := make(chan struct{})
	go func() {
		defer close(gatewayDone)
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			default:
			}
			// Exactly what State.onReady does on every gateway reconnect.
			state.Lock()
			state.Ready = discordgo.Ready{User: &discordgo.User{ID: fmt.Sprintf("bot-%d", i)}}
			state.Unlock()
		}
	}()

	for i := 0; i < 2000; i++ {
		if got := botDiscordIDFromState(state); got == "" {
			close(stop)
			<-gatewayDone
			t.Fatal("read an empty bot ID; guildSync would fail every orphan reconciliation")
		}
	}

	close(stop)
	<-gatewayDone
}
