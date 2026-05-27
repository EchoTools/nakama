// output.go provides output formatting, file writing, and statistics for the
// party lifecycle reconstructor.
//
// This IS the output and presentation layer for party lifecycle analysis.
// It accepts PartyResult values from the state machine (Agent B) and renders
// them to JSONL files, human-readable terminal output, or summary statistics.
//
// This is NOT the state machine, not the event parser, and not the correlator.
// It owns no analysis logic. It transforms completed results into output.
//
// Determinism: all output is sorted by timestamp, then party ID.
// Filenames use sanitized party IDs (replace / and . with _).
package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// ── Integration interfaces ──────────────────────────────────────────────────
// These interfaces define the contract between the state machine (Agent B)
// and the output layer. The integration agent will make Agent B's concrete
// types satisfy these interfaces.

// PartyResult is what the state machine produces for each completed party.
type PartyResult interface {
	GetPartyID() string
	GetFormedAt() time.Time
	GetLeader() string
	GetMembers() []string
	GetOutcome() string
	GetOutcomeReason() string
	GetDuration() time.Duration
	GetEvents() []OutputEvent
	GetMatchIDs() map[string][]string // username -> []matchID
}

// OutputEvent is a single event in a party's timeline.
type OutputEvent interface {
	GetTimestamp() time.Time
	GetEventType() string
	GetUsername() string
	GetRawLine() string
	GetDetails() map[string]string
}

// ── Outcome helpers ─────────────────────────────────────────────────────────
// Canonical Outcome type and constants live in statemachine.go.

// isFailureOutcome returns true for outcomes that represent party failures.
func isFailureOutcome(outcome string) bool {
	switch outcome {
	case OutcomeSplit.String(), OutcomeDegraded.String():
		return true
	default:
		return false
	}
}

// ── JSONL wire types ────────────────────────────────────────────────────────
// These are the serialized forms written to output files.

// eventLine is one line in a per-party JSONL file.
type eventLine struct {
	Timestamp string            `json:"ts"`
	Event     string            `json:"event"`
	UID       string            `json:"uid,omitempty"`
	SID       string            `json:"sid,omitempty"`
	Username  string            `json:"username,omitempty"`
	PartyID   string            `json:"party_id,omitempty"`
	RawLine   string            `json:"raw"`
	Details   map[string]string `json:"details,omitempty"`
}

// summaryLine is one line in summary.jsonl (and failures.jsonl).
type summaryLine struct {
	PartyID       string              `json:"party_id"`
	FormedAt      string              `json:"formed_at"`
	Leader        string              `json:"leader"`
	Size          int                 `json:"size"`
	Outcome       string              `json:"outcome"`
	FailureReason string              `json:"failure_reason,omitempty"`
	DurationMs    int64               `json:"duration_ms"`
	Members       []string            `json:"members"`
	MatchIDs      map[string][]string `json:"match_ids,omitempty"`
}

// ── Statistics ──────────────────────────────────────────────────────────────

// lifecycleStats holds aggregated statistics across all party results.
type lifecycleStats struct {
	Total           int
	ByOutcome       map[string]int
	FailureReasons  map[string]int
	SplitsByUser    map[string]int
}

func newLifecycleStats() *lifecycleStats {
	return &lifecycleStats{
		ByOutcome:      make(map[string]int),
		FailureReasons: make(map[string]int),
		SplitsByUser:   make(map[string]int),
	}
}

func (s *lifecycleStats) add(r PartyResult) {
	s.Total++
	s.ByOutcome[r.GetOutcome()]++

	reason := r.GetOutcomeReason()
	if reason != "" && isFailureOutcome(r.GetOutcome()) {
		s.FailureReasons[reason]++
	}

	if isFailureOutcome(r.GetOutcome()) {
		for _, m := range r.GetMembers() {
			if m != r.GetLeader() {
				s.SplitsByUser[m]++
			}
		}
	}
}

// ── Sorting ─────────────────────────────────────────────────────────────────

