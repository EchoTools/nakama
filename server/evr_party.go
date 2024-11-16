package server

import (
	"errors"
)

var (
	ErrNoParty                    = errors.New("no party")
	ErrNoEntrants                 = errors.New("no entrants")
	ErrLeaderAndFollowerSameMatch = errors.New("leader and follower are in the same match")
	ErrLeaderNotInMatch           = errors.New("leader is not in a match")
	ErrLeaderMatchNotPublic       = errors.New("leader's match is not public")
	ErrLeaderMatchNotOpen         = errors.New("leader's match is not open")
	ErrFollowerIsLeader           = errors.New("follower is the leader")
	ErrFollowerNotInMatch         = errors.New("follower is not in a match")
	ErrUnknownError               = errors.New("unknown error")
	ErrJoinFailed                 = errors.New("join failed")
)
