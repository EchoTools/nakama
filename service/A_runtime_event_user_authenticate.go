package service

import (
	evr "github.com/echotools/nakama/v3/protocol"
)

type EventUserAuthenticated struct {
	UserID                   string            `json:"user_id"`
	XPID                     evr.XPID          `json:"xpid"`
	ClientIP                 string            `json:"client_ip"`
	LoginPayload             *evr.LoginProfile `json:"login_data"`
	IsWebSocketAuthenticated bool              `json:"is_websocket_authenticated"`
}

func NewUserAuthenticatedEvent(userID string, xpid evr.XPID, clientIP string, loginPayload *evr.LoginProfile, isWebSocketAuthenticated bool) *EventUserAuthenticated {
	return &EventUserAuthenticated{
		UserID:                   userID,
		XPID:                     xpid,
		ClientIP:                 clientIP,
		LoginPayload:             loginPayload,
		IsWebSocketAuthenticated: isWebSocketAuthenticated,
	}
}
