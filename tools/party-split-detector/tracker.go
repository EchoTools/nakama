package main

import (
	"fmt"
	"log/slog"
	"time"
)

const (
	// FlushWindow is the hard time limit for a party lifecycle. Parties whose
	// formation is older than this get finalized. The 60-second bound is derived
	// from the 27-second Quest crash cycle — a party that has not converged in
	// 60 seconds is not going to.
	FlushWindow = 60 * time.Second
)

// Event type constants used by the state machine to identify lifecycle events.
// These are the internal dispatch keys. Real events from events.go use their own
// EventType() strings (e.g. "PartyFormed"); eventTypeMap translates them.
const (
	EvtPartyFormed               = "party_formed"
	EvtMemberJoined              = "member_joined"
	EvtMemberLeft                = "member_left"
	EvtPartyReady                = "party_ready"
	EvtFollowAttempt             = "follow_attempt"
	EvtFollowRetry               = "follow_retry"
	EvtFollowSuccess             = "follow_success"
	EvtFollowReleasedIndependent = "follow_released_independent"
	EvtMatchmakingSubmit         = "matchmaking_submit"
	EvtMatchBuilt                = "match_built"
	EvtMatchJoined               = "match_joined"
	EvtMemberCrashed             = "member_crashed"
	EvtMemberReconnected         = "member_reconnected"
	EvtPartyDissolved            = "party_dissolved"
)

// eventTypeMap translates EventType() strings from the real event types
// (events.go) into the tracker's internal dispatch constants. Events not
// present in this map are passed through unchanged (e.g. test events that
// already use the internal constants).
var eventTypeMap = map[string]string{
	"PartyFormed":               EvtPartyFormed,
	"PartyJoined":               EvtMemberJoined,
	"MemberFollowAttempt":       EvtFollowAttempt,
	"LeaderNotInMatch":          EvtFollowRetry,
	"SocialFallback":            EvtFollowReleasedIndependent,
	"FollowLeaderLobby":         EvtFollowSuccess,
	"MatchJoined":               EvtMatchJoined,
	"MatchBuilt":                EvtMatchBuilt,
	"FollowReleasedIndependent": EvtFollowReleasedIndependent,
	"LeaderNonJoinableMode":     EvtFollowReleasedIndependent,
	"FailedJoinLeaderLobby":     EvtFollowReleasedIndependent,
	"FailedPriorityJoinLeader":  EvtFollowReleasedIndependent,
	"FailedPriorityJoinFollower": EvtFollowReleasedIndependent,
	"CrashDetected":             EvtMemberCrashed,
	"PlayerLeft":                EvtMemberLeft,
	"PlayerLeftVoluntary":       EvtMemberLeft,
	"ReconnectRestore":          EvtMemberReconnected,
	"MatchmakingTicketAdded":    EvtMatchmakingSubmit,
	"MatchmakingStreamClosed":   EvtPartyDissolved,
}

// mapEventType translates a real event type string to the tracker's internal
// dispatch key. Returns the original string if no mapping exists (backward
// compatible with test events that use internal constants directly).
func mapEventType(evtType string) string {
	if mapped, ok := eventTypeMap[evtType]; ok {
		return mapped
	}
	return evtType
}

// PartyTracker correlates events to parties and drives the state machine.
//
// This IS: the central coordinator that maps raw lifecycle events to their
// owning party, drives state transitions, and determines outcomes.
//
// This is NOT: a parser (that is events.go/parser.go), a reporter (the
// caller decides what to do with finalized parties), or a matchmaker
// emulator (it observes, never acts).
type PartyTracker struct {
	parties       map[string]*PartyLifecycle // partyID -> lifecycle
	sidToParty    map[string]string          // session ID -> partyID
	uidToParty    map[string]string          // user ID -> partyID
	ticketToParty map[string]string          // ticket -> partyID

	// Finalized holds parties that reached a terminal state and were flushed.
	Finalized []*PartyLifecycle
}

// NewPartyTracker constructs an empty tracker ready to process events.
func NewPartyTracker() *PartyTracker {
	return &PartyTracker{
		parties:       make(map[string]*PartyLifecycle),
		sidToParty:    make(map[string]string),
		uidToParty:    make(map[string]string),
		ticketToParty: make(map[string]string),
	}
}

// ActiveParties returns the number of parties still being tracked.
func (pt *PartyTracker) ActiveParties() int {
	return len(pt.parties)
}

