package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/bwmarrin/discordgo"
	"github.com/gofrs/uuid/v5"
	"github.com/heroiclabs/nakama-common/api"
	"github.com/heroiclabs/nakama-common/runtime"
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
}

func newKickLogFixture(t *testing.T) *kickLogFixture {
	t.Helper()
	ServiceSettingsUpdate(&ServiceSettingsData{DiscordBotUserID: "discord-bot-user-id"})

	f := &kickLogFixture{
		groupID:    uuid.Must(uuid.NewV4()).String(),
		enforcerID: uuid.Must(uuid.NewV4()).String(),
		targetID:   uuid.Must(uuid.NewV4()).String(),
	}
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
		{"enforcer has no Discord ID", func(f *kickLogFixture) { f.nk.accounts[f.enforcerID].CustomId = "" }},
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

// blockingNoticeSender never answers until its context ends, as a stalled Discord would.
type blockingNoticeSender struct{ called chan struct{} }

func (b *blockingNoticeSender) ChannelMessageSendComplex(_ string, _ *discordgo.MessageSend, opts ...discordgo.RequestOption) (*discordgo.Message, error) {
	close(b.called)
	// Honour the request context the way discordgo does: apply the options to a request.
	httpReq, err := http.NewRequest(http.MethodPost, "http://discord.invalid/", nil)
	if err != nil {
		return nil, err
	}
	cfg := &discordgo.RequestConfig{Request: httpReq}
	for _, o := range opts {
		o(cfg)
	}
	<-cfg.Request.Context().Done()
	return nil, cfg.Request.Context().Err()
}

func TestEnforcementKickRPC_SlowDiscordIsBounded(t *testing.T) {
	orig := suspensionLogTimeout
	suspensionLogTimeout = 100 * time.Millisecond
	t.Cleanup(func() { suspensionLogTimeout = orig })

	f := newKickLogFixture(t)
	s := &blockingNoticeSender{called: make(chan struct{})}
	useSuspensionLogSender(t, s)

	start := time.Now()
	resp := runKickRPC(t, f, f.payload(""))
	elapsed := time.Since(start)

	select {
	case <-s.called:
	default:
		t.Fatal("the sender was never called")
	}
	if elapsed > 5*time.Second {
		t.Fatalf("the RPC was held %v by a stalled Discord", elapsed)
	}
	if !resp.Success || resp.PostedToSuspensionLog {
		t.Fatalf("want success=true posted=false, got %+v", resp)
	}
}
