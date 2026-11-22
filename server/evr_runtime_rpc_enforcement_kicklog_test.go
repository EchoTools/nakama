package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/bwmarrin/discordgo"
	"github.com/gofrs/uuid/v5"
	"github.com/heroiclabs/nakama-common/api"
	"github.com/heroiclabs/nakama-common/runtime"
	"go.uber.org/atomic"
	"go.uber.org/zap"
)

const kickLogChannelID = "suspension-log-chan"

// kickLogNakamaModule is the module EnforcementKickRPC runs against in these tests.
// Its guild carries an enforcement notice channel, and the two account lookups the
// suspension-log card depends on can be made to fail.
type kickLogNakamaModule struct {
	*mockEnforcementNakamaModule
	accounts   map[string]*api.Account
	accountErr map[string]error
}

func (m *kickLogNakamaModule) GroupsGetId(_ context.Context, groupIDs []string) ([]*api.Group, error) {
	groups := make([]*api.Group, 0, len(groupIDs))
	for _, id := range groupIDs {
		groups = append(groups, &api.Group{
			Id:       id,
			Name:     "Test Guild",
			LangTag:  "guild",
			Metadata: fmt.Sprintf(`{"guild_id":"guild-discord-id","enforcement_notice_channel_id":%q}`, kickLogChannelID),
		})
	}
	return groups, nil
}

func (m *kickLogNakamaModule) AccountGetId(_ context.Context, userID string) (*api.Account, error) {
	if err := m.accountErr[userID]; err != nil {
		return nil, err
	}
	if a := m.accounts[userID]; a != nil {
		return a, nil
	}
	return nil, errors.New("no such account")
}

func (m *kickLogNakamaModule) StreamUserList(uint8, string, string, string, bool, bool) ([]runtime.Presence, error) {
	return nil, nil
}

func (m *kickLogNakamaModule) MetricsCounterAdd(string, map[string]string, int64) {}

type kickLogFixture struct {
	nk         *kickLogNakamaModule
	ctx        context.Context
	groupID    string
	enforcerID string
	targetID   string
	realKick   func(context.Context, *zap.Logger, runtime.NakamaModule, string, []string) error
}

func newKickLogFixture(t *testing.T) *kickLogFixture {
	t.Helper()
	ServiceSettingsUpdate(&ServiceSettingsData{DiscordBotUserID: "discord-bot-user-id"})

	f := &kickLogFixture{
		groupID:    uuid.Must(uuid.NewV4()).String(),
		enforcerID: uuid.Must(uuid.NewV4()).String(),
		targetID:   uuid.Must(uuid.NewV4()).String(),
	}
	// The default kick needs the runtime module's session registry; the fixture's module is
	// a mock, so the kick is stood in with a no-op. Tests of the kick restore f.realKick.
	f.realKick = enforceGuildSuspensionFn
	enforceGuildSuspensionFn = func(context.Context, *zap.Logger, runtime.NakamaModule, string, []string) error { return nil }
	t.Cleanup(func() { enforceGuildSuspensionFn = f.realKick })
	f.nk = &kickLogNakamaModule{
		mockEnforcementNakamaModule: newMockEnforcementNakamaModule(),
		accounts: map[string]*api.Account{
			f.enforcerID: {User: &api.User{Id: f.enforcerID, Username: "mod_user", DisplayName: "Mod"}, CustomId: "111"},
			f.targetID:   {User: &api.User{Id: f.targetID, Username: "player_user"}, CustomId: "222"},
		},
		accountErr: map[string]error{},
	}

	profile := &EVRProfile{}
	profile.SetGroupIGNData(f.groupID, GroupInGameName{GroupID: f.groupID, DisplayName: "PlayerIGN"})
	profileJSON, err := json.Marshal(profile)
	if err != nil {
		t.Fatalf("marshal profile: %v", err)
	}
	meta := profile.StorageMeta()
	f.nk.storageObjects[f.targetID+":"+meta.Collection+":"+meta.Key] = []*api.StorageObject{{
		Collection: meta.Collection, Key: meta.Key, UserId: f.targetID, Value: string(profileJSON),
	}}

	//nolint:staticcheck // RUNTIME_CTX_USER_ID is the plain-string key the RPC reads the caller from.
	ctx := context.WithValue(context.Background(), runtime.RUNTIME_CTX_USER_ID, f.enforcerID)
	f.ctx = WithUserPermissions(ctx, &UserPermissions{IsGlobalOperator: true})
	return f
}

func (f *kickLogFixture) payload(extra string) string {
	return fmt.Sprintf(`{"group_id":%q,"target_user_id":%q,"user_notice":"be nice","suspension_duration":"1h"%s}`, f.groupID, f.targetID, extra)
}

