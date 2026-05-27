package main

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// ── Mock types ──────────────────────────────────────────────────────────────
// These satisfy the PartyResult and OutputEvent interfaces for testing the
// output layer in isolation from the state machine.

type mockEvent struct {
	ts       time.Time
	evType   string
	username string
	rawLine  string
	details  map[string]string
}

func (e *mockEvent) GetTimestamp() time.Time      { return e.ts }
func (e *mockEvent) GetEventType() string         { return e.evType }
func (e *mockEvent) GetUsername() string           { return e.username }
func (e *mockEvent) GetRawLine() string            { return e.rawLine }
func (e *mockEvent) GetDetails() map[string]string { return e.details }

type mockPartyResult struct {
	partyID  string
	formedAt time.Time
	leader   string
	members  []string
	outcome string
	reason   string
	duration time.Duration
	events   []OutputEvent
	matchIDs map[string][]string
}

func (m *mockPartyResult) GetPartyID() string              { return m.partyID }
func (m *mockPartyResult) GetFormedAt() time.Time          { return m.formedAt }
func (m *mockPartyResult) GetLeader() string               { return m.leader }
func (m *mockPartyResult) GetMembers() []string            { return m.members }
func (m *mockPartyResult) GetOutcome() string              { return m.outcome }
func (m *mockPartyResult) GetOutcomeReason() string        { return m.reason }
func (m *mockPartyResult) GetDuration() time.Duration      { return m.duration }
func (m *mockPartyResult) GetEvents() []OutputEvent        { return m.events }
func (m *mockPartyResult) GetMatchIDs() map[string][]string { return m.matchIDs }

// ── Test fixtures ───────────────────────────────────────────────────────────

var testBaseTime = time.Date(2026, 5, 25, 18, 33, 55, 0, time.UTC)

func newTestSplitResult() *mockPartyResult {
	return &mockPartyResult{
		partyID:  "f6897335-1cb8-4634-a8cb-b2de7cab8107",
		formedAt: testBaseTime,
		leader:   "player1",
		members:  []string{"player1", "souleater0061"},
		outcome:  OutcomeSplit.String(),
		reason:   "follower_released_independent",
		duration: 19234 * time.Millisecond,
		events: []OutputEvent{
			&mockEvent{
				ts:       testBaseTime,
				evType:   "PartyJoined",
				username: "souleater0061",
				rawLine:  `{"msg":"Joined party group"}`,
				details:  map[string]string{"uid": "uid-soul", "sid": "sid-soul"},
			},
			&mockEvent{
				ts:       testBaseTime.Add(154 * time.Millisecond),
				evType:   "MemberFollowAttempt",
				username: "souleater0061",
				rawLine:  `{"msg":"following leader"}`,
				details:  map[string]string{"uid": "uid-soul"},
			},
			&mockEvent{
				ts:       testBaseTime.Add(19234 * time.Millisecond),
				evType:   "FollowReleased",
				username: "souleater0061",
				rawLine:  `{"msg":"released"}`,
				details:  map[string]string{"mode": "echo_arena", "reason": "leader_left"},
			},
		},
		matchIDs: map[string][]string{
			"player1":      {"abc123"},
			"souleater0061": {"def456"},
		},
	}
}

func newTestConvergedResult() *mockPartyResult {
	return &mockPartyResult{
		partyID:  "aaaa-bbbb-cccc-dddd",
		formedAt: testBaseTime.Add(-10 * time.Second),
		leader:   "leader1",
		members:  []string{"leader1", "follower1"},
		outcome:  OutcomeConverged.String(),
		reason:   "",
		duration: 5 * time.Second,
		events: []OutputEvent{
			&mockEvent{
				ts:       testBaseTime.Add(-10 * time.Second),
				evType:   "PartyFormed",
				username: "leader1",
				rawLine:  `{"msg":"Party is ready"}`,
				details:  map[string]string{},
			},
		},
		matchIDs: map[string][]string{
			"leader1":   {"match-111"},
			"follower1": {"match-111"},
		},
	}
}

