package main

import (
	"encoding/json"
	"testing"
	"time"
)

// parseLine is a test helper that unmarshals a JSONL string and calls
// ParseEvent. Returns the event and whether unmarshal succeeded.
func parseLine(t *testing.T, line string) Event {
	t.Helper()
	var raw map[string]json.RawMessage
	if err := json.Unmarshal([]byte(line), &raw); err != nil {
		t.Fatalf("failed to unmarshal test fixture: %v", err)
	}
	return ParseEvent(raw, line)
}

// assertEventType checks that the event is non-nil and has the expected type.
func assertEventType(t *testing.T, ev Event, want string) {
	t.Helper()
	if ev == nil {
		t.Fatal("expected non-nil event, got nil")
	}
	if got := ev.EventType(); got != want {
		t.Errorf("EventType() = %q, want %q", got, want)
	}
}

// ── Real Production Log Line Tests ───────────────────────────────────────────
//
// These fixtures are copied verbatim from production Nakama JSONL logs.

func TestParseFollowReleasedIndependent(t *testing.T) {
	const line = `{"level":"info","ts":"2026-05-25T18:35:08.258Z","caller":"server/evr_lobby_find.go:166","msg":"Follower cannot join leader's match, releasing to independent matchmaking","sid":"6900b97b-5868-11f1-802e-383ec87527a1","login_sid":"5dd3b982-5868-11f1-802e-383ec87527a1","uid":"a0e1e536-2fd2-4a3b-93e6-b1f6b398d639","evrid":"OVR-ORG-3557071787708655","username":"souleater0061_28544","request_type":"*evr.LobbyFindSessionRequest","mode":"echo_arena"}`

	ev := parseLine(t, line)
	assertEventType(t, ev, "FollowReleasedIndependent")

	e := ev.(*FollowReleasedIndependent)
	if e.UID != "a0e1e536-2fd2-4a3b-93e6-b1f6b398d639" {
		t.Errorf("UID = %q", e.UID)
	}
	if e.SID != "6900b97b-5868-11f1-802e-383ec87527a1" {
		t.Errorf("SID = %q", e.SID)
	}
	if e.Username != "souleater0061_28544" {
		t.Errorf("Username = %q", e.Username)
	}
	if e.Mode != "echo_arena" {
		t.Errorf("Mode = %q", e.Mode)
	}
	wantTS := time.Date(2026, 5, 25, 18, 35, 8, 258000000, time.UTC)
	if !e.Timestamp.Equal(wantTS) {
		t.Errorf("Timestamp = %v, want %v", e.Timestamp, wantTS)
	}
	if e.RawLine != line {
		t.Error("RawLine does not match input")
	}
}

func TestParseSplitRepaired(t *testing.T) {
	const line = `{"level":"info","ts":"2026-05-25T19:17:59.833Z","caller":"server/evr_lobby_builder.go:724","msg":"Repaired split party team assignment"}`

	ev := parseLine(t, line)
	assertEventType(t, ev, "SplitRepaired")

	e := ev.(*SplitRepaired)
	wantTS := time.Date(2026, 5, 25, 19, 17, 59, 833000000, time.UTC)
	if !e.Timestamp.Equal(wantTS) {
		t.Errorf("Timestamp = %v, want %v", e.Timestamp, wantTS)
	}
	// This line has no mid field; verify graceful zero value.
	if e.MatchID != "" {
		t.Errorf("MatchID = %q, want empty", e.MatchID)
	}
}

func TestParsePartyJoined(t *testing.T) {
	const line = `{"level":"debug","ts":"2026-05-25T18:33:55.518Z","caller":"server/evr_pipeline_party.go:95","msg":"Joined party group","sid":"abc12345-5868-11f1-802e-383ec87527a1","uid":"a0e1e536-2fd2-4a3b-93e6-b1f6b398d639","username":"souleater0061_28544","partyID":"f6897335-1cb8-4634-a8cb-b2de7cab8107.nakama2_us-east"}`

	ev := parseLine(t, line)
	assertEventType(t, ev, "PartyJoined")

	e := ev.(*PartyJoined)
	if e.UID != "a0e1e536-2fd2-4a3b-93e6-b1f6b398d639" {
		t.Errorf("UID = %q", e.UID)
	}
	if e.SID != "abc12345-5868-11f1-802e-383ec87527a1" {
		t.Errorf("SID = %q", e.SID)
	}
	if e.Username != "souleater0061_28544" {
		t.Errorf("Username = %q", e.Username)
	}
	if e.PartyID != "f6897335-1cb8-4634-a8cb-b2de7cab8107.nakama2_us-east" {
		t.Errorf("PartyID = %q", e.PartyID)
	}
	wantTS := time.Date(2026, 5, 25, 18, 33, 55, 518000000, time.UTC)
	if !e.Timestamp.Equal(wantTS) {
		t.Errorf("Timestamp = %v, want %v", e.Timestamp, wantTS)
	}
}

