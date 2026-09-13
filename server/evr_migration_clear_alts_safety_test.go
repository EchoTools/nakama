package server

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/heroiclabs/nakama-common/api"
	"github.com/heroiclabs/nakama-common/runtime"
	"github.com/heroiclabs/nakama/v3/server/evr"
)

// seedIgnoredOnlyAccount stores a login history for the routine shape the
// erasure defect destroys: an account whose EVERY discovery item is an ignored
// value, so AltSearchPatterns returns nil and no search can be performed for it
// at all.
//
// A Quest player behind CGNAT is exactly this. The three items
// AltSearchPatterns draws on (server/evr_authenticate_alts.go:87-116) are the
// client IP, the HMD serial and the XPID token, and here all three are in the
// ignored set:
//
//   - 10.0.0.5 is RFC1918, dropped by ip.IsPrivate()
//     (matchIgnoredAltPattern, server/evr_authenticate_history.go:54-57)
//   - VRLINKHMDQUEST3 is a Meta-issued placeholder serial, a literal entry in
//     IgnoredLoginValues (:41-44)
//   - a zero EvrId renders as "UNK-0" (PlatformCode.Abbrevation default),
//     also a literal IgnoredLoginValues entry (:35)
//
// The account nevertheless carries a genuine stored alt link, formed on a
// public IP that its current History window no longer contains. That link is
// the thing at risk.
func seedIgnoredOnlyAccount(t *testing.T, m *altClearTestModule, userID, linkedUserID string) string {
	t.Helper()

	h := NewLoginHistory(userID)
	h.History = map[string]*LoginHistoryEntry{
		"entry": {
			CreatedAt: time.Now().Add(-24 * time.Hour),
			UpdatedAt: time.Now().Add(-time.Hour),
			ClientIP:  "10.0.0.5",
			LoginData: &evr.LoginProfile{HMDSerialNumber: "VRLINKHMDQUEST3"},
		},
	}
	h.AlternateMatches = map[string][]*AlternateSearchMatch{
		linkedUserID: {{OtherUserID: linkedUserID, Items: []string{"198.51.100.22"}}},
	}
	h.SecondDegreeAlternates = []string{linkedUserID}

	data, err := json.Marshal(h)
	if err != nil {
		t.Fatalf("marshal seed history for %s: %v", userID, err)
	}

	// Guard the fixture in the direction this file cares about: the patterns
	// must be EMPTY. If they are not, the migration performs a real search and
	// the assertions below stop describing the no-patterns path.
	if got := h.AltSearchPatterns(); len(got) != 0 {
		t.Fatalf("fixture is not the no-patterns case: AltSearchPatterns() = %v, want empty", got)
	}

	version := m.seedObject(userID, LoginStorageCollection, LoginHistoryStorageKey, string(data))
	m.listed = append(m.listed, &api.StorageObject{
		Collection: LoginStorageCollection,
		Key:        LoginHistoryStorageKey,
		UserId:     userID,
		Value:      string(data),
		Version:    version,
	})
	return string(data)
}

// TestClearAltsMigration_NoPatternsAccountKeepsItsLinks is the silent-erasure
// gate.
//
// Phase 2 clears AlternateMatches in memory and then restores them from a
// search. When AltSearchPatterns filters down to nothing it returns nil
// (server/evr_authenticate_alts.go:112-114), LoginAlternateSearch returns
// (nil, nil, nil) with NO error (:120-122), and UpdateAlternates takes its
// len(matches) == 0 early return (server/evr_authenticate_history.go:488-490)
// which returns (false, nil) and does not touch the maps it never populated.
//
// Every one of those is a success as far as the caller can see, so the
// migration's rebuild-failed branch never fires and the cleared state is
// marshalled and persisted. The account comes out of a "refresh" with its alt
// links erased, and nothing is logged.
//
// There is no evidence available to rebuild this account's links from. The only
// correct action is to leave the row alone.
func TestClearAltsMigration_NoPatternsAccountKeepsItsLinks(t *testing.T) {
	const (
		userID = "44444444-4444-4444-4444-444444444444"
		linked = "55555555-5555-5555-5555-555555555555"
	)

	nk := newAltClearTestModule()
	before := seedIgnoredOnlyAccount(t, nk, userID, linked)

	logger := runAltClearMigration(t, nk)

	// FAIL LOUD: a skipped account must be visible in the migration's own
	// numbers. "Left it alone" and "never saw it" must not read the same.
	if got := completionField(t, logger, "walked"); got != 1 {
		t.Errorf("walked = %d, want 1: the migration did not examine the seeded account", got)
	}
	if got := completionField(t, logger, "unsearchable"); got != 1 {
		t.Errorf("unsearchable = %d, want 1: an account that cannot be searched must be counted, not absorbed", got)
	}

	stored := nk.storedHistory(t, userID)
	if len(stored.AlternateMatches) == 0 {
		t.Errorf("AlternateMatches was erased. No search was performed for this account -- AltSearchPatterns is empty -- so the clear was persisted on the strength of a search that never ran.")
	}
	if len(stored.SecondDegreeAlternates) == 0 {
		t.Errorf("SecondDegreeAlternates was erased on the strength of a search that never ran")
	}
	if got := nk.storedValue(t, userID); got != before {
		t.Errorf("the row was rewritten:\n before: %s\n after:  %s\nan account that cannot be searched must be left untouched", before, got)
	}
}

