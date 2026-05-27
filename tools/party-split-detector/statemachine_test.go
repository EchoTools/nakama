package main

import (
	"testing"
	"time"
)

// testEvent is a minimal LifecycleEvent implementation for testing.
type testEvent struct {
	typ       string
	timestamp time.Time
	sid       string
	uid       string
	username  string
	rawLine   string
}

func (e *testEvent) EventType() string       { return e.typ }
func (e *testEvent) GetTimestamp() time.Time { return e.timestamp }
func (e *testEvent) GetSID() string          { return e.sid }
func (e *testEvent) GetUID() string          { return e.uid }
func (e *testEvent) GetUsername() string     { return e.username }
func (e *testEvent) GetRawLine() string      { return e.rawLine }

var baseTime = time.Date(2026, 4, 24, 0, 59, 50, 0, time.UTC)

func ts(offsetSeconds int) time.Time {
	return baseTime.Add(time.Duration(offsetSeconds) * time.Second)
}

// --- PartyState ---

func TestPartyState_String(t *testing.T) {
	t.Parallel()
	tests := []struct {
		state PartyState
		want  string
	}{
		{StateForming, "FORMING"},
		{StateReady, "READY"},
		{StateFollowing, "FOLLOWING"},
		{StatePolling, "POLLING"},
		{StateMatchmaking, "MATCHMAKING"},
		{StatePlacing, "PLACING"},
		{StateConverged, "CONVERGED"},
		{StateSplit, "SPLIT"},
		{StateDegraded, "DEGRADED"},
		{StateAbandoned, "ABANDONED"},
		{StateCrashed, "CRASHED"},
		{PartyState(999), "UNKNOWN(999)"},
	}
	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			t.Parallel()
			if got := tt.state.String(); got != tt.want {
				t.Fatalf("PartyState(%d).String() = %q, want %q", tt.state, got, tt.want)
			}
		})
	}
}

func TestPartyState_IsTerminal(t *testing.T) {
	t.Parallel()
	terminal := []PartyState{StateConverged, StateSplit, StateDegraded, StateAbandoned}
	nonTerminal := []PartyState{StateForming, StateReady, StateFollowing, StatePolling, StateMatchmaking, StatePlacing, StateCrashed}

	for _, s := range terminal {
		t.Run(s.String()+"_terminal", func(t *testing.T) {
			t.Parallel()
			if !s.IsTerminal() {
				t.Fatalf("%s should be terminal", s)
			}
		})
	}
	for _, s := range nonTerminal {
		t.Run(s.String()+"_not_terminal", func(t *testing.T) {
			t.Parallel()
			if s.IsTerminal() {
				t.Fatalf("%s should not be terminal", s)
			}
		})
	}
}

// --- MemberState ---

func TestMemberState_String(t *testing.T) {
	t.Parallel()
	tests := []struct {
		state MemberState
		want  string
	}{
		{MemberJoined, "JOINED"},
		{MemberFollowing, "FOLLOWING"},
		{MemberPolling, "POLLING"},
		{MemberMatchmaking, "MATCHMAKING"},
		{MemberPlaced, "PLACED"},
		{MemberConverged, "CONVERGED"},
		{MemberSplit, "SPLIT"},
		{MemberCrashed, "CRASHED"},
		{MemberReconnected, "RECONNECTED"},
		{MemberLeft, "LEFT"},
		{MemberState(999), "UNKNOWN(999)"},
	}
	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			t.Parallel()
			if got := tt.state.String(); got != tt.want {
				t.Fatalf("MemberState(%d).String() = %q, want %q", tt.state, got, tt.want)
			}
		})
	}
}

// --- Outcome ---

