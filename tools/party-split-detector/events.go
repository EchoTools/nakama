// events.go defines typed events for every Nakama log message relevant to
// party lifecycle reconstruction.
//
// Each event carries its parsed structured fields, a Timestamp, and the
// original RawLine so downstream consumers can always fall back to the source.
// The EventType() method returns a stable string identifier used for dispatch
// and filtering.
//
// This IS the canonical definition of party-relevant log events.
// This is NOT a general-purpose log parser -- it only covers messages that
// participate in party formation, matchmaking, match joins, crashes, and splits.
package main

import (
	"encoding/json"
	"fmt"
	"time"
)

// Event is the interface satisfied by every parsed party-lifecycle log event.
type Event interface {
	// EventType returns a stable identifier for the event kind (e.g.
	// "PartyFormed", "MatchJoined"). Used for switch dispatch and metrics.
	EventType() string

	// EventTimestamp returns the timestamp parsed from the log line.
	EventTimestamp() time.Time

	// EventRawLine returns the original JSONL line that produced this event.
	EventRawLine() string
}

// baseEvent contains fields common to every event. Embed it in concrete types.
type baseEvent struct {
	Timestamp time.Time `json:"timestamp"`
	RawLine   string    `json:"-"`
}

func (b baseEvent) EventTimestamp() time.Time { return b.Timestamp }
func (b baseEvent) EventRawLine() string      { return b.RawLine }

// LifecycleEvent bridge: baseEvent provides the timestamp and raw-line methods.
// Identity methods (GetSID, GetUID, GetUsername) live on identityFields to avoid
// ambiguous selectors when both are embedded. Event types that do NOT embed
// identityFields must add their own zero-value identity methods.
func (b baseEvent) GetTimestamp() time.Time { return b.Timestamp }
func (b baseEvent) GetRawLine() string      { return b.RawLine }

// identityFields holds the standard Nakama identity fields present on most log
// lines. Embedded by events that carry per-player context.
type identityFields struct {
	UID      string `json:"uid,omitempty"`
	SID      string `json:"sid,omitempty"`
	Username string `json:"username,omitempty"`
}

// LifecycleEvent bridge: identityFields overrides the zero-value methods from
// baseEvent for events that carry per-player context.
func (f identityFields) GetSID() string      { return f.SID }
func (f identityFields) GetUID() string      { return f.UID }
func (f identityFields) GetUsername() string  { return f.Username }

// ── Party Formation ──────────────────────────────────────────────────────────

// PartyFormed is emitted when a party declares itself ready for matchmaking.
// msg: "Party is ready"
type PartyFormed struct {
	baseEvent
	identityFields
	LeaderSID string   `json:"leader_sid"`
	Size      int      `json:"size"`
	Members   []string `json:"members"`
}

func (PartyFormed) EventType() string { return "PartyFormed" }

// PartyJoined is emitted when a player joins a party group.
// msg: "Joined party group"
type PartyJoined struct {
	baseEvent
	identityFields
	PartyID string `json:"party_id"`
}

func (PartyJoined) EventType() string { return "PartyJoined" }

// ── Leader-Follow Chain ──────────────────────────────────────────────────────

// MemberFollowAttempt fires when a party member begins the follow-leader flow.
// msg: "User is member of party, attempting to follow leader"
type MemberFollowAttempt struct {
	baseEvent
	identityFields
}

func (MemberFollowAttempt) EventType() string { return "MemberFollowAttempt" }

// LeaderNotInMatch fires when the leader has no active match for followers to
// join, causing a fallback to normal matchmaking.
// msg: "Leader is not in a match, falling through to normal find"
type LeaderNotInMatch struct {
	baseEvent
	identityFields
}

func (LeaderNotInMatch) EventType() string { return "LeaderNotInMatch" }

// SocialFallback fires when a follower in social mode independently searches
// for a lobby, relying on party reservations to converge later.
// msg: "Follower in social mode, finding social lobby independently (party reservations will converge)"
type SocialFallback struct {
	baseEvent
	identityFields
	Mode string `json:"mode,omitempty"`
}

func (SocialFallback) EventType() string { return "SocialFallback" }