// GetParty returns the lifecycle for a partyID, or nil if not tracked.
func (pt *PartyTracker) GetParty(partyID string) *PartyLifecycle {
	return pt.parties[partyID]
}

// resolveParty finds the party a given event belongs to. It checks the event
// fields in priority order: explicit partyID in SID (via sidToParty), UID
// (via uidToParty). Returns nil if no party can be resolved.
func (pt *PartyTracker) resolveParty(evt LifecycleEvent) *PartyLifecycle {
	// Try SID mapping first — most specific.
	if sid := evt.GetSID(); sid != "" {
		if pid, ok := pt.sidToParty[sid]; ok {
			return pt.parties[pid]
		}
	}

	// Fall back to UID mapping.
	if uid := evt.GetUID(); uid != "" {
		if pid, ok := pt.uidToParty[uid]; ok {
			return pt.parties[pid]
		}
	}

	return nil
}

// registerMember adds index entries so future events for this member resolve
// to the given party.
func (pt *PartyTracker) registerMember(partyID string, m *MemberInfo) {
	if m.SID != "" {
		pt.sidToParty[m.SID] = partyID
	}
	if m.UID != "" {
		pt.uidToParty[m.UID] = partyID
	}
}

// unregisterParty removes all index entries for a party.
func (pt *PartyTracker) unregisterParty(pl *PartyLifecycle) {
	for _, m := range pl.AllMembers() {
		if m.SID != "" {
			delete(pt.sidToParty, m.SID)
		}
		if m.UID != "" {
			delete(pt.uidToParty, m.UID)
		}
	}
	for _, t := range pl.Tickets {
		delete(pt.ticketToParty, t)
	}
	delete(pt.parties, pl.PartyID)
}

// ProcessEvent is the main entry point. It identifies which party an event
// belongs to, creates new lifecycles for formation events, and drives state
// transitions and outcome evaluation.
func (pt *PartyTracker) ProcessEvent(evt LifecycleEvent) {
	evtType := mapEventType(evt.EventType())

	// Formation events create new parties.
	if evtType == EvtPartyFormed {
		pt.handlePartyFormed(evt)
		return
	}

	pl := pt.resolveParty(evt)

	// Member-join events for new members won't resolve via SID/UID since the
	// new member's identifiers are not yet indexed. Try the PartyID field on
	// real PartyJoined events, or fall back to rawLine for test events.
	if pl == nil && evtType == EvtMemberJoined {
		partyID := ""
		if pj, ok := evt.(*PartyJoined); ok {
			partyID = pj.PartyID
		} else {
			partyID = evt.GetRawLine()
		}
		if partyID != "" {
			pl = pt.parties[partyID]
		}
	}

	if pl == nil {
		slog.Debug("event for unknown party, dropping",
			slog.String("event_type", evtType),
			slog.String("sid", evt.GetSID()),
			slog.String("uid", evt.GetUID()),
		)
		return
	}

	// Do not process events for already-finalized parties.
	if pl.State.IsTerminal() {
		return
	}

	// Append to event log.
	pl.Events = append(pl.Events, evt)

	// Dispatch to handler by event type.
	switch evtType {
	case EvtMemberJoined:
		pt.handleMemberJoined(pl, evt)
	case EvtMemberLeft:
		pt.handleMemberLeft(pl, evt)
	case EvtPartyReady:
		pt.handlePartyReady(pl, evt)
	case EvtFollowAttempt:
		pt.handleFollowAttempt(pl, evt)
	case EvtFollowRetry:
		pt.handleFollowRetry(pl, evt)
	case EvtFollowSuccess:
		pt.handleFollowSuccess(pl, evt)
	case EvtFollowReleasedIndependent:
		pt.handleFollowReleased(pl, evt)
	case EvtMatchmakingSubmit:
		pt.handleMatchmakingSubmit(pl, evt)
	case EvtMatchBuilt:
		pt.handleMatchBuilt(pl, evt)
	case EvtMatchJoined:
		pt.handleMatchJoined(pl, evt)
	case EvtMemberCrashed:
		pt.handleMemberCrashed(pl, evt)
	case EvtMemberReconnected:
		pt.handleMemberReconnected(pl, evt)
	case EvtPartyDissolved:
		pt.handlePartyDissolved(pl, evt)
	default:
		slog.Debug("unhandled event type",
			slog.String("event_type", evtType),
			slog.String("party_id", pl.PartyID),
		)
	}

	// After processing, evaluate whether we have reached an outcome.
	pl.EvaluateOutcome(evt.GetTimestamp())
}