// ── Synthetic Tests for Every Event Type ─────────────────────────────────────
//
// Each test uses a minimal but realistic JSONL line to exercise the parser
// path for that event type.

func TestParsePartyFormed(t *testing.T) {
	const line = `{"ts":"2026-05-25T12:00:00.000Z","msg":"Party is ready","uid":"uid-1","sid":"sid-1","username":"alice","leader":"sid-1","size":3,"members":["bob","charlie"]}`

	ev := parseLine(t, line)
	assertEventType(t, ev, "PartyFormed")

	e := ev.(*PartyFormed)
	if e.LeaderSID != "sid-1" {
		t.Errorf("LeaderSID = %q", e.LeaderSID)
	}
	if e.Size != 3 {
		t.Errorf("Size = %d", e.Size)
	}
	if len(e.Members) != 2 || e.Members[0] != "bob" || e.Members[1] != "charlie" {
		t.Errorf("Members = %v", e.Members)
	}
	if e.UID != "uid-1" || e.Username != "alice" {
		t.Errorf("identity = %q / %q", e.UID, e.Username)
	}
}

func TestParseMemberFollowAttempt(t *testing.T) {
	const line = `{"ts":"2026-05-25T12:00:00.000Z","msg":"User is member of party, attempting to follow leader","uid":"uid-1","sid":"sid-1","username":"alice"}`

	ev := parseLine(t, line)
	assertEventType(t, ev, "MemberFollowAttempt")

	e := ev.(*MemberFollowAttempt)
	if e.UID != "uid-1" {
		t.Errorf("UID = %q", e.UID)
	}
}

func TestParseLeaderNotInMatch(t *testing.T) {
	const line = `{"ts":"2026-05-25T12:00:00.000Z","msg":"Leader is not in a match, falling through to normal find","uid":"uid-1","sid":"sid-1","username":"alice"}`

	ev := parseLine(t, line)
	assertEventType(t, ev, "LeaderNotInMatch")

	e := ev.(*LeaderNotInMatch)
	if e.Username != "alice" {
		t.Errorf("Username = %q", e.Username)
	}
}

func TestParseSocialFallback(t *testing.T) {
	const line = `{"ts":"2026-05-25T12:00:00.000Z","msg":"Follower in social mode, finding social lobby independently (party reservations will converge)","uid":"uid-1","sid":"sid-1","username":"alice","mode":"social_2.0"}`

	ev := parseLine(t, line)
	assertEventType(t, ev, "SocialFallback")

	e := ev.(*SocialFallback)
	if e.Mode != "social_2.0" {
		t.Errorf("Mode = %q", e.Mode)
	}
}

func TestParseFollowLeaderLobby(t *testing.T) {
	const line = `{"ts":"2026-05-25T12:00:00.000Z","msg":"Joining leader's lobby","uid":"uid-1","sid":"sid-1","username":"alice","mid":"match-123"}`

	ev := parseLine(t, line)
	assertEventType(t, ev, "FollowLeaderLobby")

	e := ev.(*FollowLeaderLobby)
	if e.MatchID != "match-123" {
		t.Errorf("MatchID = %q", e.MatchID)
	}
}

func TestParseMatchJoinAttempt(t *testing.T) {
	const line = `{"ts":"2026-05-25T12:00:00.000Z","msg":"Player joining the match.","uid":"uid-1","sid":"sid-1","username":"alice","mid":"match-123"}`

	ev := parseLine(t, line)
	assertEventType(t, ev, "MatchJoinAttempt")

	e := ev.(*MatchJoinAttempt)
	if e.MatchID != "match-123" {
		t.Errorf("MatchID = %q", e.MatchID)
	}
}