// sortResults sorts party results by formed_at timestamp, then by party ID
// for deterministic output.
func sortResults(results []PartyResult) {
	sort.SliceStable(results, func(i, j int) bool {
		ti := results[i].GetFormedAt()
		tj := results[j].GetFormedAt()
		if !ti.Equal(tj) {
			return ti.Before(tj)
		}
		return results[i].GetPartyID() < results[j].GetPartyID()
	})
}

// ── Filename sanitization ───────────────────────────────────────────────────

// sanitizePartyID replaces characters unsafe for filenames.
func sanitizePartyID(id string) string {
	r := strings.NewReplacer("/", "_", ".", "_")
	return r.Replace(id)
}

// ── summaryLine builder ─────────────────────────────────────────────────────

func buildSummaryLine(r PartyResult) summaryLine {
	matchIDs := r.GetMatchIDs()
	if len(matchIDs) == 0 {
		matchIDs = nil
	}
	return summaryLine{
		PartyID:       r.GetPartyID(),
		FormedAt:      r.GetFormedAt().UTC().Format(time.RFC3339),
		Leader:        r.GetLeader(),
		Size:          len(r.GetMembers()),
		Outcome:       r.GetOutcome(),
		FailureReason: r.GetOutcomeReason(),
		DurationMs:    r.GetDuration().Milliseconds(),
		Members:       r.GetMembers(),
		MatchIDs:      matchIDs,
	}
}

// ── Per-party JSONL writer ──────────────────────────────────────────────────

// writePartyJSONL writes every event for a party as one JSON line per event.
func writePartyJSONL(w io.Writer, r PartyResult) error {
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	for _, ev := range r.GetEvents() {
		line := eventLine{
			Timestamp: ev.GetTimestamp().UTC().Format(time.RFC3339Nano),
			Event:     ev.GetEventType(),
			Username:  ev.GetUsername(),
			PartyID:   r.GetPartyID(),
			RawLine:   ev.GetRawLine(),
		}
		details := ev.GetDetails()
		if uid, ok := details["uid"]; ok {
			line.UID = uid
		}
		if sid, ok := details["sid"]; ok {
			line.SID = sid
		}
		// Carry remaining details, excluding uid/sid already promoted.
		if len(details) > 0 {
			filtered := make(map[string]string, len(details))
			for k, v := range details {
				if k != "uid" && k != "sid" {
					filtered[k] = v
				}
			}
			if len(filtered) > 0 {
				line.Details = filtered
			}
		}
		if err := enc.Encode(line); err != nil {
			return fmt.Errorf("encode event for party %s: %w", r.GetPartyID(), err)
		}
	}
	return nil
}

// ── Directory output ────────────────────────────────────────────────────────

// WriteOutputDir writes per-party JSONL files, summary.jsonl, and
// failures.jsonl to the given directory. The directory is created if it does
// not exist.
func WriteOutputDir(dir string, results []PartyResult) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create output dir: %w", err)
	}

	sortResults(results)

	// Per-party files.
	for _, r := range results {
		name := sanitizePartyID(r.GetPartyID()) + ".jsonl"
		path := filepath.Join(dir, name)
		f, err := os.Create(path)
		if err != nil {
			return fmt.Errorf("create %s: %w", path, err)
		}
		if err := writePartyJSONL(f, r); err != nil {
			f.Close()
			return err
		}
		if err := f.Close(); err != nil {
			return fmt.Errorf("close %s: %w", path, err)
		}
	}

	// summary.jsonl — one line per party.
	summaryPath := filepath.Join(dir, "summary.jsonl")
	sf, err := os.Create(summaryPath)
	if err != nil {
		return fmt.Errorf("create summary: %w", err)
	}
	defer sf.Close()

	enc := json.NewEncoder(sf)
	enc.SetEscapeHTML(false)
	for _, r := range results {
		if err := enc.Encode(buildSummaryLine(r)); err != nil {
			return fmt.Errorf("encode summary: %w", err)
		}
	}

	// failures.jsonl — only parties with SPLIT or DEGRADED outcome.
	failPath := filepath.Join(dir, "failures.jsonl")
	ff, err := os.Create(failPath)
	if err != nil {
		return fmt.Errorf("create failures: %w", err)
	}
	defer ff.Close()

	fenc := json.NewEncoder(ff)
	fenc.SetEscapeHTML(false)
	for _, r := range results {
		if isFailureOutcome(r.GetOutcome()) {
			if err := fenc.Encode(buildSummaryLine(r)); err != nil {
				return fmt.Errorf("encode failure: %w", err)
			}
		}
	}

	return nil
}