// handlePartyFormed creates a new PartyLifecycle and registers the leader.
// When processing a real PartyFormed event (which corresponds to "Party is
// ready"), this also registers all members and transitions directly to READY.
func (pt *PartyTracker) handlePartyFormed(evt LifecycleEvent) {
	// For real PartyFormed events, use LeaderSID as the party ID for
	// consistency with how Nakama identifies parties. Fall back to event's
	// SID for test events.
	partyID := ""
	if pf, ok := evt.(*PartyFormed); ok && pf.LeaderSID != "" {
		partyID = pf.LeaderSID
	} else {
		partyID = evt.GetSID()
	}
	if partyID == "" {
		slog.Warn("party_formed event with no SID, cannot create party")
		return
	}

	// Avoid duplicate formation for the same party.
	if _, exists := pt.parties[partyID]; exists {
		slog.Debug("duplicate party_formed for existing party",
			slog.String("party_id", partyID),
		)
		return
	}

	leader := MemberInfo{
		UID:      evt.GetUID(),
		SID:      evt.GetSID(),
		Username: evt.GetUsername(),
		State:    MemberJoined,
	}

	pl := NewPartyLifecycle(partyID, evt.GetTimestamp(), leader)
	pl.Events = append(pl.Events, evt)

	pt.parties[partyID] = pl
	pt.registerMember(partyID, &pl.Leader)

	// For real PartyFormed events, register all members and transition to READY.
	// The "Party is ready" message means the party has formed and is ready for
	// matchmaking in a single log line.
	if pf, ok := evt.(*PartyFormed); ok {
		for _, username := range pf.Members {
			member := MemberInfo{
				Username: username,
				State:    MemberJoined,
			}
			pl.Members = append(pl.Members, member)
		}
		pl.TransitionTo(StateReady, evt.GetTimestamp(), EvtPartyReady)
	}
}

// handleMemberJoined adds a new member to the party or enriches a pre-registered
// member (from PartyFormed) with identity fields from the PartyJoined event.
func (pt *PartyTracker) handleMemberJoined(pl *PartyLifecycle, evt LifecycleEvent) {
	uid := evt.GetUID()
	username := evt.GetUsername()

	// For real events, we may have pre-registered members (by username only)
	// during PartyFormed. Try to match and enrich them.
	if username != "" {
		for i := range pl.Members {
			m := &pl.Members[i]
			if m.Username == username && m.UID == "" {
				// Enrich the pre-registered member with identity fields.
				m.UID = uid
				m.SID = evt.GetSID()
				m.State = MemberJoined
				pt.registerMember(pl.PartyID, m)
				return
			}
		}
	}

	if uid == "" {
		return
	}

	// Check if this member is already tracked (reconnect scenario).
	existing := pl.FindMemberByUID(uid)
	if existing != nil {
		// Update SID on reconnect.
		if evt.GetSID() != "" && evt.GetSID() != existing.SID {
			oldSID := existing.SID
			existing.SID = evt.GetSID()
			delete(pt.sidToParty, oldSID)
			pt.sidToParty[existing.SID] = pl.PartyID
		}
		existing.State = MemberJoined
		return
	}

	member := MemberInfo{
		UID:      uid,
		SID:      evt.GetSID(),
		Username: username,
		State:    MemberJoined,
	}
	pl.Members = append(pl.Members, member)
	pt.registerMember(pl.PartyID, &pl.Members[len(pl.Members)-1])
}

// handleMemberLeft marks a member as having left the party.
func (pt *PartyTracker) handleMemberLeft(pl *PartyLifecycle, evt LifecycleEvent) {
	m := pt.findMemberByEvent(pl, evt)
	if m == nil {
		return
	}
	m.State = MemberLeft

	// If all members left, the party is abandoned.
	allLeft := true
	for _, member := range pl.AllMembers() {
		if member.State != MemberLeft {
			allLeft = false
			break
		}
	}
	if allLeft {
		pl.TransitionTo(StateAbandoned, evt.GetTimestamp(), EvtMemberLeft)
		pl.OutcomeInfo = "all members left"
	}
}

// handlePartyReady transitions from FORMING to READY.
func (pt *PartyTracker) handlePartyReady(pl *PartyLifecycle, evt LifecycleEvent) {
	pl.TransitionTo(StateReady, evt.GetTimestamp(), EvtPartyReady)
}

