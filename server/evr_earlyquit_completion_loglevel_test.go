package server

import (
	"context"
	"errors"
	"fmt"
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

// MultiUpdate must fail too: StorableWriteMany writes the batch through this
// entry point, and inheriting the base double's MultiUpdate would route around
// the failing StorageWrite above and let the write appear to succeed.
func (m *completionTrackFailModule) MultiUpdate(ctx context.Context, accountUpdates []*runtime.AccountUpdate, storageWrites []*runtime.StorageWrite, storageDeletes []*runtime.StorageDelete, walletUpdates []*runtime.WalletUpdate, updateLedger bool) ([]*api.StorageObjectAck, []*runtime.WalletUpdateResult, error) {
	acks, err := m.StorageWrite(ctx, storageWrites)
	return acks, nil, err
}

// TestIncrementCompletedMatches_TrackFailureLogsAboveDebug pins the visibility of
// the failure that costs the player match credit in the post-match stats path.
//
// Originally this asserted on "Failed to track match completion in history": the
// history write was a SEPARATE write that ran before the counter, it was logged
// at Debug, and the ADJACENT, less consequential eqconfig write failure logged at
// Warn — so at production log levels the expensive failure was invisible and the
// cheap one was not.
//
// That asymmetry no longer has anywhere to live. The history record and the
// credit are now committed in ONE atomic StorableWriteMany, so there is exactly
// one write that can cost the player the credit, and exactly one failure to
// report. The invariant this test exists to defend is unchanged and is what it
// still asserts: that failure must not be hidden below Warn. Only the message it
// arrives under moved, because the two writes it used to compare became one.
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

	// The atomic write of {counter, history record} failed, so the player was not
	// credited. That must be visible at Warn.
	const msg = "Failed to store early quitter config"
	_, atDebug := logger.find("debug", msg)
	entry, atWarn := logger.find("warn", msg)

	require.False(t, atDebug,
		"the failure that costs the player match credit must not be hidden at Debug")
	require.True(t, atWarn,
		"the failure that costs the player match credit must be at least Warn")

	// It must also still be diagnosable: a batch failure that named only one of
	// the two rows would send whoever reads the log at the wrong record.
	require.Contains(t, fmt.Sprint(entry.fields["error"]), StorageCollectionEarlyQuitHistory,
		"the batch failure must name the history row, not just the counter row")
}