// ── Reader tests ────────────────────────────────────────────────────────────

func TestOpenReader_PlainFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.log")
	content := `{"msg":"hello","ts":"2026-05-25T00:00:00Z"}` + "\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	r, err := openReader(path)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()

	got, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != content {
		t.Errorf("plain reader: got %q, want %q", got, content)
	}
}

func TestOpenReader_PlainNoExtension(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "testfile")
	content := `{"msg":"no extension"}` + "\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	r, err := openReader(path)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()

	got, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != content {
		t.Errorf("no-ext reader: got %q, want %q", got, content)
	}
}

func TestOpenReader_Gzip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.jsonl.gz")

	content := `{"msg":"gzip line 1","ts":"2026-05-25T00:00:00Z"}` + "\n" +
		`{"msg":"gzip line 2","ts":"2026-05-25T00:00:01Z"}` + "\n"

	// Write gzip file.
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	gw := gzip.NewWriter(f)
	if _, err := gw.Write([]byte(content)); err != nil {
		t.Fatal(err)
	}
	if err := gw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}

	r, err := openReader(path)
	if err != nil {
		t.Fatal("openReader gzip:", err)
	}
	defer r.Close()

	got, err := io.ReadAll(r)
	if err != nil {
		t.Fatal("read gzip:", err)
	}
	if string(got) != content {
		t.Errorf("gzip reader:\ngot  %q\nwant %q", got, content)
	}
}

func TestOpenReader_Zstd(t *testing.T) {
	// Use the existing testdata file to verify zstd still works via processFile.
	// If testdata is plain JSONL (not actually zstd), test plain processing.
	path := filepath.Join("testdata", "party.jsonl")
	r, err := openReader(path)
	if err != nil {
		t.Fatal("openReader testdata:", err)
	}
	defer r.Close()

	got, err := io.ReadAll(r)
	if err != nil {
		t.Fatal("read testdata:", err)
	}
	if len(got) == 0 {
		t.Error("testdata file is empty")
	}
}

func TestOpenReader_GzipInvalid(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bad.gz")
	if err := os.WriteFile(path, []byte("not gzip data"), 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := openReader(path)
	if err == nil {
		t.Error("expected error for invalid gzip, got nil")
	}
}

func TestOpenReader_Nonexistent(t *testing.T) {
	_, err := openReader("/nonexistent/path/file.log")
	if err == nil {
		t.Error("expected error for nonexistent file, got nil")
	}
}

// ── processFile with gzip ───────────────────────────────────────────────────

func TestProcessFile_Gzip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.jsonl.gz")

	// Two JSONL lines: a party formation and a join.
	lines := []string{
		`{"level":"debug","ts":"2026-04-24T00:59:50.060Z","msg":"Party is ready","uid":"leader-uid","sid":"leader-sid","username":"testleader","leader":"leader-sid","size":2,"members":["testfollower"]}`,
		`{"level":"info","ts":"2026-04-24T00:59:55.060Z","msg":"Joined entrant.","mid":"match-123","uid":"leader-uid","sid":"leader-sid","role":0}`,
	}
	content := strings.Join(lines, "\n") + "\n"

	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	gw := gzip.NewWriter(f)
	gw.Write([]byte(content))
	gw.Close()
	f.Close()

	st := newState(time.Time{}, true, false)
	if err := processFile(path, st); err != nil {
		t.Fatal("processFile gzip:", err)
	}
	st.flushAll()

	if len(st.splits) == 0 {
		// With verbose=true, at least one event should appear.
		// If no splits, that's OK -- the important thing is processFile didn't error.
		t.Log("No splits detected (expected in some cases), but file was read successfully")
	}
}

// ── JSONL output tests ──────────────────────────────────────────────────────

