package main

import (
	"fmt"
	"log/slog"
	"time"
)

// LifecycleEvent is the interface consumed by the state machine. Temporary
// definition — the integration agent will replace this with the real event
// type system from events.go / parser.go.
type LifecycleEvent interface {
	EventType() string
	GetTimestamp() time.Time
	GetSID() string
	GetUID() string
	GetUsername() string
	GetRawLine() string
}

// PartyState enumerates the lifecycle states of a party.
type PartyState int

const (
	// StateForming indicates the party group was created and is waiting for members.
	StateForming PartyState = iota
	// StateReady indicates all expected members joined and the party is viable.
	StateReady
	// StateFollowing indicates a follower is attempting to follow the leader.
	StateFollowing
	// StatePolling indicates a follower is in the pollFollowPartyLeader retry loop.
	StatePolling
	// StateMatchmaking indicates the party ticket was submitted to the matchmaker.
	StateMatchmaking
	// StatePlacing indicates the match was built and members are being placed.
	StatePlacing
	// StateConverged is a terminal state: all members in same match, same team.
	StateConverged
	// StateSplit is a terminal state: members in different matches or teams.
	StateSplit
	// StateDegraded is a terminal state: partial convergence.
	StateDegraded
	// StateAbandoned is a terminal state: party dissolved before matchmaking completed.
	StateAbandoned
	// StateCrashed indicates a member crashed and the party is awaiting reconnect.
	StateCrashed
)

var stateNames = map[PartyState]string{
	StateForming:     "FORMING",
	StateReady:       "READY",
	StateFollowing:   "FOLLOWING",
	StatePolling:     "POLLING",
	StateMatchmaking: "MATCHMAKING",
	StatePlacing:     "PLACING",
	StateConverged:   "CONVERGED",
	StateSplit:       "SPLIT",
	StateDegraded:    "DEGRADED",
	StateAbandoned:   "ABANDONED",
	StateCrashed:     "CRASHED",
}

// String returns the human-readable name of a PartyState.
func (s PartyState) String() string {
	if name, ok := stateNames[s]; ok {
		return name
	}
	return fmt.Sprintf("UNKNOWN(%d)", int(s))
}

// IsTerminal returns true if the state is a final outcome state.
func (s PartyState) IsTerminal() bool {
	switch s {
	case StateConverged, StateSplit, StateDegraded, StateAbandoned:
		return true
	default:
		return false
	}
}

// MemberState tracks the per-member lifecycle within a party.
type MemberState int

const (
	// MemberJoined indicates the member has joined the party.
	MemberJoined MemberState = iota
	// MemberFollowing indicates the member is attempting to follow the leader.
	MemberFollowing
	// MemberPolling indicates the member is in the follow retry loop.
	MemberPolling
	// MemberMatchmaking indicates the member is in the matchmaking queue.
	MemberMatchmaking
	// MemberPlaced indicates the member has been placed in a match.
	MemberPlaced
	// MemberConverged indicates the member landed in the correct match and team.
	MemberConverged
	// MemberSplit indicates the member landed in a different match or team.
	MemberSplit
	// MemberCrashed indicates the member's session crashed.
	MemberCrashed
	// MemberReconnected indicates the member reconnected after a crash.
	MemberReconnected
	// MemberLeft indicates the member left the party.
	MemberLeft
)

var memberStateNames = map[MemberState]string{
	MemberJoined:      "JOINED",
	MemberFollowing:   "FOLLOWING",
	MemberPolling:     "POLLING",
	MemberMatchmaking: "MATCHMAKING",
	MemberPlaced:      "PLACED",
	MemberConverged:   "CONVERGED",
	MemberSplit:       "SPLIT",
	MemberCrashed:     "CRASHED",
	MemberReconnected: "RECONNECTED",
	MemberLeft:        "LEFT",
}

// String returns the human-readable name of a MemberState.
func (s MemberState) String() string {
	if name, ok := memberStateNames[s]; ok {
		return name
	}
	return fmt.Sprintf("UNKNOWN(%d)", int(s))
}

// Outcome classifies the final result of a party's lifecycle.
type Outcome int

