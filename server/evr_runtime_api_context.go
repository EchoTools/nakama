package server

import (
	"context"

	"github.com/heroiclabs/nakama-common/runtime"
)

//nolint:staticcheck
func NewDeveloperAppContext(ctx context.Context, node, version string, env map[string]string, headers, queryParams map[string][]string, userID, username string, vars map[string]string, clientIP, clientPort, applicationID string) context.Context {
	ctx = context.WithValue(ctx, runtime.RUNTIME_CTX_ENV, env)
	ctx = context.WithValue(ctx, runtime.RUNTIME_CTX_NODE, node)
	ctx = context.WithValue(ctx, runtime.RUNTIME_CTX_VERSION, version)
	ctx = context.WithValue(ctx, DEVAPP_CTX_APP_ID, applicationID)

	if headers != nil {
		ctx = context.WithValue(ctx, runtime.RUNTIME_CTX_HEADERS, headers)
	}
	if queryParams != nil {
		ctx = context.WithValue(ctx, runtime.RUNTIME_CTX_QUERY_PARAMS, queryParams)
	}

	ctx = context.WithValue(ctx, runtime.RUNTIME_CTX_USER_ID, userID)
	ctx = context.WithValue(ctx, runtime.RUNTIME_CTX_USERNAME, username)
	if vars != nil {
		ctx = context.WithValue(ctx, runtime.RUNTIME_CTX_VARS, vars)
	}

	ctx = context.WithValue(ctx, runtime.RUNTIME_CTX_CLIENT_IP, clientIP)
	ctx = context.WithValue(ctx, runtime.RUNTIME_CTX_CLIENT_PORT, clientPort)
	return ctx
}
