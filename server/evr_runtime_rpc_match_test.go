package server

// Tests for shutdownMatchRpc.
//
// Regression: before this fix, the handler rejected matches with LobbyType == UnassignedLobby,
// preventing global operators from terminating parked/unassigned game servers.

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/gofrs/uuid/v5"
	"github.com/heroiclabs/nakama-common/api"
	"github.com/heroiclabs/nakama-common/runtime"
	"google.golang.org/protobuf/types/known/wrapperspb"
)

// shutdownNK is a minimal NakamaModule stub for shutdownMatchRpc tests.
type shutdownNK struct {
	runtime.NakamaModule
	matchLabel string // JSON label to return from MatchGet; "" means "not found"
	signalErr  error  // error to return from MatchSignal, nil means success
}

func (n *shutdownNK) MatchGet(_ context.Context, matchID string) (*api.Match, error) {
	if n.matchLabel == "" {
		return nil, runtime.ErrMatchNotFound
	}
	return &api.Match{
		MatchId:       matchID,
		Authoritative: true,
		Label:         &wrapperspb.StringValue{Value: n.matchLabel},
	}, nil
}

func (n *shutdownNK) MatchSignal(_ context.Context, _ string, _ string) (string, error) {
	if n.signalErr != nil {
		return "", n.signalErr
	}
	return SignalResponse{Success: true}.String(), nil
}

// makeMatchLabel builds a JSON-marshalled MatchLabel for the given lobby type.
func makeMatchLabel(t *testing.T, matchID MatchID, lobbyType LobbyType) string {
	t.Helper()
	label := &MatchLabel{
		ID:        matchID,
		LobbyType: lobbyType,
	}
	b, err := json.Marshal(label)
	if err != nil {
		t.Fatalf("marshal match label: %v", err)
	}
	return string(b)
}

// operatorCtx returns a context carrying a user ID (simulating an authenticated global operator).
// Authorization is handled by middleware; this just supplies the user ID that the handler reads.
func operatorCtx() context.Context {
	ctx := context.Background()
	ctx = context.WithValue(ctx, runtime.RUNTIME_CTX_USER_ID, uuid.Must(uuid.NewV4()).String())
	ctx = context.WithValue(ctx, runtime.RUNTIME_CTX_NODE, "testnode")
	return WithUserPermissions(ctx, &UserPermissions{IsGlobalOperator: true})
}

func TestShutdownMatchRpc_InvalidPayload(t *testing.T) {
	nk := &shutdownNK{}
	_, err := shutdownMatchRpc(operatorCtx(), &mockLogger{}, nil, nk, "not-json")
	if err == nil {
		t.Fatal("expected error for invalid JSON payload, got nil")
	}
}

func TestShutdownMatchRpc_MissingMatchID(t *testing.T) {
	nk := &shutdownNK{}
	payload := `{}`
	_, err := shutdownMatchRpc(operatorCtx(), &mockLogger{}, nil, nk, payload)
	if err == nil {
		t.Fatal("expected error for missing match_id, got nil")
	}
	if !strings.Contains(err.Error(), "match_id") {
		t.Errorf("error should mention match_id, got: %v", err)
	}
}

func TestShutdownMatchRpc_MatchNotFound(t *testing.T) {
	nk := &shutdownNK{matchLabel: ""} // MatchGet returns "not found"
	matchID := MatchID{UUID: uuid.Must(uuid.NewV4()), Node: "testnode"}
	payload, _ := json.Marshal(shutdownMatchRequest{MatchID: matchID})

	_, err := shutdownMatchRpc(operatorCtx(), &mockLogger{}, nil, nk, string(payload))
	if err == nil {
		t.Fatal("expected error for non-existent match, got nil")
	}
}

