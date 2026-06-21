// Command logreplay extracts events for a single user from a nakama JSONL log
// file, anonymises identifying fields deterministically, and writes an
// anonymised JSONL fixture suitable for replay-based debugging.
package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"regexp"
	"strings"
)

// ---------------------------------------------------------------------------
// CLI flags
// ---------------------------------------------------------------------------

var (
	flagLogfile = flag.String("logfile", "", "path to JSONL log file (required)")
	flagUID     = flag.String("uid", "", "user ID to extract (required)")
	flagOut     = flag.String("out", "", "output JSONL file path (required)")
	flagWindow  = flag.String("window", "", "time window filter, e.g. 20:22-20:24")
)

// ---------------------------------------------------------------------------
// Anonymiser — deterministic, counter-based remapping
// ---------------------------------------------------------------------------

type anonymiser struct {
	uuids      map[string]string
	uuidCount  int
	usernames  map[string]string
	userCount  int
	evrIDs     map[string]string
	evrCount   int
	dispNames  map[string]string
	dispCount  int
	clientIPs  map[string]string
	ipCount    int
	discordIDs map[string]string
}

func newAnonymiser() *anonymiser {
	return &anonymiser{
		uuids:      make(map[string]string),
		usernames:  make(map[string]string),
		evrIDs:     make(map[string]string),
		dispNames:  make(map[string]string),
		clientIPs:  make(map[string]string),
		discordIDs: make(map[string]string),
	}
}

func (a *anonymiser) anonUUID(real string) string {
	if real == "" {
		return ""
	}
	if v, ok := a.uuids[real]; ok {
		return v
	}
	a.uuidCount++
	fake := fmt.Sprintf("00000000-0000-0000-0000-%012d", a.uuidCount)
	a.uuids[real] = fake
	return fake
}

func (a *anonymiser) anonUsername(real string) string {
	if real == "" {
		return ""
	}
	if v, ok := a.usernames[real]; ok {
		return v
	}
	a.userCount++
	// player_A, player_B, ...
	letter := string(rune('A' - 1 + a.userCount))
	if a.userCount > 26 {
		letter = fmt.Sprintf("%d", a.userCount)
	}
	fake := "player_" + letter
	a.usernames[real] = fake
	return fake
}

func (a *anonymiser) anonEvrID(real string) string {
	if real == "" {
		return ""
	}
	if v, ok := a.evrIDs[real]; ok {
		return v
	}
	a.evrCount++
	fake := fmt.Sprintf("OVR-ORG-%05d", 10000+a.evrCount)
	a.evrIDs[real] = fake
	return fake
}

func (a *anonymiser) anonDisplayName(real string) string {
	if real == "" {
		return ""
	}
	if v, ok := a.dispNames[real]; ok {
		return v
	}
	a.dispCount++
	fake := fmt.Sprintf("User_%d", a.dispCount)
	a.dispNames[real] = fake
	return fake
}

func (a *anonymiser) anonClientIP(real string) string {
	if real == "" {
		return ""
	}
	if v, ok := a.clientIPs[real]; ok {
		return v
	}
	a.ipCount++
	fake := fmt.Sprintf("10.0.0.%d", a.ipCount)
	a.clientIPs[real] = fake
	return fake
}

// replaceInlineIDs replaces UUID and EVR-ID patterns that appear inside
// arbitrary string values (e.g. "request", "message" fields).
func (a *anonymiser) replaceInlineIDs(s string) string {
	s = reUUID.ReplaceAllStringFunc(s, func(m string) string {
		return a.anonUUID(m)
	})
	s = reEvrID.ReplaceAllStringFunc(s, func(m string) string {
		return a.anonEvrID(m)
	})
	return s
}

var (
	reUUID  = regexp.MustCompile(`[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}`)
	reEvrID = regexp.MustCompile(`OVR-ORG-\d+`)
)

// uuidFields are top-level keys whose values are UUIDs to anonymise.
var uuidFields = []string{
	"uid", "sid", "login_sid", "loginsid", "match_id", "mid",
	"party_id", "operator_id",
}

// anonymiseLine rewrites identifying fields in a parsed log line.
func (a *anonymiser) anonymiseLine(m map[string]any) {
	for _, k := range uuidFields {
		if v, ok := m[k].(string); ok && v != "" {
			m[k] = a.anonUUID(v)
		}
	}
	if v, ok := m["username"].(string); ok && v != "" {
		m["username"] = a.anonUsername(v)
	}
	if v, ok := m["evrid"].(string); ok && v != "" {
		m["evrid"] = a.anonEvrID(v)
	}
	if v, ok := m["display_name"].(string); ok && v != "" {
		m["display_name"] = a.anonDisplayName(v)
	}
	if v, ok := m["client_ip"].(string); ok && v != "" {
		m["client_ip"] = a.anonClientIP(v)
	}

	// Strip Discord IDs entirely.
	delete(m, "discord_id")
	delete(m, "discordId")

	// Replace inline UUIDs/EVR-IDs in string fields that carry payloads.
	for _, k := range []string{"request", "message", "request_type"} {
		if v, ok := m[k].(string); ok && v != "" {
			m[k] = a.replaceInlineIDs(v)
		}
	}
}

// ---------------------------------------------------------------------------
// Event filter
// ---------------------------------------------------------------------------

