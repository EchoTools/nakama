// party-split-detector: parse Nakama JSONL logs and detect party member splits.
//
// Usage: party-split-detector [flags] <logfile...>
//
// Modes:
//
//	(default)     Original split detection (backward compatible)
//	--lifecycle   Full party lifecycle reconstruction
//
// Flags (all modes):
//
//	-json           Output as JSON lines instead of human-readable
//	-since <dur>    Only process entries from the last N (e.g. -since 24h)
//	-verbose        Show all party events, not just failures
//
// Flags (--lifecycle mode):
//
//	-outdir <path>  Write per-party JSONL files to this directory
//	-summary        Print summary statistics to stdout after processing
package main

import (
	"bufio"
	"compress/gzip"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/klauspost/compress/zstd"
)

const windowDuration = 60 * time.Second

// splitTriggers are log messages that indicate a party member was separated.
var splitTriggers = map[string]bool{
	"Follower cannot join leader's match, redirecting to social lobby":          true,
	"Follower cannot join leader's match, releasing to independent matchmaking": true,
	"Timed out waiting for party members":                                       true,
	"Leader's match is full or closed":                                          true,
}

// ── Internal state types ─────────────────────────────────────────────────────

type partyRecord struct {
	leaderSID  string
	leaderUID  string
	leaderName string
	members    []string // usernames
	formedAt   time.Time
}

type joinRecord struct {
	matchID  string
	joinedAt time.Time
}

type splitMsg struct {
	text       string
	occurredAt time.Time
}

// ── Output types ─────────────────────────────────────────────────────────────

// MemberOutcome describes what happened to one party member.
type MemberOutcome struct {
	Username  string `json:"username"`
	UID       string `json:"uid,omitempty"`
	MatchID   string `json:"match_id"`
	TimeDelta string `json:"time_delta"`
}

// SplitReport is emitted for each detected (or -verbose) party event.
type SplitReport struct {
	FormedAt   string          `json:"formed_at"`
	Leader     string          `json:"leader"`
	PartySize  int             `json:"party_size"`
	Members    []MemberOutcome `json:"members"`
	FailureMsg string          `json:"failure_msg,omitempty"`
	formedAt   time.Time       // unexported: used for ordering, not serialised
}

// ── Correlation state ─────────────────────────────────────────────────────────

type state struct {
	uidToUsername map[string]string
	usernameToUID map[string]string
	sidToUID      map[string]string

	parties     []*partyRecord
	joinsByUID  map[string][]joinRecord
	splitsByUID map[string][]splitMsg

	splits  []SplitReport
	lineN   int
	since   time.Time
	verbose bool
	jsonOut bool
}

func newState(since time.Time, verbose, jsonOut bool) *state {
	return &state{
		uidToUsername: make(map[string]string),
		usernameToUID: make(map[string]string),
		sidToUID:      make(map[string]string),
		joinsByUID:    make(map[string][]joinRecord),
		splitsByUID:   make(map[string][]splitMsg),
		since:         since,
		verbose:       verbose,
		jsonOut:       jsonOut,
	}
}

// ── JSON field helpers ────────────────────────────────────────────────────────

func getString(raw map[string]json.RawMessage, key string) string {
	v, ok := raw[key]
	if !ok {
		return ""
	}
	var s string
	_ = json.Unmarshal(v, &s)
	return s
}

func getFloat(raw map[string]json.RawMessage, key string) float64 {
	v, ok := raw[key]
	if !ok {
		return 0
	}
	var f float64
	_ = json.Unmarshal(v, &f)
	return f
}

func getStrings(raw map[string]json.RawMessage, key string) []string {
	v, ok := raw[key]
	if !ok {
		return nil
	}
	var ss []string
	_ = json.Unmarshal(v, &ss)
	return ss
}

func parseTS(raw map[string]json.RawMessage) (time.Time, bool) {
	// Nakama's zap logger emits "ts" as an RFC3339 string (e.g. "2026-04-24T00:59:50.060Z").
	// Fall back to treating it as a Unix float for older/custom log formats.
	if s := getString(raw, "ts"); s != "" {
		if t, err := time.Parse(time.RFC3339Nano, s); err == nil {
			return t, true
		}
		if t, err := time.Parse(time.RFC3339, s); err == nil {
			return t, true
		}
	}
	f := getFloat(raw, "ts")
	if f == 0 {
		return time.Time{}, false
	}
	sec := int64(f)
	nsec := int64((f - float64(sec)) * 1e9)
	return time.Unix(sec, nsec), true
}

