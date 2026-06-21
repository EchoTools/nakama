package server

import (
	"fmt"
	"sync"
	"time"

	"go.uber.org/zap"
)

// MatchLifecycleState represents a player's position in the match participation
// lifecycle. The lifecycle is a loop (social lobby → match → social lobby),
// not a DAG with terminal nodes.
//
// This IS: an explicit, enumerated state for one player's match participation.
// This is NOT: a match state, a party state, or a session state. Those are
// separate concerns. This tracks where a single player is in the flow from
// "idle" through "converging to social lobby" through "in match" and back.
type MatchLifecycleState uint8

const (
	// StateIdle means the player is not in any party or matchmaking flow.
	StateIdle MatchLifecycleState = iota
	// StateSocialConverging means the player is heading to the leader's social
	// lobby with an active reservation.
	StateSocialConverging
	// StateSocialReady means the player is in the leader's social lobby,
	// waiting for matchmaking to begin.
	StateSocialReady
	// StateHolding means the player has sent LobbyFindSessionRequest and is
	// waiting for the leader to submit the matchmaking ticket.
	StateHolding
	// StateMatchmaking means the player is on the leader's matchmaking ticket.
	StateMatchmaking
	// StateJoining means a match was found and the player is connecting to
	// the game server.
	StateJoining
	// StateInMatch means the player is connected and playing in a match.
	StateInMatch
	// StateReturning means the match ended and the player is heading back
	// to the social lobby.
	StateReturning
	// StateCrashed means the client disconnected and a reconnect reservation
	// is active (27s on Quest).
	StateCrashed

	// matchLifecycleStateCount is not a valid state; it marks the boundary
	// for iteration and validation.
	matchLifecycleStateCount
)

// stateNames maps each state to its human-readable name. Indexed by the
// state's underlying uint8 value.
var stateNames = [matchLifecycleStateCount]string{
	StateIdle:             "Idle",
	StateSocialConverging: "SocialConverging",
	StateSocialReady:      "SocialReady",
	StateHolding:          "Holding",
	StateMatchmaking:      "Matchmaking",
	StateJoining:          "Joining",
	StateInMatch:          "InMatch",
	StateReturning:        "Returning",
	StateCrashed:          "Crashed",
}

// String returns the human-readable name for the state.
func (s MatchLifecycleState) String() string {
	if s >= matchLifecycleStateCount {
		return fmt.Sprintf("Unknown(%d)", s)
	}
	return stateNames[s]
}

// IsTerminal returns false. The lifecycle is a loop — no state is terminal.
// Players cycle through social → match → social indefinitely.
func (s MatchLifecycleState) IsTerminal() bool {
	return false
}