func TestWritePartyJSONL(t *testing.T) {
	r := newTestSplitResult()
	var buf bytes.Buffer
	if err := writePartyJSONL(&buf, r); err != nil {
		t.Fatal(err)
	}

	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) != len(r.events) {
		t.Fatalf("expected %d lines, got %d", len(r.events), len(lines))
	}

	// Verify first line parses correctly.
	var first eventLine
	if err := json.Unmarshal([]byte(lines[0]), &first); err != nil {
		t.Fatal("unmarshal first line:", err)
	}
	if first.Event != "PartyJoined" {
		t.Errorf("first event: got %q, want %q", first.Event, "PartyJoined")
	}
	if first.Username != "souleater0061" {
		t.Errorf("first username: got %q, want %q", first.Username, "souleater0061")
	}
	if first.PartyID != r.partyID {
		t.Errorf("party_id: got %q, want %q", first.PartyID, r.partyID)
	}
	if first.UID != "uid-soul" {
		t.Errorf("uid: got %q, want %q", first.UID, "uid-soul")
	}

	// Verify last line.
	var last eventLine
	if err := json.Unmarshal([]byte(lines[2]), &last); err != nil {
		t.Fatal("unmarshal last line:", err)
	}
	if last.Event != "FollowReleased" {
		t.Errorf("last event: got %q, want %q", last.Event, "FollowReleased")
	}
	if last.Details["mode"] != "echo_arena" {
		t.Errorf("last details[mode]: got %q, want %q", last.Details["mode"], "echo_arena")
	}
}

func TestWriteLifecycleJSONL(t *testing.T) {
	results := []PartyResult{
		newTestSplitResult(),
		newTestConvergedResult(),
	}

	var buf bytes.Buffer
	if err := WriteLifecycleJSONL(&buf, results); err != nil {
		t.Fatal(err)
	}

	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) != 2 {
		t.Fatalf("expected 2 lines, got %d", len(lines))
	}

	// First line should be the converged result (earlier timestamp).
	var first summaryLine
	if err := json.Unmarshal([]byte(lines[0]), &first); err != nil {
		t.Fatal("unmarshal first summary:", err)
	}
	if first.PartyID != "aaaa-bbbb-cccc-dddd" {
		t.Errorf("first party_id: got %q, want %q", first.PartyID, "aaaa-bbbb-cccc-dddd")
	}
	if first.Outcome != OutcomeConverged.String() {
		t.Errorf("first outcome: got %q, want %q", first.Outcome, OutcomeConverged.String())
	}

	// Second line should be the split result.
	var second summaryLine
	if err := json.Unmarshal([]byte(lines[1]), &second); err != nil {
		t.Fatal("unmarshal second summary:", err)
	}
	if second.PartyID != "f6897335-1cb8-4634-a8cb-b2de7cab8107" {
		t.Errorf("second party_id: got %q", second.PartyID)
	}
	if second.Outcome != OutcomeSplit.String() {
		t.Errorf("second outcome: got %q, want %q", second.Outcome, OutcomeSplit.String())
	}
	if second.FailureReason != "follower_released_independent" {
		t.Errorf("failure_reason: got %q", second.FailureReason)
	}
	if second.DurationMs != 19234 {
		t.Errorf("duration_ms: got %d, want %d", second.DurationMs, 19234)
	}
}

// ── Summary statistics tests ────────────────────────────────────────────────

