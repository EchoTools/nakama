package server

import (
	"context"
	"errors"
	"testing"

	"github.com/heroiclabs/nakama-common/api"
	"github.com/heroiclabs/nakama-common/runtime"
	"github.com/stretchr/testify/require"
)

// completionTrackFailModule reads normally but fails every write. The
// early-quit config row is pre-seeded so it still LOADS (that read must succeed
// or incrementCompletedMatches bails before reaching the code under test);
// TrackMatchCompletion's history write then fails, which is exactly the shape of
// the failure the finding is about — the player never gets credited for the
// match they stayed through.
//
// Failing the config write too keeps the test off the session-registry branch,
// which needs a live registry this test has no reason to build.
type completionTrackFailModule struct {
	*occTestNakamaModule
}

func newCompletionTrackFailModule() *completionTrackFailModule {
	return &completionTrackFailModule{occTestNakamaModule: newOCCTestNakamaModule()}
}

func (m *completionTrackFailModule) StorageWrite(ctx context.Context, writes []*runtime.StorageWrite) ([]*api.StorageObjectAck, error) {
	return nil, errors.New("simulated storage write failure")
}

func (m *completionTrackFailModule) StorageDelete(ctx context.Context, deletes []*runtime.StorageDelete) error {
	return nil
}

// TestIncrementCompletedMatches_TrackFailureLogsAboveDebug pins the log level of
// the TrackMatchCompletion failure in the post-match stats path.
//
// This is the failure that costs the player match credit: without the history
// write, IncrementCompletedMatches is never called and the completion is lost.
// It was logged at Debug while the ADJACENT, less consequential eqconfig write
// failure logged at Warn — so at production log levels the expensive failure was
// invisible and the cheap one was not.
func TestIncrementCompletedMatches_TrackFailureLogsAboveDebug(t *testing.T) {
	ctx := context.Background()
	logger := newCaptureLogger()
	nk := newCompletionTrackFailModule()

	const userID = "55555555-5555-4555-8555-555555555555"
	nk.seedObject(userID, StorageCollectionEarlyQuit, StorageKeyEarlyQuit, `{}`)

	s := &EventRemoteLogSet{}
	matchID := MatchID{}

	err := s.incrementCompletedMatches(ctx, logger, nk, nil, nil,
		userID, "66666666-6666-4666-8666-666666666666", matchID)
	require.NoError(t, err, "the completion-tracking failure is non-fatal; only its visibility is at issue")

	// Precondition: the config row loaded, so the block under test really ran.
	_, loadFailed := logger.find("warn", "Failed to load early quitter config")
	require.False(t, loadFailed, "precondition: the early quit config row must load")

	const msg = "Failed to track match completion in history"
	_, atDebug := logger.find("debug", msg)
	_, atWarn := logger.find("warn", msg)

	require.False(t, atDebug,
		"the failure that costs the player match credit must not be hidden at Debug")
	require.True(t, atWarn,
		"it must be at least Warn, matching the adjacent eqconfig write failure")
}