const (
	// OutcomeNone means the party has not reached a terminal state.
	OutcomeNone Outcome = iota
	// OutcomeConverged means all members are in the same match on the same team.
	OutcomeConverged
	// OutcomeSplit means members ended up in different matches or teams.
	OutcomeSplit
	// OutcomeDegraded means some members converged but at least one failed.
	OutcomeDegraded
	// OutcomeAbandoned means the party dissolved before matchmaking completed.
	OutcomeAbandoned
)

var outcomeNames = map[Outcome]string{
	OutcomeNone:      "NONE",
	OutcomeConverged: "CONVERGED",
	OutcomeSplit:     "SPLIT",
	OutcomeDegraded:  "DEGRADED",
	OutcomeAbandoned: "ABANDONED",
}

// String returns the human-readable name of an Outcome.
func (s Outcome) String() string {
	if name, ok := outcomeNames[s]; ok {
		return name
	}
	return fmt.Sprintf("UNKNOWN(%d)", int(s))
}

// legalTransitions defines every permitted state transition. Any transition
// not present in this map is a bug and will be logged as an anomaly.
var legalTransitions = map[PartyState]map[PartyState]bool{
	StateForming: {
		StateReady:     true,
		StateAbandoned: true,
	},
	StateReady: {
		StateFollowing:   true,
		StateMatchmaking: true,
		StateAbandoned:   true,
		StateSplit:       true, // members diverge before explicit follow/matchmaking in logs
		StateConverged:   true, // all members land in same match via out-of-band path
		StateDegraded:    true, // partial convergence from READY
	},
	StateFollowing: {
		StatePolling:     true,
		StateConverged:   true,
		StateSplit:       true,
		StateAbandoned:   true, // party dissolved while following
		StateMatchmaking: true, // switch from follow to matchmaking path
	},
	StatePolling: {
		StateConverged: true,
		StateSplit:     true,
		StateCrashed:   true,
		StateAbandoned: true, // party dissolved while polling
		StateDegraded:  true, // partial convergence during polling
	},
	StateMatchmaking: {
		StatePlacing:   true,
		StateSplit:     true,
		StateAbandoned: true, // stream closed, ticket expired, or party dissolved during matchmaking
		StateConverged: true, // all members land in same match from matchmaking
		StateDegraded:  true, // partial convergence during matchmaking
	},
	StatePlacing: {
		StateConverged: true,
		StateSplit:     true,
		StateDegraded:  true,
	},
	StateCrashed: {
		StateFollowing: true,
		StateSplit:     true,
		StateAbandoned: true,
	},
}

// IsLegalTransition checks whether transitioning from one state to another
// is defined in the legal transitions map.
func IsLegalTransition(from, to PartyState) bool {
	targets, ok := legalTransitions[from]
	if !ok {
		return false
	}
	return targets[to]
}

// StateTransition records a single state change in a party's lifecycle.
type StateTransition struct {
	From      PartyState
	To        PartyState
	At        time.Time
	Trigger   string // event type that caused the transition
	Anomaly   bool   // true if this transition was illegal
	AnomalyAt string // human-readable explanation if anomaly
}

// MemberInfo tracks a single member within a party.
type MemberInfo struct {
	UID      string
	SID      string // can change on reconnect
	Username string
	State    MemberState
	MatchID  string // match this member was placed into, if any
}

// PartyLifecycle is the complete state of a single party from formation to outcome.
type PartyLifecycle struct {
	PartyID     string
	State       PartyState
	FormedAt    time.Time
	Leader      MemberInfo
	Members     []MemberInfo
	Events      []LifecycleEvent  // every event in chronological order
	Transitions []StateTransition // state change log
	Outcome     Outcome
	OutcomeInfo string              // human-readable explanation
	MatchIDs    map[string][]string // matchID -> []UIDs that joined it
	Tickets     []string
	Duration    time.Duration // time from formation to outcome
}

// NewPartyLifecycle constructs a PartyLifecycle in the FORMING state.
func NewPartyLifecycle(partyID string, formedAt time.Time, leader MemberInfo) *PartyLifecycle {
	return &PartyLifecycle{
		PartyID:  partyID,
		State:    StateForming,
		FormedAt: formedAt,
		Leader:   leader,
		MatchIDs: make(map[string][]string),
	}
}