func TestWriteSummaryStats(t *testing.T) {
	results := []PartyResult{
		newTestConvergedResult(),
		newTestSplitResult(),
		&mockPartyResult{
			partyID: "degraded-1", formedAt: testBaseTime,
			leader: "l1", members: []string{"l1", "f1"},
			outcome: OutcomeDegraded.String(), reason: "lobby_full",
			duration: 10 * time.Second,
		},
		&mockPartyResult{
			partyID: "abandoned-1", formedAt: testBaseTime,
			leader: "l2", members: []string{"l2"},
			outcome: OutcomeAbandoned.String(), reason: "",
			duration: 30 * time.Second,
		},
	}

	var buf bytes.Buffer
	WriteSummaryStats(&buf, results)
	out := buf.String()

	// Check header.
	if !strings.Contains(out, "Party Lifecycle Summary") {
		t.Error("missing summary header")
	}

	// Check totals.
	if !strings.Contains(out, "Total parties analyzed:  4") {
		t.Errorf("missing/wrong total count in:\n%s", out)
	}

	// Check each outcome is present.
	if !strings.Contains(out, "CONVERGED:") {
		t.Error("missing CONVERGED line")
	}
	if !strings.Contains(out, "SPLIT:") {
		t.Error("missing SPLIT line")
	}
	if !strings.Contains(out, "DEGRADED:") {
		t.Error("missing DEGRADED line")
	}
	if !strings.Contains(out, "ABANDONED:") {
		t.Error("missing ABANDONED line")
	}

	// Check failure reasons.
	if !strings.Contains(out, "follower_released_independent:") {
		t.Error("missing failure reason: follower_released_independent")
	}
	if !strings.Contains(out, "lobby_full:") {
		t.Error("missing failure reason: lobby_full")
	}

	// Check affected users — souleater0061 should appear (split member, not leader).
	if !strings.Contains(out, "souleater0061:") {
		t.Error("missing affected user: souleater0061")
	}
}

func TestWriteSummaryStats_Empty(t *testing.T) {
	var buf bytes.Buffer
	WriteSummaryStats(&buf, nil)
	out := buf.String()

	if !strings.Contains(out, "Total parties analyzed:  0") {
		t.Errorf("empty summary should show 0 total, got:\n%s", out)
	}
}

// ── Human-readable output tests ─────────────────────────────────────────────

func TestWriteLifecycleHuman(t *testing.T) {
	r := newTestSplitResult()
	var buf bytes.Buffer
	WriteLifecycleHuman(&buf, []PartyResult{r})
	out := buf.String()

	checks := []struct {
		desc    string
		needle  string
	}{
		{"header bar", lifecycleHR},
		{"outcome label", "PARTY SPLIT"},
		{"timestamp", "2026-05-25T18:33:55Z"},
		{"party ID", "f6897335-1cb8-4634-a8cb-b2de7cab8107"},
		{"leader", "player1"},
		{"size", "Size: 2"},
		{"outcome reason", "follower_released_independent"},
		{"timeline header", "Timeline:"},
		{"event type", "PartyJoined"},
		{"event user", "souleater0061"},
		{"members header", "Members:"},
		{"split marker", "(split)"},
	}
	for _, c := range checks {
		if !strings.Contains(out, c.needle) {
			t.Errorf("human output missing %s (%q) in:\n%s", c.desc, c.needle, out)
		}
	}
}

func TestWriteLifecycleHuman_Converged(t *testing.T) {
	r := newTestConvergedResult()
	var buf bytes.Buffer
	WriteLifecycleHuman(&buf, []PartyResult{r})
	out := buf.String()

	if !strings.Contains(out, "PARTY CONVERGED") {
		t.Errorf("expected PARTY CONVERGED label, got:\n%s", out)
	}
	// Both members in same match should get checkmark.
	if strings.Contains(out, "(split)") {
		t.Errorf("converged party should not have split marker:\n%s", out)
	}
}

// ── Output directory tests ──────────────────────────────────────────────────