// ── Human-readable lifecycle output ─────────────────────────────────────────

const lifecycleHR = "═══════════════════════════════════════════════════════════════════════════════"

// WriteLifecycleHuman writes human-readable lifecycle output for a set of
// party results to the given writer.
func WriteLifecycleHuman(w io.Writer, results []PartyResult) {
	sortResults(results)
	for _, r := range results {
		writeOneLifecycleHuman(w, r)
	}
}

func writeOneLifecycleHuman(w io.Writer, r PartyResult) {
	outcome := r.GetOutcome()
	label := "PARTY " + outcome
	reason := r.GetOutcomeReason()

	fmt.Fprintln(w, lifecycleHR)

	reasonSuffix := ""
	if reason != "" {
		reasonSuffix = fmt.Sprintf(" (%s)", reason)
	}

	fmt.Fprintf(w, "%s  %s\n", label, r.GetFormedAt().UTC().Format(time.RFC3339))
	fmt.Fprintf(w, "Party:   %s\n", r.GetPartyID())
	fmt.Fprintf(w, "Leader:  %-30s  Size: %d\n", r.GetLeader(), len(r.GetMembers()))
	fmt.Fprintf(w, "Outcome: %s%s\n", outcome, reasonSuffix)
	fmt.Fprintf(w, "Duration: %s\n", formatDuration(r.GetDuration()))

	// Timeline.
	events := r.GetEvents()
	if len(events) > 0 {
		fmt.Fprintln(w, "")
		fmt.Fprintln(w, "Timeline:")
		base := r.GetFormedAt()
		for _, ev := range events {
			offset := ev.GetTimestamp().Sub(base)
			detailStr := formatEventDetails(ev)
			fmt.Fprintf(w, "  +%-8s %-25s %-20s %s\n",
				formatDuration(offset),
				ev.GetEventType(),
				ev.GetUsername(),
				detailStr,
			)
		}
	}

	// Members and their match outcomes.
	matchIDs := r.GetMatchIDs()
	if len(matchIDs) > 0 {
		fmt.Fprintln(w, "")
		fmt.Fprintln(w, "Members:")
		leader := r.GetLeader()
		members := r.GetMembers()

		// Determine the "correct" match (leader's first match).
		leaderMatches := matchIDs[leader]
		correctMatch := ""
		if len(leaderMatches) > 0 {
			correctMatch = leaderMatches[0]
		}

		for _, m := range members {
			matches := matchIDs[m]
			matchStr := "no match"
			status := "?"
			if len(matches) > 0 {
				matchStr = "match=" + matches[0]
				if correctMatch != "" && matches[0] == correctMatch {
					status = "✓"
				} else if correctMatch != "" {
					status = "✗ (split)"
				} else {
					status = "✓"
				}
			} else {
				status = "✗ (no match)"
			}
			fmt.Fprintf(w, "  %-20s → %-44s %s\n", m, matchStr, status)
		}
	}

	fmt.Fprintln(w, lifecycleHR)
	fmt.Fprintln(w, "")
}

// ── Lifecycle JSONL output (stdout, no outdir) ──────────────────────────────