func TestParseMatchJoined(t *testing.T) {
	const line = `{"ts":"2026-05-25T12:00:00.000Z","msg":"Joined entrant.","uid":"uid-1","sid":"sid-1","username":"alice","mid":"match-123","role":2}`

	ev := parseLine(t, line)
	assertEventType(t, ev, "MatchJoined")

	e := ev.(*MatchJoined)
	if e.MatchID != "match-123" {
		t.Errorf("MatchID = %q", e.MatchID)
	}
	if e.Role != 2 {
		t.Errorf("Role = %d", e.Role)
	}
}

func TestParseMatchJoinFailed(t *testing.T) {
	const line = `{"ts":"2026-05-25T12:00:00.000Z","msg":"failed to join match","uid":"uid-1","sid":"sid-1","username":"alice","mid":"match-123","error":"match full"}`

	ev := parseLine(t, line)
	assertEventType(t, ev, "MatchJoinFailed")

	e := ev.(*MatchJoinFailed)
	if e.Error != "match full" {
		t.Errorf("Error = %q", e.Error)
	}
	if e.MatchID != "match-123" {
		t.Errorf("MatchID = %q", e.MatchID)
	}
}

func TestParseMatchBuilt(t *testing.T) {
	const line = `{"ts":"2026-05-25T12:00:00.000Z","msg":"Building match","entrants":[{"uid":"a","role":0},{"uid":"b","role":1}],"module":"evr_match"}`

	ev := parseLine(t, line)
	assertEventType(t, ev, "MatchBuilt")

	e := ev.(*MatchBuilt)
	if e.Module != "evr_match" {
		t.Errorf("Module = %q", e.Module)
	}
	if e.Entrants == nil {
		t.Fatal("Entrants is nil")
	}
	// Verify raw JSON is preserved.
	var arr []json.RawMessage
	if err := json.Unmarshal(e.Entrants, &arr); err != nil {
		t.Fatalf("Entrants not valid JSON array: %v", err)
	}
	if len(arr) != 2 {
		t.Errorf("Entrants length = %d, want 2", len(arr))
	}
}

func TestParseLeaderNonJoinableMode(t *testing.T) {
	const line = `{"ts":"2026-05-25T12:00:00.000Z","msg":"Leader is in a non-joinable mode for party follow","uid":"uid-1","sid":"sid-1","username":"alice","mode":"echo_combat"}`

	ev := parseLine(t, line)
	assertEventType(t, ev, "LeaderNonJoinableMode")

	e := ev.(*LeaderNonJoinableMode)
	if e.Mode != "echo_combat" {
		t.Errorf("Mode = %q", e.Mode)
	}
}

func TestParseLeaderNonJoinablePoll(t *testing.T) {
	const line = `{"ts":"2026-05-25T12:00:00.000Z","msg":"Leader is in a non-joinable mode during poll","uid":"uid-1","sid":"sid-1","username":"alice","mode":"echo_combat"}`

	ev := parseLine(t, line)
	assertEventType(t, ev, "LeaderNonJoinablePoll")

	e := ev.(*LeaderNonJoinablePoll)
	if e.Mode != "echo_combat" {
		t.Errorf("Mode = %q", e.Mode)
	}
}

func TestParseLeaderLeftDuringPoll(t *testing.T) {
	const line = `{"ts":"2026-05-25T12:00:00.000Z","msg":"Leader left match during poll","uid":"uid-1","sid":"sid-1","username":"alice"}`

	ev := parseLine(t, line)
	assertEventType(t, ev, "LeaderLeftDuringPoll")

	e := ev.(*LeaderLeftDuringPoll)
	if e.SID != "sid-1" {
		t.Errorf("SID = %q", e.SID)
	}
}

func TestParseFailedJoinLeaderLobby(t *testing.T) {
	const line = `{"ts":"2026-05-25T12:00:00.000Z","msg":"Failed to join leader's lobby","uid":"uid-1","sid":"sid-1","username":"alice","error":"timeout"}`

	ev := parseLine(t, line)
	assertEventType(t, ev, "FailedJoinLeaderLobby")

	e := ev.(*FailedJoinLeaderLobby)
	if e.Error != "timeout" {
		t.Errorf("Error = %q", e.Error)
	}
}