// legalTransitions defines the set of permitted state transitions per the
// party behavioral spec. The outer key is the source state; the inner set
// contains all legal destination states from that source.
//
// Transitions not in this map are anomalies. In observer mode (Phase 2),
// illegal transitions are logged as warnings but still applied.
var legalTransitions = map[MatchLifecycleState]map[MatchLifecycleState]bool{
	StateIdle: {
		StateSocialConverging: true, // Party follower heading to leader's social lobby.
		StateSocialReady:      true, // Solo player joined social lobby directly.
		StateHolding:          true, // Solo/follower sent LobbyFindSessionRequest.
		StateMatchmaking:      true, // Solo/leader submitted ticket from menu.
		StateJoining:          true, // Direct match join (echo taxi / reconnect).
		StateIdle:             true, // Reconnect failed (idempotent).
	},
	StateSocialConverging: {
		StateSocialReady: true, // Arrived at leader's social lobby.
	},
	StateSocialReady: {
		StateHolding:     true, // Non-leader sent LobbyFindSessionRequest.
		StateMatchmaking: true, // Leader submits ticket.
		StateJoining:     true, // Match found, joining (direct placement).
		StateInMatch:     true, // Followed leader to match / reconnect.
		StateIdle:        true, // Left party.
		StateSocialReady: true, // Lobby switch or join-failed regroup.
		StateCrashed:     true, // Disconnect from social (rare).
	},
	StateHolding: {
		StateMatchmaking: true, // Leader's ticket includes this member.
		StateSocialReady: true, // Matchmaking cancelled / leader in arena.
		StateJoining:     true, // Match found while in holding (skipped Matchmaking).
		StateInMatch:     true, // Followed leader directly (rare).
		StateHolding:     true, // Ticket cancelled for late arrival, rebuilding.
		StateReturning:   true, // Stale match-end during holding.
	},
	StateMatchmaking: {
		StateJoining:     true, // Match found, join started.
		StateSocialReady: true, // Ticket cancelled / join failed.
		StateMatchmaking: true, // Ticket rebuild (late arrival, party change).
		StateHolding:     true, // Re-entered holding (ticket structure change).
		StateReturning:   true, // Stale match-end (see §5B — add guard).
		StateCrashed:     true, // Disconnect while on ticket.
	},
	StateJoining: {
		StateInMatch:     true, // Connected to game server.
		StateSocialReady: true, // Join failed, return to social.
		StateReturning:   true, // Match ended during join window.
		StateCrashed:     true, // Disconnect during join.
		StateMatchmaking: true, // Ticket placed during join window.
		StateJoining:     true, // Duplicate match-found (idempotent).
	},
	StateInMatch: {
		StateReturning: true, // Match ended naturally.
		StateCrashed:   true, // Client disconnected.
		// Below are stale-state transitions — should be eliminated by code
		// guards but listed for observer-mode tolerance during transition:
		StateSocialReady: true, // Joined social while stale InMatch.
		StateMatchmaking: true, // Ticket submitted while stale InMatch.
		StateHolding:     true, // Holding entered while stale InMatch.
		StateJoining:     true, // Match found while stale InMatch.
		StateIdle:        true, // Reconnect failed (rare).
	},
	StateReturning: {
		StateSocialReady: true, // Back in social lobby.
		StateMatchmaking: true, // Fast requeue — leader submitted ticket.
		StateHolding:     true, // Fast requeue — follower waiting for ticket.
		StateJoining:     true, // Match found during return window.
		StateReturning:   true, // Duplicate match-end (idempotent).
		StateCrashed:     true, // Disconnect during return.
		StateIdle:        true, // Reconnect failed.
	},
	StateCrashed: {
		StateInMatch:     true, // Reconnected within 27s.
		StateIdle:        true, // Reconnect failed, reservation expired.
		StateJoining:     true, // Match found while reservation active.
		StateReturning:   true, // Match ends while reservation active.
		StateMatchmaking: true, // Picked up by new ticket.
		StateSocialReady: true, // Joined social (gave up on reconnect).
		StateHolding:     true, // Re-entered party flow.
		StateCrashed:     true, // Duplicate crash event.
	},
}

// isLegalTransition reports whether from → to is in the legal transition set.
func isLegalTransition(from, to MatchLifecycleState) bool {
	dests, ok := legalTransitions[from]
	if !ok {
		return false
	}
	return dests[to]
}

// LifecycleTransition records a single state change in the lifecycle log.
type LifecycleTransition struct {
	From      MatchLifecycleState
	To        MatchLifecycleState
	Reason    string
	Timestamp time.Time
	Legal     bool
}

// TransitionOpt sets optional fields on a PlayerMatchLifecycle during a
// transition. Use the With* constructors.
type TransitionOpt func(*PlayerMatchLifecycle)

// WithPartyID sets the party ID during a transition.
func WithPartyID(id string) TransitionOpt {
	return func(p *PlayerMatchLifecycle) {
		p.partyID = id
	}
}

// WithMatchID sets the match ID during a transition.
func WithMatchID(id string) TransitionOpt {
	return func(p *PlayerMatchLifecycle) {
		p.matchID = id
	}
}