// useSuspensionLogSender points the RPC at sender and restores the real resolver after the test.
func useSuspensionLogSender(t *testing.T, sender enforcementNoticeSender) {
	t.Helper()
	orig := resolveSuspensionLogTarget
	resolveSuspensionLogTarget = func() *suspensionLogTarget {
		return &suspensionLogTarget{sender: sender}
	}
	t.Cleanup(func() { resolveSuspensionLogTarget = orig })
}

func runKickRPC(t *testing.T, f *kickLogFixture, payload string) EnforcementKickResponse {
	t.Helper()
	out, err := EnforcementKickRPC(f.ctx, NewRuntimeGoLogger(zap.NewNop()), nil, f.nk, payload)
	if err != nil {
		t.Fatalf("EnforcementKickRPC(%s): %v", payload, err)
	}
	var resp EnforcementKickResponse
	if err := json.Unmarshal([]byte(out), &resp); err != nil {
		t.Fatalf("unmarshal response %q: %v", out, err)
	}
	return resp
}

func TestEnforcementKickRPC_PostsToSuspensionLog(t *testing.T) {
	t.Run("an omitted option posts to the guild's channel", func(t *testing.T) {
		f := newKickLogFixture(t)
		s := &fakeNoticeSender{}
		useSuspensionLogSender(t, s)

		resp := runKickRPC(t, f, f.payload(""))

		if len(s.channels) != 1 || s.channels[0] != kickLogChannelID {
			t.Fatalf("expected one post to %s, got %v", kickLogChannelID, s.channels)
		}
		if !resp.PostedToSuspensionLog {
			t.Fatal("response should report posted_to_suspension_log=true")
		}
		embed := s.messages[0].Embed
		if !strings.HasPrefix(embed.Title, "Suspension:") || !strings.Contains(embed.Title, "PlayerIGN") {
			t.Fatalf("title should name the target by IGN, got %q", embed.Title)
		}
		if !strings.Contains(embed.Description, "<@!222>") {
			t.Fatalf("description should mention the target's Discord ID, got %q", embed.Description)
		}
		if strings.Contains(embed.Title+embed.Description, f.targetID) {
			t.Fatalf("the card must not carry the raw user ID: %q / %q", embed.Title, embed.Description)
		}
		if len(s.messages[0].Components) != 1 {
			t.Fatalf("a suspension card carries Edit/Void buttons, got %d rows", len(s.messages[0].Components))
		}
	})

	t.Run("post_to_suspension_log false posts nothing", func(t *testing.T) {
		f := newKickLogFixture(t)
		s := &fakeNoticeSender{}
		useSuspensionLogSender(t, s)

		resp := runKickRPC(t, f, f.payload(`,"post_to_suspension_log":false`))

		if len(s.channels) != 0 {
			t.Fatalf("opt-out must post nothing, got %v", s.channels)
		}
		if resp.PostedToSuspensionLog {
			t.Fatal("response should report posted_to_suspension_log=false")
		}
		if !resp.Success {
			t.Fatal("the kick itself must still succeed")
		}
	})

	t.Run("a send error reports not posted and does not fail the kick", func(t *testing.T) {
		f := newKickLogFixture(t)
		useSuspensionLogSender(t, &fakeNoticeSender{err: errors.New("discord down")})

		resp := runKickRPC(t, f, f.payload(""))

		if !resp.Success || resp.PostedToSuspensionLog {
			t.Fatalf("want success=true posted=false, got %+v", resp)
		}
	})

	t.Run("a void with no active suspension posts nothing", func(t *testing.T) {
		f := newKickLogFixture(t)
		s := &fakeNoticeSender{}
		useSuspensionLogSender(t, s)

		payload := fmt.Sprintf(`{"group_id":%q,"target_user_id":%q,"user_notice":"undo","suspension_duration":"0m"}`, f.groupID, f.targetID)
		resp := runKickRPC(t, f, payload)

		if len(s.channels) != 0 || resp.PostedToSuspensionLog {
			t.Fatalf("nothing was voided, nothing should post; posts=%v resp=%+v", s.channels, resp)
		}
	})
}