func TestParseFailedPriorityJoinLeader(t *testing.T) {
	const line = `{"ts":"2026-05-25T12:00:00.000Z","msg":"Failed priority join to leader's lobby","uid":"uid-1","sid":"sid-1","username":"alice","error":"lobby closed"}`

	ev := parseLine(t, line)
	assertEventType(t, ev, "FailedPriorityJoinLeader")

	e := ev.(*FailedPriorityJoinLeader)
	if e.Error != "lobby closed" {
		t.Errorf("Error = %q", e.Error)
	}
}

func TestParseFailedPriorityJoinFollower(t *testing.T) {
	const line = `{"ts":"2026-05-25T12:00:00.000Z","msg":"Failed priority join to follower's lobby","uid":"uid-1","sid":"sid-1","username":"alice","error":"not found"}`

	ev := parseLine(t, line)
	assertEventType(t, ev, "FailedPriorityJoinFollower")

	e := ev.(*FailedPriorityJoinFollower)
	if e.Error != "not found" {
		t.Errorf("Error = %q", e.Error)
	}
}

func TestParseCrashDetected(t *testing.T) {
	const line = `{"ts":"2026-05-25T12:00:00.000Z","msg":"Created reconnect reservation for crashed player","uid":"uid-1","sid":"sid-1","username":"alice","mid":"match-crash"}`

	ev := parseLine(t, line)
	assertEventType(t, ev, "CrashDetected")

	e := ev.(*CrashDetected)
	if e.MatchID != "match-crash" {
		t.Errorf("MatchID = %q", e.MatchID)
	}
}

func TestParsePlayerLeft(t *testing.T) {
	const line = `{"ts":"2026-05-25T12:00:00.000Z","msg":"Player leaving the match.","uid":"uid-1","sid":"sid-1","username":"alice","reason":3}`

	ev := parseLine(t, line)
	assertEventType(t, ev, "PlayerLeft")

	e := ev.(*PlayerLeft)
	if e.Reason != 3 {
		t.Errorf("Reason = %d", e.Reason)
	}
}

func TestParsePlayerLeftVoluntary(t *testing.T) {
	const line = `{"ts":"2026-05-25T12:00:00.000Z","msg":"Player voluntarily left. No crash reservation needed.","uid":"uid-1","sid":"sid-1","username":"alice"}`

	ev := parseLine(t, line)
	assertEventType(t, ev, "PlayerLeftVoluntary")

	e := ev.(*PlayerLeftVoluntary)
	if e.Username != "alice" {
		t.Errorf("Username = %q", e.Username)
	}
}

func TestParseMatchmakingTicketAdded(t *testing.T) {
	const line = `{"ts":"2026-05-25T12:00:00.000Z","msg":"Matchmaking ticket added","uid":"uid-1","sid":"sid-1","username":"alice","ticket":"ticket-abc","presences":[{"uid":"uid-1","sid":"sid-1"}]}`

	ev := parseLine(t, line)
	assertEventType(t, ev, "MatchmakingTicketAdded")

	e := ev.(*MatchmakingTicketAdded)
	if e.Ticket != "ticket-abc" {
		t.Errorf("Ticket = %q", e.Ticket)
	}
	if e.Presences == nil {
		t.Fatal("Presences is nil")
	}
	var arr []json.RawMessage
	if err := json.Unmarshal(e.Presences, &arr); err != nil {
		t.Fatalf("Presences not valid JSON: %v", err)
	}
	if len(arr) != 1 {
		t.Errorf("Presences length = %d", len(arr))
	}
}

func TestParseEntrantMetadataFallback(t *testing.T) {
	const line = `{"ts":"2026-05-25T12:00:00.000Z","msg":"entrant metadata not found, falling back to fresh metadata","uid":"uid-1","sid":"sid-1","username":"alice"}`

	ev := parseLine(t, line)
	assertEventType(t, ev, "EntrantMetadataFallback")
}

func TestParseLobbySessionFailure(t *testing.T) {
	const line = `{"ts":"2026-05-25T12:00:00.000Z","msg":"Sending *evr.LobbySessionFailurev4 message","uid":"uid-1","sid":"sid-1","username":"alice"}`

	ev := parseLine(t, line)
	assertEventType(t, ev, "LobbySessionFailure")
}

