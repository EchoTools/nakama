package server

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/heroiclabs/nakama-common/api"
	"github.com/heroiclabs/nakama-common/runtime"
)

func TestGuildRulesLine(t *testing.T) {
	cases := []struct {
		name, desc, rules, want string
	}{
		{"set", "Desc", "No cheating", "Desc\nNo cheating"},
		{"unset omits the rules line", "Desc", "", "Desc"},
		{"legacy placeholder is not shown", "Desc", legacyRulesPlaceholder, "Desc"},
		{"whitespace only is unset", "Desc", " \n\t", "Desc"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := guildRulesLine(c.desc, c.rules); got != c.want {
				t.Fatalf("guildRulesLine(%q,%q) = %q, want %q", c.desc, c.rules, got, c.want)
			}
		})
	}
}

func TestMigrateRulesText(t *testing.T) {
	// A guild that had a real #rules topic keeps it.
	s := &GuildGroupState{RulesText: "Be kind."}
	migrateRulesText(s)
	if s.RulesText != "Be kind." {
		t.Fatalf("real rules must survive migration, got %q", s.RulesText)
	}
	// A guild that only ever had the placeholder is cleared.
	s = &GuildGroupState{RulesText: legacyRulesPlaceholder}
	migrateRulesText(s)
	if s.RulesText != "" {
		t.Fatalf("placeholder must be cleared, got %q", s.RulesText)
	}
}

func TestApplyRulesTextUpdate(t *testing.T) {
	gg := &GuildGroup{State: &GuildGroupState{RulesText: "old"}}

	// Absent field: untouched.
	if changed, err := applyRulesTextUpdate(gg, &GuildGroupUpdateRequest{GroupID: "g"}); err != nil || changed || gg.State.RulesText != "old" {
		t.Fatalf("absent rules_text: changed=%v err=%v text=%q", changed, err, gg.State.RulesText)
	}

	// Set.
	txt := "1. Be kind"
	if changed, err := applyRulesTextUpdate(gg, &GuildGroupUpdateRequest{RulesText: &txt}); err != nil || !changed || gg.State.RulesText != txt {
		t.Fatalf("set: changed=%v err=%v text=%q", changed, err, gg.State.RulesText)
	}

	// Explicit empty string clears.
	empty := ""
	if changed, err := applyRulesTextUpdate(gg, &GuildGroupUpdateRequest{RulesText: &empty}); err != nil || !changed || gg.State.RulesText != "" {
		t.Fatalf("clear: changed=%v err=%v text=%q", changed, err, gg.State.RulesText)
	}

	// Oversized is rejected and leaves state alone.
	big := strings.Repeat("x", maxGuildRulesTextLen+1)
	gg.State.RulesText = "keep"
	if _, err := applyRulesTextUpdate(gg, &GuildGroupUpdateRequest{RulesText: &big}); err == nil || gg.State.RulesText != "keep" {
		t.Fatalf("oversized must error without mutating: err=%v text=%q", err, gg.State.RulesText)
	}
}

// rulesRPCNakama serves one guild group to GuildGroupUpdateRPC on top of the
// shared storage double. GroupsGetId and UserGroupsList are defined on this leaf
// type; the RPC calls them through runtime.NakamaModule, which dispatches here.
type rulesRPCNakama struct {
	*occTestNakamaModule
	group *api.Group
}

func (n *rulesRPCNakama) GroupsGetId(ctx context.Context, ids []string) ([]*api.Group, error) {
	return []*api.Group{n.group}, nil
}

func (n *rulesRPCNakama) UserGroupsList(ctx context.Context, userID string, limit int, state *int, cursor string) ([]*api.UserGroupList_UserGroup, string, error) {
	return nil, "", nil
}

func newRulesRPCFixture(t *testing.T, stored string) (*rulesRPCNakama, string) {
	t.Helper()
	const groupID = "11111111-1111-1111-1111-111111111111"
	// StorableRead refuses an empty owner id, so the bot user must be set.
	prev := serviceSettings.Load()
	ServiceSettingsUpdate(&ServiceSettingsData{DiscordBotUserID: "bot-uid"})
	t.Cleanup(func() { serviceSettings.Store(prev) })
	nk := &rulesRPCNakama{
		occTestNakamaModule: newOCCTestNakamaModule(),
		group: &api.Group{
			Id:       groupID,
			Name:     "g",
			LangTag:  GuildGroupLangTag,
			Metadata: `{"guild_id":"222","owner_id":"owner-uid"}`,
		},
	}
	state, _ := json.Marshal(map[string]any{"group_id": groupID, "rules_text": stored})
	nk.seedObject(ServiceSettings().DiscordBotUserID, StorageCollectionState, groupID, string(state))
	return nk, groupID
}

