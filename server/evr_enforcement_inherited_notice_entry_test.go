package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bwmarrin/discordgo"
	"github.com/heroiclabs/nakama-common/api"
	"github.com/heroiclabs/nakama-common/runtime"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"
)

// These tests drive each place an enforcement is issued (the kick handler,
// EnforcementKickRPC with a JSON payload, writeGuildBanEnforcement, the ghost-spam
// suspension) with a fake Discord sender, and assert who is told. Each fails if
// the notify call at its entry point is removed.

const (
	entryOperatorID = "00000000-0000-4000-8000-0000000000a1"
	entryTargetID   = "00000000-0000-4000-8000-0000000000b2"
)

// entryNK is an in-memory NakamaModule with the few calls the issuing paths make.
type entryNK struct {
	*banTestNakamaModule
	groups   map[string]*api.Group
	accounts map[string]*api.Account
}

func (n *entryNK) GroupsGetId(_ context.Context, ids []string) ([]*api.Group, error) {
	var out []*api.Group
	for _, id := range ids {
		if g, ok := n.groups[id]; ok {
			out = append(out, g)
		}
	}
	return out, nil
}

func (n *entryNK) AccountGetId(_ context.Context, id string) (*api.Account, error) {
	if a, ok := n.accounts[id]; ok {
		return a, nil
	}
	return nil, errors.New("account not found")
}

func (n *entryNK) StreamUserList(uint8, string, string, string, bool, bool) ([]runtime.Presence, error) {
	return nil, nil
}

func (n *entryNK) MetricsCounterAdd(string, map[string]string, int64) {}

// channelRecorder is the fake sender: it records posts in order, can fail or
// block chosen channels.
type channelRecorder struct {
	mu      sync.Mutex
	sent    []sentNotice
	failOn  map[string]bool
	blockOn map[string]chan struct{}
}

func (r *channelRecorder) send(channelID string, msg *discordgo.MessageSend) error {
	r.mu.Lock()
	block := r.blockOn[channelID]
	fail := r.failOn[channelID]
	r.mu.Unlock()
	if block != nil {
		<-block
	}
	if fail {
		return errors.New("discord says no")
	}
	r.mu.Lock()
	r.sent = append(r.sent, sentNotice{channelID, msg})
	r.mu.Unlock()
	return nil
}

func (r *channelRecorder) channels() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]string, 0, len(r.sent))
	for _, s := range r.sent {
		out = append(out, s.channelID)
	}
	return out
}

func (r *channelRecorder) to(channelID string) *sentNotice {
	r.mu.Lock()
	defer r.mu.Unlock()
	for i := range r.sent {
		if r.sent[i].channelID == channelID {
			return &r.sent[i]
		}
	}
	return nil
}

// entryFixture is guild A issuing, B and C inheriting (see inheritedNoticeFixture),
// with the target's profile naming them differently in A and in B.
type entryFixture struct {
	nk       *entryNK
	registry *GuildGroupRegistry
	rec      *channelRecorder
	ctx      context.Context
	srcID    string
}

func newEntryFixture(t *testing.T, bFilter SuspensionInheritanceFilter) *entryFixture {
	t.Helper()
	registry, srcGroup := inheritedNoticeFixture(bFilter)
	// LogAuditMessage posts through the Discord session directly, which these
	// tests do not have; A has no audit channel, so its audit line posts nothing.
	srcGroup.AuditChannelID = ""
	nk := &entryNK{
		banTestNakamaModule: newBanTestNakamaModule(),
		groups:              map[string]*api.Group{},
		accounts:            map[string]*api.Account{},
	}
	for id, gg := range registry.GuildGroups() {
		md, err := json.Marshal(gg.GroupMetadata)
		if err != nil {
			t.Fatal(err)
		}
		nk.groups[id] = &api.Group{Id: id, Name: gg.Group.Name, LangTag: GuildGroupLangTag, Metadata: string(md)}
	}
	nk.accounts[entryOperatorID] = &api.Account{User: &api.User{Id: entryOperatorID, Username: "operator"}, CustomId: "operator-discord"}
	nk.accounts[entryTargetID] = &api.Account{User: &api.User{Id: entryTargetID, Username: "targetuser"}, CustomId: "target-discord"}

	// GuildGroupLoad reads guild state under the Discord bot's user id.
	prev := ServiceSettings()
	ServiceSettingsUpdate(&ServiceSettingsData{DiscordBotUserID: "00000000-0000-4000-8000-0000000000c3"})
	t.Cleanup(func() { ServiceSettingsUpdate(prev) })

	ctx := context.Background()
	profile := &EVRProfile{InGameNames: map[string]GroupInGameName{
		inSrcID:   {GroupID: inSrcID, DisplayName: "NameInA"},
		inChildID: {GroupID: inChildID, DisplayName: "NameInB"},
	}}
	if err := StorableWrite(ctx, nk, entryTargetID, profile); err != nil {
		t.Fatal(err)
	}
	return &entryFixture{nk: nk, registry: registry, rec: &channelRecorder{}, ctx: ctx, srcID: inSrcID}
}