// runAltClearMigrationExpectingError runs the migration and requires it to
// fail. The logger comes back alongside the error so callers can assert on what
// the migration said on the way out.
func runAltClearMigrationExpectingError(t *testing.T, nk runtime.NakamaModule) (*captureLogger, error) {
	t.Helper()
	ensureAltClearPreconditions(t)
	logger := newCaptureLogger()
	m := &MigrationClearAlternateMatches{}
	err := m.MigrateSystem(context.Background(), logger, nil, nk)
	if err == nil {
		t.Fatal("MigrateSystem returned nil; want an error")
	}
	return logger, err
}

// TestClearAltsMigration_SafetyFloorAbortsMassErasure is the floor gate.
//
// The index answers "no alternates" for every account. From inside the run that
// is indistinguishable from an index that is unavailable, a CGNAT config that
// has filtered every strong signal out of the discovery keys (#589, one
// character of config), or an ASN dataset that never loaded. In all of those the
// migration clears every link it examines and rebuilds none of them.
//
// Completing that run is not acceptable. The seeded population is over
// migrationClearAltsFloorMinLinks, so the ratio is meaningful, and 100%
// destruction is over migrationClearAltsFloorFraction. The run must abort
// before the page is written, and it must say so at ERROR -- a migration that
// destroys every link and then reports "complete" is precisely the fail-open on
// a fail-closed control that AGENTS.md names as this repo's dominant
// anti-pattern.
func TestClearAltsMigration_SafetyFloorAbortsMassErasure(t *testing.T) {
	const linkTarget = "99999999-9999-9999-9999-999999999999"

	nk := newAltClearTestModule()
	userIDs := make([]string, 0, migrationClearAltsFloorMinLinks+20)
	for i := 0; i < migrationClearAltsFloorMinLinks+20; i++ {
		userID := migrationTestUserID(i + 1)
		nk.seedLinkedAccount(t, userID, linkTarget)
		userIDs = append(userIDs, userID)
	}

	logger, err := runAltClearMigrationExpectingError(t, nk)

	if !strings.Contains(err.Error(), "safety floor") {
		t.Errorf("error = %q, want it to name the safety floor", err)
	}
	if _, ok := logger.find("error", "alt-clear migration: safety floor tripped; aborting the run without writing this page"); !ok {
		t.Error("the abort was not logged at ERROR; a run that destroys every link must not fail quietly")
	}
	if _, ok := logger.find("info", "alt-clear migration complete"); ok {
		t.Error("the migration logged its completion line after tripping the floor")
	}

	// Nothing may have been written. The floor is checked before the page's
	// batch is submitted, so every seeded link must still be there.
	for _, userID := range userIDs {
		if got := nk.storedHistory(t, userID).AlternateMatches; len(got) == 0 {
			t.Fatalf("%s was cleared before the floor stopped the run; the check must precede the batch write", userID)
		}
	}
}

// TestClearAltsMigration_SafetyFloorIgnoresSmallSamples is the negative control
// for the floor: below migrationClearAltsFloorMinLinks the ratio is one page's
// worth of noise and must not abort anything.
//
// Without it, a floor that tripped on any destruction at all would leave the
// test above passing while aborting every legitimate small run.
func TestClearAltsMigration_SafetyFloorIgnoresSmallSamples(t *testing.T) {
	const linkTarget = "99999999-9999-9999-9999-999999999999"

	nk := newAltClearTestModule()
	for i := 0; i < 3; i++ {
		nk.seedLinkedAccount(t, migrationTestUserID(i+1), linkTarget)
	}

	logger := runAltClearMigration(t, nk)

	if got := completionField(t, logger, "cleared"); got != 3 {
		t.Errorf("cleared = %d, want 3: a 3-link run is below the floor's minimum sample and must complete", got)
	}
}
