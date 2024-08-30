package server

import (
	"errors"
	"fmt"

	"github.com/gofrs/uuid/v5"
	"github.com/heroiclabs/nakama/v3/server/evr"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

var (
	ErrorEntrantNotFound       = errors.New("entrant not found")
	ErrorMultipleEntrantsFound = errors.New("multiple entrants found")
	ErrMatchNotFound           = NewLobbyError(ServerDoesNotExist, "match not found")
	ErrSuspended               = NewLobbyError(KickedFromLobbyGroup, "User is suspended from this guild")
)

// LobbyErrorCode defines the type for lobby error codes.
type LobbyErrorCode int

const (
	TimeoutServerFindFailed LobbyErrorCode = iota
	UpdateRequired
	BadRequest
	Timeout
	ServerDoesNotExist
	ServerIncompatible
	ServerFindFailed
	ServerIsLocked
	ServerIsFull
	InternalError
	MissingEntitlement
	BannedFromLobbyGroup
	KickedFromLobbyGroup
	NotALobbyGroupMod
)

// LobbyError struct that implements the error interface.
type LobbyError struct {
	Code    LobbyErrorCode
	Message string
}

// Error implements the error interface.
func (e LobbyError) Error() string {
	message := e.Message
	switch e.Code {
	case TimeoutServerFindFailed:
		message = "server find failed: " + message
	case UpdateRequired:
		message = "update required: " + message
	case BadRequest:
		message = "bad request: " + message
	case Timeout:
		message = "server does not exist: " + message
	case ServerIncompatible:
		message = "server is incompatible: " + message
	case ServerFindFailed:
		message = "server find failed: " + message
	case ServerIsLocked:
		message = "server is locked: " + message
	case ServerIsFull:
		message = "server is full: " + message
	case InternalError:
		message = "internal error: " + message
	case MissingEntitlement:
		message = "missing entitlement: " + message
	case BannedFromLobbyGroup:
		message = "banned: " + message
	case KickedFromLobbyGroup:
		message = "kicked: " + message
	case NotALobbyGroupMod:
		message = "not a mod: " + message
	}
	return message
}

// NewLobbyErrorf creates a new LobbyError with the given code and message.
func NewLobbyError(code LobbyErrorCode, message string) *LobbyError {
	return &LobbyError{
		Code:    code,
		Message: message,
	}
}

// NewLobbyErrorf creates a new LobbyError with the given code and message.
func NewLobbyErrorf(code LobbyErrorCode, message string, a ...any) *LobbyError {
	return &LobbyError{
		Code:    code,
		Message: fmt.Sprintf(message, a...),
	}
}

func LobbySessionFailureFromError(mode evr.Symbol, groupID uuid.UUID, err error) *evr.LobbySessionFailurev4 {
	if err == nil {
		return nil
	}

	var code evr.LobbySessionFailureErrorCode
	var message string

	if status.Code(err) != codes.Unknown {
		// This is a grpc status error.
		message = status.Convert(err).Message()
		switch status.Code(err) {
		case codes.OK:
			return nil
		case codes.Canceled:
			code = evr.LobbySessionFailure_BadRequest
		case codes.InvalidArgument:
			code = evr.LobbySessionFailure_BadRequest
		case codes.NotFound:
			code = evr.LobbySessionFailure_ServerDoesNotExist
		case codes.AlreadyExists:
			code = evr.LobbySessionFailure_ServerIsIncompatible
		case codes.PermissionDenied:
			code = evr.LobbySessionFailure_KickedFromLobbyGroup
		case codes.ResourceExhausted:
			code = evr.LobbySessionFailure_ServerIsFull
		case codes.FailedPrecondition:
			code = evr.LobbySessionFailure_ServerIsIncompatible
		case codes.Aborted:
			code = evr.LobbySessionFailure_InternalError
		case codes.OutOfRange:
			code = evr.LobbySessionFailure_InternalError
		case codes.Unimplemented:
			code = evr.LobbySessionFailure_InternalError
		case codes.Internal:
			code = evr.LobbySessionFailure_InternalError
		case codes.Unavailable:
			code = evr.LobbySessionFailure_ServerFindFailed
		case codes.DataLoss:
			code = evr.LobbySessionFailure_InternalError
		case codes.Unauthenticated:
			code = evr.LobbySessionFailure_InternalError
		case codes.DeadlineExceeded:
			code = evr.LobbySessionFailure_Timeout_ServerFindFailed
		}
	} else if lobbyErr, ok := err.(*LobbyError); ok {
		code = evr.LobbySessionFailureErrorCode(lobbyErr.Code)
		message = lobbyErr.Message
	} else {
		code = evr.LobbySessionFailure_InternalError
		message = err.Error()
	}
	return evr.NewLobbySessionFailure(mode, groupID, code, message).Version4()
}

func LobbyErrorIs(err error, code LobbyErrorCode) bool {
	lobbyError := &LobbyError{}
	found := errors.As(err, lobbyError)
	if !found {
		return false
	}
	return lobbyError.Code == code
}