func TestOutcome_String(t *testing.T) {
	t.Parallel()
	tests := []struct {
		outcome Outcome
		want    string
	}{
		{OutcomeNone, "NONE"},
		{OutcomeConverged, "CONVERGED"},
		{OutcomeSplit, "SPLIT"},
		{OutcomeDegraded, "DEGRADED"},
		{OutcomeAbandoned, "ABANDONED"},
		{Outcome(999), "UNKNOWN(999)"},
	}
	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			t.Parallel()
			if got := tt.outcome.String(); got != tt.want {
				t.Fatalf("Outcome(%d).String() = %q, want %q", tt.outcome, got, tt.want)
			}
		})
	}
}

// --- Legal Transitions ---

func TestIsLegalTransition_AllDefined(t *testing.T) {
	t.Parallel()

	// Every entry in legalTransitions must be queryable.
	for from, targets := range legalTransitions {
		for to := range targets {
			t.Run(from.String()+"_to_"+to.String(), func(t *testing.T) {
				t.Parallel()
				if !IsLegalTransition(from, to) {
					t.Fatalf("expected %s -> %s to be legal", from, to)
				}
			})
		}
	}
}

func TestIsLegalTransition_Specific(t *testing.T) {
	t.Parallel()

	legal := []struct {
		from, to PartyState
	}{
		{StateForming, StateReady},
		{StateForming, StateAbandoned},
		{StateReady, StateFollowing},
		{StateReady, StateMatchmaking},
		{StateReady, StateAbandoned},
		{StateReady, StateSplit},
		{StateReady, StateConverged},
		{StateReady, StateDegraded},
		{StateFollowing, StatePolling},
		{StateFollowing, StateConverged},
		{StateFollowing, StateSplit},
		{StateFollowing, StateAbandoned},
		{StateFollowing, StateMatchmaking},
		{StatePolling, StateConverged},
		{StatePolling, StateSplit},
		{StatePolling, StateCrashed},
		{StatePolling, StateAbandoned},
		{StatePolling, StateDegraded},
		{StateMatchmaking, StatePlacing},
		{StateMatchmaking, StateSplit},
		{StateMatchmaking, StateAbandoned},
		{StateMatchmaking, StateConverged},
		{StateMatchmaking, StateDegraded},
		{StatePlacing, StateConverged},
		{StatePlacing, StateSplit},
		{StatePlacing, StateDegraded},
		{StateCrashed, StateFollowing},
		{StateCrashed, StateSplit},
		{StateCrashed, StateAbandoned},
	}

	for _, tt := range legal {
		t.Run(tt.from.String()+"_to_"+tt.to.String(), func(t *testing.T) {
			t.Parallel()
			if !IsLegalTransition(tt.from, tt.to) {
				t.Fatalf("expected %s -> %s to be legal", tt.from, tt.to)
			}
		})
	}
}

func TestIsLegalTransition_IllegalCases(t *testing.T) {
	t.Parallel()

	illegal := []struct {
		from, to PartyState
	}{
		{StateForming, StateFollowing},   // must go through READY
		{StateForming, StateMatchmaking}, // must go through READY
		{StateForming, StateConverged},   // no shortcut
		{StateReady, StatePolling},       // must go through FOLLOWING
		{StateReady, StatePlacing},       // must go through MATCHMAKING
		{StateConverged, StateForming},   // terminal state, no transitions out
		{StateSplit, StateForming},       // terminal state
		{StateAbandoned, StateForming},   // terminal state
		{StateDegraded, StateForming},    // terminal state
		{StateMatchmaking, StateForming}, // backwards not allowed
		{StatePlacing, StateForming},     // backwards not allowed
	}

	for _, tt := range illegal {
		t.Run(tt.from.String()+"_to_"+tt.to.String(), func(t *testing.T) {
			t.Parallel()
			if IsLegalTransition(tt.from, tt.to) {
				t.Fatalf("expected %s -> %s to be illegal", tt.from, tt.to)
			}
		})
	}
}

// --- PartyLifecycle ---

