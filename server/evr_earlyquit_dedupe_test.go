package server

import (
	"context"
	"testing"

	"github.com/gofrs/uuid/v5"
	"github.com/heroiclabs/nakama-common/runtime"
)

// TestTrackMatchCompletion_DedupeSameMatchID verifies that completing the same
// (user, match) twice — as happens when the post-match stats upload path
// (incrementCompletedMatches) and the MatchLoop MatchOver path both fire for one
// match — credits the completion only once.
//
// Both production paths increment TotalCompletedMatches AND append a
// CompletionRecord to the player's early quit history. The second call for the
// same match ID must be a no-op: no extra counter increment, no duplicate
// history record.
func TestTrackMatchCompletion_DedupeSameMatchID(t *testing.T) {
	// --- setup: in-memory storage module, no database required ---
	consoleLogger := loggerForTest(t)
	logger := NewRuntimeGoLogger(consoleLogger)
	nk := newEvrTestNakamaModule()

	ctx := context.WithValue(context.Background(), runtime.RUNTIME_CTX_NODE, "test-node")

	userID := uuid.Must(uuid.NewV4())
	uid := userID.String()

	matchID, err := NewMatchID(uuid.Must(uuid.NewV4()), "test-node")
	if err != nil {
		t.Fatalf("failed to create match ID: %v", err)
	}

	// First completion for (user, match): credits once.
	s := &EventRemoteLogSet{}
	if err := s.incrementCompletedMatches(ctx, logger, nk, nil, &testSessionRegistry{}, uid, "", matchID); err != nil {
		t.Fatalf("first incrementCompletedMatches failed: %v", err)
	}

	// Second completion for the same (user, match) — the duplicate path.
	// Must NOT credit again.
	if err := s.incrementCompletedMatches(ctx, logger, nk, nil, &testSessionRegistry{}, uid, "", matchID); err != nil {
		t.Fatalf("second incrementCompletedMatches failed: %v", err)
	}

	// The counter must reflect exactly one completed match.
	eqconfig := NewEarlyQuitPlayerState()
	if err := StorableRead(ctx, nk, uid, eqconfig, false); err != nil {
		t.Fatalf("failed to read back early quit state: %v", err)
	}
	if eqconfig.TotalCompletedMatches != 1 {
		t.Errorf("expected TotalCompletedMatches to stay 1 after duplicate completion, got %d", eqconfig.TotalCompletedMatches)
	}

	// The history must contain exactly one completion record for the match.
	history := NewEarlyQuitHistory(uid)
	if err := StorableRead(ctx, nk, uid, history, false); err != nil {
		t.Fatalf("failed to read back early quit history: %v", err)
	}
	if len(history.Completions) != 1 {
		t.Errorf("expected exactly 1 completion record after duplicate completion, got %d", len(history.Completions))
	}
}