// ── Line processing ───────────────────────────────────────────────────────────

func (s *state) processLine(raw map[string]json.RawMessage) {
	msg := getString(raw, "msg")
	if msg == "" {
		return
	}

	ts, ok := parseTS(raw)
	if !ok {
		return
	}

	if !s.since.IsZero() && ts.Before(s.since) {
		return
	}

	// Top-level identity fields present on every Nakama log line.
	uid := getString(raw, "uid")
	sid := getString(raw, "sid")
	username := getString(raw, "username")

	// Keep correlation maps fresh from every line.
	if uid != "" && username != "" {
		s.uidToUsername[uid] = username
		s.usernameToUID[username] = uid
	}
	if uid != "" && sid != "" {
		s.sidToUID[sid] = uid
	}

	switch msg {

	case "Party is ready":
		// Zap fields: leader (session UUID), size (int), members ([]string usernames)
		leaderSID := getString(raw, "leader")
		members := getStrings(raw, "members")
		if leaderSID == "" || len(members) == 0 {
			return
		}
		// top-level uid/username belong to the leader when this message fires.
		leaderUID := uid
		if leaderUID == "" {
			leaderUID = s.sidToUID[leaderSID]
		}
		leaderName := username
		if leaderName == "" {
			leaderName = s.uidToUsername[leaderUID]
		}
		s.parties = append(s.parties, &partyRecord{
			leaderSID:  leaderSID,
			leaderUID:  leaderUID,
			leaderName: leaderName,
			members:    members,
			formedAt:   ts,
		})

	case "Joined entrant.":
		// Zap fields: mid (match UUID), uid (user UUID), sid (session UUID), role (int).
		// When both the logger context and zap fields contain "uid"/"sid", the last
		// JSON key value wins during unmarshalling, giving us the entrant's ids.
		mid := getString(raw, "mid")
		if mid == "" || uid == "" {
			return
		}
		if username != "" {
			s.uidToUsername[uid] = username
			s.usernameToUID[username] = uid
		}
		if sid != "" {
			s.sidToUID[sid] = uid
		}
		s.joinsByUID[uid] = append(s.joinsByUID[uid], joinRecord{
			matchID:  mid,
			joinedAt: ts,
		})

	default:
		if splitTriggers[msg] && uid != "" {
			s.splitsByUID[uid] = append(s.splitsByUID[uid], splitMsg{
				text:       msg,
				occurredAt: ts,
			})
		}
	}

	// Periodic maintenance: analyse closed windows, prune old records.
	s.lineN++
	if s.lineN%5000 == 0 {
		s.tick(ts)
	}
}

// tick analyses parties whose 60-second window has elapsed and prunes stale records.
func (s *state) tick(now time.Time) {
	var active []*partyRecord
	for _, p := range s.parties {
		if now.Sub(p.formedAt) >= windowDuration {
			s.analyzeParty(p)
		} else {
			active = append(active, p)
		}
	}
	s.parties = active

	var cutoff time.Time
	if len(s.parties) > 0 {
		oldest := s.parties[0].formedAt
		for _, p := range s.parties[1:] {
			if p.formedAt.Before(oldest) {
				oldest = p.formedAt
			}
		}
		cutoff = oldest.Add(-5 * time.Second)
	} else {
		// No active parties — nothing useful to keep beyond the window.
		cutoff = now.Add(-windowDuration)
	}
	s.pruneOldRecords(cutoff)
}

func (s *state) pruneOldRecords(cutoff time.Time) {
	for uid, joins := range s.joinsByUID {
		var kept []joinRecord
		for _, j := range joins {
			if !j.joinedAt.Before(cutoff) {
				kept = append(kept, j)
			}
		}
		if len(kept) == 0 {
			delete(s.joinsByUID, uid)
		} else {
			s.joinsByUID[uid] = kept
		}
	}
	for uid, msgs := range s.splitsByUID {
		var kept []splitMsg
		for _, m := range msgs {
			if !m.occurredAt.Before(cutoff) {
				kept = append(kept, m)
			}
		}
		if len(kept) == 0 {
			delete(s.splitsByUID, uid)
		} else {
			s.splitsByUID[uid] = kept
		}
	}
}