func TestNewPartyLifecycle(t *testing.T) {
	t.Parallel()
	leader := MemberInfo{UID: "uid-leader", SID: "sid-leader", Username: "boss"}
	pl := NewPartyLifecycle("party-1", baseTime, leader)

	if pl.PartyID != "party-1" {
		t.Fatalf("PartyID = %q, want %q", pl.PartyID, "party-1")
	}
	if pl.State != StateForming {
		t.Fatalf("initial state = %s, want FORMING", pl.State)
	}
	if pl.FormedAt != baseTime {
		t.Fatalf("FormedAt = %v, want %v", pl.FormedAt, baseTime)
	}
	if pl.Leader.UID != "uid-leader" {
		t.Fatalf("Leader.UID = %q, want %q", pl.Leader.UID, "uid-leader")
	}
	if pl.Outcome != OutcomeNone {
		t.Fatalf("initial Outcome = %s, want NONE", pl.Outcome)
	}
}

func TestTransitionTo_Legal(t *testing.T) {
	t.Parallel()
	leader := MemberInfo{UID: "uid-leader", SID: "sid-leader"}
	pl := NewPartyLifecycle("party-1", baseTime, leader)

	pl.TransitionTo(StateReady, ts(1), "test_ready")

	if pl.State != StateReady {
		t.Fatalf("state after transition = %s, want READY", pl.State)
	}
	if len(pl.Transitions) != 1 {
		t.Fatalf("transitions count = %d, want 1", len(pl.Transitions))
	}
	tr := pl.Transitions[0]
	if tr.From != StateForming {
		t.Fatalf("transition.From = %s, want FORMING", tr.From)
	}
	if tr.To != StateReady {
		t.Fatalf("transition.To = %s, want READY", tr.To)
	}
	if tr.Anomaly {
		t.Fatal("legal transition should not be an anomaly")
	}
}

func TestTransitionTo_Illegal_LogsAnomaly(t *testing.T) {
	t.Parallel()
	leader := MemberInfo{UID: "uid-leader", SID: "sid-leader"}
	pl := NewPartyLifecycle("party-1", baseTime, leader)

	// FORMING -> CONVERGED is illegal.
	pl.TransitionTo(StateConverged, ts(1), "bogus_event")

	// The transition still applies (production resilience).
	if pl.State != StateConverged {
		t.Fatalf("state = %s, want CONVERGED (applied despite being illegal)", pl.State)
	}
	if len(pl.Transitions) != 1 {
		t.Fatalf("transitions count = %d, want 1", len(pl.Transitions))
	}
	tr := pl.Transitions[0]
	if !tr.Anomaly {
		t.Fatal("illegal transition should be flagged as anomaly")
	}
	if tr.AnomalyAt == "" {
		t.Fatal("anomaly should have an explanation")
	}
}

func TestTransitionTo_Terminal_SetsOutcomeAndDuration(t *testing.T) {
	t.Parallel()

	tests := []struct {
		terminal PartyState
		outcome  Outcome
	}{
		{StateConverged, OutcomeConverged},
		{StateSplit, OutcomeSplit},
		{StateDegraded, OutcomeDegraded},
		{StateAbandoned, OutcomeAbandoned},
	}

	for _, tt := range tests {
		t.Run(tt.terminal.String(), func(t *testing.T) {
			t.Parallel()
			leader := MemberInfo{UID: "uid-leader", SID: "sid-leader"}
			pl := NewPartyLifecycle("party-1", baseTime, leader)
			// Force state to one that can legally transition to the target.
			// Use direct assignment for test isolation.
			switch tt.terminal {
			case StateConverged:
				pl.State = StateFollowing
			case StateSplit:
				pl.State = StateFollowing
			case StateDegraded:
				pl.State = StatePlacing
			case StateAbandoned:
				pl.State = StateForming
			}

			pl.TransitionTo(tt.terminal, ts(10), "test")

			if pl.Outcome != tt.outcome {
				t.Fatalf("Outcome = %s, want %s", pl.Outcome, tt.outcome)
			}
			if pl.Duration != 10*time.Second {
				t.Fatalf("Duration = %v, want 10s", pl.Duration)
			}
		})
	}
}

