package server

import (
	"context"
	"testing"
	"time"

	"github.com/gofrs/uuid/v5"
	"github.com/heroiclabs/nakama/v3/server/evr"
	"go.uber.org/zap"
)

// partyStubSession is a non-nil Session; the placement path under test never
// calls a method on it (the executor is faked).
type partyStubSession struct{ Session }

// partyStubRegistry resolves any session ID to a stub, so prepareCandidates
// keeps every entry it is asked about.
type partyStubRegistry struct{ SessionRegistry }

func (partyStubRegistry) Get(uuid.UUID) Session { return partyStubSession{} }

// combatPartyCandidate is a prepared candidate holding one party ticket of n
// members, the shape the matchmaker hands to backfill.
func combatPartyCandidate(n int, extIP string) *preparedBackfillCandidate {
	entries := make([]*MatchmakerEntry, 0, n)
	sessions := make([]Session, 0, n)
	for range n {
		entries = append(entries, &MatchmakerEntry{
			Ticket:   "party-ticket",
			PartyId:  "party-1",
			Presence: &MatchmakerPresence{UserId: uuid.Must(uuid.NewV4()).String(), SessionId: uuid.Must(uuid.NewV4()).String()},
		})
		sessions = append(sessions, partyStubSession{})
	}
	return &preparedBackfillCandidate{
		BackfillCandidate: &BackfillCandidate{
			Ticket:         "party-ticket",
			Entries:        entries,
			Mode:           evr.ModeCombatPublic,
			TeamAlignment:  evr.TeamUnassigned,
			Rating:         15.0,
			MaxRTT:         180,
			RTTs:           map[string]int{extIP: 50},
			GroupID:        uuid.Must(uuid.NewV4()),
			SubmissionTime: time.Now().Add(-30 * time.Second),
		},
		sessions:  sessions,
		partySize: n,
	}
}

// placement is one call the placement path made to the executor.
type placement struct {
	team     int
	sessions int
	entries  int
}

// TestCombatBackfillPlacesPartyWhole: a 2-player Combat party in backfill goes
// onto ONE team in one placement, or is not placed if no team has 2 seats. It
// is never split across the teams.
func TestCombatBackfillPlacesPartyWhole(t *testing.T) {
	const ip = "203.0.113.9"

	cases := []struct {
		name                   string
		blueSlots, orangeSlots int
		blueCount, orangeCount int
		wantPlacements         int
	}{
		{"2v2 with room on both teams", 3, 3, 2, 2, 1},
		{"one seat per team", 1, 1, 4, 4, 0},
		{"two seats on one team only", 2, 1, 3, 4, 1},
		{"empty match", 5, 5, 0, 0, 1},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			b := &PostMatchmakerBackfill{logger: zap.NewNop(), sessionRegistry: partyStubRegistry{}}
			bctx := &backfillContext{now: time.Now()}
			matches := b.prepareMatches([]*BackfillMatch{
				validMatch(evr.ModeCombatPublic, ip, tc.blueSlots, tc.orangeSlots, tc.blueCount, tc.orangeCount),
			}, bctx)
			party := combatPartyCandidate(2, ip)

			var got []placement
			execute := func(_ context.Context, _ *zap.Logger, r *BackfillResult, sessions []Session, _ string) int {
				got = append(got, placement{team: r.Team, sessions: len(sessions), entries: len(r.Candidate.Entries)})
				return len(sessions)
			}

			if _, err := b.placeGroupCandidates(context.Background(), zap.NewNop(),
				[]*preparedBackfillCandidate{party}, matches, bctx, party.GroupID.String(), execute); err != nil {
				t.Fatal(err)
			}

			if len(got) != tc.wantPlacements {
				t.Fatalf("placements = %+v, want %d", got, tc.wantPlacements)
			}
			for _, p := range got {
				if p.sessions != 2 || p.entries != 2 {
					t.Errorf("party was split: placement %+v, want one placement of both members", p)
				}
			}
		})
	}
}
