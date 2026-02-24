package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"runtime"
	"time"

	nakamaRuntime "github.com/heroiclabs/nakama-common/runtime"
)

type HealthCheckResponse struct {
	Status        string `json:"status"`
	Node          string `json:"node"`
	Version       string `json:"version"`
	StartTime     string `json:"start_time"`
	UptimeSeconds int64  `json:"uptime_seconds"`
	GoVersion     string `json:"go_version"`
	NumCPU        int    `json:"num_cpu"`
	NumGoroutine  int    `json:"num_goroutine"`
	ActiveMatches int    `json:"active_matches"`
}

func HealthCheckRPC(ctx context.Context, logger nakamaRuntime.Logger, db *sql.DB, nk nakamaRuntime.NakamaModule, payload string) (string, error) {
	node, _ := ctx.Value(nakamaRuntime.RUNTIME_CTX_NODE).(string)
	version, _ := ctx.Value(nakamaRuntime.RUNTIME_CTX_VERSION).(string)

	uptime := time.Since(nakamaStartTime)

	matches, err := nk.MatchList(ctx, 10000, true, "", nil, nil, "")
	activeMatches := 0
	if err != nil {
		logger.Warn("healthcheck: failed to list matches: %v", err)
	} else {
		activeMatches = len(matches)
	}

	resp := HealthCheckResponse{
		Status:        "ok",
		Node:          node,
		Version:       version,
		StartTime:     nakamaStartTime.Format(time.RFC3339),
		UptimeSeconds: int64(uptime.Seconds()),
		GoVersion:     runtime.Version(),
		NumCPU:        runtime.NumCPU(),
		NumGoroutine:  runtime.NumGoroutine(),
		ActiveMatches: activeMatches,
	}

	data, err := json.Marshal(resp)
	if err != nil {
		return "", nakamaRuntime.NewError("failed to marshal healthcheck response", StatusInternalError)
	}
	return string(data), nil
}
