package server

import (
	"context"
	"testing"
	"time"

	"github.com/gofrs/uuid/v5"
	"github.com/heroiclabs/nakama-common/api"
	"github.com/heroiclabs/nakama-common/runtime"
)

// completionTestNK adds two things the shared in-memory double does not model,
// both of which the completion-credit paths depend on:
//
//  1. ATOMICITY. Real Nakama runs a multi-object StorageWrite inside one
//     database transaction (StorageWriteObjects wraps storageWriteObjects in
//     ExecuteInTxPgx), so a version rejection on ANY object rolls the whole
//     batch back. The shared double applies writes one at a time and returns on
//     the first conflict, which would let a partially-applied batch through.
//     This wrapper validates every write up front and delegates only when the
//     entire batch is admissible.
//
//  2. FAULT INJECTION. beforeWrite runs just before a batch is admitted, so a
//     test can deterministically stage the "someone else got there first"
//     interleaving without goroutines: it can seed a row, or reject the batch.
type completionTestNK struct {
	*evrTestNakamaModule

	// beforeWrite is called with each batch before it is validated. Returning
	// a non-nil error rejects the whole batch, applying nothing.
	beforeWrite func(writes []*runtime.StorageWrite) error
}

func newCompletionTestNK() *completionTestNK {
	return &completionTestNK{evrTestNakamaModule: newEvrTestNakamaModule()}
}

func (m *completionTestNK) StorageWrite(ctx context.Context, writes []*runtime.StorageWrite) ([]*api.StorageObjectAck, error) {
	if m.beforeWrite != nil {
		if err := m.beforeWrite(writes); err != nil {
			return nil, err
		}
	}

	// Validate the whole batch before any of it is applied.
	m.mu.Lock()
	for _, w := range writes {
		existing, ok := m.objects[occStorageKey(w.UserID, w.Collection, w.Key)]
		switch w.Version {
		case "":
			// Unconditional.
		case "*":
			if ok {
				m.mu.Unlock()
				return nil, runtime.ErrStorageRejectedVersion
			}
		default:
			if !ok || existing.Version != w.Version {
				m.mu.Unlock()
				return nil, runtime.ErrStorageRejectedVersion
			}
		}
	}
	m.mu.Unlock()

	return m.evrTestNakamaModule.StorageWrite(ctx, writes)
}

// MultiUpdate is the entry point StorableWriteMany actually calls. The atomic
// completion path carries storage writes only, so it routes through the same
// whole-batch validation and fault injection as StorageWrite above.
func (m *completionTestNK) MultiUpdate(ctx context.Context, accountUpdates []*runtime.AccountUpdate, storageWrites []*runtime.StorageWrite, storageDeletes []*runtime.StorageDelete, walletUpdates []*runtime.WalletUpdate, updateLedger bool) ([]*api.StorageObjectAck, []*runtime.WalletUpdateResult, error) {
	acks, err := m.StorageWrite(ctx, storageWrites)
	return acks, nil, err
}

// storedCompletionState reads back the player's counter and history.
func storedCompletionState(t *testing.T, ctx context.Context, nk runtime.NakamaModule, userID string) (*EarlyQuitPlayerState, *EarlyQuitHistory) {
	t.Helper()
	state := NewEarlyQuitPlayerState()
	if err := StorableRead(ctx, nk, userID, state, false); err != nil {
		t.Fatalf("read early quit state: %v", err)
	}
	history := NewEarlyQuitHistory(userID)
	if err := StorableRead(ctx, nk, userID, history, false); err != nil {
		// No history row at all is a legitimate outcome for some cases.
		history = NewEarlyQuitHistory(userID)
	}
	return state, history
}

func countCompletionsFor(h *EarlyQuitHistory, matchID MatchID) int {
	n := 0
	for _, r := range h.Completions {
		if r.MatchID.Equals(matchID) {
			n++
		}
	}
	return n
}

