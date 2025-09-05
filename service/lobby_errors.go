package service

import (
	"errors"
	"fmt"
	"time"

	evr "github.com/echotools/nakama/v3/protocol"
	"github.com/gofrs/uuid/v5"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

var (
	ErrEntrantNotFound       = errors.New("entrant not found")
	ErrMultipleEntrantsFound = errors.New("multiple entrants found")
	ErrMatchNotFound         = NewLobbyError(ServerDoesNotExist, "match not found")
	ErrSuspended             = NewLobbyError(KickedFromLobbyGroup, "User is suspended from this guild")
	ErrFailedToAcquireLock   = NewLobbyError(InternalError, "Failed to acquire lock")
)

// LobbyErrorCodeValue defines the type for lobby error codes.
type LobbyErrorCodeValue int

func (c LobbyErrorCodeValue) String() string {
	if msg, ok := LobbyErrorMessages[c]; ok {
		return msg
	}
	return "unknown"
}

// The list of lobby error codes. These are hard-coded in the evr client.
const (
	LobbyUnknownError LobbyErrorCodeValue = iota - 1 // Custom error code for unknown errors.
	TimeoutServerFindFailed
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
	SuspendedFromLobbyGroup
	KickedFromLobbyGroup
	NotALobbyGroupMod
)

var LobbyErrorMessages = map[LobbyErrorCodeValue]string{
	TimeoutServerFindFailed: "time_server_find_failed",
	UpdateRequired:          "update_required",
	BadRequest:              "bad_request",
	Timeout:                 "timeout",
	ServerDoesNotExist:      "server_does_not_exist",
	ServerIncompatible:      "server_incompatible",
	ServerFindFailed:        "server_find_failed",
	ServerIsLocked:          "server_is_locked",
	ServerIsFull:            "server_is_full",
	InternalError:           "internal_error",
	MissingEntitlement:      "missing_entitlement",
	SuspendedFromLobbyGroup: "suspended_from_lobby_group",
	KickedFromLobbyGroup:    "kicked_from_lobby_group",
	NotALobbyGroupMod:       "not_a_lobby_group_mod",
}

// LobbyError struct that implements the error interface.
type LobbyError struct {
	code       LobbyErrorCodeValue
	message    string
	expiry     time.Time
	wrappedErr error
}

// NewLobbyErrorf creates a new LobbyError with the given code and message.
func NewLobbyError(code LobbyErrorCodeValue, message string) LobbyError {
	return LobbyError{
		code:    code,
		message: message,
	}
}

func NewLobbyErrorWithExpiry(code LobbyErrorCodeValue, message string, expiry time.Time) LobbyError {
	return LobbyError{
		code:    code,
		message: message,
		expiry:  expiry,
	}
}

// NewLobbyErrorf creates a new LobbyError with the given code and message.
func NewLobbyErrorf(code LobbyErrorCodeValue, format string, a ...any) LobbyError {
	err := fmt.Errorf(format, a...)
	return LobbyError{
		code:       code,
		message:    err.Error(),
		wrappedErr: err,
	}
}

// Error implements the error interface.
func (e LobbyError) Error() string {
	message := e.message
	switch e.code {
	case TimeoutServerFindFailed:
		message = "timeout: server find failed: " + message
	case UpdateRequired:
		message = "update required: " + message
	case BadRequest:
		message = "bad request: " + message
	case Timeout:
		message = "timeout: " + message
	case ServerDoesNotExist:
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
	case SuspendedFromLobbyGroup:
		message = "suspended: " + message
	case KickedFromLobbyGroup:
		message = "kicked: " + message
	case NotALobbyGroupMod:
		message = "not a mod: " + message
	default:
		message = "unknown error: " + message
	}
	return message
}

func (e LobbyError) Message() string {
	return e.message
}

func (e LobbyError) Expiry() time.Time {
	return e.expiry
}
func (e LobbyError) Code() LobbyErrorCodeValue {
	return e.code
}

func (e LobbyError) Is(target error) bool {
	if t, ok := target.(LobbyError); ok {
		return e.code == t.code
	}
	return false
}

func (e LobbyError) WrappedErr() error {
	return e.wrappedErr
}

func (e LobbyError) String() string {
	return e.Error()
}

func (e LobbyError) Unwrap() []error {
	return []error{e.wrappedErr}
}

// LobbySessionFailureFromError converts an error into a LobbySessionFailure message.
func LobbySessionFailureFromError(mode evr.Symbol, groupID uuid.UUID, err error) evr.Message {
	if err == nil {
		return nil
	}

	var code evr.LobbySessionFailureErrorCode
	var message string

	// If the error is a LobbyError, use the code and message from it.
	if lErr, ok := err.(LobbyError); ok {
		code = evr.LobbySessionFailureErrorCode(lErr.code)
		message = lErr.Message()

		// If the error is a wrapped LobbyError, use the code and original message
	} else if errors.As(err, &lErr) {
		code = evr.LobbySessionFailureErrorCode(lErr.code)
		// Keep the original message.
		message = err.Error()

	} else if status.Code(err) != codes.Unknown {
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

	} else {
		code = evr.LobbySessionFailure_InternalError
		message = err.Error()
	}

	return evr.NewLobbySessionFailure(mode, groupID, code, message).Version4()
}

// LobbyErrorIs checks if the given error is a LobbyError with the given code.
func LobbyErrorIs(err error, code LobbyErrorCodeValue) bool {
	var lErr LobbyError
	return errors.As(err, &lErr) && lErr.code == code
}

func LobbyErrorCode(err error) LobbyErrorCodeValue {
	var lErr LobbyError
	if errors.As(err, &lErr) {
		return lErr.code
	}
	return LobbyUnknownError
}