func (f *entryFixture) bot() *DiscordAppBot {
	return &DiscordAppBot{
		ctx:                  WithUserPermissions(f.ctx, &UserPermissions{IsGlobalOperator: true}),
		nk:                   f.nk,
		logger:               NewRuntimeGoLogger(zap.NewNop()),
		guildGroupRegistry:   f.registry,
		sendChannelMessageFn: f.rec.send,
		cache: &DiscordIntegrator{
			idcache: &MapOf[string, string]{},
		},
	}
}

// rpc calls EnforcementKickRPC as the global operator with a JSON payload, and
// registers bot as the app bot the RPC reaches through globalAppBot.
func (f *entryFixture) rpc(t *testing.T, bot *DiscordAppBot, payload map[string]any) string {
	t.Helper()
	globalAppBot.Store(bot)
	t.Cleanup(func() { globalAppBot.Store(nil) })
	// These tests are about the notices, not the kick: the fake nk is not a
	// RuntimeGoNakamaModule, so stub the kick seam (the RPC refuses otherwise).
	realKick := enforceGuildSuspensionFn
	enforceGuildSuspensionFn = func(context.Context, *zap.Logger, runtime.NakamaModule, string, []string) error { return nil }
	t.Cleanup(func() { enforceGuildSuspensionFn = realKick })
	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	//nolint:staticcheck // RUNTIME_CTX_USER_ID is the runtime's own string key
	ctx := context.WithValue(WithUserPermissions(f.ctx, &UserPermissions{IsGlobalOperator: true}), runtime.RUNTIME_CTX_USER_ID, entryOperatorID)
	out, err := EnforcementKickRPC(ctx, NewRuntimeGoLogger(zap.NewNop()), nil, f.nk, string(body))
	if err != nil {
		t.Fatalf("EnforcementKickRPC: %v", err)
	}
	return out
}

func kickPayload(groupID, duration string) map[string]any {
	return map[string]any{
		"group_id":            groupID,
		"target_user_id":      entryTargetID,
		"user_notice":         "cheating",
		"moderator_notes":     "secret moderator note",
		"suspension_duration": duration,
	}
}