func TestEnforcementKickRPC_FailedLookupPostsNothing(t *testing.T) {
	tests := []struct {
		name   string
		break_ func(f *kickLogFixture)
	}{
		{"enforcer account lookup fails", func(f *kickLogFixture) { f.nk.accountErr[f.enforcerID] = errors.New("db down") }},
		{"target account lookup fails", func(f *kickLogFixture) { f.nk.accountErr[f.targetID] = errors.New("db down") }},
		{"target has no Discord ID", func(f *kickLogFixture) { f.nk.accounts[f.targetID].CustomId = "" }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newKickLogFixture(t)
			s := &fakeNoticeSender{}
			useSuspensionLogSender(t, s)
			tt.break_(f)

			resp := runKickRPC(t, f, f.payload(""))

			if len(s.channels) != 0 {
				t.Fatalf("a failed lookup must not post a card, got %d: %+v", len(s.channels), s.messages[0].Embed)
			}
			if !resp.Success || resp.PostedToSuspensionLog {
				t.Fatalf("want success=true posted=false, got %+v", resp)
			}
		})
	}
}

func TestEnforcementKickRPC_NoDiscordBotDoesNotCrash(t *testing.T) {
	prev := globalAppBot.Load()
	t.Cleanup(func() { globalAppBot.Store(prev) })

	for name, bot := range map[string]*DiscordAppBot{
		"nil appBot": nil,
		"nil dg":     {},
	} {
		t.Run(name, func(t *testing.T) {
			globalAppBot.Store(bot)
			f := newKickLogFixture(t)

			resp := runKickRPC(t, f, f.payload(""))

			if !resp.Success || resp.PostedToSuspensionLog {
				t.Fatalf("want success=true posted=false, got %+v", resp)
			}
		})
	}
}

// stuckNoticeSender ignores its context and blocks until the test releases it, as a call
// stuck below the HTTP layer would. Only the RPC's own wait can bound the request.
type stuckNoticeSender struct {
	called  chan struct{}
	release chan struct{}
}

func (b *stuckNoticeSender) ChannelMessageSendComplex(string, *discordgo.MessageSend, ...discordgo.RequestOption) (*discordgo.Message, error) {
	close(b.called)
	<-b.release
	return &discordgo.Message{}, nil
}

func TestEnforcementKickRPC_SlowDiscordIsBounded(t *testing.T) {
	orig := suspensionLogTimeout
	suspensionLogTimeout = 100 * time.Millisecond
	t.Cleanup(func() { suspensionLogTimeout = orig })

	f := newKickLogFixture(t)
	s := &stuckNoticeSender{called: make(chan struct{}), release: make(chan struct{})}
	released := false
	release := func() {
		if !released {
			released = true
			close(s.release)
		}
	}
	t.Cleanup(release)
	useSuspensionLogSender(t, s)

	type result struct {
		resp EnforcementKickResponse
		err  error
	}
	returned := make(chan result, 1)
	go func() {
		out, err := EnforcementKickRPC(f.ctx, NewRuntimeGoLogger(zap.NewNop()), nil, f.nk, f.payload(""))
		var r result
		r.err = err
		if err == nil {
			err = json.Unmarshal([]byte(out), &r.resp)
			r.err = err
		}
		returned <- r
	}()

	// The sender is still blocked (it is released only below), so the RPC returning proves
	// it stopped waiting at the timeout.
	select {
	case r := <-returned:
		if r.err != nil {
			t.Fatalf("EnforcementKickRPC: %v", r.err)
		}
		if !r.resp.Success || r.resp.PostedToSuspensionLog {
			t.Fatalf("want success=true posted=false, got %+v", r.resp)
		}
	case <-time.After(5 * time.Second):
		release()
		t.Fatal("the RPC was held by a sender that ignores its context")
	}
	select {
	case <-s.called:
	default:
		t.Fatal("the sender was never called")
	}
	release()
}

func TestEnforcementKickRPC_EnforcerWithoutDiscordIDPostsByName(t *testing.T) {
	f := newKickLogFixture(t)
	s := &fakeNoticeSender{}
	useSuspensionLogSender(t, s)
	f.nk.accounts[f.enforcerID] = &api.Account{User: &api.User{Id: f.enforcerID, Username: "svc_bot"}}

	resp := runKickRPC(t, f, f.payload(""))

	if !resp.PostedToSuspensionLog || len(s.messages) != 1 {
		t.Fatalf("a service account's kick must still be logged, got posts=%d resp=%+v", len(s.messages), resp)
	}
	embed := s.messages[0].Embed
	text := embed.Author.Name + embed.Title + embed.Description
	for _, f := range embed.Fields {
		text += f.Name + f.Value
	}
	if !strings.Contains(embed.Author.Name, "svc_bot") {
		t.Fatalf("the card should name the enforcer by account name, got %q", embed.Author.Name)
	}
	if strings.Contains(text, "<@!>") {
		t.Fatalf("the card must not carry an empty mention: %q", text)
	}
}