// TransitionTo attempts to move the party to a new state. If the transition
// is illegal, it logs an anomaly but still applies the transition — production
// logs are messy and panicking would lose the diagnostic value.
func (pl *PartyLifecycle) TransitionTo(to PartyState, at time.Time, trigger string) {
	t := StateTransition{
		From:    pl.State,
		To:      to,
		At:      at,
		Trigger: trigger,
	}

	if !IsLegalTransition(pl.State, to) {
		t.Anomaly = true
		t.AnomalyAt = fmt.Sprintf(
			"illegal transition %s -> %s triggered by %s",
			pl.State, to, trigger,
		)
		slog.Warn("illegal state transition",
			slog.String("party_id", pl.PartyID),
			slog.String("from", pl.State.String()),
			slog.String("to", to.String()),
			slog.String("trigger", trigger),
		)
	}

	pl.Transitions = append(pl.Transitions, t)
	pl.State = to

	if to.IsTerminal() {
		pl.Duration = at.Sub(pl.FormedAt)
		switch to {
		case StateConverged:
			pl.Outcome = OutcomeConverged
		case StateSplit:
			pl.Outcome = OutcomeSplit
		case StateDegraded:
			pl.Outcome = OutcomeDegraded
		case StateAbandoned:
			pl.Outcome = OutcomeAbandoned
		}
	}
}

// FindMemberByUID returns a pointer to the member with the given UID, or nil.
func (pl *PartyLifecycle) FindMemberByUID(uid string) *MemberInfo {
	if pl.Leader.UID == uid {
		return &pl.Leader
	}
	for i := range pl.Members {
		if pl.Members[i].UID == uid {
			return &pl.Members[i]
		}
	}
	return nil
}

// FindMemberByUsername returns a pointer to the member with the given username, or nil.
func (pl *PartyLifecycle) FindMemberByUsername(username string) *MemberInfo {
	if username == "" {
		return nil
	}
	if pl.Leader.Username == username {
		return &pl.Leader
	}
	for i := range pl.Members {
		if pl.Members[i].Username == username {
			return &pl.Members[i]
		}
	}
	return nil
}

// FindMemberBySID returns a pointer to the member with the given SID, or nil.
func (pl *PartyLifecycle) FindMemberBySID(sid string) *MemberInfo {
	if pl.Leader.SID == sid {
		return &pl.Leader
	}
	for i := range pl.Members {
		if pl.Members[i].SID == sid {
			return &pl.Members[i]
		}
	}
	return nil
}

// AllMembers returns a slice of pointers to every member including the leader.
func (pl *PartyLifecycle) AllMembers() []*MemberInfo {
	all := make([]*MemberInfo, 0, 1+len(pl.Members))
	all = append(all, &pl.Leader)
	for i := range pl.Members {
		all = append(all, &pl.Members[i])
	}
	return all
}

// HasAnomalies returns true if any transition was flagged as illegal.
func (pl *PartyLifecycle) HasAnomalies() bool {
	for _, t := range pl.Transitions {
		if t.Anomaly {
			return true
		}
	}
	return false
}

// Anomalies returns all transitions that were flagged as illegal.
func (pl *PartyLifecycle) Anomalies() []StateTransition {
	var out []StateTransition
	for _, t := range pl.Transitions {
		if t.Anomaly {
			out = append(out, t)
		}
	}
	return out
}

// RecordMatchJoin registers that a member joined a specific match.
func (pl *PartyLifecycle) RecordMatchJoin(uid, matchID string) {
	pl.MatchIDs[matchID] = append(pl.MatchIDs[matchID], uid)
}

// ── PartyResult interface implementation ────────────────────────────────────
// These methods make PartyLifecycle satisfy the PartyResult interface defined
// in output.go, enabling direct use with the output layer.

func (pl *PartyLifecycle) GetPartyID() string    { return pl.PartyID }
func (pl *PartyLifecycle) GetFormedAt() time.Time { return pl.FormedAt }
func (pl *PartyLifecycle) GetLeader() string      { return pl.Leader.Username }
func (pl *PartyLifecycle) GetOutcome() string     { return pl.Outcome.String() }
func (pl *PartyLifecycle) GetOutcomeReason() string { return pl.OutcomeInfo }
func (pl *PartyLifecycle) GetDuration() time.Duration { return pl.Duration }