func TestInheritedNoticeEntry_KickRPC_NotifiesInheritingGuild(t *testing.T) {
	f := newEntryFixture(t, SuspensionInheritanceFilter{})
	bot := f.bot()
	f.rpc(t, bot, kickPayload(inSrcID, "30d"))
	bot.noticeWG.Wait()

	toB := f.rec.to("chan-b-notice")
	if toB == nil {
		t.Fatalf("B was not notified; sent to %v", f.rec.channels())
	}
	text := toB.text()
	for _, want := range []string{"Guild A", "NameInB", "<@!target-discord>", "cheating"} {
		if !strings.Contains(text, want) {
			t.Errorf("B's notice lacks %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "NameInA") || strings.Contains(text, "secret moderator note") {
		t.Errorf("B's notice shows the source's name or notes:\n%s", text)
	}
	if f.rec.to("chan-c-audit") == nil {
		t.Errorf("C (audit channel only) was not notified; sent to %v", f.rec.channels())
	}
	if f.rec.to("chan-a-notice") != nil {
		t.Errorf("the source's own channel was posted to by the RPC")
	}
}

func TestInheritedNoticeEntry_KickRPC_FilteredOutGuildNotNotified(t *testing.T) {
	f := newEntryFixture(t, SuspensionInheritanceFilter{MinimumDurationSeconds: 60 * 24 * 3600})
	bot := f.bot()
	f.rpc(t, bot, kickPayload(inSrcID, "1h"))
	bot.noticeWG.Wait()

	if f.rec.to("chan-b-notice") != nil || f.rec.to("chan-b-audit") != nil {
		t.Fatalf("B's filter excludes this record, but B was notified: %v", f.rec.channels())
	}
	if f.rec.to("chan-c-audit") == nil {
		t.Fatalf("C has no filter and must still be notified: %v", f.rec.channels())
	}
}

// A kick that is not a suspension (no duration) creates no record to inherit.
func TestInheritedNoticeEntry_KickRPC_VoidNotifiesNobody(t *testing.T) {
	f := newEntryFixture(t, SuspensionInheritanceFilter{})
	bot := f.bot()
	f.rpc(t, bot, kickPayload(inSrcID, "0m"))
	bot.noticeWG.Wait()
	if got := f.rec.channels(); len(got) != 0 {
		t.Fatalf("a void notified %v", got)
	}
}

// A Discord that never answers must not hold the RPC response. The budget is far
// beyond the test's own wait, so an RPC that waited for B's blocked post would not
// return in time; no elapsed-time threshold is involved.
func TestInheritedNoticeEntry_KickRPC_SlowDiscordDoesNotHoldResponse(t *testing.T) {
	old := inheritedNoticeBudget
	inheritedNoticeBudget = time.Hour
	t.Cleanup(func() { inheritedNoticeBudget = old })

	f := newEntryFixture(t, SuspensionInheritanceFilter{})
	release := make(chan struct{})
	f.rec.blockOn = map[string]chan struct{}{"chan-b-notice": release}
	bot := f.bot()
	t.Cleanup(func() { close(release); bot.noticeWG.Wait() })

	returned := make(chan struct{})
	go func() {
		defer close(returned)
		f.rpc(t, bot, kickPayload(inSrcID, "30d"))
	}()
	select {
	case <-returned:
	case <-time.After(10 * time.Second):
		t.Fatal("the RPC did not return while B's post was blocked")
	}
	if f.rec.to("chan-b-notice") != nil {
		t.Fatal("B's post was recorded although it is still blocked")
	}
}

// Past the budget, the batch gives up on a post that never returns.
func TestInheritedNoticeEntry_KickRPC_BlockedPostAbandonedAtBudget(t *testing.T) {
	old := inheritedNoticeBudget
	inheritedNoticeBudget = 50 * time.Millisecond
	t.Cleanup(func() { inheritedNoticeBudget = old })

	f := newEntryFixture(t, SuspensionInheritanceFilter{})
	release := make(chan struct{})
	f.rec.blockOn = map[string]chan struct{}{"chan-b-notice": release}
	t.Cleanup(func() { close(release) })
	bot := f.bot()
	f.rpc(t, bot, kickPayload(inSrcID, "30d"))

	done := make(chan struct{})
	go func() { bot.noticeWG.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("inherited notices were not abandoned at the budget")
	}
	if f.rec.to("chan-b-notice") != nil {
		t.Fatal("B's blocked post was recorded")
	}
}

// B has no name in its own guild: its notice must not carry A's name for the player.
func TestInheritedNoticeEntry_KickRPC_NoNameInBShowsMentionOnly(t *testing.T) {
	f := newEntryFixture(t, SuspensionInheritanceFilter{})
	onlyA := &EVRProfile{InGameNames: map[string]GroupInGameName{inSrcID: {GroupID: inSrcID, DisplayName: "NameInA"}}}
	if err := StorableWrite(f.ctx, f.nk, entryTargetID, onlyA); err != nil {
		t.Fatal(err)
	}
	bot := f.bot()
	f.rpc(t, bot, kickPayload(inSrcID, "30d"))
	bot.noticeWG.Wait()

	toB := f.rec.to("chan-b-notice")
	if toB == nil {
		t.Fatalf("B was not notified; sent %v", f.rec.channels())
	}
	if text := toB.text(); strings.Contains(text, "NameInA") || !strings.Contains(text, "<@!target-discord>") {
		t.Errorf("B's notice should hold the mention and no name from A:\n%s", text)
	}
}

// With no app bot the notices cannot be sent; that is logged at warn, not dropped silently.
func TestInheritedNoticeEntry_KickRPC_NoAppBotLogsWarning(t *testing.T) {
	f := newEntryFixture(t, SuspensionInheritanceFilter{})
	globalAppBot.Store(nil)
	realKick := enforceGuildSuspensionFn
	enforceGuildSuspensionFn = func(context.Context, *zap.Logger, runtime.NakamaModule, string, []string) error { return nil }
	t.Cleanup(func() { enforceGuildSuspensionFn = realKick })
	core, logs := observer.New(zapcore.DebugLevel)
	body, err := json.Marshal(kickPayload(inSrcID, "30d"))
	if err != nil {
		t.Fatal(err)
	}
	//nolint:staticcheck // RUNTIME_CTX_USER_ID is the runtime's own string key
	ctx := context.WithValue(WithUserPermissions(f.ctx, &UserPermissions{IsGlobalOperator: true}), runtime.RUNTIME_CTX_USER_ID, entryOperatorID)
	if _, err := EnforcementKickRPC(ctx, NewRuntimeGoLogger(zap.New(core)), nil, f.nk, string(body)); err != nil {
		t.Fatalf("EnforcementKickRPC: %v", err)
	}
	warns := logs.FilterLevelExact(zapcore.WarnLevel).FilterMessageSnippet("Inherited enforcement notices skipped").Len()
	if warns != 1 {
		t.Fatalf("want one warn about the skipped notices, got %d: %v", warns, logs.All())
	}
}

// discordRoundTripper answers every Discord REST call with a message and records
// the channel of each post.
type discordRoundTripper struct {
	mu       sync.Mutex
	channels []string
	// failChannels are channels whose post is refused (403), as Discord does for a
	// channel the bot cannot see; the refused post is still recorded as attempted.
	failChannels map[string]bool
}

func (rt *discordRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	body := `{"id":"1"}`
	status := http.StatusOK
	if req.Method == http.MethodPost && strings.HasSuffix(req.URL.Path, "/messages") {
		parts := strings.Split(req.URL.Path, "/")
		channelID := parts[len(parts)-2]
		rt.mu.Lock()
		failed := rt.failChannels[channelID]
		if !failed {
			rt.channels = append(rt.channels, channelID)
		}
		rt.mu.Unlock()
		if failed {
			status = http.StatusForbidden
			body = `{"code":50001,"message":"Missing Access"}`
		} else {
			// Discord returns the channel the message went to.
			body = fmt.Sprintf(`{"id":"1","channel_id":%q}`, channelID)
		}
	}
	return &http.Response{
		StatusCode: status,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(body)),
		Request:    req,
	}, nil
}

func (rt *discordRoundTripper) posted() []string {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	return append([]string(nil), rt.channels...)
}

// The RPC's own audit line goes to A's audit channel. B, configured with that
// channel, is not told there a second time; with no Discord session the audit line
// is not posted, so B's notice goes out.
func TestInheritedNoticeEntry_KickRPC_AuditChannelPostedOnce(t *testing.T) {
	for _, tc := range []struct {
		name          string
		withSession   bool
		wantInheritor bool
		wantAudit     bool
	}{
		{"audit line posted: B skipped there", true, false, true},
		{"no session: B told", false, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newEntryFixture(t, SuspensionInheritanceFilter{})
			a := f.registry.GuildGroups()[inSrcID]
			a.AuditChannelID = "chan-a-audit"
			b := f.registry.GuildGroups()[inChildID]
			b.EnforcementNoticeChannelID, b.AuditChannelID = "", "chan-a-audit"
			md, err := json.Marshal(a.GroupMetadata)
			if err != nil {
				t.Fatal(err)
			}
			f.nk.groups[inSrcID].Metadata = string(md)

			bot := f.bot()
			rt := &discordRoundTripper{}
			if tc.withSession {
				dg, err := discordgo.New("Bot test")
				if err != nil {
					t.Fatal(err)
				}
				dg.Client = &http.Client{Transport: rt}
				bot.dg = dg
			}
			f.rpc(t, bot, kickPayload(inSrcID, "30d"))
			bot.noticeWG.Wait()

			if got := f.rec.to("chan-a-audit") != nil; got != tc.wantInheritor {
				t.Errorf("B's notice in A's audit channel = %v, want %v (sent %v)", got, tc.wantInheritor, f.rec.channels())
			}
			if got := slices.Contains(rt.posted(), "chan-a-audit"); got != tc.wantAudit {
				t.Errorf("RPC audit line in A's audit channel = %v, want %v (posted %v)", got, tc.wantAudit, rt.posted())
			}
		})
	}
}

