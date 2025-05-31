package server

import (
	"context"

	"github.com/heroiclabs/nakama-common/runtime"
)

type EventVRMLAccountLink struct {
	UserID   string `json:"user_id"`
	Token    string `json:"token"`               // Will only be set if the user is new
	PlayerID string `json:"player_id,omitempty"` // Optional, used for VRML player ID
}

func (e *EventVRMLAccountLink) Process(ctx context.Context, logger runtime.Logger, dispatcher *EventDispatcher) error {
	return dispatcher.vrmlVerifier.Add(e.UserID, e.Token, e.PlayerID)
}
