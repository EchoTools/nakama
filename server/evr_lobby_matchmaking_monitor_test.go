package server

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gofrs/uuid/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

// mockMatchmakingTracker is a tracker that can be controlled for testing the matchmaking monitor
type mockMatchmakingTracker struct {
	testTracker
	mu              sync.RWMutex
	presences       map[presenceKey]*PresenceMeta
	untrackAllCount atomic.Int32
}

type presenceKey struct {
	sessionID uuid.UUID
	stream    PresenceStream
	userID    uuid.UUID
}

func newMockMatchmakingTracker() *mockMatchmakingTracker {
	return &mockMatchmakingTracker{
		presences: make(map[presenceKey]*PresenceMeta),
	}
}

func (t *mockMatchmakingTracker) Track(ctx context.Context, sessionID uuid.UUID, stream PresenceStream, userID uuid.UUID, meta PresenceMeta) (bool, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	key := presenceKey{sessionID: sessionID, stream: stream, userID: userID}
	_, exists := t.presences[key]
	t.presences[key] = &meta
	return true, !exists
}

func (t *mockMatchmakingTracker) Update(ctx context.Context, sessionID uuid.UUID, stream PresenceStream, userID uuid.UUID, meta PresenceMeta) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	key := presenceKey{sessionID: sessionID, stream: stream, userID: userID}
	t.presences[key] = &meta
	return true
}

func (t *mockMatchmakingTracker) GetLocalBySessionIDStreamUserID(sessionID uuid.UUID, stream PresenceStream, userID uuid.UUID) *PresenceMeta {
	t.mu.RLock()
	defer t.mu.RUnlock()
	key := presenceKey{sessionID: sessionID, stream: stream, userID: userID}
	return t.presences[key]
}

func (t *mockMatchmakingTracker) UntrackLocalByModes(sessionID uuid.UUID, modes map[uint8]struct{}, skipStream PresenceStream) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.untrackAllCount.Add(1)
	for key := range t.presences {
		if key.sessionID != sessionID {
			continue
		}
		if _, found := modes[key.stream.Mode]; !found {
			continue
		}
		if key.stream == skipStream {
			continue
		}
		delete(t.presences, key)
	}
}

func (t *mockMatchmakingTracker) hasPresence(sessionID uuid.UUID, stream PresenceStream, userID uuid.UUID) bool {
	t.mu.RLock()
	defer t.mu.RUnlock()
	key := presenceKey{sessionID: sessionID, stream: stream, userID: userID}
	_, exists := t.presences[key]
	return exists
}

// MatchmakingSession represents the state needed for matchmaking stream monitoring
type MatchmakingSession struct {
	SessionID uuid.UUID
	UserID    uuid.UUID
	Stream    PresenceStream
	Tracker   Tracker
}

// MonitorMatchmakingStreamV2 is a refactored version that accepts a specific stream to monitor
// and doesn't call LeaveMatchmakingStream on exit (the caller is responsible for cleanup)
func MonitorMatchmakingStreamV2(
	ctx context.Context,
	logger *zap.Logger,
	session *MatchmakingSession,
	checkInterval time.Duration,
	gracePeriod time.Duration,
	cancelFn context.CancelFunc,
) {
	stream := session.Stream

	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(checkInterval):
		}

		// Check if the matchmaking stream has been closed (i.e., the user has canceled matchmaking)
		if session.Tracker.GetLocalBySessionIDStreamUserID(session.SessionID, stream, session.UserID) == nil {
			// Wait grace period before canceling to avoid race conditions
			select {
			case <-ctx.Done():
				return
			case <-time.After(gracePeriod):
			}
			// Re-check after grace period - the presence might have been re-added
			if session.Tracker.GetLocalBySessionIDStreamUserID(session.SessionID, stream, session.UserID) == nil {
				cancelFn()
				return
			}
		}
	}
}