// TestShutdownMatchRpc_UnassignedLobby is the primary regression test:
// a global operator must be able to terminate an unassigned (parked) game server.
// Before the fix, this returned "match %s is not in a lobby".
func TestShutdownMatchRpc_UnassignedLobby(t *testing.T) {
	matchID := MatchID{UUID: uuid.Must(uuid.NewV4()), Node: "testnode"}
	nk := &shutdownNK{matchLabel: makeMatchLabel(t, matchID, UnassignedLobby)}
	payload, _ := json.Marshal(shutdownMatchRequest{MatchID: matchID, GraceSeconds: 5})

	result, err := shutdownMatchRpc(operatorCtx(), &mockLogger{}, nil, nk, string(payload))
	if err != nil {
		t.Fatalf("global operator must be able to terminate unassigned match; got error: %v", err)
	}

	var resp shutdownMatchResponse
	if err := json.Unmarshal([]byte(result), &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if !resp.Success {
		t.Errorf("expected Success=true, got: %+v", resp)
	}
}

func TestShutdownMatchRpc_PublicLobby(t *testing.T) {
	matchID := MatchID{UUID: uuid.Must(uuid.NewV4()), Node: "testnode"}
	nk := &shutdownNK{matchLabel: makeMatchLabel(t, matchID, PublicLobby)}
	payload, _ := json.Marshal(shutdownMatchRequest{MatchID: matchID})

	result, err := shutdownMatchRpc(operatorCtx(), &mockLogger{}, nil, nk, string(payload))
	if err != nil {
		t.Fatalf("global operator must be able to terminate public match; got error: %v", err)
	}

	var resp shutdownMatchResponse
	if err := json.Unmarshal([]byte(result), &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if !resp.Success {
		t.Errorf("expected Success=true, got: %+v", resp)
	}
}

func TestShutdownMatchRpc_PrivateLobby(t *testing.T) {
	matchID := MatchID{UUID: uuid.Must(uuid.NewV4()), Node: "testnode"}
	nk := &shutdownNK{matchLabel: makeMatchLabel(t, matchID, PrivateLobby)}
	payload, _ := json.Marshal(shutdownMatchRequest{MatchID: matchID})

	result, err := shutdownMatchRpc(operatorCtx(), &mockLogger{}, nil, nk, string(payload))
	if err != nil {
		t.Fatalf("global operator must be able to terminate private match; got error: %v", err)
	}

	var resp shutdownMatchResponse
	if err := json.Unmarshal([]byte(result), &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if !resp.Success {
		t.Errorf("expected Success=true, got: %+v", resp)
	}
}

// TestShutdownMatchRpc_DefaultGraceSeconds verifies that omitting grace_seconds defaults to 10.
func TestShutdownMatchRpc_DefaultGraceSeconds(t *testing.T) {
	matchID := MatchID{UUID: uuid.Must(uuid.NewV4()), Node: "testnode"}

	var capturedSignalData string
	captureNK := &captureSignalNK{
		matchLabel: makeMatchLabel(t, matchID, PublicLobby),
		onSignal: func(data string) {
			capturedSignalData = data
		},
	}

	payload, _ := json.Marshal(shutdownMatchRequest{MatchID: matchID}) // no GraceSeconds
	_, err := shutdownMatchRpc(operatorCtx(), &mockLogger{}, nil, captureNK, string(payload))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Decode the envelope and check GraceSeconds == 10
	var env SignalEnvelope
	if err := json.Unmarshal([]byte(capturedSignalData), &env); err != nil {
		t.Fatalf("unmarshal signal envelope: %v", err)
	}
	var sdPayload SignalShutdownPayload
	if err := json.Unmarshal(env.Payload, &sdPayload); err != nil {
		t.Fatalf("unmarshal shutdown payload: %v", err)
	}
	if sdPayload.GraceSeconds != 10 {
		t.Errorf("expected GraceSeconds=10, got %d", sdPayload.GraceSeconds)
	}
}

// captureSignalNK records the signal data for inspection.
type captureSignalNK struct {
	runtime.NakamaModule
	matchLabel string
	onSignal   func(data string)
}

func (n *captureSignalNK) MatchGet(_ context.Context, matchID string) (*api.Match, error) {
	return &api.Match{
		MatchId:       matchID,
		Authoritative: true,
		Label:         &wrapperspb.StringValue{Value: n.matchLabel},
	}, nil
}

func (n *captureSignalNK) MatchSignal(_ context.Context, _ string, data string) (string, error) {
	if n.onSignal != nil {
		n.onSignal(data)
	}
	return SignalResponse{Success: true}.String(), nil
}