func storedRules(t *testing.T, nk *rulesRPCNakama, groupID string) string {
	t.Helper()
	objs, err := nk.StorageRead(context.Background(), []*runtime.StorageRead{{Collection: StorageCollectionState, Key: groupID, UserID: ServiceSettings().DiscordBotUserID}})
	if err != nil || len(objs) != 1 {
		t.Fatalf("read state: %v (%d objects)", err, len(objs))
	}
	var s struct {
		RulesText string `json:"rules_text"`
	}
	if err := json.Unmarshal([]byte(objs[0].Value), &s); err != nil {
		t.Fatal(err)
	}
	return s.RulesText
}

func TestGuildGroupUpdateRPC_RulesText(t *testing.T) {
	// Nakama's own RUNTIME_CTX_USER_ID is an untyped string key; the RPC reads it that way.
	ownerCtx := context.WithValue(context.Background(), runtime.RUNTIME_CTX_USER_ID, "owner-uid")       //nolint:staticcheck // SA1029: key type is dictated by the runtime API
	strangerCtx := context.WithValue(context.Background(), runtime.RUNTIME_CTX_USER_ID, "stranger-uid") //nolint:staticcheck // SA1029: as above

	t.Run("owner sets rules: stored, returned, logged", func(t *testing.T) {
		nk, gid := newRulesRPCFixture(t, "old rules")
		logger := newCaptureLogger()
		payload := `{"group_id":"` + gid + `","rules_text":"1. Be kind"}`
		out, err := GuildGroupUpdateRPC(ownerCtx, logger, nil, nk, payload)
		if err != nil {
			t.Fatalf("rpc: %v", err)
		}
		if got := storedRules(t, nk, gid); got != "1. Be kind" {
			t.Fatalf("stored rules = %q", got)
		}
		if !strings.Contains(out, `"rules_text":"1. Be kind"`) {
			t.Fatalf("response lacks new rules: %s", out)
		}
		var found bool
		for _, e := range *logger.events {
			if e.level == "info" && e.msg == "Guild rules text updated" &&
				e.fields["user_id"] == "owner-uid" && e.fields["group_id"] == gid &&
				e.fields["rules_text"] == "1. Be kind" && e.fields["op"] == "guild/group/update" {
				found = true
			}
		}
		if !found {
			t.Fatalf("no audit record; events: %+v", *logger.events)
		}
	})

	t.Run("omitted rules_text leaves stored rules and logs nothing", func(t *testing.T) {
		nk, gid := newRulesRPCFixture(t, "keep me")
		logger := newCaptureLogger()
		if _, err := GuildGroupUpdateRPC(ownerCtx, logger, nil, nk, `{"group_id":"`+gid+`"}`); err != nil {
			t.Fatal(err)
		}
		if got := storedRules(t, nk, gid); got != "keep me" {
			t.Fatalf("rules changed to %q", got)
		}
		if len(*logger.events) != 0 {
			t.Fatalf("unexpected log events: %+v", *logger.events)
		}
	})

	t.Run("empty string clears", func(t *testing.T) {
		nk, gid := newRulesRPCFixture(t, "gone soon")
		if _, err := GuildGroupUpdateRPC(ownerCtx, newCaptureLogger(), nil, nk, `{"group_id":"`+gid+`","rules_text":""}`); err != nil {
			t.Fatal(err)
		}
		if got := storedRules(t, nk, gid); got != "" {
			t.Fatalf("rules = %q, want cleared", got)
		}
	})

	t.Run("non-admin is denied and nothing is written", func(t *testing.T) {
		nk, gid := newRulesRPCFixture(t, "mine")
		logger := newCaptureLogger()
		_, err := GuildGroupUpdateRPC(strangerCtx, logger, nil, nk, `{"group_id":"`+gid+`","rules_text":"hijack"}`)
		if err == nil {
			t.Fatal("expected permission error")
		}
		if got := storedRules(t, nk, gid); got != "mine" {
			t.Fatalf("rules = %q, want unchanged", got)
		}
	})

	t.Run("oversized is rejected and nothing is written", func(t *testing.T) {
		nk, gid := newRulesRPCFixture(t, "mine")
		big, _ := json.Marshal(map[string]any{"group_id": gid, "rules_text": strings.Repeat("x", maxGuildRulesTextLen+1)})
		if _, err := GuildGroupUpdateRPC(ownerCtx, newCaptureLogger(), nil, nk, string(big)); err == nil {
			t.Fatal("expected error")
		}
		if got := storedRules(t, nk, gid); got != "mine" {
			t.Fatalf("rules = %q, want unchanged", got)
		}
	})
}