// TestMatchmakingMonitor_RaceConditionOnRequeue tests that when a player cancels and
// immediately re-queues for matchmaking, the new matchmaking session is not incorrectly
// canceled by the old monitor goroutine.
func TestMatchmakingMonitor_RaceConditionOnRequeue(t *testing.T) {
	logger := zap.NewNop()
	tracker := newMockMatchmakingTracker()

	sessionID := uuid.Must(uuid.NewV4())
	userID := uuid.Must(uuid.NewV4())
	groupID := uuid.Must(uuid.NewV4())

	stream := PresenceStream{
		Mode:    StreamModeMatchmaking,
		Subject: groupID,
	}

	// Create the matchmaking session info
	mmSession := &MatchmakingSession{
		SessionID: sessionID,
		UserID:    userID,
		Stream:    stream,
		Tracker:   tracker,
	}

	// Simulate first matchmaking session
	ctx1, cancel1 := context.WithCancel(context.Background())
	defer cancel1()

	// Add presence to stream (player joins matchmaking)
	tracker.Track(ctx1, sessionID, stream, userID, PresenceMeta{})

	// Start monitor for first session
	monitor1Done := make(chan struct{})
	go func() {
		MonitorMatchmakingStreamV2(ctx1, logger, mmSession, 50*time.Millisecond, 100*time.Millisecond, cancel1)
		close(monitor1Done)
	}()

	// Verify presence exists
	require.True(t, tracker.hasPresence(sessionID, stream, userID), "Presence should exist after joining")

	// Wait a bit for monitor to be running
	time.Sleep(100 * time.Millisecond)

	// Player cancels matchmaking - remove presence
	tracker.UntrackLocalByModes(sessionID, map[uint8]struct{}{StreamModeMatchmaking: {}}, PresenceStream{})
	require.False(t, tracker.hasPresence(sessionID, stream, userID), "Presence should be gone after cancel")

	// Immediately re-queue (before the old monitor has time to fully process the cancellation)
	ctx2, cancel2 := context.WithCancel(context.Background())
	defer cancel2()

	// Add presence again (player re-joins matchmaking)
	tracker.Track(ctx2, sessionID, stream, userID, PresenceMeta{})
	require.True(t, tracker.hasPresence(sessionID, stream, userID), "Presence should exist after re-joining")

	// Start monitor for second session
	monitor2Done := make(chan struct{})
	ctx2Canceled := atomic.Bool{}
	go func() {
		MonitorMatchmakingStreamV2(ctx2, logger, mmSession, 50*time.Millisecond, 100*time.Millisecond, func() {
			ctx2Canceled.Store(true)
			cancel2()
		})
		close(monitor2Done)
	}()

	// Wait for first monitor to finish (it should detect ctx1 is done)
	cancel1() // Cancel the first context
	select {
	case <-monitor1Done:
	case <-time.After(2 * time.Second):
		t.Fatal("First monitor did not exit in time")
	}

	// Give some time for any race condition to manifest
	time.Sleep(300 * time.Millisecond)

	// The second context should NOT have been canceled
	assert.False(t, ctx2Canceled.Load(), "Second matchmaking context should NOT have been canceled")
	assert.True(t, tracker.hasPresence(sessionID, stream, userID), "Presence should still exist for second session")

	// Clean up
	cancel2()
	<-monitor2Done
}

