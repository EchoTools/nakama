package server

import (
	"context"
	"testing"
	"time"

	"github.com/gofrs/uuid/v5"
)

func TestPartySyncMatchmakingNilDoesNotCancelParty(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	consoleLogger := loggerForTest(t)
	partyHandler, cleanup := createTestPartyHandler(t, consoleLogger)
	defer cleanup()

	node := "node1"

	// Test that all party members context's are syncronized (stop together)

	msessions := make([]*MatchmakingSession, 0)

	for i := 0; i < 8; i++ {
		sessionCtx := context.WithoutCancel(ctx)
		ctx, cancel := context.WithCancelCause(sessionCtx)

		userID := uuid.Must(uuid.NewV4())
		sessionID := uuid.Must(uuid.NewV4())
		sessionWS := &sessionWS{
			ctx:    sessionCtx,
			id:     sessionID,
			userID: userID,
		}

		msessions = append(msessions, &MatchmakingSession{
			Ctx:         ctx,
			CtxCancelFn: cancel,
			Session:     sessionWS,
		})

		partyHandler.Join([]*Presence{{
			ID: PresenceID{
				Node:      node,
				SessionID: sessionID,
			},
			// Presence stream not needed.
			UserID: userID,
			Meta: PresenceMeta{
				Username: "username",
				// Other meta fields not needed.
			},
		}})
	}
	timeout := 1 * time.Second
	// Start syncronized matchmaking
	reason := make(chan error)
	go func() {
		select {
		case <-ctx.Done():
			reason <- nil
		case reason <- PartySyncMatchmaking(ctx, msessions, partyHandler, timeout):
		}
	}()

	msessions[0].Cancel(nil) // A join to a match caused this cancel
	<-time.After(200 * time.Millisecond)
	// Check that all sessions have been canceled
	for _, ms := range msessions[1:] {
		select {
		case <-ms.Ctx.Done():
			t.Errorf("PartySyncMatchmaking canceled a context that should not have been canceled")
		default:
		}
	}

	select {
	case <-reason:
		t.Fatalf("PartySyncMatchmaking returned early")
	default:
	}
}

func TestPartySyncMatchmakingErrorWhenNonNil(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	consoleLogger := loggerForTest(t)
	partyHandler, cleanup := createTestPartyHandler(t, consoleLogger)
	defer cleanup()

	node := "node1"

	// Test that all party members context's are syncronized (stop together)

	msessions := make([]*MatchmakingSession, 0)

	for i := 0; i < 8; i++ {
		sessionCtx := context.WithoutCancel(ctx)
		ctx, cancel := context.WithCancelCause(sessionCtx)

		userID := uuid.Must(uuid.NewV4())
		sessionID := uuid.Must(uuid.NewV4())
		sessionWS := &sessionWS{
			ctx:    sessionCtx,
			id:     sessionID,
			userID: userID,
		}

		msessions = append(msessions, &MatchmakingSession{
			Ctx:         ctx,
			CtxCancelFn: cancel,
			Session:     sessionWS,
		})

		partyHandler.Join([]*Presence{{
			ID: PresenceID{
				Node:      node,
				SessionID: sessionID,
			},
			// Presence stream not needed.
			UserID: userID,
			Meta: PresenceMeta{
				Username: "username",
				// Other meta fields not needed.
			},
		}})
	}

	timeout := 1 * time.Second
	// Start syncronized matchmaking
	reason := make(chan error)
	go func() {
		select {
		case <-ctx.Done():
			reason <- nil
		case reason <- PartySyncMatchmaking(ctx, msessions, partyHandler, timeout):
		}
	}()

	msessions[0].Cancel(ErrMatchmakingCanceledByPlayer) // A join to a match caused this cancel

	<-time.After(200 * time.Millisecond)
	// Check that all sessions have been canceled
	for _, ms := range msessions[1:] {
		select {
		case <-ms.Ctx.Done():
		default:
			t.Errorf("PartySyncMatchmaking did not cancel a context that should have been canceled")
		}
	}

	select {
	case <-reason:
	default:
		t.Fatalf("PartySyncMatchmaking returned late")
	}
}

func TestPartySyncMatchmakingRespectsPartyTimeout(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	consoleLogger := loggerForTest(t)
	partyHandler, cleanup := createTestPartyHandler(t, consoleLogger)
	defer cleanup()

	node := "node1"

	// Test that all party members context's are syncronized (stop together)

	msessions := make([]*MatchmakingSession, 0)

	for i := 0; i < 8; i++ {
		sessionCtx := context.WithoutCancel(ctx)
		ctx, cancel := context.WithCancelCause(sessionCtx)

		userID := uuid.Must(uuid.NewV4())
		sessionID := uuid.Must(uuid.NewV4())
		sessionWS := &sessionWS{
			ctx:    sessionCtx,
			id:     sessionID,
			userID: userID,
		}

		msessions = append(msessions, &MatchmakingSession{
			Ctx:         ctx,
			CtxCancelFn: cancel,
			Session:     sessionWS,
		})

		partyHandler.Join([]*Presence{{
			ID: PresenceID{
				Node:      node,
				SessionID: sessionID,
			},
			// Presence stream not needed.
			UserID: userID,
			Meta: PresenceMeta{
				Username: "username",
				// Other meta fields not needed.
			},
		}})
	}

	timeout := 100 * time.Millisecond
	// Start syncronized matchmaking
	reason := make(chan error)
	go func() {
		select {
		case <-ctx.Done():
			reason <- nil
		case reason <- PartySyncMatchmaking(ctx, msessions, partyHandler, timeout):
		}
	}()

	// Check that party times out
	select {
	case err := <-reason:
		if err != ErrMatchmakingCanceledTimeout {
			t.Fatalf("PartySyncMatchmaking did not return the correct error")
		}
	case <-time.After(2 * timeout):
		t.Fatalf("PartySyncMatchmaking did not return after timeout")
	}
	<-time.After(300 * time.Millisecond)
	// Check that all sessions have been canceled
	for _, ms := range msessions[1:] {
		select {
		case <-ms.Ctx.Done():
			err := context.Cause(ms.Ctx)
			if err != ErrMatchmakingCanceledTimeout {
				t.Fatalf("PartySyncMatchmaking did not end contexts with correct error")
			}
		default:
			t.Errorf("PartySyncMatchmaking did not cancel a context that should have been canceled")
		}
	}
}
