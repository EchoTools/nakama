package server

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"

	"github.com/gofrs/uuid/v5"
	"github.com/heroiclabs/nakama-common/api"
	"github.com/heroiclabs/nakama-common/runtime"
	"google.golang.org/protobuf/types/known/wrapperspb"
)

// allocateRPCNakama is a fake Nakama module that holds one running match and the
// guild groups around it, so AllocateMatchRPC can be driven end to end. The
// methods below are defined on this leaf type; the RPC reaches them through
// runtime.NakamaModule, which dispatches to the outermost type, and no embedder
// defines them, so defect class 1 (AGENTS.md) does not apply.
type allocateRPCNakama struct {
	*occTestNakamaModule

	mu      sync.Mutex
	groups  map[string]*api.Group
	matchID string
	label   *MatchLabel // the live match label; PrepareSession rewrites it
	signals int         // MatchSignal calls received
}

func (n *allocateRPCNakama) GroupsGetId(ctx context.Context, ids []string) ([]*api.Group, error) {
	out := make([]*api.Group, 0, len(ids))
	for _, id := range ids {
		if g, ok := n.groups[id]; ok {
			out = append(out, g)
		}
	}
	return out, nil
}

func (n *allocateRPCNakama) AccountGetId(ctx context.Context, userID string) (*api.Account, error) {
	return &api.Account{User: &api.User{Id: userID}, CustomId: "discord-" + userID}, nil
}

func (n *allocateRPCNakama) MatchGet(ctx context.Context, id string) (*api.Match, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if id != n.matchID {
		return nil, nil
	}
	b, _ := json.Marshal(n.label)
	return &api.Match{MatchId: id, Authoritative: true, Label: wrapperspb.String(string(b))}, nil
}

// MatchSignal models the match handler's prepare-session: it rewrites the live
// label's GroupID to the requested one, exactly the mutation a refused call must
// never cause.
func (n *allocateRPCNakama) MatchSignal(ctx context.Context, id string, data string) (string, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.signals++
	env := SignalEnvelope{}
	if err := json.Unmarshal([]byte(data), &env); err != nil {
		return "", err
	}
	settings := &MatchSettings{}
	if err := json.Unmarshal(env.Payload, settings); err != nil {
		return "", err
	}
	gid := settings.GroupID
	n.label.GroupID = &gid
	b, _ := json.Marshal(n.label)
	resp, _ := json.Marshal(SignalResponse{Success: true, Payload: string(b)})
	return string(resp), nil
}

func (n *allocateRPCNakama) currentGroupID() uuid.UUID {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.label.GroupID == nil {
		return uuid.Nil
	}
	return *n.label.GroupID
}

const (
	allocServerGuild = "aaaaaaaa-0000-0000-0000-00000000000a" // the one the server hosts for
	allocTargetGuild = "bbbbbbbb-0000-0000-0000-00000000000b" // the guild the caller asks to allocate for
	allocMatchID     = "cccccccc-0000-0000-0000-00000000000c"
	allocCaller      = "dddddddd-0000-0000-0000-00000000000d"
	allocOperator    = "eeeeeeee-0000-0000-0000-00000000000e" // the server's own operator
	allocRole        = "allocator-role"
)

// newAllocateRPCFixture builds a server that hosts only for allocServerGuild,
// parked in an unassigned match. allocatorIn lists the guilds in which the
// caller holds the allocator role.
func newAllocateRPCFixture(t *testing.T, allocatorIn ...string) *allocateRPCNakama {
	t.Helper()
	prev := serviceSettings.Load()
	ServiceSettingsUpdate(&ServiceSettingsData{DiscordBotUserID: "bot-uid"})
	t.Cleanup(func() { serviceSettings.Store(prev) })

	nk := &allocateRPCNakama{
		occTestNakamaModule: newOCCTestNakamaModule(),
		groups:              map[string]*api.Group{},
		matchID:             allocMatchID + ".node",
	}
	for _, gid := range []string{allocServerGuild, allocTargetGuild} {
		nk.groups[gid] = &api.Group{
			Id:       gid,
			Name:     "guild-" + gid[:2],
			LangTag:  GuildGroupLangTag,
			Metadata: `{"roles":{"allocator":"` + allocRole + `"}}`,
		}
		roleCache := map[string]map[string]bool{allocRole: {}}
		for _, in := range allocatorIn {
			if in == gid {
				roleCache[allocRole][allocCaller] = true
			}
		}
		state, _ := json.Marshal(map[string]any{"group_id": gid, "role_cache": roleCache})
		nk.seedObject("bot-uid", StorageCollectionState, gid, string(state))
	}
	nk.label = &MatchLabel{
		ID:        MatchIDFromStringOrNil(nk.matchID),
		LobbyType: UnassignedLobby,
		GameServer: &GameServerPresence{
			OperatorID: uuid.FromStringOrNil(allocOperator),
			GroupIDs:   []uuid.UUID{uuid.FromStringOrNil(allocServerGuild)},
		},
	}
	return nk
}

func allocateCtx(userID string, perms *UserPermissions) context.Context {
	ctx := context.WithValue(context.Background(), runtime.RUNTIME_CTX_USER_ID, userID) //nolint:staticcheck // SA1029: key type is dictated by the runtime API
	if perms == nil {
		perms = &UserPermissions{}
	}
	return context.WithValue(ctx, contextKeyUserPermissions{}, perms)
}