// handleFollowAttempt transitions from READY to FOLLOWING.
func (pt *PartyTracker) handleFollowAttempt(pl *PartyLifecycle, evt LifecycleEvent) {
	m := pt.findMemberByEvent(pl, evt)
	if m != nil {
		m.State = MemberFollowing
	}
	if pl.State == StateReady {
		pl.TransitionTo(StateFollowing, evt.GetTimestamp(), EvtFollowAttempt)
	}
}

// handleFollowRetry transitions from FOLLOWING to POLLING.
func (pt *PartyTracker) handleFollowRetry(pl *PartyLifecycle, evt LifecycleEvent) {
	m := pt.findMemberByEvent(pl, evt)
	if m != nil {
		m.State = MemberPolling
	}
	if pl.State == StateFollowing {
		pl.TransitionTo(StatePolling, evt.GetTimestamp(), EvtFollowRetry)
	}
}

// handleFollowSuccess transitions from FOLLOWING or POLLING to CONVERGED
// by marking the member and letting EvaluateOutcome determine if the whole
// party converged.
func (pt *PartyTracker) handleFollowSuccess(pl *PartyLifecycle, evt LifecycleEvent) {
	m := pt.findMemberByEvent(pl, evt)
	if m != nil {
		m.State = MemberConverged
	}
}

// handleFollowReleased marks a member as split (released to independent
// matchmaking) and lets outcome evaluation determine the party outcome.
func (pt *PartyTracker) handleFollowReleased(pl *PartyLifecycle, evt LifecycleEvent) {
	m := pt.findMemberByEvent(pl, evt)
	if m != nil {
		m.State = MemberSplit
	}
}

// handleMatchmakingSubmit transitions from READY to MATCHMAKING and records
// the matchmaking ticket for correlation.
func (pt *PartyTracker) handleMatchmakingSubmit(pl *PartyLifecycle, evt LifecycleEvent) {
	// Record ticket for correlation if available.
	if mta, ok := evt.(*MatchmakingTicketAdded); ok && mta.Ticket != "" {
		pl.Tickets = append(pl.Tickets, mta.Ticket)
		pt.ticketToParty[mta.Ticket] = pl.PartyID
	}
	if pl.State == StateReady || pl.State == StateFollowing {
		pl.TransitionTo(StateMatchmaking, evt.GetTimestamp(), EvtMatchmakingSubmit)
	}
}

// handleMatchBuilt transitions from MATCHMAKING to PLACING.
func (pt *PartyTracker) handleMatchBuilt(pl *PartyLifecycle, evt LifecycleEvent) {
	if pl.State == StateMatchmaking {
		pl.TransitionTo(StatePlacing, evt.GetTimestamp(), EvtMatchBuilt)
	}
}

// handleMatchJoined records a member joining a specific match.
func (pt *PartyTracker) handleMatchJoined(pl *PartyLifecycle, evt LifecycleEvent) {
	m := pt.findMemberByEvent(pl, evt)
	if m == nil {
		return
	}

	// Extract match ID from the real typed event, or fall back to rawLine for
	// test events that encode matchID there.
	matchID := ""
	if mj, ok := evt.(*MatchJoined); ok {
		matchID = mj.MatchID
	} else {
		matchID = evt.GetRawLine()
	}
	if matchID == "" {
		return
	}

	m.MatchID = matchID
	m.State = MemberPlaced
	pl.RecordMatchJoin(m.UID, matchID)
}

// handleMemberCrashed marks a member as crashed and transitions the party
// to CRASHED if appropriate.
func (pt *PartyTracker) handleMemberCrashed(pl *PartyLifecycle, evt LifecycleEvent) {
	m := pt.findMemberByEvent(pl, evt)
	if m != nil {
		m.State = MemberCrashed
	}
	if pl.State == StatePolling {
		pl.TransitionTo(StateCrashed, evt.GetTimestamp(), EvtMemberCrashed)
	}
}

// handleMemberReconnected processes a reconnection. It updates the member's
// SID and transitions from CRASHED to FOLLOWING.
func (pt *PartyTracker) handleMemberReconnected(pl *PartyLifecycle, evt LifecycleEvent) {
	uid := evt.GetUID()
	if uid == "" {
		return
	}

	m := pl.FindMemberByUID(uid)
	if m == nil {
		return
	}

	// Update SID mapping for the reconnected session.
	newSID := evt.GetSID()
	if newSID != "" && newSID != m.SID {
		delete(pt.sidToParty, m.SID)
		m.SID = newSID
		pt.sidToParty[newSID] = pl.PartyID
	}

	m.State = MemberReconnected
	if pl.State == StateCrashed {
		pl.TransitionTo(StateFollowing, evt.GetTimestamp(), EvtMemberReconnected)
	}
}