// analyzeParty inspects a party's window and records a SplitReport if needed.
func (s *state) analyzeParty(p *partyRecord) {
	// Lazy leader resolution — joins seen after party formation may have filled maps.
	if p.leaderUID == "" {
		p.leaderUID = s.sidToUID[p.leaderSID]
	}
	if p.leaderName == "" && p.leaderUID != "" {
		p.leaderName = s.uidToUsername[p.leaderUID]
	}

	// Build the full participant list: leader first, then followers.
	type memberResult struct {
		username string
		uid      string
		matchID  string
		joinedAt time.Time
		joined   bool
	}

	allMembers := make([]memberResult, 0, 1+len(p.members))
	allMembers = append(allMembers, memberResult{username: p.leaderName, uid: p.leaderUID})
	for _, uname := range p.members {
		uid := s.usernameToUID[uname]
		allMembers = append(allMembers, memberResult{username: uname, uid: uid})
	}

	matchSet := make(map[string]struct{})
	var failureMsg string

	for i := range allMembers {
		r := &allMembers[i]
		if r.uid == "" {
			continue
		}

		// Find the earliest join within the 60-second window.
		for _, j := range s.joinsByUID[r.uid] {
			if j.joinedAt.Before(p.formedAt) {
				continue
			}
			if j.joinedAt.Sub(p.formedAt) > windowDuration {
				continue
			}
			if !r.joined || j.joinedAt.Before(r.joinedAt) {
				r.matchID = j.matchID
				r.joinedAt = j.joinedAt
				r.joined = true
			}
		}
		if r.joined {
			matchSet[r.matchID] = struct{}{}
		}

		// Find the earliest split message in the window for this member.
		if failureMsg == "" {
			for _, sm := range s.splitsByUID[r.uid] {
				if sm.occurredAt.Before(p.formedAt) {
					continue
				}
				if sm.occurredAt.Sub(p.formedAt) > windowDuration {
					continue
				}
				failureMsg = sm.text
				break
			}
		}
	}

	// Split conditions: multiple distinct match IDs, a failure message, or at
	// least one member joined while another did not.
	isSplit := len(matchSet) > 1 || failureMsg != ""
	if !isSplit {
		joined := 0
		for _, r := range allMembers {
			if r.joined {
				joined++
			}
		}
		if joined > 0 && joined < len(allMembers) {
			isSplit = true
		}
	}

	if !isSplit && !s.verbose {
		return
	}

	report := SplitReport{
		FormedAt:   p.formedAt.UTC().Format(time.RFC3339Nano),
		Leader:     p.leaderName,
		PartySize:  len(p.members) + 1, // followers + leader
		FailureMsg: failureMsg,
		formedAt:   p.formedAt,
	}

	for _, r := range allMembers {
		matchID := r.matchID
		if matchID == "" {
			matchID = "none"
		}
		delta := ""
		if r.joined {
			delta = r.joinedAt.Sub(p.formedAt).Round(time.Millisecond).String()
		}
		report.Members = append(report.Members, MemberOutcome{
			Username:  r.username,
			UID:       r.uid,
			MatchID:   matchID,
			TimeDelta: delta,
		})
	}

	s.splits = append(s.splits, report)
}

// flushAll analyses any remaining open parties at end-of-input.
func (s *state) flushAll() {
	for _, p := range s.parties {
		s.analyzeParty(p)
	}
	s.parties = nil
}

// ── File I/O ──────────────────────────────────────────────────────────────────

type zstdCloser struct {
	dec *zstd.Decoder
	f   *os.File
}

func (z *zstdCloser) Read(p []byte) (int, error) { return z.dec.Read(p) }
func (z *zstdCloser) Close() error {
	z.dec.Close()
	return z.f.Close()
}

type gzipCloser struct {
	gz *gzip.Reader
	f  *os.File
}

func (g *gzipCloser) Read(p []byte) (int, error) { return g.gz.Read(p) }
func (g *gzipCloser) Close() error {
	if err := g.gz.Close(); err != nil {
		g.f.Close()
		return err
	}
	return g.f.Close()
}

func openReader(path string) (io.ReadCloser, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	switch {
	case strings.HasSuffix(path, ".zst"):
		dec, err := zstd.NewReader(f)
		if err != nil {
			f.Close()
			return nil, fmt.Errorf("zstd init: %w", err)
		}
		return &zstdCloser{dec: dec, f: f}, nil
	case strings.HasSuffix(path, ".gz"):
		gz, err := gzip.NewReader(f)
		if err != nil {
			f.Close()
			return nil, fmt.Errorf("gzip init: %w", err)
		}
		return &gzipCloser{gz: gz, f: f}, nil
	default:
		// Plain text: .log, .jsonl, no extension, etc.
		return f, nil
	}
}

