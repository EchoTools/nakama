package server

import (
	"context"
	"errors"
	"time"

	"github.com/gofrs/uuid/v5"
	"github.com/heroiclabs/nakama-common/runtime"
)

var (
	ErrMatchmakingCanceledPartyMember = errors.New("matchmaking canceled by party member")
	ErrMatchmakingCanceledPartyLeader = errors.New("matchmaking canceled by party leader")
	ErrMatchmakingCanceledTimeout     = errors.New("matchmaking canceled by timeout")
	ErrMatchmakingCanceledJoin        = errors.New("matchmaking canceled by join")
)

// Syncronize the contexts of all party members
func PartySyncMatchmaking(ctx context.Context, msessions []*MatchmakingSession, ph *PartyHandler, timeout time.Duration) error {
	partyCtx, partyCancel := context.WithCancelCause(ctx)
	defer partyCancel(nil)

	// Determine which party member is the leader
	for _, ms := range msessions {
		go func(ms *MatchmakingSession) {
			select {
			case <-partyCtx.Done():
				if partyCtx.Err() != context.Cause(partyCtx) {
					ms.CtxCancelFn(context.Cause(partyCtx))
				}
			case <-ms.Ctx.Done():
				if ms.Ctx.Err() != context.Cause(ms.Ctx) {
					partyCancel(ErrMatchmakingCanceledByParty)
				}
			}
		}(ms)
	}

	select {
	case <-time.After(timeout):
		partyCancel(ErrMatchmakingCanceledTimeout)
		return ErrMatchmakingCanceledTimeout
	case <-partyCtx.Done():
	}

	if partyCtx.Err() != context.Cause(partyCtx) {
		return context.Cause(partyCtx)
	}
	return nil
}

func FollowLeader(ctx context.Context, nk runtime.NakamaModule, session *sessionWS, pg *PartyGroup) {
	// Look up the leaders current match
	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(2 * time.Second):
		}

		leaderSessionID := uuid.FromStringOrNil(pg.GetLeader().SessionId)
		if leaderSessionID == session.id {
			return
		}
		// Get the match that the leader is in
		followerMatchID, _, err := GetMatchBySessionID(nk, session.id)
		if err != nil {
			return
		}

		// Get this players current match
		leaderMatchID, _, err := GetMatchBySessionID(nk, leaderSessionID)
		if err != nil {
			return
		}

		if followerMatchID.IsNil() || leaderMatchID.IsNil() {
			continue
		}

		if followerMatchID == leaderMatchID {
			<-time.After(5 * time.Second)
			continue // Already in the same match, but keep checking in case the leader leaves
		}
		// Try to join the leader's match
		err = session.evrPipeline.JoinEvrMatch(ctx, session.logger, session, "", leaderMatchID, int(AnyTeam))
		if err == nil {
			return
		}

	}
}