// The kick handler: the source's own notice goes first, then B's; and when the
// source's notice fails, B is still told even though B shares that channel.
func TestInheritedNoticeEntry_KickHandler_NotifiesAfterSourceNotice(t *testing.T) {
	f := newEntryFixture(t, SuspensionInheritanceFilter{})
	bot := f.bot()
	f.kickAsOperator(t, bot)
	bot.noticeWG.Wait()

	got := f.rec.channels()
	if len(got) < 3 || got[0] != "chan-a-notice" {
		t.Fatalf("want the source's notice first, then the inheritors; got %v", got)
	}
	toB := f.rec.to("chan-b-notice")
	if toB == nil || !strings.Contains(toB.text(), "NameInB") {
		t.Fatalf("B was not notified with its own name for the player; sent %v", got)
	}
	if f.rec.to("chan-c-audit") == nil {
		t.Fatalf("C was not notified; sent %v", got)
	}
}

func TestInheritedNoticeEntry_KickHandler_FilteredOutGuildNotNotified(t *testing.T) {
	f := newEntryFixture(t, SuspensionInheritanceFilter{MinimumDurationSeconds: 60 * 24 * 3600})
	bot := f.bot()
	f.kickAsOperator(t, bot)
	bot.noticeWG.Wait()

	if f.rec.to("chan-b-notice") != nil || f.rec.to("chan-b-audit") != nil {
		t.Fatalf("B's filter excludes this record, but B was notified: %v", f.rec.channels())
	}
	if f.rec.to("chan-a-notice") == nil || f.rec.to("chan-c-audit") == nil {
		t.Fatalf("the source and C must still be notified: %v", f.rec.channels())
	}
}

