package server

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"net/http"

	"github.com/heroiclabs/nakama-common/runtime"
	"go.uber.org/zap"
)

// serviceStatusHTTPPath is the path the game itself requests: echovr.exe builds
// `status/services,news?env=%s&projectid=rad14` (string at 0x1416dfe50, in
// CR15NetGame::RefreshServiceStatus 0x14019b890) on its API connection. The game
// parses the body as the JSON array ServiceStatusRPC returns
// ([{"serviceid","available","message"}], UpdateServiceStatusRequests 0x1401c4c00).
// Without this route that request is a 404 and the main menu shows no server status.
const serviceStatusHTTPPath = "/status/{ids:[a-z,]+}"

// serviceStatusBody makes whatever ServiceStatusRPC returned a body the game can parse. The game reads a JSON
// array of {serviceid, available, message} (UpdateServiceStatusRequests 0x1401c4c00); a stored status that is
// not active makes the RPC return ServiceSettings().serviceStatusMessage, which is plain text (for example
// "12 players in 3 matches"), or nothing. An array is served as is. Anything else becomes one "services"
// element carrying the text as its message (CSymbol64 "services" = 0x25e8860120fc8175, the entry whose
// available flag and message the game shows).
func serviceStatusBody(rpcResult string) []byte {
	trimmed := bytes.TrimSpace([]byte(rpcResult))
	var elements []json.RawMessage
	if len(trimmed) > 0 && trimmed[0] == '[' && json.Unmarshal(trimmed, &elements) == nil {
		return trimmed
	}
	wrapped, err := json.Marshal([]ServiceStatusService{{ServiceID: "services", Available: true, Message: string(trimmed)}})
	if err != nil {
		return []byte(`[{"serviceid":"services","available":true,"message":""}]`)
	}
	return wrapped
}

// NewServiceStatusHTTPHandler serves the game's service-status request from the same
// source as the evr/servicestatus RPC, without an HTTP key: the game cannot send one.
// `fetch` is ServiceStatusRPC in production.
func NewServiceStatusHTTPHandler(logger runtime.Logger, fetch func(ctx context.Context) (string, error)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", http.MethodGet)
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		body, err := fetch(r.Context())
		if err != nil {
			logger.Warn("service status request failed", zap.Error(err))
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(serviceStatusBody(body))
	}
}

// RegisterServiceStatusHTTP registers serviceStatusHTTPPath (GET) on the runtime's HTTP router.
func RegisterServiceStatusHTTP(logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, initializer runtime.Initializer, h *RPCHandler) error {
	fetch := func(ctx context.Context) (string, error) {
		return h.ServiceStatusRPC(ctx, logger, db, nk, "")
	}
	return initializer.RegisterHttp(serviceStatusHTTPPath, NewServiceStatusHTTPHandler(logger, fetch), http.MethodGet)
}
