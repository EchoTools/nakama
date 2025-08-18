package service

import (
	"context"

	"github.com/heroiclabs/nakama-common/runtime"
)

type RuntimeContext struct {
	Env            map[string]string `json:"env"`
	ExecutionMode  string            `json:"execution_mode"`
	Node           string            `json:"node"`
	Version        string            `json:"version"`
	Headers        map[string]string `json:"headers,omitempty"`
	QueryParams    map[string]string `json:"query_params,omitempty"`
	UserID         string            `json:"user_id,omitempty"`
	Username       string            `json:"username,omitempty"`
	Vars           map[string]string `json:"vars,omitempty"`
	UserSessionExp int64             `json:"user_session_exp,omitempty"`
	SessionID      string            `json:"session_id,omitempty"`
	Lang           string            `json:"lang,omitempty"`
	ClientIP       string            `json:"client_ip,omitempty"`
	ClientPort     int               `json:"client_port,omitempty"`
	MatchID        string            `json:"match_id,omitempty"`
	MatchNode      string            `json:"match_node,omitempty"`
	MatchLabel     string            `json:"match_label,omitempty"`
	MatchTickRate  int               `json:"match_tick_rate,omitempty"`
}

func NewRuntimeContext(ctx context.Context) RuntimeContext {

	rc := RuntimeContext{}

	if env, ok := ctx.Value(runtime.RUNTIME_CTX_ENV).(map[string]string); ok {
		rc.Env = env
	}

	if executionMode, ok := ctx.Value(runtime.RUNTIME_CTX_MODE).(string); ok {
		rc.ExecutionMode = executionMode
	}

	if node, ok := ctx.Value(runtime.RUNTIME_CTX_NODE).(string); ok {
		rc.Node = node
	}

	if version, ok := ctx.Value(runtime.RUNTIME_CTX_VERSION).(string); ok {
		rc.Version = version
	}

	if headers, ok := ctx.Value(runtime.RUNTIME_CTX_HEADERS).(map[string]string); ok {
		rc.Headers = headers
	}

	if queryParams, ok := ctx.Value(runtime.RUNTIME_CTX_QUERY_PARAMS).(map[string]string); ok {
		rc.QueryParams = queryParams
	}

	if userID, ok := ctx.Value(runtime.RUNTIME_CTX_USER_ID).(string); ok {
		rc.UserID = userID
	}

	if username, ok := ctx.Value(runtime.RUNTIME_CTX_USERNAME).(string); ok {
		rc.Username = username
	}

	if vars, ok := ctx.Value(runtime.RUNTIME_CTX_VARS).(map[string]string); ok {
		rc.Vars = vars
	}

	if userSessionExp, ok := ctx.Value(runtime.RUNTIME_CTX_USER_SESSION_EXP).(int64); ok {
		rc.UserSessionExp = userSessionExp
	}

	if sessionID, ok := ctx.Value(runtime.RUNTIME_CTX_SESSION_ID).(string); ok {
		rc.SessionID = sessionID
	}

	if lang, ok := ctx.Value(runtime.RUNTIME_CTX_LANG).(string); ok {
		rc.Lang = lang
	}

	if clientIP, ok := ctx.Value(runtime.RUNTIME_CTX_CLIENT_IP).(string); ok {
		rc.ClientIP = clientIP
	}

	if clientPort, ok := ctx.Value(runtime.RUNTIME_CTX_CLIENT_PORT).(int); ok {
		rc.ClientPort = clientPort
	}

	if matchID, ok := ctx.Value(runtime.RUNTIME_CTX_MATCH_ID).(string); ok {
		rc.MatchID = matchID
	}

	if matchNode, ok := ctx.Value(runtime.RUNTIME_CTX_MATCH_NODE).(string); ok {
		rc.MatchNode = matchNode
	}

	if matchLabel, ok := ctx.Value(runtime.RUNTIME_CTX_MATCH_LABEL).(string); ok {
		rc.MatchLabel = matchLabel
	}

	if matchTickRate, ok := ctx.Value(runtime.RUNTIME_CTX_MATCH_TICK_RATE).(int); ok {
		rc.MatchTickRate = matchTickRate
	}

	return rc
}
