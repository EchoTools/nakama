package service

import (
	"time"

	evr "github.com/echotools/nakama/v3/protocol"
)

type EventUserAuthenticated struct {
	UserID                   string            `json:"user_id"`
	XPID                     evr.XPID          `json:"xpid"`
	ClientIP                 string            `json:"client_ip"`
	LoginPayload             *evr.LoginProfile `json:"login_data"`
	IsWebSocketAuthenticated bool              `json:"is_websocket_authenticated"`
	LoginDuration            time.Duration     `json:"login_duration,omitempty"`
}

func NewUserAuthenticatedEvent(userID string, xpid evr.XPID, clientIP string, loginPayload *evr.LoginProfile, isWebSocketAuthenticated bool, loginDuration time.Duration) *EventUserAuthenticated {
	return &EventUserAuthenticated{
		UserID:                   userID,
		XPID:                     xpid,
		ClientIP:                 clientIP,
		LoginPayload:             loginPayload,
		IsWebSocketAuthenticated: isWebSocketAuthenticated,
		LoginDuration:            loginDuration,
	}
}