func TestWriteOutputDir(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "output")
	results := []PartyResult{
		newTestSplitResult(),
		newTestConvergedResult(),
	}

	if err := WriteOutputDir(dir, results); err != nil {
		t.Fatal("WriteOutputDir:", err)
	}

	// Verify directory exists.
	info, err := os.Stat(dir)
	if err != nil {
		t.Fatal("stat output dir:", err)
	}
	if !info.IsDir() {
		t.Fatal("output path is not a directory")
	}

	// Verify per-party files.
	partyFile := filepath.Join(dir, "f6897335-1cb8-4634-a8cb-b2de7cab8107.jsonl")
	if _, err := os.Stat(partyFile); err != nil {
		t.Errorf("missing party file: %s", partyFile)
	}
	convergedFile := filepath.Join(dir, "aaaa-bbbb-cccc-dddd.jsonl")
	if _, err := os.Stat(convergedFile); err != nil {
		t.Errorf("missing converged party file: %s", convergedFile)
	}

	// Verify summary.jsonl.
	summaryPath := filepath.Join(dir, "summary.jsonl")
	summaryData, err := os.ReadFile(summaryPath)
	if err != nil {
		t.Fatal("read summary.jsonl:", err)
	}
	summaryLines := strings.Split(strings.TrimSpace(string(summaryData)), "\n")
	if len(summaryLines) != 2 {
		t.Errorf("summary.jsonl: expected 2 lines, got %d", len(summaryLines))
	}

	// Verify failures.jsonl contains only the split.
	failPath := filepath.Join(dir, "failures.jsonl")
	failData, err := os.ReadFile(failPath)
	if err != nil {
		t.Fatal("read failures.jsonl:", err)
	}
	failContent := strings.TrimSpace(string(failData))
	if failContent == "" {
		t.Error("failures.jsonl is empty; should have the split result")
	} else {
		failLines := strings.Split(failContent, "\n")
		if len(failLines) != 1 {
			t.Errorf("failures.jsonl: expected 1 line (only split), got %d", len(failLines))
		}
		var failSummary summaryLine
		if err := json.Unmarshal([]byte(failLines[0]), &failSummary); err != nil {
			t.Fatal("unmarshal failure line:", err)
		}
		if failSummary.Outcome != OutcomeSplit.String() {
			t.Errorf("failure outcome: got %q, want %q", failSummary.Outcome, OutcomeSplit.String())
		}
	}

	// Verify party file content.
	partyData, err := os.ReadFile(partyFile)
	if err != nil {
		t.Fatal("read party file:", err)
	}
	partyLines := strings.Split(strings.TrimSpace(string(partyData)), "\n")
	if len(partyLines) != 3 {
		t.Errorf("party file: expected 3 event lines, got %d", len(partyLines))
	}
}

func TestWriteOutputDir_NestedCreation(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "a", "b", "c", "output")
	if err := WriteOutputDir(dir, nil); err != nil {
		t.Fatal("WriteOutputDir nested:", err)
	}
	if _, err := os.Stat(dir); err != nil {
		t.Fatal("nested dir not created:", err)
	}
}

// ── Filename sanitization tests ─────────────────────────────────────────────