// FollowLeaderLobby fires when a follower attempts to join the leader's
// current lobby.
// msg: "Joining leader's lobby"
type FollowLeaderLobby struct {
	baseEvent
	identityFields
	MatchID string `json:"match_id,omitempty"`
}

func (FollowLeaderLobby) EventType() string { return "FollowLeaderLobby" }

// ── Match Lifecycle ──────────────────────────────────────────────────────────

// MatchJoinAttempt fires when a player begins joining a match.
// msg: "Player joining the match."
type MatchJoinAttempt struct {
	baseEvent
	identityFields
	MatchID string `json:"match_id,omitempty"`
}

func (MatchJoinAttempt) EventType() string { return "MatchJoinAttempt" }

// MatchJoined fires when a player has been accepted into a match as an
// entrant.
// msg: "Joined entrant."
type MatchJoined struct {
	baseEvent
	identityFields
	MatchID string `json:"match_id,omitempty"`
	Role    int    `json:"role"`
}

func (MatchJoined) EventType() string { return "MatchJoined" }

// MatchJoinFailed fires when a player's attempt to join a match is rejected.
// msg: "failed to join match"
type MatchJoinFailed struct {
	baseEvent
	identityFields
	MatchID string `json:"match_id,omitempty"`
	Error   string `json:"error,omitempty"`
}

func (MatchJoinFailed) EventType() string { return "MatchJoinFailed" }

// MatchBuilt fires when a new match is constructed by the lobby builder.
// msg: "Building match"
type MatchBuilt struct {
	baseEvent
	Entrants json.RawMessage `json:"entrants,omitempty"`
	Module   string          `json:"module,omitempty"`
}

func (MatchBuilt) EventType() string { return "MatchBuilt" }
func (MatchBuilt) GetSID() string    { return "" }
func (MatchBuilt) GetUID() string    { return "" }
func (MatchBuilt) GetUsername() string { return "" }

// ── Follow Failures ──────────────────────────────────────────────────────────

// FollowReleasedIndependent fires when a follower cannot join the leader's
// match and is released to independent matchmaking.
// msg: "Follower cannot join leader's match, releasing to independent matchmaking"
type FollowReleasedIndependent struct {
	baseEvent
	identityFields
	Mode string `json:"mode,omitempty"`
}

func (FollowReleasedIndependent) EventType() string { return "FollowReleasedIndependent" }

// LeaderNonJoinableMode fires when the leader is in a game mode that
// followers cannot join (e.g. private match, different mode).
// msg: "Leader is in a non-joinable mode for party follow"
type LeaderNonJoinableMode struct {
	baseEvent
	identityFields
	Mode string `json:"mode,omitempty"`
}

func (LeaderNonJoinableMode) EventType() string { return "LeaderNonJoinableMode" }

// LeaderNonJoinablePoll fires during polling when the leader's mode is found
// to be non-joinable.
// msg: "Leader is in a non-joinable mode during poll"
type LeaderNonJoinablePoll struct {
	baseEvent
	identityFields
	Mode string `json:"mode,omitempty"`
}

func (LeaderNonJoinablePoll) EventType() string { return "LeaderNonJoinablePoll" }

// LeaderLeftDuringPoll fires when the leader leaves their match while a
// follower is polling to join.
// msg: "Leader left match during poll"
type LeaderLeftDuringPoll struct {
	baseEvent
	identityFields
}

func (LeaderLeftDuringPoll) EventType() string { return "LeaderLeftDuringPoll" }

// FailedJoinLeaderLobby fires when a follower's attempt to join the leader's
// lobby fails.
// msg: "Failed to join leader's lobby"
type FailedJoinLeaderLobby struct {
	baseEvent
	identityFields
	Error string `json:"error,omitempty"`
}

func (FailedJoinLeaderLobby) EventType() string { return "FailedJoinLeaderLobby" }

// FailedPriorityJoinLeader fires when a priority-join to the leader's lobby
// fails.
// msg: "Failed priority join to leader's lobby"
type FailedPriorityJoinLeader struct {
	baseEvent
	identityFields
	Error string `json:"error,omitempty"`
}

func (FailedPriorityJoinLeader) EventType() string { return "FailedPriorityJoinLeader" }

