package server

import (
	"context"
	"fmt"
	"testing"

	"github.com/gofrs/uuid/v5"
	"github.com/heroiclabs/nakama-common/runtime"
	"github.com/heroiclabs/nakama/v3/server/evr"
	"go.uber.org/zap"
)

// mockOnlinePresence is a runtime.Presence with a configurable session ID and
// username, used to simulate a party member's live login-service presence.
type mockOnlinePresence struct {
	userID    string
	sessionID string
	username  string
}

func (p *mockOnlinePresence) GetUserId() string                 { return p.userID }
func (p *mockOnlinePresence) GetSessionId() string              { return p.sessionID }
func (p *mockOnlinePresence) GetNodeId() string                 { return "testnode" }
func (p *mockOnlinePresence) GetHidden() bool                   { return false }
func (p *mockOnlinePresence) GetPersistence() bool              { return false }
func (p *mockOnlinePresence) GetUsername() string               { return p.username }
func (p *mockOnlinePresence) GetStatus() string                 { return "" }
func (p *mockOnlinePresence) GetReason() runtime.PresenceReason { return runtime.PresenceReasonUnknown }

// mockNakamaModuleForReservations serves login-service presence lookups for a
// fixed set of "online" users, and asserts the caller only ever queries the
// login-service stream (the primitive getOnlinePartyReservations is documented
// to use for the online check).
type mockNakamaModuleForReservations struct {
	runtime.NakamaModule
	online map[string]*mockOnlinePresence // keyed by userID
	calls  []string                       // userIDs queried, in order
}

func (m *mockNakamaModuleForReservations) StreamUserList(mode uint8, subject, _ string, label string, _, _ bool) ([]runtime.Presence, error) {
	if mode != StreamModeService || label != StreamLabelLoginService {
		return nil, fmt.Errorf("unexpected stream lookup: mode=%d label=%s", mode, label)
	}
	m.calls = append(m.calls, subject)
	p, ok := m.online[subject]
	if !ok {
		return nil, nil
	}
	return []runtime.Presence{p}, nil
}

func TestGetOnlinePartyReservations(t *testing.T) {
	logger := NewRuntimeGoLogger(zap.NewNop())
	creatorID := uuid.Must(uuid.NewV4()).String()
	followerOnlineID := uuid.Must(uuid.NewV4()).String()
	followerOfflineID := uuid.Must(uuid.NewV4()).String()
	followerOnlineSession := uuid.Must(uuid.NewV4())

	nk := &mockNakamaModuleForReservations{
		online: map[string]*mockOnlinePresence{
			followerOnlineID: {
				userID:    followerOnlineID,
				sessionID: followerOnlineSession.String(),
				username:  "follower-online",
			},
			// creator intentionally omitted -- must never be queried (skipped
			// before any lookup, since the creator joins directly).
		},
	}

	ctx := context.WithValue(context.Background(), runtime.RUNTIME_CTX_NODE, "testnode")

	t.Run("solo creator produces no reservations", func(t *testing.T) {
		nk.calls = nil
		got := getOnlinePartyReservations(ctx, nk, logger, creatorID, []string{creatorID}, evr.TeamBlue)
		if len(got) != 0 {
			t.Fatalf("expected no reservations for solo creator, got %d", len(got))
		}
		if len(nk.calls) != 0 {
			t.Fatalf("expected creator to be skipped without a stream lookup, got calls=%v", nk.calls)
		}
	})

	t.Run("online follower gets a reservation, offline follower is skipped", func(t *testing.T) {
		nk.calls = nil
		partyUserIDs := []string{creatorID, followerOnlineID, followerOfflineID}
		got := getOnlinePartyReservations(ctx, nk, logger, creatorID, partyUserIDs, evr.TeamBlue)

		if len(got) != 1 {
			t.Fatalf("expected exactly 1 reservation (online follower only), got %d: %+v", len(got), got)
		}

		r := got[0]
		if r.UserID.String() != followerOnlineID {
			t.Errorf("expected reservation for user %s, got %s", followerOnlineID, r.UserID.String())
		}
		if r.SessionID != followerOnlineSession {
			t.Errorf("expected reservation session %s, got %s", followerOnlineSession, r.SessionID)
		}
		if r.Username != "follower-online" {
			t.Errorf("expected username 'follower-online', got %q", r.Username)
		}
		if r.RoleAlignment != evr.TeamBlue {
			t.Errorf("expected role alignment %d, got %d", evr.TeamBlue, r.RoleAlignment)
		}
		if r.Node != "testnode" {
			t.Errorf("expected node 'testnode', got %q", r.Node)
		}

		// Both followers should have been checked; the offline one produced no reservation.
		checked := map[string]bool{}
		for _, c := range nk.calls {
			checked[c] = true
		}
		if !checked[followerOnlineID] || !checked[followerOfflineID] {
			t.Errorf("expected both followers to be checked for online status, got calls=%v", nk.calls)
		}
		if checked[creatorID] {
			t.Errorf("creator should never be queried for online status, got calls=%v", nk.calls)
		}
	})

	t.Run("empty party produces no reservations", func(t *testing.T) {
		got := getOnlinePartyReservations(ctx, nk, logger, creatorID, nil, evr.TeamBlue)
		if len(got) != 0 {
			t.Fatalf("expected no reservations for empty party, got %d", len(got))
		}
	})

	t.Run("missing node in context bails out", func(t *testing.T) {
		got := getOnlinePartyReservations(context.Background(), nk, logger, creatorID, []string{creatorID, followerOnlineID}, evr.TeamBlue)
		if got != nil {
			t.Fatalf("expected nil reservations when node is missing from context, got %+v", got)
		}
	})
}
