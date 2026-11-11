package server

import (
	"github.com/heroiclabs/nakama-common/runtime"
	"go.uber.org/zap"
)

// Reasons the session registry passes to Session.Close, so the close line can
// tell a ban, a duplicate login and a server-initiated disconnect apart from a
// client that simply went away.
const (
	closeReasonClientClosed     = "client closed"
	closeReasonServerDisconnect = "disconnected by server"
	closeReasonBanned           = "banned"
	closeReasonDuplicateSession = "duplicate session"
)

func presenceReasonName(reason runtime.PresenceReason) string {
	switch reason {
	case runtime.PresenceReasonJoin:
		return "join"
	case runtime.PresenceReasonUpdate:
		return "update"
	case runtime.PresenceReasonLeave:
		return "leave"
	case runtime.PresenceReasonDisconnect:
		return "disconnect"
	default:
		return "unknown"
	}
}

// logSessionClosed is the one close line for every websocket session. msg is
// what the first Close caller passed; an empty msg means nothing on the server
// side ended the session, i.e. the client closed the socket.
func logSessionClosed(logger *zap.Logger, msg string, reason runtime.PresenceReason) {
	if msg == "" {
		msg = closeReasonClientClosed
	}
	logger.Info("Closed client connection",
		zap.String("close_reason", msg),
		zap.String("presence_reason", presenceReasonName(reason)))
}