func TestParseLobbyFindComplete(t *testing.T) {
	const line = `{"ts":"2026-05-25T12:00:00.000Z","msg":"Lobby find complete","uid":"uid-1","sid":"sid-1","username":"alice","mode":"echo_arena"}`

	ev := parseLine(t, line)
	assertEventType(t, ev, "LobbyFindComplete")

	e := ev.(*LobbyFindComplete)
	if e.Mode != "echo_arena" {
		t.Errorf("Mode = %q", e.Mode)
	}
}

func TestParseMatchmakingStreamClosed(t *testing.T) {
	const line = `{"ts":"2026-05-25T12:00:00.000Z","msg":"Matchmaking stream closed, canceling matchmaking","uid":"uid-1","sid":"sid-1","username":"alice"}`

	ev := parseLine(t, line)
	assertEventType(t, ev, "MatchmakingStreamClosed")
}

func TestParseUnexpectedError(t *testing.T) {
	const line = `{"ts":"2026-05-25T12:00:00.000Z","msg":"Unexpected error while finding match","uid":"uid-1","sid":"sid-1","username":"alice","error":"context canceled"}`

	ev := parseLine(t, line)
	assertEventType(t, ev, "UnexpectedError")

	e := ev.(*UnexpectedError)
	if e.Error != "context canceled" {
		t.Errorf("Error = %q", e.Error)
	}
}

func TestParseReconnectRestore(t *testing.T) {
	const line = `{"ts":"2026-05-25T12:00:00.000Z","msg":"Player reconnecting from crash. Restoring role alignment.","uid":"uid-1","sid":"sid-1","username":"alice"}`

	ev := parseLine(t, line)
	assertEventType(t, ev, "ReconnectRestore")

	e := ev.(*ReconnectRestore)
	if e.UID != "uid-1" {
		t.Errorf("UID = %q", e.UID)
	}
}

// ── Unknown Party Event Catch-All ────────────────────────────────────────────

func TestParseUnknownPartyEvent_LowerCase(t *testing.T) {
	const line = `{"ts":"2026-05-25T12:00:00.000Z","msg":"party member disconnected unexpectedly","uid":"uid-1"}`

	ev := parseLine(t, line)
	assertEventType(t, ev, "UnknownPartyEvent")

	e := ev.(*UnknownPartyEvent)
	if e.Msg != "party member disconnected unexpectedly" {
		t.Errorf("Msg = %q", e.Msg)
	}
}

func TestParseUnknownPartyEvent_UpperCase(t *testing.T) {
	const line = `{"ts":"2026-05-25T12:00:00.000Z","msg":"Party cleanup scheduled","uid":"uid-1"}`

	ev := parseLine(t, line)
	assertEventType(t, ev, "UnknownPartyEvent")

	e := ev.(*UnknownPartyEvent)
	if e.Msg != "Party cleanup scheduled" {
		t.Errorf("Msg = %q", e.Msg)
	}
}

func TestParseUnknownPartyEvent_PreservesFields(t *testing.T) {
	const line = `{"ts":"2026-05-25T12:00:00.000Z","msg":"party size exceeded limit","max":5,"current":7}`

	ev := parseLine(t, line)
	assertEventType(t, ev, "UnknownPartyEvent")

	e := ev.(*UnknownPartyEvent)
	if e.Fields == nil {
		t.Fatal("Fields is nil")
	}
	if _, ok := e.Fields["max"]; !ok {
		t.Error("Fields missing 'max'")
	}
}

// ── Non-Party Lines Return nil ───────────────────────────────────────────────

func TestParseNonPartyLine_ReturnsNil(t *testing.T) {
	lines := []string{
		`{"ts":"2026-05-25T12:00:00.000Z","msg":"Server starting up","version":"1.0"}`,
		`{"ts":"2026-05-25T12:00:00.000Z","msg":"Database connection established"}`,
		`{"ts":"2026-05-25T12:00:00.000Z","msg":"HTTP request completed","status":200}`,
		`{"ts":"2026-05-25T12:00:00.000Z","msg":"Garbage collection completed"}`,
	}

	for _, line := range lines {
		ev := parseLine(t, line)
		if ev != nil {
			t.Errorf("expected nil for %q, got %s", line, ev.EventType())
		}
	}
}