// FailedPriorityJoinFollower fires when a priority-join to the follower's
// lobby fails.
// msg: "Failed priority join to follower's lobby"
type FailedPriorityJoinFollower struct {
	baseEvent
	identityFields
	Error string `json:"error,omitempty"`
}

func (FailedPriorityJoinFollower) EventType() string { return "FailedPriorityJoinFollower" }

// ── Crash and Disconnect ─────────────────────────────────────────────────────

// CrashDetected fires when the server creates a reconnect reservation for a
// player that crashed mid-match.
// msg: "Created reconnect reservation for crashed player"
type CrashDetected struct {
	baseEvent
	identityFields
	MatchID string `json:"match_id,omitempty"`
}

func (CrashDetected) EventType() string { return "CrashDetected" }

// PlayerLeft fires when a player leaves a match for any reason (crash,
// voluntary, timeout, etc.).
// msg: "Player leaving the match."
type PlayerLeft struct {
	baseEvent
	identityFields
	Reason int `json:"reason"`
}

func (PlayerLeft) EventType() string { return "PlayerLeft" }

// PlayerLeftVoluntary fires when a player voluntarily leaves, confirming no
// crash reservation is needed.
// msg: "Player voluntarily left. No crash reservation needed."
type PlayerLeftVoluntary struct {
	baseEvent
	identityFields
}

func (PlayerLeftVoluntary) EventType() string { return "PlayerLeftVoluntary" }

// ReconnectRestore fires when a crashed player reconnects and their role
// alignment is restored.
// msg: "Player reconnecting from crash. Restoring role alignment."
type ReconnectRestore struct {
	baseEvent
	identityFields
}

func (ReconnectRestore) EventType() string { return "ReconnectRestore" }

// ── Repair ───────────────────────────────────────────────────────────────────

// SplitRepaired fires when the server detects and repairs a split party team
// assignment within a match.
// msg: "Repaired split party team assignment"
type SplitRepaired struct {
	baseEvent
	MatchID string `json:"match_id,omitempty"`
}

func (SplitRepaired) EventType() string { return "SplitRepaired" }
func (SplitRepaired) GetSID() string    { return "" }
func (SplitRepaired) GetUID() string    { return "" }
func (SplitRepaired) GetUsername() string { return "" }

// ── Matchmaking ──────────────────────────────────────────────────────────────

// MatchmakingTicketAdded fires when a matchmaking ticket is submitted.
// msg: "Matchmaking ticket added"
type MatchmakingTicketAdded struct {
	baseEvent
	identityFields
	Ticket    string          `json:"ticket,omitempty"`
	Presences json.RawMessage `json:"presences,omitempty"`
}

func (MatchmakingTicketAdded) EventType() string { return "MatchmakingTicketAdded" }

// MatchmakingStreamClosed fires when a matchmaking stream is closed, causing
// cancellation of any outstanding matchmaking request.
// msg: "Matchmaking stream closed, canceling matchmaking"
type MatchmakingStreamClosed struct {
	baseEvent
	identityFields
}

func (MatchmakingStreamClosed) EventType() string { return "MatchmakingStreamClosed" }

// ── Metadata and Completion ──────────────────────────────────────────────────

// EntrantMetadataFallback fires when the entrant's cached metadata is missing
// and the server falls back to fetching fresh metadata.
// msg: "entrant metadata not found, falling back to fresh metadata"
type EntrantMetadataFallback struct {
	baseEvent
	identityFields
}

func (EntrantMetadataFallback) EventType() string { return "EntrantMetadataFallback" }

// LobbySessionFailure fires when the server sends a failure message to the
// client.
// msg: "Sending *evr.LobbySessionFailurev4 message"
type LobbySessionFailure struct {
	baseEvent
	identityFields
}

func (LobbySessionFailure) EventType() string { return "LobbySessionFailure" }

// LobbyFindComplete fires when a lobby-find operation finishes.
// msg: "Lobby find complete"
type LobbyFindComplete struct {
	baseEvent
	identityFields
	Mode string `json:"mode,omitempty"`
}

func (LobbyFindComplete) EventType() string { return "LobbyFindComplete" }