func TestHasAnomalies(t *testing.T) {
	t.Parallel()
	leader := MemberInfo{UID: "uid-leader", SID: "sid-leader"}

	t.Run("no anomalies", func(t *testing.T) {
		t.Parallel()
		pl := NewPartyLifecycle("p", baseTime, leader)
		pl.TransitionTo(StateReady, ts(1), "test")
		if pl.HasAnomalies() {
			t.Fatal("should not have anomalies")
		}
	})

	t.Run("with anomaly", func(t *testing.T) {
		t.Parallel()
		pl := NewPartyLifecycle("p", baseTime, leader)
		pl.TransitionTo(StateConverged, ts(1), "bogus") // illegal
		if !pl.HasAnomalies() {
			t.Fatal("should have anomalies")
		}
		anom := pl.Anomalies()
		if len(anom) != 1 {
			t.Fatalf("anomaly count = %d, want 1", len(anom))
		}
	})
}

// --- Member Lookup ---

func TestFindMember(t *testing.T) {
	t.Parallel()
	leader := MemberInfo{UID: "uid-leader", SID: "sid-leader", Username: "boss"}
	pl := NewPartyLifecycle("party-1", baseTime, leader)
	pl.Members = append(pl.Members, MemberInfo{UID: "uid-follower", SID: "sid-follower", Username: "follower"})

	t.Run("find leader by UID", func(t *testing.T) {
		t.Parallel()
		m := pl.FindMemberByUID("uid-leader")
		if m == nil {
			t.Fatal("expected to find leader by UID")
		}
		if m.Username != "boss" {
			t.Fatalf("Username = %q, want %q", m.Username, "boss")
		}
	})

	t.Run("find follower by UID", func(t *testing.T) {
		t.Parallel()
		m := pl.FindMemberByUID("uid-follower")
		if m == nil {
			t.Fatal("expected to find follower by UID")
		}
		if m.Username != "follower" {
			t.Fatalf("Username = %q, want %q", m.Username, "follower")
		}
	})

	t.Run("find leader by SID", func(t *testing.T) {
		t.Parallel()
		m := pl.FindMemberBySID("sid-leader")
		if m == nil {
			t.Fatal("expected to find leader by SID")
		}
	})

	t.Run("find follower by SID", func(t *testing.T) {
		t.Parallel()
		m := pl.FindMemberBySID("sid-follower")
		if m == nil {
			t.Fatal("expected to find follower by SID")
		}
	})

	t.Run("not found returns nil", func(t *testing.T) {
		t.Parallel()
		if pl.FindMemberByUID("nonexistent") != nil {
			t.Fatal("expected nil for unknown UID")
		}
		if pl.FindMemberBySID("nonexistent") != nil {
			t.Fatal("expected nil for unknown SID")
		}
	})
}

func TestAllMembers(t *testing.T) {
	t.Parallel()
	leader := MemberInfo{UID: "uid-leader", SID: "sid-leader"}
	pl := NewPartyLifecycle("party-1", baseTime, leader)
	pl.Members = append(pl.Members,
		MemberInfo{UID: "uid-1", SID: "sid-1"},
		MemberInfo{UID: "uid-2", SID: "sid-2"},
	)

	all := pl.AllMembers()
	if len(all) != 3 {
		t.Fatalf("AllMembers count = %d, want 3", len(all))
	}
	if all[0].UID != "uid-leader" {
		t.Fatalf("first member should be leader, got UID=%q", all[0].UID)
	}
}

// --- RecordMatchJoin ---

func TestRecordMatchJoin(t *testing.T) {
	t.Parallel()
	leader := MemberInfo{UID: "uid-leader", SID: "sid-leader"}
	pl := NewPartyLifecycle("party-1", baseTime, leader)

	pl.RecordMatchJoin("uid-leader", "match-A")
	pl.RecordMatchJoin("uid-follower", "match-A")

	if len(pl.MatchIDs) != 1 {
		t.Fatalf("distinct matches = %d, want 1", len(pl.MatchIDs))
	}
	if len(pl.MatchIDs["match-A"]) != 2 {
		t.Fatalf("members in match-A = %d, want 2", len(pl.MatchIDs["match-A"]))
	}
}