// WithLeaderSID sets the leader's session ID during a transition.
func WithLeaderSID(sid string) TransitionOpt {
	return func(p *PlayerMatchLifecycle) {
		p.leaderSID = sid
	}
}

// WithIsLeader sets whether this player is the party leader during a transition.
func WithIsLeader(leader bool) TransitionOpt {
	return func(p *PlayerMatchLifecycle) {
		p.isLeader = leader
	}
}

// PlayerMatchLifecycle tracks one player's position in the match participation
// lifecycle. It is safe for concurrent use.
//
// Observer mode: illegal transitions are logged as warnings but still applied.
// This allows instrumentation of the existing code paths without blocking
// players. Phase 2 (Issue #456) will add enforcement.
type PlayerMatchLifecycle struct {
	mu          sync.RWMutex
	state       MatchLifecycleState
	partyID     string
	matchID     string
	leaderSID   string
	isLeader    bool
	transitions []LifecycleTransition
	logger      *zap.Logger
}

// NewPlayerMatchLifecycle creates a new lifecycle in StateIdle.
func NewPlayerMatchLifecycle(logger *zap.Logger) *PlayerMatchLifecycle {
	return &PlayerMatchLifecycle{
		state:  StateIdle,
		logger: logger,
	}
}

// State returns the current lifecycle state. Thread-safe.
func (p *PlayerMatchLifecycle) State() MatchLifecycleState {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.state
}

// Transition attempts a state transition with a reason string. Returns nil
// on success. On an illegal transition the anomaly is logged at warn level
// and recorded in history, but the transition is still applied (observer
// mode — do not block players).
func (p *PlayerMatchLifecycle) Transition(to MatchLifecycleState, reason string) error {
	return p.TransitionTo(to, reason)
}

// TransitionTo is like Transition but also applies functional options to
// update associated fields (party ID, match ID, leader SID, leader flag).
func (p *PlayerMatchLifecycle) TransitionTo(to MatchLifecycleState, reason string, opts ...TransitionOpt) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	from := p.state
	legal := isLegalTransition(from, to)

	// Apply option functions before logging so fields are current.
	for _, opt := range opts {
		opt(p)
	}

	record := LifecycleTransition{
		From:      from,
		To:        to,
		Reason:    reason,
		Timestamp: time.Now(),
		Legal:     legal,
	}
	p.transitions = append(p.transitions, record)

	// Log at the appropriate level.
	if legal {
		p.logger.Debug("Player match lifecycle transition",
			zap.String("from", from.String()),
			zap.String("to", to.String()),
			zap.String("reason", reason),
			zap.Bool("legal", legal),
			zap.String("party_id", p.partyID),
			zap.String("match_id", p.matchID),
		)
	} else {
		p.logger.Warn("Illegal player match lifecycle transition",
			zap.String("from", from.String()),
			zap.String("to", to.String()),
			zap.String("reason", reason),
			zap.Bool("legal", legal),
			zap.String("party_id", p.partyID),
			zap.String("match_id", p.matchID),
			zap.String("anomaly", "transition not in legal set"),
		)
	}

	// Observer mode: always apply the transition.
	p.state = to
	return nil
}

// Reset returns the lifecycle to StateIdle and clears all associated fields.
// Used when a session fully disconnects.
func (p *PlayerMatchLifecycle) Reset() {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.logger.Debug("Player match lifecycle reset",
		zap.String("from", p.state.String()),
		zap.String("party_id", p.partyID),
		zap.String("match_id", p.matchID),
	)

	p.state = StateIdle
	p.partyID = ""
	p.matchID = ""
	p.leaderSID = ""
	p.isLeader = false
	p.transitions = nil
}

// History returns a copy of the transition log. The caller owns the slice.
func (p *PlayerMatchLifecycle) History() []LifecycleTransition {
	p.mu.RLock()
	defer p.mu.RUnlock()

	out := make([]LifecycleTransition, len(p.transitions))
	copy(out, p.transitions)
	return out
}
