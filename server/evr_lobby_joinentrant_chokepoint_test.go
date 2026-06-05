package server

import (
	"context"
	"testing"
	"time"

	"github.com/gofrs/uuid/v5"
	"github.com/heroiclabs/nakama-common/api"
	"github.com/heroiclabs/nakama/v3/server/evr"
	"go.uber.org/atomic"
)

// paramsSession is a Session whose Context() carries the given SessionParameters,
// so the chokepoint gate (enforceEntrantsAtChokepoint) can LoadParams() it.
type paramsSession struct {
	*DummySession
	id  uuid.UUID
	ctx context.Context
}

func (s *paramsSession) ID() uuid.UUID            { return s.id }
func (s *paramsSession) Context() context.Context { return s.ctx }

// chokepointSessionRegistry resolves a fixed set of sessions by ID.
type chokepointSessionRegistry struct {
	*testSessionRegistry
	byID map[uuid.UUID]Session
}

func (r *chokepointSessionRegistry) Get(id uuid.UUID) Session { return r.byID[id] }

func ctxWithParams(p *SessionParameters) context.Context {
	return context.WithValue(context.Background(), ctxSessionParametersKey{}, atomic.NewPointer(p))
}

// buildChokepointNk wires a RuntimeGoNakamaModule with a session registry that
// returns the given entrant sessions and a guild group registry containing gg.
func buildChokepointNk(t *testing.T, gg *GuildGroup, sessions map[uuid.UUID]Session) *RuntimeGoNakamaModule {
	t.Helper()
	// Use an already-cancelled context so the registry's background rebuild
	// goroutine (which would call nil nk/db) exits immediately.
	cancelledCtx, cancel := context.WithCancel(context.Background())
	cancel()
	reg := NewGuildGroupRegistry(cancelledCtx, nil, nil, nil)
	if gg != nil {
		reg.Add(gg)
	}
	nk := &RuntimeGoNakamaModule{
		sessionRegistry: &chokepointSessionRegistry{byID: sessions},
	}
	nk.SetGuildGroupRegistry(reg)
	return nk
}

// TestChokepoint_DropsSuspendedEntrant is the H1/H2/H3 shared regression: every
// funnel path (backfill / spectator / social priority-join / matchmaker-built /
// setnextmatch) routes through LobbyJoinEntrants -> enforceEntrantsAtChokepoint.
// This proves the chokepoint DROPS a suspended/ineligible entrant against the
// BUILT match's actual group, and KEEPS a clean entrant.
//
// Against pristine /srv/src/nakama enforceEntrantsAtChokepoint does not exist
// (RED: compile failure). With the fix the suspended entrant is dropped.
func TestChokepoint_DropsSuspendedEntrant(t *testing.T) {
	groupID := uuid.Must(uuid.NewV4()).String()
	groupUUID := uuid.FromStringOrNil(groupID)
	mode := evr.ModeArenaPublic

	suspendedUser := uuid.Must(uuid.NewV4())
	cleanUser := uuid.Must(uuid.NewV4())

	// Guild group with a journal-backed suspension for suspendedUser.
	gg := &GuildGroup{
		GroupMetadata: GroupMetadata{},
		State:         &GuildGroupState{GroupID: groupID, RoleCache: map[string]map[string]bool{}},
		Group:         &api.Group{Id: groupID, Name: "Built Guild"},
	}

	journal := NewGuildEnforcementJournal(suspendedUser.String())
	journal.AddRecord(groupID, "mod", "moddisc", "banned from built guild", "notes", false, false, time.Hour)
	enf, err := CheckEnforcementSuspensions(
		GuildEnforcementJournalList{suspendedUser.String(): journal},
		map[string][]string{groupID: {}},
	)
	if err != nil {
		t.Fatalf("CheckEnforcementSuspensions: %v", err)
	}

	// Sessions carrying params. The suspended user's params include the
	// suspension snapshot (the chokepoint also re-reads fresh, but with no DB the
	// re-read is skipped because enforcementUserIDs is empty -> snapshot used).
	suspSID := uuid.Must(uuid.NewV4())
	cleanSID := uuid.Must(uuid.NewV4())

	suspParams := &SessionParameters{gameModeSuspensionsByGroupID: enf}
	cleanParams := &SessionParameters{gameModeSuspensionsByGroupID: ActiveGuildEnforcements{}}

	sessions := map[uuid.UUID]Session{
		suspSID:  &paramsSession{DummySession: &DummySession{uid: suspendedUser}, id: suspSID, ctx: ctxWithParams(suspParams)},
		cleanSID: &paramsSession{DummySession: &DummySession{uid: cleanUser}, id: cleanSID, ctx: ctxWithParams(cleanParams)},
	}

	nk := buildChokepointNk(t, gg, sessions)

	label := &MatchLabel{
		ID:      MatchID{UUID: uuid.Must(uuid.NewV4())},
		Mode:    mode,
		GroupID: &groupUUID,
	}

	suspEntrant := &EvrMatchPresence{UserID: suspendedUser, SessionID: suspSID}
	cleanEntrant := &EvrMatchPresence{UserID: cleanUser, SessionID: cleanSID}

	logger := NewConsoleLogger(nil, false)

	t.Run("suspended_primary_dropped", func(t *testing.T) {
		kept := enforceEntrantsAtChokepoint(logger, nk, label, []*EvrMatchPresence{suspEntrant})
		if len(kept) != 0 {
			t.Fatalf("BUG: suspended entrant seated into built guild via placement path; kept=%d", len(kept))
		}
	})

	t.Run("clean_entrant_kept", func(t *testing.T) {
		kept := enforceEntrantsAtChokepoint(logger, nk, label, []*EvrMatchPresence{cleanEntrant})
		if len(kept) != 1 {
			t.Fatalf("clean entrant wrongly dropped; kept=%d", len(kept))
		}
	})

	t.Run("mixed_party_only_suspended_dropped", func(t *testing.T) {
		kept := enforceEntrantsAtChokepoint(logger, nk, label, []*EvrMatchPresence{cleanEntrant, suspEntrant})
		if len(kept) != 1 || kept[0].UserID != cleanUser {
			t.Fatalf("expected only the clean entrant kept; got %d entrants", len(kept))
		}
	})
}
