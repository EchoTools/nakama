package server

import (
	"context"
	"errors"
	"time"
)

type EvrParty struct{}

var (
	ErrMatchmakingCanceledPartyMember = errors.New("matchmaking canceled by party member")
	ErrMatchmakingCanceledPartyLeader = errors.New("matchmaking canceled by party leader")
	ErrMatchmakingCanceledTimeout     = errors.New("matchmaking canceled by timeout")
	ErrMatchmakingCanceledJoin        = errors.New("matchmaking canceled by join")
)

// Syncronize the contexts of all party members
func PartySyncMatchmaking(msessions []*MatchmakingSession, ph *PartyHandler, timeout time.Duration) error {
	partyCtx, partyCancel := context.WithCancelCause(context.Background())
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