// B is configured with the channel A posts to; A's post fails, so nothing was
// posted there and B must still be told.
func TestInheritedNoticeEntry_KickHandler_FailedSourcePostDoesNotSuppressInheritor(t *testing.T) {
	f := newEntryFixture(t, SuspensionInheritanceFilter{})
	f.registry.GuildGroups()[inChildID].EnforcementNoticeChannelID = "chan-a-notice"
	var posts int
	var mu sync.Mutex
	bot := f.bot()
	bot.sendChannelMessageFn = func(ch string, msg *discordgo.MessageSend) error {
		mu.Lock()
		defer mu.Unlock()
		if ch == "chan-a-notice" {
			posts++
			if posts == 1 {
				return errors.New("missing access") // the source's own notice
			}
		}
		return f.rec.send(ch, msg)
	}
	f.kickAsOperator(t, bot)
	bot.noticeWG.Wait()

	if f.rec.to("chan-a-notice") == nil {
		t.Fatalf("B, sharing A's channel, was not notified after A's own post failed: %v", f.rec.channels())
	}
}

// and when A's post succeeded in that shared channel, B is not told twice there.
func TestInheritedNoticeEntry_KickHandler_SharedChannelPostedOnce(t *testing.T) {
	f := newEntryFixture(t, SuspensionInheritanceFilter{})
	f.registry.GuildGroups()[inChildID].EnforcementNoticeChannelID = "chan-a-notice"
	bot := f.bot()
	f.kickAsOperator(t, bot)
	bot.noticeWG.Wait()

	var n int
	for _, ch := range f.rec.channels() {
		if ch == "chan-a-notice" {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("shared channel received %d posts, want 1 (the source's): %v", n, f.rec.channels())
	}
}

func (f *entryFixture) kickAsOperator(t *testing.T, bot *DiscordAppBot) {
	t.Helper()
	bot.cache.idcache.Store("operator-discord", entryOperatorID)
	bot.cache.idcache.Store("target-discord", entryTargetID)
	bot.cache.idcache.Store("guild-"+inSrcID, inSrcID)
	// Use the A-guild group the way the interaction supplies it.
	caller := &discordgo.Member{User: &discordgo.User{ID: "operator-discord", Username: "operator"}}
	target := &discordgo.User{ID: "target-discord", Username: "targetuser"}
	// kickPlayer reads the guild id from the interaction; with none it cannot
	// resolve the group, so give it one.
	i := &discordgo.InteractionCreate{Interaction: &discordgo.Interaction{GuildID: "guild-" + inSrcID, Type: discordgo.InteractionMessageComponent}}
	if err := bot.kickPlayer(NewRuntimeGoLogger(zap.NewNop()), i, caller, target, "30d", "cheating", "secret moderator note", false, false); err != nil {
		t.Logf("kickPlayer: %v", err) // the ephemeral reply has no Discord session to go to
	}
}

func TestInheritedNoticeEntry_GuildBan_NotifiesInheritingGuild(t *testing.T) {
	f := newEntryFixture(t, SuspensionInheritanceFilter{})
	d := &DiscordIntegrator{nk: f.nk, logger: zap.NewNop(), guildGroupRegistry: f.registry, sendChannelMessageFn: f.rec.send}

	if err := d.writeGuildBanEnforcement(f.ctx, zap.NewNop(), inSrcID, entryTargetID, "", "", "Discord guild ban"); err != nil {
		t.Fatalf("writeGuildBanEnforcement: %v", err)
	}
	d.noticeWG.Wait()

	toB := f.rec.to("chan-b-notice")
	if toB == nil {
		t.Fatalf("B was not told of the lifetime ban; sent %v", f.rec.channels())
	}
	if text := toB.text(); !strings.Contains(text, "Guild A") || !strings.Contains(text, "NameInB") {
		t.Errorf("B's notice:\n%s", text)
	}
}

// A lifetime full ban passes every inheritance filter (any minimum duration, either
// scope), so there is no filtered-out case to drive for this path; a guild with no
// channel at all is the one that is not told.
func TestInheritedNoticeEntry_GuildBan_GuildWithNoChannelIsSkipped(t *testing.T) {
	f := newEntryFixture(t, SuspensionInheritanceFilter{MinimumDurationSeconds: 3650 * 24 * 3600, Scope: InheritScopeFullBans})
	b := f.registry.GuildGroups()[inChildID]
	b.EnforcementNoticeChannelID, b.AuditChannelID = "", ""
	d := &DiscordIntegrator{nk: f.nk, logger: zap.NewNop(), guildGroupRegistry: f.registry, sendChannelMessageFn: f.rec.send}

	if err := d.writeGuildBanEnforcement(f.ctx, zap.NewNop(), inSrcID, entryTargetID, "", "", "Discord guild ban"); err != nil {
		t.Fatalf("writeGuildBanEnforcement: %v", err)
	}
	d.noticeWG.Wait()
	if got := f.rec.channels(); len(got) != 1 || got[0] != "chan-c-audit" {
		t.Fatalf("sent %v, want only C's audit channel", got)
	}
}

func TestInheritedNoticeEntry_GhostSpam_NotifiesInheritingGuild(t *testing.T) {
	f := newEntryFixture(t, SuspensionInheritanceFilter{})
	bot := f.bot()

	// EvrPipeline.applyGhostSpamSuspension delegates to this with its own fields.
	applyGhostSpamSuspension(f.ctx, zap.NewNop(), f.nk, emptySessionRegistry{}, bot, entryTargetID, entryOperatorID, "operator-discord", inSrcID, "ghost spam note")
	bot.noticeWG.Wait()

	if f.rec.to("chan-b-notice") == nil {
		t.Fatalf("B was not told of the ghost-spam suspension; sent %v", f.rec.channels())
	}
}

type emptySessionRegistry struct{ SessionRegistry }

func (emptySessionRegistry) Range(func(Session) bool) {}

// attachDiscordSession gives bot a Discord session whose REST calls go to rt, so a
// path that posts through the session (the audit log) can be observed.
func attachDiscordSession(t *testing.T, bot *DiscordAppBot, rt *discordRoundTripper) {
	t.Helper()
	dg, err := discordgo.New("Bot test")
	if err != nil {
		t.Fatal(err)
	}
	dg.Client = &http.Client{Transport: rt}
	bot.dg = dg
}

// shareAuditChannel gives A an audit channel and configures B with only that channel,
// so any B notice would land in the channel A posts its audit line to. It returns the
// channel id.
func (f *entryFixture) shareAuditChannel(t *testing.T) string {
	t.Helper()
	const shared = "chan-a-audit"
	a := f.registry.GuildGroups()[inSrcID]
	a.AuditChannelID = shared
	b := f.registry.GuildGroups()[inChildID]
	b.EnforcementNoticeChannelID, b.AuditChannelID = "", shared
	md, err := json.Marshal(a.GroupMetadata)
	if err != nil {
		t.Fatal(err)
	}
	f.nk.groups[inSrcID].Metadata = string(md)
	return shared
}

// kick-player posts its summary to A's audit channel. B, configured with that
// channel, must not be told there a second time: one post in the channel.
func TestInheritedNoticeEntry_KickHandler_AuditChannelPostedOnce(t *testing.T) {
	f := newEntryFixture(t, SuspensionInheritanceFilter{})
	shared := f.shareAuditChannel(t)
	bot := f.bot()
	rt := &discordRoundTripper{}
	attachDiscordSession(t, bot, rt)

	f.kickAsOperator(t, bot)
	bot.noticeWG.Wait()

	all := append(rt.posted(), f.rec.channels()...)
	if n := countOf(all, shared); n != 1 {
		t.Fatalf("shared audit channel received %d posts, want 1 (A's summary): session %v, notices %v", n, rt.posted(), f.rec.channels())
	}
	if f.rec.to("chan-c-audit") == nil {
		t.Fatalf("C must still be told: %v", f.rec.channels())
	}
}

// When A's audit summary is refused, nothing went to the shared channel, so B is
// told there.
func TestInheritedNoticeEntry_KickHandler_FailedAuditPostStillTellsSharingGuild(t *testing.T) {
	f := newEntryFixture(t, SuspensionInheritanceFilter{})
	shared := f.shareAuditChannel(t)
	bot := f.bot()
	rt := &discordRoundTripper{failChannels: map[string]bool{shared: true}}
	attachDiscordSession(t, bot, rt)

	f.kickAsOperator(t, bot)
	bot.noticeWG.Wait()

	if f.rec.to(shared) == nil {
		t.Fatalf("A's audit summary failed, so B must be told in the shared channel: session %v, notices %v", rt.posted(), f.rec.channels())
	}
}

// Ghost-spam's audit line goes to A's audit channel; B sharing it is told once.
func TestInheritedNoticeEntry_GhostSpam_AuditChannelPostedOnce(t *testing.T) {
	f := newEntryFixture(t, SuspensionInheritanceFilter{})
	shared := f.shareAuditChannel(t)
	bot := f.bot()
	rt := &discordRoundTripper{}
	attachDiscordSession(t, bot, rt)

	applyGhostSpamSuspension(f.ctx, zap.NewNop(), f.nk, emptySessionRegistry{}, bot, entryTargetID, entryOperatorID, "operator-discord", inSrcID, "ghost spam note")
	bot.noticeWG.Wait()

	all := append(rt.posted(), f.rec.channels()...)
	if n := countOf(all, shared); n != 1 {
		t.Fatalf("shared audit channel received %d posts, want 1 (A's audit line): session %v, notices %v", n, rt.posted(), f.rec.channels())
	}
	if f.rec.to("chan-c-audit") == nil {
		t.Fatalf("C must still be told: %v", f.rec.channels())
	}
}

// The RPC has a session but A's audit post is refused: nothing went to the shared
// channel, so B must be told there.
func TestInheritedNoticeEntry_KickRPC_FailedAuditPostStillTellsSharingGuild(t *testing.T) {
	f := newEntryFixture(t, SuspensionInheritanceFilter{})
	shared := f.shareAuditChannel(t)
	bot := f.bot()
	rt := &discordRoundTripper{failChannels: map[string]bool{shared: true}}
	attachDiscordSession(t, bot, rt)

	f.rpc(t, bot, kickPayload(inSrcID, "30d"))
	bot.noticeWG.Wait()

	if f.rec.to(shared) == nil {
		t.Fatalf("A's audit post failed, so B must be told in the shared channel: session %v, notices %v", rt.posted(), f.rec.channels())
	}
}

func countOf(channels []string, id string) int {
	n := 0
	for _, ch := range channels {
		if ch == id {
			n++
		}
	}
	return n
}
