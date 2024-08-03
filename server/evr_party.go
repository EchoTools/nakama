package server

import (
	"context"
	"errors"
	"fmt"
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
func PartySyncMatchmaking(ctx context.Context, msessions []*MatchmakingSession, timeout time.Duration) error {
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

		// If the leader is not in a match, they might be soon.
		if leaderMatchID.IsNil() {
			continue
		}

		followerMatchID, _, err := GetMatchBySessionID(nk, session.id)
		if err != nil {
			return
		}

		if followerMatchID == leaderMatchID {
			return
		}
		label, err := MatchLabelByID(msession.Context(), nk, leaderMatchID)
		if err != nil {
			return
		}

		if label == nil || !label.Open {
			continue
		}

		// Try to join the leader's match
		err = session.evrPipeline.LobbyJoin(session.Context(), logger, leaderMatchID, int(AnyTeam), "", msession)
		if err == nil {
			// Successful
			return
		}
	}
}

func FollowUserID(logger *zap.Logger, msession *MatchmakingSession, nk runtime.NakamaModule, userID string) error {
	// Look up the leaders current match

	session := msession.Session
	for {
		select {
		case <-msession.Ctx.Done():
			return nil
		case <-time.After(2 * time.Second):
		}
		presences, err := nk.StreamUserList(StreamModeService, userID, StreamContextMatch.String(), "", true, true)
		if err != nil {
			return fmt.Errorf("error listing user stream: %v", err)
		}
		if len(presences) == 0 {
			return fmt.Errorf("no presences found for user %v", userID)
		}
		leaderSessionID := uuid.FromStringOrNil(presences[0].GetSessionId())
		if leaderSessionID == session.id {
			return fmt.Errorf("leader is the same as the follower")
		}

		leaderMatchID, _, err := GetMatchBySessionID(nk, leaderSessionID)
		if err != nil {
			return fmt.Errorf("error getting match by session id: %v", err)
		}

		// If the leader is not in a match, they might be soon.
		if leaderMatchID.IsNil() {
			continue
		}

		followerMatchID, _, err := GetMatchBySessionID(nk, session.id)
		if err != nil {
			return fmt.Errorf("error getting match by session id: %v", err)
		}

		if followerMatchID == leaderMatchID {
			return fmt.Errorf("follower is already in the leader's match")
		}
		// Try to join the leader's match
		return session.evrPipeline.LobbyJoin(session.Context(), logger, leaderMatchID, int(AnyTeam), "", msession)
	}
}