// GetMembers returns all member usernames including the leader.
func (pl *PartyLifecycle) GetMembers() []string {
	all := pl.AllMembers()
	names := make([]string, 0, len(all))
	for _, m := range all {
		name := m.Username
		if name == "" {
			name = m.UID // fall back to UID if username unknown
		}
		if name != "" {
			names = append(names, name)
		}
	}
	return names
}

// GetEvents returns each lifecycle event wrapped as an OutputEvent.
func (pl *PartyLifecycle) GetEvents() []OutputEvent {
	out := make([]OutputEvent, 0, len(pl.Events))
	for _, evt := range pl.Events {
		if oe, ok := evt.(OutputEvent); ok {
			out = append(out, oe)
		}
	}
	return out
}

// GetMatchIDs returns a map of username -> []matchID for output.
func (pl *PartyLifecycle) GetMatchIDs() map[string][]string {
	if len(pl.MatchIDs) == 0 {
		return nil
	}
	// pl.MatchIDs is matchID -> []UIDs. Invert to username -> []matchID.
	result := make(map[string][]string)
	for matchID, uids := range pl.MatchIDs {
		for _, uid := range uids {
			// Resolve UID to username via member lookup.
			name := uid
			if m := pl.FindMemberByUID(uid); m != nil && m.Username != "" {
				name = m.Username
			}
			result[name] = append(result[name], matchID)
		}
	}
	return result
}

// EvaluateOutcome inspects member states and match placements to determine
// whether the party has reached a terminal condition.
func (pl *PartyLifecycle) EvaluateOutcome(now time.Time) {
	if pl.State.IsTerminal() {
		return
	}

	allMembers := pl.AllMembers()
	totalMembers := len(allMembers)

	// Count members in each terminal-ish member state.
	var converged, split, crashed, placed int
	for _, m := range allMembers {
		switch m.State {
		case MemberConverged:
			converged++
		case MemberSplit:
			split++
		case MemberCrashed:
			crashed++
		case MemberPlaced:
			placed++
		}
	}

	// Count distinct current matches using each member's latest MatchID.
	// This is more accurate than pl.MatchIDs which accumulates all historical
	// joins (a member leaving one match and joining another is not a split).
	currentMatches := make(map[string]int) // matchID -> count of members in it
	for _, m := range allMembers {
		if m.MatchID != "" {
			currentMatches[m.MatchID]++
		}
	}
	distinctMatches := len(currentMatches)

	// All members converged to the same match.
	if converged == totalMembers && distinctMatches == 1 {
		pl.TransitionTo(StateConverged, now, "all members converged")
		pl.OutcomeInfo = "all members in same match"
		return
	}

	// Any member explicitly split (released to independent matchmaking).
	if split > 0 {
		if converged > 0 || placed > 0 {
			pl.TransitionTo(StateDegraded, now, "partial convergence with split members")
			pl.OutcomeInfo = fmt.Sprintf(
				"%d converged, %d split, %d crashed of %d total",
				converged, split, crashed, totalMembers,
			)
		} else {
			pl.TransitionTo(StateSplit, now, "member split detected")
			pl.OutcomeInfo = fmt.Sprintf(
				"%d split of %d total",
				split, totalMembers,
			)
		}
		return
	}

	// Members in different current matches.
	if distinctMatches > 1 {
		if crashed > 0 {
			pl.TransitionTo(StateDegraded, now, "multiple matches with crashed members")
			pl.OutcomeInfo = fmt.Sprintf(
				"%d distinct matches, %d crashed of %d total",
				distinctMatches, crashed, totalMembers,
			)
		} else {
			pl.TransitionTo(StateSplit, now, "members in different matches")
			pl.OutcomeInfo = fmt.Sprintf(
				"%d distinct matches for %d members",
				distinctMatches, totalMembers,
			)
		}
		return
	}

	// All placed members in same match -- converged.
	if placed+converged == totalMembers && totalMembers > 0 && distinctMatches == 1 {
		pl.TransitionTo(StateConverged, now, "all members placed in same match")
		pl.OutcomeInfo = "all members in same match"
		return
	}

	// Some converged, some crashed -- degraded.
	if converged > 0 && crashed > 0 && converged+crashed == totalMembers {
		pl.TransitionTo(StateDegraded, now, "partial convergence with crashes")
		pl.OutcomeInfo = fmt.Sprintf(
			"%d converged, %d crashed of %d total",
			converged, crashed, totalMembers,
		)
		return
	}
}