func TestRecordMatchJoin_DifferentMatches(t *testing.T) {
	t.Parallel()
	leader := MemberInfo{UID: "uid-leader", SID: "sid-leader"}
	pl := NewPartyLifecycle("party-1", baseTime, leader)

	pl.RecordMatchJoin("uid-leader", "match-A")
	pl.RecordMatchJoin("uid-follower", "match-B")

	if len(pl.MatchIDs) != 2 {
		t.Fatalf("distinct matches = %d, want 2", len(pl.MatchIDs))
	}
}

// --- EvaluateOutcome ---

func TestEvaluateOutcome_Converged(t *testing.T) {
	t.Parallel()
	leader := MemberInfo{UID: "uid-leader", SID: "sid-leader", State: MemberConverged, MatchID: "match-A"}
	pl := NewPartyLifecycle("party-1", baseTime, leader)
	pl.State = StatePlacing
	pl.Members = append(pl.Members, MemberInfo{UID: "uid-follower", SID: "sid-follower", State: MemberConverged, MatchID: "match-A"})
	pl.RecordMatchJoin("uid-leader", "match-A")
	pl.RecordMatchJoin("uid-follower", "match-A")

	pl.EvaluateOutcome(ts(5))

	if pl.Outcome != OutcomeConverged {
		t.Fatalf("Outcome = %s, want CONVERGED", pl.Outcome)
	}
	if pl.State != StateConverged {
		t.Fatalf("State = %s, want CONVERGED", pl.State)
	}
}

func TestEvaluateOutcome_Split_DifferentMatches(t *testing.T) {
	t.Parallel()
	leader := MemberInfo{UID: "uid-leader", SID: "sid-leader", State: MemberPlaced, MatchID: "match-A"}
	pl := NewPartyLifecycle("party-1", baseTime, leader)
	pl.State = StatePlacing
	pl.Members = append(pl.Members, MemberInfo{UID: "uid-follower", SID: "sid-follower", State: MemberPlaced, MatchID: "match-B"})

	pl.EvaluateOutcome(ts(5))

	if pl.Outcome != OutcomeSplit {
		t.Fatalf("Outcome = %s, want SPLIT", pl.Outcome)
	}
}

func TestEvaluateOutcome_Split_MemberReleased(t *testing.T) {
	t.Parallel()
	leader := MemberInfo{UID: "uid-leader", SID: "sid-leader", State: MemberPlaced}
	pl := NewPartyLifecycle("party-1", baseTime, leader)
	pl.State = StatePlacing
	pl.Members = append(pl.Members, MemberInfo{UID: "uid-follower", SID: "sid-follower", State: MemberSplit})

	pl.EvaluateOutcome(ts(5))

	if pl.Outcome != OutcomeDegraded {
		t.Fatalf("Outcome = %s, want DEGRADED (placed + split)", pl.Outcome)
	}
}

func TestEvaluateOutcome_Degraded_ConvergedPlusCrashed(t *testing.T) {
	t.Parallel()
	leader := MemberInfo{UID: "uid-leader", SID: "sid-leader", State: MemberConverged, MatchID: "match-A"}
	pl := NewPartyLifecycle("party-1", baseTime, leader)
	pl.State = StatePlacing
	pl.Members = append(pl.Members,
		MemberInfo{UID: "uid-f1", SID: "sid-f1", State: MemberConverged, MatchID: "match-A"},
		MemberInfo{UID: "uid-f2", SID: "sid-f2", State: MemberCrashed},
	)
	pl.RecordMatchJoin("uid-leader", "match-A")
	pl.RecordMatchJoin("uid-f1", "match-A")

	pl.EvaluateOutcome(ts(5))

	if pl.Outcome != OutcomeDegraded {
		t.Fatalf("Outcome = %s, want DEGRADED", pl.Outcome)
	}
}