// TestMatchmakingMonitor_CancelsWhenPresenceRemoved tests that the monitor correctly
// cancels the context when the presence is removed and stays removed.
func TestMatchmakingMonitor_CancelsWhenPresenceRemoved(t *testing.T) {
	logger := zap.NewNop()
	tracker := newMockMatchmakingTracker()

	sessionID := uuid.Must(uuid.NewV4())
	userID := uuid.Must(uuid.NewV4())
	groupID := uuid.Must(uuid.NewV4())

	stream := PresenceStream{
		Mode:    StreamModeMatchmaking,
		Subject: groupID,
	}

	mmSession := &MatchmakingSession{
		SessionID: sessionID,
		UserID:    userID,
		Stream:    stream,
		Tracker:   tracker,
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Add presence
	tracker.Track(ctx, sessionID, stream, userID, PresenceMeta{})

	canceled := atomic.Bool{}
	monitorDone := make(chan struct{})
	go func() {
		MonitorMatchmakingStreamV2(ctx, logger, mmSession, 50*time.Millisecond, 100*time.Millisecond, func() {
			canceled.Store(true)
			cancel()
		})
		close(monitorDone)
	}()

	// Wait for monitor to start
	time.Sleep(100 * time.Millisecond)

	// Remove presence (player cancels)
	tracker.UntrackLocalByModes(sessionID, map[uint8]struct{}{StreamModeMatchmaking: {}}, PresenceStream{})

	// Wait for monitor to detect and cancel
	select {
	case <-monitorDone:
	case <-time.After(2 * time.Second):
		t.Fatal("Monitor did not exit in time")
	}

	assert.True(t, canceled.Load(), "Context should have been canceled when presence was removed")
}

// TestMatchmakingMonitor_ExitsWhenContextCanceled tests that the monitor exits
// cleanly when its context is canceled externally.
func TestMatchmakingMonitor_ExitsWhenContextCanceled(t *testing.T) {
	logger := zap.NewNop()
	tracker := newMockMatchmakingTracker()

	sessionID := uuid.Must(uuid.NewV4())
	userID := uuid.Must(uuid.NewV4())
	groupID := uuid.Must(uuid.NewV4())

	stream := PresenceStream{
		Mode:    StreamModeMatchmaking,
		Subject: groupID,
	}

	mmSession := &MatchmakingSession{
		SessionID: sessionID,
		UserID:    userID,
		Stream:    stream,
		Tracker:   tracker,
	}

	ctx, cancel := context.WithCancel(context.Background())

	// Add presence
	tracker.Track(ctx, sessionID, stream, userID, PresenceMeta{})

	monitorDone := make(chan struct{})
	go func() {
		MonitorMatchmakingStreamV2(ctx, logger, mmSession, 50*time.Millisecond, 100*time.Millisecond, cancel)
		close(monitorDone)
	}()

	// Wait for monitor to start
	time.Sleep(100 * time.Millisecond)

	// Cancel context externally (e.g., player found a match)
	cancel()

	// Monitor should exit
	select {
	case <-monitorDone:
	case <-time.After(2 * time.Second):
		t.Fatal("Monitor did not exit when context was canceled")
	}

	// Presence should still exist (not removed by monitor)
	assert.True(t, tracker.hasPresence(sessionID, stream, userID), "Presence should still exist - monitor should not clean up on external cancel")
}

// TestOldMonitorBehavior_RaceConditionBug demonstrates the bug in the original implementation
// where the defer LeaveMatchmakingStream in the old monitor removes the new session's presence
func TestOldMonitorBehavior_RaceConditionBug(t *testing.T) {
	_ = zap.NewNop() // logger would be used in real implementation
	tracker := newMockMatchmakingTracker()

	sessionID := uuid.Must(uuid.NewV4())
	userID := uuid.Must(uuid.NewV4())
	groupID := uuid.Must(uuid.NewV4())

	stream := PresenceStream{
		Mode:    StreamModeMatchmaking,
		Subject: groupID,
	}

	// Simulate the OLD buggy behavior:
	// The old monitor had: defer LeaveMatchmakingStream(logger, session)
	// Which would call: s.tracker.UntrackLocalByModes(s.id, map[uint8]struct{}{StreamModeMatchmaking: {}}, PresenceStream{})
	// This removes ALL matchmaking presences for the session, including new ones!

	oldMonitorBuggy := func(ctx context.Context, checkPresence func() bool, cancelFn context.CancelFunc, onExit func()) {
		defer onExit() // This simulates the buggy defer LeaveMatchmakingStream
		for {
			select {
			case <-ctx.Done():
				return
			case <-time.After(50 * time.Millisecond):
			}

			if !checkPresence() {
				<-time.After(100 * time.Millisecond)
				cancelFn()
				return
			}
		}
	}

	// First matchmaking session
	ctx1, cancel1 := context.WithCancel(context.Background())
	defer cancel1()

	tracker.Track(ctx1, sessionID, stream, userID, PresenceMeta{})

	monitor1Done := make(chan struct{})
	go func() {
		oldMonitorBuggy(
			ctx1,
			func() bool { return tracker.hasPresence(sessionID, stream, userID) },
			cancel1,
			func() {
				// This simulates LeaveMatchmakingStream - removes ALL matchmaking presences!
				tracker.UntrackLocalByModes(sessionID, map[uint8]struct{}{StreamModeMatchmaking: {}}, PresenceStream{})
			},
		)
		close(monitor1Done)
	}()

	time.Sleep(100 * time.Millisecond)

	// Player cancels - remove presence
	tracker.UntrackLocalByModes(sessionID, map[uint8]struct{}{StreamModeMatchmaking: {}}, PresenceStream{})

	// Player IMMEDIATELY re-queues
	ctx2, cancel2 := context.WithCancel(context.Background())
	defer cancel2()
	tracker.Track(ctx2, sessionID, stream, userID, PresenceMeta{})

	// Start second monitor
	monitor2Done := make(chan struct{})
	ctx2Canceled := atomic.Bool{}
	go func() {
		oldMonitorBuggy(
			ctx2,
			func() bool { return tracker.hasPresence(sessionID, stream, userID) },
			func() {
				ctx2Canceled.Store(true)
				cancel2()
			},
			func() {
				tracker.UntrackLocalByModes(sessionID, map[uint8]struct{}{StreamModeMatchmaking: {}}, PresenceStream{})
			},
		)
		close(monitor2Done)
	}()

	// Cancel the first context - this triggers the defer which removes ALL matchmaking presences
	cancel1()
	<-monitor1Done

	// Give time for the second monitor to detect the (wrongly removed) presence
	time.Sleep(300 * time.Millisecond)

	// BUG DEMONSTRATION: The second context gets canceled because the first monitor's
	// defer removed its presence!
	// In the FIXED version, this should NOT happen.
	if ctx2Canceled.Load() {
		t.Log("BUG CONFIRMED: Second matchmaking context was incorrectly canceled due to first monitor's cleanup")
	} else {
		t.Log("Note: Race condition did not manifest in this run (timing dependent)")
	}

	cancel2()
	<-monitor2Done
}
