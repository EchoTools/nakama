// parser.go converts raw JSONL map entries into typed Event values.
//
// ParseEvent is the single entry point. It examines the "msg" field of a
// parsed JSON log line and dispatches to the appropriate event constructor.
// Lines that are not party-relevant return nil.
//
// This IS a fast, allocation-conscious parser for high-volume log processing.
// This is NOT a validator — it extracts what is present and leaves missing
// optional fields at their zero values. Callers decide what is required.
package main

import (
	"encoding/json"
	"strings"
)

// getInt extracts an integer field from a raw JSON map. Returns 0 if the key
// is absent or not a number. JSON numbers unmarshal to float64 by default, so
// we go through float64 and truncate.
func getInt(raw map[string]json.RawMessage, key string) int {
	v, ok := raw[key]
	if !ok {
		return 0
	}
	var f float64
	if err := json.Unmarshal(v, &f); err != nil {
		return 0
	}
	return int(f)
}

// getRaw extracts a json.RawMessage for a key, returning nil if absent. This
// is used for complex nested fields (entrants, presences) where we preserve
// the original JSON rather than parsing into a Go struct.
func getRaw(raw map[string]json.RawMessage, key string) json.RawMessage {
	v, ok := raw[key]
	if !ok {
		return nil
	}
	return v
}

// knownMessages is the set of msg strings that map to first-class event types.
// Used to decide whether a party-mentioning line is UnknownPartyEvent.
var knownMessages = map[string]bool{
	"Party is ready":       true,
	"Joined party group":   true,
	"User is member of party, attempting to follow leader":                                            true,
	"Leader is not in a match, falling through to normal find":                                        true,
	"Follower in social mode, finding social lobby independently (party reservations will converge)":  true,
	"Joining leader's lobby":                                                                          true,
	"Player joining the match.":                                                                       true,
	"Joined entrant.":                                                                                 true,
	"failed to join match":                                                                            true,
	"Building match":                                                                                  true,
	"Follower cannot join leader's match, releasing to independent matchmaking":                       true,
	"Leader is in a non-joinable mode for party follow":                                               true,
	"Leader is in a non-joinable mode during poll":                                                    true,
	"Leader left match during poll":                                                                   true,
	"Failed to join leader's lobby":                                                                   true,
	"Failed priority join to leader's lobby":                                                          true,
	"Failed priority join to follower's lobby":                                                        true,
	"Created reconnect reservation for crashed player":                                                true,
	"Player leaving the match.":                                                                       true,
	"Player voluntarily left. No crash reservation needed.":                                           true,
	"Repaired split party team assignment":                                                            true,
	"Matchmaking ticket added":                                                                        true,
	"entrant metadata not found, falling back to fresh metadata":                                      true,
	"Sending *evr.LobbySessionFailurev4 message":                                                     true,
	"Lobby find complete":                                                                             true,
	"Matchmaking stream closed, canceling matchmaking":                                                true,
	"Unexpected error while finding match":                                                            true,
	"Player reconnecting from crash. Restoring role alignment.":                                       true,
}