func processFile(path string, st *state) error {
	r, err := openReader(path)
	if err != nil {
		return err
	}
	defer r.Close()

	scanner := bufio.NewScanner(r)
	// Start with 64 KiB; allow lines up to 16 MiB (handles wide zap payloads).
	buf := make([]byte, 64*1024)
	scanner.Buffer(buf, 16*1024*1024)

	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var raw map[string]json.RawMessage
		if err := json.Unmarshal(line, &raw); err != nil {
			continue // skip malformed lines
		}
		st.processLine(raw)
	}
	return scanner.Err()
}

// ── Human-readable output ─────────────────────────────────────────────────────

func printHumanTo(w io.Writer, r SplitReport) {
	const hr = "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
	label := "PARTY SPLIT"
	if r.FailureMsg == "" {
		label = "PARTY EVENT"
	}
	fmt.Fprintln(w, hr)
	fmt.Fprintf(w, "%s  %s\n", label, r.FormedAt)
	fmt.Fprintf(w, "Leader : %-30s  Size: %d\n", r.Leader, r.PartySize)
	if r.FailureMsg != "" {
		fmt.Fprintf(w, "Failure: %s\n", r.FailureMsg)
	}
	fmt.Fprintln(w, "Members:")
	for _, m := range r.Members {
		delta := m.TimeDelta
		if delta == "" {
			delta = "—"
		}
		fmt.Fprintf(w, "  %-32s  match=%-38s  +%s\n", m.Username, m.MatchID, delta)
	}
	fmt.Fprintln(w)
}

// ── main ──────────────────────────────────────────────────────────────────────

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