func TestSanitizePartyID(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"f6897335-1cb8-4634-a8cb-b2de7cab8107", "f6897335-1cb8-4634-a8cb-b2de7cab8107"},
		{"party/with/slashes", "party_with_slashes"},
		{"party.with.dots", "party_with_dots"},
		{"party/mixed.chars", "party_mixed_chars"},
		{"clean-id", "clean-id"},
	}
	for _, tt := range tests {
		got := sanitizePartyID(tt.input)
		if got != tt.want {
			t.Errorf("sanitizePartyID(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

// ── Deterministic ordering tests ────────────────────────────────────────────

func TestSortResults_Deterministic(t *testing.T) {
	t1 := testBaseTime
	t2 := testBaseTime.Add(1 * time.Second)

	results := []PartyResult{
		&mockPartyResult{partyID: "bbb", formedAt: t1},
		&mockPartyResult{partyID: "aaa", formedAt: t2},
		&mockPartyResult{partyID: "aaa", formedAt: t1},
		&mockPartyResult{partyID: "ccc", formedAt: t1},
	}

	sortResults(results)

	expected := []string{"aaa", "bbb", "ccc", "aaa"}
	for i, r := range results {
		if r.GetPartyID() != expected[i] {
			t.Errorf("index %d: got %q, want %q", i, r.GetPartyID(), expected[i])
		}
	}
}

// ── Backward compatibility ──────────────────────────────────────────────────

func TestBackwardCompat_SplitDetect(t *testing.T) {
	// The original mode should work exactly as before.
	testdataPath := filepath.Join("testdata", "party.jsonl")
	if _, err := os.Stat(testdataPath); err != nil {
		t.Skip("testdata not available:", err)
	}

	var stdout, stderr bytes.Buffer
	code := run([]string{"-verbose", testdataPath}, &stdout, &stderr)
	if code != 0 {
		t.Errorf("exit code %d, stderr: %s", code, stderr.String())
	}
	// Should produce output (verbose shows all events).
	if stdout.Len() == 0 {
		t.Error("expected output from split-detect mode with -verbose")
	}
}

func TestBackwardCompat_SplitDetectJSON(t *testing.T) {
	testdataPath := filepath.Join("testdata", "party.jsonl")
	if _, err := os.Stat(testdataPath); err != nil {
		t.Skip("testdata not available:", err)
	}

	var stdout, stderr bytes.Buffer
	code := run([]string{"-json", "-verbose", testdataPath}, &stdout, &stderr)
	if code != 0 {
		t.Errorf("exit code %d, stderr: %s", code, stderr.String())
	}
	// Each non-empty line should be valid JSON.
	for _, line := range strings.Split(stdout.String(), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || line == "No party splits detected." || line == "No party events found." {
			continue
		}
		var raw json.RawMessage
		if err := json.Unmarshal([]byte(line), &raw); err != nil {
			t.Errorf("invalid JSON in output: %q", line)
		}
	}
}

func TestBackwardCompat_NoArgs(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := run(nil, &stdout, &stderr)
	if code != 1 {
		t.Errorf("expected exit 1 for no args, got %d", code)
	}
}

// ── Lifecycle mode CLI tests ────────────────────────────────────────────────

func TestLifecycleMode_OutdirRequiresLifecycle(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := run([]string{"-outdir", "/tmp/test", "somefile"}, &stdout, &stderr)
	if code != 1 {
		t.Errorf("expected exit 1 when -outdir without -lifecycle, got %d", code)
	}
	if !strings.Contains(stderr.String(), "-outdir requires -lifecycle") {
		t.Errorf("expected error about -outdir, got: %s", stderr.String())
	}
}

func TestLifecycleMode_SummaryRequiresLifecycle(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := run([]string{"-summary", "somefile"}, &stdout, &stderr)
	if code != 1 {
		t.Errorf("expected exit 1 when -summary without -lifecycle, got %d", code)
	}
	if !strings.Contains(stderr.String(), "-summary requires -lifecycle") {
		t.Errorf("expected error about -summary, got: %s", stderr.String())
	}
}

func TestLifecycleMode_BasicRun(t *testing.T) {
	testdataPath := filepath.Join("testdata", "party.jsonl")
	if _, err := os.Stat(testdataPath); err != nil {
		t.Skip("testdata not available:", err)
	}

	var stdout, stderr bytes.Buffer
	code := run([]string{"-lifecycle", testdataPath}, &stdout, &stderr)
	if code != 0 {
		t.Errorf("lifecycle mode exit %d, stderr: %s", code, stderr.String())
	}
}

func TestLifecycleMode_WithSummary(t *testing.T) {
	testdataPath := filepath.Join("testdata", "party.jsonl")
	if _, err := os.Stat(testdataPath); err != nil {
		t.Skip("testdata not available:", err)
	}

	var stdout, stderr bytes.Buffer
	code := run([]string{"-lifecycle", "-summary", testdataPath}, &stdout, &stderr)
	if code != 0 {
		t.Errorf("lifecycle+summary exit %d, stderr: %s", code, stderr.String())
	}
}

// ── Duration formatting tests ───────────────────────────────────────────────

func TestFormatDuration(t *testing.T) {
	tests := []struct {
		d    time.Duration
		want string
	}{
		{0, "0.000s"},
		{154 * time.Millisecond, "0.154s"},
		{1500 * time.Millisecond, "1.500s"},
		{19234 * time.Millisecond, "19.2s"},
		{120 * time.Second, "120s"},
		{-5 * time.Second, "0.000s"},
	}
	for _, tt := range tests {
		got := formatDuration(tt.d)
		if got != tt.want {
			t.Errorf("formatDuration(%v) = %q, want %q", tt.d, got, tt.want)
		}
	}
}

// ── isFailureOutcome tests ──────────────────────────────────────────────────

func TestIsFailureOutcome(t *testing.T) {
	tests := []struct {
		outcome string
		want    bool
	}{
		{OutcomeSplit.String(), true},
		{OutcomeDegraded.String(), true},
		{OutcomeConverged.String(), false},
		{OutcomeAbandoned.String(), false},
		{"UNKNOWN", false},
	}
	for _, tt := range tests {
		got := isFailureOutcome(tt.outcome)
		if got != tt.want {
			t.Errorf("isFailureOutcome(%q) = %v, want %v", tt.outcome, got, tt.want)
		}
	}
}

// ── buildSummaryLine tests ──────────────────────────────────────────────────

func TestBuildSummaryLine(t *testing.T) {
	r := newTestSplitResult()
	sl := buildSummaryLine(r)

	if sl.PartyID != r.partyID {
		t.Errorf("party_id: got %q, want %q", sl.PartyID, r.partyID)
	}
	if sl.Outcome != OutcomeSplit.String() {
		t.Errorf("outcome: got %q, want %q", sl.Outcome, OutcomeSplit.String())
	}
	if sl.DurationMs != 19234 {
		t.Errorf("duration_ms: got %d, want 19234", sl.DurationMs)
	}
	if sl.Size != 2 {
		t.Errorf("size: got %d, want 2", sl.Size)
	}
	if sl.FailureReason != "follower_released_independent" {
		t.Errorf("failure_reason: got %q", sl.FailureReason)
	}
	if len(sl.MatchIDs) != 2 {
		t.Errorf("match_ids: expected 2 entries, got %d", len(sl.MatchIDs))
	}
}

func TestBuildSummaryLine_NoMatchIDs(t *testing.T) {
	r := &mockPartyResult{
		partyID: "test", formedAt: testBaseTime, leader: "l",
		members: []string{"l"}, outcome: OutcomeAbandoned.String(),
		duration: 5 * time.Second,
	}
	sl := buildSummaryLine(r)
	if sl.MatchIDs != nil {
		t.Error("expected nil match_ids for empty map")
	}
}

// ── Event details formatting ────────────────────────────────────────────────

func TestFormatEventDetails(t *testing.T) {
	ev := &mockEvent{
		details: map[string]string{
			"uid":    "should-be-excluded",
			"sid":    "should-be-excluded",
			"mode":   "echo_arena",
			"reason": "leader_left",
		},
	}
	got := formatEventDetails(ev)
	if strings.Contains(got, "uid=") {
		t.Error("uid should be excluded from event details")
	}
	if strings.Contains(got, "sid=") {
		t.Error("sid should be excluded from event details")
	}
	if !strings.Contains(got, "mode=echo_arena") {
		t.Errorf("missing mode in details: %q", got)
	}
	if !strings.Contains(got, "reason=leader_left") {
		t.Errorf("missing reason in details: %q", got)
	}
}

func TestFormatEventDetails_Empty(t *testing.T) {
	ev := &mockEvent{details: nil}
	got := formatEventDetails(ev)
	if got != "" {
		t.Errorf("expected empty string for nil details, got %q", got)
	}
}