// handlePartyDissolved transitions the party to ABANDONED.
func (pt *PartyTracker) handlePartyDissolved(pl *PartyLifecycle, evt LifecycleEvent) {
	if pl.State == StateCrashed {
		pl.TransitionTo(StateAbandoned, evt.GetTimestamp(), EvtPartyDissolved)
		pl.OutcomeInfo = "party dissolved after crash, no reconnect"
	} else {
		pl.TransitionTo(StateAbandoned, evt.GetTimestamp(), EvtPartyDissolved)
		pl.OutcomeInfo = "party dissolved"
	}
}

// findMemberByEvent locates a member using the event's UID, SID, or username.
// If a member is found by username but lacks identity fields, enrich it.
func (pt *PartyTracker) findMemberByEvent(pl *PartyLifecycle, evt LifecycleEvent) *MemberInfo {
	if uid := evt.GetUID(); uid != "" {
		if m := pl.FindMemberByUID(uid); m != nil {
			return m
		}
	}
	if sid := evt.GetSID(); sid != "" {
		if m := pl.FindMemberBySID(sid); m != nil {
			return m
		}
	}
	// Fall back to username match for members pre-registered by PartyFormed
	// that never received a PartyJoined event with identity fields.
	if username := evt.GetUsername(); username != "" {
		if m := pl.FindMemberByUsername(username); m != nil {
			// Enrich with identity fields from this event.
			if m.UID == "" && evt.GetUID() != "" {
				m.UID = evt.GetUID()
				pt.uidToParty[m.UID] = pl.PartyID
			}
			if m.SID == "" && evt.GetSID() != "" {
				m.SID = evt.GetSID()
				pt.sidToParty[m.SID] = pl.PartyID
			}
			return m
		}
	}
	return nil
}

// Tick finalizes parties whose formation is older than FlushWindow. Parties
// that have not reached a terminal state within the window are force-evaluated
// and assigned an ABANDONED outcome if no matchmaking occurred, or their
// current best outcome otherwise.
func (pt *PartyTracker) Tick(now time.Time) {
	for pid, pl := range pt.parties {
		if now.Sub(pl.FormedAt) < FlushWindow {
			continue
		}

		if !pl.State.IsTerminal() {
			// Force outcome evaluation before flush.
			pl.EvaluateOutcome(now)
		}

		if !pl.State.IsTerminal() {
			// Still no terminal state — mark as abandoned.
			pl.TransitionTo(StateAbandoned, now, "flush_window_expired")
			pl.OutcomeInfo = fmt.Sprintf(
				"no terminal state reached within %s window",
				FlushWindow,
			)
		}

		pt.Finalized = append(pt.Finalized, pl)
		pt.unregisterParty(pl)
		// unregisterParty deletes from pt.parties, which is safe during
		// range iteration in Go.
		_ = pid
	}
}

// FlushAll finalizes every active party regardless of age. Called at end of
// input to ensure no parties are left dangling.
func (pt *PartyTracker) FlushAll(now time.Time) {
	for _, pl := range pt.parties {
		if !pl.State.IsTerminal() {
			pl.EvaluateOutcome(now)
		}
		if !pl.State.IsTerminal() {
			pl.TransitionTo(StateAbandoned, now, "flush_all")
			pl.OutcomeInfo = "flushed at end of input"
		}
		pt.Finalized = append(pt.Finalized, pl)
	}

	// Clear all maps.
	pt.parties = make(map[string]*PartyLifecycle)
	pt.sidToParty = make(map[string]string)
	pt.uidToParty = make(map[string]string)
	pt.ticketToParty = make(map[string]string)
}

// Completed returns all parties that reached a terminal state. This includes
// both parties finalized by Tick/FlushAll and parties that reached terminal
// state during normal event processing but have not been flushed yet.
func (pt *PartyTracker) Completed() []*PartyLifecycle {
	result := make([]*PartyLifecycle, 0, len(pt.Finalized)+len(pt.parties))
	result = append(result, pt.Finalized...)
	for _, pl := range pt.parties {
		if pl.State.IsTerminal() {
			result = append(result, pl)
		}
	}
	return result
}