func TestEvaluateOutcome_NoopOnTerminal(t *testing.T) {
	t.Parallel()
	leader := MemberInfo{UID: "uid-leader", SID: "sid-leader"}
	pl := NewPartyLifecycle("party-1", baseTime, leader)
	pl.State = StateConverged
	pl.Outcome = OutcomeConverged

	pl.EvaluateOutcome(ts(5))

	// Should not change.
	if pl.Outcome != OutcomeConverged {
		t.Fatalf("Outcome changed on terminal party")
	}
}

func TestEvaluateOutcome_AllSplit(t *testing.T) {
	t.Parallel()
	leader := MemberInfo{UID: "uid-leader", SID: "sid-leader", State: MemberSplit}
	pl := NewPartyLifecycle("party-1", baseTime, leader)
	pl.State = StateFollowing
	pl.Members = append(pl.Members, MemberInfo{UID: "uid-follower", SID: "sid-follower", State: MemberSplit})

	pl.EvaluateOutcome(ts(5))

	if pl.Outcome != OutcomeSplit {
		t.Fatalf("Outcome = %s, want SPLIT", pl.Outcome)
	}
}

// --- Full Transition Chain Tests ---

func TestFullChain_FormingToConverged(t *testing.T) {
	t.Parallel()
	leader := MemberInfo{UID: "uid-leader", SID: "sid-leader"}
	pl := NewPartyLifecycle("party-1", baseTime, leader)

	pl.TransitionTo(StateReady, ts(1), EvtPartyReady)
	pl.TransitionTo(StateFollowing, ts(2), EvtFollowAttempt)
	pl.TransitionTo(StateConverged, ts(3), EvtFollowSuccess)

	if pl.State != StateConverged {
		t.Fatalf("State = %s, want CONVERGED", pl.State)
	}
	if pl.Outcome != OutcomeConverged {
		t.Fatalf("Outcome = %s, want CONVERGED", pl.Outcome)
	}
	if pl.Duration != 3*time.Second {
		t.Fatalf("Duration = %v, want 3s", pl.Duration)
	}
	if pl.HasAnomalies() {
		t.Fatal("no anomalies expected on legal chain")
	}
}

func TestFullChain_FormingToSplitViaPolling(t *testing.T) {
	t.Parallel()
	leader := MemberInfo{UID: "uid-leader", SID: "sid-leader"}
	pl := NewPartyLifecycle("party-1", baseTime, leader)

	pl.TransitionTo(StateReady, ts(1), EvtPartyReady)
	pl.TransitionTo(StateFollowing, ts(2), EvtFollowAttempt)
	pl.TransitionTo(StatePolling, ts(3), EvtFollowRetry)
	pl.TransitionTo(StateSplit, ts(10), EvtFollowReleasedIndependent)

	if pl.State != StateSplit {
		t.Fatalf("State = %s, want SPLIT", pl.State)
	}
	if pl.Outcome != OutcomeSplit {
		t.Fatalf("Outcome = %s, want SPLIT", pl.Outcome)
	}
}

func TestFullChain_MatchmakingPath(t *testing.T) {
	t.Parallel()
	leader := MemberInfo{UID: "uid-leader", SID: "sid-leader"}
	pl := NewPartyLifecycle("party-1", baseTime, leader)

	pl.TransitionTo(StateReady, ts(1), EvtPartyReady)
	pl.TransitionTo(StateMatchmaking, ts(2), EvtMatchmakingSubmit)
	pl.TransitionTo(StatePlacing, ts(5), EvtMatchBuilt)
	pl.TransitionTo(StateConverged, ts(6), EvtMatchJoined)

	if pl.State != StateConverged {
		t.Fatalf("State = %s, want CONVERGED", pl.State)
	}
}