// isReplayRelevant returns true if the log line should be included in
// the replay fixture.
func isReplayRelevant(m map[string]any) bool {
	msg, _ := m["msg"].(string)
	if msg == "" {
		return false
	}

	// Exact matches.
	switch msg {
	case "Received message",
		"Player match lifecycle transition",
		"Illegal player match lifecycle transition",
		"Sending messages.",
		"Player joining the match.",
		"Player leaving the match.":
		return true
	}

	// Prefix match for "Sending *evr.*".
	if strings.HasPrefix(msg, "Sending ") {
		return true
	}

	// Substring matches.
	for _, sub := range []string{
		"Joined party group",
		"Social lobby search",
		"Lobby find complete",
		"Joined entrant",
		"Authorized access",
		"Leader is currently matchmaking",
		"Already in leader's match",
	} {
		if strings.Contains(msg, sub) {
			return true
		}
	}

	return false
}

// direction classifies a log line for the _direction field.
func direction(m map[string]any) string {
	msg, _ := m["msg"].(string)
	if msg == "Received message" {
		return "in"
	}
	if strings.HasPrefix(msg, "Sending") {
		return "out"
	}
	return "internal"
}

// ---------------------------------------------------------------------------
// Time window parsing
// ---------------------------------------------------------------------------

// parseWindow splits a "HH:MM-HH:MM" window into start/end strings.
// Returns ("", "", false) when no window is set.
func parseWindow(w string) (start, end string, ok bool) {
	if w == "" {
		return "", "", false
	}
	parts := strings.SplitN(w, "-", 2)
	if len(parts) != 2 {
		return "", "", false
	}
	return strings.TrimSpace(parts[0]), strings.TrimSpace(parts[1]), true
}

// inWindow checks whether the timestamp in a log line falls within the
// configured window. The timestamp field is expected to be an ISO-8601
// string; the window is compared against the "HH:MM" portion.
func inWindow(m map[string]any, start, end string) bool {
	ts, _ := m["ts"].(string)
	if ts == "" {
		// Try alternate key.
		ts, _ = m["T"].(string)
	}
	if ts == "" {
		ts, _ = m["time"].(string)
	}
	if ts == "" {
		return true // no timestamp — include by default
	}

	// Extract HH:MM — look for "T" separator in ISO-8601.
	idx := strings.IndexByte(ts, 'T')
	if idx < 0 {
		// Maybe space-separated.
		idx = strings.IndexByte(ts, ' ')
	}
	if idx < 0 {
		return true
	}
	timePart := ts[idx+1:]
	if len(timePart) < 5 {
		return true
	}
	hhmm := timePart[:5] // "HH:MM"
	return hhmm >= start && hhmm <= end
}

// ---------------------------------------------------------------------------
// Main
// ---------------------------------------------------------------------------

func main() {
	flag.Parse()

	if *flagLogfile == "" || *flagUID == "" || *flagOut == "" {
		fmt.Fprintf(os.Stderr, "Usage: logreplay -logfile <path> -uid <id> -out <path> [-window HH:MM-HH:MM]\n")
		os.Exit(1)
	}

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))

	// Open input.
	f, err := os.Open(*flagLogfile)
	if err != nil {
		logger.Error("open logfile", "error", err)
		os.Exit(1)
	}
	defer f.Close()

	// Open output.
	out, err := os.Create(*flagOut)
	if err != nil {
		logger.Error("create output", "error", err)
		os.Exit(1)
	}
	defer out.Close()

	// Parse optional time window.
	winStart, winEnd, hasWindow := parseWindow(*flagWindow)
	if *flagWindow != "" && !hasWindow {
		logger.Error("invalid window format, expected HH:MM-HH:MM", "window", *flagWindow)
		os.Exit(1)
	}

	anon := newAnonymiser()

	scanner := bufio.NewScanner(f)
	// Some log lines (e.g. match built events) exceed the default 64 KiB.
	const maxLine = 4 * 1024 * 1024 // 4 MiB
	scanner.Buffer(make([]byte, 0, maxLine), maxLine)

	var totalLines, matchedLines int
	enc := json.NewEncoder(out)

	for scanner.Scan() {
		totalLines++
		line := scanner.Bytes()

		var m map[string]any
		if err := json.Unmarshal(line, &m); err != nil {
			// Skip non-JSON lines (e.g. startup banners).
			continue
		}

		// UID filter — check uid field.
		uid, _ := m["uid"].(string)
		if uid != *flagUID {
			continue
		}

		// Time window filter.
		if hasWindow && !inWindow(m, winStart, winEnd) {
			continue
		}

		// Event relevance filter.
		if !isReplayRelevant(m) {
			continue
		}

		// Classify direction.
		m["_direction"] = direction(m)

		// Anonymise.
		anon.anonymiseLine(m)

		if err := enc.Encode(m); err != nil {
			logger.Error("write output line", "error", fmt.Errorf("encode line %d: %w", totalLines, err))
			os.Exit(1)
		}
		matchedLines++
	}

	if err := scanner.Err(); err != nil {
		logger.Error("scan error", "error", fmt.Errorf("reading logfile: %w", err))
		os.Exit(1)
	}

	logger.Info("done",
		"total_lines", totalLines,
		"matched_lines", matchedLines,
		"output", *flagOut,
	)
}