func TestEnforcementKickRPC_EnforcerWithNoNamePostsNothing(t *testing.T) {
	f := newKickLogFixture(t)
	s := &fakeNoticeSender{}
	useSuspensionLogSender(t, s)
	f.nk.accounts[f.enforcerID] = &api.Account{User: &api.User{Id: f.enforcerID}}

	resp := runKickRPC(t, f, f.payload(""))

	if len(s.messages) != 0 || !resp.Success || resp.PostedToSuspensionLog {
		t.Fatalf("an unnamed enforcer must not post; posts=%d resp=%+v", len(s.messages), resp)
	}
}

// With the real resolver and a registry that knows the group, the card names the group,
// not its ID. The resolver's sender is swapped for a fake after the real resolver ran.
func TestEnforcementKickRPC_RealResolverNamesTheGroup(t *testing.T) {
	prev := globalAppBot.Load()
	t.Cleanup(func() { globalAppBot.Store(prev) })

	f := newKickLogFixture(t)
	gg, err := NewGuildGroup(&api.Group{Id: f.groupID, Name: "Echo Guild", LangTag: "guild", Metadata: `{"guild_id":"guild-discord-id"}`}, nil)
	if err != nil {
		t.Fatalf("NewGuildGroup: %v", err)
	}
	registry := &GuildGroupRegistry{
		guildGroups:    atomic.NewPointer(&map[string]*GuildGroup{}),
		inheritanceMap: atomic.NewPointer(&map[string][]string{}),
	}
	registry.Add(gg)
	dg := &discordgo.Session{}
	globalAppBot.Store(&DiscordAppBot{dg: dg, guildGroupRegistry: registry})

	target := resolveSuspensionLogTarget()
	if target == nil || target.sender != enforcementNoticeSender(dg) {
		t.Fatalf("the real resolver should target the bot's session, got %+v", target)
	}
	if got := target.groupName(f.groupID); got != "Echo Guild" {
		t.Fatalf("groupName(%s) = %q, want the registry's name", f.groupID, got)
	}
	if got := target.groupName("unknown"); got != "" {
		t.Fatalf("an unknown group must have no name, got %q", got)
	}

	s := &fakeNoticeSender{}
	real := resolveSuspensionLogTarget
	resolveSuspensionLogTarget = func() *suspensionLogTarget {
		tg := real()
		tg.sender = s
		return tg
	}
	t.Cleanup(func() { resolveSuspensionLogTarget = real })

	resp := runKickRPC(t, f, f.payload(""))

	if !resp.PostedToSuspensionLog || len(s.messages) != 1 {
		t.Fatalf("want one post, got %d resp=%+v", len(s.messages), resp)
	}
	var names string
	for _, fld := range s.messages[0].Embed.Fields {
		names += fld.Name + fld.Value
	}
	if !strings.Contains(names, "Echo Guild") || strings.Contains(names, f.groupID) {
		t.Fatalf("the card should carry the group name, not its ID: %q", names)
	}
}

// The kick itself is the seam: a module that is not the runtime module cannot reach the
// session registry, so the RPC refuses rather than report a kick that did not happen.
func TestEnforcementKickRPC_RefusesWhenTheKickCannotRun(t *testing.T) {
	f := newKickLogFixture(t)
	s := &fakeNoticeSender{}
	useSuspensionLogSender(t, s)
	enforceGuildSuspensionFn = f.realKick

	out, err := EnforcementKickRPC(f.ctx, NewRuntimeGoLogger(zap.NewNop()), nil, f.nk, f.payload(""))
	if err == nil {
		t.Fatalf("a kick that could not run must be refused, got success: %s", out)
	}
	if len(s.messages) != 0 {
		t.Fatalf("a refused kick must not be posted, got %d", len(s.messages))
	}
}

func TestEnforcementKickRPC_KicksTheTargetFromTheGuildAndItsInheritors(t *testing.T) {
	f := newKickLogFixture(t)
	useSuspensionLogSender(t, &fakeNoticeSender{})
	var gotUser string
	var gotGuilds []string
	orig := enforceGuildSuspensionFn
	enforceGuildSuspensionFn = func(_ context.Context, _ *zap.Logger, _ runtime.NakamaModule, userID string, guildIDs []string) error {
		gotUser, gotGuilds = userID, guildIDs
		return nil
	}
	t.Cleanup(func() { enforceGuildSuspensionFn = orig })

	resp := runKickRPC(t, f, f.payload(""))

	if gotUser != f.targetID || len(gotGuilds) == 0 || gotGuilds[0] != f.groupID {
		t.Fatalf("kick called with user=%q guilds=%v", gotUser, gotGuilds)
	}
	if !resp.Success || !strings.Contains(strings.Join(resp.Actions, "|"), "kicked from current match") {
		t.Fatalf("want the kick reported, got %+v", resp)
	}
}