// ── Edge Cases ───────────────────────────────────────────────────────────────

func TestParseEmptyMsg_ReturnsNil(t *testing.T) {
	const line = `{"ts":"2026-05-25T12:00:00.000Z","msg":""}`

	ev := parseLine(t, line)
	if ev != nil {
		t.Errorf("expected nil for empty msg, got %s", ev.EventType())
	}
}

func TestParseMissingMsg_ReturnsNil(t *testing.T) {
	const line = `{"ts":"2026-05-25T12:00:00.000Z","level":"info"}`

	ev := parseLine(t, line)
	if ev != nil {
		t.Errorf("expected nil for missing msg, got %s", ev.EventType())
	}
}

func TestParseMissingTimestamp_ReturnsNil(t *testing.T) {
	const line = `{"msg":"Party is ready","leader":"sid-1","size":2,"members":["bob"]}`

	ev := parseLine(t, line)
	if ev != nil {
		t.Errorf("expected nil for missing timestamp, got %s", ev.EventType())
	}
}

func TestParseMalformedJSON_ReturnsNil(t *testing.T) {
	// Confirm that the caller (parseLine helper) handles this gracefully.
	const line = `{not valid json at all`
	var raw map[string]json.RawMessage
	err := json.Unmarshal([]byte(line), &raw)
	if err == nil {
		t.Fatal("expected unmarshal error for malformed JSON")
	}
	// In production, the caller skips malformed lines before calling
	// ParseEvent. Verify ParseEvent handles a nil-equivalent map safely.
	ev := ParseEvent(nil, line)
	if ev != nil {
		t.Errorf("expected nil for nil raw map, got %s", ev.EventType())
	}
}

func TestParseMinimalPartyFormed_MissingOptionalFields(t *testing.T) {
	// A line with the right msg but no identity fields -- parser should still
	// produce an event with zero-value identity.
	const line = `{"ts":"2026-05-25T12:00:00.000Z","msg":"Party is ready","leader":"sid-x","size":1,"members":[]}`

	ev := parseLine(t, line)
	assertEventType(t, ev, "PartyFormed")

	e := ev.(*PartyFormed)
	if e.UID != "" {
		t.Errorf("UID should be empty, got %q", e.UID)
	}
	if e.Username != "" {
		t.Errorf("Username should be empty, got %q", e.Username)
	}
	if e.Size != 1 {
		t.Errorf("Size = %d", e.Size)
	}
}

func TestParseUnixTimestamp(t *testing.T) {
	// Some older log formats use numeric timestamps.
	const line = `{"ts":1748189708.258,"msg":"Joined party group","sid":"s","uid":"u","username":"n","partyID":"p"}`

	ev := parseLine(t, line)
	assertEventType(t, ev, "PartyJoined")

	e := ev.(*PartyJoined)
	// Verify the timestamp parsed as a Unix float.
	if e.Timestamp.IsZero() {
		t.Error("Timestamp is zero")
	}
	if e.Timestamp.Year() != 2025 {
		t.Errorf("Timestamp year = %d, expected 2025 for epoch 1748189708", e.Timestamp.Year())
	}
}

// ── Helper Function Tests ────────────────────────────────────────────────────

func TestGetInt(t *testing.T) {
	raw := map[string]json.RawMessage{
		"role":    json.RawMessage(`2`),
		"reason":  json.RawMessage(`3`),
		"zero":    json.RawMessage(`0`),
		"float":   json.RawMessage(`1.7`),
		"invalid": json.RawMessage(`"not a number"`),
	}

	if got := getInt(raw, "role"); got != 2 {
		t.Errorf("getInt(role) = %d, want 2", got)
	}
	if got := getInt(raw, "reason"); got != 3 {
		t.Errorf("getInt(reason) = %d, want 3", got)
	}
	if got := getInt(raw, "zero"); got != 0 {
		t.Errorf("getInt(zero) = %d, want 0", got)
	}
	if got := getInt(raw, "float"); got != 1 {
		t.Errorf("getInt(float) = %d, want 1 (truncated)", got)
	}
	if got := getInt(raw, "invalid"); got != 0 {
		t.Errorf("getInt(invalid) = %d, want 0", got)
	}
	if got := getInt(raw, "missing"); got != 0 {
		t.Errorf("getInt(missing) = %d, want 0", got)
	}
}

