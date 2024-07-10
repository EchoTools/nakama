package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/gofrs/uuid/v5"
	"github.com/heroiclabs/nakama-common/runtime"
)

type HTTPRequest[T any] struct {
	Data T           `json:"data"`
	Meta RequestMeta `json:"meta"`
}

func (r *HTTPRequest[T]) ID() string {
	return r.Meta.RequestID
}

type HTTPResponse struct {
	Meta  Meta  `json:"meta"`
	Data  any   `json:"data,omitempty"`
	Error error `json:"error,omitempty"`
}

func NewHTTPResponse(requestID string, data any, err error) *HTTPResponse {
	resp := HTTPResponse{
		Meta: Meta{
			Timestamp: time.Now().String(),
			RequestID: requestID,
		},
		Data:  data,
		Error: err,
	}
	return &resp
}

func (r *HTTPResponse) Payload() (string, *runtime.Error) {
	rpcErr, ok := r.Error.(*runtime.Error)
	if !ok {
		rpcErr = runtime.NewError(r.Error.Error(), StatusInternalError)
	}
	data, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return "", ErrorFailedToMarshalError
	}
	return string(data), rpcErr
}

func NewErrorPayload(requestID string, err error) (string, *runtime.Error) {
	response := HTTPResponse{
		Meta: Meta{
			Timestamp: time.Now().String(),
			RequestID: requestID,
		},
		Error: err,
	}
	return response.Payload()
}

type Meta struct {
	Timestamp string `json:"timestamp"`
	RequestID string `json:"requestid"`
}

type RPCMessage map[string]interface{}

type RequestMeta struct {
	UserID    string `json:"user_id"`
	RequestID string `json:"request_id"`
}

var (
	ErrorUserIDNotFound       = runtime.NewError("user_id not found in context", StatusUnauthenticated)
	ErrorFailedToMarshalError = runtime.NewError("failed to marshal error", StatusInternalError)
	ErrorFailedToUnmarshal    = runtime.NewError("failed to unmarshal request", StatusInvalidArgument)
)

type runtimeRPCHandler func(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, payload string) (string, error)

type restfulRPCHandler func(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, in any) (any, error)

func RESTfulRPCHandlerFactory[T any](rpcFn restfulRPCHandler) runtimeRPCHandler {

	return func(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, payload string) (string, error) {

		request := HTTPRequest[T]{
			Meta: RequestMeta{
				UserID:    ctx.Value(runtime.RUNTIME_CTX_USER_ID).(string),
				RequestID: uuid.Must(uuid.NewV4()).String(),
			},
		}

		if err := json.Unmarshal([]byte(payload), &request.Data); err != nil {
			return NewErrorPayload(request.ID(), ErrorFailedToUnmarshal)
		}

		data, err := rpcFn(ctx, logger, db, nk, request)
		if err != nil {
			return NewErrorPayload(request.ID(), err)
		}

		response := NewHTTPResponse(request.ID(), data, nil)

		return response.Payload()
	}
}

type SetNextMatchRPCRequest struct {
	TargetDiscordID string  `json:"discord_id"`
	TargetUserID    string  `json:"user_id"`
	MatchID         MatchID `json:"match_id"`
}

type SetNextMatchRPCResponse struct {
	UserID     string `json:"user_id"`
	MatchLabel string `json:"match_label"`
}

func SetNextMatchRPC(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, in any) (any, error) {
	message, ok := in.(HTTPRequest[SetNextMatchRPCRequest])
	if !ok {
		return nil, runtime.NewError("Request is not of type SetNextMatchRPCRequest", StatusInternalError)
	}

	request := message.Data

	var err error
	if request.TargetDiscordID != "" {
		request.TargetUserID, err = GetUserIDByDiscordID(ctx, db, request.TargetDiscordID)
		if err != nil {
			return nil, runtime.NewError(fmt.Sprintf("Error getting user ID using Discord ID: %s", err.Error()), StatusInternalError)
		}
	}

	settings, err := LoadMatchmakingSettings(ctx, nk, request.TargetUserID)
	if err != nil {
		return nil, runtime.NewError(fmt.Sprintf("Error loading matchmaking settings: %s", err.Error()), StatusInternalError)
	}

	if request.MatchID.IsNil() {
		// Remove the next match ID
		settings.NextMatchID = MatchID{}
	} else {

		label, err := MatchLabelByID(ctx, nk, request.MatchID)
		if err != nil {
			return nil, runtime.NewError(fmt.Sprintf("Error getting match label: %s", err.Error()), StatusInternalError)
		}

		settings.NextMatchID = label.ID
	}

	if err = StoreMatchmakingSettings(ctx, nk, request.TargetUserID, settings); err != nil {
		return nil, runtime.NewError(fmt.Sprintf("Error saving matchmaking settings: %s", err.Error()), StatusInternalError)
	}

	return &SetNextMatchRPCResponse{
		UserID:     request.TargetUserID,
		MatchLabel: settings.NextMatchID.String(),
	}, nil
}
