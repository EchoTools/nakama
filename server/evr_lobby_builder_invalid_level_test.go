package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"strings"
	"testing"

	"github.com/gofrs/uuid/v5"
	"github.com/heroiclabs/nakama-common/api"
	"github.com/heroiclabs/nakama-common/runtime"
	"github.com/heroiclabs/nakama/v3/server/evr"
	"go.uber.org/zap/zapcore"
	"google.golang.org/protobuf/types/known/wrapperspb"
)

// prepareFailNK lists unassigned matches in a fixed order and answers each
// SignalPrepareSession with the failure message chosen for that match ("" = success).
type prepareFailNK struct {
	runtime.NakamaModule
	matches  []*api.Match
	failures map[string]string
	labels   map[string]string
	signaled []string
}

func (n *prepareFailNK) add(extIP, failure string) {
	label := &MatchLabel{
		ID:        MatchID{UUID: uuid.Must(uuid.NewV4()), Node: "testnode"},
		LobbyType: UnassignedLobby,
		Mode:      evr.ModeUnloaded,
		GameServer: &GameServerPresence{
			Endpoint: evr.Endpoint{ExternalIP: net.ParseIP(extIP), Port: 6792},
		},
	}
	id := label.ID.String()
	n.matches = append(n.matches, &api.Match{MatchId: id, Label: wrapperspb.String(label.GetLabel())})
	n.failures[id] = failure
	n.labels[id] = label.GetLabel()
}

func (n *prepareFailNK) MatchList(_ context.Context, _ int, _ bool, _ string, _, _ *int, _ string) ([]*api.Match, error) {
	return n.matches, nil
}

func (n *prepareFailNK) MatchSignal(_ context.Context, id string, _ string) (string, error) {
	n.signaled = append(n.signaled, id)
	resp := SignalResponse{Success: true, Payload: n.labels[id]}
	if msg := n.failures[id]; msg != "" {
		resp = SignalResponse{Message: msg}
	}
	b, _ := json.Marshal(resp)
	return string(b), nil
}

func TestLobbyGameServerAllocate_InvalidLevelOnlyWhenEveryPrepareRefusesIt(t *testing.T) {
	const (
		invalidLevel = "bad request: invalid level `mpl_arena_a` for mode `echo_combat_private`"
		busy         = "session already prepared"
	)
	logger := NewRuntimeGoLogger(NewJSONLogger(os.Stdout, zapcore.ErrorLevel, JSONFormat))
	settings := &MatchSettings{Mode: evr.ModeCombatPrivate, Level: evr.LevelArena}

	cases := []struct {
		name        string
		failures    []string
		wantInvalid bool
		wantNoSrv   bool
	}{
		{"all refuse the level", []string{invalidLevel, invalidLevel}, true, false},
		{"invalid level then other failure", []string{invalidLevel, busy}, false, true},
		{"other failure then invalid level", []string{busy, invalidLevel}, false, true},
		{"all fail otherwise", []string{busy, busy}, false, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			nk := &prepareFailNK{failures: map[string]string{}, labels: map[string]string{}}
			for i, f := range tc.failures {
				nk.add(net.IPv4(198, 51, 100, byte(i+1)).String(), f)
			}
			label, err := LobbyGameServerAllocate(context.Background(), logger, nk, []string{uuid.Must(uuid.NewV4()).String()}, nil, settings, nil, false, false, "", nil)
			if label != nil {
				t.Fatalf("label = %v, want nil", label)
			}
			if len(nk.signaled) != len(tc.failures) {
				t.Fatalf("signaled %d matches, want %d", len(nk.signaled), len(tc.failures))
			}
			if got := strings.Contains(fmt.Sprint(err), "bad request: invalid level"); got != tc.wantInvalid {
				t.Errorf("invalid level (%v) = %v, want %v", err, got, tc.wantInvalid)
			}
			if got := errors.Is(err, ErrMatchmakingNoAvailableServers); got != tc.wantNoSrv {
				t.Errorf("errors.Is(%v, ErrMatchmakingNoAvailableServers) = %v, want %v", err, got, tc.wantNoSrv)
			}
		})
	}

	t.Run("a later success still wins", func(t *testing.T) {
		nk := &prepareFailNK{failures: map[string]string{}, labels: map[string]string{}}
		nk.add("198.51.100.1", invalidLevel)
		nk.add("198.51.100.2", "")
		label, err := LobbyGameServerAllocate(context.Background(), logger, nk, []string{uuid.Must(uuid.NewV4()).String()}, nil, settings, nil, false, false, "", nil)
		if err != nil || label == nil {
			t.Fatalf("got (%v, %v), want a label", label, err)
		}
	})
}
