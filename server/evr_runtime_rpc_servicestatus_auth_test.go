package server

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/heroiclabs/nakama-common/runtime"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"
)

const statusTestKey = "synthetic-signing-key-for-tests-only"

type fakePinger struct {
	err   error
	block chan struct{} // if set, PingContext ignores ctx and waits on it
	calls atomic.Int32
}

func (f *fakePinger) PingContext(ctx context.Context) error {
	f.calls.Add(1)
	if f.block != nil {
		<-f.block
	}
	return f.err
}

func statusCtx(components string) context.Context {
	params := map[string][]string{"components": {components}}
	// nakama-common defines the runtime ctx keys as plain strings, which is what the handler reads.
	return context.WithValue(context.Background(), runtime.RUNTIME_CTX_QUERY_PARAMS, params) //nolint:staticcheck
}

func TestCheckAuthComponent(t *testing.T) {
	tests := []struct {
		name       string
		db         dbPinger
		key        string
		wantStatus string
		wantReason string
	}{
		{"up", &fakePinger{}, statusTestKey, "up", "ok"},
		{"database unavailable", &fakePinger{err: errors.New("dial tcp 10.0.0.1:26257: connection refused")}, statusTestKey, "down", "database unavailable"},
		{"no database handle", nil, statusTestKey, "down", "database unavailable"},
		{"bad signing key", &fakePinger{}, "", "down", "no signing key"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := checkAuthComponent(context.Background(), tt.db, tt.key, time.Second)
			require.Equal(t, StatusComponent{Status: tt.wantStatus, Reason: tt.wantReason}, got)
		})
	}
}

// A token signed with one key must not verify under another.
func TestProbeAuth_VerificationFailureIsDown(t *testing.T) {
	got := probeAuth(context.Background(), &fakePinger{}, statusTestKey, "a-different-synthetic-key")
	require.Equal(t, StatusComponent{Status: "down", Reason: "token verification failed"}, got)
}

// The probe must come back within its timeout even when the driver ignores ctx.
func TestCheckAuthComponent_HangingDatabaseTimesOut(t *testing.T) {
	block := make(chan struct{})
	defer close(block)

	start := time.Now()
	got := checkAuthComponent(context.Background(), &fakePinger{block: block}, statusTestKey, 50*time.Millisecond)

	require.Equal(t, StatusComponent{Status: "down", Reason: "timeout"}, got)
	require.Less(t, time.Since(start), 2*time.Second)
}

// The reason must be a fixed string: the driver's error text can carry a DSN.
func TestCheckAuthComponent_ReasonDoesNotLeakDriverError(t *testing.T) {
	got := checkAuthComponent(context.Background(), &fakePinger{err: errors.New("password=hunter2 host=db")}, statusTestKey, time.Second)
	require.NotContains(t, got.Reason, "hunter2")
}

func TestServiceStatusRPC_DefaultResponseUnchanged(t *testing.T) {
	h := NewRPCHandler(context.Background(), nil, nil)
	h.sessionKey = statusTestKey
	h.responseCache.Set("server-status", "3 players in 1 matches", time.Minute)

	// No components requested: byte-identical, and the probe never runs.
	got, err := h.ServiceStatusRPC(context.Background(), NewRuntimeGoLogger(zap.NewNop()), nil, nil, "")
	require.NoError(t, err)
	require.Equal(t, "3 players in 1 matches", got)

	// An unknown component name does not change the response either way it is read.
	got, err = h.ServiceStatusRPC(statusCtx("bogus"), NewRuntimeGoLogger(zap.NewNop()), nil, nil, "")
	require.NoError(t, err)
	require.JSONEq(t, `{"services":"3 players in 1 matches","components":{}}`, got)
}

func TestServiceStatusRPC_NilDBReportsAuthDown(t *testing.T) {
	h := NewRPCHandler(context.Background(), nil, nil)
	h.sessionKey = statusTestKey
	h.responseCache.Set("server-status", `[{"serviceid":"services","available":true,"message":""}]`, time.Minute)

	got, err := h.ServiceStatusRPC(statusCtx("auth"), NewRuntimeGoLogger(zap.NewNop()), nil, nil, "")
	require.NoError(t, err)
	require.JSONEq(t, `{"services":[{"serviceid":"services","available":true,"message":""}],"components":{"auth":{"status":"down","reason":"database unavailable"}}}`, got)
}

func TestServiceStatusWithComponents_UpKeepsExistingAndLeaksNoToken(t *testing.T) {
	h := NewRPCHandler(context.Background(), nil, nil)
	h.sessionKey = statusTestKey
	base := `[{"serviceid":"services","available":true,"message":"m"}]`

	got, err := h.serviceStatusWithRequestedComponents(statusCtx("auth"), NewRuntimeGoLogger(zap.NewNop()), &fakePinger{}, base, map[string]bool{"auth": true})
	require.NoError(t, err)
	require.JSONEq(t, `{"services":`+base+`,"components":{"auth":{"status":"up","reason":"ok"}}}`, got)
	require.NotContains(t, got, "eyJ", "a JWT must never be returned")
	require.False(t, strings.Contains(got, statusTestKey), "the signing key must never be returned")
}

func TestServiceStatusWithComponents_AuthCachedAndDownLogged(t *testing.T) {
	core, logs := observer.New(zapcore.InfoLevel)
	logger := NewRuntimeGoLogger(zap.New(core))
	h := NewRPCHandler(context.Background(), nil, nil)
	h.sessionKey = statusTestKey
	db := &fakePinger{err: errors.New("down")}

	for range 3 {
		got, err := h.serviceStatusWithRequestedComponents(context.Background(), logger, db, "x", map[string]bool{"auth": true})
		require.NoError(t, err)
		require.Contains(t, got, `"status":"down"`)
	}

	require.EqualValues(t, 1, db.calls.Load(), "polling inside the TTL must not reach the database")
	require.Equal(t, 1, logs.FilterMessage("servicestatus component is down").Len(), "a down component is logged once per probe")
}