// WriteLifecycleJSONL writes one summary JSON line per party to the writer.
func WriteLifecycleJSONL(w io.Writer, results []PartyResult) error {
	sortResults(results)
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	for _, r := range results {
		if err := enc.Encode(buildSummaryLine(r)); err != nil {
			return fmt.Errorf("encode lifecycle json: %w", err)
		}
	}
	return nil
}

// ── Summary statistics ──────────────────────────────────────────────────────

// WriteSummaryStats writes the aggregated summary statistics in
// human-readable form.
func WriteSummaryStats(w io.Writer, results []PartyResult) {
	stats := newLifecycleStats()
	for _, r := range results {
		stats.add(r)
	}

	fmt.Fprintln(w, "")
	fmt.Fprintln(w, "Party Lifecycle Summary")
	fmt.Fprintln(w, "═══════════════════════")

	fmt.Fprintf(w, "Total parties analyzed:  %d\n", stats.Total)

	// Fixed outcome order for deterministic output.
	outcomes := []string{
		OutcomeConverged.String(),
		OutcomeSplit.String(),
		OutcomeDegraded.String(),
		OutcomeAbandoned.String(),
	}
	for _, o := range outcomes {
		count := stats.ByOutcome[o]
		if count == 0 && stats.Total == 0 {
			continue
		}
		pct := 0.0
		if stats.Total > 0 {
			pct = float64(count) / float64(stats.Total) * 100
		}
		fmt.Fprintf(w, "  %-22s %5d  (%4.1f%%)\n", o+":", count, pct)
	}

	// Failure reasons, sorted by count descending, then alphabetically.
	if len(stats.FailureReasons) > 0 {
		fmt.Fprintln(w, "")
		fmt.Fprintln(w, "Top failure reasons:")
		type kv struct {
			key   string
			count int
		}
		sorted := make([]kv, 0, len(stats.FailureReasons))
		for k, v := range stats.FailureReasons {
			sorted = append(sorted, kv{k, v})
		}
		sort.Slice(sorted, func(i, j int) bool {
			if sorted[i].count != sorted[j].count {
				return sorted[i].count > sorted[j].count
			}
			return sorted[i].key < sorted[j].key
		})
		for _, r := range sorted {
			fmt.Fprintf(w, "  %-40s %5d\n", r.key+":", r.count)
		}
	}

	// Most affected users, sorted by split count descending, then alphabetically.
	if len(stats.SplitsByUser) > 0 {
		fmt.Fprintln(w, "")
		fmt.Fprintln(w, "Most affected users:")
		type kv struct {
			key   string
			count int
		}
		sorted := make([]kv, 0, len(stats.SplitsByUser))
		for k, v := range stats.SplitsByUser {
			sorted = append(sorted, kv{k, v})
		}
		sort.Slice(sorted, func(i, j int) bool {
			if sorted[i].count != sorted[j].count {
				return sorted[i].count > sorted[j].count
			}
			return sorted[i].key < sorted[j].key
		})
		for _, u := range sorted {
			fmt.Fprintf(w, "  %-30s %5d splits\n", u.key+":", u.count)
		}
	}

	fmt.Fprintln(w, "")
}

// ── Helpers ─────────────────────────────────────────────────────────────────

// formatDuration returns a concise human duration like "19.2s" or "0.154s".
func formatDuration(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	sec := d.Seconds()
	if sec < 0.001 {
		return "0.000s"
	}
	if sec < 10 {
		return fmt.Sprintf("%.3fs", sec)
	}
	if sec < 100 {
		return fmt.Sprintf("%.1fs", sec)
	}
	return fmt.Sprintf("%.0fs", sec)
}

// formatEventDetails extracts a concise detail string from an event's
// details map for the timeline view.
func formatEventDetails(ev OutputEvent) string {
	details := ev.GetDetails()
	if len(details) == 0 {
		return ""
	}
	// Build a stable key-sorted representation.
	keys := make([]string, 0, len(details))
	for k := range details {
		if k == "uid" || k == "sid" {
			continue
		}
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, k+"="+details[k])
	}
	return strings.Join(parts, " ")
}