// ── Errors ───────────────────────────────────────────────────────────────────

// UnexpectedError fires on unhandled errors during match finding.
// msg: "Unexpected error while finding match"
type UnexpectedError struct {
	baseEvent
	identityFields
	Error string `json:"error,omitempty"`
}

func (UnexpectedError) EventType() string { return "UnexpectedError" }

// ── Catch-All ────────────────────────────────────────────────────────────────

// UnknownPartyEvent captures any log line containing "party" or "Party" in
// its msg field that does not match a known event type. This is how we
// discover new party-relevant messages that need first-class event types.
type UnknownPartyEvent struct {
	baseEvent
	Msg    string                     `json:"msg"`
	Fields map[string]json.RawMessage `json:"fields,omitempty"`
}

func (UnknownPartyEvent) EventType() string { return "UnknownPartyEvent" }
func (UnknownPartyEvent) GetSID() string    { return "" }
func (UnknownPartyEvent) GetUID() string    { return "" }
func (UnknownPartyEvent) GetUsername() string { return "" }

// ── OutputEvent bridge: GetEventType() and GetDetails() ─────────────────────
// Every concrete event type gets GetEventType() (delegating to EventType()) and
// GetDetails() returning event-specific fields as a string map.

func (e *PartyFormed) GetEventType() string { return e.EventType() }
func (e *PartyFormed) GetDetails() map[string]string {
	return map[string]string{
		"uid": e.UID, "sid": e.SID,
		"leader_sid": e.LeaderSID,
		"size":       fmt.Sprintf("%d", e.Size),
	}
}

func (e *PartyJoined) GetEventType() string { return e.EventType() }
func (e *PartyJoined) GetDetails() map[string]string {
	return map[string]string{"uid": e.UID, "sid": e.SID, "party_id": e.PartyID}
}

func (e *MemberFollowAttempt) GetEventType() string { return e.EventType() }
func (e *MemberFollowAttempt) GetDetails() map[string]string {
	return map[string]string{"uid": e.UID, "sid": e.SID}
}

func (e *LeaderNotInMatch) GetEventType() string { return e.EventType() }
func (e *LeaderNotInMatch) GetDetails() map[string]string {
	return map[string]string{"uid": e.UID, "sid": e.SID}
}

func (e *SocialFallback) GetEventType() string { return e.EventType() }
func (e *SocialFallback) GetDetails() map[string]string {
	return map[string]string{"uid": e.UID, "sid": e.SID, "mode": e.Mode}
}

func (e *FollowLeaderLobby) GetEventType() string { return e.EventType() }
func (e *FollowLeaderLobby) GetDetails() map[string]string {
	return map[string]string{"uid": e.UID, "sid": e.SID, "mid": e.MatchID}
}

func (e *MatchJoinAttempt) GetEventType() string { return e.EventType() }
func (e *MatchJoinAttempt) GetDetails() map[string]string {
	return map[string]string{"uid": e.UID, "sid": e.SID, "mid": e.MatchID}
}

func (e *MatchJoined) GetEventType() string { return e.EventType() }
func (e *MatchJoined) GetDetails() map[string]string {
	return map[string]string{
		"uid": e.UID, "sid": e.SID,
		"mid":  e.MatchID,
		"role": fmt.Sprintf("%d", e.Role),
	}
}

func (e *MatchJoinFailed) GetEventType() string { return e.EventType() }
func (e *MatchJoinFailed) GetDetails() map[string]string {
	return map[string]string{"uid": e.UID, "sid": e.SID, "mid": e.MatchID, "error": e.Error}
}

func (e *MatchBuilt) GetEventType() string { return e.EventType() }
func (e *MatchBuilt) GetDetails() map[string]string {
	return map[string]string{"module": e.Module}
}

func (e *FollowReleasedIndependent) GetEventType() string { return e.EventType() }
func (e *FollowReleasedIndependent) GetDetails() map[string]string {
	return map[string]string{"uid": e.UID, "sid": e.SID, "mode": e.Mode}
}

func (e *LeaderNonJoinableMode) GetEventType() string { return e.EventType() }
func (e *LeaderNonJoinableMode) GetDetails() map[string]string {
	return map[string]string{"uid": e.UID, "sid": e.SID, "mode": e.Mode}
}

