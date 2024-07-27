package server

import (
	"context"
	"errors"
	"time"

	"github.com/gofrs/uuid/v5"
	"github.com/heroiclabs/nakama-common/runtime"
	"go.uber.org/zap"
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

func FollowLeader(logger *zap.Logger, msession *MatchmakingSession, nk runtime.NakamaModule) {
	// Look up the leaders current match
	if msession.Party == nil {
		return
	}

	session := msession.Session
	for {
		select {
		case <-msession.Ctx.Done():
			return
		case <-time.After(2 * time.Second):
		}

		leaderSessionID := uuid.FromStringOrNil(msession.Party.GetLeader().SessionId)
		if leaderSessionID == session.id {
			return
		}

		leaderMatchID, _, err := GetMatchBySessionID(nk, leaderSessionID)
		if err != nil {
			return
		}

		// Get the match that the leader is in
		followerMatchID, _, err := GetMatchBySessionID(nk, session.id)
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
		err = session.evrPipeline.LobbyJoin(session.Context(), logger, leaderMatchID, int(AnyTeam), "", msession)
		if err == nil {
			return
		}
	}
}