func allocatePayload() string {
	b, _ := json.Marshal(map[string]any{
		"group_id": allocTargetGuild,
		"mode":     "echo_arena",
		"region":   allocMatchID + ".node",
	})
	return string(b)
}

// A caller who is allocator in the guild they ask for AND in a guild the server
// hosts for may allocate that server for the target, even though the target is
// outside the server's group_ids.
func TestAllocateMatchRPC_AllocatorInBothGuilds_Allowed(t *testing.T) {
	nk := newAllocateRPCFixture(t, allocServerGuild, allocTargetGuild)
	if _, err := AllocateMatchRPC(allocateCtx(allocCaller, nil), newCaptureLogger(), nil, nk, allocatePayload()); err != nil {
		t.Fatalf("allocator in both guilds was refused: %v", err)
	}
	if got := nk.currentGroupID(); got != uuid.FromStringOrNil(allocTargetGuild) {
		t.Fatalf("match group = %s, want target %s", got, allocTargetGuild)
	}
}

// Allocator only in the target guild, with no standing in any guild the server
// hosts for: refused, and the match is untouched.
func TestAllocateMatchRPC_AllocatorOnlyInTarget_Refused(t *testing.T) {
	nk := newAllocateRPCFixture(t, allocTargetGuild)
	logger := newCaptureLogger()
	_, err := AllocateMatchRPC(allocateCtx(allocCaller, nil), logger, nil, nk, allocatePayload())
	assertAllocateRefused(t, nk, err)

	// The refusal is attributable: who asked, for which guild, on which match.
	var found bool
	for _, e := range *logger.events {
		if e.level == "warn" && e.msg == "Allocation refused: caller is not allocator in any guild the game server hosts for" &&
			e.fields["caller_id"] == allocCaller && e.fields["target_gid"] == allocTargetGuild {
			found = true
		}
	}
	if !found {
		t.Fatalf("no refusal record; events: %+v", *logger.events)
	}
}

// Allocator only in the server's guild: they have no right to allocate for the
// target, so refused and untouched.
func TestAllocateMatchRPC_AllocatorOnlyInServerGuild_Refused(t *testing.T) {
	nk := newAllocateRPCFixture(t, allocServerGuild)
	_, err := AllocateMatchRPC(allocateCtx(allocCaller, nil), newCaptureLogger(), nil, nk, allocatePayload())
	assertAllocateRefused(t, nk, err)
}

// A refused call must change no match state: the label's group is what it was
// and the match never received a prepare signal.
func TestAllocateMatchRPC_RefusedCallLeavesMatchLabelUnchanged(t *testing.T) {
	nk := newAllocateRPCFixture(t, allocTargetGuild)
	before, _ := json.Marshal(nk.label)
	if _, err := AllocateMatchRPC(allocateCtx(allocCaller, nil), newCaptureLogger(), nil, nk, allocatePayload()); err == nil {
		t.Fatal("expected a refusal")
	}
	after, _ := json.Marshal(nk.label)
	if string(before) != string(after) {
		t.Fatalf("match label changed by a refused call:\nbefore %s\nafter  %s", before, after)
	}
	if nk.signals != 0 {
		t.Fatalf("refused call sent %d signal(s) to the match, want 0", nk.signals)
	}
}

// The bypasses stay: a global operator, and the server's own operator who is
// allocator in the target guild.
func TestAllocateMatchRPC_BypassesKept(t *testing.T) {
	t.Run("global operator", func(t *testing.T) {
		nk := newAllocateRPCFixture(t) // allocator nowhere
		if _, err := AllocateMatchRPC(allocateCtx(allocCaller, &UserPermissions{IsGlobalOperator: true}), newCaptureLogger(), nil, nk, allocatePayload()); err != nil {
			t.Fatalf("global operator refused: %v", err)
		}
	})
	t.Run("server operator allocator in target", func(t *testing.T) {
		nk := newAllocateRPCFixture(t)
		// make the operator an allocator in the target guild
		roleCache := map[string]map[string]bool{allocRole: {allocOperator: true}}
		state, _ := json.Marshal(map[string]any{"group_id": allocTargetGuild, "role_cache": roleCache})
		nk.seedObject("bot-uid", StorageCollectionState, allocTargetGuild, string(state))
		if _, err := AllocateMatchRPC(allocateCtx(allocOperator, nil), newCaptureLogger(), nil, nk, allocatePayload()); err != nil {
			t.Fatalf("server operator refused: %v", err)
		}
	})
}

func assertAllocateRefused(t *testing.T, nk *allocateRPCNakama, err error) {
	t.Helper()
	if err == nil {
		t.Fatal("expected a permission refusal, call succeeded")
	}
	var rerr *runtime.Error
	if !errors.As(err, &rerr) || rerr.Code != StatusPermissionDenied {
		t.Fatalf("err = %v, want PermissionDenied", err)
	}
	if nk.signals != 0 {
		t.Fatalf("refused call still signalled the match %d time(s)", nk.signals)
	}
	if got := nk.currentGroupID(); got != uuid.Nil {
		t.Fatalf("refused call left match group = %s, want unset", got)
	}
}