// ParseEvent converts a raw JSON log line into a typed Event.
//
// Returns nil if the line is not relevant to party lifecycle. The raw map
// should come from json.Unmarshal of a single JSONL line; line is the original
// text and is stored on the returned event for traceability.
//
// Performance: one map lookup on msg for the fast path, no allocations for
// non-matching lines.
func ParseEvent(raw map[string]json.RawMessage, line string) Event {
	msg := getString(raw, "msg")
	if msg == "" {
		return nil
	}

	ts, ok := parseTS(raw)
	if !ok {
		return nil
	}

	base := baseEvent{Timestamp: ts, RawLine: line}
	ident := identityFields{
		UID:      getString(raw, "uid"),
		SID:      getString(raw, "sid"),
		Username: getString(raw, "username"),
	}

	switch msg {

	case "Party is ready":
		return &PartyFormed{
			baseEvent:      base,
			identityFields: ident,
			LeaderSID:      getString(raw, "leader"),
			Size:           getInt(raw, "size"),
			Members:        getStrings(raw, "members"),
		}

	case "Joined party group":
		return &PartyJoined{
			baseEvent:      base,
			identityFields: ident,
			PartyID:        getString(raw, "partyID"),
		}

	case "User is member of party, attempting to follow leader":
		return &MemberFollowAttempt{
			baseEvent:      base,
			identityFields: ident,
		}

	case "Leader is not in a match, falling through to normal find":
		return &LeaderNotInMatch{
			baseEvent:      base,
			identityFields: ident,
		}

	case "Follower in social mode, finding social lobby independently (party reservations will converge)":
		return &SocialFallback{
			baseEvent:      base,
			identityFields: ident,
			Mode:           getString(raw, "mode"),
		}

	case "Joining leader's lobby":
		return &FollowLeaderLobby{
			baseEvent:      base,
			identityFields: ident,
			MatchID:        getString(raw, "mid"),
		}

	case "Player joining the match.":
		return &MatchJoinAttempt{
			baseEvent:      base,
			identityFields: ident,
			MatchID:        getString(raw, "mid"),
		}

	case "Joined entrant.":
		return &MatchJoined{
			baseEvent:      base,
			identityFields: ident,
			MatchID:        getString(raw, "mid"),
			Role:           getInt(raw, "role"),
		}

	case "failed to join match":
		return &MatchJoinFailed{
			baseEvent:      base,
			identityFields: ident,
			MatchID:        getString(raw, "mid"),
			Error:          getString(raw, "error"),
		}

	case "Building match":
		return &MatchBuilt{
			baseEvent: base,
			Entrants:  getRaw(raw, "entrants"),
			Module:    getString(raw, "module"),
		}

	case "Follower cannot join leader's match, releasing to independent matchmaking":
		return &FollowReleasedIndependent{
			baseEvent:      base,
			identityFields: ident,
			Mode:           getString(raw, "mode"),
		}

	case "Leader is in a non-joinable mode for party follow":
		return &LeaderNonJoinableMode{
			baseEvent:      base,
			identityFields: ident,
			Mode:           getString(raw, "mode"),
		}

	case "Leader is in a non-joinable mode during poll":
		return &LeaderNonJoinablePoll{
			baseEvent:      base,
			identityFields: ident,
			Mode:           getString(raw, "mode"),
		}

	case "Leader left match during poll":
		return &LeaderLeftDuringPoll{
			baseEvent:      base,
			identityFields: ident,
		}

	case "Failed to join leader's lobby":
		return &FailedJoinLeaderLobby{
			baseEvent:      base,
			identityFields: ident,
			Error:          getString(raw, "error"),
		}

	case "Failed priority join to leader's lobby":
		return &FailedPriorityJoinLeader{
			baseEvent:      base,
			identityFields: ident,
			Error:          getString(raw, "error"),
		}

	case "Failed priority join to follower's lobby":
		return &FailedPriorityJoinFollower{
			baseEvent:      base,
			identityFields: ident,
			Error:          getString(raw, "error"),
		}

	case "Created reconnect reservation for crashed player":
		return &CrashDetected{
			baseEvent:      base,
			identityFields: ident,
			MatchID:        getString(raw, "mid"),
		}

	case "Player leaving the match.":
		return &PlayerLeft{
			baseEvent:      base,
			identityFields: ident,
			Reason:         getInt(raw, "reason"),
		}

	case "Player voluntarily left. No crash reservation needed.":
		return &PlayerLeftVoluntary{
			baseEvent:      base,
			identityFields: ident,
		}

	case "Repaired split party team assignment":
		return &SplitRepaired{
			baseEvent: base,
			MatchID:   getString(raw, "mid"),
		}

	case "Matchmaking ticket added":
		return &MatchmakingTicketAdded{
			baseEvent:      base,
			identityFields: ident,
			Ticket:         getString(raw, "ticket"),
			Presences:      getRaw(raw, "presences"),
		}

	case "entrant metadata not found, falling back to fresh metadata":
		return &EntrantMetadataFallback{
			baseEvent:      base,
			identityFields: ident,
		}

	case "Sending *evr.LobbySessionFailurev4 message":
		return &LobbySessionFailure{
			baseEvent:      base,
			identityFields: ident,
		}

	case "Lobby find complete":
		return &LobbyFindComplete{
			baseEvent:      base,
			identityFields: ident,
			Mode:           getString(raw, "mode"),
		}

	case "Matchmaking stream closed, canceling matchmaking":
		return &MatchmakingStreamClosed{
			baseEvent:      base,
			identityFields: ident,
		}

	case "Unexpected error while finding match":
		return &UnexpectedError{
			baseEvent:      base,
			identityFields: ident,
			Error:          getString(raw, "error"),
		}

	case "Player reconnecting from crash. Restoring role alignment.":
		return &ReconnectRestore{
			baseEvent:      base,
			identityFields: ident,
		}
	}

	// Catch-all: if the message mentions "party" or "Party" and we did not
	// handle it above, capture it so we can discover new event types.
	if strings.Contains(msg, "party") || strings.Contains(msg, "Party") {
		return &UnknownPartyEvent{
			baseEvent: base,
			Msg:       msg,
			Fields:    raw,
		}
	}

	return nil
}