// TestMatchCompletion_CreditSurvivesARejectedCounterWrite pins the ordering
// between the dedupe record and the credit it guards.
//
// A completed match is reported twice — once by the MatchLoop MatchOver
// dispatch and once by the post-match stats upload — and the completion history
// is the dedupe record that makes only the first report count. That record was
// committed BEFORE the counter write, and the counter write's failure was only
// Warn-logged. So when the counter write lost an optimistic-concurrency race
// (another writer touched the player's early quit row in between — a quit, the
// tier scheduler, the other reporter) the credit was dropped, and because the
// dedupe record was already committed the other reporter returned first=false.
// The completion was recorded as counted and never counted: credit gone for
// good, with no error surfaced to anyone.
//
// Correct behaviour: the dedupe record and the credit are committed together or
// not at all. A rejected write leaves nothing behind, so the other reporter
// still sees an uncounted match and credits it.
func TestMatchCompletion_CreditSurvivesARejectedCounterWrite(t *testing.T) {
	logger := NewRuntimeGoLogger(loggerForTest(t))
	nk := newCompletionTestNK()
	ctx := context.WithValue(context.Background(), runtime.RUNTIME_CTX_NODE, "test-node")

	userID := uuid.Must(uuid.NewV4()).String()
	matchID, err := NewMatchID(uuid.Must(uuid.NewV4()), "test-node")
	if err != nil {
		t.Fatalf("new match id: %v", err)
	}

	// One concurrent writer wins the race against the counter row, exactly
	// once. Every later write goes through.
	rejected := false
	nk.beforeWrite = func(writes []*runtime.StorageWrite) error {
		if rejected {
			return nil
		}
		for _, w := range writes {
			if w.Collection == StorageCollectionEarlyQuit && w.Key == StorageKeyEarlyQuit && w.UserID == userID && w.Version != "" {
				rejected = true
				return runtime.ErrStorageRejectedVersion
			}
		}
		return nil
	}

	s := &EventRemoteLogSet{}

	// Reporter 1: the counter write is rejected out from under it.
	if err := s.incrementCompletedMatches(ctx, logger, nk, nil, &testSessionRegistry{}, userID, "", matchID); err != nil {
		t.Fatalf("first report: %v", err)
	}
	if !rejected {
		t.Fatal("precondition: the injected rejection never fired; the counter write was not attempted")
	}

	// Reporter 2: the other production reporter for the same match.
	if err := s.incrementCompletedMatches(ctx, logger, nk, nil, &testSessionRegistry{}, userID, "", matchID); err != nil {
		t.Fatalf("second report: %v", err)
	}

	state, history := storedCompletionState(t, ctx, nk, userID)
	if state.TotalCompletedMatches != 1 {
		t.Errorf("TotalCompletedMatches = %d, want 1: the completion was recorded but never credited",
			state.TotalCompletedMatches)
	}
	if got := countCompletionsFor(history, matchID); got != 1 {
		t.Errorf("completion records for the match = %d, want 1", got)
	}
}