func TestFullChain_CrashAndRecover(t *testing.T) {
	t.Parallel()
	leader := MemberInfo{UID: "uid-leader", SID: "sid-leader"}
	pl := NewPartyLifecycle("party-1", baseTime, leader)

	pl.TransitionTo(StateReady, ts(1), EvtPartyReady)
	pl.TransitionTo(StateFollowing, ts(2), EvtFollowAttempt)
	pl.TransitionTo(StatePolling, ts(3), EvtFollowRetry)
	pl.TransitionTo(StateCrashed, ts(5), EvtMemberCrashed)
	pl.TransitionTo(StateFollowing, ts(8), EvtMemberReconnected)
	pl.TransitionTo(StateConverged, ts(10), EvtFollowSuccess)

	if pl.State != StateConverged {
		t.Fatalf("State = %s, want CONVERGED", pl.State)
	}
	if pl.HasAnomalies() {
		t.Fatal("no anomalies expected")
	}
}

func TestFullChain_CrashToAbandoned(t *testing.T) {
	t.Parallel()
	leader := MemberInfo{UID: "uid-leader", SID: "sid-leader"}
	pl := NewPartyLifecycle("party-1", baseTime, leader)

	pl.TransitionTo(StateReady, ts(1), EvtPartyReady)
	pl.TransitionTo(StateFollowing, ts(2), EvtFollowAttempt)
	pl.TransitionTo(StatePolling, ts(3), EvtFollowRetry)
	pl.TransitionTo(StateCrashed, ts(5), EvtMemberCrashed)
	pl.TransitionTo(StateAbandoned, ts(60), "flush_window_expired")

	if pl.State != StateAbandoned {
		t.Fatalf("State = %s, want ABANDONED", pl.State)
	}
	if pl.Outcome != OutcomeAbandoned {
		t.Fatalf("Outcome = %s, want ABANDONED", pl.Outcome)
	}
}

func TestFullChain_CrashToSplit(t *testing.T) {
	t.Parallel()
	leader := MemberInfo{UID: "uid-leader", SID: "sid-leader"}
	pl := NewPartyLifecycle("party-1", baseTime, leader)

	pl.TransitionTo(StateReady, ts(1), EvtPartyReady)
	pl.TransitionTo(StateFollowing, ts(2), EvtFollowAttempt)
	pl.TransitionTo(StatePolling, ts(3), EvtFollowRetry)
	pl.TransitionTo(StateCrashed, ts(5), EvtMemberCrashed)
	pl.TransitionTo(StateSplit, ts(12), "party_dissolved_reconnect_mismatch")

	if pl.State != StateSplit {
		t.Fatalf("State = %s, want SPLIT", pl.State)
	}
}

func TestFullChain_PlacingToDegraded(t *testing.T) {
	t.Parallel()
	leader := MemberInfo{UID: "uid-leader", SID: "sid-leader"}
	pl := NewPartyLifecycle("party-1", baseTime, leader)

	pl.TransitionTo(StateReady, ts(1), EvtPartyReady)
	pl.TransitionTo(StateMatchmaking, ts(2), EvtMatchmakingSubmit)
	pl.TransitionTo(StatePlacing, ts(5), EvtMatchBuilt)
	pl.TransitionTo(StateDegraded, ts(8), "partial_join")

	if pl.State != StateDegraded {
		t.Fatalf("State = %s, want DEGRADED", pl.State)
	}
	if pl.Outcome != OutcomeDegraded {
		t.Fatalf("Outcome = %s, want DEGRADED", pl.Outcome)
	}
}

func TestFullChain_MatchmakingToSplit(t *testing.T) {
	t.Parallel()
	leader := MemberInfo{UID: "uid-leader", SID: "sid-leader"}
	pl := NewPartyLifecycle("party-1", baseTime, leader)

	pl.TransitionTo(StateReady, ts(1), EvtPartyReady)
	pl.TransitionTo(StateMatchmaking, ts(2), EvtMatchmakingSubmit)
	pl.TransitionTo(StateSplit, ts(8), "leader_placed_follower_not")

	if pl.State != StateSplit {
		t.Fatalf("State = %s, want SPLIT", pl.State)
	}
}