func (e *LeaderNonJoinablePoll) GetEventType() string { return e.EventType() }
func (e *LeaderNonJoinablePoll) GetDetails() map[string]string {
	return map[string]string{"uid": e.UID, "sid": e.SID, "mode": e.Mode}
}

func (e *LeaderLeftDuringPoll) GetEventType() string { return e.EventType() }
func (e *LeaderLeftDuringPoll) GetDetails() map[string]string {
	return map[string]string{"uid": e.UID, "sid": e.SID}
}

func (e *FailedJoinLeaderLobby) GetEventType() string { return e.EventType() }
func (e *FailedJoinLeaderLobby) GetDetails() map[string]string {
	return map[string]string{"uid": e.UID, "sid": e.SID, "error": e.Error}
}

func (e *FailedPriorityJoinLeader) GetEventType() string { return e.EventType() }
func (e *FailedPriorityJoinLeader) GetDetails() map[string]string {
	return map[string]string{"uid": e.UID, "sid": e.SID, "error": e.Error}
}

func (e *FailedPriorityJoinFollower) GetEventType() string { return e.EventType() }
func (e *FailedPriorityJoinFollower) GetDetails() map[string]string {
	return map[string]string{"uid": e.UID, "sid": e.SID, "error": e.Error}
}

func (e *CrashDetected) GetEventType() string { return e.EventType() }
func (e *CrashDetected) GetDetails() map[string]string {
	return map[string]string{"uid": e.UID, "sid": e.SID, "mid": e.MatchID}
}

func (e *PlayerLeft) GetEventType() string { return e.EventType() }
func (e *PlayerLeft) GetDetails() map[string]string {
	return map[string]string{"uid": e.UID, "sid": e.SID, "reason": fmt.Sprintf("%d", e.Reason)}
}

func (e *PlayerLeftVoluntary) GetEventType() string { return e.EventType() }
func (e *PlayerLeftVoluntary) GetDetails() map[string]string {
	return map[string]string{"uid": e.UID, "sid": e.SID}
}

func (e *ReconnectRestore) GetEventType() string { return e.EventType() }
func (e *ReconnectRestore) GetDetails() map[string]string {
	return map[string]string{"uid": e.UID, "sid": e.SID}
}

func (e *SplitRepaired) GetEventType() string { return e.EventType() }
func (e *SplitRepaired) GetDetails() map[string]string {
	return map[string]string{"mid": e.MatchID}
}

func (e *MatchmakingTicketAdded) GetEventType() string { return e.EventType() }
func (e *MatchmakingTicketAdded) GetDetails() map[string]string {
	return map[string]string{"uid": e.UID, "sid": e.SID, "ticket": e.Ticket}
}

func (e *MatchmakingStreamClosed) GetEventType() string { return e.EventType() }
func (e *MatchmakingStreamClosed) GetDetails() map[string]string {
	return map[string]string{"uid": e.UID, "sid": e.SID}
}

func (e *EntrantMetadataFallback) GetEventType() string { return e.EventType() }
func (e *EntrantMetadataFallback) GetDetails() map[string]string {
	return map[string]string{"uid": e.UID, "sid": e.SID}
}

func (e *LobbySessionFailure) GetEventType() string { return e.EventType() }
func (e *LobbySessionFailure) GetDetails() map[string]string {
	return map[string]string{"uid": e.UID, "sid": e.SID}
}

func (e *LobbyFindComplete) GetEventType() string { return e.EventType() }
func (e *LobbyFindComplete) GetDetails() map[string]string {
	return map[string]string{"uid": e.UID, "sid": e.SID, "mode": e.Mode}
}

func (e *UnexpectedError) GetEventType() string { return e.EventType() }
func (e *UnexpectedError) GetDetails() map[string]string {
	return map[string]string{"uid": e.UID, "sid": e.SID, "error": e.Error}
}

func (e *UnknownPartyEvent) GetEventType() string { return e.EventType() }
func (e *UnknownPartyEvent) GetDetails() map[string]string {
	return map[string]string{"msg": e.Msg}
}
