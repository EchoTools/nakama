package server

import (
	"context"
	"testing"
	"time"

	"github.com/gofrs/uuid/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"
)

// newPingLogObserver returns a logger whose entries are captured for assertions.
func newPingLogObserver() (*zap.Logger, *observer.ObservedLogs) {
	core, logs := observer.New(zapcore.DebugLevel)
	return zap.New(core), logs
}

// Test A: a member who stays silent is pinged once, and the pass logs the send
// and then the timeout with how long it waited.
func TestValidatePreJoinPing_LogsSendAndTimeout(t *testing.T) {
	w := newPingTestWorld(t)
	entrants, follower, _ := w.partyOfTwo(t, 0, 79)
	logger, logs := newPingLogObserver()

	err := w.pipeline.validatePreJoinPing(context.Background(), logger, w.label, entrants)
	require.ErrorIs(t, err, ErrPreJoinPingFailed, "a silent joining member fails the check")

	sent := logs.FilterMessage("Pre-join ping sent").All()
	require.Len(t, sent, 1, "one send entry for the one pinged member")
	assert.Equal(t, zapcore.InfoLevel, sent[0].Level)
	assert.Equal(t, entrants[0].SessionID.String(), sent[0].ContextMap()["sid"], "the send entry names the silent member's session")
	assert.Equal(t, w.endpoint.String(), sent[0].ContextMap()["endpoint"], "the send entry names the endpoint")
	assert.Equal(t, w.label.ID.UUID.String(), sent[0].ContextMap()["mid"])
	assert.Equal(t, entrants[0].UserID.String(), sent[0].ContextMap()["uid"])
	assert.Equal(t, int64(preJoinPingTimeout/time.Millisecond), sent[0].ContextMap()["timeout_ms"])

	timedOut := logs.FilterMessage("Pre-join ping timed out").All()
	require.Len(t, timedOut, 1, "one timeout entry for the silent member")
	assert.Equal(t, zapcore.WarnLevel, timedOut[0].Level)
	assert.Equal(t, entrants[0].SessionID.String(), timedOut[0].ContextMap()["sid"])
	elapsed, ok := timedOut[0].ContextMap()["elapsed_ms"].(int64)
	require.True(t, ok, "elapsed_ms is an int64 field")
	assert.GreaterOrEqual(t, elapsed, int64(4000), "the timeout entry reports the time waited")

	assert.Greater(t, follower.sendCount(), 0, "the silent member was sent the ping")
}

// Test B: a member who answers is logged as answered, with the RTT the
// response recorded and the time it took.
func TestValidatePreJoinPing_LogsAnswered(t *testing.T) {
	w := newPingTestWorld(t)
	entrants, _, _ := w.partyOfTwo(t, 79, 0)
	logger, logs := newPingLogObserver()

	err := w.pipeline.validatePreJoinPing(context.Background(), logger, w.label, entrants)
	require.NoError(t, err, "a member who answers within the RTT ceiling passes")

	answered := logs.FilterMessage("Pre-join ping answered").All()
	require.Len(t, answered, 1, "one answered entry for the member who answered")
	assert.Equal(t, zapcore.InfoLevel, answered[0].Level)
	assert.Equal(t, entrants[0].SessionID.String(), answered[0].ContextMap()["sid"])
	assert.Equal(t, int64(79), answered[0].ContextMap()["rtt_ms"], "rtt_ms is the RTT the response recorded")
	_, ok := answered[0].ContextMap()["elapsed_ms"].(int64)
	assert.True(t, ok, "elapsed_ms is present as an int64 field")

	assert.Empty(t, logs.FilterMessage("Pre-join ping timed out").All(), "an answered member must not log a timeout")
}

// Test C: a member with a fresh cached latency entry needs no ping, so the
// send entry must not appear.
func TestValidatePreJoinPing_CachedEntryLogsNoSend(t *testing.T) {
	w := newPingTestWorld(t)
	entrants, follower, _ := w.partyOfTwo(t, 0, 0)
	follower.history.Add(w.endpoint.ExternalIP, 50, 10, time.Now())
	logger, logs := newPingLogObserver()

	err := w.pipeline.validatePreJoinPing(context.Background(), logger, w.label, entrants)
	require.NoError(t, err)

	assert.Equal(t, 0, follower.sendCount(), "a fresh cached entry is not pinged")
	assert.Empty(t, logs.FilterMessage("Pre-join ping sent").All(), "no send entry when nothing was sent")
}

// Unit level: the waiter registered for a session is signalled by notify.
func TestNotifyPreJoinPingWaiter_SignalsRegisteredWaiter(t *testing.T) {
	sid := uuid.Must(uuid.NewV4())
	ch := registerPreJoinPingWaiter(sid)
	defer cleanupPreJoinPingWaiter(sid)

	assert.True(t, notifyPreJoinPingWaiter(sid), "a registered waiter reports it was signalled")

	select {
	case <-ch:
	default:
		t.Fatal("the registered waiter was not signalled")
	}

	assert.False(t, notifyPreJoinPingWaiter(sid), "with no waiter left, nothing is signalled")
}