func TestGetRaw(t *testing.T) {
	raw := map[string]json.RawMessage{
		"entrants": json.RawMessage(`[{"uid":"a"}]`),
	}

	if got := getRaw(raw, "entrants"); got == nil {
		t.Error("getRaw(entrants) = nil")
	}
	if got := getRaw(raw, "missing"); got != nil {
		t.Error("getRaw(missing) should be nil")
	}
}

// ── Testdata Integration ─────────────────────────────────────────────────────

func TestParseTestdataPartyFormed(t *testing.T) {
	// This is the first line from testdata/party.jsonl.
	const line = `{"level":"debug","ts":"2026-04-24T00:59:50.060Z","caller":"server/evr_lobby_find.go:323","msg":"Party is ready","sid":"d4eebe31-3f75-11f1-91ba-383ec87527a1","login_sid":"cc4d4c02-3f75-11f1-91ba-383ec87527a1","uid":"b65c8d64-2cd9-4f15-abee-96c32fd06ce3","evrid":"OVR-ORG-1234","username":"nikothedrummer_87818","request_type":"*evr.LobbyFindSessionRequest","request":"LobbyFindSessionRequest","uid":"b65c8d64-2cd9-4f15-abee-96c32fd06ce3","sid":"d4eebe31-3f75-11f1-91ba-383ec87527a1","username":"nikothedrummer_87818","evrid":"OVR-ORG-1234","leader":"d4eebe31-3f75-11f1-91ba-383ec87527a1","size":2,"members":["outcastmain"]}`

	ev := parseLine(t, line)
	assertEventType(t, ev, "PartyFormed")

	e := ev.(*PartyFormed)
	// The line has duplicate uid/sid/username keys; JSON unmarshal uses the last value.
	if e.UID != "b65c8d64-2cd9-4f15-abee-96c32fd06ce3" {
		t.Errorf("UID = %q", e.UID)
	}
	if e.LeaderSID != "d4eebe31-3f75-11f1-91ba-383ec87527a1" {
		t.Errorf("LeaderSID = %q", e.LeaderSID)
	}
	if e.Size != 2 {
		t.Errorf("Size = %d", e.Size)
	}
	if len(e.Members) != 1 || e.Members[0] != "outcastmain" {
		t.Errorf("Members = %v", e.Members)
	}
	wantTS := time.Date(2026, 4, 24, 0, 59, 50, 60000000, time.UTC)
	if !e.Timestamp.Equal(wantTS) {
		t.Errorf("Timestamp = %v, want %v", e.Timestamp, wantTS)
	}
}

func TestParseTestdataMatchJoined(t *testing.T) {
	// Lines 2 and 3 from testdata/party.jsonl.
	lines := []struct {
		line    string
		matchID string
		uid     string
	}{
		{
			line:    `{"level":"info","ts":"2026-04-24T00:59:55.060Z","caller":"server/evr_lobby_joinentrant.go:287","msg":"Joined entrant.","mid":"aaaa-1111","uid":"b65c8d64-2cd9-4f15-abee-96c32fd06ce3","sid":"d4eebe31-3f75-11f1-91ba-383ec87527a1","role":0}`,
			matchID: "aaaa-1111",
			uid:     "b65c8d64-2cd9-4f15-abee-96c32fd06ce3",
		},
		{
			line:    `{"level":"info","ts":"2026-04-24T00:59:56.060Z","caller":"server/evr_lobby_joinentrant.go:287","msg":"Joined entrant.","mid":"bbbb-2222","uid":"follower-uid-1234","sid":"follower-sid-1234","role":0}`,
			matchID: "bbbb-2222",
			uid:     "follower-uid-1234",
		},
	}

	for _, tc := range lines {
		ev := parseLine(t, tc.line)
		assertEventType(t, ev, "MatchJoined")

		e := ev.(*MatchJoined)
		if e.MatchID != tc.matchID {
			t.Errorf("MatchID = %q, want %q", e.MatchID, tc.matchID)
		}
		if e.UID != tc.uid {
			t.Errorf("UID = %q, want %q", e.UID, tc.uid)
		}
		if e.Role != 0 {
			t.Errorf("Role = %d, want 0", e.Role)
		}
	}
}