// run is the testable entry point. Returns an exit code.
func run(args []string, stdout, stderr io.Writer) int {
	var (
		jsonOut   bool
		sinceStr  string
		verbose   bool
		lifecycle bool
		outdir    string
		summary   bool
	)

	fs := flag.NewFlagSet("party-split-detector", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.BoolVar(&jsonOut, "json", false, "Output as JSON lines instead of human-readable")
	fs.StringVar(&sinceStr, "since", "", "Only process entries from the last N (e.g. 24h)")
	fs.BoolVar(&verbose, "verbose", false, "Show all party events, not just failures")
	fs.BoolVar(&lifecycle, "lifecycle", false, "Full party lifecycle reconstruction mode")
	fs.StringVar(&outdir, "outdir", "", "Write per-party JSONL files to this directory (lifecycle mode)")
	fs.BoolVar(&summary, "summary", false, "Print summary statistics after processing (lifecycle mode)")

	fs.Usage = func() {
		fmt.Fprintf(stderr, "Usage: party-split-detector [flags] <logfile...>\n\n")
		fmt.Fprintf(stderr, "Modes:\n")
		fmt.Fprintf(stderr, "  (default)     Original split detection (backward compatible)\n")
		fmt.Fprintf(stderr, "  --lifecycle   Full party lifecycle reconstruction\n\n")
		fmt.Fprintf(stderr, "Flags:\n")
		fs.PrintDefaults()
	}

	if err := fs.Parse(args); err != nil {
		return 1
	}

	if fs.NArg() == 0 {
		fs.Usage()
		return 1
	}

	// Validate lifecycle-only flags.
	if !lifecycle {
		if outdir != "" {
			fmt.Fprintf(stderr, "error: -outdir requires -lifecycle\n")
			return 1
		}
		if summary {
			fmt.Fprintf(stderr, "error: -summary requires -lifecycle\n")
			return 1
		}
	}

	var since time.Time
	if sinceStr != "" {
		dur, err := time.ParseDuration(sinceStr)
		if err != nil {
			fmt.Fprintf(stderr, "invalid -since %q: %v\n", sinceStr, err)
			return 1
		}
		since = time.Now().Add(-dur)
	}

	if lifecycle {
		return runLifecycle(fs.Args(), stdout, stderr, since, jsonOut, verbose, outdir, summary)
	}
	return runSplitDetect(fs.Args(), stdout, stderr, since, jsonOut, verbose)
}

// runSplitDetect is the original split-detection mode, fully backward compatible.
func runSplitDetect(files []string, stdout, stderr io.Writer, since time.Time, jsonOut, verbose bool) int {
	st := newState(since, verbose, jsonOut)

	for _, path := range files {
		if err := processFile(path, st); err != nil {
			fmt.Fprintf(stderr, "error: %s: %v\n", path, err)
		}
	}

	st.flushAll()

	if len(st.splits) == 0 {
		if verbose {
			fmt.Fprintln(stdout, "No party events found.")
		} else {
			fmt.Fprintln(stdout, "No party splits detected.")
		}
		return 0
	}

	for _, r := range st.splits {
		if jsonOut {
			b, _ := json.Marshal(r)
			fmt.Fprintln(stdout, string(b))
		} else {
			printHumanTo(stdout, r)
		}
	}
	return 0
}

// runLifecycle is the new lifecycle reconstruction mode.
// It processes files through the state machine and produces structured output.
// Until Agent B's state machine is integrated, this provides the I/O skeleton
// that will be wired up in the integration pass.
func runLifecycle(files []string, stdout, stderr io.Writer, since time.Time, jsonOut, verbose bool, outdir string, showSummary bool) int {
	// Phase 1: Process files. The integration agent will wire this to
	// Agent B's state machine. For now, we read and parse events.
	results, err := processLifecycleFiles(files, since, verbose)
	if err != nil {
		fmt.Fprintf(stderr, "error: %v\n", err)
		return 1
	}

	if len(results) == 0 {
		if verbose {
			fmt.Fprintln(stdout, "No party lifecycle events found.")
		} else {
			fmt.Fprintln(stdout, "No party lifecycle results.")
		}
		return 0
	}

	// Phase 2: Output.
	if outdir != "" {
		if err := WriteOutputDir(outdir, results); err != nil {
			fmt.Fprintf(stderr, "error writing output: %v\n", err)
			return 1
		}
		fmt.Fprintf(stdout, "Wrote %d party files to %s\n", len(results), outdir)
	} else {
		// Write to stdout.
		if jsonOut {
			if err := WriteLifecycleJSONL(stdout, results); err != nil {
				fmt.Fprintf(stderr, "error: %v\n", err)
				return 1
			}
		} else {
			WriteLifecycleHuman(stdout, results)
		}
	}

	if showSummary {
		WriteSummaryStats(stdout, results)
	}

	return 0
}

// processLifecycleFiles reads JSONL log files, parses events, drives the party
// lifecycle state machine, and returns completed party results for output.
func processLifecycleFiles(files []string, since time.Time, verbose bool) ([]PartyResult, error) {
	tracker := NewPartyTracker()
	var lastTS time.Time

	for _, path := range files {
		r, err := openReader(path)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}

		scanner := bufio.NewScanner(r)
		buf := make([]byte, 64*1024)
		scanner.Buffer(buf, 16*1024*1024)

		for scanner.Scan() {
			line := scanner.Text()
			if len(line) == 0 {
				continue
			}
			var raw map[string]json.RawMessage
			if err := json.Unmarshal([]byte(line), &raw); err != nil {
				continue
			}

			// Apply time filter.
			if !since.IsZero() {
				ts, ok := parseTS(raw)
				if !ok {
					continue
				}
				if ts.Before(since) {
					continue
				}
			}

			evt := ParseEvent(raw, line)
			if evt == nil {
				continue
			}

			// Track the latest timestamp for flush.
			evtTS := evt.EventTimestamp()
			if evtTS.After(lastTS) {
				lastTS = evtTS
			}

			// Feed into the state machine. All parsed events satisfy
			// LifecycleEvent via the bridge methods on baseEvent and
			// identityFields.
			tracker.ProcessEvent(evt.(LifecycleEvent))

			// Periodic tick to flush old parties and reclaim memory.
			tracker.Tick(evtTS)
		}
		if err := scanner.Err(); err != nil {
			r.Close()
			return nil, fmt.Errorf("%s: scan: %w", path, err)
		}
		r.Close()
	}

	// Flush remaining active parties.
	if lastTS.IsZero() {
		lastTS = time.Now()
	}
	tracker.FlushAll(lastTS)

	// Convert completed parties to PartyResult interface.
	completed := tracker.Completed()
	results := make([]PartyResult, len(completed))
	for i, pl := range completed {
		results[i] = pl
	}

	return results, nil
}