// TestMatchCompletion_FirstEverCompletionDoesNotClobberAConcurrentOne pins the
// create path of the completion history.
//
// TrackMatchCompletion read the history with create=false and, on NotFound,
// carried on with a fresh history whose storage version was still "" —
// StorableRead never calls SetStorageMeta on its error paths. An empty version
// is an UNCONDITIONAL write. So the first-ever completion for a player did not
// merely fail to notice a concurrently created history: it overwrote it,
// destroying the completion record already committed there. The destroyed
// record is the dedupe marker for its own match, so that match's other reporter
// then sees an uncounted match and credits it a second time.
//
// (Note: the write must be conditional here without relying on StorableRead
// itself learning to honour "*" — that is a separate, concurrent fix.)
//
// Correct behaviour: creating the history is conditional on it not existing.
// A loser of that race writes nothing and credits nothing, leaving the match
// available to be credited on a later report.
func TestMatchCompletion_FirstEverCompletionDoesNotClobberAConcurrentOne(t *testing.T) {
	logger := NewRuntimeGoLogger(loggerForTest(t))
	nk := newCompletionTestNK()
	ctx := context.WithValue(context.Background(), runtime.RUNTIME_CTX_NODE, "test-node")

	userID := uuid.Must(uuid.NewV4()).String()
	mine, err := NewMatchID(uuid.Must(uuid.NewV4()), "test-node")
	if err != nil {
		t.Fatalf("new match id: %v", err)
	}
	theirs, err := NewMatchID(uuid.Must(uuid.NewV4()), "test-node")
	if err != nil {
		t.Fatalf("new match id: %v", err)
	}

	s := &EventRemoteLogSet{}

	// A concurrent reporter for a DIFFERENT match of the same player creates
	// the history row first, in the window between our reporter's read (which
	// found nothing) and its write. Staged from the write hook so the
	// interleaving is identical on every run.
	raced := false
	nk.beforeWrite = func(writes []*runtime.StorageWrite) error {
		if raced {
			return nil
		}
		for _, w := range writes {
			if w.Collection == StorageCollectionEarlyQuitHistory && w.UserID == userID {
				raced = true
				other := NewEarlyQuitHistory(userID)
				other.AddCompletion(theirs, time.Now().UTC())
				if err := StorableWrite(ctx, nk.evrTestNakamaModule, userID, other); err != nil {
					t.Errorf("stage concurrent history: %v", err)
				}
				break
			}
		}
		return nil
	}

	if err := s.incrementCompletedMatches(ctx, logger, nk, nil, &testSessionRegistry{}, userID, "", mine); err != nil {
		t.Fatalf("report: %v", err)
	}
	if !raced {
		t.Fatal("precondition: the history write was never attempted, so nothing was raced")
	}
	nk.beforeWrite = nil

	_, history := storedCompletionState(t, ctx, nk, userID)
	if got := countCompletionsFor(history, theirs); got != 1 {
		t.Errorf("the concurrently created completion record was destroyed: found %d records for it, want 1", got)
	}

	// The loser credited nothing, so its match is still creditable: the other
	// reporter for it must be able to record and credit it.
	if err := s.incrementCompletedMatches(ctx, logger, nk, nil, &testSessionRegistry{}, userID, "", mine); err != nil {
		t.Fatalf("re-report: %v", err)
	}

	state, history := storedCompletionState(t, ctx, nk, userID)
	if got := countCompletionsFor(history, mine); got != 1 {
		t.Errorf("completion records for the re-reported match = %d, want 1", got)
	}
	if got := countCompletionsFor(history, theirs); got != 1 {
		t.Errorf("completion records for the concurrent match = %d, want 1", got)
	}
	if state.TotalCompletedMatches != 1 {
		t.Errorf("TotalCompletedMatches = %d, want 1 (only our own match was credited by this reporter)",
			state.TotalCompletedMatches)
	}
}

// TestMatchCompletion_CorruptHistoryRowStillCreditsTheCompletion pins the
// interaction between the completion history's version staging and the atomic
// batch that now commits the history together with the credit.
//
// The history row is read with create=false, so a row that EXISTS but does not
// unmarshal comes back as codes.Internal — not NotFound. Staging that read
// failure as a create-only write ("*") makes the batch fail against the row
// that is still sitting there, and because the credit is in the same
// transaction the credit is rolled back with it. Nothing on this path ever
// repairs the row, so the player never gains completion credit or recovers
// tier again — every completed match, forever.
//
// Correct behaviour: only a genuinely absent row is staged create-only. A row
// that could not be read is overwritten unconditionally, which is what this
// path did before the batch existed and is how the quit-record path recovers
// too.
func TestMatchCompletion_CorruptHistoryRowStillCreditsTheCompletion(t *testing.T) {
	logger := NewRuntimeGoLogger(loggerForTest(t))
	nk := newCompletionTestNK()
	ctx := context.WithValue(context.Background(), runtime.RUNTIME_CTX_NODE, "test-node")

	userID := uuid.Must(uuid.NewV4()).String()
	matchID, err := NewMatchID(uuid.Must(uuid.NewV4()), "test-node")
	if err != nil {
		t.Fatalf("new match id: %v", err)
	}

	// The row exists and is unreadable.
	nk.seedObject(userID, StorageCollectionEarlyQuitHistory, StorageKeyEarlyQuitHistory, "{ not json")

	s := &EventRemoteLogSet{}

	// Report the same match three times: the first must credit it, the rest
	// must dedupe against the now-repaired history.
	for i := 0; i < 3; i++ {
		if err := s.incrementCompletedMatches(ctx, logger, nk, nil, &testSessionRegistry{}, userID, "", matchID); err != nil {
			t.Fatalf("report %d: %v", i+1, err)
		}
	}

	state, history := storedCompletionState(t, ctx, nk, userID)
	if state.TotalCompletedMatches != 1 {
		t.Errorf("TotalCompletedMatches = %d, want 1: an unreadable history row rolled back the credit", state.TotalCompletedMatches)
	}
	if got := countCompletionsFor(history, matchID); got != 1 {
		t.Errorf("completion records for the match = %d, want 1 (the corrupt row was not repaired)", got)
	}
}
